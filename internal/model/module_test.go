package model

import "testing"

func module(dir string, isRoot bool, children ...string) *Module {
	return &Module{
		Dir:      dir,
		RelDir:   dir,
		IsRoot:   isRoot,
		Children: children,
	}
}

func relDirs(mods []*Module) []string {
	out := make([]string, 0, len(mods))
	for _, m := range mods {
		out = append(out, m.RelDir)
	}
	return out
}

func contains(list []string, want string) bool {
	for _, s := range list {
		if s == want {
			return true
		}
	}
	return false
}

func TestRootModules(t *testing.T) {
	inv := &Inventory{Modules: []*Module{
		module("envs/prod", true),
		module("modules/network", false),
		module("envs/dev", true),
	}}

	got := relDirs(inv.RootModules())
	if len(got) != 2 {
		t.Fatalf("RootModules() = %v, want 2 entries", got)
	}
}

func TestAffectedRootsDirectRoot(t *testing.T) {
	inv := &Inventory{Modules: []*Module{
		module("envs/prod", true),
		module("envs/dev", true),
	}}

	got := relDirs(inv.AffectedRoots([]string{"envs/prod"}))
	if len(got) != 1 || got[0] != "envs/prod" {
		t.Errorf("AffectedRoots() = %v, want [envs/prod]", got)
	}
}

func TestAffectedRootsResolvesThroughChildren(t *testing.T) {
	// A change in a nested child must re-initialise the root that calls it,
	// because the root owns the lock file.
	inv := &Inventory{Modules: []*Module{
		module("envs/prod", true, "modules/network"),
		module("modules/network", false, "modules/subnet"),
		module("modules/subnet", false),
	}}

	got := relDirs(inv.AffectedRoots([]string{"modules/subnet"}))
	if len(got) != 1 || got[0] != "envs/prod" {
		t.Errorf("AffectedRoots() = %v, want [envs/prod]", got)
	}
}

func TestAffectedRootsFansOutToEveryConsumer(t *testing.T) {
	// A shared module is used by several roots; all of them need re-init.
	inv := &Inventory{Modules: []*Module{
		module("envs/prod", true, "modules/shared"),
		module("envs/dev", true, "modules/shared"),
		module("envs/unrelated", true),
		module("modules/shared", false),
	}}

	got := relDirs(inv.AffectedRoots([]string{"modules/shared"}))
	if len(got) != 2 {
		t.Fatalf("AffectedRoots() = %v, want 2 roots", got)
	}
	for _, want := range []string{"envs/prod", "envs/dev"} {
		if !contains(got, want) {
			t.Errorf("AffectedRoots() = %v, missing %s", got, want)
		}
	}
	if contains(got, "envs/unrelated") {
		t.Errorf("AffectedRoots() = %v, must not include an unrelated root", got)
	}
}

func TestAffectedRootsTerminatesOnCycles(t *testing.T) {
	// Terraform rejects module cycles, but a malformed repository must not
	// cause the scan to recurse forever.
	inv := &Inventory{Modules: []*Module{
		module("envs/prod", true, "modules/a"),
		module("modules/a", false, "modules/b"),
		module("modules/b", false, "modules/a"),
	}}

	done := make(chan []string, 1)
	go func() { done <- relDirs(inv.AffectedRoots([]string{"modules/b"})) }()

	got := <-done
	if len(got) != 1 || got[0] != "envs/prod" {
		t.Errorf("AffectedRoots() = %v, want [envs/prod]", got)
	}
}

func TestAffectedRootsUnknownDirectory(t *testing.T) {
	inv := &Inventory{Modules: []*Module{module("envs/prod", true)}}

	if got := inv.AffectedRoots([]string{"does/not/exist"}); len(got) != 0 {
		t.Errorf("AffectedRoots() = %v, want empty", relDirs(got))
	}
}

func TestDistinctProvidersDeduplicatesAndSorts(t *testing.T) {
	azurerm, err := ParseAddress("hashicorp/azurerm")
	if err != nil {
		t.Fatal(err)
	}
	github, err := ParseAddress("integrations/github")
	if err != nil {
		t.Fatal(err)
	}

	inv := &Inventory{Modules: []*Module{
		{
			RelDir:       "a",
			Requirements: []ProviderRequirement{{Address: azurerm}},
			Locks:        map[string]ProviderLock{github.String(): {Address: github}},
		},
		{
			RelDir:       "b",
			Requirements: []ProviderRequirement{{Address: azurerm}, {Address: github}},
		},
	}}

	got := inv.DistinctProviders()
	if len(got) != 2 {
		t.Fatalf("DistinctProviders() = %v, want 2", got)
	}
	// Sorted by full address: hashicorp sorts before integrations.
	if got[0].String() != azurerm.String() || got[1].String() != github.String() {
		t.Errorf("DistinctProviders() = [%s %s], want sorted order", got[0], got[1])
	}
}

func TestModuleLookups(t *testing.T) {
	azurerm, err := ParseAddress("hashicorp/azurerm")
	if err != nil {
		t.Fatal(err)
	}
	github, err := ParseAddress("integrations/github")
	if err != nil {
		t.Fatal(err)
	}

	mod := &Module{
		Requirements: []ProviderRequirement{{LocalName: "azurerm", Address: azurerm}},
		Locks: map[string]ProviderLock{
			azurerm.String(): {Address: azurerm, Version: "4.65.0"},
		},
	}

	t.Run("lock found", func(t *testing.T) {
		lock, ok := mod.Lock(azurerm)
		if !ok || lock.Version != "4.65.0" {
			t.Errorf("Lock() = %+v, %v", lock, ok)
		}
	})

	t.Run("lock missing", func(t *testing.T) {
		if _, ok := mod.Lock(github); ok {
			t.Error("Lock() reported a provider that is not locked")
		}
	})

	t.Run("requirement found", func(t *testing.T) {
		req, ok := mod.Requirement(azurerm)
		if !ok || req.LocalName != "azurerm" {
			t.Errorf("Requirement() = %+v, %v", req, ok)
		}
	})

	t.Run("requirement missing", func(t *testing.T) {
		if _, ok := mod.Requirement(github); ok {
			t.Error("Requirement() reported a provider that is not declared")
		}
	})
}
