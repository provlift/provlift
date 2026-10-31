package drift

import (
	"testing"

	"github.com/provlift/provlift/internal/model"
)

func addr(t *testing.T, source string) model.Address {
	t.Helper()
	a, err := model.ParseAddress(source)
	if err != nil {
		t.Fatalf("ParseAddress(%q): %v", source, err)
	}
	return a
}

type moduleSpec struct {
	relDir      string
	isRoot      bool
	hasLock     bool
	constraints map[string]string // source -> constraint
	locks       map[string]string // source -> locked version
}

func inventory(t *testing.T, specs ...moduleSpec) *model.Inventory {
	t.Helper()

	inv := &model.Inventory{Root: "/repo"}
	for _, spec := range specs {
		mod := &model.Module{
			Dir:         "/repo/" + spec.relDir,
			RelDir:      spec.relDir,
			IsRoot:      spec.isRoot,
			HasLockFile: spec.hasLock,
			Locks:       map[string]model.ProviderLock{},
		}

		for source, constraint := range spec.constraints {
			a := addr(t, source)
			mod.Requirements = append(mod.Requirements, model.ProviderRequirement{
				LocalName:  a.Type,
				Address:    a,
				Source:     a.String(),
				Constraint: constraint,
			})
		}
		for source, v := range spec.locks {
			a := addr(t, source)
			mod.Locks[a.String()] = model.ProviderLock{Address: a, Source: a.String(), Version: v}
		}

		inv.Modules = append(inv.Modules, mod)
	}
	return inv
}

// realWorldInventory mirrors a common real-world shape, where the same
// provider is locked at three different versions across supposedly equivalent
// environments.
func realWorldInventory(t *testing.T) *model.Inventory {
	t.Helper()

	return inventory(t,
		moduleSpec{
			relDir: "jumphost/envs/westeurope-prod", isRoot: true, hasLock: true,
			constraints: map[string]string{"hashicorp/azurerm": ">= 3.85.0"},
			locks:       map[string]string{"hashicorp/azurerm": "3.117.1"},
		},
		moduleSpec{
			relDir: "jumphost/envs/westeurope-prod-secondary", isRoot: true, hasLock: true,
			constraints: map[string]string{"hashicorp/azurerm": ">= 3.85.0"},
			locks:       map[string]string{"hashicorp/azurerm": "4.81.0"},
		},
		moduleSpec{
			relDir: "jumphost/envs/westus2-prod", isRoot: true, hasLock: true,
			constraints: map[string]string{"hashicorp/azurerm": ">= 3.85.0"},
			locks:       map[string]string{"hashicorp/azurerm": "4.81.0"},
		},
	)
}

func TestVersionSkewIsDetected(t *testing.T) {
	report := Analyze(realWorldInventory(t), Options{})

	if len(report.Skew) != 1 {
		t.Fatalf("got %d skew findings, want 1", len(report.Skew))
	}

	skew := report.Skew[0]
	if skew.Oldest != "3.117.1" || skew.Newest != "4.81.0" {
		t.Errorf("span = %s..%s, want 3.117.1..4.81.0", skew.Oldest, skew.Newest)
	}
	// A split across a major boundary is materially worse than a patch drift.
	if skew.Span != "major" {
		t.Errorf("Span = %q, want major", skew.Span)
	}

	if len(skew.Uses) != 2 {
		t.Fatalf("got %d distinct versions, want 2", len(skew.Uses))
	}
	if skew.Uses[0].Version != "3.117.1" {
		t.Errorf("versions are not ordered oldest first: %+v", skew.Uses)
	}
	if len(skew.Uses[1].Modules) != 2 {
		t.Errorf("4.81.0 should be shared by 2 modules, got %v", skew.Uses[1].Modules)
	}
}

func TestNoSkewWhenVersionsAgree(t *testing.T) {
	inv := inventory(t,
		moduleSpec{relDir: "a", isRoot: true, hasLock: true,
			locks: map[string]string{"hashicorp/azurerm": "4.81.0"}},
		moduleSpec{relDir: "b", isRoot: true, hasLock: true,
			locks: map[string]string{"hashicorp/azurerm": "4.81.0"}},
	)

	if got := Analyze(inv, Options{}).Skew; len(got) != 0 {
		t.Errorf("Skew = %+v, want none", got)
	}
}

func TestSkewSpanClassification(t *testing.T) {
	tests := []struct {
		name     string
		versions []string
		want     string
	}{
		{"patch spread", []string{"4.81.0", "4.81.3"}, "patch"},
		{"minor spread", []string{"4.65.0", "4.81.0"}, "minor"},
		{"major spread", []string{"3.117.1", "5.6.0"}, "major"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			inv := inventory(t,
				moduleSpec{relDir: "a", isRoot: true, hasLock: true,
					locks: map[string]string{"hashicorp/azurerm": tc.versions[0]}},
				moduleSpec{relDir: "b", isRoot: true, hasLock: true,
					locks: map[string]string{"hashicorp/azurerm": tc.versions[1]}},
			)

			skew := Analyze(inv, Options{}).Skew
			if len(skew) != 1 {
				t.Fatalf("got %d skew findings, want 1", len(skew))
			}
			if skew[0].Span != tc.want {
				t.Errorf("Span = %q, want %q", skew[0].Span, tc.want)
			}
		})
	}
}

func TestModulesWithoutLockFilesAreExcludedFromSkew(t *testing.T) {
	// A module with no lock file has selected nothing, so it cannot disagree.
	inv := inventory(t,
		moduleSpec{relDir: "a", isRoot: true, hasLock: true,
			locks: map[string]string{"hashicorp/azurerm": "4.81.0"}},
		moduleSpec{relDir: "b", isRoot: true, hasLock: false,
			constraints: map[string]string{"hashicorp/azurerm": ">= 3.0"}},
	)

	if got := Analyze(inv, Options{}).Skew; len(got) != 0 {
		t.Errorf("Skew = %+v, want none", got)
	}
}

func TestConstraintDivergenceIsDetected(t *testing.T) {
	inv := inventory(t,
		moduleSpec{relDir: "a", isRoot: true,
			constraints: map[string]string{"hashicorp/azurerm": ">= 3.85.0"}},
		moduleSpec{relDir: "b", isRoot: true,
			constraints: map[string]string{"hashicorp/azurerm": "~> 4.65"}},
		moduleSpec{relDir: "c", isRoot: true,
			constraints: map[string]string{"hashicorp/azurerm": ">= 3.47.0, < 4.0.0"}},
	)

	report := Analyze(inv, Options{})
	if len(report.Divergence) != 1 {
		t.Fatalf("got %d divergence findings, want 1", len(report.Divergence))
	}
	if len(report.Divergence[0].Uses) != 3 {
		t.Errorf("got %d distinct constraints, want 3", len(report.Divergence[0].Uses))
	}
}

func TestCosmeticConstraintDifferencesAreNotDivergence(t *testing.T) {
	// ">=4.16.0" and ">= 4.16.0" mean the same thing and must not be reported.
	inv := inventory(t,
		moduleSpec{relDir: "a", isRoot: true,
			constraints: map[string]string{"hashicorp/azurerm": ">=4.16.0"}},
		moduleSpec{relDir: "b", isRoot: true,
			constraints: map[string]string{"hashicorp/azurerm": ">= 4.16.0"}},
		moduleSpec{relDir: "c", isRoot: true,
			constraints: map[string]string{"hashicorp/azurerm": ">=  4.16.0  "}},
	)

	if got := Analyze(inv, Options{}).Divergence; len(got) != 0 {
		t.Errorf("Divergence = %+v, want none", got)
	}
}

func TestMissingConstraintIsReportedAsDivergence(t *testing.T) {
	inv := inventory(t,
		moduleSpec{relDir: "a", isRoot: true,
			constraints: map[string]string{"hashicorp/azurerm": "~> 4.65"}},
		moduleSpec{relDir: "b", isRoot: true,
			constraints: map[string]string{"hashicorp/azurerm": ""}},
	)

	report := Analyze(inv, Options{})
	if len(report.Divergence) != 1 {
		t.Fatalf("got %d divergence findings, want 1", len(report.Divergence))
	}

	var sawNone bool
	for _, use := range report.Divergence[0].Uses {
		if use.Constraint == "(none)" {
			sawNone = true
		}
	}
	if !sawNone {
		t.Errorf("an absent constraint should be visible: %+v", report.Divergence[0].Uses)
	}
}

func TestUnpinnedRootModules(t *testing.T) {
	inv := inventory(t,
		moduleSpec{relDir: "pinned", isRoot: true, hasLock: true,
			constraints: map[string]string{"hashicorp/azurerm": "~> 4.65"},
			locks:       map[string]string{"hashicorp/azurerm": "4.65.0"}},
		moduleSpec{relDir: "unpinned", isRoot: true,
			constraints: map[string]string{"hashicorp/azurerm": ">= 3.85.0"}},
	)

	report := Analyze(inv, Options{})
	if len(report.Unpinned) != 1 {
		t.Fatalf("got %d unpinned findings, want 1", len(report.Unpinned))
	}
	if report.Unpinned[0].Module != "unpinned" {
		t.Errorf("Module = %q, want unpinned", report.Unpinned[0].Module)
	}
	if len(report.Unpinned[0].Providers) != 1 || report.Unpinned[0].Providers[0] != "hashicorp/azurerm" {
		t.Errorf("Providers = %v", report.Unpinned[0].Providers)
	}
}

func TestChildModulesAreNotReportedAsUnpinned(t *testing.T) {
	// Shared modules deliberately commit no lock file; that is correct, not a
	// finding.
	inv := inventory(t,
		moduleSpec{relDir: "modules/network", isRoot: false,
			constraints: map[string]string{"hashicorp/azurerm": ">= 3.85.0"}},
	)

	if got := Analyze(inv, Options{}).Unpinned; len(got) != 0 {
		t.Errorf("Unpinned = %+v, want none", got)
	}
}

func TestSelectorFiltersFindings(t *testing.T) {
	inv := inventory(t,
		moduleSpec{relDir: "a", isRoot: true, hasLock: true,
			locks: map[string]string{"hashicorp/azurerm": "4.65.0", "integrations/github": "6.2.1"}},
		moduleSpec{relDir: "b", isRoot: true, hasLock: true,
			locks: map[string]string{"hashicorp/azurerm": "5.6.0", "integrations/github": "6.13.0"}},
	)

	all := Analyze(inv, Options{})
	if len(all.Skew) != 2 {
		t.Fatalf("got %d skew findings, want 2", len(all.Skew))
	}

	filtered := Analyze(inv, Options{Selectors: []string{"azurerm"}})
	if len(filtered.Skew) != 1 {
		t.Fatalf("got %d skew findings, want 1", len(filtered.Skew))
	}
	if filtered.Skew[0].Address.Type != "azurerm" {
		t.Errorf("wrong provider retained: %s", filtered.Skew[0].Source)
	}
}

func TestEmptyInventory(t *testing.T) {
	report := Analyze(&model.Inventory{}, Options{})

	if report.HasFindings() {
		t.Errorf("empty inventory produced findings: %+v", report)
	}
	if report.Count() != 0 {
		t.Errorf("Count() = %d, want 0", report.Count())
	}
}

func TestNormalizeConstraint(t *testing.T) {
	tests := []struct {
		in   string
		want string
	}{
		{">=4.16.0", ">= 4.16.0"},
		{">= 4.16.0", ">= 4.16.0"},
		{"  ~>1.2.31  ", "~> 1.2.31"},
		{">=3.47.0,<4.0.0", ">= 3.47.0, < 4.0.0"},
		{">= 3.47.0, < 4.0.0", ">= 3.47.0, < 4.0.0"},
		{"3.4.0", "3.4.0"},
		{"", ""},
	}

	for _, tc := range tests {
		t.Run(tc.in, func(t *testing.T) {
			if got := NormalizeConstraint(tc.in); got != tc.want {
				t.Errorf("NormalizeConstraint(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}
