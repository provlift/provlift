package cli

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func TestUpgradeAnnouncesEachTerraformStep(t *testing.T) {
	// A provider download can take minutes. Without progress output the tool
	// looks hung.
	h := newHarness(t, nil)
	useFakeTerraform(t, h)

	err := h.run(t, newUpgradeCommand(h.opts),
		"--provider", "azurerm", "--level", "major", "--allow-major")
	if err != nil {
		t.Fatalf("upgrade: %v", err)
	}

	progress := h.stderr.String()
	if !strings.Contains(progress, "Running Terraform") {
		t.Errorf("expected a progress header:\n%s", progress)
	}
	for _, want := range []string{"init -upgrade", "providers lock", "validate"} {
		if !strings.Contains(progress, want) {
			t.Errorf("step %q was not announced:\n%s", want, progress)
		}
	}
	for _, want := range []string{"Initializing provider plugins", "Obtained provider checksums", "init -upgrade: complete (", "providers lock: complete (", "validate: complete ("} {
		if !strings.Contains(progress, want) {
			t.Errorf("missing live output or stage result %q:\n%s", want, progress)
		}
	}
}

func TestUpgradeProgressUsesStderrInJSONMode(t *testing.T) {
	h := newHarness(t, nil)
	useFakeTerraform(t, h)
	h.opts.jsonOut = true

	err := h.run(t, newUpgradeCommand(h.opts),
		"--provider", "azurerm", "--level", "major", "--allow-major")
	if err != nil {
		t.Fatalf("upgrade: %v", err)
	}

	if !strings.Contains(h.stderr.String(), "Running Terraform") || !strings.Contains(h.stderr.String(), "Initializing provider plugins") {
		t.Errorf("JSON mode must retain live progress on stderr:\n%s", h.stderr.String())
	}
	var response map[string]json.RawMessage
	if err := json.Unmarshal(h.stdout.Bytes(), &response); err != nil || len(response) != 5 {
		t.Fatalf("progress corrupted JSON stdout: %v\n%s", err, h.stdout.String())
	}
	if strings.Contains(h.stderr.String(), `"valid":true`) {
		t.Fatal("raw Terraform validation JSON leaked into progress output")
	}
}

func TestUpgradeProgressReportsFailedStep(t *testing.T) {
	h := newHarness(t, nil)
	useFakeTerraform(t, h)
	t.Setenv(fakeTerraformFail, "init")
	err := h.run(t, newUpgradeCommand(h.opts), "--provider", "azurerm", "--level", "minor")
	if err == nil {
		t.Fatal("expected simulated init failure")
	}
	for _, want := range []string{"simulated init failure", "init -upgrade: failed (exit 1)"} {
		if !strings.Contains(h.stderr.String(), want) {
			t.Errorf("missing failure feedback %q:\n%s", want, h.stderr.String())
		}
	}
	if strings.Contains(h.stderr.String(), "terraform validate ...") {
		t.Fatal("validation must not start after init failure")
	}
}

func TestUpgradeProgressReportsInterruptedStep(t *testing.T) {
	h := newHarness(t, nil)
	useFakeTerraform(t, h)
	t.Setenv("PATH", t.TempDir())
	t.Setenv("PROVLIFT_TERRAFORM_BIN", "")
	err := h.runWithGlobalFlags(newUpgradeCommand(h.opts), "--provider", "azurerm", "--level", "minor", "--terraform-binary", h.opts.tfBinary, "--timeout", "1ns")
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected timeout, got %v", err)
	}
	if !strings.Contains(h.stderr.String(), "init -upgrade: failed (") {
		t.Fatalf("interrupted command omitted failure feedback: %s", h.stderr.String())
	}
}

func TestUpgradeFormatsOnlyEditedDirectories(t *testing.T) {
	// github 6.2.1 -> 6.13.0 is permitted by "~> 6.0", so no file is edited.
	// Running terraform fmt anyway could pull unrelated pre-existing drift
	// into the diff.
	h := newHarness(t, nil)
	logPath := useFakeTerraform(t, h)

	err := h.run(t, newUpgradeCommand(h.opts),
		"--provider", "github", "--level", "minor")
	if err != nil {
		t.Fatalf("upgrade: %v", err)
	}

	calls := readInvocations(t, logPath)
	for _, c := range calls {
		if strings.Contains(c, "\tfmt") {
			t.Errorf("fmt ran despite no file being edited: %q\nall calls:\n%s",
				c, strings.Join(calls, "\n"))
		}
	}

	// The lock file still needs regenerating, so init must run.
	if indexOfCall(calls, "init") < 0 {
		t.Errorf("init should still run to move the lock file:\n%s", strings.Join(calls, "\n"))
	}
}

func TestUpgradeFormatsWhenAFileIsEdited(t *testing.T) {
	h := newHarness(t, nil)
	logPath := useFakeTerraform(t, h)

	// "~> 4.65" cannot admit 5.6.0, so this one does edit a file.
	err := h.run(t, newUpgradeCommand(h.opts),
		"--provider", "azurerm", "--level", "major", "--exact-level", "--allow-major")
	if err != nil {
		t.Fatalf("upgrade: %v", err)
	}

	if indexOfCall(readInvocations(t, logPath), "fmt") < 0 {
		t.Error("fmt should run on a directory whose file was edited")
	}
}
