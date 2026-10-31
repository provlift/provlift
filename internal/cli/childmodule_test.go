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
