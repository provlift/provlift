package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/provlift/provlift/internal/discovery"
	"github.com/provlift/provlift/internal/lockfile"
	"github.com/provlift/provlift/internal/model"
	"github.com/provlift/provlift/internal/terraform"
	"github.com/provlift/provlift/internal/upgrade"
)

// The test binary impersonates terraform when this variable is set, which lets
// the upgrade and validate commands be driven end to end without a real
// Terraform installation. The child process inherits the environment, so
// t.Setenv is enough to switch it on.
const (
	fakeTerraformEnv       = "PROVLIFT_CLI_FAKE_TERRAFORM"
	fakeTerraformLog       = "PROVLIFT_CLI_FAKE_LOG"
	fakeTerraformFail      = "PROVLIFT_CLI_FAKE_FAIL"
	fakeUnexpectedProvider = "PROVLIFT_CLI_FAKE_UNEXPECTED_PROVIDER"
	fakeExceedsTarget      = "PROVLIFT_CLI_FAKE_EXCEEDS_TARGET"
	fakeLockSnapshot       = "PROVLIFT_CLI_FAKE_LOCK_SNAPSHOT"
)

func TestMain(m *testing.M) {
	if os.Getenv(fakeTerraformEnv) != "" {
		os.Exit(runFakeTerraform())
	}
	os.Exit(m.Run())
}

func runFakeTerraform() int {
	args := os.Args[1:]
	if len(args) == 0 {
		return 1
	}

	command := args[0]
	if command == "providers" && len(args) > 1 {
		command = "providers " + args[1]
	}

	if logPath := os.Getenv(fakeTerraformLog); logPath != "" {
		f, err := os.OpenFile(logPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
		if err == nil {
			cwd, _ := os.Getwd()
			fmt.Fprintf(f, "%s\t%s\n", filepath.Base(cwd), strings.Join(args, " "))
			f.Close()
		}
	}

	if fail := os.Getenv(fakeTerraformFail); fail != "" && strings.HasPrefix(command, fail) {
		fmt.Fprintf(os.Stderr, "Error: simulated %s failure\n", command)
		return 1
	}

	switch command {
	case "init":
		fmt.Println("Initializing provider plugins...")
		// Only terraform writes the lock file; emulate that here so the test
		// can assert the CLI delegated rather than writing it itself.
		cwd, err := os.Getwd()
		if err != nil {
			return 1
		}
		content := `provider "registry.terraform.io/hashicorp/azurerm" {
  version = "5.6.0"
}

provider "registry.terraform.io/integrations/github" {
  version = "6.2.1"
}
`
		if _, err := os.Stat(filepath.Join(cwd, "provlift_override.tf")); err == nil {
			var locks strings.Builder
			for _, requirement := range discovery.RequirementsIn(cwd) {
				if filepath.Base(requirement.FilePath) != "provlift_override.tf" {
					continue
				}
				fmt.Fprintf(&locks, "provider %q {\n  version = %q\n}\n", requirement.Source,
					strings.TrimPrefix(requirement.Constraint, "= "))
			}
			content = locks.String()
			if content == "" {
				fmt.Fprintln(os.Stderr, "fake Terraform could not parse provider pins")
				return 1
			}
		}
		if os.Getenv(fakeUnexpectedProvider) != "" {
			content = strings.Replace(content, `version = "6.2.1"`, `version = "6.13.0"`, 1)
		}
		if os.Getenv(fakeExceedsTarget) != "" {
			content = strings.Replace(content, `version = "4.81.0"`, `version = "5.6.0"`, 1)
		}
		if err := os.WriteFile(filepath.Join(cwd, ".terraform.lock.hcl"), []byte(content), 0o644); err != nil {
			return 1
		}
		// Record exactly what Terraform wrote, so a test can prove the CLI
		// left those bytes alone afterwards.
		if snapshot := os.Getenv(fakeLockSnapshot); snapshot != "" {
			if err := os.WriteFile(snapshot, []byte(content), 0o644); err != nil {
				return 1
			}
		}

	case "providers lock":
		fmt.Println("Obtained provider checksums")

	case "validate":
		fmt.Print(`{"valid":true,"diagnostics":[]}`)

	case "version":
		fmt.Print(`{"terraform_version":"1.10.5"}`)
	}
	return 0
}

// useFakeTerraform points the harness at the impersonating binary and returns
// the path of the invocation log.
func useFakeTerraform(t *testing.T, h *harness) string {
	t.Helper()

	logPath := filepath.Join(t.TempDir(), "terraform-calls.log")
	t.Setenv(fakeTerraformEnv, "1")
	t.Setenv(fakeTerraformLog, logPath)
	h.opts.tfBinary = os.Args[0]
	return logPath
}

func readInvocations(t *testing.T, logPath string) []string {
	t.Helper()

	data, err := os.ReadFile(logPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		t.Fatalf("reading invocation log: %v", err)
	}

	var out []string
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		if line != "" {
			out = append(out, line)
		}
	}
	return out
}

func indexOfCall(calls []string, substr string) int {
	for i, c := range calls {
		if strings.Contains(c, substr) {
			return i
		}
	}
	return -1
}

func TestUpgradeDelegatesLockFileToTerraform(t *testing.T) {
	h := newHarness(t, nil)
	logPath := useFakeTerraform(t, h)

	err := h.run(t, newUpgradeCommand(h.opts),
		"--provider", "azurerm", "--to", "5.6.0", "--level", "major", "--allow-major",
		"--platforms", "linux_amd64,windows_amd64")
	if err != nil {
		t.Fatalf("upgrade: %v", err)
	}

	calls := readInvocations(t, logPath)
	if len(calls) == 0 {
		t.Fatal("terraform was never invoked")
	}

	fmtAt := indexOfCall(calls, "fmt")
	initAt := indexOfCall(calls, "init")
	lockAt := indexOfCall(calls, "providers lock")
	validateAt := indexOfCall(calls, "validate")

	for name, idx := range map[string]int{
		"fmt": fmtAt, "init": initAt, "providers lock": lockAt, "validate": validateAt,
	} {
		if idx < 0 {
			t.Errorf("terraform %s was never run; calls:\n%s", name, strings.Join(calls, "\n"))
		}
	}

	// Order matters: formatting tidies the edit, init re-resolves providers and
	// rewrites the lock file, providers lock widens the hashes, validate checks
	// the result against the new schema.
	if fmtAt > initAt || initAt > lockAt || lockAt > validateAt {
		t.Errorf("commands ran out of order:\n%s", strings.Join(calls, "\n"))
	}
	if lockAt >= 0 {
		arguments := strings.Fields(calls[lockAt])
		var platformArguments []string
		for _, argument := range arguments {
			if strings.HasPrefix(argument, "-platform=") {
				platformArguments = append(platformArguments, argument)
			}
		}
		if got := strings.Join(platformArguments, " "); got != "-platform=linux_amd64 -platform=windows_amd64" {
			t.Errorf("providers lock platform arguments = %q, want one argument per configured platform", got)
		}
	}
}

func TestUpgradeInitUsesUpgradeAndSkipsBackend(t *testing.T) {
	h := newHarness(t, nil)
	logPath := useFakeTerraform(t, h)

	err := h.run(t, newUpgradeCommand(h.opts),
		"--provider", "azurerm", "--level", "major", "--allow-major")
	if err != nil {
		t.Fatalf("upgrade: %v", err)
	}

	calls := readInvocations(t, logPath)
	initCall := ""
	for _, c := range calls {
		if strings.Contains(c, "init") {
			initCall = c
			break
		}
	}
	if initCall == "" {
		t.Fatal("init was never run")
	}

	if !strings.Contains(initCall, "-upgrade") {
		t.Errorf("init must use -upgrade to move the lock file: %q", initCall)
	}
	// A routine provider upgrade must not require state access.
	if !strings.Contains(initCall, "-backend=false") {
		t.Errorf("init should default to -backend=false: %q", initCall)
	}
}

func TestUpgradeLocksEveryRequestedPlatform(t *testing.T) {
	h := newHarness(t, nil)
	logPath := useFakeTerraform(t, h)

	err := h.run(t, newUpgradeCommand(h.opts),
		"--provider", "azurerm", "--level", "major", "--allow-major",
		"--platforms", "linux_amd64,windows_amd64,darwin_arm64")
	if err != nil {
		t.Fatalf("upgrade: %v", err)
	}

	calls := readInvocations(t, logPath)
	lockCall := ""
	for _, c := range calls {
		if strings.Contains(c, "providers lock") {
			lockCall = c
			break
		}
	}
	if lockCall == "" {
		t.Fatal("providers lock was never run")
	}

	for _, p := range []string{"linux_amd64", "windows_amd64", "darwin_arm64"} {
		if !strings.Contains(lockCall, "-platform="+p) {
			t.Errorf("providers lock missing %s: %q", p, lockCall)
		}
	}
}

func TestUpgradeBackendFlagIsHonoured(t *testing.T) {
	h := newHarness(t, nil)
	logPath := useFakeTerraform(t, h)

	err := h.run(t, newUpgradeCommand(h.opts),
		"--provider", "azurerm", "--level", "major", "--allow-major", "--backend")
	if err != nil {
		t.Fatalf("upgrade: %v", err)
	}

	calls := readInvocations(t, logPath)
	if indexOfCall(calls, "-backend=true") < 0 {
		t.Errorf("--backend was not passed through:\n%s", strings.Join(calls, "\n"))
	}
}

func TestUpgradeReportsTerraformFailure(t *testing.T) {
	h := newHarness(t, nil)
	useFakeTerraform(t, h)
	t.Setenv(fakeTerraformFail, "validate")

	err := h.run(t, newUpgradeCommand(h.opts),
		"--provider", "azurerm", "--level", "major", "--allow-major")
	if err == nil {
		t.Fatal("expected a validation failure to be reported")
	}

	var exit *ExitCodeError
	if !errors.As(err, &exit) {
		t.Fatalf("err = %T(%v), want *ExitCodeError", err, err)
	}
	if exit.Code != ExitValidationFailed {
		t.Errorf("exit code = %d, want %d", exit.Code, ExitValidationFailed)
	}
}

func TestUpgradeStopsAfterFailedInit(t *testing.T) {
	// Without a successful init there is no provider schema, so running
	// validate afterwards would fail for a misleading reason.
	h := newHarness(t, nil)
	logPath := useFakeTerraform(t, h)
	t.Setenv(fakeTerraformFail, "init")
	lockPath := filepath.Join(h.dir, ".terraform.lock.hcl")
	original, err := os.ReadFile(lockPath)
	if err != nil {
		t.Fatal(err)
	}

	err = h.run(t, newUpgradeCommand(h.opts),
		"--provider", "azurerm", "--level", "major", "--allow-major")
	if err == nil {
		t.Fatal("expected an error when init fails")
	}

	calls := readInvocations(t, logPath)
	if indexOfCall(calls, "validate") >= 0 {
		t.Errorf("validate must not run after init fails:\n%s", strings.Join(calls, "\n"))
	}
	current, err := os.ReadFile(lockPath)
	if err != nil || string(current) != string(original) {
		t.Fatalf("original lock was not preserved: %v", err)
	}
}

func TestUpgradeRejectsUnplannedProviderChange(t *testing.T) {
	h := newHarness(t, nil)
	logPath := useFakeTerraform(t, h)
	t.Setenv(fakeUnexpectedProvider, "1")
	lockPath := filepath.Join(h.dir, ".terraform.lock.hcl")
	original, err := os.ReadFile(lockPath)
	if err != nil {
		t.Fatal(err)
	}
	err = h.run(t, newUpgradeCommand(h.opts),
		"--provider", "azurerm", "--to", "5.6.0", "--level", "major", "--allow-major")
	if err == nil || !strings.Contains(err.Error(), "unplanned provider change") {
		t.Fatalf("expected unplanned provider change, got %v", err)
	}
	current, err := os.ReadFile(lockPath)
	if err != nil || string(current) != string(original) {
		t.Fatalf("original lock was not restored: %v", err)
	}
	for _, call := range readInvocations(t, logPath) {
		if strings.Contains(call, "providers lock") || strings.Contains(call, "validate") {
			t.Errorf("Terraform continued after unauthorized change: %s", call)
		}
	}
}

func TestUpgradeRejectsProviderVersionBeyondPlannedLevel(t *testing.T) {
	h := newHarness(t, nil)
	useFakeTerraform(t, h)
	t.Setenv(fakeExceedsTarget, "1")
	lockPath := filepath.Join(h.dir, ".terraform.lock.hcl")
	original, err := os.ReadFile(lockPath)
	if err != nil {
		t.Fatal(err)
	}
	err = h.run(t, newUpgradeCommand(h.opts),
		"--provider", "azurerm", "--level", "minor", "--exact-level")
	if err == nil || !strings.Contains(err.Error(), "instead of planned 4.81.0") {
		t.Fatalf("expected planned target refusal, got %v", err)
	}
	current, err := os.ReadFile(lockPath)
	if err != nil || string(current) != string(original) {
		t.Fatalf("original lock was not restored: %v", err)
	}
}

func TestSyntheticChildActionDoesNotDowngradeRootLock(t *testing.T) {
	h := newHarness(t, nil)
	useFakeTerraform(t, h)
	ws, err := h.opts.open()
	if err != nil {
		t.Fatal(err)
	}
	address, err := model.ParseAddress("hashicorp/azurerm")
	if err != nil {
		t.Fatal(err)
	}
	plan := &upgrade.Plan{Actions: []upgrade.Action{{
		ModuleDir: h.dir, Address: address, Level: upgrade.LevelNone,
		From: "3.85.0", To: "3.85.0",
	}}}
	if _, err := applyTerraform(context.Background(), h.opts, ws, plan, &upgradeOptions{skipValidate: true}, nil); err != nil {
		t.Fatal(err)
	}
	locks, _, err := lockfile.Read(h.dir)
	if err != nil {
		t.Fatal(err)
	}
	if got := locks[address.String()].Version; got != "4.65.0" {
		t.Errorf("root lock downgraded to %s from 4.65.0", got)
	}
}

func TestSyntheticChildActionDoesNotOverrideRealTarget(t *testing.T) {
	h := newHarness(t, nil)
	useFakeTerraform(t, h)
	ws, err := h.opts.open()
	if err != nil {
		t.Fatal(err)
	}
	address, err := model.ParseAddress("hashicorp/azurerm")
	if err != nil {
		t.Fatal(err)
	}
	plan := &upgrade.Plan{Actions: []upgrade.Action{
		{ModuleDir: h.dir, Address: address, Level: upgrade.LevelMajor, From: "4.65.0", To: "5.6.0"},
		{ModuleDir: h.dir, Address: address, Level: upgrade.LevelNone, From: "3.85.0", To: "3.85.0"},
	}}
	if _, err := applyTerraform(context.Background(), h.opts, ws, plan, &upgradeOptions{skipValidate: true}, nil); err != nil {
		t.Fatal(err)
	}
	locks, _, err := lockfile.Read(h.dir)
	if err != nil {
		t.Fatal(err)
	}
	if got := locks[address.String()].Version; got != "5.6.0" {
		t.Errorf("synthetic child floor replaced planned 5.6.0 with %s", got)
	}
}

func TestChildUpgradeCannotLowerConsumingRootTarget(t *testing.T) {
	for _, tc := range []struct {
		name       string
		rootTarget string
		want       string
	}{
		{"child floor below root lock", "", "4.65.0"},
		{"child action after real root upgrade", "5.6.0", "5.6.0"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t, nil)
			useFakeTerraform(t, h)
			ws, err := h.opts.open()
			if err != nil {
				t.Fatal(err)
			}
			childDir := filepath.Join(h.dir, "child")
			ws.inv.Modules[0].Children = append(ws.inv.Modules[0].Children, childDir)
			ws.inv.Modules = append(ws.inv.Modules, &model.Module{Dir: childDir, RelDir: "child"})
			address, err := model.ParseAddress("hashicorp/azurerm")
			if err != nil {
				t.Fatal(err)
			}
			plan := &upgrade.Plan{}
			if tc.rootTarget != "" {
				plan.Actions = append(plan.Actions, upgrade.Action{
					ModuleDir: h.dir, Address: address, Level: upgrade.LevelMajor,
					From: "4.65.0", To: tc.rootTarget,
				})
			}
			plan.Actions = append(plan.Actions, upgrade.Action{
				ModuleDir: childDir, Address: address, Level: upgrade.LevelPatch,
				From: "3.85.0", To: "3.85.1",
			})
			if _, err := applyTerraform(context.Background(), h.opts, ws, plan, &upgradeOptions{skipValidate: true}, nil); err != nil {
				t.Fatal(err)
			}
			locks, _, err := lockfile.Read(h.dir)
			if err != nil {
				t.Fatal(err)
			}
			if got := locks[address.String()].Version; got != tc.want {
				t.Errorf("consuming root lock = %s, want %s", got, tc.want)
			}
		})
	}
}

func TestInitWithPlannedProvidersRefusesExistingOverride(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "provlift_override.tf")
	const original = "operator-owned override"
	if err := os.WriteFile(path, []byte(original), 0o644); err != nil {
		t.Fatal(err)
	}
	root := &model.Module{Dir: dir, RelDir: "."}
	_, err := initWithPlannedProviders(context.Background(), &terraform.Runner{}, root,
		map[string]string{"registry.terraform.io/aztfmod/azurecaf": "1.2.32"}, false)
	if !errors.Is(err, os.ErrExist) {
		t.Fatalf("expected existing override refusal, got %v", err)
	}
	got, err := os.ReadFile(path)
	if err != nil || string(got) != original {
		t.Fatalf("existing override changed: %q, %v", got, err)
	}
}

func TestUpgradeStopsAfterFailedProvidersLock(t *testing.T) {
	h := newHarness(t, nil)
	logPath := useFakeTerraform(t, h)
	t.Setenv(fakeTerraformFail, "providers lock")
	lockPath := filepath.Join(h.dir, ".terraform.lock.hcl")
	original, err := os.ReadFile(lockPath)
	if err != nil {
		t.Fatal(err)
	}

	err = h.run(t, newUpgradeCommand(h.opts),
		"--provider", "azurerm", "--level", "major", "--allow-major")
	if err == nil {
		t.Fatal("expected an error when providers lock fails")
	}

	calls := readInvocations(t, logPath)
	if indexOfCall(calls, "validate") >= 0 {
		t.Errorf("validate must not run after providers lock fails:\n%s", strings.Join(calls, "\n"))
	}
	current, err := os.ReadFile(lockPath)
	if err != nil || string(current) != string(original) {
		t.Fatalf("original lock was not restored: %v", err)
	}
}

func TestUpgradeSkipValidate(t *testing.T) {
	h := newHarness(t, nil)
	logPath := useFakeTerraform(t, h)

	err := h.run(t, newUpgradeCommand(h.opts),
		"--provider", "azurerm", "--level", "major", "--allow-major", "--skip-validate")
	if err != nil {
		t.Fatalf("upgrade: %v", err)
	}

	calls := readInvocations(t, logPath)
	if indexOfCall(calls, "validate") >= 0 {
		t.Errorf("--skip-validate did not suppress validate:\n%s", strings.Join(calls, "\n"))
	}
	if indexOfCall(calls, "init") < 0 {
		t.Error("init should still run so the lock file is regenerated")
	}
}

func TestUpgradeNoFmtFlag(t *testing.T) {
	h := newHarness(t, nil)
	logPath := useFakeTerraform(t, h)

	err := h.run(t, newUpgradeCommand(h.opts),
		"--provider", "azurerm", "--level", "major", "--allow-major", "--fmt=false")
	if err != nil {
		t.Fatalf("upgrade: %v", err)
	}

	for _, c := range readInvocations(t, logPath) {
		if strings.Contains(c, "\tfmt") {
			t.Errorf("--fmt=false did not suppress terraform fmt: %q", c)
		}
	}
}

func TestUpgradeFmtTargetsEditedFilesOnly(t *testing.T) {
	h := newHarness(t, map[string]string{
		// Pre-existing drift in a sibling file and in a nested module must not
		// be pulled into the upgrade diff by the post-edit fmt step.
		"drift.tf":            "variable \"drift\" {\n  type = string\n}\n",
		"modules/net/vars.tf": "variable \"net_cidr\" {\n  default = \"a\"\n}\n",
	})
	logPath := useFakeTerraform(t, h)

	err := h.run(t, newUpgradeCommand(h.opts),
		"--provider", "azurerm", "--to", "5.6.0", "--level", "major", "--allow-major")
	if err != nil {
		t.Fatalf("upgrade: %v", err)
	}

	var fmtCalls []string
	for _, c := range readInvocations(t, logPath) {
		if strings.Contains(c, "\tfmt") {
			fmtCalls = append(fmtCalls, c)
		}
	}
	if len(fmtCalls) != 1 {
		t.Fatalf("expected one fmt invocation, got %d:\n%s", len(fmtCalls), strings.Join(fmtCalls, "\n"))
	}
	if strings.Contains(fmtCalls[0], "-recursive") {
		t.Errorf("post-edit fmt must not recurse into nested modules: %q", fmtCalls[0])
	}
	if !strings.Contains(fmtCalls[0], " ./main.tf") {
		t.Errorf("fmt must target the edited file: %q", fmtCalls[0])
	}
	for _, untouched := range []string{"drift.tf", "vars.tf", "modules"} {
		if strings.Contains(fmtCalls[0], untouched) {
			t.Errorf("fmt must not target unedited %s: %q", untouched, fmtCalls[0])
		}
	}

	// The unformatted file in the nested module is byte-identical afterwards.
	got, err := os.ReadFile(filepath.Join(h.dir, "modules", "net", "vars.tf"))
	if err != nil {
		t.Fatalf("reading nested module file: %v", err)
	}
	if want := "variable \"net_cidr\" {\n  default = \"a\"\n}\n"; string(got) != want {
		t.Errorf("nested module file was rewritten:\n%s", got)
	}
}

func TestValidateCommandRunsFmtCheckAndValidate(t *testing.T) {
	h := newHarness(t, nil)
	logPath := useFakeTerraform(t, h)

	if err := h.run(t, newValidateCommand(h.opts)); err != nil {
		t.Fatalf("validate: %v", err)
	}

	calls := readInvocations(t, logPath)
	if indexOfCall(calls, "fmt -check") < 0 {
		t.Errorf("fmt -check was not run:\n%s", strings.Join(calls, "\n"))
	}
	if indexOfCall(calls, "validate") < 0 {
		t.Errorf("validate was not run:\n%s", strings.Join(calls, "\n"))
	}
	// plan touches real infrastructure and must stay opt-in.
	if indexOfCall(calls, "plan") >= 0 {
		t.Errorf("plan must not run by default:\n%s", strings.Join(calls, "\n"))
	}
}

func TestValidateCommandPlanIsOptIn(t *testing.T) {
	h := newHarness(t, nil)
	logPath := useFakeTerraform(t, h)

	if err := h.run(t, newValidateCommand(h.opts), "--plan"); err != nil {
		t.Fatalf("validate: %v", err)
	}

	if indexOfCall(readInvocations(t, logPath), "plan") < 0 {
		t.Error("--plan did not run terraform plan")
	}
}

func TestValidateCommandReportsFailure(t *testing.T) {
	h := newHarness(t, nil)
	useFakeTerraform(t, h)
	t.Setenv(fakeTerraformFail, "validate")

	err := h.run(t, newValidateCommand(h.opts))
	if err == nil {
		t.Fatal("expected a failure to be reported")
	}

	var exit *ExitCodeError
	if !errors.As(err, &exit) {
		t.Fatalf("err = %T(%v), want *ExitCodeError", err, err)
	}
	if exit.Code != ExitValidationFailed {
		t.Errorf("exit code = %d, want %d", exit.Code, ExitValidationFailed)
	}
}

func TestValidateCommandNoInit(t *testing.T) {
	h := newHarness(t, nil)
	logPath := useFakeTerraform(t, h)

	if err := h.run(t, newValidateCommand(h.opts), "--no-init"); err != nil {
		t.Fatalf("validate: %v", err)
	}

	for _, c := range readInvocations(t, logPath) {
		if strings.Contains(c, "\tinit") {
			t.Errorf("--no-init did not suppress init: %q", c)
		}
	}
}
