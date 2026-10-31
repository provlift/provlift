package pullrequest

import (
	"strings"
	"testing"

	version "github.com/hashicorp/go-version"

	"github.com/provlift/provlift/internal/model"
	"github.com/provlift/provlift/internal/upgrade"
)

func addr(t *testing.T, source string) model.Address {
	t.Helper()
	a, err := model.ParseAddress(source)
	if err != nil {
		t.Fatalf("ParseAddress(%q): %v", source, err)
	}
	return a
}

func action(t *testing.T, module, source, from, to string, level upgrade.Level, breaking bool) upgrade.Action {
	t.Helper()
	a := addr(t, source)
	return upgrade.Action{
		Module:   module,
		Source:   a.String(),
		Address:  a,
		From:     from,
		To:       to,
		Level:    level,
		Breaking: breaking,
	}
}

func TestBranchNameIsStableForTheSameUpgrade(t *testing.T) {
	plan := &upgrade.Plan{Actions: []upgrade.Action{
		action(t, "envs/prod", "hashicorp/azurerm", "4.11.0", "4.19.0", upgrade.LevelMinor, false),
	}}

	first := Build(plan, nil, nil)
	second := Build(plan, nil, nil)

	if first.Branch != second.Branch {
		t.Errorf("branch is not deterministic: %q vs %q", first.Branch, second.Branch)
	}
	// Re-running the same upgrade must update the existing pull request
	// rather than opening a second one, which requires the target version in
	// the name.
	if first.Branch != "provlift/hashicorp-azurerm-4.19.0" {
		t.Errorf("Branch = %q", first.Branch)
	}
}

func TestBranchNameChangesWithTheTargetVersion(t *testing.T) {
	older := Build(&upgrade.Plan{Actions: []upgrade.Action{
		action(t, "envs/prod", "hashicorp/azurerm", "4.11.0", "4.19.0", upgrade.LevelMinor, false),
	}}, nil, nil)

	newer := Build(&upgrade.Plan{Actions: []upgrade.Action{
		action(t, "envs/prod", "hashicorp/azurerm", "4.11.0", "4.81.0", upgrade.LevelMinor, false),
	}}, nil, nil)

	if older.Branch == newer.Branch {
		t.Errorf("a newer target must produce a new branch, both were %q", older.Branch)
	}
}

func TestTitleForASingleProvider(t *testing.T) {
	pr := Build(&upgrade.Plan{Actions: []upgrade.Action{
		action(t, "envs/prod", "hashicorp/azurerm", "4.11.0", "4.19.0", upgrade.LevelMinor, false),
	}}, nil, nil)

	if pr.Title != "Upgrade hashicorp/azurerm to 4.19.0" {
		t.Errorf("Title = %q", pr.Title)
	}
}

func TestTitleForSeveralProviders(t *testing.T) {
	pr := Build(&upgrade.Plan{Actions: []upgrade.Action{
		action(t, "envs/prod", "hashicorp/azurerm", "4.11.0", "4.19.0", upgrade.LevelMinor, false),
		action(t, "envs/prod", "integrations/github", "6.2.1", "6.13.0", upgrade.LevelMinor, false),
	}}, nil, nil)

	if !strings.Contains(pr.Title, "2 Terraform providers") {
		t.Errorf("Title = %q", pr.Title)
	}
	if !strings.HasPrefix(pr.Branch, "provlift/providers-") {
		t.Errorf("Branch = %q", pr.Branch)
	}
}

func TestConstraintOnlyChangeIsDescribedAsSuch(t *testing.T) {
	a := action(t, "initializer", "aztfmod/azurecaf", "1.2.34", "1.2.34", upgrade.LevelNone, false)
	a.ConstraintOnly = true
	a.Constraint = upgrade.Rewrite{Old: "~> 1.2.26", New: "~> 1.2.34", Changed: true}

	pr := Build(&upgrade.Plan{Actions: []upgrade.Action{a}}, nil, nil)

	// Calling this an "upgrade" would misrepresent it: no version moves.
	if !strings.Contains(pr.Title, "supported minimum") {
		t.Errorf("Title = %q", pr.Title)
	}
	if !strings.Contains(pr.Body, "floor") {
		t.Errorf("body should mark the change as floor-only:\n%s", pr.Body)
	}
}

func TestBreakingUpgradeIsLabelledAndExplained(t *testing.T) {
	pr := Build(&upgrade.Plan{Actions: []upgrade.Action{
		action(t, "envs/prod", "hashicorp/azurerm", "3.117.1", "4.19.0", upgrade.LevelMajor, true),
	}}, nil, nil)

	var labelled bool
	for _, l := range pr.Labels {
		if l == "breaking-change" {
			labelled = true
		}
	}
	if !labelled {
		t.Errorf("Labels = %v, want breaking-change", pr.Labels)
	}
	if !strings.Contains(pr.Body, "## Breaking changes") {
		t.Errorf("body is missing the breaking section:\n%s", pr.Body)
	}
	// A reviewer needs the upgrade guide, not just a version number.
	if !strings.Contains(pr.Body, "registry.terraform.io/providers/hashicorp/azurerm/4.19.0/docs") {
		t.Errorf("body is missing the documentation link:\n%s", pr.Body)
	}
}

func TestNonBreakingUpgradeHasNoBreakingSection(t *testing.T) {
	pr := Build(&upgrade.Plan{Actions: []upgrade.Action{
		action(t, "envs/prod", "hashicorp/azurerm", "4.11.0", "4.19.0", upgrade.LevelMinor, false),
	}}, nil, nil)

	if strings.Contains(pr.Body, "Breaking changes") {
		t.Errorf("unexpected breaking section:\n%s", pr.Body)
	}
	for _, l := range pr.Labels {
		if l == "breaking-change" {
			t.Errorf("Labels = %v, should not be marked breaking", pr.Labels)
		}
	}
}

func TestBodyIncludesVerificationResults(t *testing.T) {
	runs := []TerraformRun{
		{Module: "envs/prod", Command: "init -upgrade", OK: true},
		{Module: "envs/prod", Command: "validate", OK: false},
	}

	pr := Build(&upgrade.Plan{Actions: []upgrade.Action{
		action(t, "envs/prod", "hashicorp/azurerm", "4.11.0", "4.19.0", upgrade.LevelMinor, false),
	}}, runs, []string{"envs/prod/versions.tf"})

	if !strings.Contains(pr.Body, "## Verification") {
		t.Errorf("body is missing verification:\n%s", pr.Body)
	}
	for _, row := range []string{
		"| `envs/prod` | `terraform init -upgrade` | :white_check_mark: passed |",
		"| `envs/prod` | `terraform validate` | :x: **failed** |",
	} {
		if !strings.Contains(pr.Body, row) {
			t.Errorf("verification row missing icon and result %q:\n%s", row, pr.Body)
		}
	}
	if !strings.Contains(pr.Body, "envs/prod/versions.tf") {
		t.Errorf("body is missing the changed files:\n%s", pr.Body)
	}
}

func TestBodyExplainsDependencyCeilings(t *testing.T) {
	plan := &upgrade.Plan{
		Actions: []upgrade.Action{
			action(t, "envs/prod", "hashicorp/azurerm", "4.11.0", "4.19.0", upgrade.LevelMinor, false),
		},
		Skipped: []upgrade.Skip{{
			Module:  "envs/dev",
			Source:  "registry.terraform.io/hashicorp/azurerm",
			Address: addr(t, "hashicorp/azurerm"),
			Reason:  "already at the newest major-level version (4.19.0); a dependency constrains this provider to <= 4.19.0",
		}},
	}

	pr := Build(plan, nil, nil)

	// A reviewer asking "why not 5.x?" should find the answer in the PR.
	if !strings.Contains(pr.Body, "Not upgraded further") {
		t.Errorf("body should explain the ceiling:\n%s", pr.Body)
	}
	if !strings.Contains(pr.Body, "<= 4.19.0") {
		t.Errorf("body should name the constraint:\n%s", pr.Body)
	}
}

func TestSkippedEntriesUnrelatedToDependenciesAreOmitted(t *testing.T) {
	plan := &upgrade.Plan{
		Actions: []upgrade.Action{
			action(t, "envs/prod", "hashicorp/azurerm", "4.11.0", "4.19.0", upgrade.LevelMinor, false),
		},
		Skipped: []upgrade.Skip{{
			Module:  "envs/dev",
			Address: addr(t, "integrations/github"),
			Reason:  "already at the newest patch-level version (6.13.0)",
		}},
	}

	pr := Build(plan, nil, nil)

	if strings.Contains(pr.Body, "Not upgraded further") {
		t.Errorf("routine skips are noise in a pull request:\n%s", pr.Body)
	}
}

func TestModulesLandingOnDifferentVersions(t *testing.T) {
	// Real repositories hold modules on different major lines, so one
	// provider can have several targets in a single run.
	plan := &upgrade.Plan{Actions: []upgrade.Action{
		action(t, "tf-sql", "hashicorp/azurerm", "4.67.0", "4.81.0", upgrade.LevelMinor, false),
		action(t, "tf-postgres", "hashicorp/azurerm", "3.85.0", "3.117.1", upgrade.LevelMinor, false),
	}}

	pr := Build(plan, nil, nil)

	// Claiming a single target would be untrue for the module capped at 3.x.
	if strings.Contains(pr.Title, "azurerm to 4.81.0") {
		t.Errorf("Title = %q, must not claim one target for both modules", pr.Title)
	}
	if !strings.Contains(pr.Title, "up to 4.81.0") || !strings.Contains(pr.Title, "2 modules") {
		t.Errorf("Title = %q", pr.Title)
	}
	// The branch still has to carry a version, or a later release would
	// silently rewrite this pull request to mean something else.
	if pr.Branch != "provlift/hashicorp-azurerm-4.81.0" {
		t.Errorf("Branch = %q", pr.Branch)
	}
}

func TestBodyIsAscii(t *testing.T) {
	a := action(t, "envs/prod", "hashicorp/azurerm", "3.117.1", "4.19.0", upgrade.LevelMajor, true)
	a.Constraint = upgrade.Rewrite{Old: "~> 3.0", New: "~> 4.0", Changed: true}
	plan := &upgrade.Plan{
		Actions: []upgrade.Action{a},
		Skipped: []upgrade.Skip{{
			Module:  "envs/dev",
			Address: addr(t, "hashicorp/azurerm"),
			Reason:  "a dependency constrains this provider to <= 4.19.0",
		}},
	}

	pr := Build(plan, []TerraformRun{{Module: "envs/prod", Command: "validate", OK: true}}, []string{"envs/prod/versions.tf"})

	// The description is written to a file and echoed through shells and
	// terminals of unknown encoding before it reaches the forge.
	for _, r := range pr.Title + pr.Branch + pr.Body {
		if r > 127 {
			t.Errorf("non-ASCII rune %q in generated output", r)
			break
		}
	}
}

func TestEmptyPlan(t *testing.T) {
	pr := Build(&upgrade.Plan{}, nil, nil)

	if !strings.Contains(pr.Body, "No provider changes") {
		t.Errorf("Body = %q", pr.Body)
	}
	if pr.Branch != "provlift/no-changes" {
		t.Errorf("Branch = %q", pr.Branch)
	}
}

func TestBranchNamesAreSafe(t *testing.T) {
	pr := Build(&upgrade.Plan{Actions: []upgrade.Action{
		action(t, "envs/prod", "tf.example.com/platform/internal", "1.0.0", "2.0.0", upgrade.LevelMajor, true),
	}}, nil, nil)

	// A private registry address carries dots and slashes.
	if strings.Contains(pr.Branch, "/") && !strings.HasPrefix(pr.Branch, "provlift/") {
		t.Errorf("Branch contains an unexpected slash: %q", pr.Branch)
	}
	if strings.Contains(strings.TrimPrefix(pr.Branch, "provlift/"), "/") {
		t.Errorf("Branch = %q, slashes beyond the prefix are unsafe", pr.Branch)
	}
}

func TestLabelsCarryTheUpgradeLevel(t *testing.T) {
	pr := Build(&upgrade.Plan{Actions: []upgrade.Action{
		action(t, "envs/prod", "hashicorp/azurerm", "4.11.0", "4.19.0", upgrade.LevelMinor, false),
	}}, nil, nil)

	var found bool
	for _, l := range pr.Labels {
		if l == "provider-minor" {
			found = true
		}
	}
	if !found {
		t.Errorf("Labels = %v, want provider-minor", pr.Labels)
	}
}

func TestBodyListsChangesVerificationAndModulesHeldBackByADependency(t *testing.T) {
	// The held-back section is chosen by matching the planner's wording, so
	// take that wording from the planner rather than restating it here.
	held := addr(t, "hashicorp/google")
	requirement := model.ProviderRequirement{LocalName: "google", Address: held, Source: held.String(), Constraint: ">= 5.0.0"}
	module := &model.Module{
		Dir: "envs/dev", RelDir: "envs/dev", IsRoot: true, HasLockFile: true,
		Requirements: []model.ProviderRequirement{requirement},
		Locks: map[string]model.ProviderLock{held.String(): {
			Address: held, Source: held.String(), Version: "5.40.0",
			// Terraform records the merged constraints of the whole module tree.
			Constraints: ">= 5.0.0, < 5.41.0",
		}},
	}
	var candidates []*version.Version
	for _, raw := range []string{"5.40.0", "5.41.0", "6.0.0"} {
		candidates = append(candidates, version.Must(version.NewVersion(raw)))
	}
	status := upgrade.BuildStatus(module, requirement, candidates, false)
	blocked, reason, err := upgrade.PlanAction(module, status, candidates, upgrade.PlanOptions{Level: upgrade.LevelMajor})
	if err != nil || blocked != nil {
		t.Fatalf("fixture must be held back by its dependency: action=%+v reason=%q err=%v", blocked, reason, err)
	}

	changed := action(t, "envs/prod", "hashicorp/google", "5.40.0", "5.41.0", upgrade.LevelMinor, false)
	changed.Constraint = upgrade.Rewrite{Old: "< 5.41.0", New: "< 5.42.0", Changed: true}
	plan := &upgrade.Plan{
		Actions: []upgrade.Action{changed},
		Skipped: []upgrade.Skip{{Module: "envs/dev", Source: held.String(), Address: held, Reason: reason}},
	}
	pr := Build(plan, []TerraformRun{{Module: "envs/prod", Command: "validate", OK: true}}, []string{"envs/prod/versions.tf"})

	for _, want := range []string{
		"## Changes",
		"| `envs/prod` | `hashicorp/google` | 5.40.0 | 5.41.0 | minor | `< 5.41.0` -> `< 5.42.0` |",
		"## Files changed", "- `envs/prod/versions.tf`",
		"## Verification", "| `envs/prod` | `terraform validate` | :white_check_mark: passed |",
		"## Not upgraded further", "- `hashicorp/google` in `envs/dev`: " + reason,
	} {
		if !strings.Contains(pr.Body, want) {
			t.Errorf("body is missing %q:\n%s", want, pr.Body)
		}
	}
}
