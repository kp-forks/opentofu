// Copyright (c) The OpenTofu Authors
// SPDX-License-Identifier: MPL-2.0
// Copyright (c) 2023 HashiCorp, Inc.
// SPDX-License-Identifier: MPL-2.0

package command

import (
	"encoding/json"
	"io"
	"os"
	"path"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/mitchellh/cli"
	"github.com/zclconf/go-cty/cty"

	testing_command "github.com/opentofu/opentofu/internal/command/testing"
	"github.com/opentofu/opentofu/internal/command/views"
	"github.com/opentofu/opentofu/internal/configs/configschema"
	"github.com/opentofu/opentofu/internal/providers"
	"github.com/opentofu/opentofu/internal/terminal"
)

func setupTest(t *testing.T, fixturepath string, args ...string) (*terminal.TestOutput, int) {
	view, done := testView(t)
	p := testProvider()
	p.GetProviderSchemaResponse = &providers.GetProviderSchemaResponse{
		ResourceTypes: map[string]providers.Schema{
			"test_instance": {
				Block: &configschema.Block{
					Attributes: map[string]*configschema.Attribute{
						"ami": {Type: cty.String, Optional: true},
					},
					BlockTypes: map[string]*configschema.NestedBlock{
						"network_interface": {
							Nesting: configschema.NestingList,
							Block: configschema.Block{
								Attributes: map[string]*configschema.Attribute{
									"device_index": {Type: cty.String, Optional: true},
									"description":  {Type: cty.String, Optional: true},
									"name":         {Type: cty.String, Optional: true},
								},
							},
						},
					},
				},
			},
		},
	}
	c := &ValidateCommand{
		Meta: Meta{
			testingOverrides: metaOverridesForProvider(p),
			View:             view,
		},
	}

	args = append(args, "-no-color")
	args = append(args, testFixturePath(fixturepath))

	code := c.Run(args)
	return done(t), code
}

func TestValidateCommand(t *testing.T) {
	if output, code := setupTest(t, "validate-valid"); code != 0 {
		t.Fatalf("unexpected non-successful exit code %d\n\n%s", code, output.Stderr())
	}
}

func TestValidateCommandWithTfvarsFile(t *testing.T) {
	// Create a temporary working directory that is empty because this test
	// requires scanning the current working directory by validate command.
	td := t.TempDir()
	testCopyDir(t, testFixturePath("validate-valid/with-tfvars-file"), td)
	t.Chdir(td)

	view, done := testView(t)
	c := &ValidateCommand{
		Meta: Meta{
			testingOverrides: metaOverridesForProvider(testProvider()),
			View:             view,
		},
	}

	args := []string{}
	code := c.Run(args)
	output := done(t)
	if code != 0 {
		t.Fatalf("bad %d\n\n%s", code, output.Stderr())
	}
}

func TestValidateFailingCommand(t *testing.T) {
	if output, code := setupTest(t, "validate-invalid"); code != 1 {
		t.Fatalf("Should have failed: %d\n\n%s", code, output.Stderr())
	}
}

func TestValidateFailingCommandMissingQuote(t *testing.T) {
	output, code := setupTest(t, "validate-invalid/missing_quote")

	if code != 1 {
		t.Fatalf("Should have failed: %d\n\n%s", code, output.Stderr())
	}
	wantError := "Error: Invalid reference"
	if !strings.Contains(output.Stderr(), wantError) {
		t.Fatalf("Missing error string %q\n\n'%s'", wantError, output.Stderr())
	}
}

func TestValidateFailingCommandMissingVariable(t *testing.T) {
	output, code := setupTest(t, "validate-invalid/missing_var")
	if code != 1 {
		t.Fatalf("Should have failed: %d\n\n%s", code, output.Stderr())
	}
	wantError := "Error: Reference to undeclared input variable"
	if !strings.Contains(output.Stderr(), wantError) {
		t.Fatalf("Missing error string %q\n\n'%s'", wantError, output.Stderr())
	}
}

func TestSameProviderMultipleTimesShouldFail(t *testing.T) {
	output, code := setupTest(t, "validate-invalid/multiple_providers")
	if code != 1 {
		t.Fatalf("Should have failed: %d\n\n%s", code, output.Stderr())
	}
	wantError := "Error: Duplicate provider configuration"
	if !strings.Contains(output.Stderr(), wantError) {
		t.Fatalf("Missing error string %q\n\n'%s'", wantError, output.Stderr())
	}
}

func TestSameModuleMultipleTimesShouldFail(t *testing.T) {
	output, code := setupTest(t, "validate-invalid/multiple_modules")
	if code != 1 {
		t.Fatalf("Should have failed: %d\n\n%s", code, output.Stderr())
	}
	wantError := "Error: Duplicate module call"
	if !strings.Contains(output.Stderr(), wantError) {
		t.Fatalf("Missing error string %q\n\n'%s'", wantError, output.Stderr())
	}
}

func TestSameResourceMultipleTimesShouldFail(t *testing.T) {
	output, code := setupTest(t, "validate-invalid/multiple_resources")
	if code != 1 {
		t.Fatalf("Should have failed: %d\n\n%s", code, output.Stderr())
	}
	wantError := `Error: Duplicate resource "aws_instance" configuration`
	if !strings.Contains(output.Stderr(), wantError) {
		t.Fatalf("Missing error string %q\n\n'%s'", wantError, output.Stderr())
	}
}

func TestSameImportTargetMultipleTimesShouldFail(t *testing.T) {
	output, code := setupTest(t, "validate-invalid/duplicate_import_targets")
	if code != 1 {
		t.Fatalf("Should have failed: %d\n\n%s", code, output.Stderr())
	}
	wantError := `Error: Duplicate import configuration for "aws_instance.web"`
	if !strings.Contains(output.Stderr(), wantError) {
		t.Fatalf("Missing error string %q\n\n'%s'", wantError, output.Stderr())
	}
}

func TestUndefinedVariableAsImportIDShouldFail(t *testing.T) {
	output, code := setupTest(t, "validate-invalid/import_undefined_var")
	if code != 1 {
		t.Fatalf("Should have failed: %d\n\n%s", code, output.Stderr())
	}
	wantError := `Error: Reference to undeclared input variable`
	if !strings.Contains(output.Stderr(), wantError) {
		t.Fatalf("Missing error string %q\n\n'%s'", wantError, output.Stderr())
	}
}

func TestUndefinedResourceAsImportTargetShouldSucceed(t *testing.T) {
	// -generate-config-out is the reason we can have undefined resources as targets
	output, code := setupTest(t, "validate-valid/import_undefined_resource")
	if code != 0 {
		t.Fatalf("Should have succeeded: %d\n\n%s", code, output.Stderr())
	}
}

func TestDefinedVarAsImportIDShouldSucceed(t *testing.T) {
	output, code := setupTest(t, "validate-valid/import_id_defined_var")
	if code != 0 {
		t.Fatalf("Should have succeeded: %d\n\n%s", code, output.Stderr())
	}
}

func TestOutputWithoutValueShouldFail(t *testing.T) {
	output, code := setupTest(t, "validate-invalid/outputs")
	if code != 1 {
		t.Fatalf("Should have failed: %d\n\n%s", code, output.Stderr())
	}
	wantError := `The argument "value" is required, but no definition was found.`
	if !strings.Contains(output.Stderr(), wantError) {
		t.Fatalf("Missing error string %q\n\n'%s'", wantError, output.Stderr())
	}
	wantError = `An argument named "values" is not expected here. Did you mean "value"?`
	if !strings.Contains(output.Stderr(), wantError) {
		t.Fatalf("Missing error string %q\n\n'%s'", wantError, output.Stderr())
	}
}

func TestModuleWithIncorrectNameShouldFail(t *testing.T) {
	output, code := setupTest(t, "validate-invalid/incorrectmodulename")
	if code != 1 {
		t.Fatalf("Should have failed: %d\n\n%s", code, output.Stderr())
	}

	wantError := `Error: Invalid module instance name`
	if !strings.Contains(output.Stderr(), wantError) {
		t.Fatalf("Missing error string %q\n\n'%s'", wantError, output.Stderr())
	}
}

func TestWronglyUsedInterpolationShouldFail(t *testing.T) {
	output, code := setupTest(t, "validate-invalid/interpolation")
	if code != 1 {
		t.Fatalf("Should have failed: %d\n\n%s", code, output.Stderr())
	}

	wantError := `Error: Variables not allowed`
	if !strings.Contains(output.Stderr(), wantError) {
		t.Fatalf("Missing error string %q\n\n'%s'", wantError, output.Stderr())
	}
	wantError = `A single static variable reference is required`
	if !strings.Contains(output.Stderr(), wantError) {
		t.Fatalf("Missing error string %q\n\n'%s'", wantError, output.Stderr())
	}
}

func TestMissingDefinedVar(t *testing.T) {
	output, code := setupTest(t, "validate-invalid/missing_defined_var")
	// This is allowed because validate tests only that variables are referenced
	// correctly, not that they all have defined values.
	if code != 0 {
		t.Fatalf("Should have passed: %d\n\n%s", code, output.Stderr())
	}
}

func TestValidateWithInvalidTestFile(t *testing.T) {

	// We're reusing some testing configs that were written for testing the
	// test command here, so we have to initialise things slightly differently
	// to the other tests.

	view, done := testView(t)
	provider := testing_command.NewProvider(nil)
	c := &ValidateCommand{
		Meta: Meta{
			testingOverrides: metaOverridesForProvider(provider.Provider),
			View:             view,
		},
	}

	var args []string
	args = append(args, "-no-color")
	args = append(args, testFixturePath("test/invalid"))

	code := c.Run(args)
	output := done(t)

	if code != 1 {
		t.Fatalf("Should have failed: %d\n\n%s", code, output.Stderr())
	}

	wantError := "Error: Invalid `expect_failures` reference"
	if !strings.Contains(output.Stderr(), wantError) {
		t.Fatalf("Missing error string %q\n\n'%s'", wantError, output.Stderr())
	}
}

func TestValidateWithInvalidTestModule(t *testing.T) {

	// We're reusing some testing configs that were written for testing the
	// test command here, so we have to initialise things slightly differently
	// to the other tests.

	td := t.TempDir()
	testCopyDir(t, testFixturePath(path.Join("test", "invalid-module")), td)
	t.Chdir(td)

	streams, done := terminal.StreamsForTesting(t)
	view := views.NewView(streams)
	ui := new(cli.MockUi)

	provider := testing_command.NewProvider(nil)

	providerSource, close := newMockProviderSource(t, map[string][]string{
		"test": {"1.0.0"},
	})
	defer close()

	meta := Meta{
		testingOverrides: metaOverridesForProvider(provider.Provider),
		Ui:               ui,
		View:             view,
		Streams:          streams,
		ProviderSource:   providerSource,
	}

	init := &InitCommand{
		Meta: meta,
	}

	if code := init.Run(nil); code != 0 {
		t.Fatalf("expected status code 0 but got %d: %s", code, ui.ErrorWriter)
	}

	c := &ValidateCommand{
		Meta: meta,
	}

	var args []string
	args = append(args, "-no-color")

	code := c.Run(args)
	output := done(t)

	if code != 1 {
		t.Fatalf("Should have failed: %d\n\n%s", code, output.Stderr())
	}

	wantError := "Error: Reference to undeclared input variable"
	if !strings.Contains(output.Stderr(), wantError) {
		t.Fatalf("Missing error string %q\n\n'%s'", wantError, output.Stderr())
	}
}

func TestValidate_json(t *testing.T) {
	tests := []struct {
		path  string
		valid bool
	}{
		{"validate-valid", true},
		{"validate-invalid", false},
		{"validate-invalid/missing_quote", false},
		{"validate-invalid/missing_var", false},
		{"validate-invalid/multiple_providers", false},
		{"validate-invalid/multiple_modules", false},
		{"validate-invalid/multiple_resources", false},
		{"validate-invalid/duplicate_import_targets", false},
		{"validate-invalid/outputs", false},
		{"validate-invalid/incorrectmodulename", false},
		{"validate-invalid/interpolation", false},
		{"validate-invalid/missing_defined_var", true},
	}

	for _, tc := range tests {
		t.Run(tc.path, func(t *testing.T) {
			var want, got map[string]interface{}

			wantFile, err := os.Open(path.Join(testFixturePath(tc.path), "output.json"))
			if err != nil {
				t.Fatalf("failed to open output file: %s", err)
			}
			defer wantFile.Close()
			wantBytes, err := io.ReadAll(wantFile)
			if err != nil {
				t.Fatalf("failed to read output file: %s", err)
			}
			err = json.Unmarshal([]byte(wantBytes), &want)
			if err != nil {
				t.Fatalf("failed to unmarshal expected JSON: %s", err)
			}

			output, code := setupTest(t, tc.path, "-json")

			gotString := output.Stdout()
			err = json.Unmarshal([]byte(gotString), &got)
			if err != nil {
				t.Fatalf("failed to unmarshal actual JSON: %s", err)
			}

			if !cmp.Equal(got, want) {
				t.Errorf("wrong output:\n %v\n", cmp.Diff(got, want))
				t.Errorf("raw output:\n%s\n", gotString)
			}

			if tc.valid && code != 0 {
				t.Errorf("wrong exit code: want 0, got %d", code)
			} else if !tc.valid && code != 1 {
				t.Errorf("wrong exit code: want 1, got %d", code)
			}

			if errorOutput := output.Stderr(); errorOutput != "" {
				t.Errorf("unexpected error output:\n%s", errorOutput)
			}
		})
	}
}
