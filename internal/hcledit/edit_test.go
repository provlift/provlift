package hcledit_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/provlift/provlift/internal/discovery"
	"github.com/provlift/provlift/internal/hcledit"
	"github.com/provlift/provlift/internal/model"
)

const sample = `terraform {
  required_providers {
    # Keep this comment.
    github = {
      source  = "integrations/github"
      version = "~> 6.0"
    }

    mongodbatlas = {
      source = "mongodb/mongodbatlas"
    }
  }
}

# Trailing comment.
`

func writeFixture(t *testing.T, content string) (dir string) {
	t.Helper()
	dir = t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "main.tf"), []byte(content), 0o644); err != nil {
		t.Fatalf("writing fixture: %v", err)
	}
	return dir
}

func requirement(t *testing.T, dir, localName string) model.ProviderRequirement {
	t.Helper()

	inv, err := discovery.Scan(discovery.Options{Root: dir})
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	for _, m := range inv.Modules {
		for _, r := range m.Requirements {
			if r.LocalName == localName {
				return r
			}
		}
	}
	t.Fatalf("requirement %q not found", localName)
	return model.ProviderRequirement{}
}

func TestApplyReplacesVersionAndPreservesEverythingElse(t *testing.T) {
	dir := writeFixture(t, sample)
	req := requirement(t, dir, "github")

	if _, err := hcledit.Apply([]hcledit.Edit{{Requirement: req, New: "~> 7.0"}}); err != nil {
		t.Fatalf("Apply: %v", err)
	}

	got := readFile(t, filepath.Join(dir, "main.tf"))

	if !strings.Contains(got, `version = "~> 7.0"`) {
		t.Errorf("constraint was not updated:\n%s", got)
	}
	if strings.Contains(got, `"~> 6.0"`) {
		t.Errorf("old constraint still present:\n%s", got)
	}
	if !strings.Contains(got, "# Keep this comment.") {
		t.Error("an unrelated comment was lost")
	}
	if !strings.Contains(got, "# Trailing comment.") {
		t.Error("the trailing comment was lost")
	}
	if !strings.Contains(got, `source  = "integrations/github"`) {
		t.Error("existing alignment was disturbed")
	}
	if !strings.Contains(got, `source = "mongodb/mongodbatlas"`) {
		t.Error("an untouched requirement was rewritten")
	}
}

func TestApplyInsertsVersionWhenAbsent(t *testing.T) {
	dir := writeFixture(t, sample)
	req := requirement(t, dir, "mongodbatlas")

	if req.VersionRange != nil {
		t.Fatal("fixture should have no version argument for mongodbatlas")
	}

	if _, err := hcledit.Apply([]hcledit.Edit{{Requirement: req, New: "~> 1.25"}}); err != nil {
		t.Fatalf("Apply: %v", err)
	}

	got := readFile(t, filepath.Join(dir, "main.tf"))
	if !strings.Contains(got, `version = "~> 1.25"`) {
		t.Errorf("version argument was not inserted:\n%s", got)
	}
	if !strings.Contains(got, `version = "~> 6.0"`) {
		t.Error("the github constraint should be untouched")
	}
}

func TestApplyRejectsStaleObjectInsertion(t *testing.T) {
	dir := writeFixture(t, sample)
	req := requirement(t, dir, "mongodbatlas")
	path := filepath.Join(dir, "main.tf")
	content := readFile(t, path)
	content = strings.Replace(content, `source = "mongodb/mongodbatlas"`, `source = "other/provider"`, 1)
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("rewriting fixture: %v", err)
	}
	if _, err := hcledit.Apply([]hcledit.Edit{{Requirement: req, New: "~> 1.25"}}); err == nil {
		t.Fatal("expected an error when the recorded object changed")
	}
}

func TestApplyHandlesSeveralEditsInOneFile(t *testing.T) {
	dir := writeFixture(t, sample)

	github := requirement(t, dir, "github")
	atlas := requirement(t, dir, "mongodbatlas")

	if _, err := hcledit.Apply([]hcledit.Edit{
		{Requirement: github, New: "~> 7.0"},
		{Requirement: atlas, New: "~> 1.25"},
	}); err != nil {
		t.Fatalf("Apply: %v", err)
	}

	got := readFile(t, filepath.Join(dir, "main.tf"))
	if !strings.Contains(got, `version = "~> 7.0"`) {
		t.Errorf("first edit missing:\n%s", got)
	}
	if !strings.Contains(got, `version = "~> 1.25"`) {
		t.Errorf("second edit missing:\n%s", got)
	}
}

func TestApplyRejectsStaleRanges(t *testing.T) {
	dir := writeFixture(t, sample)
	req := requirement(t, dir, "github")

	// Simulate the file changing between the scan and the write.
	if err := os.WriteFile(filepath.Join(dir, "main.tf"), []byte("terraform {}\n"), 0o644); err != nil {
		t.Fatalf("rewriting fixture: %v", err)
	}

	if _, err := hcledit.Apply([]hcledit.Edit{{Requirement: req, New: "~> 7.0"}}); err == nil {
		t.Fatal("expected an error when the recorded byte range no longer matches")
	}
}

func TestApplyRejectsUnrelatedChangeSinceScan(t *testing.T) {
	dir := writeFixture(t, sample)
	req := requirement(t, dir, "github")
	path := filepath.Join(dir, "main.tf")
	changed := strings.Replace(sample, "# Trailing comment.", "# A changed comment.", 1)
	if err := os.WriteFile(path, []byte(changed), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := hcledit.Apply([]hcledit.Edit{{Requirement: req, New: "~> 7.0"}}); err == nil || !strings.Contains(err.Error(), "changed since it was scanned") {
		t.Fatalf("expected stale-scan refusal, got %v", err)
	}
	if got := readFile(t, path); got != changed {
		t.Fatal("stale scan modified the file")
	}
}

func TestRenderDoesNotWriteToDisk(t *testing.T) {
	dir := writeFixture(t, sample)
	req := requirement(t, dir, "github")

	rendered, err := hcledit.Render([]hcledit.Edit{{Requirement: req, New: "~> 7.0"}})
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if len(rendered) != 1 || !rendered[0].Changed {
		t.Fatalf("Render returned %+v, want one changed file", rendered)
	}
	if !strings.Contains(string(rendered[0].Content), `version = "~> 7.0"`) {
		t.Error("rendered content does not contain the new constraint")
	}

	onDisk := readFile(t, filepath.Join(dir, "main.tf"))
	if !strings.Contains(onDisk, `version = "~> 6.0"`) {
		t.Error("Render must not modify the file on disk")
	}
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}
	return string(b)
}
