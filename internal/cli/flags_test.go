package cli

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

// exitCode maps a command error to the process exit code main would use.
func exitCode(t *testing.T, err error) int {
	t.Helper()
	if err == nil {
		return ExitOK
	}
	var coded *ExitCodeError
	if !errors.As(err, &coded) {
		t.Fatalf("uncoded error: %v", err)
	}
	return coded.Code
}

// listedModules runs `list --json` and returns the distinct modules reported.
func listedModules(t *testing.T, h *harness, arguments ...string) []string {
	t.Helper()
	if err := h.runWithGlobalFlags(newListCommand(h.opts), append([]string{"--json"}, arguments...)...); err != nil {
		t.Fatal(err)
	}
	var response struct {
		Providers []struct {
			Module string `json:"module"`
		} `json:"providers"`
	}
	if err := json.Unmarshal(h.stdout.Bytes(), &response); err != nil {
		t.Fatalf("invalid JSON: %v\n%s", err, h.stdout.String())
	}
	seen := map[string]bool{}
	var modules []string
	for _, provider := range response.Providers {
		if !seen[provider.Module] {
			seen[provider.Module] = true
			modules = append(modules, provider.Module)
		}
	}
	sort.Strings(modules)
	return modules
}

func TestDirShorthandAcceptsRelativePath(t *testing.T) {
	h := newHarness(t, nil)
	before := h.mainTF(t)
	t.Chdir(filepath.Dir(h.dir))

	root := &cobra.Command{Use: "provlift", SilenceErrors: true, SilenceUsage: true}
	h.opts.bind(root)
	root.AddCommand(newUpgradeCommand(h.opts))
	err := ExecuteContext(context.Background(), root, []string{"-C", filepath.Base(h.dir), "upgrade",
		"--provider", "azurerm", "--to", "5.6.0", "--level", "major", "--allow-major", "--skip-init", "--json"})
	if err != nil {
		t.Fatal(err)
	}
	var response struct {
		ChangedFiles []string `json:"changed_files"`
	}
	if err := json.Unmarshal(h.stdout.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if len(response.ChangedFiles) != 1 || response.ChangedFiles[0] != "main.tf" || h.mainTF(t) == before {
		t.Fatalf("-C with a relative path did not select the repository: %s", h.stdout.String())
	}
}

func TestExcludeFlagIsRepeatableAndTakesWholeGlobs(t *testing.T) {
	modules := map[string]map[string]string{".": moduleFiles("hashicorp/azurerm", "~> 4.65", "")}
	for _, dir := range []string{"modules/a", "modules/b", "modules/c/nested", "legacy"} {
		modules[dir] = moduleFiles("hashicorp/azurerm", "~> 4.65", "")
	}
	for _, scenario := range []struct {
		name      string
		arguments []string
		want      string
	}{
		{"none", nil, ". legacy modules/a modules/b modules/c/nested"},
		{"recursive", []string{"--exclude", "modules/**"}, ". legacy"},
		{"repeated", []string{"--exclude", "modules/a", "--exclude", "legacy"}, ". modules/b modules/c/nested"},
		// A comma inside a glob is part of a brace alternative, not a list
		// separator, so the pattern must reach the matcher intact.
		{"brace alternatives", []string{"--exclude", "modules/{a,b}"}, ". legacy modules/c/nested"},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			h := newDriftHarness(t, modules)
			if got := strings.Join(listedModules(t, h, scenario.arguments...), " "); got != scenario.want {
				t.Fatalf("modules = %q, want %q", got, scenario.want)
			}
		})
	}
}

func TestCheckModuleAndLevelFlags(t *testing.T) {
	newRepo := func(t *testing.T) *harness {
		return newHarness(t, map[string]string{
			"main.tf":       fixtureMainTF + "\nmodule \"child\" {\n  source = \"./child\"\n}\n",
			"child/main.tf": moduleFiles("hashicorp/azurerm", "~> 4.65", "")["main.tf"],
		})
	}
	type response struct {
		Providers []struct {
			Module string `json:"module"`
		} `json:"providers"`
		UpgradesAvailable int `json:"upgrades_available"`
	}

	// github has a minor upgrade and azurerm a major one, so each level
	// budget counts a different number and decides the exit code.
	for _, scenario := range []struct {
		level     string
		wantCount int
		wantCode  int
	}{
		{"patch", 0, ExitOK},
		{"minor", 1, ExitUpgradesAvailable},
		{"major", 3, ExitUpgradesAvailable},
	} {
		t.Run("level "+scenario.level, func(t *testing.T) {
			h := newRepo(t)
			err := h.runWithGlobalFlags(newCheckCommand(h.opts), "--json", "--exit-code", "--level", scenario.level)
			if code := exitCode(t, err); code != scenario.wantCode {
				t.Fatalf("exit code = %d, want %d", code, scenario.wantCode)
			}
			var got response
			if err := json.Unmarshal(h.stdout.Bytes(), &got); err != nil {
				t.Fatal(err)
			}
			if got.UpgradesAvailable != scenario.wantCount {
				t.Fatalf("upgrades_available = %d, want %d", got.UpgradesAvailable, scenario.wantCount)
			}
		})
	}

	t.Run("module", func(t *testing.T) {
		h := newRepo(t)
		if err := h.runWithGlobalFlags(newCheckCommand(h.opts), "--json", "--module", "child"); err != nil {
			t.Fatal(err)
		}
		var got response
		if err := json.Unmarshal(h.stdout.Bytes(), &got); err != nil {
			t.Fatal(err)
		}
		if len(got.Providers) != 1 || got.Providers[0].Module != "child" {
			t.Fatalf("--module did not restrict the check: %s", h.stdout.String())
		}
	})
}

func TestUpgradeExactLevelFlag(t *testing.T) {
	for _, scenario := range []struct {
		exact bool
		want  string
	}{
		{false, "3.117.1"},
		{true, "4.0.0"},
	} {
		h := multiMajorRepo(t)
		arguments := []string{"--level", "major", "--allow-major", "--dry-run", "--json"}
		if scenario.exact {
			arguments = append(arguments, "--exact-level")
		}
		if err := h.runWithGlobalFlags(newUpgradeCommand(h.opts), arguments...); err != nil {
			t.Fatal(err)
		}
		var response struct {
			Plan struct {
				Actions []struct {
					To string `json:"to"`
				} `json:"actions"`
			} `json:"plan"`
		}
		if err := json.Unmarshal(h.stdout.Bytes(), &response); err != nil {
			t.Fatal(err)
		}
		if len(response.Plan.Actions) != 1 || response.Plan.Actions[0].To != scenario.want {
			t.Fatalf("--exact-level=%t selected %s, want %s", scenario.exact, h.stdout.String(), scenario.want)
		}
	}
}

func TestUpgradeExitCodesSeparateErrorsFromValidation(t *testing.T) {
	for _, scenario := range []struct {
		name     string
		setup    func(t *testing.T, h *harness)
		wantCode int
	}{
		{"terraform missing", func(t *testing.T, h *harness) {
			h.opts.tfBinary = filepath.Join(h.dir, "missing-terraform")
		}, ExitError},
		{"unplanned provider change refused", func(t *testing.T, h *harness) {
			useFakeTerraform(t, h)
			t.Setenv(fakeUnexpectedProvider, "1")
		}, ExitError},
		{"terraform timed out", func(t *testing.T, h *harness) {
			useFakeTerraform(t, h)
			h.opts.timeout = 1
		}, ExitError},
		{"init failed", func(t *testing.T, h *harness) {
			useFakeTerraform(t, h)
			t.Setenv(fakeTerraformFail, "init")
		}, ExitValidationFailed},
		{"validate failed", func(t *testing.T, h *harness) {
			useFakeTerraform(t, h)
			t.Setenv(fakeTerraformFail, "validate")
		}, ExitValidationFailed},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			h := newHarness(t, nil)
			scenario.setup(t, h)
			err := h.run(t, newUpgradeCommand(h.opts), "--provider", "azurerm", "--to", "5.6.0", "--level", "major", "--allow-major")
			if code := exitCode(t, err); code != scenario.wantCode {
				t.Fatalf("exit code = %d, want %d: %v", code, scenario.wantCode, err)
			}
		})
	}
}

func TestUpgradeMissingTerraformLeavesFilesUntouched(t *testing.T) {
	h := newHarness(t, nil)
	h.opts.tfBinary = filepath.Join(h.dir, "missing-terraform")
	before := snapshotTree(t, h.dir)

	err := h.run(t, newUpgradeCommand(h.opts), "--provider", "azurerm", "--to", "5.6.0", "--level", "major", "--allow-major")
	if code := exitCode(t, err); code != ExitError {
		t.Fatalf("exit code = %d, want %d: %v", code, ExitError, err)
	}
	after := snapshotTree(t, h.dir)
	if len(after) != len(before) {
		t.Fatalf("files changed: before %v, after %v", keys(before), keys(after))
	}
	for path, content := range before {
		if after[path] != content {
			t.Errorf("%s was modified although Terraform could not run", path)
		}
	}
}

func snapshotTree(t *testing.T, root string) map[string]string {
	t.Helper()
	out := map[string]string{}
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		b, err := os.ReadFile(path)
		out[path] = string(b)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func keys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func TestUpgradeReportsScanDiagnostics(t *testing.T) {
	h := newHarness(t, map[string]string{"versions.tf.json": `{"terraform": {}}`})
	if err := h.runWithGlobalFlags(newUpgradeCommand(h.opts), "--provider", "azurerm", "--level", "minor", "--dry-run", "--json"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(h.stderr.String(), "versions.tf.json") || !strings.Contains(h.stderr.String(), "JSON configuration is not supported") {
		t.Errorf("upgrade skipped .tf.json without saying so: %s", h.stderr.String())
	}
	if !json.Valid(h.stdout.Bytes()) {
		t.Errorf("diagnostics corrupted JSON output: %s", h.stdout.String())
	}
}

func TestValidateFlagsHaveObservableEffects(t *testing.T) {
	newRepo := func(t *testing.T) (*harness, string) {
		h := newHarness(t, map[string]string{
			"prod/main.tf": moduleFiles("hashicorp/azurerm", "~> 4.65", "")["main.tf"],
		})
		return h, useFakeTerraform(t, h)
	}

	t.Run("module", func(t *testing.T) {
		h, logPath := newRepo(t)
		if err := h.run(t, newValidateCommand(h.opts), "--module", "prod"); err != nil {
			t.Fatal(err)
		}
		calls := readInvocations(t, logPath)
		for _, call := range calls {
			if !strings.HasPrefix(call, "prod\t") {
				t.Errorf("--module ran Terraform outside the selected module: %q", call)
			}
		}
		if len(calls) == 0 {
			t.Fatal("selected module was not validated")
		}
	})

	t.Run("backend", func(t *testing.T) {
		h, logPath := newRepo(t)
		if err := h.run(t, newValidateCommand(h.opts), "--backend"); err != nil {
			t.Fatal(err)
		}
		if indexOfCall(readInvocations(t, logPath), "-backend=true") < 0 {
			t.Errorf("--backend was not passed through: %v", readInvocations(t, logPath))
		}
	})

	t.Run("json", func(t *testing.T) {
		h, _ := newRepo(t)
		h.opts.jsonOut = true
		if err := h.run(t, newValidateCommand(h.opts)); err != nil {
			t.Fatal(err)
		}
		var response struct {
			Runs []struct {
				Module  string `json:"module"`
				Command string `json:"command"`
			} `json:"runs"`
			Failures []string `json:"failures"`
		}
		if err := json.Unmarshal(h.stdout.Bytes(), &response); err != nil || len(response.Runs) != 6 {
			t.Fatalf("validate --json = %v: %s", err, h.stdout.String())
		}
	})

	t.Run("json without root modules", func(t *testing.T) {
		h, _ := newRepo(t)
		h.opts.jsonOut = true
		if err := h.run(t, newValidateCommand(h.opts), "--module", "absent"); err != nil {
			t.Fatal(err)
		}
		if !json.Valid(h.stdout.Bytes()) {
			t.Fatalf("validate --json printed text: %s", h.stdout.String())
		}
	})
}

func TestVersionCommandJSON(t *testing.T) {
	h := newHarness(t, nil)
	if err := h.runWithGlobalFlags(newVersionCommand(h.opts, BuildInfo{Version: "1.2.3"}), "--json"); err != nil {
		t.Fatal(err)
	}
	var info map[string]string
	if err := json.Unmarshal(h.stdout.Bytes(), &info); err != nil || info["version"] != "1.2.3" {
		t.Fatalf("version --json = %v: %s", err, h.stdout.String())
	}
}

// TestDefaultCommandsNeedNoBackendOrPlan pins down that cloud credentials are
// only ever needed on request: the offline commands never start Terraform, and
// the commands that do start it neither plan nor initialise a backend.
func TestDefaultCommandsNeedNoBackendOrPlan(t *testing.T) {
	for _, scenario := range []struct {
		name      string
		command   func(*globalOptions) *cobra.Command
		arguments []string
		wantCalls bool
	}{
		{"list", newListCommand, nil, false},
		{"check", newCheckCommand, nil, false},
		{"drift", newDriftCommand, nil, false},
		{"upgrade", newUpgradeCommand, []string{"--provider", "azurerm", "--level", "major", "--allow-major"}, true},
		{"validate", newValidateCommand, nil, true},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			h := newHarness(t, nil)
			logPath := useFakeTerraform(t, h)
			if err := h.run(t, scenario.command(h.opts), scenario.arguments...); err != nil {
				t.Fatal(err)
			}
			calls := readInvocations(t, logPath)
			if got := len(calls) > 0; got != scenario.wantCalls {
				t.Fatalf("Terraform invoked = %t, want %t: %v", got, scenario.wantCalls, calls)
			}
			for _, call := range calls {
				command := strings.SplitN(call, "\t", 2)[1]
				if strings.HasPrefix(command, "plan") {
					t.Errorf("plan ran without --plan: %q", call)
				}
				if strings.HasPrefix(command, "init") && !strings.Contains(command, "-backend=false") {
					t.Errorf("init may reach a backend without --backend: %q", call)
				}
			}
		})
	}
}
