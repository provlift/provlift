package upgrade

import (
	"errors"
	"testing"

	version "github.com/hashicorp/go-version"

	"github.com/provlift/provlift/internal/model"
)

// versions builds an ascending candidate list.
func versions(t *testing.T, list ...string) []*version.Version {
	t.Helper()
	out := make([]*version.Version, 0, len(list))
	for _, s := range list {
		out = append(out, mustVersion(t, s))
	}
	return out
}

func mustAddress(t *testing.T, source string) model.Address {
	t.Helper()
	addr, err := model.ParseAddress(source)
	if err != nil {
		t.Fatalf("ParseAddress(%q): %v", source, err)
	}
	return addr
}

// testCase describes one provider in one module.
type testCase struct {
	source     string
	localName  string
	constraint string
	locked     string // empty means the module has no lock entry
}

func buildModule(t *testing.T, relDir string, tc testCase) (*model.Module, model.ProviderRequirement) {
	t.Helper()

	addr := mustAddress(t, tc.source)

	req := model.ProviderRequirement{
		LocalName:     tc.localName,
		Address:       addr,
		Source:        addr.String(),
		Constraint:    tc.constraint,
		ConstraintPos: model.SourcePos{File: relDir + "/versions.tf", Line: 7},
		FilePath:      relDir + "/versions.tf",
		IsObject:      true,
		VersionRange:  &model.ByteRange{Start: 10, End: 20},
	}

	mod := &model.Module{
		Dir:          relDir,
		RelDir:       relDir,
		IsRoot:       true,
		Requirements: []model.ProviderRequirement{req},
		Locks:        map[string]model.ProviderLock{},
	}

	if tc.locked != "" {
		mod.HasLockFile = true
		mod.Locks[addr.String()] = model.ProviderLock{
			Address: addr,
			Source:  addr.String(),
			Version: tc.locked,
		}
	}
	return mod, req
}

func TestBuildStatusPrefersLockedVersion(t *testing.T) {
	mod, req := buildModule(t, "tf-hub", testCase{
		source:     "hashicorp/azurerm",
		localName:  "azurerm",
		constraint: ">= 3.85.0",
		locked:     "3.117.1",
	})

	st := BuildStatus(mod, req, versions(t, "3.117.1", "4.81.0", "5.6.0"), false)

	if st.CurrentSource != SourceLock {
		t.Errorf("CurrentSource = %q, want %q", st.CurrentSource, SourceLock)
	}
	if st.Current != "3.117.1" {
		t.Errorf("Current = %q, want 3.117.1", st.Current)
	}
	if st.Latest != "5.6.0" {
		t.Errorf("Latest = %q, want 5.6.0", st.Latest)
	}
}

func TestBuildStatusUsesConstraintFloorWhenUnlocked(t *testing.T) {
	// A shared-module repository commits no lock file. It has selected nothing,
	// but it does declare a supported floor, and how far that floor sits behind
	// the newest release is the question worth answering.
	mod, req := buildModule(t, "modules/aks", testCase{
		source:     "hashicorp/azurerm",
		localName:  "azurerm",
		constraint: ">= 3.85.0",
	})

	st := BuildStatus(mod, req, versions(t, "3.117.1", "4.81.0", "5.6.0"), false)

	if st.CurrentSource != SourceFloor {
		t.Errorf("CurrentSource = %q, want %q", st.CurrentSource, SourceFloor)
	}
	if st.Current != "3.117.1" {
		t.Errorf("Current = %q, want 3.117.1 (the lowest version the constraint admits)", st.Current)
	}
	// Reporting "nothing to do" here would hide two majors of untested range.
	if st.Available != LevelMajor {
		t.Errorf("Available = %s, want major", st.Available)
	}
}

func TestBuildStatusUnknownWithoutLockOrConstraint(t *testing.T) {
	// With neither a lock file nor a constraint there is no basis for a floor,
	// and inventing one from the oldest published release would be meaningless.
	mod, req := buildModule(t, "modules/aks", testCase{
		source:    "mongodb/mongodbatlas",
		localName: "mongodbatlas",
	})

	st := BuildStatus(mod, req, versions(t, "1.0.0", "2.18.0"), false)

	if st.CurrentSource != SourceUnknown {
		t.Errorf("CurrentSource = %q, want %q", st.CurrentSource, SourceUnknown)
	}
	if st.Current != "" {
		t.Errorf("Current = %q, want empty", st.Current)
	}
}

func TestLowestSatisfying(t *testing.T) {
	candidates := versions(t, "3.0.0", "3.117.1", "4.81.0", "5.6.0")

	tests := []struct {
		constraint string
		want       string
	}{
		{">= 3.85.0", "3.117.1"},
		{"~> 4.0", "4.81.0"},
		{"", "3.0.0"},
	}

	for _, tc := range tests {
		t.Run(tc.constraint, func(t *testing.T) {
			got := LowestSatisfying(candidates, tc.constraint)
			if got == nil || got.String() != tc.want {
				t.Errorf("LowestSatisfying(%q) = %v, want %s", tc.constraint, got, tc.want)
			}
		})
	}
}

func TestBuildStatusUnknownWhenNoLockAndNoCandidates(t *testing.T) {
	mod, req := buildModule(t, "tf-hub", testCase{
		source:     "hashicorp/azurerm",
		localName:  "azurerm",
		constraint: ">= 3.85.0",
	})

	st := BuildStatus(mod, req, nil, false)

	if st.CurrentSource != SourceUnknown {
		t.Errorf("CurrentSource = %q, want %q", st.CurrentSource, SourceUnknown)
	}
	if st.Current != "" {
		t.Errorf("Current = %q, want empty", st.Current)
	}
}

func TestBuildStatusBlockedDetection(t *testing.T) {
	// Each case mirrors a constraint shape found in real configurations, with
	// the candidate list that provider actually publishes.
	tests := []struct {
		name        string
		source      string
		constraint  string
		locked      string
		candidates  []string
		wantLevel   Level
		wantBlocked bool
	}{
		{
			name:        "exact pin blocks a minor",
			source:      "hashicorp/azuread",
			constraint:  "3.4.0",
			locked:      "3.4.0",
			candidates:  []string{"3.4.0", "3.5.0", "3.9.0"},
			wantLevel:   LevelMinor,
			wantBlocked: true,
		},
		{
			name:        "pessimistic blocks a major",
			source:      "hashicorp/azurerm",
			constraint:  "~> 4.65",
			locked:      "4.65.0",
			candidates:  []string{"4.65.0", "4.81.0", "5.6.0"},
			wantLevel:   LevelMajor,
			wantBlocked: true,
		},
		{
			name:        "one-segment pessimistic blocks a major",
			source:      "integrations/github",
			constraint:  "~> 6",
			locked:      "6.2.1",
			candidates:  []string{"6.2.1", "6.9.0", "7.0.0"},
			wantLevel:   LevelMajor,
			wantBlocked: true,
		},
		{
			name:        "bounded range blocks a major",
			source:      "hashicorp/azurerm",
			constraint:  ">= 3.47.0, < 4.0.0",
			locked:      "3.117.1",
			candidates:  []string{"3.117.1", "4.81.0", "5.6.0"},
			wantLevel:   LevelMajor,
			wantBlocked: true,
		},
		{
			name:        "lower bound blocks nothing",
			source:      "hashicorp/azurerm",
			constraint:  ">= 3.85.0",
			locked:      "3.117.1",
			candidates:  []string{"3.117.1", "4.81.0", "5.6.0"},
			wantLevel:   LevelMajor,
			wantBlocked: false,
		},
		{
			name:        "patch only, nothing blocked",
			source:      "aztfmod/azurecaf",
			constraint:  ">= 1.2",
			locked:      "1.2.31",
			candidates:  []string{"1.2.31", "1.2.34"},
			wantLevel:   LevelPatch,
			wantBlocked: false,
		},
		{
			name:        "already at the only published version",
			source:      "bwoznicki/assert",
			constraint:  "0.0.1",
			locked:      "0.0.1",
			candidates:  []string{"0.0.1"},
			wantLevel:   LevelNone,
			wantBlocked: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			mod, req := buildModule(t, "tf-hub", testCase{
				source:     tc.source,
				localName:  "provider",
				constraint: tc.constraint,
				locked:     tc.locked,
			})

			st := BuildStatus(mod, req, versions(t, tc.candidates...), false)

			if st.Available != tc.wantLevel {
				t.Errorf("Available = %s, want %s", st.Available, tc.wantLevel)
			}
			if st.Blocked != tc.wantBlocked {
				t.Errorf("Blocked = %v, want %v", st.Blocked, tc.wantBlocked)
			}
		})
	}
}

func TestBuildStatusReadsOneSegmentPessimisticAsTerraformDoes(t *testing.T) {
	// Terraform reads "~> 6" as ">= 6.0, < 7.0", so 7.0.0 is out of reach.
	mod, req := buildModule(t, "tf-hub", testCase{
		source:     "integrations/github",
		localName:  "github",
		constraint: "~> 6",
		locked:     "6.2.1",
	})

	st := BuildStatus(mod, req, versions(t, "6.2.1", "6.9.0", "7.0.0"), false)

	if st.LatestAllowed != "6.9.0" {
		t.Errorf("LatestAllowed = %q, want 6.9.0", st.LatestAllowed)
	}
}

func TestBuildStatusReportsPerLevelTargets(t *testing.T) {
	mod, req := buildModule(t, "tf-hub", testCase{
		source:     "hashicorp/azurerm",
		localName:  "azurerm",
		constraint: "~> 4.65",
		locked:     "4.65.0",
	})

	st := BuildStatus(mod, req, versions(t, "4.65.0", "4.65.3", "4.81.0", "5.6.0"), false)

	if st.LatestPatch != "4.65.3" {
		t.Errorf("LatestPatch = %q, want 4.65.3", st.LatestPatch)
	}
	if st.LatestMinor != "4.81.0" {
		t.Errorf("LatestMinor = %q, want 4.81.0", st.LatestMinor)
	}
	if st.LatestAllowed != "4.81.0" {
		t.Errorf("LatestAllowed = %q, want 4.81.0 (highest within ~> 4.65)", st.LatestAllowed)
	}
	if st.Latest != "5.6.0" {
		t.Errorf("Latest = %q, want 5.6.0", st.Latest)
	}
}

func TestBuildStatusPrereleaseHandling(t *testing.T) {
	mod, req := buildModule(t, "tf-hub", testCase{
		source:     "hashicorp/azurerm",
		localName:  "azurerm",
		constraint: ">= 4.0.0",
		locked:     "4.81.0",
	})

	candidates := versions(t, "4.81.0", "5.0.0-beta1")

	excluded := BuildStatus(mod, req, candidates, false)
	if excluded.Latest != "4.81.0" {
		t.Errorf("Latest = %q, want 4.81.0 when prereleases are excluded", excluded.Latest)
	}

	included := BuildStatus(mod, req, candidates, true)
	if included.Latest != "5.0.0-beta1" {
		t.Errorf("Latest = %q, want 5.0.0-beta1 when prereleases are included", included.Latest)
	}
}

func TestPlanActionSkipsWhenCurrentUnknown(t *testing.T) {
	mod, req := buildModule(t, "tf-hub", testCase{
		source:     "hashicorp/azurerm",
		localName:  "azurerm",
		constraint: ">= 3.85.0",
	})
	st := BuildStatus(mod, req, nil, false)

	action, reason, err := PlanAction(mod, st, nil, PlanOptions{Level: LevelMinor})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if action != nil {
		t.Errorf("action = %+v, want nil", action)
	}
	if reason == "" {
		t.Error("expected a reason explaining why nothing was planned")
	}
}

func TestPlanActionLeavesSatisfiedConstraintAlone(t *testing.T) {
	// The common case: the lock file moves but versions.tf is not touched.
	mod, req := buildModule(t, "tf-hub", testCase{
		source:     "hashicorp/azurerm",
		localName:  "azurerm",
		constraint: ">= 3.85.0",
		locked:     "3.117.1",
	})

	candidates := versions(t, "3.117.1", "4.81.0")
	st := BuildStatus(mod, req, candidates, false)

	action, _, err := PlanAction(mod, st, candidates, PlanOptions{Level: LevelMajor})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if action == nil {
		t.Fatal("expected an action")
	}

	if action.From != "3.117.1" || action.To != "4.81.0" {
		t.Errorf("From/To = %s/%s, want 3.117.1/4.81.0", action.From, action.To)
	}
	if action.Level != LevelMajor {
		t.Errorf("Level = %s, want major", action.Level)
	}
	if !action.Breaking {
		t.Error("a major upgrade must be flagged breaking")
	}
	if action.Constraint.Changed {
		t.Errorf("constraint should be untouched, got %q", action.Constraint.New)
	}
}

func TestPlanActionExplicitPrereleaseRequiresOptIn(t *testing.T) {
	for _, includePrerelease := range []bool{false, true} {
		mod, requirement := buildModule(t, "root", testCase{
			source: "hashicorp/azurerm", localName: "azurerm", constraint: ">= 4.0.0", locked: "4.65.0",
		})
		candidates := versions(t, "4.65.0", "5.0.0-beta.1")
		status := BuildStatus(mod, requirement, candidates, includePrerelease)
		action, reason, err := PlanAction(mod, status, candidates, PlanOptions{
			Level: LevelMajor, Explicit: candidates[1], IncludePrerelease: includePrerelease,
		})
		if err != nil {
			t.Fatal(err)
		}
		if includePrerelease && action == nil {
			t.Fatalf("opted-in prerelease refused: %s", reason)
		}
		if !includePrerelease && action != nil {
			t.Fatal("explicit target bypassed prerelease opt-in")
		}
	}
}

func TestPlanActionExplicitVersionHonoursExactLevel(t *testing.T) {
	mod, requirement := buildModule(t, "root", testCase{
		source: "hashicorp/azurerm", localName: "azurerm", constraint: ">= 4.0.0", locked: "4.65.0",
	})
	candidates := versions(t, "4.65.0", "4.65.1")
	status := BuildStatus(mod, requirement, candidates, false)
	action, reason, err := PlanAction(mod, status, candidates, PlanOptions{
		Level: LevelMajor, ExactLevel: true, Explicit: candidates[1],
	})
	if err != nil {
		t.Fatal(err)
	}
	if action != nil || reason == "" {
		t.Fatalf("exact major selection admitted patch target: %+v (%s)", action, reason)
	}
}

func TestPlanActionMarksBreakingFloorDuringVersionUpgrade(t *testing.T) {
	mod, requirement := buildModule(t, "root", testCase{
		source: "hashicorp/azurerm", localName: "azurerm", constraint: ">= 3.0.0", locked: "4.65.0",
	})
	candidates := versions(t, "3.0.0", "4.65.0", "4.65.1")
	status := BuildStatus(mod, requirement, candidates, false)
	action, reason, err := PlanAction(mod, status, candidates, PlanOptions{Level: LevelPatch, RaiseFloor: true})
	if err != nil || action == nil {
		t.Fatalf("expected floor upgrade: %s, %v", reason, err)
	}
	if !action.Breaking {
		t.Fatal("raising the supported floor from major 3 to major 4 must require acknowledgement")
	}
}

func TestPlanActionRaisesFloorForExplicitTargetEqualToLock(t *testing.T) {
	mod, requirement := buildModule(t, "root", testCase{
		source: "hashicorp/azurerm", localName: "azurerm", constraint: ">= 3.85.0", locked: "5.6.0",
	})
	candidates := versions(t, "3.85.0", "5.6.0")
	status := BuildStatus(mod, requirement, candidates, false)
	action, reason, err := PlanAction(mod, status, candidates, PlanOptions{
		Level: LevelMajor, Explicit: candidates[1], RaiseFloor: true,
	})
	if err != nil || action == nil {
		t.Fatalf("expected constraint-only floor raise: %s, %v", reason, err)
	}
	if !action.ConstraintOnly || action.Constraint.New != ">= 5.6.0" || !action.Breaking {
		t.Fatalf("unexpected floor raise action: %+v", action)
	}
}

func TestPlanActionRewritesBlockingConstraint(t *testing.T) {
	mod, req := buildModule(t, "tf-hub", testCase{
		source:     "hashicorp/azurerm",
		localName:  "azurerm",
		constraint: "~> 4.65",
		locked:     "4.65.0",
	})

	candidates := versions(t, "4.65.0", "5.6.0")
	st := BuildStatus(mod, req, candidates, false)

	action, _, err := PlanAction(mod, st, candidates, PlanOptions{Level: LevelMajor})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if action == nil {
		t.Fatal("expected an action")
	}
	if !action.Constraint.Changed {
		t.Fatal("constraint must change: ~> 4.65 cannot admit 5.6.0")
	}
	if action.Constraint.New != "~> 5.0" {
		t.Errorf("new constraint = %q, want \"~> 5.0\"", action.Constraint.New)
	}
	if action.Requirement.FilePath == "" {
		t.Error("the action must carry the requirement so the file can be edited")
	}
}

func TestPlanActionRespectsLevelBudget(t *testing.T) {
	mod, req := buildModule(t, "tf-hub", testCase{
		source:     "hashicorp/azurerm",
		localName:  "azurerm",
		constraint: ">= 3.85.0",
		locked:     "4.65.0",
	})

	candidates := versions(t, "4.65.0", "4.65.2", "4.81.0", "5.6.0")
	st := BuildStatus(mod, req, candidates, false)

	tests := []struct {
		level  Level
		wantTo string
	}{
		{LevelPatch, "4.65.2"},
		{LevelMinor, "4.65.2"},
		{LevelMajor, "4.65.2"},
	}

	for _, tc := range tests {
		t.Run(tc.level.String(), func(t *testing.T) {
			action, reason, err := PlanAction(mod, st, candidates, PlanOptions{Level: tc.level})
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if action == nil {
				t.Fatalf("expected an action, got reason %q", reason)
			}
			if action.To != tc.wantTo {
				t.Errorf("To = %s, want %s", action.To, tc.wantTo)
			}
		})
	}
}

func TestPlanActionCanSelectAnExactProgressiveStage(t *testing.T) {
	mod, req := buildModule(t, "tf-hub", testCase{
		source:     "hashicorp/azurerm",
		localName:  "azurerm",
		constraint: ">= 3.85.0",
		locked:     "4.65.0",
	})

	candidates := versions(t, "4.65.0", "4.65.2", "4.81.0", "5.6.0")
	st := BuildStatus(mod, req, candidates, false)

	action, reason, err := PlanAction(mod, st, candidates, PlanOptions{
		Level:      LevelMinor,
		ExactLevel: true,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if action == nil {
		t.Fatalf("expected a minor action, got reason %q", reason)
	}
	if action.To != "4.81.0" {
		t.Errorf("To = %s, want 4.81.0", action.To)
	}

	patch, reason, err := PlanAction(mod, st, candidates, PlanOptions{
		Level:      LevelPatch,
		ExactLevel: true,
	})
	if err != nil {
		t.Fatalf("unexpected patch error: %v", err)
	}
	if patch == nil || patch.To != "4.65.2" {
		t.Errorf("patch = %#v, reason %q; want 4.65.2", patch, reason)
	}
}

func TestPlanActionSelectsNextPublishedRelease(t *testing.T) {
	mod, req := buildModule(t, "tf-hub", testCase{
		source: "hashicorp/azurerm", localName: "azurerm", constraint: ">= 4.57.0", locked: "4.57.0",
	})
	candidates := versions(t, "4.57.0", "4.57.2", "4.58.0", "4.81.0", "5.0.0", "5.8.0")
	status := BuildStatus(mod, req, candidates, false)
	for _, tc := range []struct {
		name  string
		level Level
		exact bool
		want  string
	}{
		{"budget", LevelMinor, false, "4.57.2"},
		{"patch", LevelPatch, true, "4.57.2"},
		{"minor", LevelMinor, true, "4.58.0"},
		{"major", LevelMajor, true, "5.0.0"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			action, reason, err := PlanAction(mod, status, candidates, PlanOptions{Level: tc.level, ExactLevel: tc.exact})
			if err != nil || action == nil || action.To != tc.want {
				t.Fatalf("action = %#v, reason = %q, err = %v; want %s", action, reason, err, tc.want)
			}
		})
	}
}

func TestPlanActionExplicitTarget(t *testing.T) {
	mod, req := buildModule(t, "tf-hub", testCase{
		source:     "hashicorp/azurerm",
		localName:  "azurerm",
		constraint: ">= 3.85.0",
		locked:     "4.65.0",
	})

	candidates := versions(t, "4.65.0", "4.81.0", "5.6.0")
	st := BuildStatus(mod, req, candidates, false)

	t.Run("pins to the requested version", func(t *testing.T) {
		action, reason, err := PlanAction(mod, st, candidates, PlanOptions{
			Level:    LevelMinor,
			Explicit: mustVersion(t, "4.81.0"),
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if action == nil {
			t.Fatalf("expected an action, got reason %q", reason)
		}
		if action.To != "4.81.0" {
			t.Errorf("To = %s, want 4.81.0", action.To)
		}
	})

	t.Run("refuses a target above the level budget", func(t *testing.T) {
		action, reason, err := PlanAction(mod, st, candidates, PlanOptions{
			Level:    LevelMinor,
			Explicit: mustVersion(t, "5.6.0"),
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if action != nil {
			t.Fatal("a major target must not be planned under --level minor")
		}
		if reason == "" {
			t.Error("expected an explanatory reason")
		}
	})

	t.Run("refuses a target that is not newer", func(t *testing.T) {
		action, reason, err := PlanAction(mod, st, candidates, PlanOptions{
			Level:    LevelMajor,
			Explicit: mustVersion(t, "4.65.0"),
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if action != nil {
			t.Fatal("the current version must not be planned as an upgrade")
		}
		if reason == "" {
			t.Error("expected an explanatory reason")
		}
	})
}

func TestPlanActionPropagatesUnsupportedConstraint(t *testing.T) {
	mod, req := buildModule(t, "tf-hub", testCase{
		source:     "hashicorp/azurerm",
		localName:  "azurerm",
		constraint: ">= 1.0.0, != 2.0.0",
		locked:     "1.5.0",
	})

	candidates := versions(t, "1.5.0", "2.0.0")
	st := BuildStatus(mod, req, candidates, false)

	_, _, err := PlanAction(mod, st, candidates, PlanOptions{Level: LevelMajor})
	if !errors.Is(err, ErrUnsupportedConstraint) {
		t.Errorf("err = %v, want ErrUnsupportedConstraint", err)
	}
}

func TestPlanActionFlagsZeroMajorMinorAsBreaking(t *testing.T) {
	mod, req := buildModule(t, "tf-hub", testCase{
		source:     "bwoznicki/assert",
		localName:  "assert",
		constraint: ">= 0.0.1",
		locked:     "0.1.0",
	})

	candidates := versions(t, "0.1.0", "0.2.0")
	st := BuildStatus(mod, req, candidates, false)

	action, _, err := PlanAction(mod, st, candidates, PlanOptions{Level: LevelMinor})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if action == nil {
		t.Fatal("expected an action")
	}
	if action.Level != LevelMinor {
		t.Errorf("Level = %s, want minor", action.Level)
	}
	if !action.Breaking {
		t.Error("a 0.x minor bump may break and must be flagged")
	}
}

func TestPlanHelpers(t *testing.T) {
	plan := &Plan{
		Actions: []Action{
			{Module: "a", ModuleDir: "/repo/a", Breaking: false},
			{Module: "b", ModuleDir: "/repo/b", Breaking: true},
			{Module: "a again", ModuleDir: "/repo/a", Breaking: false},
		},
	}

	if !plan.HasBreaking() {
		t.Error("HasBreaking() = false, want true")
	}

	dirs := plan.ModuleDirs()
	if len(dirs) != 2 {
		t.Errorf("ModuleDirs() = %v, want 2 distinct directories", dirs)
	}
}

func TestPlanActionNeverLeavesTheLevelBudget(t *testing.T) {
	// --level is a ceiling and --exact-level a filter. Neither may fall
	// through to a neighbouring level when the requested one has nothing.
	tests := []struct {
		name      string
		level     Level
		exact     bool
		published []string
		wantTo    string // empty means no action
	}{
		{"patch budget does not climb to minor", LevelPatch, false, []string{"4.65.0", "4.81.0", "5.6.0"}, ""},
		{"minor budget does not climb to major", LevelMinor, false, []string{"4.65.0", "5.6.0"}, ""},
		{"minor budget admits a minor", LevelMinor, false, []string{"4.65.0", "4.81.0", "5.6.0"}, "4.81.0"},
		{"major budget admits a major", LevelMajor, false, []string{"4.65.0", "5.6.0"}, "5.6.0"},
		{"exact patch ignores minor", LevelPatch, true, []string{"4.65.0", "4.81.0"}, ""},
		{"exact minor ignores patch", LevelMinor, true, []string{"4.65.0", "4.65.2", "5.6.0"}, ""},
		{"exact major ignores patch and minor", LevelMajor, true, []string{"4.65.0", "4.65.2", "4.81.0"}, ""},
		{"exact minor takes the first minor", LevelMinor, true, []string{"4.65.0", "4.65.2", "4.81.0", "4.90.0", "5.6.0"}, "4.81.0"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			mod, req := buildModule(t, "tf-hub", testCase{
				source:     "hashicorp/azurerm",
				localName:  "azurerm",
				constraint: ">= 4.0.0",
				locked:     "4.65.0",
			})
			candidates := versions(t, tc.published...)
			st := BuildStatus(mod, req, candidates, false)

			action, reason, err := PlanAction(mod, st, candidates, PlanOptions{Level: tc.level, ExactLevel: tc.exact})
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if tc.wantTo == "" {
				if action != nil {
					t.Fatalf("expected no action, got %s -> %s (%s)", action.From, action.To, action.Level)
				}
				if reason == "" {
					t.Error("expected a reason explaining why nothing was planned")
				}
				return
			}
			if action == nil {
				t.Fatalf("expected an action, got reason %q", reason)
			}
			if action.To != tc.wantTo {
				t.Errorf("To = %s, want %s", action.To, tc.wantTo)
			}
			if !tc.level.Allows(action.Level) || (tc.exact && action.Level != tc.level) {
				t.Errorf("Level = %s is outside --level %s (exact=%t)", action.Level, tc.level, tc.exact)
			}
		})
	}
}

func TestPlanActionFlagsFloorAddedToUnconstrainedProviderAsBreaking(t *testing.T) {
	// Declaring a floor where none existed changes what the module promises
	// its consumers, so it is gated like a major even for a patch upgrade.
	tests := []struct {
		name               string
		locked             string
		published          []string
		wantTo             string
		wantConstraintOnly bool
	}{
		{"alongside a patch upgrade", "4.65.0", []string{"4.65.0", "4.65.1"}, "4.65.1", false},
		{"constraint only", "4.65.1", []string{"4.65.0", "4.65.1"}, "4.65.1", true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			mod, req := buildModule(t, "tf-hub", testCase{
				source:    "hashicorp/azurerm",
				localName: "azurerm",
				locked:    tc.locked,
			})
			candidates := versions(t, tc.published...)
			st := BuildStatus(mod, req, candidates, false)

			action, reason, err := PlanAction(mod, st, candidates, PlanOptions{Level: LevelPatch, RaiseFloor: true})
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if action == nil {
				t.Fatalf("expected an action, got reason %q", reason)
			}
			if action.To != tc.wantTo || action.ConstraintOnly != tc.wantConstraintOnly {
				t.Errorf("To = %s, ConstraintOnly = %t; want %s, %t", action.To, action.ConstraintOnly, tc.wantTo, tc.wantConstraintOnly)
			}
			if want := ">= " + tc.wantTo; action.Constraint.New != want {
				t.Errorf("New = %q, want %q", action.Constraint.New, want)
			}
			if !action.Breaking {
				t.Error("adding a floor to an unconstrained provider must require --allow-major")
			}
		})
	}
}
