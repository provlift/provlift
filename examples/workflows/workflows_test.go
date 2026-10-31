package workflows

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

type workflowStep struct {
	ID   string            `yaml:"id"`
	If   string            `yaml:"if"`
	Name string            `yaml:"name"`
	Run  string            `yaml:"run"`
	Uses string            `yaml:"uses"`
	Env  map[string]string `yaml:"env"`
	With map[string]string `yaml:"with"`
}

type workflow struct {
	Env map[string]string `yaml:"env"`
	On  struct {
		Schedule []struct {
			Cron string `yaml:"cron"`
		} `yaml:"schedule"`
		Dispatch struct {
			Inputs map[string]struct {
				Type    string   `yaml:"type"`
				Options []string `yaml:"options"`
				Default any      `yaml:"default"`
			} `yaml:"inputs"`
		} `yaml:"workflow_dispatch"`
	} `yaml:"on"`
	Jobs map[string]struct {
		If       string `yaml:"if"`
		Strategy struct {
			FailFast *bool  `yaml:"fail-fast"`
			Matrix   string `yaml:"matrix"`
		} `yaml:"strategy"`
		Steps []workflowStep `yaml:"steps"`
	} `yaml:"jobs"`
}

func TestMain(suite *testing.M) {
	if os.Getenv("PROVLIFT_WORKFLOW_HELPER") == "1" {
		os.Exit(fakeProvlift())
	}
	os.Exit(suite.Run())
}

func fakeProvlift() int {
	arguments := os.Args[1:]
	log, err := os.OpenFile(os.Getenv("PROVLIFT_WORKFLOW_CALLS"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return 1
	}
	err = json.NewEncoder(log).Encode(arguments)
	log.Close()
	if err != nil {
		return 1
	}
	if len(arguments) > 0 && arguments[0] == "list" {
		response := os.Getenv("PROVLIFT_WORKFLOW_LIST_RESPONSE")
		if response == "" {
			response = `{"providers":[],"diagnostics":[]}`
		}
		fmt.Println(response)
		return 0
	}
	level := ""
	for index, argument := range arguments {
		if argument == "--level" && index+1 < len(arguments) {
			level = arguments[index+1]
		}
	}
	var responses map[string]json.RawMessage
	if err := json.Unmarshal([]byte(os.Getenv("PROVLIFT_WORKFLOW_RESPONSES")), &responses); err != nil {
		return 1
	}
	response := responses[level]
	if len(response) == 0 {
		response = json.RawMessage(`{"plan":{"actions":[],"skipped":[]},"pull_request":{"branch":"provlift/testing-1.0.1","title":"Upgrade fixture","labels":[]}}`)
	}
	fmt.Println(string(response))
	return 0
}

func readWorkflow(t *testing.T, name string) workflow {
	t.Helper()
	content, err := os.ReadFile(name)
	if err != nil {
		t.Fatal(err)
	}
	var result workflow
	if err := yaml.Unmarshal(content, &result); err != nil {
		t.Fatal(err)
	}
	return result
}

func findStep(t *testing.T, document workflow, job, id string) workflowStep {
	t.Helper()
	for _, step := range document.Jobs[job].Steps {
		if step.ID == id {
			return step
		}
	}
	t.Fatalf("step %s missing from job %s", id, job)
	return workflowStep{}
}

type scriptHarness struct {
	dir         string
	bin         string
	bash        string
	output      string
	calls       string
	responses   string
	environment []string
}

func newScriptHarness(t *testing.T) *scriptHarness {
	t.Helper()
	harness := newBashHarness(t)
	if _, err := exec.LookPath("jq"); err != nil {
		t.Skip("jq is required to execute workflow shell tests")
	}
	return harness
}

// newBashHarness is newScriptHarness for steps that do not use jq.
func newBashHarness(t *testing.T) *scriptHarness {
	t.Helper()
	bash, err := exec.LookPath("bash")
	if runtime.GOOS == "windows" {
		for _, candidate := range []string{`C:\Program Files\Git\bin\bash.exe`, `C:\Program Files\Git\usr\bin\bash.exe`} {
			if _, statErr := os.Stat(candidate); statErr == nil {
				bash, err = candidate, nil
				break
			}
		}
	}
	if err != nil {
		t.Skip("Bash is required to execute workflow shell tests")
	}
	dir := t.TempDir()
	bin := filepath.Join(dir, "bin")
	if err := os.Mkdir(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	stub := "#!/bin/sh\nexec \"$PROVLIFT_WORKFLOW_BINARY\" \"$@\"\n"
	if err := os.WriteFile(filepath.Join(bin, "provlift"), []byte(stub), 0o755); err != nil {
		t.Fatal(err)
	}
	return &scriptHarness{
		dir: dir, bin: bin, bash: bash, output: filepath.Join(dir, "output"),
		calls: filepath.Join(dir, "calls.jsonl"), responses: "{}",
	}
}

func (h *scriptHarness) run(t *testing.T, script string, values map[string]string) (string, error) {
	t.Helper()
	for expression, value := range values {
		script = strings.ReplaceAll(script, "${{ "+expression+" }}", value)
	}
	if strings.Contains(script, "${{") {
		t.Fatal("test did not supply all workflow expression values")
	}
	script = "bin=\"$PROVLIFT_WORKFLOW_BIN\"\nif command -v cygpath >/dev/null; then bin=$(cygpath -u \"$bin\"); fi\nexport PATH=\"$bin:$PATH\"\n" + script
	path := filepath.Join(h.dir, "step.sh")
	if err := os.WriteFile(path, []byte(script), 0o600); err != nil {
		t.Fatal(err)
	}
	command := exec.Command(h.bash, filepath.ToSlash(path))
	command.Dir = h.dir
	command.Env = append(os.Environ(),
		"PROVLIFT_WORKFLOW_BIN="+h.bin,
		"PROVLIFT_WORKFLOW_HELPER=1", "PROVLIFT_WORKFLOW_BINARY="+filepath.ToSlash(os.Args[0]),
		"PROVLIFT_WORKFLOW_CALLS="+h.calls, "PROVLIFT_WORKFLOW_RESPONSES="+h.responses,
		"RUNNER_TEMP="+filepath.ToSlash(h.dir), "GITHUB_OUTPUT="+filepath.ToSlash(h.output),
		"GITHUB_ENV="+filepath.ToSlash(filepath.Join(h.dir, "github-env")))
	command.Env = append(command.Env, h.environment...)
	output, err := command.CombinedOutput()
	return string(output), err
}

func (h *scriptHarness) invocations(t *testing.T) [][]string {
	t.Helper()
	content, err := os.ReadFile(h.calls)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	var calls [][]string
	for _, line := range strings.Split(strings.TrimSpace(string(content)), "\n") {
		var arguments []string
		if err := json.Unmarshal([]byte(line), &arguments); err != nil {
			t.Fatal(err)
		}
		calls = append(calls, arguments)
	}
	return calls
}

func TestUpgradeWorkerRefusesUnacknowledgedMajor(t *testing.T) {
	step := findStep(t, readWorkflow(t, "provider-upgrades.yml"), "upgrade", "upgrade")
	harness := newScriptHarness(t)
	output, err := harness.run(t, step.Run, map[string]string{
		"matrix.provider": "hashicorp/null", "matrix.level": "major", "inputs.allow_major || 'false'": "false",
	})
	if err == nil || len(harness.invocations(t)) != 0 || !strings.Contains(output, "requires allow_major") {
		t.Fatalf("worker must refuse a major without acknowledgement before invoking provlift: err=%v\n%s", err, output)
	}
}

func hasArgument(arguments []string, wanted string) bool {
	for _, argument := range arguments {
		if argument == wanted {
			return true
		}
	}
	return false
}

func TestDiscoveryMatrixAndScheduledSafety(t *testing.T) {
	document := readWorkflow(t, "provider-upgrades.yml")
	step := findStep(t, document, "discover", "plan")
	if document.Jobs["upgrade"].If != "needs.discover.outputs.found == 'true'" {
		t.Fatal("upgrade jobs must be gated by a nonempty discovery matrix")
	}
	for _, scenario := range []struct {
		name          string
		acknowledged  string
		responses     string
		wantProviders int
		wantStages    string
	}{
		{"no upgrades", "false", "{}", 0, "patch,minor"},
		{"null actions from CLI no-op", "false", `{"minor":{"plan":{"actions":null,"skipped":[]}}}`, 0, "patch,minor"},
		{"one entry per provider", "false", `{"patch":{"plan":{"actions":[{"source":"registry.terraform.io/hashicorp/null"},{"source":"registry.terraform.io/hashicorp/null"},{"source":"registry.terraform.io/hashicorp/random"}]}}}`, 2, "patch"},
		{"minor fallback", "false", `{"minor":{"plan":{"actions":[{"source":"registry.terraform.io/hashicorp/null"}]}}}`, 1, "patch,minor"},
		{"scheduled major available but not authorized", "false", `{"major":{"plan":{"actions":[{"source":"registry.terraform.io/hashicorp/null"}]}}}`, 0, "patch,minor"},
		{"acknowledged major fallback", "true", `{"major":{"plan":{"actions":[{"source":"registry.terraform.io/hashicorp/null"}]}}}`, 1, "patch,minor,major"},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			harness := newScriptHarness(t)
			harness.responses = scenario.responses
			output, err := harness.run(t, step.Run, map[string]string{
				"inputs.level || 'auto'": "auto", "inputs.allow_major || 'false'": scenario.acknowledged,
			})
			if err != nil {
				t.Fatalf("discovery script failed: %v\n%s", err, output)
			}
			var stages []string
			for _, arguments := range harness.invocations(t) {
				if !hasArgument(arguments, "--dry-run") || !hasArgument(arguments, "--exact-level") {
					t.Fatalf("discovery must use read-only exact-stage plans: %v", arguments)
				}
				for index, argument := range arguments {
					if argument == "--level" && index+1 < len(arguments) {
						stages = append(stages, arguments[index+1])
					}
				}
				if scenario.acknowledged == "false" && hasArgument(arguments, "--allow-major") {
					t.Fatalf("scheduled discovery authorized a breaking upgrade: %v", arguments)
				}
			}
			if got := strings.Join(stages, ","); got != scenario.wantStages {
				t.Fatalf("stages=%s, want %s", got, scenario.wantStages)
			}
			content, err := os.ReadFile(harness.output)
			if err != nil {
				t.Fatal(err)
			}
			found := false
			var matrix struct {
				Include []struct {
					Provider string `json:"provider"`
				} `json:"include"`
			}
			for _, line := range strings.Split(string(content), "\n") {
				if strings.HasPrefix(line, "matrix=") {
					if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "matrix=")), &matrix); err != nil {
						t.Fatal(err)
					}
				}
				found = found || line == "found=true"
			}
			if len(matrix.Include) != scenario.wantProviders || found != (scenario.wantProviders > 0) {
				t.Fatalf("unexpected matrix/gate: %s", content)
			}
			seen := map[string]bool{}
			for _, entry := range matrix.Include {
				if seen[entry.Provider] {
					t.Fatalf("duplicate provider entry: %s", entry.Provider)
				}
				seen[entry.Provider] = true
			}
		})
	}
}

func TestWorkerRerunUsesSamePullRequestBranch(t *testing.T) {
	document := readWorkflow(t, "provider-upgrades.yml")
	step := findStep(t, document, "upgrade", "upgrade")
	var prStep *workflowStep
	for index := range document.Jobs["upgrade"].Steps {
		candidate := &document.Jobs["upgrade"].Steps[index]
		if strings.HasPrefix(candidate.Uses, "peter-evans/create-pull-request@") {
			prStep = candidate
			break
		}
	}
	if prStep == nil || prStep.With["branch"] != "${{ steps.upgrade.outputs.branch }}" {
		t.Fatal("create-pull-request must use the planner's deterministic branch")
	}
	harness := newScriptHarness(t)
	for attempt := 0; attempt < 2; attempt++ {
		output, err := harness.run(t, step.Run, map[string]string{
			"matrix.provider": "hashicorp/null", "matrix.level": "minor", "inputs.allow_major || 'false'": "false",
		})
		if err != nil {
			t.Fatalf("worker run %d failed: %v\n%s", attempt, err, output)
		}
	}
	content, err := os.ReadFile(harness.output)
	if err != nil {
		t.Fatal(err)
	}
	if count := strings.Count(string(content), "branch=provlift/testing-1.0.1"); count != 2 {
		t.Fatalf("rerun changed or omitted the target branch: %s", content)
	}
	if calls := harness.invocations(t); len(calls) != 2 {
		t.Fatalf("expected two isolated provider executions, got %v", calls)
	}
}

func TestAcknowledgedMajorWorkerPassesExplicitAuthorization(t *testing.T) {
	step := findStep(t, readWorkflow(t, "provider-upgrades.yml"), "upgrade", "upgrade")
	harness := newScriptHarness(t)
	output, err := harness.run(t, step.Run, map[string]string{
		"matrix.provider": "hashicorp/null", "matrix.level": "major", "inputs.allow_major || 'false'": "true",
	})
	if err != nil {
		t.Fatalf("acknowledged major refused: %v\n%s", err, output)
	}
	calls := harness.invocations(t)
	if len(calls) != 1 || !hasArgument(calls[0], "--allow-major") || !hasArgument(calls[0], "--next-major") {
		t.Fatalf("worker did not pass acknowledgement and next-major: %v", calls)
	}
}

func TestCompatibilityVendorsBeforeChecking(t *testing.T) {
	document := readWorkflow(t, "provider-compatibility.yml")
	steps := document.Jobs["compatibility"].Steps
	vendorIndex := -1
	for index, step := range steps {
		if step.Name == "Vendor the module tree" {
			vendorIndex = index
		}
		if strings.Contains(step.Run, "provlift check") && (vendorIndex < 0 || vendorIndex >= index) {
			t.Fatal("compatibility check runs before module vendoring")
		}
	}
	if vendorIndex < 0 {
		t.Fatal("compatibility workflow has no vendoring step")
	}
	harness := newScriptHarness(t)
	for _, dir := range []string{".", "example", "examples/single"} {
		path := filepath.Join(harness.dir, filepath.FromSlash(dir), "main.tf")
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("terraform {}\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	terraformLog := filepath.Join(harness.dir, "terraform-calls")
	stub := "#!/bin/sh\nprintf '%s|%s\\n' \"$PWD\" \"$*\" >> \"$PROVLIFT_TERRAFORM_CALLS\"\n"
	if err := os.WriteFile(filepath.Join(harness.bin, "terraform"), []byte(stub), 0o755); err != nil {
		t.Fatal(err)
	}
	harness.environment = []string{"MODULES=.,example,examples/*,does-not-exist", "PROVLIFT_TERRAFORM_CALLS=" + filepath.ToSlash(terraformLog)}
	output, err := harness.run(t, steps[vendorIndex].Run, nil)
	if err != nil {
		t.Fatalf("vendoring step failed: %v\n%s", err, output)
	}
	content, err := os.ReadFile(terraformLog)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(content)), "\n")
	if len(lines) != 3 {
		t.Fatalf("expected get on root and both example roots: %s", content)
	}
	for _, line := range lines {
		if !strings.HasSuffix(strings.TrimSpace(line), "|get") {
			t.Fatalf("vendoring must not initialize backends or providers: %s", line)
		}
	}
	for _, suffix := range []string{"/example|get", "/examples/single|get"} {
		if !strings.Contains(string(content), suffix) {
			t.Fatalf("vendoring omitted %s: %s", suffix, content)
		}
	}
}

func TestCompatibilityRequiresMajorAcknowledgement(t *testing.T) {
	step := findStep(t, readWorkflow(t, "provider-compatibility.yml"), "compatibility", "attempt")
	harness := newScriptHarness(t)
	harness.environment = []string{"MODULES=.", "PROVIDER_SOURCE=" + testProviderSource}
	harness.responses = `{"major":{"plan":{"actions":[]},"changed_files":[],"terraform_runs":[],"pull_request":{"branch":"provlift/no-changes","title":"No changes","labels":[]}}}`
	output, err := harness.run(t, step.Run, map[string]string{
		"inputs.level || 'minor'": "major", "inputs.raise_floor || 'false'": "false",
		"inputs.allow_major || 'false'": "false", "inputs.target_version || ''": "",
	})
	if err == nil || len(harness.invocations(t)) != 0 || !strings.Contains(output, "requires allow_major") {
		t.Fatalf("compatibility major must require acknowledgement before invoking provlift: err=%v\n%s", err, output)
	}
}

func TestCompatibilityDoesNotRequestPRForLockOnlyChanges(t *testing.T) {
	step := findStep(t, readWorkflow(t, "provider-compatibility.yml"), "compatibility", "attempt")
	harness := newScriptHarness(t)
	harness.environment = []string{"MODULES=.", "PROVIDER_SOURCE=" + testProviderSource, "GITHUB_STEP_SUMMARY=" + filepath.ToSlash(filepath.Join(harness.dir, "summary"))}
	harness.responses = `{"minor":{"plan":{"actions":[{"constraint":{"changed":false}}]},"changed_files":[".terraform.lock.hcl","examples/single/.terraform.lock.hcl"],"terraform_runs":[{"ok":true}],"pull_request":{"branch":"provlift/testing","title":"Upgrade fixture","labels":[]}}}`
	if err := os.WriteFile(filepath.Join(harness.dir, "check.json"), []byte(`{"providers":[]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	output, err := harness.run(t, step.Run, map[string]string{
		"inputs.level || 'minor'": "minor", "inputs.raise_floor || 'false'": "false",
		"inputs.allow_major || 'false'": "false", "inputs.target_version || ''": "",
	})
	if err != nil {
		t.Fatalf("compatibility step failed: %v\n%s", err, output)
	}
	content, err := os.ReadFile(harness.output)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(content), "changed=0\n") {
		t.Fatalf("lock-only changes must not enable a shared-module PR: %s", content)
	}
}

func TestWorkflowScheduleAndDispatchInputs(t *testing.T) {
	for _, name := range []string{"provider-upgrades.yml", "provider-compatibility.yml"} {
		document := readWorkflow(t, name)
		if want := "${{ vars.PROVLIFT_VERSION || 'v" + releasedVersion(t) + "' }}"; document.Env["PROVLIFT_VERSION"] != want {
			t.Fatalf("%s must allow a repository PROVLIFT_VERSION override and default to the released tag: got %q, want %q",
				name, document.Env["PROVLIFT_VERSION"], want)
		}
		var installSteps []workflowStep
		for _, job := range document.Jobs {
			for _, step := range job.Steps {
				if step.Name == "Install provlift" {
					installSteps = append(installSteps, step)
				}
			}
		}
		if len(installSteps) == 0 {
			t.Fatalf("%s must install provlift", name)
		}
		for _, step := range installSteps {
			if !strings.Contains(step.Run, `@"$PROVLIFT_VERSION"`) || strings.Contains(step.Run, "${{") {
				t.Fatalf("%s must pass PROVLIFT_VERSION through a quoted shell variable: %s", name, step.Run)
			}
		}
		if len(document.On.Schedule) != 1 || document.On.Schedule[0].Cron != "0 6 * * 1" {
			t.Fatalf("%s must declare its weekly schedule", name)
		}
		inputs := document.On.Dispatch.Inputs
		if inputs["level"].Type != "choice" {
			t.Fatalf("%s must support manual level selection", name)
		}
		for _, level := range []string{"patch", "minor", "major"} {
			if !hasArgument(inputs["level"].Options, level) {
				t.Fatalf("%s is missing the %s level", name, level)
			}
		}
		if inputs["allow_major"].Type != "boolean" || inputs["allow_major"].Default != false {
			t.Fatalf("%s must require explicit major acknowledgement", name)
		}
		if name == "provider-compatibility.yml" && (inputs["target_version"].Type != "string" || inputs["target_version"].Default != "") {
			t.Fatal("compatibility workflow must allow an optional exact target version")
		}
		if name == "provider-compatibility.yml" && (inputs["provider"].Type != "string" || inputs["provider"].Default != "") {
			t.Fatal("compatibility workflow must take the provider as a free-form input")
		}
	}
}

func TestCompatibilityPassesExplicitTargetWithoutNextMajor(t *testing.T) {
	step := findStep(t, readWorkflow(t, "provider-compatibility.yml"), "compatibility", "attempt")
	if step.Env["PROVLIFT_TARGET_VERSION"] != "${{ inputs.target_version }}" || strings.Contains(step.Run, "inputs.target_version") {
		t.Fatal("free-form target_version must be passed through the step environment, not interpolated into shell")
	}
	harness := newScriptHarness(t)
	if err := os.WriteFile(filepath.Join(harness.dir, "main.tf"), []byte("terraform {}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	terraformLog := filepath.Join(harness.dir, "terraform-calls")
	terraformStub := "#!/bin/sh\nprintf '%s\\n' \"$*\" >> \"$PROVLIFT_WORKFLOW_TERRAFORM_CALLS\"\n"
	if err := os.WriteFile(filepath.Join(harness.bin, "terraform"), []byte(terraformStub), 0o755); err != nil {
		t.Fatal(err)
	}
	harness.environment = []string{
		"MODULES=.", "PROVIDER_SOURCE=" + testProviderSource, "PROVLIFT_TARGET_VERSION=5.6.0", "GITHUB_STEP_SUMMARY=" + filepath.ToSlash(filepath.Join(harness.dir, "summary")),
		"PROVLIFT_WORKFLOW_TERRAFORM_CALLS=" + filepath.ToSlash(terraformLog),
		"PROVLIFT_WORKFLOW_LIST_RESPONSE={\"providers\":[{\"module\":\".\",\"is_root\":true,\"source\":\"registry.terraform.io/hashicorp/aws\",\"locked\":\"5.6.0\"},{\"module\":\"examples/single\",\"is_root\":true,\"source\":\"registry.terraform.io/hashicorp/aws\",\"locked\":\"5.5.0\"}],\"diagnostics\":[]}",
	}
	harness.responses = `{"major":{"plan":{"actions":[{"constraint":{"changed":true}}],"skipped":[]},"changed_files":["versions.tf"],"terraform_runs":[{"module":".","command":"validate","ok":true}],"pull_request":{"branch":"provlift/storage-5.6.0","title":"Upgrade fixture","labels":["provider-major"]}}}`
	if err := os.WriteFile(filepath.Join(harness.dir, "pr-body.md"), []byte("Provider floor update"), 0o600); err != nil {
		t.Fatal(err)
	}
	output, err := harness.run(t, step.Run, map[string]string{
		"inputs.level || 'minor'":       "major",
		"inputs.allow_major || 'false'": "true", "inputs.raise_floor || 'false'": "false",
	})
	if err != nil {
		t.Fatalf("explicit compatibility target failed: %v\n%s\ninvocations: %v", err, output, harness.invocations(t))
	}
	calls := harness.invocations(t)
	if len(calls) != 2 || calls[0][0] != "upgrade" || calls[1][0] != "list" || !hasArgument(calls[0], "--to") || !hasArgument(calls[0], "5.6.0") ||
		!hasArgument(calls[0], testProviderSource) || !hasArgument(calls[1], testProviderSource) ||
		!hasArgument(calls[0], "--allow-major") || hasArgument(calls[0], "--next-major") {
		t.Fatalf("explicit target must omit --next-major and retain acknowledgement: %v", calls)
	}
	if calls, err := os.ReadFile(terraformLog); err == nil && len(calls) > 0 {
		t.Fatalf("validated root with an exact lock must not be revalidated because of a nested root: %s", calls)
	} else if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
}

func TestCompatibilityValidatesExactTargetWhenPlanIsNoOp(t *testing.T) {
	step := findStep(t, readWorkflow(t, "provider-compatibility.yml"), "compatibility", "attempt")
	for _, lockVersion := range []string{"5.6.0", "5.5.0"} {
		t.Run("locked_"+strings.ReplaceAll(lockVersion, ".", "_"), func(t *testing.T) {
			harness := newScriptHarness(t)
			if err := os.WriteFile(filepath.Join(harness.dir, "main.tf"), []byte("terraform {}\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			terraformLog := filepath.Join(harness.dir, "terraform-calls")
			stub := "#!/bin/sh\nprintf '%s|%s\\n' \"$PWD\" \"$*\" >> \"$PROVLIFT_TERRAFORM_CALLS\"\n"
			stub += "if [ \"$1\" = init ]; then grep -F 'provlift-compat-target = { source = \"registry.terraform.io/hashicorp/aws\"' \"$PWD/provlift_compatibility_override.tf\" >/dev/null || exit 3; grep -F 'version = \"= 5.6.0\"' \"$PWD/provlift_compatibility_override.tf\" >/dev/null || exit 2; fi\n"
			stub += "if [ \"$1\" = init ]; then printf 'provider \\\"registry.terraform.io/hashicorp/aws\\\" {\\n  version = \\\"%s\\\"\\n}\\n' '" + lockVersion + "' > .terraform.lock.hcl; fi\n"
			if err := os.WriteFile(filepath.Join(harness.bin, "terraform"), []byte(stub), 0o755); err != nil {
				t.Fatal(err)
			}
			harness.environment = []string{
				"MODULES=.", "PROVIDER_SOURCE=" + testProviderSource, "PROVLIFT_TARGET_VERSION=5.6.0",
				"PROVLIFT_TERRAFORM_CALLS=" + filepath.ToSlash(terraformLog),
				"PROVLIFT_WORKFLOW_LIST_RESPONSE={\"providers\":[{\"module\":\".\",\"is_root\":true,\"source\":\"registry.terraform.io/hashicorp/aws\",\"locked\":\"" + lockVersion + "\"},{\"module\":\"examples/single\",\"is_root\":true,\"source\":\"registry.terraform.io/hashicorp/aws\",\"locked\":\"5.5.0\"}],\"diagnostics\":[]}",
				"GITHUB_STEP_SUMMARY=" + filepath.ToSlash(filepath.Join(harness.dir, "summary")),
			}
			harness.responses = `{"major":{"plan":{"actions":null,"skipped":[{"module":".","reason":"5.6.0 is not newer than the current version 5.6.0"}]},"changed_files":[],"terraform_runs":[],"pull_request":{"branch":"provlift/no-changes","title":"No changes","labels":[]}}}`
			if err := os.WriteFile(filepath.Join(harness.dir, "check.json"), []byte(`{"providers":[]}`), 0o600); err != nil {
				t.Fatal(err)
			}
			output, err := harness.run(t, step.Run, map[string]string{
				"inputs.level || 'minor'": "major", "inputs.allow_major || 'false'": "true",
				"inputs.raise_floor || 'false'": "false",
			})
			if lockVersion == "5.6.0" {
				if err != nil {
					t.Fatalf("exact-target fallback failed: %v\n%s", err, output)
				}
				calls, readErr := os.ReadFile(terraformLog)
				if readErr != nil || !strings.Contains(string(calls), "init -upgrade -input=false -no-color -backend=false") || !strings.Contains(string(calls), "validate -no-color") {
					t.Fatalf("fallback did not init and validate the exact target: %v\n%s", readErr, calls)
				}
				if _, statErr := os.Stat(filepath.Join(harness.dir, ".terraform.lock.hcl")); !os.IsNotExist(statErr) {
					t.Fatalf("fallback must restore the absent lock file, stat err=%v", statErr)
				}
				if _, statErr := os.Stat(filepath.Join(harness.dir, "provlift_compatibility_override.tf")); !os.IsNotExist(statErr) {
					t.Fatalf("temporary exact-version override leaked, stat err=%v", statErr)
				}
				summary, readErr := os.ReadFile(filepath.Join(harness.dir, "summary"))
				if readErr != nil || !strings.Contains(string(summary), "Validated registry.terraform.io/hashicorp/aws 5.6.0") {
					t.Fatalf("summary did not report exact-target validation: %v\n%s", readErr, summary)
				}
			} else if err == nil || !strings.Contains(output, "Terraform did not select registry.terraform.io/hashicorp/aws 5.6.0") {
				t.Fatalf("mismatched lock selection must fail compatibility: err=%v\n%s", err, output)
			}
		})
	}
}

func TestCompatibilityValidatesTargetOlderThanLock(t *testing.T) {
	step := findStep(t, readWorkflow(t, "provider-compatibility.yml"), "compatibility", "attempt")
	harness := newScriptHarness(t)
	if err := os.WriteFile(filepath.Join(harness.dir, "main.tf"), []byte("terraform {}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	terraformLog := filepath.Join(harness.dir, "terraform-calls")
	stub := "#!/bin/sh\nprintf '%s\\n' \"$*\" >> \"$PROVLIFT_WORKFLOW_TERRAFORM_CALLS\"\n"
	stub += "if [ \"$1\" = init ]; then grep -F 'source = \"registry.terraform.io/hashicorp/random\", version = \"= 3.6.0\"' provlift_compatibility_override.tf >/dev/null || exit 4; ! grep -F 'hashicorp/null' provlift_compatibility_override.tf >/dev/null || exit 5; fi\n"
	stub += "if [ \"$1\" = init ]; then printf 'provider \\\"registry.terraform.io/hashicorp/aws\\\" {\\n  version = \\\"5.6.0\\\"\\n}\\n' > .terraform.lock.hcl; fi\n"
	if err := os.WriteFile(filepath.Join(harness.bin, "terraform"), []byte(stub), 0o755); err != nil {
		t.Fatal(err)
	}
	harness.environment = []string{
		"MODULES=.", "PROVIDER_SOURCE=" + testProviderSource, "PROVLIFT_TARGET_VERSION=5.6.0",
		"PROVLIFT_WORKFLOW_TERRAFORM_CALLS=" + filepath.ToSlash(terraformLog),
		"GITHUB_STEP_SUMMARY=" + filepath.ToSlash(filepath.Join(harness.dir, "summary")),
		"PROVLIFT_WORKFLOW_LIST_RESPONSE={\"providers\":[{\"module\":\".\",\"is_root\":true,\"source\":\"registry.terraform.io/hashicorp/aws\",\"locked\":\"5.6.0\"},{\"module\":\".\",\"is_root\":true,\"source\":\"registry.terraform.io/hashicorp/random\",\"locked\":\"3.6.0\"},{\"module\":\".\",\"is_root\":true,\"source\":\"registry.terraform.io/hashicorp/null\"}],\"diagnostics\":[]}",
	}
	harness.responses = `{"major":{"plan":{"actions":null,"skipped":[{"module":".","reason":"5.6.0 is not newer than the current version 5.7.0"}]},"changed_files":[],"terraform_runs":[],"pull_request":{"branch":"provlift/no-changes","title":"No changes","labels":[]}}}`
	if err := os.WriteFile(filepath.Join(harness.dir, "check.json"), []byte(`{"providers":[]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	output, err := harness.run(t, step.Run, map[string]string{
		"inputs.level || 'minor'": "major", "inputs.allow_major || 'false'": "true",
		"inputs.raise_floor || 'false'": "false",
	})
	if err != nil {
		t.Fatalf("baseline below the lock must reach exact-target validation: %v\n%s", err, output)
	}
	calls, readErr := os.ReadFile(terraformLog)
	if readErr != nil || !strings.Contains(string(calls), "validate -no-color") {
		t.Fatalf("exact-target fallback did not validate: %v\n%s", readErr, calls)
	}
}

func TestCompatibilityRejectsExplicitTargetSkippedByLevel(t *testing.T) {
	step := findStep(t, readWorkflow(t, "provider-compatibility.yml"), "compatibility", "attempt")
	harness := newScriptHarness(t)
	terraformLog := filepath.Join(harness.dir, "terraform-calls")
	terraformStub := "#!/bin/sh\nprintf '%s\\n' \"$*\" >> \"$PROVLIFT_WORKFLOW_TERRAFORM_CALLS\"\n"
	if err := os.WriteFile(filepath.Join(harness.bin, "terraform"), []byte(terraformStub), 0o755); err != nil {
		t.Fatal(err)
	}
	harness.environment = []string{
		"MODULES=.", "PROVIDER_SOURCE=" + testProviderSource, "PROVLIFT_TARGET_VERSION=5.6.0",
		"PROVLIFT_WORKFLOW_TERRAFORM_CALLS=" + filepath.ToSlash(terraformLog),
	}
	harness.responses = `{"minor":{"plan":{"actions":null,"skipped":[{"module":".","reason":"5.6.0 is a major upgrade but --level is minor"}]},"changed_files":[],"terraform_runs":[],"pull_request":{"branch":"provlift/no-changes","title":"No changes","labels":[]}}}`
	output, err := harness.run(t, step.Run, map[string]string{
		"inputs.level || 'minor'": "minor", "inputs.target_version || ''": "5.6.0",
		"inputs.allow_major || 'false'": "false", "inputs.raise_floor || 'false'": "false",
	})
	if err == nil || !strings.Contains(output, "skipped by the requested level or provider policy") {
		t.Fatalf("major target skipped by a minor level must fail: err=%v\n%s", err, output)
	}
	calls := harness.invocations(t)
	if len(calls) != 1 || calls[0][0] != "upgrade" {
		t.Fatalf("policy skip should stop after planning: %v", calls)
	}
	if terraformCalls, err := os.ReadFile(terraformLog); err == nil && len(terraformCalls) > 0 {
		t.Fatalf("policy-skipped target must not invoke Terraform: %s", terraformCalls)
	} else if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
}

func TestCompatibilityExactTargetPreservesExistingOverride(t *testing.T) {
	step := findStep(t, readWorkflow(t, "provider-compatibility.yml"), "compatibility", "attempt")
	harness := newScriptHarness(t)
	if err := os.WriteFile(filepath.Join(harness.dir, "main.tf"), []byte("terraform {}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	overridePath := filepath.Join(harness.dir, "provlift_compatibility_override.tf")
	const sentinel = "pre-existing override must survive"
	if err := os.WriteFile(overridePath, []byte(sentinel), 0o600); err != nil {
		t.Fatal(err)
	}
	harness.environment = []string{
		"MODULES=.", "PROVIDER_SOURCE=" + testProviderSource, "PROVLIFT_TARGET_VERSION=5.6.0",
		"GITHUB_STEP_SUMMARY=" + filepath.ToSlash(filepath.Join(harness.dir, "summary")),
		"PROVLIFT_WORKFLOW_LIST_RESPONSE={\"providers\":[{\"module\":\".\",\"is_root\":true,\"source\":\"registry.terraform.io/hashicorp/aws\",\"locked\":\"4.57.0\"}],\"diagnostics\":[]}",
	}
	harness.responses = `{"major":{"plan":{"actions":null,"skipped":[{"module":".","reason":"5.6.0 is not newer than the current version 5.6.0"}]},"changed_files":[],"terraform_runs":[],"pull_request":{"branch":"provlift/no-changes","title":"No changes","labels":[]}}}`
	output, err := harness.run(t, step.Run, map[string]string{
		"inputs.level || 'minor'": "major", "inputs.allow_major || 'false'": "true",
		"inputs.raise_floor || 'false'": "false",
	})
	if err == nil || !strings.Contains(output, "temporary compatibility override already exists") {
		t.Fatalf("existing override must stop fallback validation: err=%v\n%s", err, output)
	}
	content, readErr := os.ReadFile(overridePath)
	if readErr != nil || string(content) != sentinel {
		t.Fatalf("existing override was changed or removed: err=%v content=%q", readErr, content)
	}
}

func TestDirectDiscoveryStages(t *testing.T) {
	step := findStep(t, readWorkflow(t, "provider-upgrades.yml"), "discover", "plan")
	for _, level := range []string{"patch", "minor", "major"} {
		t.Run(level, func(t *testing.T) {
			harness := newScriptHarness(t)
			acknowledged := "false"
			if level == "major" {
				acknowledged = "true"
			}
			output, err := harness.run(t, step.Run, map[string]string{
				"inputs.level || 'auto'": level, "inputs.allow_major || 'false'": acknowledged,
			})
			if err != nil {
				t.Fatalf("direct discovery failed: %v\n%s", err, output)
			}
			calls := harness.invocations(t)
			if len(calls) != 1 || !hasArgument(calls[0], level) || !hasArgument(calls[0], "--exact-level") {
				t.Fatalf("direct stage did not select exactly %s: %v", level, calls)
			}
			if hasArgument(calls[0], "--next-major") != (level == "major") || hasArgument(calls[0], "--allow-major") != (level == "major") {
				t.Fatalf("incorrect next-major or acknowledgement flags: %v", calls)
			}
		})
	}
}

func TestUpgradeWorkerPullRequestChangeGate(t *testing.T) {
	document := readWorkflow(t, "provider-upgrades.yml")
	step := findStep(t, document, "upgrade", "upgrade")
	for _, candidate := range document.Jobs["upgrade"].Steps {
		if strings.HasPrefix(candidate.Uses, "peter-evans/create-pull-request@") && candidate.If != "steps.upgrade.outputs.has_changes == 'true'" {
			t.Fatal("PR creation must require a nonempty executed change")
		}
	}
	for _, changed := range []bool{false, true} {
		harness := newScriptHarness(t)
		response := `{"plan":{"actions":null},"changed_files":[],"pull_request":{"branch":"provlift/no-changes","title":"No changes","labels":[]}}`
		if changed {
			response = `{"plan":{"actions":[{"source":"hashicorp/null"}]},"changed_files":[".terraform.lock.hcl"],"pull_request":{"branch":"provlift/null-1.0.1","title":"Upgrade fixture","labels":["dependencies"]}}`
		}
		harness.responses = `{"minor":` + response + `}`
		output, err := harness.run(t, step.Run, map[string]string{
			"matrix.provider": "hashicorp/null", "matrix.level": "minor", "inputs.allow_major || 'false'": "false",
		})
		if err != nil {
			t.Fatalf("worker failed: %v\n%s", err, output)
		}
		content, err := os.ReadFile(harness.output)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(strings.ReplaceAll(string(content), "\r\n", "\n"), fmt.Sprintf("has_changes=%t\n", changed)) {
			t.Fatalf("unexpected PR gate for changed=%t: %s", changed, content)
		}
	}
}

func TestCompatibilityConstraintChangeAndValidationGates(t *testing.T) {
	document := readWorkflow(t, "provider-compatibility.yml")
	step := findStep(t, document, "compatibility", "attempt")
	for _, candidate := range document.Jobs["compatibility"].Steps {
		if strings.HasPrefix(candidate.Uses, "peter-evans/create-pull-request@") && candidate.If != "steps.attempt.outputs.changed != '0'" {
			t.Fatal("compatibility PR creation must be gated by constraint changes")
		}
	}
	for _, scenario := range []struct {
		name  string
		level string
		valid bool
	}{
		{"minor ceiling widened", "minor", true},
		{"acknowledged major", "major", true},
		{"validation failure", "minor", false},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			harness := newScriptHarness(t)
			harness.environment = []string{"MODULES=.", "PROVIDER_SOURCE=" + testProviderSource, "GITHUB_STEP_SUMMARY=" + filepath.ToSlash(filepath.Join(harness.dir, "summary"))}
			harness.responses = fmt.Sprintf(`{%q:{"plan":{"actions":[{"constraint":{"changed":true}}]},"changed_files":["main.tf",".terraform.lock.hcl"],"terraform_runs":[{"ok":%t}],"pull_request":{"branch":"provlift/testing","title":"Upgrade fixture","labels":[]}}}`, scenario.level, scenario.valid)
			if err := os.WriteFile(filepath.Join(harness.dir, "pr-body.md"), []byte("Changes and validation"), 0o600); err != nil {
				t.Fatal(err)
			}
			output, err := harness.run(t, step.Run, map[string]string{
				"inputs.level || 'minor'": scenario.level, "inputs.raise_floor || 'false'": "false",
				"inputs.allow_major || 'false'": "true", "inputs.target_version || ''": "",
			})
			if scenario.valid {
				if err != nil {
					t.Fatalf("compatibility failed: %v\n%s", err, output)
				}
				content, err := os.ReadFile(harness.output)
				if err != nil || !strings.Contains(string(content), "changed=1\n") {
					t.Fatalf("constraint change omitted from gate: %v, %s", err, content)
				}
			} else if err == nil || !strings.Contains(output, "does not validate") {
				t.Fatalf("failed validation must stop the workflow: %v\n%s", err, output)
			}
			calls := harness.invocations(t)
			if len(calls) != 1 || !hasArgument(calls[0], "--allow-major") || hasArgument(calls[0], "--next-major") != (scenario.level == "major") {
				t.Fatalf("compatibility stage flags incorrect: %v", calls)
			}
		})
	}
}

// testProviderSource is the provider the compatibility tests stand in for.
// Nothing about it is special: the workflow takes the provider as an input.
const testProviderSource = "registry.terraform.io/hashicorp/aws"

// releasedVersion is the version release-please last released, without the
// leading "v".
func releasedVersion(t *testing.T) string {
	t.Helper()
	content, err := os.ReadFile(filepath.Join("..", "..", ".release-please-manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	var manifest map[string]string
	if err := json.Unmarshal(content, &manifest); err != nil {
		t.Fatal(err)
	}
	if manifest["."] == "" {
		t.Fatal("release-please manifest has no root package version")
	}
	return manifest["."]
}

var (
	inputWithFallback = regexp.MustCompile(`\$\{\{\s*inputs\.[A-Za-z_]+\s*\|\|\s*'([^']*)'\s*\}\}`)
	bareInput         = regexp.MustCompile(`\$\{\{\s*inputs\.[A-Za-z_]+\s*\}\}`)
)

// asScheduled resolves input expressions the way GitHub does for a scheduled
// run, where the inputs context is empty and every fallback applies.
func asScheduled(script string) string {
	script = inputWithFallback.ReplaceAllString(script, "$1")
	return bareInput.ReplaceAllString(script, "")
}

func TestCompatibilityResolvesProvider(t *testing.T) {
	document := readWorkflow(t, "provider-compatibility.yml")
	if document.Env["PROVIDER"] != "${{ inputs.provider || vars.PROVLIFT_PROVIDER }}" {
		t.Fatalf("provider must come from the input, then the repository variable: %q", document.Env["PROVIDER"])
	}
	steps := document.Jobs["compatibility"].Steps
	checkIndex, providerIndex, attemptIndex := -1, -1, -1
	for index, step := range steps {
		switch {
		case step.ID == "provider":
			providerIndex = index
		case step.ID == "attempt":
			attemptIndex = index
		case strings.Contains(step.Run, `> "$RUNNER_TEMP/check.json"`):
			checkIndex = index
		}
	}
	if checkIndex < 0 || providerIndex <= checkIndex || attemptIndex <= providerIndex {
		t.Fatalf("provider must be resolved after check.json is written and before the attempt: check=%d provider=%d attempt=%d",
			checkIndex, providerIndex, attemptIndex)
	}
	step := steps[providerIndex]
	if strings.Contains(step.Run, "${{") {
		t.Fatal("the free-form provider must reach the shell through the environment, not interpolation")
	}
	declared := `{"providers":[{"source":"registry.terraform.io/hashicorp/aws"},{"source":"registry.terraform.io/integrations/github"},{"source":"registry.terraform.io/hashicorp/google"}]}`
	for _, scenario := range []struct {
		name, provider, check, wantSource, wantError string
	}{
		{"unset", "", declared, "", "No provider selected"},
		{"namespace and type", "hashicorp/aws", declared, testProviderSource, ""},
		{"mixed case", "Integrations/GitHub", declared, "registry.terraform.io/integrations/github", ""},
		{"fully qualified", "registry.terraform.io/hashicorp/google", declared, "registry.terraform.io/hashicorp/google", ""},
		{"bare type is ambiguous", "aws", declared, "", "must be namespace/type"},
		{"shell or HCL metacharacters", `hashicorp/aws"; echo injected`, declared, "", "must be namespace/type"},
		{"not declared anywhere", "hashicorp/kubernetes", declared, "", "is not declared by any module"},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			harness := newScriptHarness(t)
			if err := os.WriteFile(filepath.Join(harness.dir, "check.json"), []byte(scenario.check), 0o600); err != nil {
				t.Fatal(err)
			}
			harness.environment = []string{"PROVIDER=" + scenario.provider}
			output, err := harness.run(t, step.Run, nil)
			environment, _ := os.ReadFile(filepath.Join(harness.dir, "github-env"))
			if scenario.wantError != "" {
				if err == nil || !strings.Contains(output, scenario.wantError) || len(environment) != 0 {
					t.Fatalf("want failure %q: err=%v env=%q\n%s", scenario.wantError, err, environment, output)
				}
				return
			}
			if err != nil {
				t.Fatalf("provider resolution failed: %v\n%s", err, output)
			}
			if got := strings.TrimSpace(string(environment)); got != "PROVIDER_SOURCE="+scenario.wantSource {
				t.Fatalf("exported %q, want PROVIDER_SOURCE=%s", got, scenario.wantSource)
			}
		})
	}
}

func TestInstallStepRequiresReleaseTag(t *testing.T) {
	config, err := os.ReadFile(filepath.Join("..", "..", "release-please-config.json"))
	if err != nil {
		t.Fatal(err)
	}
	var releasePlease struct {
		Packages map[string]struct {
			ExtraFiles []string `json:"extra-files"`
		} `json:"packages"`
	}
	if err := json.Unmarshal(config, &releasePlease); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"provider-upgrades.yml", "provider-compatibility.yml"} {
		// release-please moves the default to each new tag, so the example
		// never points at a release that predates the workflow it ships with.
		if !hasArgument(releasePlease.Packages["."].ExtraFiles, "examples/workflows/"+name) {
			t.Fatalf("release-please does not keep the PROVLIFT_VERSION default in %s current", name)
		}
		raw, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		if !regexp.MustCompile(`(?m)^  PROVLIFT_VERSION: .*'v[0-9.]+' \}\} # x-release-please-version$`).Match(raw) {
			t.Fatalf("%s PROVLIFT_VERSION default lacks the release-please marker", name)
		}
		for _, job := range readWorkflow(t, name).Jobs {
			for _, step := range job.Steps {
				if step.Name != "Install provlift" {
					continue
				}
				for _, scenario := range []struct {
					version string
					allowed bool
				}{
					{"v1.2.3", true}, {"v1.3.0-rc.1", true},
					{"main", false}, {"latest", false}, {"1.2.3", false}, {"v1.2", false}, {"", false},
				} {
					harness := newBashHarness(t)
					goLog := filepath.Join(harness.dir, "go-calls")
					stub := "#!/bin/sh\nprintf '%s\\n' \"$*\" >> \"$PROVLIFT_GO_CALLS\"\n"
					if err := os.WriteFile(filepath.Join(harness.bin, "go"), []byte(stub), 0o755); err != nil {
						t.Fatal(err)
					}
					harness.environment = []string{"PROVLIFT_VERSION=" + scenario.version, "PROVLIFT_GO_CALLS=" + filepath.ToSlash(goLog)}
					output, err := harness.run(t, step.Run, nil)
					calls, _ := os.ReadFile(goLog)
					if scenario.allowed {
						want := "install github.com/provlift/provlift/cmd/provlift@" + scenario.version
						if err != nil || strings.TrimSpace(string(calls)) != want {
							t.Fatalf("%s: tag %s refused or misused: err=%v calls=%q\n%s", name, scenario.version, err, calls, output)
						}
					} else if err == nil || len(calls) != 0 || !strings.Contains(output, "must be a release tag") {
						t.Fatalf("%s: %q must be refused before go install: err=%v calls=%q\n%s", name, scenario.version, err, calls, output)
					}
				}
			}
		}
	}
}

func TestUpgradeWorkerScopesToItsMatrixProvider(t *testing.T) {
	document := readWorkflow(t, "provider-upgrades.yml")
	job := document.Jobs["upgrade"]
	if job.Strategy.Matrix != "${{ fromJSON(needs.discover.outputs.matrix) }}" {
		t.Fatalf("worker matrix must come from discovery: %q", job.Strategy.Matrix)
	}
	if job.Strategy.FailFast == nil || *job.Strategy.FailFast {
		t.Fatal("one provider failing must not cancel the other providers' pull requests")
	}
	step := findStep(t, document, "upgrade", "upgrade")
	const provider = "registry.terraform.io/integrations/github"
	harness := newScriptHarness(t)
	output, err := harness.run(t, step.Run, map[string]string{
		"matrix.provider": provider, "matrix.level": "minor", "inputs.allow_major || 'false'": "false",
	})
	if err != nil {
		t.Fatalf("worker failed: %v\n%s", err, output)
	}
	calls := harness.invocations(t)
	if len(calls) != 1 {
		t.Fatalf("expected one upgrade per worker, got %v", calls)
	}
	var selected []string
	for index, argument := range calls[0] {
		if argument == "--provider" && index+1 < len(calls[0]) {
			selected = append(selected, calls[0][index+1])
		}
	}
	if len(selected) != 1 || selected[0] != provider {
		t.Fatalf("worker must upgrade exactly its own provider, selected %v", selected)
	}
}

func TestPullRequestStepsUsePlannerMetadata(t *testing.T) {
	for _, scenario := range []struct{ file, job, step string }{
		{"provider-upgrades.yml", "upgrade", "upgrade"},
		{"provider-compatibility.yml", "compatibility", "attempt"},
	} {
		document := readWorkflow(t, scenario.file)
		run := findStep(t, document, scenario.job, scenario.step).Run
		if !strings.Contains(run, `--pr-body "$RUNNER_TEMP/pr-body.md"`) {
			t.Fatalf("%s must have provlift write the pull request description", scenario.file)
		}
		for _, field := range []string{"branch", "title", "labels"} {
			if !strings.Contains(run, `.pull_request.`+field) {
				t.Fatalf("%s does not take the %s from the planner", scenario.file, field)
			}
		}
		var pullRequests []workflowStep
		for _, candidate := range document.Jobs[scenario.job].Steps {
			if strings.HasPrefix(candidate.Uses, "peter-evans/create-pull-request@") {
				pullRequests = append(pullRequests, candidate)
			}
		}
		if len(pullRequests) != 1 {
			t.Fatalf("%s must raise exactly one pull request per job, found %d", scenario.file, len(pullRequests))
		}
		with := pullRequests[0].With
		outputs := "${{ steps." + scenario.step + ".outputs."
		if with["branch"] != outputs+"branch }}" || with["title"] != outputs+"title }}" || with["labels"] != outputs+"labels }}" ||
			with["body-path"] != "${{ runner.temp }}/pr-body.md" {
			t.Fatalf("%s pull request is not built from the planner's branch, title, labels and body: %v", scenario.file, with)
		}
	}
}

func TestScheduledRunsNeverAuthorizeMajor(t *testing.T) {
	upgrades := readWorkflow(t, "provider-upgrades.yml")
	majorOnly := `{"major":{"plan":{"actions":[{"source":"registry.terraform.io/hashicorp/google"}]}}}`

	t.Run("discovery", func(t *testing.T) {
		harness := newScriptHarness(t)
		harness.responses = majorOnly
		script := asScheduled(findStep(t, upgrades, "discover", "plan").Run)
		output, err := harness.run(t, script, nil)
		if err != nil {
			t.Fatalf("scheduled discovery failed: %v\n%s", err, output)
		}
		for _, call := range harness.invocations(t) {
			if hasArgument(call, "major") || hasArgument(call, "--allow-major") || hasArgument(call, "--next-major") {
				t.Fatalf("scheduled discovery considered a major: %v", call)
			}
		}
		content, err := os.ReadFile(harness.output)
		if err != nil || !strings.Contains(string(content), "found=false") {
			t.Fatalf("a major-only upgrade must not reach a worker on schedule: %v %s", err, content)
		}
	})

	t.Run("worker", func(t *testing.T) {
		harness := newScriptHarness(t)
		script := asScheduled(findStep(t, upgrades, "upgrade", "upgrade").Run)
		output, err := harness.run(t, script, map[string]string{"matrix.provider": "hashicorp/google", "matrix.level": "major"})
		if err == nil || len(harness.invocations(t)) != 0 {
			t.Fatalf("a scheduled worker handed a major must refuse it: err=%v\n%s", err, output)
		}
	})

	t.Run("compatibility", func(t *testing.T) {
		harness := newScriptHarness(t)
		harness.environment = []string{"MODULES=.", "PROVIDER_SOURCE=" + testProviderSource,
			"GITHUB_STEP_SUMMARY=" + filepath.ToSlash(filepath.Join(harness.dir, "summary"))}
		harness.responses = `{"minor":{"plan":{"actions":[]},"changed_files":[],"terraform_runs":[],"pull_request":{"branch":"provlift/no-changes","title":"No changes","labels":[]}}}`
		if err := os.WriteFile(filepath.Join(harness.dir, "check.json"), []byte(`{"providers":[]}`), 0o600); err != nil {
			t.Fatal(err)
		}
		script := asScheduled(findStep(t, readWorkflow(t, "provider-compatibility.yml"), "compatibility", "attempt").Run)
		output, err := harness.run(t, script, nil)
		if err != nil {
			t.Fatalf("scheduled compatibility failed: %v\n%s", err, output)
		}
		calls := harness.invocations(t)
		if len(calls) != 1 || !hasArgument(calls[0], "minor") || hasArgument(calls[0], "--allow-major") ||
			hasArgument(calls[0], "--next-major") || hasArgument(calls[0], "--raise-floor") {
			t.Fatalf("scheduled compatibility must test minor without acknowledgement or a floor raise: %v", calls)
		}
	})
}
