// Package terraform runs the Terraform CLI.
//
// Terraform generates lock selections and checksums; callers may restore the
// original lock bytes unchanged after a failed refresh.
package terraform

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"
)

// ErrNotFound means no Terraform binary could be located.
var ErrNotFound = errors.New("terraform binary not found")

// Runner executes Terraform commands in a module directory.
type Runner struct {
	// Binary is the executable to run. It may be "tofu" to drive OpenTofu,
	// which implements the same commands this tool relies on.
	Binary string

	// Timeout bounds a single command. Zero means no timeout.
	Timeout time.Duration

	// ExtraEnv is appended to the inherited environment.
	ExtraEnv []string

	Output io.Writer
}

type synchronizedOutput struct {
	mu     sync.Mutex
	writer io.Writer
}

func (output *synchronizedOutput) Write(content []byte) (int, error) {
	output.mu.Lock()
	defer output.mu.Unlock()
	return output.writer.Write(content)
}

// Result captures everything about one command execution.
type Result struct {
	Args     []string      `json:"args"`
	Dir      string        `json:"dir"`
	ExitCode int           `json:"exit_code"`
	Stdout   string        `json:"-"`
	Stderr   string        `json:"stderr,omitempty"`
	Duration time.Duration `json:"duration"`
}

// Succeeded reports whether the command exited cleanly.
func (r Result) Succeeded() bool { return r.ExitCode == 0 }

// New builds a Runner, resolving the binary from binary, then the
// PROVLIFT_TERRAFORM_BIN environment variable, then PATH.
func New(binary string, timeout time.Duration) (*Runner, error) {
	candidate := binary
	if candidate == "" {
		candidate = os.Getenv("PROVLIFT_TERRAFORM_BIN")
	}
	if candidate == "" {
		candidate = "terraform"
	}

	resolved, err := exec.LookPath(candidate)
	if err != nil {
		return nil, fmt.Errorf("%w: %q is not on PATH", ErrNotFound, candidate)
	}
	return &Runner{Binary: resolved, Timeout: timeout}, nil
}

func (r *Runner) run(ctx context.Context, dir string, args ...string) (Result, error) {
	if r.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, r.Timeout)
		defer cancel()
	}

	cmd := exec.CommandContext(ctx, r.Binary, args...)
	cmd.Dir = dir

	// TF_IN_AUTOMATION trims interactive hints from the output and TF_INPUT=0
	// guarantees Terraform never blocks waiting on a prompt.
	cmd.Env = append(os.Environ(), "TF_IN_AUTOMATION=1", "TF_INPUT=0")
	cmd.Env = append(cmd.Env, r.ExtraEnv...)

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if r.Output != nil {
		output := &synchronizedOutput{writer: r.Output}
		cmd.Stderr = io.MultiWriter(&stderr, output)
		machineReadable := false
		for _, argument := range args {
			if argument == "-json" {
				machineReadable = true
				break
			}
		}
		if !machineReadable {
			cmd.Stdout = io.MultiWriter(&stdout, output)
		}
	}

	start := time.Now()
	err := cmd.Run()

	res := Result{
		Args:     args,
		Dir:      dir,
		Stdout:   stdout.String(),
		Stderr:   stderr.String(),
		Duration: time.Since(start),
	}

	var exitErr *exec.ExitError
	switch {
	case err == nil:
		res.ExitCode = 0
	case errors.As(err, &exitErr):
		res.ExitCode = exitErr.ExitCode()
	default:
		return res, fmt.Errorf("running terraform %s in %s: %w", strings.Join(args, " "), dir, err)
	}

	if ctx.Err() != nil {
		return res, fmt.Errorf("terraform %s in %s: %w", strings.Join(args, " "), dir, ctx.Err())
	}
	return res, nil
}

// InitOptions controls `terraform init`.
type InitOptions struct {
	// Upgrade sets -upgrade, which is what re-resolves providers and rewrites
	// the lock file.
	Upgrade bool

	// Backend enables backend initialisation. It is off by default so provider
	// upgrades do not require state access or cloud credentials.
	Backend bool
}

// Init runs `terraform init`.
func (r *Runner) Init(ctx context.Context, dir string, opts InitOptions) (Result, error) {
	args := []string{"init", "-input=false", "-no-color"}
	if opts.Upgrade {
		args = append(args, "-upgrade")
	}
	args = append(args, fmt.Sprintf("-backend=%t", opts.Backend))
	return r.run(ctx, dir, args...)
}

// ProvidersLock runs `terraform providers lock`, recording checksums for every
// requested platform. Without this, a lock file written on one OS omits hashes
// needed by CI or colleagues on another.
func (r *Runner) ProvidersLock(ctx context.Context, dir string, platforms []string) (Result, error) {
	args := []string{"providers", "lock"}
	for _, p := range platforms {
		args = append(args, "-platform="+p)
	}
	return r.run(ctx, dir, args...)
}

// FmtCheck reports formatting drift without modifying files.
func (r *Runner) FmtCheck(ctx context.Context, dir string) (Result, error) {
	return r.run(ctx, dir, "fmt", "-check", "-recursive", "-no-color")
}

// Fmt rewrites the named files into canonical form, which tidies constraints
// this tool inserted. The names are passed straight to terraform fmt: without
// -recursive or a directory-wide target, pre-existing drift in sibling files
// and nested modules stays out of the diff.
func (r *Runner) Fmt(ctx context.Context, dir string, names ...string) (Result, error) {
	return r.run(ctx, dir, append([]string{"fmt", "-no-color"}, names...)...)
}

// ValidationDiagnostic is one entry from `terraform validate -json`.
type ValidationDiagnostic struct {
	Severity string `json:"severity"`
	Summary  string `json:"summary"`
	Detail   string `json:"detail"`
	Range    *struct {
		Filename string `json:"filename"`
		Start    struct {
			Line int `json:"line"`
		} `json:"start"`
	} `json:"range"`
}

// Validate runs `terraform validate -json` and decodes its diagnostics.
func (r *Runner) Validate(ctx context.Context, dir string) (Result, []ValidationDiagnostic, error) {
	res, err := r.run(ctx, dir, "validate", "-no-color", "-json")
	if err != nil {
		return res, nil, err
	}

	var payload struct {
		Valid       bool                   `json:"valid"`
		Diagnostics []ValidationDiagnostic `json:"diagnostics"`
	}
	// A non-JSON body means Terraform failed before validation started; the
	// captured stderr is the useful signal in that case.
	if jsonErr := json.Unmarshal([]byte(res.Stdout), &payload); jsonErr != nil {
		return res, nil, nil
	}
	return res, payload.Diagnostics, nil
}

// Plan runs `terraform plan -detailed-exitcode`. Exit code 2 means the plan
// succeeded and changes are pending, which is not a failure.
func (r *Runner) Plan(ctx context.Context, dir string) (Result, error) {
	return r.run(ctx, dir, "plan", "-input=false", "-no-color", "-lock=false", "-detailed-exitcode")
}

// Version reports the Terraform version in use.
func (r *Runner) Version(ctx context.Context, dir string) (string, error) {
	res, err := r.run(ctx, dir, "version", "-json")
	if err != nil {
		return "", err
	}

	var payload struct {
		Version string `json:"terraform_version"`
	}
	if err := json.Unmarshal([]byte(res.Stdout), &payload); err != nil {
		return strings.TrimSpace(res.Stdout), nil
	}
	return payload.Version, nil
}
