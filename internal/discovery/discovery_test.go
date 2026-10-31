package discovery

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/provlift/provlift/internal/model"
)

const fixtureRoot = "testdata/repo"

func scanFixture(t *testing.T, excludes ...string) *model.Inventory {
	t.Helper()
	inv, err := Scan(Options{Root: fixtureRoot, ExcludeGlobs: excludes})
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	return inv
}

func moduleByRelDir(inv *model.Inventory, rel string) *model.Module {
	for _, m := range inv.Modules {
		if m.RelDir == rel {
			return m
		}
	}
	return nil
}

func scanFiles(t *testing.T, files map[string]string, excludes ...string) *model.Inventory {
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
	inv, err := Scan(Options{Root: root, ExcludeGlobs: excludes})
	if err != nil {
		t.Fatal(err)
	}
	return inv
}

func TestScanPrunesReservedDirectoriesAtEveryDepth(t *testing.T) {
	files := map[string]string{"main.tf": "terraform {}\n", "visible/main.tf": "terraform {}\n"}
	for _, prefix := range []string{"", "visible/nested/"} {
		for _, directory := range []string{".terraform", ".git", "node_modules"} {
			files[prefix+directory+"/deep/broken.tf"] = "terraform {"
		}
	}
	inv := scanFiles(t, files)
	if len(inv.Modules) != 2 || len(inv.Diagnostics) != 0 {
		t.Fatalf("reserved directories were scanned: modules=%+v diagnostics=%+v", inv.Modules, inv.Diagnostics)
	}
}

func TestScanRecursiveExcludePrunesBeforeParsing(t *testing.T) {
	inv := scanFiles(t, map[string]string{
		"main.tf":                            "terraform {}\n",
		"envs/dev/modules/network/broken.tf": "terraform {",
		"envs/dev/modules-other/main.tf":     "terraform {}\n",
	}, "**/modules/**")
	if len(inv.Modules) != 2 || len(inv.Diagnostics) != 0 || moduleByRelDir(inv, "envs/dev/modules-other") == nil {
		t.Fatalf("recursive exclusion failed to prune precisely: %+v", inv)
	}
}

func TestScanClassifiesCrossReferencedNestedChildren(t *testing.T) {
	inv := scanFiles(t, map[string]string{
		"envs/a/main.tf":              "module \"network\" {\n  source = \"../../shared/network\"\n}\n",
		"envs/b/main.tf":              "module \"network\" {\n  source = \"../../shared/network\"\n}\n",
		"shared/network/main.tf":      "module \"common\" {\n  source = \"../common\"\n}\n",
		"shared/common/main.tf":       "terraform {}\n",
		"shared/unreferenced/main.tf": "terraform {}\n",
	})
	for name, isRoot := range map[string]bool{"envs/a": true, "envs/b": true, "shared/network": false, "shared/common": false, "shared/unreferenced": true} {
		module := moduleByRelDir(inv, name)
		if module == nil || module.IsRoot != isRoot {
			t.Errorf("module %s classification = %+v, want root=%t", name, module, isRoot)
		}
	}
	common := moduleByRelDir(inv, "shared/common")
	if common == nil {
		t.Fatal("nested child missing")
	}
	roots := inv.AffectedRoots([]string{common.Dir})
	if len(roots) != 2 || roots[0].RelDir != "envs/a" || roots[1].RelDir != "envs/b" {
		t.Fatalf("nested shared child must refresh both consuming roots: %+v", roots)
	}
}

func TestScanAggregatesFilesAndLegacyGitHubShorthand(t *testing.T) {
	inv := scanFiles(t, map[string]string{
		"github.tf": "terraform {\n  required_providers {\n    github = \"~> 6.0\"\n  }\n}\n",
		"azure.tf":  "terraform {\n  required_providers {\n    azurerm = {\n      source = \"hashicorp/azurerm\"\n      version = \">= 4.0\"\n    }\n  }\n}\n",
	})
	if len(inv.Modules) != 1 || len(inv.Modules[0].Requirements) != 2 || len(inv.Diagnostics) != 0 {
		t.Fatalf("multi-file module did not aggregate: %+v", inv)
	}
	for _, requirement := range inv.Modules[0].Requirements {
		if requirement.LocalName == "github" {
			if requirement.Constraint != "~> 6.0" || !requirement.ImpliedSource || requirement.IsObject {
				t.Errorf("legacy GitHub shorthand parsed incorrectly: %+v", requirement)
			}
			return
		}
	}
	t.Fatal("GitHub shorthand requirement missing")
}

func TestScanReportsJSONAndParseErrorsWithoutAborting(t *testing.T) {
	inv := scanFiles(t, map[string]string{
		"main.tf":             "terraform {\n  required_providers {\n    github = \"~> 6.0\"\n  }\n}\n",
		"broken.tf":           "terraform {",
		"extra.tf.json":       "{\"terraform\": {}}",
		"valid-child/main.tf": "terraform {}\n",
	})
	if len(inv.Modules) != 2 || moduleByRelDir(inv, ".") == nil || len(moduleByRelDir(inv, ".").Requirements) != 1 {
		t.Fatalf("bad file prevented valid configuration discovery: %+v", inv)
	}
	reported := map[string]bool{}
	for _, diagnostic := range inv.Diagnostics {
		if diagnostic.Summary != "" {
			reported[diagnostic.Pos.File] = true
		}
	}
	if !reported["broken.tf"] || !reported["extra.tf.json"] {
		t.Fatalf("missing file diagnostics: %+v", inv.Diagnostics)
	}
}

func TestScanReportsNestedFilesWithSlashSeparatedPaths(t *testing.T) {
	// A directory holding only JSON configuration is not a module, but its
	// file must still be reported rather than skipped with the directory.
	inv := scanFiles(t, map[string]string{
		"main.tf":                   "terraform {}\n",
		"json-only/main.tf.json":    "{\"terraform\": {}}",
		"envs/dev/broken.tf":        "terraform {",
		"envs/dev/main.tf":          "terraform {}\n",
		"envs/dev/override.tf.json": "{}",
	})
	reported := map[string]bool{}
	for _, diagnostic := range inv.Diagnostics {
		reported[diagnostic.Pos.File] = true
	}
	for _, file := range []string{"json-only/main.tf.json", "envs/dev/broken.tf", "envs/dev/override.tf.json"} {
		if !reported[file] {
			t.Errorf("no diagnostic for %s: %+v", file, inv.Diagnostics)
		}
	}
	if moduleByRelDir(inv, "json-only") != nil || moduleByRelDir(inv, "envs/dev") == nil {
		t.Fatalf("unexpected modules: %+v", inv.Modules)
	}
}

func TestScanFindsModules(t *testing.T) {
	inv := scanFixture(t)

	want := []string{".", "archive/old", "modules/network", "standalone"}
	for _, rel := range want {
		if moduleByRelDir(inv, rel) == nil {
			t.Errorf("module %q was not discovered", rel)
		}
	}
}

func TestScanSkipsTerraformCacheDirectory(t *testing.T) {
	inv := scanFixture(t)

	// .terraform holds Terraform's copies of child modules. Treating those as
	// first-class configuration would report providers this repo does not own.
	for _, m := range inv.Modules {
		if filepath.ToSlash(m.RelDir) == ".terraform/modules/cached" {
			t.Fatal("scan descended into .terraform")
		}
	}
}

func TestScanHonoursExcludeGlobs(t *testing.T) {
	inv := scanFixture(t, "archive")

	if moduleByRelDir(inv, "archive/old") != nil {
		t.Error("excluded directory was still scanned")
	}
	if moduleByRelDir(inv, ".") == nil {
		t.Error("exclude removed unrelated modules")
	}
}

func TestShouldSkipDirRecursivePatterns(t *testing.T) {
	root := t.TempDir()
	for _, scenario := range []struct {
		pattern string
		path    string
		want    bool
	}{
		{"modules/**", "modules", true},
		{"modules/**", "modules/network/nested", true},
		{"modules/**", "modules-other/network", false},
		{"**/modules/**", "modules", true},
		{"**/modules/**", "envs/dev/modules", true},
		{"**/modules/**", "envs/dev/modules/network", true},
		{"**/modules/**", "envs/dev/modules-other", false},
		{"modules/*/legacy/**", "modules/team/legacy", true},
		{"modules/*/legacy/**", "modules/team/legacy/nested", true},
		{"modules/*/legacy/**", "modules/team/current", false},
		{"modules/**/legacy/**", "modules/legacy", true},
		{"modules/**/legacy/**", "modules/team/legacy/nested", true},
		{"modules/**/legacy/**", "modules/team/current", false},
		{"modules/[ab]*/legacy/**", "modules/alpha/legacy", true},
		{"modules/[ab]*/legacy/**", "modules/charlie/legacy", false},
	} {
		t.Run(scenario.pattern+"/"+scenario.path, func(t *testing.T) {
			path := filepath.Join(root, filepath.FromSlash(scenario.path))
			if got := ShouldSkipDir(root, path, []string{scenario.pattern}); got != scenario.want {
				t.Errorf("ShouldSkipDir(%q, %q) = %t, want %t", scenario.path, scenario.pattern, got, scenario.want)
			}
		})
	}
}

func TestScanDistinguishesRootFromChildModules(t *testing.T) {
	inv := scanFixture(t)

	root := moduleByRelDir(inv, ".")
	if root == nil || !root.IsRoot {
		t.Fatalf("expected %q to be a root module", ".")
	}

	child := moduleByRelDir(inv, "modules/network")
	if child == nil {
		t.Fatal("child module was not discovered")
	}
	if child.IsRoot {
		t.Error("modules/network is referenced by a local module block, so it is not a root")
	}

	standalone := moduleByRelDir(inv, "standalone")
	if standalone == nil || !standalone.IsRoot {
		t.Error("standalone is not referenced by anything, so it is a root")
	}
}

func TestScanReadsProviderRequirements(t *testing.T) {
	inv := scanFixture(t)
	root := moduleByRelDir(inv, ".")

	tests := []struct {
		localName  string
		source     string
		constraint string
	}{
		{"github", "registry.terraform.io/integrations/github", "~> 6.0"},
		{"azurerm", "registry.terraform.io/hashicorp/azurerm", ">= 3.0, < 4.0"},
	}

	for _, tc := range tests {
		t.Run(tc.localName, func(t *testing.T) {
			var found *model.ProviderRequirement
			for i := range root.Requirements {
				if root.Requirements[i].LocalName == tc.localName {
					found = &root.Requirements[i]
					break
				}
			}
			if found == nil {
				t.Fatalf("requirement %q not found", tc.localName)
			}
			if found.Source != tc.source {
				t.Errorf("Source = %q, want %q", found.Source, tc.source)
			}
			if found.Constraint != tc.constraint {
				t.Errorf("Constraint = %q, want %q", found.Constraint, tc.constraint)
			}
			if found.VersionRange == nil {
				t.Error("VersionRange must be recorded so the constraint can be edited")
			}
			if found.ConstraintPos.Line == 0 {
				t.Error("ConstraintPos.Line must be recorded for reporting")
			}
		})
	}
}

func TestScanReadsRequiredTerraformVersion(t *testing.T) {
	inv := scanFixture(t)
	root := moduleByRelDir(inv, ".")

	if root.RequiredVersion != ">= 1.5.0" {
		t.Errorf("RequiredVersion = %q, want \">= 1.5.0\"", root.RequiredVersion)
	}
}

func TestScanHandlesLegacyShorthandAndMissingVersion(t *testing.T) {
	inv := scanFixture(t)
	mod := moduleByRelDir(inv, "standalone")

	byName := map[string]model.ProviderRequirement{}
	for _, r := range mod.Requirements {
		byName[r.LocalName] = r
	}

	random, ok := byName["random"]
	if !ok {
		t.Fatal("legacy shorthand requirement was not parsed")
	}
	if random.Source != "registry.terraform.io/hashicorp/random" {
		t.Errorf("implied source = %q, want registry.terraform.io/hashicorp/random", random.Source)
	}
	if !random.ImpliedSource {
		t.Error("ImpliedSource should be true when no source argument is present")
	}
	if random.Constraint != "~> 3.0" {
		t.Errorf("Constraint = %q, want \"~> 3.0\"", random.Constraint)
	}

	atlas, ok := byName["mongodbatlas"]
	if !ok {
		t.Fatal("object requirement without a version was not parsed")
	}
	if atlas.Constraint != "" {
		t.Errorf("Constraint = %q, want empty", atlas.Constraint)
	}
	if atlas.VersionRange != nil {
		t.Error("VersionRange must be nil when no version argument exists")
	}
	if atlas.InsertPoint == 0 {
		t.Error("InsertPoint must be recorded so a version argument can be added")
	}
}

func TestScanReadsLockFile(t *testing.T) {
	inv := scanFixture(t)
	root := moduleByRelDir(inv, ".")

	if !root.HasLockFile {
		t.Fatal("expected the root module to have a lock file")
	}

	github, _ := model.ParseAddress("integrations/github")
	lock, ok := root.Lock(github)
	if !ok {
		t.Fatal("github lock entry not found")
	}
	if lock.Version != "6.2.1" {
		t.Errorf("locked version = %q, want 6.2.1", lock.Version)
	}
}

func TestAffectedRootsResolvesChildModules(t *testing.T) {
	inv := scanFixture(t)
	child := moduleByRelDir(inv, "modules/network")

	roots := inv.AffectedRoots([]string{child.Dir})
	if len(roots) != 1 || roots[0].RelDir != "." {
		t.Fatalf("AffectedRoots = %v, want the repository root", roots)
	}
}

func TestDistinctProviders(t *testing.T) {
	inv := scanFixture(t, "archive")

	got := map[string]bool{}
	for _, a := range inv.DistinctProviders() {
		got[a.String()] = true
	}

	for _, want := range []string{
		"registry.terraform.io/integrations/github",
		"registry.terraform.io/hashicorp/azurerm",
		"registry.terraform.io/hashicorp/random",
		"registry.terraform.io/mongodb/mongodbatlas",
	} {
		if !got[want] {
			t.Errorf("provider %q missing from DistinctProviders", want)
		}
	}
}
