package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// sharedModuleRepo has no lock file, which is correct for a reusable module:
// it publishes a supported floor rather than a pinned selection.
func sharedModuleRepo() map[string]map[string]string {
	return map[string]map[string]string{
		".": moduleFiles("hashicorp/azurerm", ">= 3.85.0", ""),
	}
}

func TestCheckReportsConstraintFloorWhenUnlocked(t *testing.T) {
	h := newDriftHarness(t, sharedModuleRepo())

	if err := h.run(t, newCheckCommand(h.opts)); err != nil {
		t.Fatalf("check: %v", err)
	}

	out := h.stdout.String()
	if !strings.Contains(out, "(floor)") {
		t.Errorf("the current version should be labelled as a floor:\n%s", out)
	}
	// The floor is the oldest version the constraint admits, not the newest.
	if !strings.Contains(out, "4.65.0") {
		t.Errorf("expected the floor 4.65.0:\n%s", out)
	}
	// Two majors of untested range must not be reported as healthy.
	if !strings.Contains(out, "major") {
		t.Errorf("expected a major gap between floor and latest:\n%s", out)
	}
	if strings.Contains(out, "0 of 1") {
		t.Errorf("a stale floor must not be reported as nothing to do:\n%s", out)
	}
}

func TestUpgradeRaiseFloorRewritesLowerBound(t *testing.T) {
	h := newDriftHarness(t, sharedModuleRepo())

	err := h.run(t, newUpgradeCommand(h.opts),
		"--provider", "azurerm", "--level", "major", "--exact-level", "--allow-major",
		"--raise-floor", "--skip-init")
	if err != nil {
		t.Fatalf("upgrade: %v", err)
	}

	got := h.mainTF(t)
	if !strings.Contains(got, `version = ">= 5.6.0"`) {
		t.Errorf("the supported minimum was not raised:\n%s", got)
	}
}

func TestUpgradeWithoutRaiseFloorLeavesLowerBoundAlone(t *testing.T) {
	// ">= 3.85.0" already admits 5.6.0, so the minimum-diff rule applies and
	// the module's contract with its consumers is left untouched.
	h := newDriftHarness(t, sharedModuleRepo())
	before := h.mainTF(t)

	err := h.run(t, newUpgradeCommand(h.opts),
		"--provider", "azurerm", "--level", "major", "--allow-major", "--skip-init")
	if err != nil {
		t.Fatalf("upgrade: %v", err)
	}

	if h.mainTF(t) != before {
		t.Errorf("constraint changed without --raise-floor:\n%s", h.mainTF(t))
	}
}

func TestUpgradeRaiseFloorRespectsLevel(t *testing.T) {
	h := newDriftHarness(t, map[string]map[string]string{
		".": moduleFiles("hashicorp/azurerm", ">= 4.65.0", ""),
	})

	// --level minor must not raise the floor across a major boundary.
	err := h.run(t, newUpgradeCommand(h.opts),
		"--provider", "azurerm", "--level", "minor", "--raise-floor", "--skip-init")
	if err != nil {
		t.Fatalf("upgrade: %v", err)
	}

	got := h.mainTF(t)
	if strings.Contains(got, "5.6.0") {
		t.Errorf("--level minor must not reach a major version:\n%s", got)
	}
	if !strings.Contains(got, `version = ">= 4.81.0"`) {
		t.Errorf("floor should have been raised to 4.81.0:\n%s", got)
	}
}

func TestUpgradeBreakingBoundaryRequiresAcknowledgement(t *testing.T) {
	for _, scenario := range []struct {
		name       string
		constraint string
		locked     string
		versions   []string
		args       []string
	}{
		{"zero major minor", ">= 0.1.0", "0.1.0", []string{"0.1.0", "0.2.0"}, []string{"--level", "minor"}},
		{"breaking floor and patch", ">= 3.0.0", "4.65.0", []string{"3.0.0", "4.65.0", "4.65.1"}, []string{"--level", "patch", "--raise-floor"}},
		{"pessimistic zero major floor and patch", "~> 0.1", "0.2.0", []string{"0.1.0", "0.2.0", "0.2.1"}, []string{"--level", "patch", "--raise-floor"}},
		{"pessimistic zero major floor only", "~> 0.1", "0.2.1", []string{"0.1.0", "0.2.1"}, []string{"--level", "patch", "--raise-floor"}},
		{"exact zero major floor and patch", "= 0.1.0", "0.2.0", []string{"0.1.0", "0.2.0", "0.2.1"}, []string{"--level", "patch", "--raise-floor"}},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			h := newDriftHarness(t, map[string]map[string]string{
				".": moduleFiles("hashicorp/azurerm", scenario.constraint, scenario.locked),
			})
			h.fake.versions = map[string][]string{"hashicorp/azurerm": scenario.versions}
			before := h.mainTF(t)
			arguments := append([]string{"--provider", "azurerm", "--skip-init"}, scenario.args...)
			if err := h.run(t, newUpgradeCommand(h.opts), arguments...); err == nil || !strings.Contains(err.Error(), "--allow-major") {
				t.Fatalf("expected breaking-change refusal, got %v", err)
			}
			if h.mainTF(t) != before {
				t.Fatal("refusal modified configuration")
			}
			arguments = append(arguments, "--allow-major")
			if err := h.run(t, newUpgradeCommand(h.opts), arguments...); err != nil {
				t.Fatalf("acknowledged upgrade refused: %v", err)
			}
		})
	}
}

func TestUpgradeRaiseFloorStillRequiresMajorAcknowledgement(t *testing.T) {
	h := newDriftHarness(t, sharedModuleRepo())
	before := h.mainTF(t)

	err := h.run(t, newUpgradeCommand(h.opts),
		"--provider", "azurerm", "--level", "major", "--exact-level", "--raise-floor", "--skip-init")
	if err == nil {
		t.Fatal("expected a refusal without --allow-major")
	}
	if !strings.Contains(err.Error(), "--allow-major") {
		t.Errorf("unexpected error: %v", err)
	}
	if h.mainTF(t) != before {
		t.Error("the refusal must happen before any file is written")
	}
}

func TestUpgradeRaiseFloorExplainsWhyTwoSegmentPessimisticCannotMove(t *testing.T) {
	// "~> 1.2" admits all of 1.x. Its floor cannot reach 1.2.34 without the
	// ceiling dropping to < 1.3.0, so the constraint stays put and the output
	// has to say why, whichever path the plan takes.
	withChild := func(files map[string]string) map[string]string {
		files["shared.tf"] = "module \"shared\" {\n  source = \"./modules/shared\"\n}\n"
		return files
	}
	for _, scenario := range []struct {
		name  string
		files map[string]map[string]string
	}{
		{"root upgrade", map[string]map[string]string{
			".": moduleFiles("aztfmod/azurecaf", "~> 1.2", "1.2.0"),
		}},
		{"root already current", map[string]map[string]string{
			".": moduleFiles("aztfmod/azurecaf", "~> 1.2", "1.2.34"),
		}},
		{"child module", map[string]map[string]string{
			".":              withChild(moduleFiles("aztfmod/azurecaf", ">= 1.2.34", "1.2.34")),
			"modules/shared": moduleFiles("aztfmod/azurecaf", "~> 1.2", ""),
		}},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			h := newDriftHarness(t, scenario.files)
			h.fake.versions = map[string][]string{"aztfmod/azurecaf": {"1.2.0", "1.2.34"}}

			err := h.run(t, newUpgradeCommand(h.opts),
				"--provider", "azurecaf", "--level", "patch", "--raise-floor", "--skip-init")
			if err != nil {
				t.Fatalf("upgrade: %v", err)
			}

			for dir, files := range scenario.files {
				path := filepath.Join(h.dir, filepath.FromSlash(dir), "main.tf")
				got, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				if string(got) != files["main.tf"] {
					t.Errorf("%s changed:\n%s", path, got)
				}
			}
			out := h.stdout.String()
			if !strings.Contains(out, "narrowing its ceiling") || !strings.Contains(out, ">= 1.2.34, < 2.0.0") {
				t.Errorf("output should explain why the floor cannot move:\n%s", out)
			}
		})
	}
}

func TestUpgradeFloorAddedToUnconstrainedProviderRequiresAcknowledgement(t *testing.T) {
	// No version argument at all: the module accepts anything, so declaring a
	// floor is a contract change even when the version only moves by a patch.
	const unconstrained = `terraform {
  required_providers {
    p = {
      source = "hashicorp/azurerm"
    }
  }
}
`
	for _, scenario := range []struct {
		name   string
		locked string
		want   string
	}{
		{"alongside a patch upgrade", "4.65.0", "4.65.1"},
		{"constraint only", "4.65.1", "4.65.1"},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			files := moduleFiles("hashicorp/azurerm", "", scenario.locked)
			files["main.tf"] = unconstrained
			h := newDriftHarness(t, map[string]map[string]string{".": files})
			h.fake.versions = map[string][]string{"hashicorp/azurerm": {"4.65.0", "4.65.1"}}
			arguments := []string{"--provider", "azurerm", "--level", "patch", "--raise-floor", "--skip-init"}

			err := h.run(t, newUpgradeCommand(h.opts), arguments...)
			if err == nil || !strings.Contains(err.Error(), "--allow-major") {
				t.Fatalf("expected breaking-change refusal, got %v", err)
			}
			if h.mainTF(t) != unconstrained {
				t.Fatalf("refusal modified configuration:\n%s", h.mainTF(t))
			}

			if err := h.run(t, newUpgradeCommand(h.opts), append(arguments, "--allow-major")...); err != nil {
				t.Fatalf("acknowledged floor raise refused: %v", err)
			}
			if want := `version = ">= ` + scenario.want + `"`; !strings.Contains(h.mainTF(t), want) {
				t.Errorf("expected %s:\n%s", want, h.mainTF(t))
			}
		})
	}
}
