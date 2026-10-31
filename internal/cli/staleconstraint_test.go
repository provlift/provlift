package cli

import (
	"strings"
	"testing"
)

// staleLockRepo reproduces the state left by an earlier upgrade that was
// reverted: versions.tf declares the original constraint again, but the lock
// file still records the merged constraints from the run in between.
func staleLockRepo() map[string]map[string]string {
	return map[string]map[string]string{
		".": {
			"main.tf": `terraform {
  required_providers {
    azurerm = {
      source  = "hashicorp/azurerm"
      version = ">= 3.85.0"
    }
    azurecaf = {
      source  = "aztfmod/azurecaf"
      version = "~> 1.2.26"
    }
  }
}
`,
			".terraform.lock.hcl": `provider "registry.terraform.io/hashicorp/azurerm" {
  version     = "4.19.0"
  constraints = ">= 3.85.0, >= 4.11.0, <= 4.19.0"
}

provider "registry.terraform.io/aztfmod/azurecaf" {
  version     = "1.2.34"
  constraints = "~> 1.2.34"
}
`,
			// A vendored module that constrains azurerm and says nothing at
			// all about azurecaf.
			".terraform/modules/modules.json": `{"Modules":[
  {"Key":"","Source":"","Dir":"."},
  {"Key":"regions","Source":"git::https://github.com/acme/terraform-azurerm-regions.git?ref=v2.0.0","Dir":".terraform/modules/regions"}
]}`,
			".terraform/modules/regions/versions.tf": `terraform {
  required_providers {
    azurerm = {
      source  = "hashicorp/azurerm"
      version = ">= 4.11.0, <= 4.19.0"
    }
  }
}
`,
		},
	}
}

func TestCheckIgnoresStaleLockConstraintsWhenModulesAreVendored(t *testing.T) {
	h := newDriftHarness(t, staleLockRepo())
	h.fake.versions = map[string][]string{
		"hashicorp/azurerm": {"3.85.0", "4.11.0", "4.19.0", "4.81.0", "5.6.0"},
		"aztfmod/azurecaf":  {"1.2.26", "1.2.34"},
	}

	if err := h.run(t, newCheckCommand(h.opts)); err != nil {
		t.Fatalf("check: %v", err)
	}

	out := h.stdout.String()

	// The vendored modules are a complete account of what dependencies
	// require. None mentions azurecaf, so nothing external constrains it and
	// the lock file's stale record must not be attributed to a dependency.
	if strings.Contains(out, "~> 1.2.34") {
		t.Errorf("a stale lock entry was reported as a dependency constraint:\n%s", out)
	}

	// The genuine azurerm ceiling must still be found and attributed.
	if !strings.Contains(out, "terraform-azurerm-regions v2.0.0") {
		t.Errorf("the real dependency ceiling was not attributed:\n%s", out)
	}
	if !strings.Contains(out, ">= 4.11.0, <= 4.19.0") {
		t.Errorf("the real dependency constraint is missing:\n%s", out)
	}
}

func TestCheckUsesLockConstraintsWhenNothingIsVendored(t *testing.T) {
	// Without a module manifest nothing is known about dependencies, so the
	// merged constraints in the lock file remain the best available signal.
	files := staleLockRepo()
	delete(files["."], ".terraform/modules/modules.json")
	delete(files["."], ".terraform/modules/regions/versions.tf")

	h := newDriftHarness(t, files)
	h.fake.versions = map[string][]string{
		"hashicorp/azurerm": {"3.85.0", "4.11.0", "4.19.0", "4.81.0", "5.6.0"},
		"aztfmod/azurecaf":  {"1.2.26", "1.2.34"},
	}

	if err := h.run(t, newCheckCommand(h.opts)); err != nil {
		t.Fatalf("check: %v", err)
	}

	out := h.stdout.String()
	if !strings.Contains(out, "4.19.0") {
		t.Errorf("the ceiling recovered from the lock file is missing:\n%s", out)
	}
	// Without the manifest the originating module cannot be named.
	if !strings.Contains(out, "run `terraform init`") {
		t.Errorf("expected the unattributed form of the message:\n%s", out)
	}
}

func TestUpgradeNeverExceedsDependencyCeiling(t *testing.T) {
	// Every way of choosing a target must stop at the ceiling, whether it was
	// read from vendored modules or recovered from the lock file.
	selections := []struct {
		name string
		args []string
		want string
	}{
		{"explicit target", []string{"--to", "5.6.0"}, "forbidden by a dependency"},
		{"major budget", nil, "a dependency constrains"},
		{"next major", []string{"--next-major"}, "a dependency constrains"},
	}
	for _, vendored := range []bool{false, true} {
		for _, selection := range selections {
			files := staleLockRepo()
			if !vendored {
				delete(files["."], ".terraform/modules/modules.json")
				delete(files["."], ".terraform/modules/regions/versions.tf")
			}
			h := newDriftHarness(t, files)
			h.fake.versions = map[string][]string{
				"hashicorp/azurerm": {"3.85.0", "4.11.0", "4.19.0", "4.81.0", "5.6.0"},
				"aztfmod/azurecaf":  {"1.2.26", "1.2.34"},
			}
			before := h.mainTF(t)
			arguments := append([]string{"--provider", "azurerm", "--level", "major", "--allow-major", "--skip-init"}, selection.args...)
			if err := h.run(t, newUpgradeCommand(h.opts), arguments...); err != nil {
				t.Fatal(err)
			}
			if h.mainTF(t) != before || !strings.Contains(h.stdout.String(), selection.want) {
				t.Fatalf("dependency ceiling bypassed (%s, vendored=%t): %s", selection.name, vendored, h.stdout.String())
			}
		}
	}
}

func TestUpgradeRaiseFloorCatchesUpWithInstalledVersion(t *testing.T) {
	// Locked at 1.2.34 but still declaring ">= 1.2.26". There is no newer
	// version to install, yet the declared floor is behind what is in use.
	h := newDriftHarness(t, map[string]map[string]string{
		".": moduleFiles("aztfmod/azurecaf", ">= 1.2.26", "1.2.34"),
	})
	h.fake.versions = map[string][]string{
		"aztfmod/azurecaf": {"1.2.26", "1.2.34"},
	}

	err := h.run(t, newUpgradeCommand(h.opts),
		"--provider", "azurecaf", "--level", "minor", "--raise-floor", "--skip-init")
	if err != nil {
		t.Fatalf("upgrade: %v", err)
	}

	got := h.mainTF(t)
	if !strings.Contains(got, `version = ">= 1.2.34"`) {
		t.Errorf("the stale floor was not raised to the installed version:\n%s", got)
	}
	if !strings.Contains(h.stdout.String(), "floor") {
		t.Errorf("the plan should mark this as a constraint-only change:\n%s", h.stdout.String())
	}
}

func TestUpgradeWithoutRaiseFloorLeavesStaleFloorAlone(t *testing.T) {
	h := newDriftHarness(t, map[string]map[string]string{
		".": moduleFiles("aztfmod/azurecaf", ">= 1.2.26", "1.2.34"),
	})
	h.fake.versions = map[string][]string{
		"aztfmod/azurecaf": {"1.2.26", "1.2.34"},
	}
	before := h.mainTF(t)

	err := h.run(t, newUpgradeCommand(h.opts),
		"--provider", "azurecaf", "--level", "minor", "--skip-init")
	if err != nil {
		t.Fatalf("upgrade: %v", err)
	}

	if h.mainTF(t) != before {
		t.Errorf("the constraint changed without --raise-floor:\n%s", h.mainTF(t))
	}
}
