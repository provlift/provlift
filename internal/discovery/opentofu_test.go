package discovery

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/provlift/provlift/internal/model"
)

// OpenTofu resolves a source without a hostname against its own registry and
// records that host in the lock file. Scanning with the OpenTofu host must
// therefore pair the requirement with its lock entry, while an explicit host
// stays exactly as written.
func TestScanResolvesImpliedHostsAgainstOpenTofuRegistry(t *testing.T) {
	root := t.TempDir()
	files := map[string]string{
		"main.tf": `terraform {
  required_providers {
    aws    = { source = "hashicorp/aws", version = "~> 5.0" }
    random = { version = ">= 3.0" }
    google = { source = "registry.terraform.io/hashicorp/google", version = ">= 6.0" }
  }
}
`,
		".terraform.lock.hcl": `provider "registry.opentofu.org/hashicorp/aws" {
  version     = "5.80.0"
  constraints = "~> 5.0"
}

provider "registry.opentofu.org/hashicorp/random" {
  version     = "3.6.3"
  constraints = ">= 3.0"
}
`,
	}
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(root, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	inv, err := Scan(Options{Root: root, RegistryHost: model.OpenTofuRegistryHost})
	if err != nil {
		t.Fatal(err)
	}
	if len(inv.Modules) != 1 {
		t.Fatalf("modules = %d, want 1", len(inv.Modules))
	}
	mod := inv.Modules[0]

	want := map[string]string{
		"aws":    "registry.opentofu.org/hashicorp/aws",
		"random": "registry.opentofu.org/hashicorp/random",
		"google": "registry.terraform.io/hashicorp/google",
	}
	for _, req := range mod.Requirements {
		if got := req.Address.String(); got != want[req.LocalName] {
			t.Errorf("%s address = %q, want %q", req.LocalName, got, want[req.LocalName])
		}
	}
	for _, name := range []string{"aws", "random"} {
		addr, err := model.ParseAddressOn(name, model.OpenTofuRegistryHost)
		if err != nil {
			t.Fatal(err)
		}
		if _, ok := mod.Lock(addr); !ok {
			t.Errorf("%s: lock entry not matched to requirement", name)
		}
	}
}

func TestScanDefaultsImpliedHostsToTerraformRegistry(t *testing.T) {
	root := t.TempDir()
	src := "terraform {\n  required_providers {\n    aws = { source = \"hashicorp/aws\" }\n  }\n}\n"
	if err := os.WriteFile(filepath.Join(root, "main.tf"), []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}

	inv, err := Scan(Options{Root: root})
	if err != nil {
		t.Fatal(err)
	}
	if got := inv.Modules[0].Requirements[0].Address.String(); got != "registry.terraform.io/hashicorp/aws" {
		t.Errorf("address = %q, want registry.terraform.io/hashicorp/aws", got)
	}
}
