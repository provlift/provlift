package cli

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestCheckModuleFilterWarningsAndValidation(t *testing.T) {
	newRepo := func(t *testing.T) *harness {
		return newHarness(t, map[string]string{
			"envs/prod/main.tf":         moduleFiles("hashicorp/azurerm", "~> 4.65", "")["main.tf"],
			"envs/prod/network/main.tf": moduleFiles("hashicorp/azurerm", "~> 4.65", "")["main.tf"],
		})
	}

	t.Run("bare path selects only itself", func(t *testing.T) {
		h := newRepo(t)
		if err := h.runWithGlobalFlags(newCheckCommand(h.opts), "--json", "--module", "envs/prod"); err != nil {
			t.Fatal(err)
		}
		var response struct {
			Providers []struct {
				Module string `json:"module"`
			} `json:"providers"`
		}
		if err := json.Unmarshal(h.stdout.Bytes(), &response); err != nil {
			t.Fatalf("invalid JSON: %v\n%s", err, h.stdout.String())
		}
		if len(response.Providers) != 1 || response.Providers[0].Module != "envs/prod" {
			t.Fatalf("bare --module selected %s, want only envs/prod", h.stdout.String())
		}
	})

	t.Run("unmatched filter warns without failing", func(t *testing.T) {
		h := newRepo(t)
		if err := h.runWithGlobalFlags(newCheckCommand(h.opts), "--module", "envz/*"); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(h.stderr.String(), `warning: --module "envz/*" matched no modules`) {
			t.Fatalf("missing unmatched-filter warning: %s", h.stderr.String())
		}
	})

	t.Run("invalid filter fails clearly", func(t *testing.T) {
		h := newRepo(t)
		err := h.runWithGlobalFlags(newCheckCommand(h.opts), "--module", "envs/[")
		if exitCode(t, err) != ExitError || !strings.Contains(err.Error(), "invalid --module pattern") {
			t.Fatalf("invalid --module error = %v", err)
		}
	})
}

func TestUpgradeModuleFilterWarningsAndValidation(t *testing.T) {
	t.Run("unmatched filter warns without failing", func(t *testing.T) {
		h := newDriftHarness(t, map[string]map[string]string{
			"envs/prod": moduleFiles("hashicorp/azurerm", "~> 4.65", "4.65.0"),
		})
		err := h.runWithGlobalFlags(newUpgradeCommand(h.opts), "--module", "envz/*", "--dry-run")
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(h.stderr.String(), `warning: --module "envz/*" matched no modules`) {
			t.Fatalf("missing unmatched-filter warning: %s", h.stderr.String())
		}
	})

	t.Run("invalid filter fails clearly", func(t *testing.T) {
		h := newDriftHarness(t, map[string]map[string]string{
			"envs/prod": moduleFiles("hashicorp/azurerm", "~> 4.65", "4.65.0"),
		})
		err := h.runWithGlobalFlags(newUpgradeCommand(h.opts), "--module", "envs/[", "--dry-run")
		if exitCode(t, err) != ExitError || !strings.Contains(err.Error(), "invalid --module pattern") {
			t.Fatalf("invalid --module error = %v", err)
		}
	})
}

func TestValidateModuleFilterWarningsAndValidation(t *testing.T) {
	newRepo := func(t *testing.T) *harness {
		return newHarness(t, map[string]string{
			"envs/prod/main.tf": moduleFiles("hashicorp/azurerm", "~> 4.65", "")["main.tf"],
		})
	}

	t.Run("unmatched filter warns without needing Terraform", func(t *testing.T) {
		h := newRepo(t)
		if err := h.runWithGlobalFlags(newValidateCommand(h.opts), "--module", "envz/*"); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(h.stderr.String(), `warning: --module "envz/*" matched no modules`) {
			t.Fatalf("missing unmatched-filter warning: %s", h.stderr.String())
		}
	})

	t.Run("invalid filter fails clearly", func(t *testing.T) {
		h := newRepo(t)
		err := h.runWithGlobalFlags(newValidateCommand(h.opts), "--module", "envs/[")
		if exitCode(t, err) != ExitError || !strings.Contains(err.Error(), "invalid --module pattern") {
			t.Fatalf("invalid --module error = %v", err)
		}
	})
}
