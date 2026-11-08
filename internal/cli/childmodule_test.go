package cli

import (
	"encoding/json"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/provlift/provlift/internal/lockfile"
)

// sharedChildRepo has two root modules that both call one local child module.
// Only the roots own lock files; the child declares its own constraint.
func sharedChildRepo(childConstraint string) map[string]map[string]string {
	root := func() map[string]string {
		files := moduleFiles("hashicorp/azurerm", "~> 4.65", "4.65.0")
		files["shared.tf"] = `module "shared" {
  source = "../../modules/shared"
}
`
		return files
	}
	return map[string]map[string]string{
		"envs/a":         root(),
		"envs/b":         root(),
		"modules/shared": moduleFiles("hashicorp/azurerm", childConstraint, ""),
	}
}

type upgradeJSON struct {
	Plan struct {
		Actions []struct {
			Module string `json:"module"`
			To     string `json:"to"`
		} `json:"actions"`
		Skipped []struct {
			Module string `json:"module"`
			Reason string `json:"reason"`
		} `json:"skipped"`
	} `json:"plan"`
	ChangedFiles []string `json:"changed_files"`
}

func runUpgradeJSON(t *testing.T, h *harness, arguments ...string) upgradeJSON {
	t.Helper()
	h.stdout.Reset()
	h.opts.jsonOut = true
	if err := h.run(t, newUpgradeCommand(h.opts), arguments...); err != nil {
		t.Fatalf("upgrade %v: %v", arguments, err)
	}
	var result upgradeJSON
	if err := json.Unmarshal(h.stdout.Bytes(), &result); err != nil {
		t.Fatalf("parsing upgrade JSON: %v\n%s", err, h.stdout.String())
	}
	return result
}

func TestUpgradeChildConstraintChangeRefreshesEveryConsumingRoot(t *testing.T) {
	// "~> 4.65.0" forbids 4.81.0, so the child's constraint must change. The
	// child owns no lock file; each root that calls it owns one, and every one
	// of them must be regenerated, even though --module selected only the child.
	h := newDriftHarness(t, sharedChildRepo("~> 4.65.0"))
	logPath := useFakeTerraform(t, h)

	result := runUpgradeJSON(t, h, "--provider", "azurerm", "--module", "modules/shared",
		"--to", "4.81.0", "--level", "minor")

	if len(result.Plan.Actions) != 1 || result.Plan.Actions[0].Module != "modules/shared" {
		t.Fatalf("expected a single child action, got %+v", result.Plan.Actions)
	}

	calls := readInvocations(t, logPath)
	for _, root := range []string{"a", "b"} {
		for _, command := range []string{"init", "providers lock", "validate"} {
			if indexOfCall(calls, root+"\t"+command) < 0 {
				t.Errorf("terraform %s did not run in consuming root %s:\n%s", command, root, strings.Join(calls, "\n"))
			}
		}
		locks, _, err := lockfile.Read(filepath.Join(h.dir, "envs", root))
		if err != nil {
			t.Fatal(err)
		}
		if got := locks["registry.terraform.io/hashicorp/azurerm"].Version; got != "4.81.0" {
			t.Errorf("envs/%s lock = %s, want 4.81.0", root, got)
		}
	}

	changed := append([]string(nil), result.ChangedFiles...)
	sort.Strings(changed)
	want := []string{"envs/a/.terraform.lock.hcl", "envs/b/.terraform.lock.hcl", "modules/shared/main.tf"}
	if strings.Join(changed, ",") != strings.Join(want, ",") {
		t.Errorf("changed_files = %v, want %v", changed, want)
	}
}

type checkJSON struct {
	Providers []struct {
		Module              string   `json:"module"`
		BlockedByDependency bool     `json:"blocked_by_dependency"`
		ExternalConstraint  string   `json:"external_constraint"`
		ExternalSources     []string `json:"external_sources"`
	} `json:"providers"`
}

func runCheckJSON(t *testing.T, h *harness) checkJSON {
	t.Helper()
	h.stdout.Reset()
	h.opts.jsonOut = true
	if err := h.run(t, newCheckCommand(h.opts)); err != nil {
		t.Fatalf("check: %v", err)
	}
	var result checkJSON
	if err := json.Unmarshal(h.stdout.Bytes(), &result); err != nil {
		t.Fatalf("parsing check JSON: %v\n%s", err, h.stdout.String())
	}
	return result
}

// vendoredLocalManifest is the modules.json terraform init writes for a root
// that calls only the shared local module.
const vendoredLocalManifest = `{"Modules":[{"Key":"","Source":"","Dir":"."},{"Key":"shared","Source":"../../modules/shared","Dir":"../../modules/shared"}]}`

func TestCheckAttributesLocalModuleCeilingToCallingRoots(t *testing.T) {
	// The roots allow 4.81.0 on their own, but the module they both call caps
	// azurerm below 4.70.0, and Terraform intersects the two.
	for _, vendored := range []bool{false, true} {
		repo := sharedChildRepo(">= 4.0, < 4.70.0")
		if vendored {
			// Once a root is initialised its modules.json lists the local call.
			// That must not hide the ceiling the scan already knows about.
			repo["envs/a"][".terraform/modules/modules.json"] = vendoredLocalManifest
			repo["envs/b"][".terraform/modules/modules.json"] = vendoredLocalManifest
		}
		h := newDriftHarness(t, repo)

		result := runCheckJSON(t, h)
		for _, provider := range result.Providers {
			if provider.Module == "modules/shared" {
				if provider.ExternalConstraint != "" || provider.BlockedByDependency {
					t.Errorf("vendored=%t: the shared module was capped by its own declaration: %+v", vendored, provider)
				}
				continue
			}
			if provider.ExternalConstraint != ">= 4.0, < 4.70.0" || !provider.BlockedByDependency ||
				strings.Join(provider.ExternalSources, ",") != "modules/shared" {
				t.Errorf("vendored=%t: %s does not report the shared module's ceiling: %+v", vendored, provider.Module, provider)
			}
		}
	}
}

func TestCheckDoesNotRepeatLocalCeilingRecordedInLock(t *testing.T) {
	// Terraform folds the shared module's terms into each root's lock file.
	// Spelled differently there, they must still be reported only once.
	repo := sharedChildRepo(">= 4.0, < 4.70.0")
	repo["envs/a"][".terraform.lock.hcl"] = `provider "registry.terraform.io/hashicorp/azurerm" {
  version     = "4.65.0"
  constraints = "~> 4.65, >= 4.0.0, < 4.70.0"
}
`
	h := newDriftHarness(t, repo)

	for _, provider := range runCheckJSON(t, h).Providers {
		if provider.Module == "envs/a" && provider.ExternalConstraint != ">= 4.0, < 4.70.0" {
			t.Errorf("external_constraint = %q, want %q", provider.ExternalConstraint, ">= 4.0, < 4.70.0")
		}
	}
}

func TestUpgradeNeverPlansPastLocalModuleCeiling(t *testing.T) {
	// "< 4.70.0" in the shared module forbids 4.81.0. With only the roots
	// selected, the run cannot lift that ceiling, so the roots must stay put.
	// Selecting the shared module as well lifts it, which
	// TestUpgradeRaisingSharedCeilingPlansTheRootsToo covers.
	h := newDriftHarness(t, sharedChildRepo(">= 4.0, < 4.70.0"))

	result := runUpgradeJSON(t, h, "--provider", "azurerm", "--level", "minor", "--dry-run", "--module", "envs/*")
	for _, action := range result.Plan.Actions {
		t.Errorf("planned %s -> %s past the shared module's ceiling", action.Module, action.To)
	}
	for _, root := range []string{"envs/a", "envs/b"} {
		blamed := false
		for _, skip := range result.Plan.Skipped {
			blamed = blamed || (skip.Module == root && strings.Contains(skip.Reason, "< 4.70.0"))
		}
		if !blamed {
			t.Errorf("%s was not skipped with the ceiling as its reason: %+v", root, result.Plan.Skipped)
		}
	}
}

func TestUpgradeRaisingSharedCeilingPlansTheRootsToo(t *testing.T) {
	// One run rewrites the shared module's "~> 4.65.0" to admit 4.81.0. The
	// roots are judged against the ceiling as it will stand, so they are
	// planned too rather than reported as blocked by the module being lifted.
	h := newDriftHarness(t, sharedChildRepo("~> 4.65.0"))
	useFakeTerraform(t, h)

	result := runUpgradeJSON(t, h, "--provider", "azurerm", "--to", "4.81.0", "--level", "minor")

	planned := map[string]string{}
	for _, action := range result.Plan.Actions {
		planned[action.Module] = action.To
	}
	for _, module := range []string{"envs/a", "envs/b", "modules/shared"} {
		if planned[module] != "4.81.0" {
			t.Errorf("%s was not planned to 4.81.0: actions %+v, skipped %+v", module, result.Plan.Actions, result.Plan.Skipped)
		}
	}
}

func TestUpgradeRepeatedRunWithChildModulePlansNothing(t *testing.T) {
	// "~> 4.0" already admits 4.81.0, so the child needs no edit and has no
	// lock file to move. Once the roots are at the target, a repeat of the
	// same command has nothing left to do and must not invoke Terraform.
	h := newDriftHarness(t, sharedChildRepo("~> 4.0"))
	logPath := useFakeTerraform(t, h)
	arguments := []string{"--provider", "azurerm", "--to", "4.81.0", "--level", "minor"}

	first := runUpgradeJSON(t, h, arguments...)
	for _, action := range first.Plan.Actions {
		if action.To != "4.81.0" {
			t.Errorf("first run planned %s -> %s, want only moves to 4.81.0", action.Module, action.To)
		}
	}
	calls := len(readInvocations(t, logPath))

	second := runUpgradeJSON(t, h, arguments...)
	if len(second.Plan.Actions) != 0 || len(second.ChangedFiles) != 0 {
		t.Fatalf("second run planned work: %+v", second)
	}
	if got := len(readInvocations(t, logPath)); got != calls {
		t.Errorf("second run invoked Terraform %d more time(s)", got-calls)
	}
	skippedChild := false
	for _, skip := range second.Plan.Skipped {
		skippedChild = skippedChild || (skip.Module == "modules/shared" && strings.Contains(skip.Reason, "no lock file"))
	}
	if !skippedChild {
		t.Errorf("the child should be reported as skipped with a reason: %+v", second.Plan.Skipped)
	}
}
