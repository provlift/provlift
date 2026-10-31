package upgrade

import (
	"strings"
	"testing"

	"github.com/provlift/provlift/internal/model"
)

// childModule builds a module that another module consumes, so it owns no lock
// file of its own.
func childModule(t *testing.T, constraint string) (*model.Module, model.ProviderRequirement) {
	t.Helper()

	mod, req := buildModule(t, "modules/storage", testCase{
		source:     "aztfmod/azurecaf",
		localName:  "azurecaf",
		constraint: constraint,
	})
	mod.IsRoot = false
	return mod, req
}

func TestPlanActionSkipsNoOpChildModuleUpgrade(t *testing.T) {
	// "~> 1.2" already permits 1.2.34 and cannot raise its floor without
	// narrowing its ceiling, so there is no constraint change. A child module
	// has no lock file either, which leaves nothing for this action to do.
	mod, req := childModule(t, "~> 1.2")

	candidates := versions(t, "1.2.0", "1.2.34")
	st := BuildStatus(mod, req, candidates, false)

	action, reason, err := PlanAction(mod, st, candidates, PlanOptions{
		Level:      LevelMinor,
		RaiseFloor: true,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if action != nil {
		t.Fatalf("expected no action, got %+v", action)
	}
	if !strings.Contains(reason, "nothing to change") {
		t.Errorf("reason should explain the no-op: %q", reason)
	}
	if !strings.Contains(reason, "narrowing its ceiling") {
		t.Errorf("reason should say why --raise-floor could not move the floor: %q", reason)
	}
}

func TestPlanActionKeepsChildModuleUpgradeWhenConstraintChanges(t *testing.T) {
	// An explicit range can raise its floor, so the edit is real work even in
	// a child module.
	mod, req := childModule(t, ">= 1.2.0")

	candidates := versions(t, "1.2.0", "1.2.34")
	st := BuildStatus(mod, req, candidates, false)

	action, reason, err := PlanAction(mod, st, candidates, PlanOptions{
		Level:      LevelMinor,
		RaiseFloor: true,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if action == nil {
		t.Fatalf("expected an action, got reason %q", reason)
	}
	if action.Constraint.New != ">= 1.2.34" {
		t.Errorf("New = %q, want \">= 1.2.34\"", action.Constraint.New)
	}
}

func TestPlanActionKeepsRootModuleUpgradeWithUnchangedConstraint(t *testing.T) {
	// A root module's lock file still moves, so an unchanged constraint is not
	// a no-op there.
	mod, req := buildModule(t, "envs/prod", testCase{
		source:     "aztfmod/azurecaf",
		localName:  "azurecaf",
		constraint: "~> 1.2",
		locked:     "1.2.0",
	})

	candidates := versions(t, "1.2.0", "1.2.34")
	st := BuildStatus(mod, req, candidates, false)

	action, reason, err := PlanAction(mod, st, candidates, PlanOptions{Level: LevelMinor})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if action == nil {
		t.Fatalf("expected an action, got reason %q", reason)
	}
	if action.Constraint.Changed {
		t.Errorf("constraint should be unchanged, got %q", action.Constraint.New)
	}
	if action.To != "1.2.34" {
		t.Errorf("To = %s, want 1.2.34", action.To)
	}
}

func TestRaiseConstraintFloorExplainsWhyPessimisticCannotMove(t *testing.T) {
	// "~> 1.2" permits all of 1.x. Rewriting it to "~> 1.2.34" would also drop
	// the ceiling to < 1.3.0, so refusing is right, but the reason has to say
	// so rather than claim the floor is already there.
	got, err := RaiseConstraintFloor("~> 1.2", mustVersion(t, "1.2.34"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.Changed {
		t.Fatalf("expected no change, got %q", got.New)
	}
	if strings.Contains(got.Reason, "already") {
		t.Errorf("reason wrongly claims the floor is already set: %q", got.Reason)
	}
	if !strings.Contains(got.Reason, "narrowing its ceiling") {
		t.Errorf("reason should explain the ceiling problem: %q", got.Reason)
	}
	if !strings.Contains(got.Reason, ">= 1.2.34, < 2.0.0") {
		t.Errorf("reason should suggest an explicit range: %q", got.Reason)
	}
}

func TestRaiseConstraintFloorExplainsWhyOneSegmentPessimisticCannotMove(t *testing.T) {
	// "~> 6" can only name a whole major version as its floor, so it cannot
	// declare 6.3.0 without changing its shape. The reason has to say so.
	got, err := RaiseConstraintFloor("~> 6", mustVersion(t, "6.3.0"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.Changed {
		t.Fatalf("expected no change, got %q", got.New)
	}
	if strings.Contains(got.Reason, "already") {
		t.Errorf("reason wrongly claims the floor is already set: %q", got.Reason)
	}
	if !strings.Contains(got.Reason, ">= 6.3.0, < 7.0.0") {
		t.Errorf("reason should suggest an explicit range: %q", got.Reason)
	}
}
