package cli

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

// coreRepo has one module whose .terraform-version satisfies required_version
// and one whose .terraform-version does not.
func coreRepo() map[string]map[string]string {
	ok := moduleFiles("hashicorp/aws", "~> 5.0", "5.40.0")
	ok["versions.tf"] = "terraform {\n  required_version = \">= 1.5.0\"\n}\n"
	ok[".terraform-version"] = "1.5.7\n"

	bad := moduleFiles("hashicorp/aws", "~> 5.0", "5.40.0")
	bad["versions.tf"] = "terraform {\n  required_version = \">= 1.6.0\"\n}\n"
	bad[".terraform-version"] = "1.5.7\n"
	bad[".opentofu-version"] = "1.8.2\n"

	return map[string]map[string]string{"envs/dev": ok, "envs/prod": bad}
}

func TestListCoreShowsRequiredVersionAndVersionFile(t *testing.T) {
	h := newDriftHarness(t, coreRepo())

	if err := h.run(t, newListCommand(h.opts), "--core"); err != nil {
		t.Fatalf("list --core: %v", err)
	}
	out := h.stdout.String()
	for _, want := range []string{
		"REQUIRED_VERSION", "VERSION FILE",
		">= 1.5.0", "1.5.7 (envs/dev/.terraform-version)",
		">= 1.6.0", "1.5.7 (envs/prod/.terraform-version)",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "hashicorp/aws") {
		t.Errorf("--core must replace the provider table:\n%s", out)
	}
}

func TestListCoreFollowsConfiguredBinary(t *testing.T) {
	t.Setenv("PROVLIFT_TERRAFORM_BIN", "/usr/local/bin/tofu")
	h := newDriftHarness(t, coreRepo())

	if err := h.run(t, newListCommand(h.opts), "--core"); err != nil {
		t.Fatalf("list --core: %v", err)
	}
	if out := h.stdout.String(); !strings.Contains(out, "1.8.2 (envs/prod/.opentofu-version)") {
		t.Errorf("with tofu configured, .opentofu-version should win:\n%s", out)
	}
}

func TestListJSONIncludesModules(t *testing.T) {
	h := newDriftHarness(t, coreRepo())
	h.opts.jsonOut = true

	if err := h.run(t, newListCommand(h.opts)); err != nil {
		t.Fatalf("list: %v", err)
	}
	var got struct {
		Modules []struct {
			Module          string `json:"module"`
			IsRoot          bool   `json:"is_root"`
			RequiredVersion string `json:"required_version"`
			VersionFile     string `json:"version_file"`
			VersionFilePath string `json:"version_file_path"`
		} `json:"modules"`
		Providers []json.RawMessage `json:"providers"`
	}
	if err := json.Unmarshal(h.stdout.Bytes(), &got); err != nil {
		t.Fatalf("parsing JSON: %v\n%s", err, h.stdout.String())
	}
	if len(got.Providers) != 2 {
		t.Errorf("providers = %d rows, want 2", len(got.Providers))
	}
	if len(got.Modules) != 2 {
		t.Fatalf("modules = %+v, want 2", got.Modules)
	}
	dev := got.Modules[0]
	if dev.Module != "envs/dev" || !dev.IsRoot || dev.RequiredVersion != ">= 1.5.0" ||
		dev.VersionFile != "1.5.7" || dev.VersionFilePath != "envs/dev/.terraform-version" {
		t.Errorf("modules[0] = %+v", dev)
	}
}

func TestDriftReportsCoreVersionMismatch(t *testing.T) {
	h := newDriftHarness(t, coreRepo())

	if err := h.run(t, newDriftCommand(h.opts)); err != nil {
		t.Fatalf("drift: %v", err)
	}
	out := h.stdout.String()
	for _, want := range []string{"Core version mismatch", "envs/prod", ">= 1.6.0", "1.5.7", "1 finding(s)."} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "envs/dev") {
		t.Errorf("envs/dev's version file satisfies its required_version:\n%s", out)
	}
}

func TestDriftCoreJSONAndFailOn(t *testing.T) {
	h := newDriftHarness(t, coreRepo())
	h.opts.jsonOut = true

	err := h.run(t, newDriftCommand(h.opts), "--exit-code", "--fail-on", "core")
	var exit *ExitCodeError
	if !errors.As(err, &exit) || exit.Code != ExitUpgradesAvailable {
		t.Fatalf("err = %v, want exit %d", err, ExitUpgradesAvailable)
	}

	var got struct {
		Findings struct {
			Core []map[string]string `json:"core_version_mismatch"`
		} `json:"findings"`
		Count int `json:"count"`
	}
	if err := json.Unmarshal(h.stdout.Bytes(), &got); err != nil {
		t.Fatalf("parsing JSON: %v\n%s", err, h.stdout.String())
	}
	want := map[string]string{
		"module": "envs/prod", "required_version": ">= 1.6.0",
		"version": "1.5.7", "version_file": "envs/prod/.terraform-version",
	}
	if got.Count != 1 || len(got.Findings.Core) != 1 {
		t.Fatalf("got %+v, want one core finding", got)
	}
	for k, v := range want {
		if got.Findings.Core[0][k] != v {
			t.Errorf("%s = %q, want %q", k, got.Findings.Core[0][k], v)
		}
	}

	h = newDriftHarness(t, coreRepo())
	if err := h.run(t, newDriftCommand(h.opts), "--exit-code", "--fail-on", "skew"); err != nil {
		t.Errorf("--fail-on skew must ignore a core mismatch, got %v", err)
	}
}
