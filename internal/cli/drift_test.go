package cli

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/provlift/provlift/internal/registry"
)

func moduleFiles(source, constraint, lockedVersion string) map[string]string {
	files := map[string]string{
		"main.tf": `terraform {
  required_providers {
    p = {
      source  = "` + source + `"
      version = "` + constraint + `"
    }
  }
}
`,
	}
	if lockedVersion != "" {
		files[".terraform.lock.hcl"] = `provider "registry.terraform.io/` + source + `" {
  version = "` + lockedVersion + `"
}
`
	}
	return files
}

// newDriftHarness builds a multi-module repository without the single-module
// defaults used by the other command tests.
func newDriftHarness(t *testing.T, modules map[string]map[string]string) *harness {
	t.Helper()

	dir := t.TempDir()
	for moduleDir, files := range modules {
		for name, content := range files {
			path := filepath.Join(dir, filepath.FromSlash(moduleDir), name)
			if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				t.Fatalf("creating %s: %v", path, err)
			}
			if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
				t.Fatalf("writing %s: %v", path, err)
			}
		}
	}

	fake := &fakeRegistry{versions: fixtureVersions}
	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}

	return &harness{
		dir:    dir,
		stdout: stdout,
		stderr: stderr,
		fake:   fake,
		opts: &globalOptions{
			dir: dir,
			out: stdout,
			err: stderr,
			registryClient: registry.New(
				registry.WithHTTPClient(&http.Client{Transport: fake}),
				registry.WithCredentials(registry.NoCredentials{}),
			),
		},
	}
}

// skewedRepo mirrors a common real-world layout: identical configuration and
// identical constraints, but three environments locked at two different major
// versions.
func skewedRepo() map[string]map[string]string {
	return map[string]map[string]string{
		"envs/westeurope-prod":           moduleFiles("hashicorp/azurerm", ">= 3.85.0", "3.117.1"),
		"envs/westeurope-prod-secondary": moduleFiles("hashicorp/azurerm", ">= 3.85.0", "4.81.0"),
		"envs/westus2-prod":              moduleFiles("hashicorp/azurerm", ">= 3.85.0", "4.81.0"),
	}
}

func TestDriftDetectsVersionSkew(t *testing.T) {
	h := newDriftHarness(t, skewedRepo())

	if err := h.run(t, newDriftCommand(h.opts)); err != nil {
		t.Fatalf("drift: %v", err)
	}

	out := h.stdout.String()
	if !strings.Contains(out, "Version skew") {
		t.Fatalf("version skew section missing:\n%s", out)
	}
	for _, want := range []string{"hashicorp/azurerm", "3.117.1", "4.81.0", "MAJOR"} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q:\n%s", want, out)
		}
	}
	// The module names are what make the finding actionable.
	if !strings.Contains(out, "envs/westeurope-prod") {
		t.Errorf("module names missing:\n%s", out)
	}
}

func TestDriftIsOffline(t *testing.T) {
	h := newDriftHarness(t, skewedRepo())

	if err := h.run(t, newDriftCommand(h.opts)); err != nil {
		t.Fatalf("drift: %v", err)
	}
	if h.fake.calls != 0 {
		t.Errorf("drift made %d HTTP calls, want 0", h.fake.calls)
	}
}

func TestDriftAlignsVersionsOfDifferingWidth(t *testing.T) {
	// "2.9.0" and "2.10.0" differ in width; the module column must still line
	// up so the listing is readable.
	h := newDriftHarness(t, map[string]map[string]string{
		"envs/a": moduleFiles("azure/azapi", ">= 2.0", "2.9.0"),
		"envs/b": moduleFiles("azure/azapi", ">= 2.0", "2.10.0"),
	})

	if err := h.run(t, newDriftCommand(h.opts)); err != nil {
		t.Fatalf("drift: %v", err)
	}

	var offsets []int
	for _, line := range strings.Split(h.stdout.String(), "\n") {
		if idx := strings.Index(line, "envs/"); idx > 0 && strings.HasPrefix(line, "    ") {
			offsets = append(offsets, idx)
		}
	}
	if len(offsets) < 2 {
		t.Fatalf("expected at least two module lines:\n%s", h.stdout.String())
	}
	for _, got := range offsets[1:] {
		if got != offsets[0] {
			t.Errorf("module column is misaligned (%v):\n%s", offsets, h.stdout.String())
			break
		}
	}
}

func TestDriftDetectsConstraintDivergence(t *testing.T) {
	h := newDriftHarness(t, map[string]map[string]string{
		"envs/a": moduleFiles("hashicorp/azurerm", ">= 3.85.0", "4.81.0"),
		"envs/b": moduleFiles("hashicorp/azurerm", "~> 4.65", "4.81.0"),
	})

	if err := h.run(t, newDriftCommand(h.opts)); err != nil {
		t.Fatalf("drift: %v", err)
	}

	out := h.stdout.String()
	if !strings.Contains(out, "Constraint divergence") {
		t.Fatalf("divergence section missing:\n%s", out)
	}
	if !strings.Contains(out, ">= 3.85.0") || !strings.Contains(out, "~> 4.65") {
		t.Errorf("both constraints should be listed:\n%s", out)
	}
}

func TestDriftDetectsUnpinnedRootModules(t *testing.T) {
	h := newDriftHarness(t, map[string]map[string]string{
		"envs/pinned":   moduleFiles("hashicorp/azurerm", "~> 4.65", "4.65.0"),
		"envs/unpinned": moduleFiles("hashicorp/azurerm", "~> 4.65", ""),
	})

	if err := h.run(t, newDriftCommand(h.opts)); err != nil {
		t.Fatalf("drift: %v", err)
	}

	out := h.stdout.String()
	if !strings.Contains(out, "Unpinned root modules") {
		t.Fatalf("unpinned section missing:\n%s", out)
	}
	if !strings.Contains(out, "envs/unpinned") {
		t.Errorf("the unpinned module is not named:\n%s", out)
	}
}

func TestDriftReportsNothingWhenConsistent(t *testing.T) {
	h := newDriftHarness(t, map[string]map[string]string{
		"envs/a": moduleFiles("hashicorp/azurerm", "~> 4.65", "4.65.0"),
		"envs/b": moduleFiles("hashicorp/azurerm", "~> 4.65", "4.65.0"),
	})

	if err := h.run(t, newDriftCommand(h.opts)); err != nil {
		t.Fatalf("drift: %v", err)
	}

	if !strings.Contains(h.stdout.String(), "No inconsistencies found") {
		t.Errorf("expected a clean result:\n%s", h.stdout.String())
	}
}

func TestDriftExitCode(t *testing.T) {
	t.Run("signals findings", func(t *testing.T) {
		h := newDriftHarness(t, skewedRepo())

		err := h.run(t, newDriftCommand(h.opts), "--exit-code")
		if err == nil {
			t.Fatal("expected a non-zero exit when drift is found")
		}

		var exit *ExitCodeError
		if !errors.As(err, &exit) {
			t.Fatalf("err = %T(%v), want *ExitCodeError", err, err)
		}
		if exit.Code != ExitUpgradesAvailable {
			t.Errorf("exit code = %d, want %d", exit.Code, ExitUpgradesAvailable)
		}
	})

	t.Run("stays silent when consistent", func(t *testing.T) {
		h := newDriftHarness(t, map[string]map[string]string{
			"envs/a": moduleFiles("hashicorp/azurerm", "~> 4.65", "4.65.0"),
			"envs/b": moduleFiles("hashicorp/azurerm", "~> 4.65", "4.65.0"),
		})

		if err := h.run(t, newDriftCommand(h.opts), "--exit-code"); err != nil {
			t.Errorf("a consistent repository must exit zero, got %v", err)
		}
	})
}

func TestDriftProviderFilter(t *testing.T) {
	modules := map[string]map[string]string{
		"envs/a": {
			"main.tf": `terraform {
  required_providers {
    azurerm = {
      source  = "hashicorp/azurerm"
      version = ">= 3.0"
    }
    github = {
      source  = "integrations/github"
      version = ">= 5.0"
    }
  }
}
`,
			".terraform.lock.hcl": `provider "registry.terraform.io/hashicorp/azurerm" {
  version = "4.65.0"
}

provider "registry.terraform.io/integrations/github" {
  version = "6.2.1"
}
`,
		},
		"envs/b": {
			"main.tf": `terraform {
  required_providers {
    azurerm = {
      source  = "hashicorp/azurerm"
      version = ">= 3.0"
    }
    github = {
      source  = "integrations/github"
      version = ">= 5.0"
    }
  }
}
`,
			".terraform.lock.hcl": `provider "registry.terraform.io/hashicorp/azurerm" {
  version = "5.6.0"
}

provider "registry.terraform.io/integrations/github" {
  version = "6.13.0"
}
`,
		},
	}

	h := newDriftHarness(t, modules)

	if err := h.run(t, newDriftCommand(h.opts), "--provider", "azurerm"); err != nil {
		t.Fatalf("drift: %v", err)
	}

	out := h.stdout.String()
	if !strings.Contains(out, "hashicorp/azurerm") {
		t.Errorf("azurerm finding missing:\n%s", out)
	}
	if strings.Contains(out, "integrations/github") {
		t.Errorf("github should have been filtered out:\n%s", out)
	}
}

func TestDriftJSONIsParsable(t *testing.T) {
	h := newDriftHarness(t, skewedRepo())
	h.opts.jsonOut = true

	if err := h.run(t, newDriftCommand(h.opts)); err != nil {
		t.Fatalf("drift: %v", err)
	}

	var payload struct {
		Count    int `json:"count"`
		Findings struct {
			Skew []struct {
				Source   string `json:"source"`
				Span     string `json:"span"`
				Versions []struct {
					Version string   `json:"version"`
					Modules []string `json:"modules"`
				} `json:"versions"`
			} `json:"version_skew"`
		} `json:"findings"`
	}
	if err := json.Unmarshal(h.stdout.Bytes(), &payload); err != nil {
		t.Fatalf("output is not valid JSON: %v\n%s", err, h.stdout.String())
	}

	if payload.Count == 0 {
		t.Error("count should be non-zero")
	}
	if len(payload.Findings.Skew) != 1 {
		t.Fatalf("got %d skew findings, want 1", len(payload.Findings.Skew))
	}
	if payload.Findings.Skew[0].Span != "major" {
		t.Errorf("span = %q, want major", payload.Findings.Skew[0].Span)
	}
	// JSON keeps the fully qualified address for machine consumers.
	if payload.Findings.Skew[0].Source != "registry.terraform.io/hashicorp/azurerm" {
		t.Errorf("source = %q, want the full address", payload.Findings.Skew[0].Source)
	}
}
