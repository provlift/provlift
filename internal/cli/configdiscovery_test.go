package cli

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

// subdirHarness lays out a repository with the module under envs/prod and the
// policy file at the repository root, and scans envs/prod as -C would.
func subdirHarness(t *testing.T, files map[string]string) *harness {
	t.Helper()
	all := map[string]string{
		".git/HEAD":                     "ref: refs/heads/main\n",
		"envs/prod/main.tf":             fixtureMainTF,
		"envs/prod/.terraform.lock.hcl": fixtureLock,
	}
	for name, content := range files {
		all[name] = content
	}
	h := newHarness(t, all)
	h.opts.dir = filepath.Join(h.dir, "envs", "prod")
	return h
}

type configJSON struct {
	ConfigFile string `json:"config_file"`
	Plan       struct {
		Actions []struct {
			Source string `json:"source"`
		} `json:"actions"`
		Skipped []struct {
			Source string `json:"source"`
			Reason string `json:"reason"`
		} `json:"skipped"`
	} `json:"plan"`
	Providers []struct {
		Source string `json:"source"`
	} `json:"providers"`
}

func decodeConfigJSON(t *testing.T, h *harness) configJSON {
	t.Helper()
	var out configJSON
	if err := json.Unmarshal(h.stdout.Bytes(), &out); err != nil {
		t.Fatalf("decoding JSON: %v\n%s", err, h.stdout.String())
	}
	return out
}

func TestSubdirectoryScanAppliesRepositoryRootPolicy(t *testing.T) {
	h := subdirHarness(t, map[string]string{
		".provlift.yaml": "providers:\n  max_level:\n    hashicorp/azurerm: none\n",
	})
	h.opts.jsonOut = true
	if err := h.run(t, newUpgradeCommand(h.opts), "--provider", "azurerm", "--level", "minor", "--dry-run"); err != nil {
		t.Fatalf("upgrade: %v", err)
	}
	out := decodeConfigJSON(t, h)
	if len(out.Plan.Actions) != 0 {
		t.Fatalf("max_level: none from the repository root was ignored: %s", h.stdout.String())
	}
	if len(out.Plan.Skipped) != 1 || !strings.Contains(out.Plan.Skipped[0].Reason, "max_level: none") {
		t.Fatalf("expected the policy skip: %s", h.stdout.String())
	}
	if want := filepath.Join(h.dir, ".provlift.yaml"); out.ConfigFile != want {
		t.Errorf("config_file = %q, want %q", out.ConfigFile, want)
	}
	if !strings.Contains(h.stderr.String(), "using config: ../../.provlift.yaml\n") {
		t.Errorf("stderr does not name the config in use: %q", h.stderr.String())
	}
}

func TestSubdirectoryScanAppliesRepositoryRootIgnore(t *testing.T) {
	h := subdirHarness(t, map[string]string{".provlift.yaml": "providers:\n  ignore: [azurerm]\n"})
	h.opts.jsonOut = true
	if err := h.run(t, newListCommand(h.opts)); err != nil {
		t.Fatalf("list: %v", err)
	}
	for _, row := range decodeConfigJSON(t, h).Providers {
		if strings.HasSuffix(row.Source, "azurerm") {
			t.Fatalf("providers.ignore from the repository root was not applied: %s", h.stdout.String())
		}
	}
}

func TestConfigNotSearchedAboveTheRepository(t *testing.T) {
	h := subdirHarness(t, map[string]string{
		"repo/.git/HEAD": "ref: refs/heads/main\n",
		"repo/main.tf":   fixtureMainTF,
		".provlift.yaml": "providers:\n  ignore: [azurerm]\n",
	})
	h.opts.dir = filepath.Join(h.dir, "repo")
	h.opts.jsonOut = true
	if err := h.run(t, newListCommand(h.opts)); err != nil {
		t.Fatalf("list: %v", err)
	}
	out := decodeConfigJSON(t, h)
	if out.ConfigFile != "" {
		t.Errorf("config_file = %q; a file above the repository root must not apply", out.ConfigFile)
	}
	if h.stderr.String() != "" {
		t.Errorf("defaults should be quiet on stderr, got %q", h.stderr.String())
	}
}

func TestYmlConfigIsRefusedWithAHint(t *testing.T) {
	h := subdirHarness(t, map[string]string{".provlift.yml": "providers:\n  ignore: [azurerm]\n"})
	err := h.run(t, newListCommand(h.opts))
	var coded *ExitCodeError
	if !errors.As(err, &coded) || !strings.Contains(err.Error(), ".provlift.yml; provlift reads .provlift.yaml") {
		t.Fatalf("expected the .yml hint, got %v", err)
	}
}

func TestConfigFlagAndEnvironmentPrecedence(t *testing.T) {
	h := subdirHarness(t, map[string]string{
		".provlift.yaml":   "providers:\n  ignore: [azurerm]\n",
		"policy/env.yaml":  "providers:\n  ignore: [github]\n",
		"policy/flag.yaml": "providers:\n  ignore: [random]\n",
	})
	h.opts.dir = filepath.Join(h.dir, "envs", "prod")
	run := func(args ...string) configJSON {
		t.Helper()
		h.stdout.Reset()
		h.stderr.Reset()
		root := []string{"--dir", h.opts.dir, "--json"}
		cmd := newListCommand(h.opts)
		if err := h.runRaw(cmd, append(root, args...)...); err != nil {
			t.Fatalf("list %v: %v", args, err)
		}
		return decodeConfigJSON(t, h)
	}

	if got := run().ConfigFile; got != filepath.Join(h.dir, ".provlift.yaml") {
		t.Errorf("discovery: config_file = %q", got)
	}
	t.Setenv("PROVLIFT_CONFIG", filepath.Join(h.dir, "policy", "env.yaml"))
	if got := run().ConfigFile; got != filepath.Join(h.dir, "policy", "env.yaml") {
		t.Errorf("PROVLIFT_CONFIG: config_file = %q", got)
	}
	if !strings.Contains(h.stderr.String(), "using config: ../../policy/env.yaml") {
		t.Errorf("explicit config not announced: %q", h.stderr.String())
	}
	if got := run("--config", filepath.Join(h.dir, "policy", "flag.yaml")).ConfigFile; got != filepath.Join(h.dir, "policy", "flag.yaml") {
		t.Errorf("--config must win over PROVLIFT_CONFIG: config_file = %q", got)
	}

	h.stdout.Reset()
	err := h.runRaw(newListCommand(h.opts), "--dir", h.opts.dir, "--config", filepath.Join(h.dir, "missing.yaml"))
	if err == nil || !strings.Contains(err.Error(), "missing.yaml") {
		t.Fatalf("a missing --config file must fail, got %v", err)
	}
	if _, statErr := os.Stat(filepath.Join(h.dir, "missing.yaml")); statErr == nil {
		t.Fatal("test setup: missing.yaml exists")
	}
}

// runRaw runs cmd under a root command with the global flags bound, passing
// arguments exactly as given (no implicit --dir).
func (h *harness) runRaw(cmd *cobra.Command, arguments ...string) error {
	root := &cobra.Command{Use: "provlift", SilenceErrors: true, SilenceUsage: true}
	h.opts.bind(root)
	root.AddCommand(cmd)
	root.SetOut(h.stdout)
	root.SetErr(h.stderr)
	return ExecuteContext(context.Background(), root, append([]string{cmd.Name()}, arguments...))
}

func TestRelativeFSMirrorIsReadFromTheConfigDirectory(t *testing.T) {
	h := subdirHarness(t, map[string]string{
		".provlift.yaml": "terraform:\n  fs_mirror: mirror\n",
	})
	ws, err := h.opts.open()
	if err != nil {
		t.Fatal(err)
	}
	mirror, err := resolveLockMirror(ws, &upgradeOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(h.dir, "mirror"); mirror.FS != want {
		t.Fatalf("fs_mirror resolved to %q, want %q (next to the config file)", mirror.FS, want)
	}
}
