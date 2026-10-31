package cli

import (
	"strings"
	"testing"
)

// multiMajorRepo is locked two majors behind the newest release, which is the
// situation the stepping flag exists for.
func multiMajorRepo(t *testing.T) *harness {
	t.Helper()

	h := newDriftHarness(t, map[string]map[string]string{
		".": moduleFiles("hashicorp/azurerm", ">= 3.0.0", "3.100.0"),
	})
	h.fake.versions = map[string][]string{
		"hashicorp/azurerm": {"3.100.0", "3.117.1", "4.0.0", "4.81.0", "5.0.0", "5.6.0"},
	}
	return h
}

func TestUpgradeDefaultsToNextAvailableRelease(t *testing.T) {
	h := multiMajorRepo(t)

	err := h.run(t, newUpgradeCommand(h.opts),
		"--level", "major", "--allow-major", "--dry-run")
	if err != nil {
		t.Fatalf("upgrade: %v", err)
	}

	if !strings.Contains(h.stdout.String(), "3.117.1") {
		t.Errorf("expected the next published release:\n%s", h.stdout.String())
	}
}

func TestUpgradeNextMajorStepsOneMajor(t *testing.T) {
	h := multiMajorRepo(t)

	err := h.run(t, newUpgradeCommand(h.opts),
		"--level", "major", "--allow-major", "--next-major", "--dry-run")
	if err != nil {
		t.Fatalf("upgrade: %v", err)
	}

	out := h.stdout.String()
	if !strings.Contains(out, "4.0.0") {
		t.Errorf("expected the first release in the 4.x series:\n%s", out)
	}
	// Crossing two migration guides in one move is exactly what this prevents.
	if strings.Contains(out, "5.6.0") {
		t.Errorf("must not reach 5.x in a single step:\n%s", out)
	}
}

func TestUpgradeNextMajorRequiresMajorLevel(t *testing.T) {
	h := multiMajorRepo(t)

	err := h.run(t, newUpgradeCommand(h.opts), "--level", "minor", "--next-major")
	if err == nil {
		t.Fatal("expected an error")
	}
	if !strings.Contains(err.Error(), "--next-major requires --level major") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestUpgradeNextMajorConflictsWithExplicitVersion(t *testing.T) {
	h := multiMajorRepo(t)

	err := h.run(t, newUpgradeCommand(h.opts),
		"--provider", "azurerm", "--to", "5.6.0",
		"--level", "major", "--allow-major", "--next-major")
	if err == nil {
		t.Fatal("expected an error")
	}
	if !strings.Contains(err.Error(), "cannot be combined with --to") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestUpgradeNextMajorStillRequiresAcknowledgement(t *testing.T) {
	h := multiMajorRepo(t)

	err := h.run(t, newUpgradeCommand(h.opts),
		"--level", "major", "--next-major", "--dry-run")
	if err == nil {
		t.Fatal("expected a refusal without --allow-major")
	}
	if !strings.Contains(err.Error(), "--allow-major") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestUpgradeNextMajorReportsWhenAlreadyCurrent(t *testing.T) {
	h := newDriftHarness(t, map[string]map[string]string{
		".": moduleFiles("hashicorp/azurerm", ">= 5.0.0", "5.6.0"),
	})
	h.fake.versions = map[string][]string{
		"hashicorp/azurerm": {"5.0.0", "5.6.0"},
	}

	err := h.run(t, newUpgradeCommand(h.opts),
		"--level", "major", "--allow-major", "--next-major", "--dry-run")
	if err != nil {
		t.Fatalf("upgrade: %v", err)
	}

	out := h.stdout.String()
	if !strings.Contains(out, "Nothing to upgrade") {
		t.Errorf("expected nothing to do:\n%s", out)
	}
	if !strings.Contains(out, "no major version newer") {
		t.Errorf("expected an explanatory reason:\n%s", out)
	}
}
