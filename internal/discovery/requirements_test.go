package discovery

import (
	"os"
	"path/filepath"
	"testing"
)

func TestRequirementsIn(t *testing.T) {
	dir := t.TempDir()

	content := `terraform {
  required_providers {
    azurerm = {
      source  = "hashicorp/azurerm"
      version = ">= 4.11.0, <= 4.19.0"
    }
    azurecaf = {
      source  = "aztfmod/azurecaf"
      version = "~> 1.2"
    }
  }
}
`
	if err := os.WriteFile(filepath.Join(dir, "versions.tf"), []byte(content), 0o644); err != nil {
		t.Fatalf("writing fixture: %v", err)
	}

	got := RequirementsIn(dir)
	if len(got) != 2 {
		t.Fatalf("got %d requirements, want 2: %+v", len(got), got)
	}

	byName := map[string]string{}
	for _, r := range got {
		byName[r.LocalName] = r.Constraint
	}
	if byName["azurerm"] != ">= 4.11.0, <= 4.19.0" {
		t.Errorf("azurerm constraint = %q", byName["azurerm"])
	}
	if byName["azurecaf"] != "~> 1.2" {
		t.Errorf("azurecaf constraint = %q", byName["azurecaf"])
	}
}

func TestRequirementsInIgnoresSubdirectories(t *testing.T) {
	// Vendored modules nest their own examples; only the module's own
	// declarations constrain the caller.
	dir := t.TempDir()

	root := `terraform {
  required_providers {
    azurerm = {
      source  = "hashicorp/azurerm"
      version = ">= 4.11.0"
    }
  }
}
`
	nested := `terraform {
  required_providers {
    azurerm = {
      source  = "hashicorp/azurerm"
      version = "~> 3.0"
    }
  }
}
`
	if err := os.WriteFile(filepath.Join(dir, "versions.tf"), []byte(root), 0o644); err != nil {
		t.Fatal(err)
	}
	sub := filepath.Join(dir, "examples", "single")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sub, "versions.tf"), []byte(nested), 0o644); err != nil {
		t.Fatal(err)
	}

	got := RequirementsIn(dir)
	if len(got) != 1 {
		t.Fatalf("got %d requirements, want 1: %+v", len(got), got)
	}
	if got[0].Constraint != ">= 4.11.0" {
		t.Errorf("Constraint = %q, want \">= 4.11.0\"", got[0].Constraint)
	}
}

func TestRequirementsInMissingDirectory(t *testing.T) {
	if got := RequirementsIn(filepath.Join(t.TempDir(), "nope")); got != nil {
		t.Errorf("RequirementsIn() = %+v, want nil", got)
	}
}

func TestRequirementsInDirectoryWithoutTerraformFiles(t *testing.T) {
	if got := RequirementsIn(t.TempDir()); got != nil {
		t.Errorf("RequirementsIn() = %+v, want nil", got)
	}
}
