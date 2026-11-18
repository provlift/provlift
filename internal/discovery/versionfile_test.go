package discovery

import (
	"os"
	"path/filepath"
	"testing"
)

const coreModule = `terraform {
  required_version = ">= 1.5.0"
}
`

func writeTree(t *testing.T, root string, files map[string]string) {
	t.Helper()
	for name, content := range files {
		path := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func TestScanReadsNearestVersionFile(t *testing.T) {
	inv := scanFiles(t, map[string]string{
		".terraform-version":           "1.5.7\n",
		"envs/dev/main.tf":             coreModule,
		"envs/prod/main.tf":            coreModule,
		"envs/prod/.terraform-version": "# pinned for prod\n\n  1.9.0  \n",
	})

	for _, tc := range []struct{ dir, version, file string }{
		{"envs/dev", "1.5.7", ".terraform-version"},
		{"envs/prod", "1.9.0", "envs/prod/.terraform-version"},
	} {
		mod := moduleByRelDir(inv, tc.dir)
		if mod == nil {
			t.Fatalf("module %s not found", tc.dir)
		}
		if mod.VersionFile != tc.version || mod.VersionFilePath != tc.file {
			t.Errorf("%s: version file = %q (%q), want %q (%q)",
				tc.dir, mod.VersionFile, mod.VersionFilePath, tc.version, tc.file)
		}
		if mod.RequiredVersion != ">= 1.5.0" {
			t.Errorf("%s: required_version = %q", tc.dir, mod.RequiredVersion)
		}
	}
}

func TestScanVersionFileBinaryDecidesWhenBothExist(t *testing.T) {
	root := t.TempDir()
	writeTree(t, root, map[string]string{
		"main.tf":            coreModule,
		".terraform-version": "1.5.7\n",
		".opentofu-version":  "1.8.2\n",
		// A nearer file of the other kind still wins over a farther preferred one.
		"child/main.tf":            coreModule,
		"child/.terraform-version": "1.6.0\n",
	})

	for _, tc := range []struct {
		openTofu          bool
		rootVer, childVer string
	}{
		{false, "1.5.7", "1.6.0"},
		{true, "1.8.2", "1.6.0"},
	} {
		inv, err := Scan(Options{Root: root, OpenTofu: tc.openTofu})
		if err != nil {
			t.Fatal(err)
		}
		if got := moduleByRelDir(inv, ".").VersionFile; got != tc.rootVer {
			t.Errorf("OpenTofu=%t: root version = %q, want %q", tc.openTofu, got, tc.rootVer)
		}
		if got := moduleByRelDir(inv, "child").VersionFile; got != tc.childVer {
			t.Errorf("OpenTofu=%t: child version = %q, want %q", tc.openTofu, got, tc.childVer)
		}
	}
}

func TestScanIgnoresVersionFilesAboveRoot(t *testing.T) {
	outer := t.TempDir()
	writeTree(t, outer, map[string]string{
		".terraform-version":     "1.5.7\n",
		"repo/main.tf":           coreModule,
		"repo/.opentofu-version": "\n",
	})

	inv, err := Scan(Options{Root: filepath.Join(outer, "repo")})
	if err != nil {
		t.Fatal(err)
	}
	mod := moduleByRelDir(inv, ".")
	if mod.VersionFile != "" || mod.VersionFilePath != "" {
		t.Errorf("version file = %q (%q), want none: an empty file is skipped and the scan must not read above its root",
			mod.VersionFile, mod.VersionFilePath)
	}
}
