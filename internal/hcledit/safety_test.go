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

// isolateJournal points the recovery journal at a temporary directory.
func isolateJournal(t *testing.T) {
	t.Helper()
	cache := t.TempDir()
	CacheDir = func() (string, error) { return cache, nil }
	t.Cleanup(func() { CacheDir = os.UserCacheDir })
}

// interruptReplace stops every replacement once the original has been moved
// to its backup, as if the process died between the two renames. The
// returned function lets later renames through again.
func interruptReplace(t *testing.T) func() {
	t.Helper()
	renameFile = func(from, to string) error {
		if strings.HasSuffix(to, ".provlift-backup") {
			return os.Rename(from, to)
		}
		return errors.New("simulated interruption")
	}
	resume := func() { renameFile = os.Rename }
	t.Cleanup(resume)
	return resume
}

// interruptedWrite leaves path as an interrupted replacement would: the
// original moved to its backup and recorded in the journal.
func interruptedWrite(t *testing.T, path string) {
	t.Helper()
	resume := interruptReplace(t)
	defer resume()
	if err := writeFileAtomic(path, []byte("partially written")); err == nil {
		t.Fatal("replacement was not interrupted")
	}
	if _, err := os.Stat(path + ".provlift-backup"); err != nil {
		t.Fatalf("interrupted replacement left no backup: %v", err)
	}
}

func assertNoJournal(t *testing.T) {
	t.Helper()
	cache, err := CacheDir()
	if err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(filepath.Join(cache, "provlift", "recovery"))
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	for _, entry := range entries {
		t.Errorf("unexpected journal entry: %s", entry.Name())
	}
}

func assertContent(t *testing.T, path, want string) {
	t.Helper()
	got, err := os.ReadFile(path)
	if err != nil || string(got) != want {
		t.Errorf("%s = %q, error = %v; want %q", path, got, err, want)
	}
}

func safetyFixture(t *testing.T) (string, []Edit) {
	t.Helper()
	isolateJournal(t)
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
			resume := interruptReplace(t)
			defer resume()
			if err := writeFileAtomic(path, content); err != nil {
				return err
			}
			return errors.New("replacement was not interrupted")
		}
		return writeFileAtomic(path, content)
	})
	if err == nil {
		t.Fatal("expected an error after interrupting the second file")
	}
	for path, original := range originals {
		got, err := os.ReadFile(path)
		if err != nil || !bytes.Equal(got, original) {
			t.Errorf("%s not restored: %v", path, err)
		}
	}
	assertNoArtifacts(t, dir)
	assertNoJournal(t)
}

func TestApplyRecoversInterruptedBackupBeforeRendering(t *testing.T) {
	dir, edits := safetyFixture(t)
	path := filepath.Join(dir, "a.tf")
	interruptedWrite(t, path)
	if _, err := Apply(edits); err != nil {
		t.Fatalf("Apply should restore backup before rendering: %v", err)
	}
	got, err := os.ReadFile(path)
	if err != nil || !bytes.Contains(got, []byte(`"~> 5.0"`)) {
		t.Fatalf("recovered file = %q, error = %v", got, err)
	}
	assertNoArtifacts(t, dir)
	assertNoJournal(t)
}

func TestApplyRefusesUnrecordedBackup(t *testing.T) {
	dir, edits := safetyFixture(t)
	path := filepath.Join(dir, "a.tf")
	original, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path+".provlift-backup", []byte("unexpected"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Apply(edits); err == nil || !strings.Contains(err.Error(), "no record of creating it") {
		t.Fatalf("expected unrecorded backup refusal, got %v", err)
	}
	assertContent(t, path, string(original))
	assertContent(t, path+".provlift-backup", "unexpected")
}

func TestReplaceFallbackClearsJournal(t *testing.T) {
	isolateJournal(t)
	path := filepath.Join(t.TempDir(), "main.tf")
	if err := os.WriteFile(path, []byte("original"), 0o600); err != nil {
		t.Fatal(err)
	}
	// Refuse only the first direct rename, forcing the backup path.
	refused := false
	renameFile = func(from, to string) error {
		if !refused && to == path {
			refused = true
			return errors.New("destination in use")
		}
		return os.Rename(from, to)
	}
	t.Cleanup(func() { renameFile = os.Rename })
	if err := writeFileAtomic(path, []byte("updated")); err != nil {
		t.Fatal(err)
	}
	if !refused {
		t.Fatal("backup path was not exercised")
	}
	assertContent(t, path, "updated")
	assertNoArtifacts(t, filepath.Dir(path))
	assertNoJournal(t)
}

func TestReplaceWithoutCacheDirKeepsOriginal(t *testing.T) {
	CacheDir = func() (string, error) { return "", errors.New("no cache directory") }
	t.Cleanup(func() { CacheDir = os.UserCacheDir })
	path := filepath.Join(t.TempDir(), "main.tf")
	if err := os.WriteFile(path, []byte("original"), 0o600); err != nil {
		t.Fatal(err)
	}
	interruptReplace(t)
	if err := writeFileAtomic(path, []byte("updated")); err == nil || !strings.Contains(err.Error(), "recording backup") {
		t.Fatalf("expected the backup to be refused, got %v", err)
	}
	assertContent(t, path, "original")
	assertNoArtifacts(t, filepath.Dir(path))
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
	isolateJournal(t)
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
	path := filepath.Join(module, "main.tf")
	if err := os.WriteFile(path, []byte("original"), 0o600); err != nil {
		t.Fatal(err)
	}
	interruptedWrite(t, path)
	if err := Recover(dir, []string{".backup/**", "custom", "**/ignored/**"}); err != nil {
		t.Fatal(err)
	}
	for _, name := range excluded {
		if _, err := os.Stat(filepath.Join(dir, name, "main.tf.provlift-backup")); err != nil {
			t.Errorf("backup inside %s should be untouched: %v", name, err)
		}
		if _, err := os.Stat(filepath.Join(dir, name, "main.tf")); !os.IsNotExist(err) {
			t.Errorf("backup inside %s should not be restored: %v", name, err)
		}
	}
	assertContent(t, path, "original")
	if _, err := os.Stat(path + ".provlift-backup"); !os.IsNotExist(err) {
		t.Errorf("restored module backup should be removed: %v", err)
	}
	assertNoJournal(t)
}

func TestRecoverRefusesUnrecordedBackup(t *testing.T) {
	for _, scenario := range []struct {
		name   string
		target bool
	}{
		{"with target", true},
		{"without target", false},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			isolateJournal(t)
			dir := t.TempDir()
			path := filepath.Join(dir, "main.tf")
			backup := path + ".provlift-backup"
			if err := os.WriteFile(backup, []byte("unexpected"), 0o600); err != nil {
				t.Fatal(err)
			}
			if scenario.target {
				if err := os.WriteFile(path, []byte("original"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			err := Recover(dir, nil)
			if err == nil || !strings.Contains(err.Error(), backup) || !strings.Contains(err.Error(), "delete it if it is unexpected") {
				t.Fatalf("expected refusal naming %s, got %v", backup, err)
			}
			assertContent(t, backup, "unexpected")
			if scenario.target {
				assertContent(t, path, "original")
			} else if _, err := os.Stat(path); !os.IsNotExist(err) {
				t.Errorf("target should not be created: %v", err)
			}
		})
	}
}

func TestRecoverRefusesBackupThatDiffersFromJournal(t *testing.T) {
	isolateJournal(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "main.tf")
	if err := os.WriteFile(path, []byte("original"), 0o600); err != nil {
		t.Fatal(err)
	}
	interruptedWrite(t, path)
	if err := os.WriteFile(path+".provlift-backup", []byte("altered"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := Recover(dir, nil); err == nil || !strings.Contains(err.Error(), "does not match") {
		t.Fatalf("expected mismatch refusal, got %v", err)
	}
	assertContent(t, path+".provlift-backup", "altered")
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("target should not be restored: %v", err)
	}
}

func TestRecoverChecksEveryBackupBeforeRestoring(t *testing.T) {
	isolateJournal(t)
	dir := t.TempDir()
	recorded := filepath.Join(dir, "a", "main.tf")
	unexpected := filepath.Join(dir, "b", "main.tf.provlift-backup")
	for _, path := range []string{recorded, unexpected} {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("original"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	interruptedWrite(t, recorded)
	if err := Recover(dir, nil); err == nil || !strings.Contains(err.Error(), unexpected) {
		t.Fatalf("expected refusal naming %s, got %v", unexpected, err)
	}
	if _, err := os.Stat(recorded); !os.IsNotExist(err) {
		t.Fatalf("no backup should be restored while another is refused: %v", err)
	}
	if err := os.Remove(unexpected); err != nil {
		t.Fatal(err)
	}
	if err := Recover(dir, nil); err != nil {
		t.Fatal(err)
	}
	assertContent(t, recorded, "original")
	assertNoJournal(t)
}

func TestRecoverWithoutCacheDirRestoresNothing(t *testing.T) {
	CacheDir = func() (string, error) { return "", errors.New("no cache directory") }
	t.Cleanup(func() { CacheDir = os.UserCacheDir })
	dir := t.TempDir()
	backup := filepath.Join(dir, "main.tf.provlift-backup")
	if err := os.WriteFile(backup, []byte("unexpected"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := Recover(dir, nil); err == nil || !strings.Contains(err.Error(), "recovery journal unavailable") {
		t.Fatalf("expected refusal without a journal, got %v", err)
	}
	assertContent(t, backup, "unexpected")
	if _, err := os.Stat(filepath.Join(dir, "main.tf")); !os.IsNotExist(err) {
		t.Errorf("target should not be created: %v", err)
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

func TestCompletedReplacementForgetsStaleRecord(t *testing.T) {
	// A run that died after recording a backup but before moving the original
	// aside leaves a record and no backup. The next completed write must drop
	// that record, so a backup holding the old bytes is not trusted later.
	isolateJournal(t)
	root := t.TempDir()
	path := filepath.Join(root, "main.tf")
	if err := os.WriteFile(path, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := recordBackup(path, []byte("old")); err != nil {
		t.Fatal(err)
	}
	if err := writeFileAtomic(path, []byte("new")); err != nil {
		t.Fatal(err)
	}
	assertNoJournal(t)

	if err := os.WriteFile(path+".provlift-backup", []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := Recover(root, nil); err == nil {
		t.Fatal("restored a backup whose record should have been forgotten")
	}
	assertContent(t, path, "new")
}

func TestRecoverRefusesJournalInsideTree(t *testing.T) {
	// A journal inside the tree being edited could have been supplied by it.
	root := t.TempDir()
	cache := filepath.Join(root, ".cache")
	CacheDir = func() (string, error) { return cache, nil }
	t.Cleanup(func() { CacheDir = os.UserCacheDir })

	path := filepath.Join(root, "main.tf")
	if err := os.WriteFile(path, []byte("original"), 0o644); err != nil {
		t.Fatal(err)
	}
	interruptedWrite(t, path)

	err := Recover(root, nil)
	if err == nil || !strings.Contains(err.Error(), "recovery journal") {
		t.Fatalf("Recover() error = %v, want refusal naming the recovery journal", err)
	}
	if _, err := os.Stat(path + ".provlift-backup"); err != nil {
		t.Errorf("backup should be left in place: %v", err)
	}
}

func TestRecoverRefusesBackupNamedDirectory(t *testing.T) {
	// A directory with a backup's name must stop recovery before any recorded
	// backup elsewhere is restored.
	isolateJournal(t)
	root := t.TempDir()
	path := filepath.Join(root, "a.tf")
	if err := os.WriteFile(path, []byte("original"), 0o644); err != nil {
		t.Fatal(err)
	}
	interruptedWrite(t, path)
	if err := os.Mkdir(filepath.Join(root, "b.tf.provlift-backup"), 0o755); err != nil {
		t.Fatal(err)
	}

	if err := Recover(root, nil); err == nil || !strings.Contains(err.Error(), "b.tf.provlift-backup") {
		t.Fatalf("Recover() error = %v, want refusal naming the directory", err)
	}
	if _, err := os.Stat(path + ".provlift-backup"); err != nil {
		t.Errorf("recorded backup should not have been restored yet: %v", err)
	}
}
