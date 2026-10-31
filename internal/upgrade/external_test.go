package upgrade

import (
	"strings"
	"testing"

	version "github.com/hashicorp/go-version"

	"github.com/provlift/provlift/internal/model"
)

func TestExternalConstraint(t *testing.T) {
	tests := []struct {
		name   string
		merged string
		local  string
		want   string
	}{
		{
			// The shape observed in a real repository: the root declares one
			// floor, three shared modules add a ceiling nothing local mentions.
			name:   "dependency ceiling is recovered",
			merged: ">= 3.85.0, >= 4.11.0, <= 4.19.0",
			local:  ">= 3.85.0",
			want:   ">= 4.11.0, <= 4.19.0",
		},
		{
			name:   "nothing external",
			merged: ">= 3.85.0",
			local:  ">= 3.85.0",
			want:   "",
		},
		{
			name:   "cosmetic spacing is not treated as external",
			merged: ">=3.85.0, <= 4.19.0",
			local:  ">= 3.85.0",
			want:   "<= 4.19.0",
		},
		{
			name:   "no lock constraints recorded",
			merged: "",
			local:  ">= 3.85.0",
			want:   "",
		},
		{
			name:   "local declares nothing",
			merged: "<= 4.19.0",
			local:  "",
			want:   "<= 4.19.0",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := ExternalConstraint(tc.merged, tc.local); got != tc.want {
				t.Errorf("ExternalConstraint(%q, %q) = %q, want %q",
					tc.merged, tc.local, got, tc.want)
			}
		})
	}
}

func TestExternalConstraintIgnoresLockFileSpelling(t *testing.T) {
	// Terraform rewrites every constraint it records in the lock file into a
	// fixed spelling. Each of these is the repository's own constraint, so
	// nothing is left over to attribute to a dependency.
	tests := []struct {
		name     string
		lockForm string
		declared string
	}{
		{"range padded to three segments", ">= 4.65.0, < 5.0.0", ">= 4.65, < 5.0"},
		{"lower bound padded", ">= 1.9.0", ">= 1.9"},
		{"one-segment lower bound padded", ">= 4.0.0", ">= 4"},
		{"one-segment pessimistic widened", "~> 1.0", "~> 1"},
		{"two-segment pessimistic kept", "~> 6.0", "~> 6.0"},
		{"three-segment pessimistic kept", "~> 1.2.0", "~> 1.2.0"},
		{"bare pin", "6.0.1", "6.0.1"},
		{"explicit equals recorded bare", "6.0.1", "= 6.0.1"},
		{"short pin padded", "3.1.0", "3.1"},
		{"strict bounds padded", "> 2.0.0, <= 3.0.0", "> 2, <= 3"},
		{"exclusion padded", ">= 1.0.0, != 1.5.0", ">= 1.0, != 1.5"},
		{"prerelease pin", "5.0.0-beta.1", "= 5.0.0-beta.1"},
		{"terms reordered", ">= 4.65.0, < 5.0.0", "< 5.0, >= 4.65"},
		{"spacing removed", ">= 4.65.0, < 5.0.0", ">=4.65,<5.0"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := ExternalConstraint(tc.lockForm, tc.declared); got != "" {
				t.Errorf("ExternalConstraint(%q, %q) = %q, want nothing external",
					tc.lockForm, tc.declared, got)
			}
		})
	}
}

func TestExternalConstraintStillFindsDependencyTerms(t *testing.T) {
	tests := []struct {
		name   string
		merged string
		local  string
		want   string
	}{
		{"ceiling beside a padded floor", ">= 4.65.0, < 4.70.0", ">= 4.65", "< 4.70.0"},
		{"second floor beside a padded floor", ">= 4.65.0, >= 4.70.0", ">= 4.65", ">= 4.70.0"},
		{"finer pessimistic is a different term", "~> 1.2.0, ~> 1.2", "~> 1.2", "~> 1.2.0"},
		{"coarser pessimistic is a different term", "~> 1.2", "~> 1.2.0", "~> 1.2"},
		{"different pin", "6.0.2", "= 6.0.1", "= 6.0.2"},
		{"different operator on the same version", "> 4.65.0", ">= 4.65", "> 4.65.0"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := ExternalConstraint(tc.merged, tc.local); got != tc.want {
				t.Errorf("ExternalConstraint(%q, %q) = %q, want %q",
					tc.merged, tc.local, got, tc.want)
			}
		})
	}
}

func TestBuildStatusAttributesNormalisedLockConstraintToTheRepository(t *testing.T) {
	// The lock file records ">= 3.85, < 5.0" as ">= 3.85.0, < 5.0.0". It is
	// the repository's own range, not a dependency's, that forbids 5.6.0.
	_, st, _ := lockedModule(t, ">= 3.85, < 5.0", "4.81.0", ">= 3.85.0, < 5.0.0")

	if st.ExternalConstraint != "" {
		t.Errorf("ExternalConstraint = %q, want none", st.ExternalConstraint)
	}
	if st.BlockedByDependency {
		t.Error("BlockedByDependency should be false: no dependency constrains this provider")
	}
	if !st.Blocked {
		t.Error("Blocked should be true: the repository's own range forbids 5.6.0")
	}
}

func TestFilterSatisfying(t *testing.T) {
	candidates := versions(t, "3.117.1", "4.11.0", "4.19.0", "4.81.0", "5.6.0")

	got := FilterSatisfying(candidates, ">= 4.11.0, <= 4.19.0")
	if len(got) != 2 || got[0].String() != "4.11.0" || got[1].String() != "4.19.0" {
		t.Errorf("FilterSatisfying() = %v, want [4.11.0 4.19.0]", got)
	}

	if all := FilterSatisfying(candidates, ""); len(all) != len(candidates) {
		t.Errorf("an empty constraint must permit everything, got %v", all)
	}
}

// lockedModule builds a root module whose lock file records merged constraints
// from the whole module tree, as Terraform writes it.
func lockedModule(t *testing.T, localConstraint, lockedVersion, mergedConstraints string) (*model.Module, *Status, []*version.Version) {
	t.Helper()

	mod, req := buildModule(t, "examples/single", testCase{
		source:     "hashicorp/azurerm",
		localName:  "azurerm",
		constraint: localConstraint,
		locked:     lockedVersion,
	})
	lock := mod.Locks[req.Address.String()]
	lock.Constraints = mergedConstraints
	mod.Locks[req.Address.String()] = lock

	candidates := versions(t, "3.117.1", "4.11.0", "4.19.0", "4.81.0", "5.6.0")
	return mod, BuildStatus(mod, req, candidates, false), candidates
}

func TestBuildStatusDetectsDependencyCeiling(t *testing.T) {
	_, st, _ := lockedModule(t, ">= 3.85.0", "4.19.0", ">= 3.85.0, >= 4.11.0, <= 4.19.0")

	if st.ExternalConstraint != ">= 4.11.0, <= 4.19.0" {
		t.Errorf("ExternalConstraint = %q", st.ExternalConstraint)
	}
	if !st.BlockedByDependency {
		t.Error("5.6.0 is forbidden by the dependency, so BlockedByDependency should be true")
	}
	// The repository's own constraint allows 5.6.0, so it is not the blocker.
	if st.Blocked {
		t.Error("Blocked should be false: the local constraint permits the newest version")
	}
	if st.LatestReachable != "4.19.0" {
		t.Errorf("LatestReachable = %q, want 4.19.0", st.LatestReachable)
	}
}

func TestPlanActionNeverTargetsAVersionADependencyForbids(t *testing.T) {
	mod, st, candidates := lockedModule(t, ">= 3.85.0", "4.11.0", ">= 3.85.0, >= 4.11.0, <= 4.19.0")

	action, reason, err := PlanAction(mod, st, candidates, PlanOptions{
		Level:      LevelMajor,
		RaiseFloor: true,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if action == nil {
		t.Fatalf("expected an action, got reason %q", reason)
	}

	// Writing ">= 5.6.0" here would make `terraform init` unsatisfiable.
	if action.To != "4.19.0" {
		t.Errorf("To = %s, want 4.19.0 (the dependency ceiling)", action.To)
	}
}

func TestPlanActionAcceptsMatchingPrereleaseDependencyPins(t *testing.T) {
	for _, explicit := range []bool{false, true} {
		mod, requirement := buildModule(t, "root", testCase{
			source: "hashicorp/azurerm", localName: "azurerm", constraint: ">= 4.0.0", locked: "4.65.0",
		})
		candidates := versions(t, "4.65.0", "5.0.0-beta.1")
		status := BuildStatus(mod, requirement, candidates, true)
		status.SetExternalConstraint("5.0.0-beta.1, = 5.0.0-beta.1", candidates)
		options := PlanOptions{Level: LevelMajor, IncludePrerelease: true}
		if explicit {
			options.Explicit = mustVersion(t, "5.0.0-beta.1")
		}
		action, reason, err := PlanAction(mod, status, candidates, options)
		if err != nil || action == nil || action.To != "5.0.0-beta.1" {
			t.Fatalf("matching dependency pins refused (explicit=%t): action=%+v, reason=%s, err=%v", explicit, action, reason, err)
		}
	}
}

func TestPlanActionRefusesExplicitVersionADependencyForbids(t *testing.T) {
	mod, st, candidates := lockedModule(t, ">= 3.85.0", "4.19.0", ">= 3.85.0, >= 4.11.0, <= 4.19.0")

	action, reason, err := PlanAction(mod, st, candidates, PlanOptions{
		Level:    LevelMajor,
		Explicit: mustVersion(t, "5.6.0"),
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if action != nil {
		t.Fatalf("expected a refusal, got %+v", action)
	}
	if !strings.Contains(reason, "dependency") {
		t.Errorf("reason should name the dependency: %q", reason)
	}
}

func TestPlanActionExplainsDependencyCeilingWhenNothingToDo(t *testing.T) {
	mod, st, candidates := lockedModule(t, ">= 3.85.0", "4.19.0", ">= 3.85.0, >= 4.11.0, <= 4.19.0")

	action, reason, err := PlanAction(mod, st, candidates, PlanOptions{Level: LevelMajor})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if action != nil {
		t.Fatalf("expected no action, got %+v", action)
	}
	// Otherwise the user sees "already at the newest" and cannot tell why.
	if !strings.Contains(reason, "dependency constrains") {
		t.Errorf("reason should explain the invisible ceiling: %q", reason)
	}
}

func TestPlanActionUnaffectedWithoutExternalConstraints(t *testing.T) {
	mod, st, candidates := lockedModule(t, ">= 3.85.0", "4.11.0", ">= 3.85.0")

	action, reason, err := PlanAction(mod, st, candidates, PlanOptions{Level: LevelMajor})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if action == nil {
		t.Fatalf("expected an action, got reason %q", reason)
	}
	if action.To != "4.19.0" {
		t.Errorf("To = %s, want the next release 4.19.0 when nothing external constrains it", action.To)
	}
}
