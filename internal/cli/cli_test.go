package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/spf13/cobra"

	"github.com/provlift/provlift/internal/config"
	"github.com/provlift/provlift/internal/hcledit"
	"github.com/provlift/provlift/internal/registry"
)

// fakeRegistry intercepts every HTTP request, so no test in this package can
// reach the network regardless of which provider address is resolved.
type fakeRegistry struct {
	versions  map[string][]string
	calls     int
	paths     []string
	onRequest func(*http.Request)
	mu        sync.Mutex
}

func (f *fakeRegistry) RoundTrip(req *http.Request) (*http.Response, error) {
	f.mu.Lock()
	f.calls++
	f.paths = append(f.paths, req.URL.Path)
	f.mu.Unlock()
	if f.onRequest != nil {
		f.onRequest(req)
	}

	if strings.HasSuffix(req.URL.Path, "/.well-known/terraform.json") {
		return jsonResponse(http.StatusOK, `{"providers.v1":"/v1/providers/"}`), nil
	}

	parts := strings.Split(strings.Trim(req.URL.Path, "/"), "/")
	if len(parts) >= 3 && parts[len(parts)-1] == "versions" {
		key := parts[len(parts)-3] + "/" + parts[len(parts)-2]
		list, ok := f.versions[key]
		if !ok {
			return jsonResponse(http.StatusNotFound, `{"errors":["not found"]}`), nil
		}

		var sb strings.Builder
		sb.WriteString(`{"versions":[`)
		for i, v := range list {
			if i > 0 {
				sb.WriteString(",")
			}
			fmt.Fprintf(&sb, `{"version":%q,"protocols":["5.0"],"platforms":[`, v)
			for j, platform := range config.DefaultPlatforms {
				if j > 0 {
					sb.WriteString(",")
				}
				parts := strings.SplitN(platform, "_", 2)
				fmt.Fprintf(&sb, `{"os":%q,"arch":%q}`, parts[0], parts[1])
			}
			sb.WriteString("]}")
		}
		sb.WriteString("]}")
		return jsonResponse(http.StatusOK, sb.String()), nil
	}

	return jsonResponse(http.StatusNotFound, `{}`), nil
}

func jsonResponse(status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Body:       io.NopCloser(strings.NewReader(body)),
		Header:     http.Header{"Content-Type": []string{"application/json"}},
	}
}

const fixtureMainTF = `terraform {
  required_providers {
    github = {
      source  = "integrations/github"
      version = "~> 6.0"
    }

    azurerm = {
      source  = "hashicorp/azurerm"
      version = "~> 4.65"
    }
  }
}
`

const fixtureLock = `provider "registry.terraform.io/integrations/github" {
  version = "6.2.1"
}

provider "registry.terraform.io/hashicorp/azurerm" {
  version = "4.65.0"
}
`

var fixtureVersions = map[string][]string{
	"integrations/github": {"6.2.1", "6.2.3", "6.13.0"},
	"hashicorp/azurerm":   {"4.65.0", "4.81.0", "5.6.0"},
}

type harness struct {
	dir    string
	opts   *globalOptions
	stdout *bytes.Buffer
	stderr *bytes.Buffer
	fake   *fakeRegistry
}

func newHarness(t *testing.T, extraFiles map[string]string) *harness {
	t.Helper()

	// Keep the recovery journal out of the real user cache.
	cache := t.TempDir()
	hcledit.CacheDir = func() (string, error) { return cache, nil }
	t.Cleanup(func() { hcledit.CacheDir = os.UserCacheDir })

	dir := t.TempDir()
	files := map[string]string{
		"main.tf":             fixtureMainTF,
		".terraform.lock.hcl": fixtureLock,
	}
	for name, content := range extraFiles {
		files[name] = content
	}
	for name, content := range files {
		path := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatalf("creating %s: %v", path, err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatalf("writing %s: %v", path, err)
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

func (h *harness) run(t *testing.T, cmd *cobra.Command, args ...string) error {
	t.Helper()
	cmd.SetArgs(args)
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	return cmd.ExecuteContext(context.Background())
}

func (h *harness) mainTF(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(h.dir, "main.tf"))
	if err != nil {
		t.Fatalf("reading main.tf: %v", err)
	}
	return string(b)
}

func (h *harness) runWithGlobalFlags(cmd *cobra.Command, arguments ...string) error {
	root := &cobra.Command{Use: "provlift", SilenceErrors: true, SilenceUsage: true}
	h.opts.bind(root)
	root.AddCommand(cmd)
	root.SetOut(h.stdout)
	root.SetErr(h.stderr)
	arguments = append([]string{"--dir", h.dir, cmd.Name()}, arguments...)
	return ExecuteContext(context.Background(), root, arguments)
}

func TestListFlagsHaveObservableEffects(t *testing.T) {
	for _, scenario := range []struct {
		name       string
		arguments  []string
		wantRows   int
		wantChild  bool
		wantHidden bool
	}{
		{"default", nil, 2, true, false},
		{"roots only", []string{"--roots-only"}, 1, false, false},
		{"show ignored", []string{"--show-ignored"}, 3, true, true},
		{"exclude child", []string{"--exclude", "child"}, 1, false, false},
		{"provider", []string{"--provider", "github"}, 1, false, false},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			h := newHarness(t, map[string]string{
				"main.tf":        fixtureMainTF + "\nmodule \"child\" {\n  source = \"./child\"\n}\n",
				"child/main.tf":  moduleFiles("hashicorp/azurerm", ">= 4.0.0", "")["main.tf"],
				".provlift.yaml": "providers:\n  ignore:\n    - integrations/github\n",
			})
			if scenario.name == "provider" {
				if err := os.Remove(filepath.Join(h.dir, ".provlift.yaml")); err != nil {
					t.Fatal(err)
				}
			}
			arguments := append([]string{"--json"}, scenario.arguments...)
			if err := h.runWithGlobalFlags(newListCommand(h.opts), arguments...); err != nil {
				t.Fatal(err)
			}
			var response struct {
				Root      string `json:"root"`
				Providers []struct {
					Module  string `json:"module"`
					Ignored bool   `json:"ignored"`
				} `json:"providers"`
			}
			if err := json.Unmarshal(h.stdout.Bytes(), &response); err != nil {
				t.Fatal(err)
			}
			if response.Root != h.dir || len(response.Providers) != scenario.wantRows {
				t.Fatalf("unexpected --dir/selection result: %s", h.stdout.String())
			}
			var child, hidden bool
			for _, provider := range response.Providers {
				child = child || provider.Module == "child"
				hidden = hidden || provider.Ignored
			}
			if child != scenario.wantChild || hidden != scenario.wantHidden {
				t.Fatalf("flag did not change list membership: %s", h.stdout.String())
			}
		})
	}
}

func TestUpgradeModuleFlagAndRelativeChangedFiles(t *testing.T) {
	h := newDriftHarness(t, map[string]map[string]string{
		"envs/selected":  moduleFiles("hashicorp/azurerm", "~> 4.65", "4.65.0"),
		"envs/untouched": moduleFiles("hashicorp/azurerm", "~> 4.65", "4.65.0"),
	})
	untouched := filepath.Join(h.dir, "envs", "untouched", "main.tf")
	before, err := os.ReadFile(untouched)
	if err != nil {
		t.Fatal(err)
	}
	err = h.runWithGlobalFlags(newUpgradeCommand(h.opts), "--module", "envs/selected", "--provider", "azurerm",
		"--to", "5.6.0", "--level", "major", "--allow-major", "--skip-init", "--json")
	if err != nil {
		t.Fatal(err)
	}
	var response struct {
		ChangedFiles []string `json:"changed_files"`
	}
	if err := json.Unmarshal(h.stdout.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if len(response.ChangedFiles) != 1 || response.ChangedFiles[0] != "envs/selected/main.tf" {
		t.Fatalf("changed_files must be repository-relative and selected: %s", h.stdout.String())
	}
	after, err := os.ReadFile(untouched)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatalf("--module edited another root: %v", err)
	}
}

func TestUpgradePinUnconstrainedFlag(t *testing.T) {
	for _, pin := range []bool{false, true} {
		h := newHarness(t, map[string]string{
			"main.tf": strings.Replace(fixtureMainTF, `version = "~> 4.65"`, "", 1),
		})
		arguments := []string{"--provider", "azurerm", "--to", "5.6.0", "--level", "major", "--allow-major", "--skip-init"}
		if pin {
			arguments = append(arguments, "--pin-unconstrained")
		}
		if err := h.runWithGlobalFlags(newUpgradeCommand(h.opts), arguments...); err != nil {
			t.Fatal(err)
		}
		if got := strings.Contains(h.mainTF(t), `version = "~> 5.6"`); got != pin {
			t.Fatalf("--pin-unconstrained=%t did not control the added constraint", pin)
		}
	}
}

func TestUpgradePullRequestBodyFlag(t *testing.T) {
	h := newHarness(t, nil)
	path := filepath.Join(t.TempDir(), "upgrade.md")
	if err := h.runWithGlobalFlags(newUpgradeCommand(h.opts), "--provider", "azurerm", "--level", "minor",
		"--dry-run", "--json", "--pr-body", path); err != nil {
		t.Fatal(err)
	}
	var response struct {
		PullRequest struct {
			Body string `json:"body"`
		} `json:"pull_request"`
	}
	if err := json.Unmarshal(h.stdout.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(path)
	if err != nil || string(content) != response.PullRequest.Body || len(content) == 0 {
		t.Fatalf("--pr-body did not write the reported description: %v", err)
	}
}

func TestUpgradeTerraformBinaryAndTimeoutFlags(t *testing.T) {
	for _, timeout := range []bool{false, true} {
		h := newHarness(t, nil)
		logPath := useFakeTerraform(t, h)
		t.Setenv("PROVLIFT_TERRAFORM_BIN", filepath.Join(h.dir, "missing-terraform"))
		arguments := []string{"--provider", "azurerm", "--level", "minor", "--terraform-binary", os.Args[0]}
		if timeout {
			arguments = append(arguments, "--timeout", "1ns")
		}
		err := h.runWithGlobalFlags(newUpgradeCommand(h.opts), arguments...)
		if timeout {
			if !errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("--timeout did not bound Terraform execution: %v", err)
			}
		} else if err != nil || len(readInvocations(t, logPath)) == 0 {
			t.Fatalf("--terraform-binary did not override the invalid environment binary: %v", err)
		}
	}
}

func TestOpenWarnsOnCrossNamespaceDeny(t *testing.T) {
	h := newHarness(t, map[string]string{
		".provlift.yaml": "providers:\n  ignore:\n    - hashicorp/azurerm\n",
		"other/main.tf": `terraform {
  required_providers {
    azurerm = {
      source = "contoso/azurerm"
      version = ">= 1.0"
    }
  }
}
`,
	})
	ws, err := h.opts.open()
	if err != nil {
		t.Fatal(err)
	}
	if got := eligibleProviders(ws, nil, nil); len(got) != 1 || got[0].Namespace != "integrations" {
		t.Fatalf("eligibleProviders = %v, want only integrations/github", got)
	}
	if !strings.Contains(h.stderr.String(), `selector "hashicorp/azurerm" matches 2 namespaces`) {
		t.Errorf("missing ambiguity warning: %s", h.stderr.String())
	}
}

func TestCheckWarnsOnCrossNamespaceProviderSelector(t *testing.T) {
	h := newHarness(t, map[string]string{
		"other/main.tf": `terraform {
  required_providers {
    azurerm = {
      source = "contoso/azurerm"
      version = ">= 1.0"
    }
  }
}
`,
	})
	if err := h.run(t, newCheckCommand(h.opts), "--provider", "azurerm"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(h.stderr.String(), `selector "azurerm" matches 2 namespaces`) {
		t.Errorf("missing ambiguity warning: %s", h.stderr.String())
	}
}

func TestOpenUsesEnvironmentBinaryUnlessFlagOverrides(t *testing.T) {
	t.Setenv("PROVLIFT_TERRAFORM_BIN", "custom-terraform")
	h := newHarness(t, nil)
	ws, err := h.opts.open()
	if err != nil || ws.cfg.Terraform.Binary != "custom-terraform" {
		t.Fatalf("environment binary = %q, error = %v", ws.cfg.Terraform.Binary, err)
	}
	h.opts.tfBinary = "explicit-terraform"
	ws, err = h.opts.open()
	if err != nil || ws.cfg.Terraform.Binary != "explicit-terraform" {
		t.Fatalf("flag binary = %q, error = %v", ws.cfg.Terraform.Binary, err)
	}
}

func TestCheckNeverFetchesIgnoredOrUnselectedProviders(t *testing.T) {
	for _, tc := range []struct {
		name   string
		config string
		args   []string
	}{
		{"ignored", "providers:\n  ignore:\n    - hashicorp/azurerm\n", nil},
		{"unselected", "", []string{"--provider", "github"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			files := map[string]string{
				"other/main.tf": `terraform {
  required_providers {
    azurerm = {
      source = "contoso/azurerm"
      version = ">= 1.0"
    }
  }
}
`,
			}
			if tc.config != "" {
				files[".provlift.yaml"] = tc.config
			}
			h := newHarness(t, files)
			if err := h.run(t, newCheckCommand(h.opts), tc.args...); err != nil {
				t.Fatal(err)
			}
			h.fake.mu.Lock()
			paths := append([]string(nil), h.fake.paths...)
			h.fake.mu.Unlock()
			for _, path := range paths {
				if strings.Contains(path, "azurerm") || strings.Contains(path, "contoso") {
					t.Errorf("excluded provider reached network: %s", path)
				}
			}
			if len(paths) == 0 {
				t.Fatal("test must query a selected provider")
			}
		})
	}
}

func TestCheckDoesNotPrintRegistryToken(t *testing.T) {
	const token = "secret-registry-token-check"
	for _, jsonOutput := range []bool{false, true} {
		h := newHarness(t, nil)
		h.opts.jsonOut = jsonOutput
		h.opts.registryClient = registry.New(
			registry.WithHTTPClient(&http.Client{Transport: h.fake}),
			registry.WithCredentials(registry.StaticCredentials{"registry.terraform.io": token}),
		)
		if err := h.run(t, newCheckCommand(h.opts), "--provider", "github"); err != nil {
			t.Fatal(err)
		}
		for _, output := range []string{h.stdout.String(), h.stderr.String()} {
			if strings.Contains(output, token) {
				t.Fatalf("token leaked in output (json=%v)", jsonOutput)
			}
		}
	}
}

func TestListReportsConstraintsAndLocks(t *testing.T) {
	h := newHarness(t, nil)

	if err := h.run(t, newListCommand(h.opts)); err != nil {
		t.Fatalf("list: %v", err)
	}

	out := h.stdout.String()
	for _, want := range []string{
		"integrations/github",
		"hashicorp/azurerm",
		"~> 6.0",
		"~> 4.65",
		"6.2.1",
		"4.65.0",
		"main.tf:",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q:\n%s", want, out)
		}
	}

	// The public registry host is noise in a table; it is kept in JSON instead.
	if strings.Contains(out, "registry.terraform.io") {
		t.Errorf("default registry host should be omitted from the table:\n%s", out)
	}
}

func TestListKeepsPrivateRegistryHostVisible(t *testing.T) {
	// Where a provider comes from matters when it is not the public registry,
	// so that host must survive the shortening.
	h := newHarness(t, map[string]string{
		"private/main.tf": `terraform {
  required_providers {
    internal = {
      source  = "tf.example.com/platform/internal"
      version = "~> 1.0"
    }
  }
}
`,
	})

	if err := h.run(t, newListCommand(h.opts)); err != nil {
		t.Fatalf("list: %v", err)
	}

	out := h.stdout.String()
	if !strings.Contains(out, "tf.example.com/platform/internal") {
		t.Errorf("private registry host was stripped:\n%s", out)
	}
}

func TestListJSONKeepsFullyQualifiedSource(t *testing.T) {
	// Machine consumers need the canonical address even though the table shows
	// a shortened form.
	h := newHarness(t, nil)
	h.opts.jsonOut = true

	if err := h.run(t, newListCommand(h.opts)); err != nil {
		t.Fatalf("list: %v", err)
	}

	if !strings.Contains(h.stdout.String(), "registry.terraform.io/integrations/github") {
		t.Errorf("JSON must carry the full address:\n%s", h.stdout.String())
	}
}

func TestListMakesNoNetworkCalls(t *testing.T) {
	h := newHarness(t, nil)

	if err := h.run(t, newListCommand(h.opts)); err != nil {
		t.Fatalf("list: %v", err)
	}
	if h.fake.calls != 0 {
		t.Errorf("list made %d HTTP calls, want 0", h.fake.calls)
	}
}

func TestListJSONIsParsable(t *testing.T) {
	h := newHarness(t, nil)
	h.opts.jsonOut = true

	if err := h.run(t, newListCommand(h.opts)); err != nil {
		t.Fatalf("list: %v", err)
	}

	var payload struct {
		Providers []struct {
			Source     string `json:"source"`
			Constraint string `json:"constraint"`
			Locked     string `json:"locked"`
		} `json:"providers"`
	}
	if err := json.Unmarshal(h.stdout.Bytes(), &payload); err != nil {
		t.Fatalf("output is not valid JSON: %v\n%s", err, h.stdout.String())
	}
	if len(payload.Providers) != 2 {
		t.Fatalf("got %d providers, want 2", len(payload.Providers))
	}
}

func TestListProviderFilter(t *testing.T) {
	h := newHarness(t, nil)

	if err := h.run(t, newListCommand(h.opts), "--provider", "github"); err != nil {
		t.Fatalf("list: %v", err)
	}

	out := h.stdout.String()
	if !strings.Contains(out, "integrations/github") {
		t.Errorf("github row missing:\n%s", out)
	}
	if strings.Contains(out, "hashicorp/azurerm") {
		t.Errorf("azurerm should have been filtered out:\n%s", out)
	}
}
func TestCheckClassifiesUpgrades(t *testing.T) {
	h := newHarness(t, nil)

	if err := h.run(t, newCheckCommand(h.opts)); err != nil {
		t.Fatalf("check: %v", err)
	}

	out := h.stdout.String()
	if !strings.Contains(out, "minor") {
		t.Errorf("expected a minor upgrade for github:\n%s", out)
	}
	if !strings.Contains(out, "major") {
		t.Errorf("expected a major upgrade for azurerm:\n%s", out)
	}
	if !strings.Contains(out, "2 of 2") {
		t.Errorf("expected both providers to have upgrades:\n%s", out)
	}
}

func TestCheckExitCodeSignalsAvailableUpgrades(t *testing.T) {
	h := newHarness(t, nil)

	err := h.run(t, newCheckCommand(h.opts), "--exit-code")
	if err == nil {
		t.Fatal("expected a non-zero exit when upgrades are available")
	}

	var exit *ExitCodeError
	if !errors.As(err, &exit) {
		t.Fatalf("err = %T(%v), want *ExitCodeError", err, err)
	}
	if exit.Code != ExitUpgradesAvailable {
		t.Errorf("exit code = %d, want %d", exit.Code, ExitUpgradesAvailable)
	}
}

func TestUpgradeRefusesMajorWithoutAcknowledgement(t *testing.T) {
	h := newHarness(t, nil)
	before := h.mainTF(t)

	err := h.run(t, newUpgradeCommand(h.opts), "--provider", "azurerm", "--level", "major", "--exact-level")
	if err == nil {
		t.Fatal("expected a refusal")
	}
	if !strings.Contains(err.Error(), "--allow-major") {
		t.Errorf("error should name the flag that unblocks it: %v", err)
	}
	if h.mainTF(t) != before {
		t.Error("the refusal must happen before any file is written")
	}
}

func TestUpgradeDryRunWritesNothing(t *testing.T) {
	h := newHarness(t, nil)
	before := h.mainTF(t)

	err := h.run(t, newUpgradeCommand(h.opts),
		"--provider", "azurerm", "--level", "major", "--allow-major", "--dry-run")
	if err != nil {
		t.Fatalf("upgrade: %v", err)
	}

	if h.mainTF(t) != before {
		t.Errorf("--dry-run modified the file:\n%s", h.mainTF(t))
	}
	if !strings.Contains(h.stdout.String(), "no files were modified") {
		t.Errorf("dry run should say so:\n%s", h.stdout.String())
	}
}

func TestUpgradeRewritesBlockingConstraint(t *testing.T) {
	h := newHarness(t, nil)

	err := h.run(t, newUpgradeCommand(h.opts),
		"--provider", "azurerm", "--level", "major", "--exact-level", "--allow-major", "--skip-init")
	if err != nil {
		t.Fatalf("upgrade: %v", err)
	}

	got := h.mainTF(t)
	if !strings.Contains(got, `version = "~> 5.0"`) {
		t.Errorf("azurerm constraint was not raised:\n%s", got)
	}
	if !strings.Contains(got, `version = "~> 6.0"`) {
		t.Errorf("the github constraint must be untouched:\n%s", got)
	}
}

func TestUpgradeRefusesUnrecordedBackup(t *testing.T) {
	for _, scenario := range []struct {
		name   string
		target string
	}{
		{"existing target", "main.tf"},
		{"missing target", "modules/extra/versions.tf"},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			backup := scenario.target + ".provlift-backup"
			h := newHarness(t, map[string]string{backup: "terraform {}\n"})
			before := map[string][]byte{}
			for _, name := range []string{"main.tf", backup} {
				content, err := os.ReadFile(filepath.Join(h.dir, name))
				if err != nil {
					t.Fatal(err)
				}
				before[name] = content
			}

			err := h.run(t, newUpgradeCommand(h.opts),
				"--provider", "azurerm", "--level", "major", "--exact-level", "--allow-major", "--skip-init")
			if err == nil || !strings.Contains(err.Error(), filepath.Join(h.dir, backup)) ||
				!strings.Contains(err.Error(), "delete it if it is unexpected") {
				t.Fatalf("expected a refusal naming %s, got %v", backup, err)
			}
			for name, content := range before {
				got, err := os.ReadFile(filepath.Join(h.dir, name))
				if err != nil || !bytes.Equal(got, content) {
					t.Errorf("%s changed: %q, error = %v", name, got, err)
				}
			}
			if scenario.target != "main.tf" {
				if _, err := os.Stat(filepath.Join(h.dir, scenario.target)); !os.IsNotExist(err) {
					t.Errorf("%s should not be created: %v", scenario.target, err)
				}
			}
		})
	}
}

func TestUpgradeDoesNotRecoverExcludedDirectories(t *testing.T) {
	h := newHarness(t, map[string]string{
		".provlift.yaml":                       "exclude:\n  - .backup/**\n",
		".backup/versions.tf.provlift-backup":  "backup fixture",
		"cli-only/versions.tf.provlift-backup": "CLI fixture",
		".idea/versions.tf.provlift-backup":    "built-in fixture",
	})
	h.opts.excludes = []string{"cli-only"}
	if err := h.run(t, newUpgradeCommand(h.opts),
		"--provider", "azurerm", "--level", "major", "--exact-level", "--allow-major", "--skip-init"); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{".backup/versions.tf", "cli-only/versions.tf", ".idea/versions.tf"} {
		if _, err := os.Stat(filepath.Join(h.dir, path+".provlift-backup")); err != nil {
			t.Errorf("excluded backup %s was touched: %v", path, err)
		}
		if _, err := os.Stat(filepath.Join(h.dir, path)); !os.IsNotExist(err) {
			t.Errorf("excluded file %s was restored: %v", path, err)
		}
	}
	if !strings.Contains(h.mainTF(t), `version = "~> 5.0"`) {
		t.Fatal("upgrade did not edit the selected repository root")
	}
}

func TestUpgradeLeavesSatisfiedConstraintAlone(t *testing.T) {
	// github 6.2.1 -> 6.13.0 is permitted by "~> 6.0", so only the lock file
	// would move and versions.tf must not be touched.
	h := newHarness(t, nil)
	before := h.mainTF(t)

	err := h.run(t, newUpgradeCommand(h.opts),
		"--provider", "github", "--level", "minor", "--skip-init")
	if err != nil {
		t.Fatalf("upgrade: %v", err)
	}

	if h.mainTF(t) != before {
		t.Errorf("constraint was rewritten unnecessarily:\n%s", h.mainTF(t))
	}
	if !strings.Contains(h.stdout.String(), "No constraint changes were needed") {
		t.Errorf("expected the no-change message:\n%s", h.stdout.String())
	}
}

func TestUpgradeSkipInitWarnsAboutStaleLockFile(t *testing.T) {
	h := newHarness(t, nil)

	err := h.run(t, newUpgradeCommand(h.opts),
		"--provider", "azurerm", "--level", "major", "--allow-major", "--skip-init")
	if err != nil {
		t.Fatalf("upgrade: %v", err)
	}

	if !strings.Contains(h.stderr.String(), "out of step") {
		t.Errorf("skipping init must warn that the lock file is now stale:\n%s", h.stderr.String())
	}
}

func TestUpgradeJSONParsingErrors(t *testing.T) {
	for _, arguments := range [][]string{
		{"upgrade", "--json", "--unknown-flag"},
		{"upgrade", "--unknown-flag", "--json"},
		{"upgrade", "--json", "--fmt=invalid"},
		{"upgrade", "--fmt=invalid", "--json"},
		{"upgrade", "--json", "--timeout=invalid"},
		{"upgrade", "--json", "unexpected-argument"},
		{"upgrade", "--json", "--level"},
		{"upgrade", "--json", "--platforms"},
		{"--json", "upgrade", "--unknown-flag"},
		{"upgrade", "--unknown-flag", "--json", "--dry-run"},
	} {
		t.Run(strings.Join(arguments, " "), func(t *testing.T) {
			root := NewRootCommand(BuildInfo{})
			var output bytes.Buffer
			root.SetOut(&output)
			root.SetErr(io.Discard)
			err := ExecuteContext(context.Background(), root, arguments)
			var coded *ExitCodeError
			if !errors.As(err, &coded) || coded.Code != ExitError {
				t.Fatalf("expected argument parsing failure with exit 1, got %v", err)
			}
			var response map[string]json.RawMessage
			if err := json.Unmarshal(output.Bytes(), &response); err != nil {
				t.Fatalf("argument error omitted JSON response: %v\n%s", err, output.String())
			}
			for _, key := range []string{"dry_run", "plan", "changed_files", "terraform_runs", "pull_request"} {
				if _, exists := response[key]; !exists {
					t.Errorf("argument error omitted %s", key)
				}
			}
			if len(response) != 5 {
				t.Fatalf("unexpected response keys: %s", output.String())
			}
		})
	}
}

func TestUpgradeParsingErrorsDoNotInferJSONFromValues(t *testing.T) {
	for _, arguments := range [][]string{
		{"upgrade", "--provider=--json", "--unknown-flag"},
		{"upgrade", "--provider", "--json", "--unknown-flag"},
		{"upgrade", "--unknown-flag", "--json=false"},
		{"upgrade", "--", "--json"},
	} {
		root := NewRootCommand(BuildInfo{})
		var output bytes.Buffer
		root.SetOut(&output)
		root.SetErr(io.Discard)
		if err := ExecuteContext(context.Background(), root, arguments); err == nil {
			t.Fatalf("expected parsing failure for %v", arguments)
		}
		if output.Len() != 0 {
			t.Fatalf("flag-like argument value enabled JSON for %v: %s", arguments, output.String())
		}
	}
}

func TestUpgradeJSONOutcomeContract(t *testing.T) {
	for _, scenario := range []struct {
		name     string
		args     []string
		failure  string
		wantCode int
	}{
		{"no upgrade", []string{"--provider", "azurerm", "--level", "patch"}, "", ExitOK},
		{"dry run", []string{"--provider", "azurerm", "--level", "minor", "--dry-run"}, "", ExitOK},
		{"success", []string{"--provider", "azurerm", "--level", "major", "--exact-level", "--allow-major"}, "", ExitOK},
		{"skip init", []string{"--provider", "azurerm", "--level", "major", "--exact-level", "--allow-major", "--skip-init"}, "", ExitOK},
		{"major refused", []string{"--provider", "azurerm", "--level", "major", "--exact-level"}, "", ExitError},
		{"invalid level", []string{"--level", "invalid"}, "", ExitError},
		{"validation failed", []string{"--provider", "azurerm", "--level", "minor"}, "validate", ExitValidationFailed},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			h := newHarness(t, nil)
			h.opts.jsonOut = true
			useFakeTerraform(t, h)
			if scenario.failure != "" {
				t.Setenv(fakeTerraformFail, scenario.failure)
			}
			err := h.run(t, newUpgradeCommand(h.opts), scenario.args...)
			code := ExitOK
			if err != nil {
				var coded *ExitCodeError
				if !errors.As(err, &coded) {
					t.Fatalf("uncoded error: %v", err)
				}
				code = coded.Code
			}
			if code != scenario.wantCode {
				t.Fatalf("exit code = %d, want %d: %v", code, scenario.wantCode, err)
			}
			var result map[string]json.RawMessage
			if err := json.Unmarshal(h.stdout.Bytes(), &result); err != nil {
				t.Fatalf("invalid JSON response: %v\n%s", err, h.stdout.String())
			}
			keys := []string{"dry_run", "plan", "changed_files", "terraform_runs", "pull_request"}
			if len(result) != len(keys) {
				t.Errorf("response has %d keys, want %d", len(result), len(keys))
			}
			for _, key := range keys {
				if _, found := result[key]; !found {
					t.Errorf("missing response key %q", key)
				}
			}
			var files []string
			if err := json.Unmarshal(result["changed_files"], &files); err != nil {
				t.Fatal(err)
			}
			for _, file := range files {
				if filepath.IsAbs(file) || file == ".." || strings.HasPrefix(file, "../") {
					t.Errorf("changed file is not repository-relative: %q", file)
				}
			}
		})
	}
}

func TestUpgradeJSONReportsTerraformChangedLockFile(t *testing.T) {
	main := strings.Replace(fixtureMainTF, `version = "~> 4.65"`, `version = ">= 3.85.0"`, 1)
	h := newHarness(t, map[string]string{"main.tf": main})
	h.opts.jsonOut = true
	useFakeTerraform(t, h)
	lockPath := filepath.Join(h.dir, ".terraform.lock.hcl")
	before, err := os.ReadFile(lockPath)
	if err != nil {
		t.Fatal(err)
	}

	if err := h.run(t, newUpgradeCommand(h.opts), "--provider", "azurerm", "--level", "major", "--exact-level", "--allow-major"); err != nil {
		t.Fatalf("upgrade: %v", err)
	}
	var response struct {
		ChangedFiles []string `json:"changed_files"`
	}
	if err := json.Unmarshal(h.stdout.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if len(response.ChangedFiles) != 1 || response.ChangedFiles[0] != ".terraform.lock.hcl" {
		t.Fatalf("changed_files = %v, want only .terraform.lock.hcl: %s", response.ChangedFiles, h.stdout.String())
	}
	after, err := os.ReadFile(lockPath)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(before, after) {
		t.Fatal("fake Terraform did not change the lock file")
	}
}

func TestUpgradeJSONDoesNotReportRestoredLockFile(t *testing.T) {
	main := strings.Replace(fixtureMainTF, `version = "~> 4.65"`, `version = ">= 3.85.0"`, 1)
	h := newHarness(t, map[string]string{"main.tf": main})
	h.opts.jsonOut = true
	useFakeTerraform(t, h)
	t.Setenv(fakeTerraformFail, "providers lock")
	lockPath := filepath.Join(h.dir, ".terraform.lock.hcl")
	before, err := os.ReadFile(lockPath)
	if err != nil {
		t.Fatal(err)
	}

	err = h.run(t, newUpgradeCommand(h.opts), "--provider", "azurerm", "--level", "major", "--exact-level", "--allow-major")
	var coded *ExitCodeError
	if !errors.As(err, &coded) || coded.Code != ExitValidationFailed {
		t.Fatalf("expected providers lock failure, got %v", err)
	}
	var response struct {
		ChangedFiles []string `json:"changed_files"`
	}
	if err := json.Unmarshal(h.stdout.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if len(response.ChangedFiles) != 0 {
		t.Fatalf("changed_files = %v, want no persistent lock changes: %s", response.ChangedFiles, h.stdout.String())
	}
	after, err := os.ReadFile(lockPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Fatal("original lock was not restored after the simulated failure")
	}
}

func TestUpgradeJSONReportsConstraintEditsWhenLockSnapshotFails(t *testing.T) {
	h := newHarness(t, nil)
	h.opts.jsonOut = true
	// The binary must resolve so the constraint edit lands before the snapshot fails.
	useFakeTerraform(t, h)
	lockPath := filepath.Join(h.dir, ".terraform.lock.hcl")
	var once sync.Once
	var replaceErr error
	h.fake.onRequest = func(*http.Request) {
		once.Do(func() {
			if err := os.Remove(lockPath); err != nil {
				replaceErr = err
				return
			}
			replaceErr = os.Mkdir(lockPath, 0o700)
		})
	}
	before := h.mainTF(t)

	err := h.run(t, newUpgradeCommand(h.opts), "--provider", "azurerm", "--level", "major", "--exact-level", "--allow-major")
	if replaceErr != nil {
		t.Fatalf("making lock unreadable after discovery: %v", replaceErr)
	}
	var coded *ExitCodeError
	if !errors.As(err, &coded) || coded.Code != ExitError {
		t.Fatalf("expected lock snapshot error, got %v", err)
	}
	var response struct {
		ChangedFiles []string `json:"changed_files"`
		PullRequest  struct {
			Body string `json:"body"`
		} `json:"pull_request"`
	}
	if err := json.Unmarshal(h.stdout.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if len(response.ChangedFiles) != 1 || response.ChangedFiles[0] != "main.tf" {
		t.Fatalf("changed_files = %v, want main.tf: %s", response.ChangedFiles, h.stdout.String())
	}
	if !strings.Contains(response.PullRequest.Body, "4.65.0") || !strings.Contains(response.PullRequest.Body, "5.6.0") {
		t.Fatalf("pull request metadata omitted the persisted constraint change: %s", response.PullRequest.Body)
	}
	if h.mainTF(t) == before {
		t.Fatal("the constraint edit was not persisted before lock snapshot failed")
	}
}

func TestUpgradeSkipInitJSONWarnsAboutStaleLockFile(t *testing.T) {
	h := newHarness(t, nil)
	h.opts.jsonOut = true
	if err := h.run(t, newUpgradeCommand(h.opts),
		"--provider", "azurerm", "--level", "major", "--exact-level", "--allow-major", "--skip-init"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(h.stderr.String(), "out of step") {
		t.Errorf("JSON mode omitted stale-lock warning: %s", h.stderr.String())
	}
}

func TestUpgradePinnedProviderLookupFailureDoesNotBlockOthers(t *testing.T) {
	h := newHarness(t, map[string]string{
		".provlift.yaml": "providers:\n  max_level:\n    hashicorp/azurerm: none\n",
	})
	h.opts.jsonOut = true
	h.fake.versions = map[string][]string{"integrations/github": {"6.2.1", "6.2.3"}}
	before := h.mainTF(t)
	if err := h.run(t, newUpgradeCommand(h.opts), "--level", "patch", "--skip-init"); err != nil {
		t.Fatalf("lookup failure for a pinned provider blocked an eligible provider: %v", err)
	}
	var response struct {
		Plan struct {
			Actions []struct {
				Source string `json:"source"`
				To     string `json:"to"`
			} `json:"actions"`
			Skipped []struct {
				Reason string `json:"reason"`
			} `json:"skipped"`
		} `json:"plan"`
	}
	if err := json.Unmarshal(h.stdout.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if len(response.Plan.Actions) != 1 || !strings.HasSuffix(response.Plan.Actions[0].Source, "integrations/github") || response.Plan.Actions[0].To != "6.2.3" {
		t.Fatalf("expected the eligible GitHub upgrade: %s", h.stdout.String())
	}
	if len(response.Plan.Skipped) != 1 || !strings.Contains(response.Plan.Skipped[0].Reason, "max_level: none") {
		t.Fatalf("expected policy skip for AzureRM: %s", h.stdout.String())
	}
	if h.mainTF(t) != before {
		t.Fatal("pinned or already-satisfied constraints changed")
	}
}

func TestUpgradeRegistryFailureReturnsError(t *testing.T) {
	h := newHarness(t, nil)
	h.opts.jsonOut = true
	h.fake.versions = map[string][]string{}
	before := h.mainTF(t)
	err := h.run(t, newUpgradeCommand(h.opts), "--provider", "azurerm", "--level", "minor", "--skip-init")
	var coded *ExitCodeError
	if !errors.As(err, &coded) || coded.Code != ExitError {
		t.Fatalf("registry lookup failure must return exit 1, got %v", err)
	}
	if h.mainTF(t) != before {
		t.Fatal("failed registry lookup modified the configuration")
	}
	var result map[string]json.RawMessage
	if err := json.Unmarshal(h.stdout.Bytes(), &result); err != nil || len(result) != 5 {
		t.Fatalf("registry error did not emit the JSON envelope: %s (%v)", h.stdout.String(), err)
	}
}

func TestUpgradeUnsupportedConstraintIsRefused(t *testing.T) {
	h := newHarness(t, nil)
	path := filepath.Join(h.dir, "main.tf")
	before := strings.Replace(fixtureMainTF, `~> 4.65`, `!= 5.6.0`, 1)
	if err := os.WriteFile(path, []byte(before), 0o644); err != nil {
		t.Fatal(err)
	}
	err := h.run(t, newUpgradeCommand(h.opts), "--provider", "azurerm", "--to", "5.6.0",
		"--level", "major", "--allow-major", "--skip-init")
	var coded *ExitCodeError
	if !errors.As(err, &coded) || coded.Code != ExitError {
		t.Fatalf("unsupported rewrite must return exit 1, got %v", err)
	}
	if !strings.Contains(err.Error(), "by hand") {
		t.Errorf("refusal must provide an actionable message: %v", err)
	}
	if h.mainTF(t) != before {
		t.Fatal("refused rewrite modified the configuration")
	}
}

func TestUpgradePrereleaseRejectsStaleLockDowngrade(t *testing.T) {
	for _, constraint := range []string{">= 5.0.0", ">= 5.0.0, < 6.0.0"} {
		t.Run(constraint, func(t *testing.T) {
			h := newDriftHarness(t, map[string]map[string]string{
				".": moduleFiles("hashicorp/azurerm", constraint, "4.65.0"),
			})
			h.fake.versions = map[string][]string{"hashicorp/azurerm": {"4.65.0", "4.66.0-beta.1", "5.0.0"}}
			logPath := useFakeTerraform(t, h)
			before := h.mainTF(t)
			err := h.runWithGlobalFlags(newUpgradeCommand(h.opts), "--provider", "azurerm", "--to", "4.66.0-beta.1",
				"--level", "minor", "--include-prerelease", "--terraform-binary", os.Args[0])
			var coded *ExitCodeError
			if !errors.As(err, &coded) || coded.Code != ExitError || !strings.Contains(err.Error(), "below the existing lower bound") {
				t.Fatalf("stale-lock prerelease downgrade must return exit 1: %v", err)
			}
			if h.mainTF(t) != before || len(readInvocations(t, logPath)) != 0 {
				t.Fatal("refused downgrade changed configuration or invoked Terraform")
			}
		})
	}
}

func TestUpgradePrereleaseSelectionPaths(t *testing.T) {
	for _, explicit := range []bool{false, true} {
		for _, includePrerelease := range []bool{false, true} {
			h := newHarness(t, nil)
			h.fake.versions = map[string][]string{"hashicorp/azurerm": {"4.65.0", "5.0.0-beta.1"}}
			arguments := []string{"--provider", "azurerm", "--level", "major", "--allow-major", "--dry-run", "--json"}
			if includePrerelease {
				arguments = append(arguments, "--include-prerelease")
			}
			if explicit {
				arguments = append(arguments, "--to", "5.0.0-beta.1")
			}
			if err := h.runWithGlobalFlags(newUpgradeCommand(h.opts), arguments...); err != nil {
				t.Fatal(err)
			}
			var result struct {
				Plan struct {
					Actions []struct {
						To string `json:"to"`
					} `json:"actions"`
				} `json:"plan"`
			}
			if err := json.Unmarshal(h.stdout.Bytes(), &result); err != nil {
				t.Fatal(err)
			}
			if includePrerelease {
				if len(result.Plan.Actions) != 1 || result.Plan.Actions[0].To != "5.0.0-beta.1" {
					t.Fatalf("prerelease opt-in ignored (explicit=%t): %s", explicit, h.stdout.String())
				}
			} else if len(result.Plan.Actions) != 0 {
				t.Fatalf("prerelease selected without opt-in (explicit=%t)", explicit)
			}
		}
	}
}

func TestUpgradeSameTargetIsIdempotent(t *testing.T) {
	h := newHarness(t, nil)
	logPath := useFakeTerraform(t, h)
	h.opts.jsonOut = true
	arguments := []string{"--provider", "azurerm", "--to", "5.6.0", "--level", "major", "--allow-major"}
	if err := h.run(t, newUpgradeCommand(h.opts), arguments...); err != nil {
		t.Fatal(err)
	}
	before := h.mainTF(t)
	beforeCalls := readInvocations(t, logPath)
	h.stdout.Reset()
	if err := h.run(t, newUpgradeCommand(h.opts), arguments...); err != nil {
		t.Fatal(err)
	}
	var response struct {
		Plan struct {
			Actions []json.RawMessage `json:"actions"`
		} `json:"plan"`
		ChangedFiles []string `json:"changed_files"`
	}
	if err := json.Unmarshal(h.stdout.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if len(response.Plan.Actions) != 0 || len(response.ChangedFiles) != 0 {
		t.Fatalf("second run planned work: %s", h.stdout.String())
	}
	if before != h.mainTF(t) || len(readInvocations(t, logPath)) != len(beforeCalls) {
		t.Fatal("second run modified files or invoked Terraform")
	}
}

func TestUpgradeHonoursConfiguredMaxLevel(t *testing.T) {
	h := newHarness(t, map[string]string{
		".provlift.yaml": "providers:\n  max_level:\n    hashicorp/azurerm: none\n",
	})
	before := h.mainTF(t)

	err := h.run(t, newUpgradeCommand(h.opts),
		"--provider", "azurerm", "--level", "major", "--allow-major", "--skip-init")
	if err != nil {
		t.Fatalf("upgrade: %v", err)
	}

	if h.mainTF(t) != before {
		t.Errorf("a provider pinned by configuration was upgraded:\n%s", h.mainTF(t))
	}
	if !strings.Contains(h.stdout.String(), "max_level") {
		t.Errorf("the skip reason should cite the configuration:\n%s", h.stdout.String())
	}
}

func TestUpgradeIgnoredProviderIsNeverTouched(t *testing.T) {
	h := newHarness(t, map[string]string{
		".provlift.yaml": "providers:\n  ignore:\n    - hashicorp/azurerm\n",
	})
	before := h.mainTF(t)

	err := h.run(t, newUpgradeCommand(h.opts),
		"--level", "major", "--allow-major", "--skip-init")
	if err != nil {
		t.Fatalf("upgrade: %v", err)
	}

	if h.mainTF(t) != before {
		t.Errorf("an ignored provider was modified:\n%s", h.mainTF(t))
	}
}

func TestUpgradeToRequiresExactlyOneProvider(t *testing.T) {
	h := newHarness(t, nil)

	err := h.run(t, newUpgradeCommand(h.opts), "--to", "5.6.0", "--level", "major")
	if err == nil {
		t.Fatal("expected an error when --to is used without a single --provider")
	}
	if !strings.Contains(err.Error(), "--to requires exactly one --provider") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestUpgradeRejectsInvalidLevel(t *testing.T) {
	h := newHarness(t, nil)

	err := h.run(t, newUpgradeCommand(h.opts), "--level", "enormous")
	if err == nil {
		t.Fatal("expected an error for an unknown level")
	}
	if !strings.Contains(err.Error(), "unknown upgrade level") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestVersionCommand(t *testing.T) {
	h := newHarness(t, nil)

	build := BuildInfo{Version: "1.2.3", Commit: "abc123", Date: "2026-01-01"}
	if err := h.run(t, newVersionCommand(h.opts, build)); err != nil {
		t.Fatalf("version: %v", err)
	}

	if !strings.Contains(h.stdout.String(), "1.2.3") {
		t.Errorf("version not reported:\n%s", h.stdout.String())
	}
}

func TestRegistryFailureIsReportedPerProvider(t *testing.T) {
	// An unknown provider must not abort the whole run.
	h := newHarness(t, nil)
	h.fake.versions = map[string][]string{
		"integrations/github": {"6.2.1", "6.13.0"},
	}

	if err := h.run(t, newCheckCommand(h.opts)); err != nil {
		t.Fatalf("check: %v", err)
	}

	if !strings.Contains(h.stdout.String(), "lookup failed") {
		t.Errorf("expected a per-row failure marker:\n%s", h.stdout.String())
	}
	if !strings.Contains(h.stdout.String(), "6.13.0") {
		t.Errorf("the working provider should still be reported:\n%s", h.stdout.String())
	}
}
