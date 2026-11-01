package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	version "github.com/hashicorp/go-version"
	"github.com/hashicorp/hcl/v2/hclwrite"
	"github.com/spf13/cobra"
	"github.com/zclconf/go-cty/cty"

	"github.com/provlift/provlift/internal/lockfile"
	"github.com/provlift/provlift/internal/model"
	"github.com/provlift/provlift/internal/report"
	"github.com/provlift/provlift/internal/terraform"
	"github.com/provlift/provlift/internal/upgrade"
)

// runRecord is one Terraform invocation, flattened for reporting.
type runRecord struct {
	Module   string `json:"module"`
	Command  string `json:"command"`
	ExitCode int    `json:"exit_code"`
	OK       bool   `json:"ok"`
	Detail   string `json:"detail,omitempty"`
}

func newRunner(ws *workspace, g *globalOptions) (*terraform.Runner, error) {
	return terraform.New(ws.cfg.Terraform.Binary, g.timeout)
}

// applyTerraform regenerates the lock file and validates the result.
//
// Ordering matters: fmt tidies the constraint edits, init -upgrade re-resolves
// providers and rewrites the lock file, providers lock then widens the recorded
// hashes to every configured platform, and validate confirms the configuration
// still loads against the new provider schemas.
func applyTerraform(ctx context.Context, g *globalOptions, ws *workspace, plan *upgrade.Plan, opts *upgradeOptions, changedFiles []string) ([]runRecord, error) {
	runner, err := newRunner(ws, g)
	if err != nil {
		return nil, err
	}
	runner.Output = g.stderr()

	platforms := opts.platforms
	if len(platforms) == 0 {
		platforms = ws.cfg.Platforms
	}

	roots := ws.inv.AffectedRoots(plan.ModuleDirs())
	allowed := map[string]map[string]string{}
	for _, action := range plan.Actions {
		if action.Level == upgrade.LevelNone {
			continue
		}
		for _, root := range ws.inv.AffectedRoots([]string{action.ModuleDir}) {
			if allowed[root.Dir] == nil {
				allowed[root.Dir] = map[string]string{}
			}
			source := action.Address.String()
			target := action.To
			for _, candidate := range []string{root.Locks[source].Version, allowed[root.Dir][source]} {
				if candidate == "" {
					continue
				}
				planned, plannedErr := version.NewVersion(target)
				locked, lockedErr := version.NewVersion(candidate)
				if plannedErr != nil || lockedErr != nil {
					return nil, fmt.Errorf("invalid provider version for %s in %s: planned %q, locked %q", source, root.RelDir, target, candidate)
				}
				if locked.GreaterThan(planned) {
					target = candidate
				}
			}
			allowed[root.Dir][source] = target
		}
	}

	var runs []runRecord
	var failures []string

	// Provider downloads can take minutes, so each step is announced before it
	// starts rather than only appearing in the summary afterwards.
	announced := false
	activeModule, activeStep := "", ""
	var started time.Time
	defer func() {
		if activeStep != "" {
			fmt.Fprintf(g.stderr(), "  [%s] %s: failed (%s)\n", activeModule, activeStep, time.Since(started).Round(time.Millisecond))
		}
	}()
	announce := func(mod, label string) {
		if !announced {
			fmt.Fprintln(g.stderr(), "\nRunning Terraform (provider downloads may take several minutes)")
			announced = true
		}
		activeModule, activeStep = mod, label
		started = time.Now()
		fmt.Fprintf(g.stderr(), "  [%s] terraform %s ...\n", mod, label)
	}

	record := func(mod string, res terraform.Result, label string, okCodes ...int) bool {
		ok := false
		for _, c := range append([]int{0}, okCodes...) {
			if res.ExitCode == c {
				ok = true
				break
			}
		}
		detail := ""
		if !ok {
			detail = firstLines(res.Stderr+res.Stdout, 12)
			failures = append(failures, fmt.Sprintf("%s: %s failed (exit %d)", mod, label, res.ExitCode))
		}
		status := "complete"
		if !ok {
			status = fmt.Sprintf("failed (exit %d)", res.ExitCode)
		}
		fmt.Fprintf(g.stderr(), "  [%s] %s: %s (%s)\n", mod, label, status, res.Duration.Round(time.Millisecond))
		activeStep = ""
		runs = append(runs, runRecord{Module: mod, Command: label, ExitCode: res.ExitCode, OK: ok, Detail: detail})
		return ok
	}

	if opts.runFmt {
		// Only the files that were actually edited are formatted. A recursive
		// fmt of their directories would also rewrite pre-existing drift in
		// sibling files and nested modules, pulling unrelated changes into
		// the diff.
		for _, dir := range editedDirs(changedFiles) {
			announce(relTo(ws.root, dir), "fmt")
			res, err := runner.Fmt(ctx, dir, editedFileNames(changedFiles, dir)...)
			if err != nil {
				return runs, err
			}
			record(relTo(ws.root, dir), res, "fmt")
		}
	}

	for _, root := range roots {
		lockPath := filepath.Join(root.Dir, lockfile.Name)
		original, readErr := os.ReadFile(lockPath)
		if readErr != nil && !os.IsNotExist(readErr) {
			return runs, fmt.Errorf("reading original lock for %s: %w", root.RelDir, readErr)
		}
		announce(root.RelDir, "init -upgrade")
		res, err := initWithPlannedProviders(ctx, runner, root, allowed[root.Dir], opts.backend)
		if err != nil {
			return runs, restoreLockAfterFailure(root, original, readErr == nil, err)
		}
		if !record(root.RelDir, res, "init -upgrade") {
			// Without a successful init there is no provider schema, so the
			// later steps would fail for a misleading reason.
			if err := restoreOriginalLock(root, original, readErr == nil); err != nil {
				return runs, fmt.Errorf("restoring lock after failed init in %s: %w", root.RelDir, err)
			}
			continue
		}
		if err := verifyPlannedLock(root, allowed[root.Dir], original, readErr == nil); err != nil {
			return runs, err
		}

		if len(platforms) > 0 {
			announce(root.RelDir, "providers lock")
			res, err := runner.ProvidersLock(ctx, root.Dir, platforms)
			if err != nil {
				return runs, restoreLockAfterFailure(root, original, readErr == nil, err)
			}
			if !record(root.RelDir, res, "providers lock") {
				if err := restoreOriginalLock(root, original, readErr == nil); err != nil {
					return runs, fmt.Errorf("restoring lock after failed providers lock in %s: %w", root.RelDir, err)
				}
				continue
			}
			if err := verifyPlannedLock(root, allowed[root.Dir], original, readErr == nil); err != nil {
				return runs, err
			}
		}

		if opts.skipValidate {
			continue
		}
		announce(root.RelDir, "validate")
		res, diags, err := runner.Validate(ctx, root.Dir)
		if err != nil {
			return runs, err
		}
		if !record(root.RelDir, res, "validate") && len(diags) > 0 {
			runs[len(runs)-1].Detail = formatDiagnostics(diags)
		}
	}

	if len(failures) > 0 {
		return runs, fmt.Errorf("%w:\n  %s", errTerraformReported, strings.Join(failures, "\n  "))
	}
	return runs, nil
}

// errTerraformReported marks failures Terraform itself reported, which exit
// with ExitValidationFailed. Any other error from applyTerraform means
// provlift could not run Terraform or refused its result, and exits with
// ExitError.
var errTerraformReported = errors.New("terraform reported problems")

func initWithPlannedProviders(ctx context.Context, runner *terraform.Runner, root *model.Module, targets map[string]string, backend bool) (result terraform.Result, err error) {
	const overrideName = "provlift_override.tf"
	path := filepath.Join(root.Dir, overrideName)
	constraints := map[string]string{}
	for source, lock := range root.Locks {
		constraints[source] = lock.Version
	}
	for source, target := range targets {
		constraints[source] = target
	}
	if len(constraints) == 0 {
		return runner.Init(ctx, root.Dir, terraform.InitOptions{Upgrade: true, Backend: backend})
	}

	file := hclwrite.NewEmptyFile()
	required := file.Body().AppendNewBlock("terraform", nil).Body().AppendNewBlock("required_providers", nil).Body()
	sources := make([]string, 0, len(constraints))
	for source := range constraints {
		sources = append(sources, source)
	}
	sort.Strings(sources)
	used := map[string]bool{}
	for _, requirement := range root.Requirements {
		used[requirement.LocalName] = true
	}
	for _, source := range sources {
		local := ""
		for _, requirement := range root.Requirements {
			if requirement.Address.String() == source {
				local = requirement.LocalName
				break
			}
		}
		if local == "" {
			for index := 0; ; index++ {
				local = fmt.Sprintf("provlift%d", index)
				if !used[local] {
					break
				}
			}
		}
		used[local] = true
		required.SetAttributeValue(local, cty.ObjectVal(map[string]cty.Value{
			"source":  cty.StringVal(source),
			"version": cty.StringVal("= " + constraints[source]),
		}))
	}

	created, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return result, fmt.Errorf("creating temporary Terraform override %s: %w", path, err)
	}
	defer func() {
		if removeErr := os.Remove(path); removeErr != nil {
			err = errors.Join(err, fmt.Errorf("removing temporary Terraform override %s: %w", path, removeErr))
		}
	}()
	if _, err = created.Write(file.Bytes()); err != nil {
		created.Close()
		return result, err
	}
	if err = created.Close(); err != nil {
		return result, err
	}
	return runner.Init(ctx, root.Dir, terraform.InitOptions{Upgrade: true, Backend: backend})
}

func verifyPlannedLock(root *model.Module, allowed map[string]string, original []byte, hadLock bool) error {
	current, _, err := lockfile.Read(root.Dir)
	if err == nil {
		for source, lock := range current {
			previous, existed := root.Locks[source]
			if target, selected := allowed[source]; selected && lock.Version != target {
				err = fmt.Errorf("unplanned provider version in %s: %s selected %s instead of planned %s", root.RelDir, source, lock.Version, target)
				break
			} else if !selected && (!existed || previous.Version != lock.Version) {
				err = fmt.Errorf("unplanned provider change in %s: %s moved from %q to %q", root.RelDir, source, previous.Version, lock.Version)
				break
			}
		}
	}
	if err == nil {
		for source, previous := range root.Locks {
			if _, exists := current[source]; !exists {
				err = fmt.Errorf("unplanned provider removal in %s: %s at %s", root.RelDir, source, previous.Version)
				break
			}
		}
		for source, target := range allowed {
			if _, exists := current[source]; !exists {
				err = fmt.Errorf("planned provider %s at %s missing from %s lock", source, target, root.RelDir)
				break
			}
		}
	}
	if err == nil {
		return nil
	}
	return restoreLockAfterFailure(root, original, hadLock, err)
}

func restoreLockAfterFailure(root *model.Module, original []byte, hadLock bool, failure error) error {
	if err := restoreOriginalLock(root, original, hadLock); err != nil {
		return fmt.Errorf("%w; restoring original lock failed: %v", failure, err)
	}
	return fmt.Errorf("%w; original lock restored", failure)
}

func restoreOriginalLock(root *model.Module, original []byte, hadLock bool) error {
	path := filepath.Join(root.Dir, lockfile.Name)
	if hadLock {
		return os.WriteFile(path, original, 0o644)
	}
	err := os.Remove(path)
	if os.IsNotExist(err) {
		return nil
	}
	return err
}

// editedFileNames returns the base names of the given files that live in dir,
// so fmt can be pointed at exactly the files that were edited.
func editedFileNames(files []string, dir string) []string {
	var out []string
	for _, f := range files {
		if filepath.Dir(f) == dir {
			out = append(out, filepath.Base(f))
		}
	}
	return out
}

// editedDirs returns the distinct directories containing the given files.
func editedDirs(files []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, f := range files {
		dir := filepath.Dir(f)
		if !seen[dir] {
			seen[dir] = true
			out = append(out, dir)
		}
	}
	return out
}

func newValidateCommand(g *globalOptions) *cobra.Command {
	var (
		modules  []string
		withPlan bool
		backend  bool
		noInit   bool
	)

	cmd := &cobra.Command{
		Use:   "validate",
		Short: "Validate the Terraform configuration in every root module",
		Long: `Runs terraform fmt -check and terraform validate in each root module.

terraform plan is not run by default. A plan needs backend access, state and
live provider credentials, and it reaches out to real infrastructure, so it
belongs in CI where those are available rather than on a developer machine
during an upgrade. Use --plan to opt in.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx := cmd.Context()

			ws, err := g.open()
			if err != nil {
				return exitWith(ExitError, err)
			}
			runner, err := newRunner(ws, g)
			if err != nil {
				return exitWith(ExitError, err)
			}

			var targets []*model.Module
			for _, m := range ws.inv.RootModules() {
				if moduleMatches(m.RelDir, modules) && len(m.Requirements) > 0 {
					targets = append(targets, m)
				}
			}
			runs := []runRecord{}
			failures := []string{}
			if len(targets) == 0 {
				if g.jsonOut {
					if err := writeJSON(g, map[string]any{"runs": runs, "failures": failures}); err != nil {
						return exitWith(ExitError, err)
					}
					return nil
				}
				fmt.Fprintln(g.stdout(), "No root modules to validate.")
				return nil
			}

			for _, mod := range targets {
				res, err := runner.FmtCheck(ctx, mod.Dir)
				if err != nil {
					return exitWith(ExitError, err)
				}
				ok := res.ExitCode == 0
				detail := ""
				if !ok {
					detail = firstLines(res.Stdout+res.Stderr, 12)
					failures = append(failures, mod.RelDir+": formatting drift")
				}
				runs = append(runs, runRecord{Module: mod.RelDir, Command: "fmt -check", ExitCode: res.ExitCode, OK: ok, Detail: detail})

				if !noInit {
					res, err := runner.Init(ctx, mod.Dir, terraform.InitOptions{Backend: backend})
					if err != nil {
						return exitWith(ExitError, err)
					}
					ok := res.ExitCode == 0
					detail := ""
					if !ok {
						detail = firstLines(res.Stderr+res.Stdout, 12)
						failures = append(failures, mod.RelDir+": init failed")
					}
					runs = append(runs, runRecord{Module: mod.RelDir, Command: "init", ExitCode: res.ExitCode, OK: ok, Detail: detail})
					if !ok {
						continue
					}
				}

				vres, diags, err := runner.Validate(ctx, mod.Dir)
				if err != nil {
					return exitWith(ExitError, err)
				}
				ok = vres.ExitCode == 0
				detail = ""
				if !ok {
					detail = formatDiagnostics(diags)
					if detail == "" {
						detail = firstLines(vres.Stderr+vres.Stdout, 12)
					}
					failures = append(failures, mod.RelDir+": validate failed")
				}
				runs = append(runs, runRecord{Module: mod.RelDir, Command: "validate", ExitCode: vres.ExitCode, OK: ok, Detail: detail})

				if withPlan && ok {
					pres, err := runner.Plan(ctx, mod.Dir)
					if err != nil {
						return exitWith(ExitError, err)
					}
					// -detailed-exitcode returns 2 for "changes pending",
					// which is a successful plan.
					planOK := pres.ExitCode == 0 || pres.ExitCode == 2
					detail := ""
					if !planOK {
						detail = firstLines(pres.Stderr+pres.Stdout, 12)
						failures = append(failures, mod.RelDir+": plan failed")
					}
					runs = append(runs, runRecord{Module: mod.RelDir, Command: "plan", ExitCode: pres.ExitCode, OK: planOK, Detail: detail})
				}
			}

			if g.jsonOut {
				if err := writeJSON(g, map[string]any{"runs": runs, "failures": failures}); err != nil {
					return exitWith(ExitError, err)
				}
			} else {
				printRuns(g, runs)
			}

			if len(failures) > 0 {
				return exitWith(ExitValidationFailed,
					fmt.Errorf("validation failed:\n  %s", strings.Join(failures, "\n  ")))
			}
			return nil
		},
	}

	f := cmd.Flags()
	f.StringSliceVar(&modules, "module", nil, "only validate these module paths (repeatable, glob)")
	f.BoolVar(&withPlan, "plan", false, "also run terraform plan (needs credentials and backend access)")
	f.BoolVar(&backend, "backend", false, "initialise the backend during validation")
	f.BoolVar(&noInit, "no-init", false, "skip init; assumes .terraform is already populated")
	return cmd
}

func printRuns(g *globalOptions, runs []runRecord) {
	if len(runs) == 0 {
		return
	}
	report.Section(g.stdout(), "Terraform")

	rows := make([][]string, 0, len(runs))
	for _, r := range runs {
		status := "ok"
		if !r.OK {
			status = "FAILED"
		}
		rows = append(rows, []string{r.Module, r.Command, fmt.Sprint(r.ExitCode), status})
	}
	_ = report.Table(g.stdout(), []string{"MODULE", "COMMAND", "EXIT", "STATUS"}, rows)

	for _, r := range runs {
		if r.OK || r.Detail == "" {
			continue
		}
		fmt.Fprintf(g.stderr(), "\n%s %s:\n%s\n", r.Module, r.Command, indent(r.Detail, "  "))
	}
}

func formatDiagnostics(diags []terraform.ValidationDiagnostic) string {
	var b strings.Builder
	for _, d := range diags {
		location := ""
		if d.Range != nil {
			location = fmt.Sprintf("%s:%d: ", d.Range.Filename, d.Range.Start.Line)
		}
		fmt.Fprintf(&b, "[%s] %s%s\n", d.Severity, location, d.Summary)
		if d.Detail != "" {
			fmt.Fprintf(&b, "  %s\n", d.Detail)
		}
	}
	return strings.TrimRight(b.String(), "\n")
}

func firstLines(s string, n int) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	lines := strings.Split(s, "\n")
	if len(lines) > n {
		lines = append(lines[:n], fmt.Sprintf("... (%d more lines)", len(lines)-n))
	}
	return strings.Join(lines, "\n")
}

func indent(s, prefix string) string {
	lines := strings.Split(s, "\n")
	for i, l := range lines {
		lines[i] = prefix + l
	}
	return strings.Join(lines, "\n")
}

func relTo(root, path string) string {
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return path
	}
	return filepath.ToSlash(rel)
}
