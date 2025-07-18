// Copyright (c) The OpenTofu Authors
// SPDX-License-Identifier: MPL-2.0
// Copyright (c) 2023 HashiCorp, Inc.
// SPDX-License-Identifier: MPL-2.0

package remote

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path"
	"testing"
	"time"

	tfe "github.com/hashicorp/go-tfe"
	"github.com/mitchellh/cli"
	"github.com/opentofu/svchost"
	"github.com/opentofu/svchost/disco"
	"github.com/opentofu/svchost/svcauth"
	"github.com/zclconf/go-cty/cty"

	"github.com/opentofu/opentofu/internal/backend"
	backendLocal "github.com/opentofu/opentofu/internal/backend/local"
	"github.com/opentofu/opentofu/internal/cloud"
	"github.com/opentofu/opentofu/internal/configs"
	"github.com/opentofu/opentofu/internal/configs/configschema"
	"github.com/opentofu/opentofu/internal/encryption"
	"github.com/opentofu/opentofu/internal/providers"
	"github.com/opentofu/opentofu/internal/states/remote"
	"github.com/opentofu/opentofu/internal/tfdiags"
	"github.com/opentofu/opentofu/internal/tofu"
)

const (
	testCred = "test-auth-token"
)

var (
	mockedBackendHost = "app.example.com"
	credsSrc          = svcauth.StaticCredentialsSource(map[svchost.Hostname]svcauth.HostCredentials{
		svchost.Hostname(mockedBackendHost): svcauth.HostCredentialsToken(testCred),
	})
)

// mockInput is a mock implementation of tofu.UIInput.
type mockInput struct {
	answers map[string]string
}

func (m *mockInput) Input(ctx context.Context, opts *tofu.InputOpts) (string, error) {
	v, ok := m.answers[opts.Id]
	if !ok {
		return "", fmt.Errorf("unexpected input request in test: %s", opts.Id)
	}
	if v == "wait-for-external-update" {
		select {
		case <-ctx.Done():
		case <-time.After(time.Minute):
		}
	}
	delete(m.answers, opts.Id)
	return v, nil
}

func testInput(t *testing.T, answers map[string]string) *mockInput {
	t.Helper()
	return &mockInput{answers: answers}
}

func testBackendDefault(t *testing.T) (*Remote, func()) {
	t.Helper()
	obj := cty.ObjectVal(map[string]cty.Value{
		"hostname":     cty.StringVal(mockedBackendHost),
		"organization": cty.StringVal("hashicorp"),
		"token":        cty.NullVal(cty.String),
		"workspaces": cty.ObjectVal(map[string]cty.Value{
			"name":   cty.StringVal("prod"),
			"prefix": cty.NullVal(cty.String),
		}),
	})
	return testBackend(t, obj)
}

func testBackendNoDefault(t *testing.T) (*Remote, func()) {
	obj := cty.ObjectVal(map[string]cty.Value{
		"hostname":     cty.StringVal(mockedBackendHost),
		"organization": cty.StringVal("hashicorp"),
		"token":        cty.NullVal(cty.String),
		"workspaces": cty.ObjectVal(map[string]cty.Value{
			"name":   cty.NullVal(cty.String),
			"prefix": cty.StringVal("my-app-"),
		}),
	})
	return testBackend(t, obj)
}

func testBackendNoOperations(t *testing.T) (*Remote, func()) {
	t.Helper()
	obj := cty.ObjectVal(map[string]cty.Value{
		"hostname":     cty.StringVal(mockedBackendHost),
		"organization": cty.StringVal("no-operations"),
		"token":        cty.NullVal(cty.String),
		"workspaces": cty.ObjectVal(map[string]cty.Value{
			"name":   cty.StringVal("prod"),
			"prefix": cty.NullVal(cty.String),
		}),
	})
	return testBackend(t, obj)
}

func testRemoteClient(t *testing.T) remote.Client {
	t.Helper()
	b, bCleanup := testBackendDefault(t)
	defer bCleanup()

	raw, err := b.StateMgr(t.Context(), backend.DefaultStateName)
	if err != nil {
		t.Fatalf("error: %v", err)
	}

	return raw.(*remote.State).Client
}

func testBackend(t *testing.T, obj cty.Value) (*Remote, func()) {
	t.Helper()

	s := testServer(t)
	b := New(testDisco(s), encryption.StateEncryptionDisabled())

	// Configure the backend so the client is created.
	newObj, valDiags := b.PrepareConfig(obj)
	if len(valDiags) != 0 {
		t.Fatal(valDiags.ErrWithWarnings())
	}
	obj = newObj

	confDiags := b.Configure(t.Context(), obj)
	if len(confDiags) != 0 {
		t.Fatal(confDiags.ErrWithWarnings())
	}

	// Get a new mock client.
	mc := cloud.NewMockClient()

	// Replace the services we use with our mock services.
	b.CLI = cli.NewMockUi()
	b.client.Applies = mc.Applies
	b.client.ConfigurationVersions = mc.ConfigurationVersions
	b.client.CostEstimates = mc.CostEstimates
	b.client.Organizations = mc.Organizations
	b.client.Plans = mc.Plans
	b.client.PolicyChecks = mc.PolicyChecks
	b.client.Runs = mc.Runs
	b.client.RunEvents = mc.RunEvents
	b.client.StateVersions = mc.StateVersions
	b.client.Variables = mc.Variables
	b.client.Workspaces = mc.Workspaces

	// Set local to a local test backend.
	b.local = testLocalBackend(t, b)

	ctx := context.Background()

	// Create the organization.
	_, err := b.client.Organizations.Create(ctx, tfe.OrganizationCreateOptions{
		Name: tfe.String(b.organization),
	})
	if err != nil {
		t.Fatalf("error: %v", err)
	}

	// Create the default workspace if required.
	if b.workspace != "" {
		_, err = b.client.Workspaces.Create(ctx, b.organization, tfe.WorkspaceCreateOptions{
			Name: tfe.String(b.workspace),
		})
		if err != nil {
			t.Fatalf("error: %v", err)
		}
	}

	return b, s.Close
}

func testLocalBackend(t *testing.T, remote *Remote) backend.Enhanced {
	t.Helper()
	b := backendLocal.NewWithBackend(remote, nil)

	// Add a test provider to the local backend.
	p := backendLocal.TestLocalProvider(t, b, "null", providers.ProviderSchema{
		ResourceTypes: map[string]providers.Schema{
			"null_resource": {
				Block: &configschema.Block{
					Attributes: map[string]*configschema.Attribute{
						"id": {Type: cty.String, Computed: true},
					},
				},
			},
		},
	})
	p.ApplyResourceChangeResponse = &providers.ApplyResourceChangeResponse{NewState: cty.ObjectVal(map[string]cty.Value{
		"id": cty.StringVal("yes"),
	})}

	return b
}

// testServer returns a *httptest.Server used for local testing.
func testServer(t *testing.T) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()

	// Respond to service discovery calls.
	mux.HandleFunc("/well-known/terraform.json", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, err := io.WriteString(w, `{
  "state.v2": "/api/v2/",
  "tfe.v2.1": "/api/v2/",
  "versions.v1": "/v1/versions/"
}`)
		if err != nil {
			w.WriteHeader(500)
		}
	})

	// Respond to service version constraints calls.
	mux.HandleFunc("/v1/versions/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, err := io.WriteString(w, fmt.Sprintf(`{
  "service": "%s",
  "product": "terraform",
  "minimum": "0.1.0",
  "maximum": "10.0.0"
}`, path.Base(r.URL.Path)))
		if err != nil {
			w.WriteHeader(500)
		}
	})

	// Respond to pings to get the API version header.
	mux.HandleFunc("/api/v2/ping", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("TFP-API-Version", "2.4")
	})

	// Respond to the initial query to read the hashicorp org entitlements.
	mux.HandleFunc("/api/v2/organizations/hashicorp/entitlement-set", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/vnd.api+json")
		_, err := io.WriteString(w, `{
  "data": {
    "id": "org-GExadygjSbKP8hsY",
    "type": "entitlement-sets",
    "attributes": {
      "operations": true,
      "private-module-registry": true,
      "sentinel": true,
      "state-storage": true,
      "teams": true,
      "vcs-integrations": true
    }
  }
}`)
		if err != nil {
			w.WriteHeader(500)
		}
	})

	// Respond to the initial query to read the no-operations org entitlements.
	mux.HandleFunc("/api/v2/organizations/no-operations/entitlement-set", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/vnd.api+json")
		_, err := io.WriteString(w, `{
  "data": {
    "id": "org-ufxa3y8jSbKP8hsT",
    "type": "entitlement-sets",
    "attributes": {
      "operations": false,
      "private-module-registry": true,
      "sentinel": true,
      "state-storage": true,
      "teams": true,
      "vcs-integrations": true
    }
  }
}`)
		if err != nil {
			w.WriteHeader(500)
		}
	})

	// All tests that are assumed to pass will use the hashicorp organization,
	// so for all other organization requests we will return a 404.
	mux.HandleFunc("/api/v2/organizations/", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(404)
		_, err := io.WriteString(w, `{
  "errors": [
    {
      "status": "404",
      "title": "not found"
    }
  ]
}`)
		if err != nil {
			w.WriteHeader(500)
		}
	})

	return httptest.NewServer(mux)
}

// testDisco returns a *disco.Disco mapping to mockedBackendHost and
// localhost to a local test server.
func testDisco(s *httptest.Server) *disco.Disco {
	services := map[string]interface{}{
		"state.v2":    fmt.Sprintf("%s/api/v2/", s.URL),
		"tfe.v2.1":    fmt.Sprintf("%s/api/v2/", s.URL),
		"versions.v1": fmt.Sprintf("%s/v1/versions/", s.URL),
	}
	d := disco.New(
		disco.WithCredentials(credsSrc),
		disco.WithHTTPClient(s.Client()),
	)

	d.ForceHostServices(svchost.Hostname(mockedBackendHost), services)
	d.ForceHostServices(svchost.Hostname("localhost"), services)
	return d
}

type unparsedVariableValue struct {
	value  string
	source tofu.ValueSourceType
}

func (v *unparsedVariableValue) ParseVariableValue(mode configs.VariableParsingMode) (*tofu.InputValue, tfdiags.Diagnostics) {
	return &tofu.InputValue{
		Value:      cty.StringVal(v.value),
		SourceType: v.source,
	}, tfdiags.Diagnostics{}
}

// testVariable returns a backend.UnparsedVariableValue used for testing.
func testVariables(s tofu.ValueSourceType, vs ...string) map[string]backend.UnparsedVariableValue {
	vars := make(map[string]backend.UnparsedVariableValue, len(vs))
	for _, v := range vs {
		vars[v] = &unparsedVariableValue{
			value:  v,
			source: s,
		}
	}
	return vars
}
