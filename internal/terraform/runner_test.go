package terraform

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"
)

// The test binary impersonates the terraform executable when this variable is
// set. That lets the runner be exercised end to end, including exact argument
// construction and exit-code handling, with no real Terraform installed.
const (
	fakeMarker  = "PROVLIFT_FAKE_TERRAFORM"
	fakeModeVar = "PROVLIFT_FAKE_MODE"
)

func TestMain(m *testing.M) {
	if os.Getenv(fakeMarker) != "" {
		runFakeTerraform()
		return
	}
	os.Exit(m.Run())
}

func runFakeTerraform() {
	args := strings.Join(os.Args[1:], " ")

	switch os.Getenv(fakeModeVar) {
	case "stream-until-cancel":
		fmt.Println("Installing provider plugins...")
		<-time.After(10 * time.Second)
		os.Exit(0)

	case "both-streams":
		fmt.Fprintln(os.Stdout, "Initializing provider plugins...")
		fmt.Fprintln(os.Stderr, "Provider download warning")
		os.Exit(0)

	case "validate-json":
		fmt.Print(`{"valid":false,"diagnostics":[{"severity":"error","summary":"Unsupported argument","detail":"An argument named \"foo\" is not expected here.","range":{"filename":"main.tf","start":{"line":12}}}]}`)
		os.Exit(1)

	case "validate-ok":
		fmt.Print(`{"valid":true,"diagnostics":[]}`)
		os.Exit(0)

	case "validate-garbage":
		// Terraform failed before validation started, so stdout is not JSON.
		fmt.Fprint(os.Stderr, "Error: could not load plugin schemas")
		os.Exit(1)

	case "version-json":
		fmt.Print(`{"terraform_version":"1.10.5","platform":"windows_amd64"}`)
		os.Exit(0)

	case "exit-2":
		fmt.Print(args)
		os.Exit(2)

	case "fail":
		fmt.Fprint(os.Stderr, "boom")
		os.Exit(1)

	case "sleep":
		time.Sleep(10 * time.Second)
		os.Exit(0)

	case "echo-env":
		fmt.Printf("TF_IN_AUTOMATION=%s TF_INPUT=%s", os.Getenv("TF_IN_AUTOMATION"), os.Getenv("TF_INPUT"))
		os.Exit(0)

	default:
		fmt.Print(args)
		os.Exit(0)
	}
}

func fakeRunner(t *testing.T, mode string) *Runner {
	t.Helper()
	return &Runner{
		Binary:   os.Args[0],
		Timeout:  30 * time.Second,
		ExtraEnv: []string{fakeMarker + "=1", fakeModeVar + "=" + mode},
	}
}

func TestInitBuildsExpectedArguments(t *testing.T) {
	tests := []struct {
		name string
		opts InitOptions
		want []string
	}{
		{
			name: "plain init keeps the backend disabled",
			opts: InitOptions{},
			want: []string{"init", "-input=false", "-no-color", "-backend=false"},
		},
		{
			name: "upgrade is what moves the lock file",
			opts: InitOptions{Upgrade: true},
			want: []string{"init", "-input=false", "-no-color", "-upgrade", "-backend=false"},
		},
		{
			name: "backend can be enabled explicitly",
			opts: InitOptions{Upgrade: true, Backend: true},
			want: []string{"init", "-input=false", "-no-color", "-upgrade", "-backend=true"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			res, err := fakeRunner(t, "echo-args").Init(context.Background(), t.TempDir(), tc.opts)
			if err != nil {
				t.Fatalf("Init: %v", err)
			}
			if !res.Succeeded() {
				t.Fatalf("exit code %d", res.ExitCode)
			}

			got := res.Stdout
			for _, want := range tc.want {
				if !strings.Contains(got, want) {
					t.Errorf("arguments %q missing %q", got, want)
				}
			}
		})
	}
}

func TestProvidersLockRequestsEveryPlatform(t *testing.T) {
	platforms := []string{"windows_amd64", "linux_amd64", "darwin_arm64"}

	res, err := fakeRunner(t, "echo-args").ProvidersLock(context.Background(), t.TempDir(), platforms)
	if err != nil {
		t.Fatalf("ProvidersLock: %v", err)
	}

	if !strings.Contains(res.Stdout, "providers lock") {
		t.Errorf("unexpected command: %q", res.Stdout)
	}
	if strings.Contains(res.Stdout, "-no-color") {
		t.Errorf("providers lock must not receive unsupported -no-color: %q", res.Stdout)
	}
	// Without every platform the lock file is unusable on other machines.
	for _, p := range platforms {
		if !strings.Contains(res.Stdout, "-platform="+p) {
			t.Errorf("arguments %q missing -platform=%s", res.Stdout, p)
		}
	}
}

func TestFmtCommands(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()

	check, err := fakeRunner(t, "echo-args").FmtCheck(ctx, dir)
	if err != nil {
		t.Fatalf("FmtCheck: %v", err)
	}
	if !strings.Contains(check.Stdout, "-check") || !strings.Contains(check.Stdout, "-recursive") {
		t.Errorf("fmt -check arguments = %q", check.Stdout)
	}

	write, err := fakeRunner(t, "echo-args").Fmt(ctx, dir)
	if err != nil {
		t.Fatalf("Fmt: %v", err)
	}
	if strings.Contains(write.Stdout, "-check") {
		t.Errorf("fmt must not be called with -check when rewriting: %q", write.Stdout)
	}

	// A file name that starts with "-" must reach terraform as a path, not
	// as a flag.
	named, err := fakeRunner(t, "echo-args").Fmt(ctx, dir, "main.tf", "-diff.tf")
	if err != nil {
		t.Fatalf("Fmt with names: %v", err)
	}
	if !strings.Contains(named.Stdout, "./main.tf") || !strings.Contains(named.Stdout, "./-diff.tf") {
		t.Errorf("fmt file arguments must be ./-prefixed: %q", named.Stdout)
	}
}

func TestValidateParsesDiagnostics(t *testing.T) {
	res, diags, err := fakeRunner(t, "validate-json").Validate(context.Background(), t.TempDir())
	if err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if res.Succeeded() {
		t.Error("expected a non-zero exit code")
	}
	if len(diags) != 1 {
		t.Fatalf("got %d diagnostics, want 1", len(diags))
	}

	d := diags[0]
	if d.Severity != "error" || d.Summary != "Unsupported argument" {
		t.Errorf("unexpected diagnostic: %+v", d)
	}
	if d.Range == nil || d.Range.Filename != "main.tf" || d.Range.Start.Line != 12 {
		t.Errorf("diagnostic range not decoded: %+v", d.Range)
	}
}

func TestValidateSuccess(t *testing.T) {
	res, diags, err := fakeRunner(t, "validate-ok").Validate(context.Background(), t.TempDir())
	if err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if !res.Succeeded() {
		t.Errorf("exit code = %d, want 0", res.ExitCode)
	}
	if len(diags) != 0 {
		t.Errorf("diagnostics = %v, want none", diags)
	}
}

func TestValidateToleratesNonJSONOutput(t *testing.T) {
	// Terraform can fail before it produces JSON; stderr is the useful signal
	// and the runner must not treat that as a hard error.
	res, diags, err := fakeRunner(t, "validate-garbage").Validate(context.Background(), t.TempDir())
	if err != nil {
		t.Fatalf("Validate returned an error instead of a result: %v", err)
	}
	if diags != nil {
		t.Errorf("diagnostics = %v, want nil", diags)
	}
	if !strings.Contains(res.Stderr, "could not load plugin schemas") {
		t.Errorf("stderr was not captured: %q", res.Stderr)
	}
}

func TestPlanDetailedExitCodeTwoIsNotAFailure(t *testing.T) {
	res, err := fakeRunner(t, "exit-2").Plan(context.Background(), t.TempDir())
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	// 2 means "succeeded, changes pending"; callers must be able to see it.
	if res.ExitCode != 2 {
		t.Errorf("ExitCode = %d, want 2", res.ExitCode)
	}
	if !strings.Contains(res.Stdout, "-detailed-exitcode") {
		t.Errorf("plan arguments = %q", res.Stdout)
	}
}

func TestVersionParsesJSON(t *testing.T) {
	got, err := fakeRunner(t, "version-json").Version(context.Background(), t.TempDir())
	if err != nil {
		t.Fatalf("Version: %v", err)
	}
	if got != "1.10.5" {
		t.Errorf("Version = %q, want 1.10.5", got)
	}
}

func TestNonZeroExitIsReportedNotErrored(t *testing.T) {
	res, err := fakeRunner(t, "fail").Init(context.Background(), t.TempDir(), InitOptions{})
	if err != nil {
		t.Fatalf("a failing command must not return a Go error: %v", err)
	}
	if res.ExitCode != 1 {
		t.Errorf("ExitCode = %d, want 1", res.ExitCode)
	}
	if !strings.Contains(res.Stderr, "boom") {
		t.Errorf("Stderr = %q", res.Stderr)
	}
}

func TestAutomationEnvironmentIsSet(t *testing.T) {
	// TF_INPUT=0 guarantees Terraform never blocks on a prompt.
	res, err := fakeRunner(t, "echo-env").Init(context.Background(), t.TempDir(), InitOptions{})
	if err != nil {
		t.Fatalf("Init: %v", err)
	}
	if !strings.Contains(res.Stdout, "TF_IN_AUTOMATION=1") {
		t.Errorf("TF_IN_AUTOMATION not set: %q", res.Stdout)
	}
	if !strings.Contains(res.Stdout, "TF_INPUT=0") {
		t.Errorf("TF_INPUT not set: %q", res.Stdout)
	}
}

func TestTimeoutIsEnforced(t *testing.T) {
	runner := fakeRunner(t, "sleep")
	runner.Timeout = 200 * time.Millisecond

	_, err := runner.Init(context.Background(), t.TempDir(), InitOptions{})
	if err == nil {
		t.Fatal("expected a timeout error")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("err = %v, want context.DeadlineExceeded", err)
	}
}

func TestNewRejectsMissingBinary(t *testing.T) {
	_, err := New("definitely-not-a-real-binary-xyz", time.Minute)
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("err = %v, want ErrNotFound", err)
	}
}

func TestNewHonoursEnvironmentOverride(t *testing.T) {
	t.Setenv("PROVLIFT_TERRAFORM_BIN", os.Args[0])

	runner, err := New("", time.Minute)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if runner.Binary == "" {
		t.Error("Binary was not resolved")
	}
}

type cancelOnOutput struct {
	cancel context.CancelFunc
}

func (output cancelOnOutput) Write(content []byte) (int, error) {
	output.cancel()
	return len(content), nil
}

func TestRunnerStreamsBeforeProcessExits(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	runner := fakeRunner(t, "stream-until-cancel")
	runner.Output = cancelOnOutput{cancel: cancel}
	result, err := runner.Init(ctx, t.TempDir(), InitOptions{})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("output must arrive while the child is still running, got %v", err)
	}
	if !strings.Contains(result.Stdout, "Installing provider plugins") {
		t.Fatalf("live output was not also captured: %q", result.Stdout)
	}
}

func TestRunnerStreamsAndCapturesBothOutputs(t *testing.T) {
	runner := fakeRunner(t, "both-streams")
	var output bytes.Buffer
	runner.Output = &output
	result, err := runner.Init(context.Background(), t.TempDir(), InitOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), "Initializing provider plugins") || !strings.Contains(output.String(), "Provider download warning") {
		t.Fatalf("Terraform output was not streamed: %s", output.String())
	}
	if !strings.Contains(result.Stdout, "Initializing provider plugins") || !strings.Contains(result.Stderr, "Provider download warning") {
		t.Fatalf("streaming lost captured output: %+v", result)
	}
}

func TestRunnerDoesNotStreamMachineReadableStdout(t *testing.T) {
	runner := fakeRunner(t, "validate-json")
	var output bytes.Buffer
	runner.Output = &output
	_, diagnostics, err := runner.Validate(context.Background(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if output.Len() != 0 || len(diagnostics) != 1 {
		t.Fatalf("validation JSON must remain captured and parsed, output=%s diagnostics=%v", output.String(), diagnostics)
	}
}
