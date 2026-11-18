package config

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func writeFiles(t *testing.T, root string, files ...string) {
	t.Helper()
	for _, name := range files {
		path := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func TestLocate(t *testing.T) {
	repo := t.TempDir()
	writeFiles(t, repo, ".git/HEAD", FileName, "envs/prod/main.tf", "modules/vpc/"+FileName, "other/"+FileName)
	sub := filepath.Join(repo, "envs", "prod")

	for _, tc := range []struct {
		name, root, explicit, want string
	}{
		{"scan root's own file", filepath.Join(repo, "modules", "vpc"), "", filepath.Join(repo, "modules", "vpc", FileName)},
		{"nearest parent", sub, "", filepath.Join(repo, FileName)},
		{"repository root", repo, "", filepath.Join(repo, FileName)},
		{"explicit wins", sub, filepath.Join(repo, "other", FileName), filepath.Join(repo, "other", FileName)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := Locate(tc.root, tc.explicit)
			if err != nil || got != tc.want {
				t.Fatalf("Locate(%s, %q) = %q, %v; want %q", tc.root, tc.explicit, got, err, tc.want)
			}
		})
	}

	if _, err := Locate(sub, filepath.Join(repo, "nope.yaml")); err == nil || !strings.Contains(err.Error(), "nope.yaml") {
		t.Errorf("missing explicit file: %v", err)
	}
	if _, err := Locate(sub, filepath.Join(repo, "envs")); err == nil || !strings.Contains(err.Error(), "is a directory") {
		t.Errorf("explicit directory: %v", err)
	}
}

func TestLocateStopsAtTheRepositoryRoot(t *testing.T) {
	outer := t.TempDir()
	writeFiles(t, outer, FileName, "repo/.git", "repo/envs/main.tf")
	got, err := Locate(filepath.Join(outer, "repo", "envs"), "")
	if err != nil || got != "" {
		t.Fatalf("a file above the repository root was used: %q, %v", got, err)
	}
}

func TestLocateRefusesYml(t *testing.T) {
	repo := t.TempDir()
	writeFiles(t, repo, ".git/HEAD", AltFileName, "envs/main.tf")
	_, err := Locate(filepath.Join(repo, "envs"), "")
	if err == nil || !strings.HasSuffix(err.Error(), AltFileName+"; provlift reads "+FileName) {
		t.Fatalf("expected the .yml hint, got %v", err)
	}
	// Both present: .provlift.yaml is read and the other is not an error.
	writeFiles(t, repo, FileName)
	if got, err := Locate(repo, ""); err != nil || got != filepath.Join(repo, FileName) {
		t.Fatalf("with both files: %q, %v", got, err)
	}
}

func TestLoadFileRecordsItsPath(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, FileName)
	if err := os.WriteFile(path, []byte("exclude: [legacy]\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadFile(path)
	if err != nil || cfg.Path() != path || cfg.Dir() != dir {
		t.Fatalf("LoadFile: path %q dir %q err %v", cfg.Path(), cfg.Dir(), err)
	}
	if def, err := LoadFile(""); err != nil || def.Path() != "" || def.Dir() != "" {
		t.Fatalf("defaults: %q %v", def.Path(), err)
	}
}

func TestExcludesForRebasesGlobsOntoTheScanRoot(t *testing.T) {
	dir := t.TempDir()
	cfg := &Config{path: filepath.Join(dir, FileName), Exclude: []string{
		"envs/prod/legacy",   // under the scan root: rebased
		"envs/*/scratch/",    // wildcard segment matching the root's path
		"envs/staging/**",    // only outside the scan root: dropped
		"**/fixtures",        // applies anywhere
		"envs/prod/{a,b}/**", // brace glob kept intact after the prefix
		"modules/**",         // outside the scan root: dropped
	}}
	got := cfg.ExcludesFor(filepath.Join(dir, "envs", "prod"))
	want := []string{"legacy", "scratch", "**/fixtures", "{a,b}/**"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ExcludesFor = %q, want %q", got, want)
	}
	if got := cfg.ExcludesFor(dir); !reflect.DeepEqual(got, cfg.Exclude) {
		t.Errorf("at the config's own directory the globs are unchanged: %q", got)
	}
	if got := (&Config{path: filepath.Join(dir, "policy", FileName), Exclude: []string{"x"}}).ExcludesFor(dir); !reflect.DeepEqual(got, []string{"x"}) {
		t.Errorf("an explicit config outside the scan root keeps root-relative globs: %q", got)
	}
	if got := (&Config{path: filepath.Join(dir, FileName), Exclude: []string{"envs"}}).ExcludesFor(filepath.Join(dir, "envs", "prod")); !reflect.DeepEqual(got, []string{"**"}) {
		t.Errorf("a scan root inside an excluded directory excludes everything below it: %q", got)
	}
}
