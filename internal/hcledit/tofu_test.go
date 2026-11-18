package hcledit

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/provlift/provlift/internal/discovery"
	"github.com/provlift/provlift/internal/model"
)

const tofuConfig = "terraform {\n  required_providers {\n    aws = { source = \"hashicorp/aws\", version = \"~> 5.0\" }\n  }\n}\n"

// tofuFixture writes main.tofu next to the main.tf it shadows.
func tofuFixture(t *testing.T) (dir, shadowed string) {
	t.Helper()
	isolateJournal(t)
	dir = t.TempDir()
	shadowed = "terraform {\n  required_providers {\n    aws = { source = \"hashicorp/aws\", version = \"~> 4.0\" }\n  }\n}\n"
	for name, content := range map[string]string{"main.tofu": tofuConfig, "main.tf": shadowed} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return dir, shadowed
}

func TestApplyEditsTheTofuFileThatShadowsTF(t *testing.T) {
	dir, shadowed := tofuFixture(t)

	inv, err := discovery.Scan(discovery.Options{Root: dir, RegistryHost: model.OpenTofuRegistryHost})
	if err != nil {
		t.Fatal(err)
	}
	if len(inv.Modules) != 1 || len(inv.Modules[0].Requirements) != 1 {
		t.Fatalf("modules = %+v, want one module with one requirement", inv.Modules)
	}
	req := inv.Modules[0].Requirements[0]
	if req.ConstraintPos.File != "main.tofu" || req.Constraint != "~> 5.0" {
		t.Fatalf("requirement = %s at %s, want ~> 5.0 at main.tofu", req.Constraint, req.ConstraintPos.File)
	}

	if _, err := Apply([]Edit{{Requirement: req, New: "~> 6.0"}}); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	got, err := os.ReadFile(filepath.Join(dir, "main.tofu"))
	if err != nil || !bytes.Contains(got, []byte(`"~> 6.0"`)) {
		t.Fatalf("main.tofu = %q, error = %v", got, err)
	}
	assertContent(t, filepath.Join(dir, "main.tf"), shadowed)
	assertNoArtifacts(t, dir)
	assertNoJournal(t)
}

func TestRecoverRestoresInterruptedTofuWrite(t *testing.T) {
	dir, _ := tofuFixture(t)
	path := filepath.Join(dir, "main.tofu")
	interruptedWrite(t, path)

	if err := Recover(dir, nil); err != nil {
		t.Fatalf("Recover: %v", err)
	}
	assertContent(t, path, tofuConfig)
	assertNoArtifacts(t, dir)
	assertNoJournal(t)
}
