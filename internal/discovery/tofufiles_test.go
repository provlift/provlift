package discovery

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/provlift/provlift/internal/model"
)

func tofuRequirement(version string) string {
	return "terraform {\n  required_providers {\n    aws = { source = \"hashicorp/aws\", version = \"" + version + "\" }\n  }\n}\n"
}

func scanTofu(t *testing.T, registryHost string, files map[string]string) *model.Inventory {
	t.Helper()
	root := t.TempDir()
	for name, content := range files {
		path := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	inv, err := Scan(Options{Root: root, RegistryHost: registryHost})
	if err != nil {
		t.Fatal(err)
	}
	return inv
}

func TestScanDiscoversTofuOnlyModuleWithOpenTofu(t *testing.T) {
	inv := scanTofu(t, model.OpenTofuRegistryHost, map[string]string{
		"tofu/main.tofu": tofuRequirement("~> 5.0"),
	})
	mod := moduleByRelDir(inv, "tofu")
	if mod == nil {
		t.Fatalf("tofu module not discovered: %+v", inv.Modules)
	}
	if len(mod.Requirements) != 1 || mod.Requirements[0].ConstraintPos.File != "tofu/main.tofu" {
		t.Errorf("requirements = %+v", mod.Requirements)
	}
}

func TestScanTofuFileShadowsTFWithSameName(t *testing.T) {
	inv := scanTofu(t, model.OpenTofuRegistryHost, map[string]string{
		"main.tofu":      tofuRequirement("~> 5.0"),
		"main.tf":        tofuRequirement("~> 4.0"),
		"extra.tf":       "variable \"x\" {}\n",
		"data.tf.json":   "{}",
		"data.tofu.json": "{}",
	})
	mod := moduleByRelDir(inv, ".")
	if mod == nil || len(mod.Requirements) != 1 {
		t.Fatalf("module = %+v, want one requirement (main.tf is shadowed)", mod)
	}
	if req := mod.Requirements[0]; req.Constraint != "~> 5.0" || req.ConstraintPos.File != "main.tofu" {
		t.Errorf("requirement = %s at %s, want ~> 5.0 at main.tofu", req.Constraint, req.ConstraintPos.File)
	}

	var jsonFiles []string
	for _, d := range inv.Diagnostics {
		if d.Summary == "JSON configuration is not supported" {
			jsonFiles = append(jsonFiles, d.Pos.File)
			if !strings.Contains(d.Detail, ".tofu.json") {
				t.Errorf("detail = %q, want it to name .tofu.json", d.Detail)
			}
		}
	}
	if len(jsonFiles) != 1 || jsonFiles[0] != "data.tofu.json" {
		t.Errorf("JSON diagnostics for %v, want only data.tofu.json (data.tf.json is shadowed)", jsonFiles)
	}
}

func TestScanIgnoresTofuFilesWithTerraform(t *testing.T) {
	for _, host := range []string{"", "registry.terraform.io"} {
		inv := scanTofu(t, host, map[string]string{
			"main.tofu":           tofuRequirement("~> 5.0"),
			"main.tf":             tofuRequirement("~> 4.0"),
			"only/main.tofu.json": "{}",
		})
		mod := moduleByRelDir(inv, ".")
		if mod == nil || len(mod.Requirements) != 1 || mod.Requirements[0].Constraint != "~> 4.0" {
			t.Fatalf("host %q: module = %+v, want main.tf's ~> 4.0", host, mod)
		}
		if moduleByRelDir(inv, "only") != nil {
			t.Errorf("host %q: a .tofu.json-only directory is not a Terraform module", host)
		}

		var infos []string
		for _, d := range inv.Diagnostics {
			if d.Severity == "info" && strings.Contains(d.Summary, "OpenTofu files are ignored") {
				infos = append(infos, d.Pos.File)
			}
		}
		if strings.Join(infos, ",") != "main.tofu,only/main.tofu.json" {
			t.Errorf("host %q: info diagnostics at %v, want main.tofu and only/main.tofu.json", host, infos)
		}
	}
}

func TestRequirementsInRegistryReadsTofuFiles(t *testing.T) {
	dir := t.TempDir()
	for name, content := range map[string]string{
		"main.tofu": tofuRequirement("~> 5.0"),
		"main.tf":   tofuRequirement("~> 4.0"),
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	tofu := RequirementsInRegistry(dir, model.OpenTofuRegistryHost)
	if len(tofu) != 1 || tofu[0].Constraint != "~> 5.0" {
		t.Errorf("OpenTofu requirements = %+v, want main.tofu's ~> 5.0 only", tofu)
	}
	tf := RequirementsIn(dir)
	if len(tf) != 1 || tf[0].Constraint != "~> 4.0" {
		t.Errorf("Terraform requirements = %+v, want main.tf's ~> 4.0 only", tf)
	}
}
