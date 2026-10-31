package upgrade

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	version "github.com/hashicorp/go-version"

	"github.com/provlift/provlift/internal/terraform"
)

func TestDetectShape(t *testing.T) {
	tests := []struct {
		constraint string
		want       Shape
	}{
		{"", ShapeEmpty},
		{"   ", ShapeEmpty},
		{"6.0.1", ShapeExact},
		{"= 6.0.1", ShapeExact},
		{"~> 6.0", ShapePessimistic},
		{"~> 6.2.0", ShapePessimistic},
		{">= 3.0", ShapeLowerBound},
		{">= 3.0, < 4.0", ShapeRange},
		{">= 3.0.0, <= 3.0.0", ShapeRange},
		{">= 3.0, < 2.0", ShapeUnsupported},
		{">= 3.0, < 3.0", ShapeUnsupported},
		{">= 3.0, != 3.5.0", ShapeUnsupported},
		{">= 1.0, >= 2.0", ShapeUnsupported},
		{">= 1.0, < 2.0, != 1.5.0", ShapeUnsupported},
		{"not-a-version", ShapeUnsupported},
	}

	for _, tc := range tests {
		t.Run(tc.constraint, func(t *testing.T) {
			if got := DetectShape(tc.constraint); got != tc.want {
				t.Errorf("DetectShape(%q) = %s, want %s", tc.constraint, got, tc.want)
			}
		})
	}
}

func TestRewriteConstraintLeavesSatisfiedConstraintsAlone(t *testing.T) {
	// The most important behaviour in the tool: when the declared constraint
	// already permits the target, only the lock file should move.
	tests := []struct {
		constraint string
		target     string
	}{
		{"~> 6.0", "6.3.1"},
		{"~> 6.2.0", "6.2.5"},
		{">= 3.0", "5.0.0"},
		{">= 3.0, < 4.0", "3.9.0"},
	}

	for _, tc := range tests {
		t.Run(tc.constraint+" -> "+tc.target, func(t *testing.T) {
			got, err := RewriteConstraint(tc.constraint, mustVersion(t, tc.target))
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got.Changed {
				t.Errorf("constraint %q was rewritten to %q for target %s, but it already allowed it",
					tc.constraint, got.New, tc.target)
			}
			if got.New != tc.constraint {
				t.Errorf("New = %q, want %q", got.New, tc.constraint)
			}
		})
	}
}

func TestRewriteConstraintPreservesStyle(t *testing.T) {
	tests := []struct {
		name       string
		constraint string
		target     string
		want       string
	}{
		{"pessimistic major bump tracks the new series", "~> 6.0", "7.0.0", "~> 7.0"},
		{"pessimistic major bump from a non-zero minor", "~> 6.4", "7.2.0", "~> 7.0"},
		{"three-segment pessimistic keeps precision", "~> 6.2.0", "6.4.1", "~> 6.4.1"},
		{"explicit equals is preserved", "= 3.1.0", "3.2.0", "= 3.2.0"},
		{"bare pin stays bare", "3.1.0", "3.2.0", "3.2.0"},
		{"short exact pin retains the full target", "= 3.1", "3.2.4", "= 3.2.4"},
		{"short bare pin retains the full target", "3.1", "3.2.4", "3.2.4"},
		{"range moves both bounds", ">= 3.0, < 4.0", "5.1.2", ">= 5.1, < 6.0"},
		{"range with three-segment bounds", ">= 3.0.0, < 4.0.0", "5.1.2", ">= 5.1.2, < 6.0.0"},
		{"strict lower range includes the target", "> 3.0.0, < 4.0.0", "5.1.2", ">= 5.1.2, < 6.0.0"},
		{"inclusive upper bound retains patch precision", ">= 3.0, <= 4.0", "5.1.2", ">= 5.1, <= 5.1.2"},
		{"prerelease exact pin", "= 3.0.0", "5.0.0-beta.1", "= 5.0.0-beta.1"},
		{"prerelease pessimistic becomes exact", "~> 3.0", "5.0.0-beta.1", "= 5.0.0-beta.1"},
		{"prerelease lower bound becomes exact", ">= 3.0", "5.0.0-beta.1", "= 5.0.0-beta.1"},
		{"prerelease range becomes exact", ">= 3.0, < 4.0", "5.0.0-beta.1", "= 5.0.0-beta.1"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := RewriteConstraint(tc.constraint, mustVersion(t, tc.target))
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if !got.Changed {
				t.Fatalf("expected constraint %q to change for target %s", tc.constraint, tc.target)
			}
			if got.New != tc.want {
				t.Errorf("New = %q, want %q", got.New, tc.want)
			}

			// Whatever we produced must actually admit the target.
			ok, err := Satisfies(got.New, mustVersion(t, tc.target))
			if err != nil {
				t.Fatalf("rewritten constraint %q is invalid: %v", got.New, err)
			}
			if !ok {
				t.Errorf("rewritten constraint %q does not allow target %s", got.New, tc.target)
			}
		})
	}
}

func TestRewriteConstraintEmptyIsLeftAloneByDefault(t *testing.T) {
	got, err := RewriteConstraint("", mustVersion(t, "6.1.0"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.Changed {
		t.Errorf("an absent constraint should not be added implicitly, got %q", got.New)
	}
	if got.Reason == "" {
		t.Error("expected a reason explaining why nothing changed")
	}
}

func TestRewriteConstraintPinUnconstrained(t *testing.T) {
	got, err := RewriteConstraintPinned("", mustVersion(t, "6.1.3"), true)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !got.Changed || got.New != "~> 6.1" {
		t.Errorf("New = %q (changed=%v), want \"~> 6.1\"", got.New, got.Changed)
	}
}

func TestPrereleaseConstraintCreationAdmitsTarget(t *testing.T) {
	target := mustVersion(t, "5.0.0-beta.1")
	result, err := RewriteConstraintPinned("", target, true)
	if err != nil {
		t.Fatal(err)
	}
	if result.New != "= 5.0.0-beta.1" {
		t.Fatalf("Terraform requires an exact prerelease pin, got %q", result.New)
	}
}

func TestPrereleaseSatisfactionMatchesTerraform(t *testing.T) {
	target := mustVersion(t, "5.0.0-beta.1")
	for _, constraint := range []string{"", ">= 3.0", ">= 5.0.0-beta.1", "~> 5.0.0-beta.1", ">= 5.0.0-beta.1, <= 5.0.0-beta.1",
		"5.0.0-beta.1, >= 3.0", "5.0.0-beta.1, = 5.0.0-beta.2", "= 5.0.0-beta.2, 5.0.0-beta.1"} {
		if allowed, err := Satisfies(constraint, target); err != nil || allowed {
			t.Errorf("Terraform must not select prereleases using %q: allowed=%t, err=%v", constraint, allowed, err)
		}
	}
	for _, constraint := range []string{"= 5.0.0-beta.1", "5.0.0-beta.1",
		"5.0.0-beta.1, = 5.0.0-beta.1", "= 5.0.0-beta.1, 5.0.0-beta.1", "= 5.0.0-beta.1, = 5.0.0-beta.1"} {
		if allowed, err := Satisfies(constraint, target); err != nil || !allowed {
			t.Errorf("exact prerelease pin refused: %q: %v", constraint, err)
		}
	}
}

func TestPrereleaseRewriteRejectsLowerBoundDowngrade(t *testing.T) {
	for _, constraint := range []string{">= 5.0.0", "> 5.0.0", ">= 5.0.0, < 6.0.0", "< 6.0.0, >= 5.0.0", "> 5.0.0, <= 6.0.0"} {
		t.Run(constraint, func(t *testing.T) {
			for _, target := range []string{"4.66.0", "4.66.0-beta.1", "5.0.0-beta.1"} {
				result, err := RewriteConstraint(constraint, mustVersion(t, target))
				if !errors.Is(err, ErrDowngrade) {
					t.Errorf("target %s below %q must return ErrDowngrade, got %v", target, constraint, err)
				}
				if result.Changed || result.New != constraint {
					t.Errorf("refused target %s changed %q to %q", target, constraint, result.New)
				}
			}
		})
	}
}

func TestPrereleaseFloorRaisesAreRefused(t *testing.T) {
	for _, constraint := range []string{"", ">= 3.0", "~> 3.0", ">= 3.0, < 6.0"} {
		_, err := RaiseConstraintFloor(constraint, mustVersion(t, "5.0.0-beta.1"))
		if !errors.Is(err, ErrUnsupportedConstraint) {
			t.Errorf("prerelease floor %q must be refused: %v", constraint, err)
		}
	}
}

func TestPrereleaseExactPinWithTerraform(t *testing.T) {
	binary, err := exec.LookPath("terraform")
	if err != nil {
		t.Skip("Terraform executable is required for the offline prerelease selection check")
	}
	root := t.TempDir()
	mirror := t.TempDir()
	version := "5.0.0-beta.1"
	packageDir := filepath.Join(mirror, "provlift.example.com", "testing", "fixture", version, runtime.GOOS+"_"+runtime.GOARCH)
	if err := os.MkdirAll(packageDir, 0o755); err != nil {
		t.Fatal(err)
	}
	packageName := "terraform-provider-fixture_v" + version
	if runtime.GOOS == "windows" {
		packageName += ".exe"
	}
	if err := os.WriteFile(filepath.Join(packageDir, packageName), []byte("init-only provider fixture"), 0o755); err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(t.TempDir(), "terraform.rc")
	config := fmt.Sprintf("disable_checkpoint = true\nprovider_installation {\n  filesystem_mirror {\n    path = %q\n    include = [\"provlift.example.com/*/*\"]\n  }\n}\n", filepath.ToSlash(mirror))
	if err := os.WriteFile(configPath, []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}
	runner := &terraform.Runner{Binary: binary, Timeout: 30 * time.Second, ExtraEnv: []string{
		"TF_CLI_CONFIG_FILE=" + configPath, "CHECKPOINT_DISABLE=1",
	}}
	writeConstraint := func(constraint string) {
		t.Helper()
		content := fmt.Sprintf("terraform {\n  required_providers {\n    fixture = {\n      source = \"provlift.example.com/testing/fixture\"\n      version = %q\n    }\n  }\n}\n", constraint)
		if err := os.WriteFile(filepath.Join(root, "main.tf"), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	writeConstraint(">= " + version + ", <= " + version)
	result, err := runner.Init(context.Background(), root, terraform.InitOptions{})
	if err != nil || result.ExitCode == 0 {
		t.Fatalf("expected Terraform to reject prerelease range: %v\n%s\n%s", err, result.Stdout, result.Stderr)
	}
	rewrite, err := RewriteConstraint(">= 3.0", mustVersion(t, version))
	if err != nil {
		t.Fatal(err)
	}
	writeConstraint(rewrite.New)
	for _, upgrade := range []bool{true, false} {
		result, err := runner.Init(context.Background(), root, terraform.InitOptions{Upgrade: upgrade})
		if err != nil || result.ExitCode != 0 {
			t.Fatalf("Terraform init (upgrade=%t) rejected permanent prerelease pin: %v\n%s\n%s", upgrade, err, result.Stdout, result.Stderr)
		}
	}
}

func TestRewriteConstraintRejectsDowngrade(t *testing.T) {
	_, err := RewriteConstraint(">= 5.0", mustVersion(t, "4.0.0"))
	if !errors.Is(err, ErrDowngrade) {
		t.Errorf("err = %v, want ErrDowngrade", err)
	}
}

func TestRewriteConstraintRejectsUnsupportedShapes(t *testing.T) {
	// A constraint the tool cannot reason about must fail loudly rather than
	// be silently widened.
	_, err := RewriteConstraint(">= 1.0.0, != 2.0.0", mustVersion(t, "2.0.0"))
	if !errors.Is(err, ErrUnsupportedConstraint) {
		t.Errorf("err = %v, want ErrUnsupportedConstraint", err)
	}
}

func TestSatisfies(t *testing.T) {
	tests := []struct {
		constraint string
		v          string
		want       bool
	}{
		{"", "9.9.9", true},
		{"~> 6.0", "6.9.9", true},
		{"~> 6.0", "7.0.0", false},
		{"~> 6.2.0", "6.2.9", true},
		{"~> 6.2.0", "6.3.0", false},
		{">= 3.0, < 4.0", "3.5.0", true},
		{">= 3.0, < 4.0", "4.0.0", false},
	}

	for _, tc := range tests {
		t.Run(tc.constraint+" / "+tc.v, func(t *testing.T) {
			got, err := Satisfies(tc.constraint, mustVersion(t, tc.v))
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tc.want {
				t.Errorf("Satisfies(%q, %s) = %v, want %v", tc.constraint, tc.v, got, tc.want)
			}
		})
	}
}

func TestNextMajor(t *testing.T) {
	if got := NextMajor(mustVersion(t, "6.4.2")); got.String() != "7.0.0" {
		t.Errorf("NextMajor(6.4.2) = %s, want 7.0.0", got)
	}
}

func TestRaiseConstraintFloor(t *testing.T) {
	// Raising a floor differs from a normal upgrade: the lower bound moves even
	// when the existing constraint would already admit the target.
	tests := []struct {
		name       string
		constraint string
		target     string
		want       string
	}{
		{"lower bound moves even though it already allows", ">= 3.85.0", "5.6.0", ">= 5.6.0"},
		{"lower bound keeps its precision", ">= 3.85", "5.6.0", ">= 5.6"},
		{"strict lower bound becomes inclusive", "> 3.85.0", "5.6.0", ">= 5.6.0"},
		{"range floor moves, ceiling stays when still valid", ">= 3.0.0, < 6.0.0", "5.6.0", ">= 5.6.0, < 6.0.0"},
		{"range ceiling is pushed out when passed", ">= 3.47.0, < 4.0.0", "5.6.0", ">= 5.6.0, < 6.0.0"},
		{"strict range floor becomes inclusive", "> 3.0.0, < 6.0.0", "5.6.0", ">= 5.6.0, < 6.0.0"},
		{"inclusive range ceiling retains patch precision", ">= 3.0, <= 4.0", "5.6.1", ">= 5.6, <= 5.6.1"},
		{"pessimistic retargets", "~> 1.2.21", "1.2.34", "~> 1.2.34"},
		{"pessimistic two segment", "~> 4.65", "4.81.0", "~> 4.81"},
		{"exact pin moves", "3.4.0", "3.9.0", "3.9.0"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := RaiseConstraintFloor(tc.constraint, mustVersion(t, tc.target))
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got.New != tc.want {
				t.Errorf("New = %q, want %q", got.New, tc.want)
			}
			if !got.Changed {
				t.Errorf("expected a change from %q to %q", tc.constraint, tc.want)
			}

			// The result must still admit the version it was raised to.
			ok, err := Satisfies(got.New, mustVersion(t, tc.target))
			if err != nil {
				t.Fatalf("rewritten constraint %q is invalid: %v", got.New, err)
			}
			if !ok {
				t.Errorf("raised constraint %q does not allow %s", got.New, tc.target)
			}
		})
	}
}

func TestRaiseConstraintFloorAddsOneWhenAbsent(t *testing.T) {
	got, err := RaiseConstraintFloor("", mustVersion(t, "5.6.0"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.New != ">= 5.6.0" || !got.Changed {
		t.Errorf("New = %q (changed=%v), want \">= 5.6.0\"", got.New, got.Changed)
	}
}

func TestRaiseConstraintFloorNoOpWhenAlreadyThere(t *testing.T) {
	got, err := RaiseConstraintFloor(">= 5.6.0", mustVersion(t, "5.6.0"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.Changed {
		t.Errorf("floor is already 5.6.0; got a change to %q", got.New)
	}
}

func TestRaiseConstraintFloorRejectsUnsupportedShapes(t *testing.T) {
	_, err := RaiseConstraintFloor(">= 1.0.0, != 2.0.0", mustVersion(t, "3.0.0"))
	if !errors.Is(err, ErrUnsupportedConstraint) {
		t.Errorf("err = %v, want ErrUnsupportedConstraint", err)
	}
}

func TestConstraintShapesRoundTrip(t *testing.T) {
	// Every supported shape is rewritten into the same shape, the result
	// admits its target, and reconciling the result with the same target is a
	// no-op. Lower bounds already admit every newer release, so only a floor
	// raise exercises them.
	tests := []struct {
		shape      string
		constraint string
		target     string
		raiseFloor bool
		want       string
	}{
		{"exact", "= 3.1.0", "3.2.0", false, "= 3.2.0"},
		{"bare pin", "3.1.0", "3.2.0", false, "3.2.0"},
		{"two-segment pessimistic", "~> 6.0", "7.0.0", false, "~> 7.0"},
		{"three-segment pessimistic", "~> 6.2.0", "6.4.1", false, "~> 6.4.1"},
		{"inclusive lower bound", ">= 3.85.0", "5.6.0", true, ">= 5.6.0"},
		{"strict lower bound", "> 3.85.0", "5.6.0", true, ">= 5.6.0"},
		{"exclusive upper range", ">= 3.0, < 4.0", "5.1.2", false, ">= 5.1, < 6.0"},
		{"inclusive upper range", ">= 3.0, <= 4.0", "5.1.2", false, ">= 5.1, <= 5.1.2"},
	}

	for _, tc := range tests {
		t.Run(tc.shape, func(t *testing.T) {
			target := mustVersion(t, tc.target)
			reconcile := RewriteConstraint
			if tc.raiseFloor {
				reconcile = RaiseConstraintFloor
			}

			got, err := reconcile(tc.constraint, target)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if !got.Changed || got.New != tc.want {
				t.Fatalf("New = %q (changed=%t), want %q", got.New, got.Changed, tc.want)
			}
			if before, after := DetectShape(tc.constraint), DetectShape(got.New); before != after {
				t.Errorf("shape changed from %s to %s: %q -> %q", before, after, tc.constraint, got.New)
			}
			if ok, err := Satisfies(got.New, target); err != nil || !ok {
				t.Errorf("rewritten constraint %q does not admit %s (err=%v)", got.New, target, err)
			}

			again, err := reconcile(got.New, target)
			if err != nil {
				t.Fatalf("second pass over %q: %v", got.New, err)
			}
			if again.Changed {
				t.Errorf("second pass rewrote %q to %q", got.New, again.New)
			}
		})
	}
}

func TestUnsupportedShapesAreRefusedWithAnActionableMessage(t *testing.T) {
	// Each constraint blocks its target, so a rewrite is genuinely needed and
	// must be refused rather than guessed at.
	tests := []struct {
		name       string
		constraint string
		target     string
	}{
		{"exclusion", ">= 1.0.0, != 2.0.0", "2.0.0"},
		{"exclusion inside a range", ">= 1.0.0, < 2.0.0, != 1.5.0", "2.1.0"},
		{"two lower bounds", ">= 1.0, >= 2.0, < 2.5", "3.0.0"},
		{"two disjoint ranges", ">= 1.0, < 2.0, >= 3.0, < 4.0", "3.1.0"},
		{"crossed bounds", ">= 3.0, < 2.0", "3.1.0"},
		{"empty strict range", "> 3.0.0, <= 3.0.0", "3.1.0"},
		{"upper bound only", "< 2.0.0", "2.1.0"},
		{"two pessimistic terms", "~> 1.0, ~> 1.2", "2.0.0"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			target := mustVersion(t, tc.target)
			if ok, _ := Satisfies(tc.constraint, target); ok {
				t.Fatalf("fixture error: %q already admits %s", tc.constraint, target)
			}

			for _, reconcile := range []struct {
				name string
				fn   func(string, *version.Version) (Rewrite, error)
			}{
				{"rewrite", RewriteConstraint},
				{"raise floor", RaiseConstraintFloor},
			} {
				name := reconcile.name
				got, err := reconcile.fn(tc.constraint, target)
				if !errors.Is(err, ErrUnsupportedConstraint) {
					t.Fatalf("%s: err = %v, want ErrUnsupportedConstraint", name, err)
				}
				if !strings.Contains(err.Error(), tc.constraint) || !strings.Contains(err.Error(), "by hand") {
					t.Errorf("%s: message should name the constraint and the remedy: %v", name, err)
				}
				if got.Changed || got.New != tc.constraint {
					t.Errorf("%s: refused constraint was still rewritten to %q", name, got.New)
				}
			}
		})
	}
}
