package depgraph

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/provlift/provlift/internal/model"
)

// vendored builds a directory tree in the shape terraform init produces. Each
// entry is keyed by module name with a "source|constraint" value; an empty
// constraint means the module declares no required_providers.
func vendored(t *testing.T, modules map[string]string) string {
	t.Helper()

	root := t.TempDir()
	manifest := `{"Modules":[{"Key":"","Source":"","Dir":"."}`

	for key, spec := range modules {
		dir := filepath.Join(root, ".terraform", "modules", key)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("creating %s: %v", dir, err)
		}

		source, constraint, _ := strings.Cut(spec, "|")

		if constraint != "" {
			content := `terraform {
  required_providers {
    azurerm = {
      source  = "hashicorp/azurerm"
      version = "` + constraint + `"
    }
  }
}
`
			if err := os.WriteFile(filepath.Join(dir, "versions.tf"), []byte(content), 0o644); err != nil {
				t.Fatalf("writing versions.tf: %v", err)
			}
		}

		manifest += `,{"Key":"` + key + `","Source":"` + source +
			`","Dir":".terraform/modules/` + key + `"}`
	}
	manifest += `]}`

	manifestFile := filepath.Join(root, ".terraform", "modules", "modules.json")
	if err := os.WriteFile(manifestFile, []byte(manifest), 0o644); err != nil {
		t.Fatalf("writing manifest: %v", err)
	}
	return root
}

func azurerm(t *testing.T) model.Address {
	t.Helper()
	addr, err := model.ParseAddress("hashicorp/azurerm")
	if err != nil {
		t.Fatal(err)
	}
	return addr
}

func TestLoadReadsVendoredConstraints(t *testing.T) {
	// The shape found in a real repository: three shared modules each capping
	// the provider well below its newest release.
	root := vendored(t, map[string]string{
		"azure_region":            "git::https://github.com/acme/terraform-azurerm-regions.git?ref=v2.0.0|>= 4.11.0, <= 4.19.0",
		"resource_group":          "git::https://github.com/acme/terraform-azurerm-resource-group.git?ref=v3.0.0|>= 4.11.0, <= 4.19.0",
		"log_analytics_workspace": "git::https://github.com/acme/terraform-azurerm-log-analytics-workspace.git?ref=v1.4.5|>= 4.11.0, <= 4.19.0",
	})

	reqs, err := Load(root)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(reqs) != 3 {
		t.Fatalf("got %d requirements, want 3: %+v", len(reqs), reqs)
	}

	matched := For(reqs, azurerm(t))
	if len(matched) != 3 {
		t.Fatalf("For() returned %d, want 3", len(matched))
	}

	// Duplicate terms collapse into one constraint.
	if got := Constraint(matched); got != ">= 4.11.0, <= 4.19.0" {
		t.Errorf("Constraint() = %q, want \">= 4.11.0, <= 4.19.0\"", got)
	}

	sources := Sources(matched)
	if len(sources) != 3 {
		t.Fatalf("Sources() = %v, want 3 entries", sources)
	}
	// The repository name and its pinned ref are what identify the blocker.
	want := "terraform-azurerm-regions v2.0.0"
	var found bool
	for _, s := range sources {
		if s == want {
			found = true
		}
	}
	if !found {
		t.Errorf("Sources() = %v, missing %q", sources, want)
	}
}

func TestLoadIgnoresLocalModules(t *testing.T) {
	// A local module is already covered by the ordinary repository scan.
	root := vendored(t, map[string]string{
		"nested": "../..|>= 4.11.0, <= 4.19.0",
	})

	reqs, err := Load(root)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(reqs) != 0 {
		t.Errorf("Load() = %+v, want none for a local source", reqs)
	}
}

func TestLoadSkipsModulesDeclaringNoConstraint(t *testing.T) {
	root := vendored(t, map[string]string{
		"bare": "git::https://github.com/acme/mod.git?ref=v1.0.0|",
	})

	reqs, err := Load(root)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(reqs) != 0 {
		t.Errorf("Load() = %+v, want none", reqs)
	}
}

func TestLoadWithoutManifest(t *testing.T) {
	// A module tree that has never been initialised records nothing. That is
	// an absence of information, not a failure.
	reqs, err := Load(t.TempDir())
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if reqs != nil {
		t.Errorf("Load() = %+v, want nil", reqs)
	}
}

func TestLoadRejectsCorruptManifest(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, ".terraform", "modules")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "modules.json"), []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, err := Load(root); err == nil {
		t.Error("expected an error for a corrupt manifest")
	}
}

func TestRefOf(t *testing.T) {
	tests := []struct {
		source string
		want   string
	}{
		{"git::https://github.com/acme/mod.git?ref=v2.0.0", "v2.0.0"},
		{"git::https://github.com/acme/mod.git?ref=v2.0.0&depth=1", "v2.0.0"},
		{"git::https://github.com/acme/mod.git", ""},
		{"../local", ""},
	}

	for _, tc := range tests {
		t.Run(tc.source, func(t *testing.T) {
			if got := refOf(tc.source); got != tc.want {
				t.Errorf("refOf(%q) = %q, want %q", tc.source, got, tc.want)
			}
		})
	}
}

func TestShortSource(t *testing.T) {
	tests := []struct {
		source string
		want   string
	}{
		{"git::https://github.com/acme/terraform-azurerm-regions.git?ref=v2.0.0", "terraform-azurerm-regions"},
		{"registry.terraform.io/acme/vnet/azurerm", "azurerm"},
		{"", ""},
	}

	for _, tc := range tests {
		t.Run(tc.source, func(t *testing.T) {
			if got := shortSource(tc.source); got != tc.want {
				t.Errorf("shortSource(%q) = %q, want %q", tc.source, got, tc.want)
			}
		})
	}
}

func TestLabelFallsBackToModuleKey(t *testing.T) {
	r := Requirement{Module: "azure_region", Source: "", Ref: ""}
	if got := r.Label(); got != "azure_region" {
		t.Errorf("Label() = %q, want azure_region", got)
	}
}
