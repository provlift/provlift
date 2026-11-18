package cli

import (
	"strings"
	"testing"
)

func TestListDiscoversTofuFilesWithOpenTofuBinary(t *testing.T) {
	repo := map[string]map[string]string{
		"tofu": {"main.tofu": "terraform {\n  required_providers {\n    aws = { source = \"hashicorp/aws\", version = \"~> 5.0\" }\n  }\n}\n"},
	}

	h := newDriftHarness(t, repo)
	h.opts.tfBinary = "tofu"
	if err := h.run(t, newListCommand(h.opts)); err != nil {
		t.Fatalf("list: %v", err)
	}
	if out := h.stdout.String(); !strings.Contains(out, "main.tofu:3") || !strings.Contains(out, "~> 5.0") {
		t.Errorf("with tofu, the .tofu-only module should be listed:\n%s", out)
	}

	h = newDriftHarness(t, repo)
	if err := h.run(t, newListCommand(h.opts)); err != nil {
		t.Fatalf("list: %v", err)
	}
	if out := h.stdout.String() + h.stderr.String(); !strings.Contains(out, "No Terraform provider requirements found.") ||
		!strings.Contains(out, "OpenTofu files are ignored") {
		t.Errorf("with terraform, .tofu files are skipped with a note:\n%s", out)
	}
}
