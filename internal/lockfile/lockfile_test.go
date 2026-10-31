package lockfile

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/provlift/provlift/internal/model"
)

func writeLock(t *testing.T, content string) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, Name), []byte(content), 0o644); err != nil {
		t.Fatalf("writing lock file: %v", err)
	}
	return dir
}

const validLock = `# This file is maintained automatically by "terraform init".

provider "registry.terraform.io/hashicorp/azurerm" {
  version     = "3.117.1"
  constraints = ">= 3.47.0, < 4.0.0"
  hashes = [
    "h1:aaaa=",
    "zh:bbbb",
  ]
}

provider "registry.terraform.io/bwoznicki/assert" {
  version = "0.0.1"
}
`

func TestReadParsesProviderBlocks(t *testing.T) {
	dir := writeLock(t, validLock)

	locks, found, err := Read(dir)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if !found {
		t.Fatal("found = false, want true")
	}
	if len(locks) != 2 {
		t.Fatalf("got %d providers, want 2", len(locks))
	}

	addr, err := model.ParseAddress("hashicorp/azurerm")
	if err != nil {
		t.Fatal(err)
	}

	lock, ok := locks[addr.String()]
	if !ok {
		t.Fatal("azurerm lock entry not found")
	}
	if lock.Version != "3.117.1" {
		t.Errorf("Version = %q, want 3.117.1", lock.Version)
	}
	if lock.Constraints != ">= 3.47.0, < 4.0.0" {
		t.Errorf("Constraints = %q", lock.Constraints)
	}
	if len(lock.Hashes) != 2 {
		t.Errorf("Hashes = %v, want 2 entries", lock.Hashes)
	}
}

func TestReadAllowsMissingConstraintsAndHashes(t *testing.T) {
	dir := writeLock(t, validLock)

	locks, _, err := Read(dir)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}

	addr, err := model.ParseAddress("bwoznicki/assert")
	if err != nil {
		t.Fatal(err)
	}
	lock := locks[addr.String()]

	if lock.Version != "0.0.1" {
		t.Errorf("Version = %q, want 0.0.1", lock.Version)
	}
	if lock.Constraints != "" {
		t.Errorf("Constraints = %q, want empty", lock.Constraints)
	}
}

func TestReadMissingFileIsNotAnError(t *testing.T) {
	// A module that has never been initialised is a normal state, not a fault.
	locks, found, err := Read(t.TempDir())
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if found {
		t.Error("found = true, want false")
	}
	if locks != nil {
		t.Errorf("locks = %v, want nil", locks)
	}
}

func TestReadRejectsMalformedFile(t *testing.T) {
	tests := []struct {
		name    string
		content string
	}{
		{
			name:    "invalid HCL",
			content: `provider "x" {`,
		},
		{
			name: "provider block without a version",
			content: `provider "registry.terraform.io/hashicorp/azurerm" {
  constraints = "~> 3.0"
}`,
		},
		{
			name: "unparsable provider address",
			content: `provider "not a valid address!!" {
  version = "1.0.0"
}`,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			dir := writeLock(t, tc.content)

			_, found, err := Read(dir)
			if err == nil {
				t.Fatal("expected an error")
			}
			if !found {
				t.Error("found should be true: the file exists but could not be read")
			}
		})
	}
}

func TestReadHandlesNonDefaultRegistryHost(t *testing.T) {
	dir := writeLock(t, `provider "tf.example.com/platform/internal" {
  version = "2.1.0"
}`)

	locks, _, err := Read(dir)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}

	if _, ok := locks["tf.example.com/platform/internal"]; !ok {
		t.Errorf("private registry provider not found, got keys %v", keys(locks))
	}
}

func keys(m map[string]model.ProviderLock) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
