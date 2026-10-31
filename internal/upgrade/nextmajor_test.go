package upgrade

import "testing"

func TestNextExactMajor(t *testing.T) {
	tests := []struct {
		name       string
		current    string
		candidates []string
		want       string
	}{
		{
			name:       "steps to the next major, not the newest",
			current:    "3.100.0",
			candidates: []string{"3.100.0", "3.117.1", "4.0.0", "4.81.0", "5.0.0", "5.6.0"},
			want:       "4.0.0",
		},
		{
			name:       "picks the first release within that major",
			current:    "4.0.0",
			candidates: []string{"4.0.0", "5.0.0", "5.3.1", "5.6.0"},
			want:       "5.0.0",
		},
		{
			// A major series that was never published must not stall the walk.
			name:       "skips a missing major series",
			current:    "3.117.1",
			candidates: []string{"3.117.1", "5.0.0", "5.6.0"},
			want:       "5.0.0",
		},
		{
			name:       "ignores older and equal majors",
			current:    "4.81.0",
			candidates: []string{"3.117.1", "4.0.0", "4.81.0", "5.6.0"},
			want:       "5.6.0",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := NextExact(versions(t, tc.candidates...), mustVersion(t, tc.current), LevelMajor)
			if got == nil || got.String() != tc.want {
				t.Errorf("NextExact(%s, major) = %v, want %s", tc.current, got, tc.want)
			}
		})
	}
}

func TestNextExactMajorWhenAlreadyNewest(t *testing.T) {
	got := NextExact(
		versions(t, "5.0.0", "5.6.0"),
		mustVersion(t, "5.6.0"),
		LevelMajor,
	)
	if got != nil {
		t.Errorf("NextExact() = %v, want nil when no newer major exists", got)
	}
}

func TestPlanActionStepsOneMajorAtATime(t *testing.T) {
	mod, req := buildModule(t, "tf-hub", testCase{
		source:     "hashicorp/azurerm",
		localName:  "azurerm",
		constraint: ">= 3.0.0",
		locked:     "3.100.0",
	})

	candidates := versions(t, "3.100.0", "3.117.1", "4.0.0", "4.81.0", "5.0.0", "5.6.0")
	st := BuildStatus(mod, req, candidates, false)

	t.Run("without stepping it takes the next available version", func(t *testing.T) {
		action, _, err := PlanAction(mod, st, candidates, PlanOptions{Level: LevelMajor})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if action == nil || action.To != "3.117.1" {
			t.Fatalf("To = %v, want 3.117.1", action)
		}
	})

	t.Run("with stepping it advances one major", func(t *testing.T) {
		action, reason, err := PlanAction(mod, st, candidates, PlanOptions{
			Level:         LevelMajor,
			NextMajorOnly: true,
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if action == nil {
			t.Fatalf("expected an action, got reason %q", reason)
		}
		if action.To != "4.0.0" {
			t.Errorf("To = %s, want 4.0.0", action.To)
		}
		if action.Level != LevelMajor || !action.Breaking {
			t.Errorf("a major step must still be flagged breaking: %+v", action)
		}
	})
}

func TestPlanActionNextMajorWhenAlreadyOnNewestMajor(t *testing.T) {
	mod, req := buildModule(t, "tf-hub", testCase{
		source:     "hashicorp/azurerm",
		localName:  "azurerm",
		constraint: ">= 5.0.0",
		locked:     "5.6.0",
	})

	candidates := versions(t, "5.0.0", "5.6.0")
	st := BuildStatus(mod, req, candidates, false)

	action, reason, err := PlanAction(mod, st, candidates, PlanOptions{
		Level:         LevelMajor,
		NextMajorOnly: true,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if action != nil {
		t.Fatalf("expected no action, got %+v", action)
	}
	if reason == "" {
		t.Error("expected a reason explaining that no newer major exists")
	}
}

func TestPlanActionNextMajorRewritesBlockingConstraint(t *testing.T) {
	// Stepping must still repair a constraint that would block the step.
	mod, req := buildModule(t, "tf-hub", testCase{
		source:     "hashicorp/azurerm",
		localName:  "azurerm",
		constraint: "~> 3.100",
		locked:     "3.100.0",
	})

	candidates := versions(t, "3.100.0", "4.81.0", "5.6.0")
	st := BuildStatus(mod, req, candidates, false)

	action, _, err := PlanAction(mod, st, candidates, PlanOptions{
		Level:         LevelMajor,
		NextMajorOnly: true,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if action == nil {
		t.Fatal("expected an action")
	}
	if action.To != "4.81.0" {
		t.Errorf("To = %s, want 4.81.0", action.To)
	}
	if !action.Constraint.Changed || action.Constraint.New != "~> 4.0" {
		t.Errorf("constraint = %+v, want a rewrite to \"~> 4.0\"", action.Constraint)
	}
}
