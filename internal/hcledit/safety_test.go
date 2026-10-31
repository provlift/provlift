package hcledit

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/provlift/provlift/internal/discovery"
)

func safetyFixture(t *testing.T) (string, []Edit) {
	t.Helper()
	dir := t.TempDir()
	for _, name := range []string{"a.tf", "b.tf"} {
		provider := "azurerm"
		if name == "b.tf" {
			provider = "github"
		}
		content := []byte("terraform {\r\n  required_providers {\r\n    " + provider + " = \"~> 4.0\" # untouched\r\n  }\r\n}\r\n")
		if err := os.WriteFile(filepath.Join(dir, name), content, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	inv, err := discovery.Scan(discovery.Options{Root: dir})
	if err != nil {
		t.Fatal(err)
	}
	var edits []Edit
	for _, mod := range inv.Modules {
		for _, req := range mod.Requirements {
			edits = append(edits, Edit{Requirement: req, New: "~> 5.0"})
		}
	}
	if len(edits) != 2 {
		t.Fatalf("requirements = %d, want 2", len(edits))
	}
	return dir, edits
}

func TestApplySecondFileFailureRollsBack(t *testing.T) {
	dir, edits := safetyFixture(t)
	first := filepath.Join(dir, "a.tf")
	original, err := os.ReadFile(first)
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	_, err = applyWithWriter(edits, func(path string, content []byte) error {
		calls++
		if calls == 2 {
			return errors.New("second file unwritable")
		}
		return writeFileAtomic(path, content)
	})
	if err == nil || calls != 2 {
		t.Fatalf("error = %v, writes = %d", err, calls)
	}
	got, err := os.ReadFile(first)
	if err != nil || !bytes.Equal(got, original) {
		t.Fatalf("first file not restored: %v", err)
	}
	assertNoArtifacts(t, dir)
}

func TestApplySecondFileFailureAfterReplacementRollsBackBoth(t *testing.T) {
	dir, edits := safetyFixture(t)
	originals := map[string][]byte{}
	for _, name := range []string{"a.tf", "b.tf"} {
		path := filepath.Join(dir, name)
		content, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		originals[path] = content
	}
	calls := 0
	_, err := applyWithWriter(edits, func(path string, content []byte) error {
		calls++
		if err := writeFileAtomic(path, content); err != nil {
			return err
		}
		if calls == 2 {
			return errors.New("backup cleanup failed after replacement")
		}
		return nil
	})
	if err == nil || calls != 2 {
		t.Fatalf("error = %v, writes = %d", err, calls)
	}
	for path, original := range originals {
		got, err := os.ReadFile(path)
		if err != nil || !bytes.Equal(got, original) {
			t.Errorf("%s not restored: %v", path, err)
		}
	}
	assertNoArtifacts(t, dir)
}

func TestApplyFailureAfterReplacementRecoversBackup(t *testing.T) {
	dir, edits := safetyFixture(t)
	first := filepath.Join(dir, "a.tf")
	second := filepath.Join(dir, "b.tf")
	originals := map[string][]byte{}
	for _, path := range []string{first, second} {
		content, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		originals[path] = content
	}
	_, err := applyWithWriter(edits, func(path string, content []byte) error {
		if path == second {
			if err := os.Rename(path, path+".provlift-backup"); err != nil {
				return err
			}
			if err := os.WriteFile(path, content, 0o600); err != nil {
				return err
			}
			return errors.New("failed to remove backup after replacement")
		}
		return writeFileAtomic(path, content)
	})
	if err == nil {
		t.Fatal("expected an error after replacing the second file")
	}
	for path, original := range originals {
		got, err := os.ReadFile(path)
		if err != nil || !bytes.Equal(got, original) {
			t.Errorf("%s not restored: %v", path, err)
		}
	}
	assertNoArtifacts(t, dir)
}

func TestApplyRecoversInterruptedBackupBeforeRendering(t *testing.T) {
	dir, edits := safetyFixture(t)
	path := filepath.Join(dir, "a.tf")
	backup := path + ".provlift-backup"
	if err := os.Rename(path, backup); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("partially written"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Apply(edits); err != nil {
		t.Fatalf("Apply should restore backup before rendering: %v", err)
	}
	got, err := os.ReadFile(path)
	if err != nil || !bytes.Contains(got, []byte(`"~> 5.0"`)) {
		t.Fatalf("recovered file = %q, error = %v", got, err)
	}
	assertNoArtifacts(t, dir)
}

func TestApplyRejectsSymlinkBackup(t *testing.T) {
	dir, edits := safetyFixture(t)
	path := filepath.Join(dir, "a.tf")
	if err := os.Symlink(path, path+".provlift-backup"); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if _, err := Apply(edits); err == nil || !strings.Contains(err.Error(), "non-regular backup") {
		t.Fatalf("expected symlink backup refusal, got %v", err)
	}
}

func TestRecoverSkipsGeneratedDirectories(t *testing.T) {
	dir := t.TempDir()
	excluded := []string{".git", ".terraform", ".terragrunt", ".idea", ".vscode", "node_modules", ".backup", "custom", filepath.Join("envs", "dev", "ignored")}
	for _, name := range excluded {
		generated := filepath.Join(dir, name)
		if err := os.MkdirAll(generated, 0o755); err != nil {
			t.Fatal(err)
		}
		backup := filepath.Join(generated, "main.tf.provlift-backup")
		if err := os.WriteFile(backup, []byte("original"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	module := filepath.Join(dir, "modules")
	if err := os.Mkdir(module, 0o755); err != nil {
		t.Fatal(err)
	}
	backup := filepath.Join(module, "main.tf.provlift-backup")
	if err := os.WriteFile(backup, []byte("original"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := Recover(dir, []string{".backup/**", "custom", "**/ignored/**"}); err != nil {
		t.Fatal(err)
	}
	for _, name := range excluded {
		if _, err := os.Stat(filepath.Join(dir, name, "main.tf.provlift-backup")); err != nil {
			t.Errorf("backup inside %s should be untouched: %v", name, err)
		}
	}
	if got, err := os.ReadFile(filepath.Join(module, "main.tf")); err != nil || string(got) != "original" {
		t.Errorf("module recovery = %q, error = %v", got, err)
	}
	if _, err := os.Stat(backup); !os.IsNotExist(err) {
		t.Errorf("restored module backup should be removed: %v", err)
	}
}

func TestApplyPreservesBytesModeAndCleansArtifacts(t *testing.T) {
	dir, edits := safetyFixture(t)
	modes := map[string]os.FileMode{}
	for _, name := range []string{"a.tf", "b.tf"} {
		info, err := os.Stat(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		modes[name] = info.Mode().Perm()
	}
	if _, err := Apply(edits); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"a.tf", "b.tf"} {
		path := filepath.Join(dir, name)
		got, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		provider := "azurerm"
		if name == "b.tf" {
			provider = "github"
		}
		want := "terraform {\r\n  required_providers {\r\n    " + provider + " = \"~> 5.0\" # untouched\r\n  }\r\n}\r\n"
		if string(got) != want {
			t.Errorf("%s changed unexpected bytes: %q", name, got)
		}
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != modes[name] {
			t.Errorf("%s mode = %v, want %v", name, info.Mode().Perm(), modes[name])
		}
	}
	assertNoArtifacts(t, dir)
}

func assertNoArtifacts(t *testing.T, dir string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.HasSuffix(entry.Name(), ".provlift-backup") || strings.HasPrefix(entry.Name(), ".provlift-") {
			t.Errorf("unexpected write artifact: %s", entry.Name())
		}
	}
}
