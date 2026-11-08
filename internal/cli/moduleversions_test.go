package cli

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/provlift/provlift/internal/pullrequest"
)

// moduleManifest renders a modules.json with one registry module at the given
// version and one local module, which never carries a version.
func moduleManifest(vpcVersion string) string {
	return `{"Modules":[` +
		`{"Key":"","Source":"","Dir":"."},` +
		`{"Key":"vpc","Source":"registry.terraform.io/acme/vpc/aws","Version":"` + vpcVersion + `","Dir":".terraform/modules/vpc"},` +
		`{"Key":"tags","Source":"./modules/tags","Dir":"modules/tags"}]}`
}

type moduleVersionsResult struct {
	ModuleVersions pullrequest.ModuleVersions `json:"module_versions"`
	PullRequest    pullrequest.Request        `json:"pull_request"`
}

func runModuleUpgrade(t *testing.T, baseline, afterInit string) moduleVersionsResult {
	t.Helper()
	files := map[string]string{}
	if baseline != "" {
		files[".terraform/modules/modules.json"] = baseline
	}
	h := newHarness(t, files)
	useFakeTerraform(t, h)
	t.Setenv(fakeModuleManifest, afterInit)
	h.opts.jsonOut = true

	if err := h.run(t, newUpgradeCommand(h.opts), "--provider", "azurerm", "--to", "4.81.0", "--level", "minor"); err != nil {
		t.Fatalf("upgrade: %v", err)
	}
	var result moduleVersionsResult
	if err := json.Unmarshal(h.stdout.Bytes(), &result); err != nil {
		t.Fatalf("parsing upgrade JSON: %v\n%s", err, h.stdout.String())
	}
	return result
}

func TestUpgradeReportsModuleVersionMovedByInit(t *testing.T) {
	// init -upgrade moved the registry module from 5.1.0 to 5.4.0 without any
	// edit to its module block. The local module has no version and must not
	// be reported.
	result := runModuleUpgrade(t, moduleManifest("5.1.0"), moduleManifest("5.4.0"))

	changes := result.ModuleVersions.Changes
	if len(changes) != 1 {
		t.Fatalf("changes = %+v, want exactly the vpc module", changes)
	}
	if c := changes[0]; c.Module != "vpc" || c.From != "5.1.0" || c.To != "5.4.0" || c.Source != "registry.terraform.io/acme/vpc/aws" {
		t.Errorf("change = %+v, want vpc 5.1.0 -> 5.4.0", c)
	}
	if len(result.ModuleVersions.NoBaseline) != 0 {
		t.Errorf("no_baseline = %v, want none when a manifest existed", result.ModuleVersions.NoBaseline)
	}
	body := result.PullRequest.Body
	if !strings.Contains(body, "## Module versions") || !strings.Contains(body, "| `vpc` | `registry.terraform.io/acme/vpc/aws` | 5.1.0 | 5.4.0 |") {
		t.Errorf("pull request body does not report the module change:\n%s", body)
	}
}

func TestUpgradeReportsModulesMovedByFailedInit(t *testing.T) {
	// Terraform installs modules before it resolves providers. When init then
	// fails, the lock file is restored but the newer module code stays on
	// disk, so the movement must still be reported.
	h := newHarness(t, map[string]string{".terraform/modules/modules.json": moduleManifest("5.1.0")})
	useFakeTerraform(t, h)
	t.Setenv(fakeModuleManifest, moduleManifest("5.4.0"))
	t.Setenv(fakeTerraformFail, "init")
	h.opts.jsonOut = true

	if err := h.run(t, newUpgradeCommand(h.opts), "--provider", "azurerm", "--to", "4.81.0", "--level", "minor"); err == nil {
		t.Fatal("expected the failed init to fail the upgrade")
	}
	var result moduleVersionsResult
	if err := json.Unmarshal(h.stdout.Bytes(), &result); err != nil {
		t.Fatalf("parsing upgrade JSON: %v\n%s", err, h.stdout.String())
	}
	if changes := result.ModuleVersions.Changes; len(changes) != 1 || changes[0].To != "5.4.0" {
		t.Errorf("changes = %+v, want vpc moved to 5.4.0 despite the failed init", changes)
	}
}

func TestUpgradeSaysWhenNoModuleBaselineExists(t *testing.T) {
	// A clean checkout has no modules.json, so nothing can be compared. That
	// must be said rather than reported as "no changes".
	result := runModuleUpgrade(t, "", moduleManifest("5.4.0"))

	if len(result.ModuleVersions.Changes) != 0 {
		t.Errorf("changes = %+v, want none without a baseline", result.ModuleVersions.Changes)
	}
	if len(result.ModuleVersions.NoBaseline) != 1 {
		t.Fatalf("no_baseline = %v, want the upgraded root", result.ModuleVersions.NoBaseline)
	}
	if !strings.Contains(result.PullRequest.Body, "No earlier module selection was recorded") {
		t.Errorf("pull request body does not explain the missing baseline:\n%s", result.PullRequest.Body)
	}
}

func TestUpgradeIsQuietWhenModulesDidNotMove(t *testing.T) {
	for name, scenario := range map[string]struct{ baseline, after string }{
		"unchanged registry module": {moduleManifest("5.1.0"), moduleManifest("5.1.0")},
		"only local modules, no baseline": {"", `{"Modules":[{"Key":"","Source":"","Dir":"."},` +
			`{"Key":"tags","Source":"./modules/tags","Dir":"modules/tags"}]}`},
	} {
		t.Run(name, func(t *testing.T) {
			result := runModuleUpgrade(t, scenario.baseline, scenario.after)
			if len(result.ModuleVersions.Changes) != 0 || len(result.ModuleVersions.NoBaseline) != 0 {
				t.Errorf("module_versions = %+v, want nothing to report", result.ModuleVersions)
			}
			if strings.Contains(result.PullRequest.Body, "## Module versions") {
				t.Errorf("pull request body has a module section with nothing to report:\n%s", result.PullRequest.Body)
			}
		})
	}
}
