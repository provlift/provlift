package cli

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	version "github.com/hashicorp/go-version"
	"github.com/spf13/cobra"

	"github.com/provlift/provlift/internal/config"
	"github.com/provlift/provlift/internal/hcledit"
	"github.com/provlift/provlift/internal/lockfile"
	"github.com/provlift/provlift/internal/model"
	"github.com/provlift/provlift/internal/pullrequest"
	"github.com/provlift/provlift/internal/registry"
	"github.com/provlift/provlift/internal/report"
	"github.com/provlift/provlift/internal/terraform"
	"github.com/provlift/provlift/internal/upgrade"
)

type upgradeOptions struct {
	selectors        []string
	modules          []string
	level            string
	exactLevel       bool
	to               string
	allowMajor       bool
	nextMajor        bool
	pinUnconstrained bool
	raiseFloor       bool
	dryRun           bool
	skipInit         bool
	skipValidate     bool
	backend          bool
	platforms        []string
	fsMirror         string
	netMirror        string
	runFmt           bool
	prBody           string

	// lockMirror is the mirror providers lock uses, resolved from the flags
	// or, failing those, the configuration.
	lockMirror terraform.LockMirror
}

func newUpgradeCommand(g *globalOptions) *cobra.Command {
	opts := &upgradeOptions{}

	cmd := &cobra.Command{
		Use:   "upgrade",
		Short: "Upgrade Terraform provider versions",
		Long: `Plans and applies provider upgrades across a repository.

Version constraints are only rewritten when they would otherwise block the
target version, and the rewrite preserves the operator style already in use, so
"~> 6.0" becomes "~> 7.0" rather than being reshaped.

Only Terraform generates dependency lock content; provlift may restore the original
bytes after a failed refresh. After constraints change, Terraform regenerates it:

  terraform init -upgrade -backend=false
  terraform providers lock -platform=...

Locking for several platforms keeps the resulting lock file usable on every
machine in the team and in CI, not just the one that ran the upgrade.

Major upgrades may contain breaking changes and therefore require the explicit
--allow-major flag.`,
		Example: `  # See what a patch upgrade would do, without touching anything
  provlift upgrade --level patch --dry-run

	# Apply the next published minor version of the AzureRM provider
	provlift upgrade --provider azurerm --level minor --exact-level

  # Take a deliberate major upgrade of the GitHub provider
  provlift upgrade --provider integrations/github --level major --allow-major

  # Pin an exact version
  provlift upgrade --provider mongodb/mongodbatlas --to 1.25.0 --level minor`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runUpgrade(cmd, g, opts)
		},
	}

	f := cmd.Flags()
	f.StringSliceVar(&opts.selectors, "provider", nil, "only upgrade these providers (repeatable)")
	f.StringSliceVar(&opts.modules, "module", nil, "only upgrade these module paths (repeatable, glob)")
	f.StringVar(&opts.level, "level", "patch", "highest upgrade level to apply: patch, minor or major")
	f.BoolVar(&opts.exactLevel, "exact-level", false, "select the next published release classified at exactly --level")
	f.StringVar(&opts.to, "to", "", "upgrade to this exact version (requires a single --provider)")
	f.BoolVar(&opts.allowMajor, "allow-major", false, "acknowledge that major upgrades may contain breaking changes")
	f.BoolVar(&opts.nextMajor, "next-major", false, "select the first published release in the next major series")
	f.BoolVar(&opts.pinUnconstrained, "pin-unconstrained", false, "add a constraint to providers that declare none")
	f.BoolVar(&opts.raiseFloor, "raise-floor", false, "raise the constraint's supported minimum, for shared module repositories")
	f.BoolVar(&opts.dryRun, "dry-run", false, "show the plan without changing any files")
	f.BoolVar(&opts.skipInit, "skip-init", false, "do not run Terraform; leaves the lock file stale")
	f.BoolVar(&opts.skipValidate, "skip-validate", false, "do not run terraform validate after upgrading")
	f.BoolVar(&opts.backend, "backend", false, "initialise the backend (needs state access and credentials)")
	f.StringSliceVar(&opts.platforms, "platforms", nil, "platforms to record lock hashes for (default: from config)")
	f.StringVar(&opts.fsMirror, "fs-mirror", "", "fetch provider packages for the lock file from this filesystem mirror (default: from config)")
	f.StringVar(&opts.netMirror, "net-mirror", "", "fetch provider packages for the lock file from this HTTPS network mirror (default: from config)")
	f.BoolVar(&opts.runFmt, "fmt", true, "run terraform fmt on modified directories")
	f.StringVar(&opts.prBody, "pr-body", "", "write a pull request description to this file")
	return cmd
}

func upgradeResult(dryRun bool) map[string]any {
	emptyPlan := &upgrade.Plan{}
	return map[string]any{
		"dry_run":        dryRun,
		"plan":           emptyPlan,
		"changed_files":  []string{},
		"terraform_runs": []any{},
		"module_versions": pullrequest.ModuleVersions{
			Changes: []pullrequest.ModuleChange{}, NoBaseline: []string{},
		},
		"pull_request": pullrequest.Build(emptyPlan, nil, nil),
	}
}

func runUpgrade(cmd *cobra.Command, g *globalOptions, opts *upgradeOptions) (outcome error) {
	result := upgradeResult(opts.dryRun)
	defer func() {
		if g.jsonOut {
			if err := writeJSON(g, result); err != nil {
				outcome = exitWith(ExitError, err)
			}
		}
	}()
	ctx := cmd.Context()
	// Refuse unusable mirror flags before recovery can rewrite any file, so a
	// refused invocation leaves the repository exactly as it found it.
	if err := checkMirrorFlags(opts); err != nil {
		return exitWith(ExitError, err)
	}
	if !opts.dryRun {
		root, err := filepath.Abs(g.dir)
		if err != nil {
			return exitWith(ExitError, err)
		}
		cfg, err := config.Load(root)
		if err != nil {
			return exitWith(ExitError, err)
		}
		excludes := append(append([]string{}, cfg.Exclude...), g.excludes...)
		if err := hcledit.Recover(root, excludes); err != nil {
			return exitWith(ExitError, fmt.Errorf("recovering interrupted writes: %w", err))
		}
	}

	ws, err := g.open()
	if err != nil {
		return exitWith(ExitError, err)
	}
	// Requirements in unreadable or .tf.json files are never upgraded, so say
	// so up front. Diagnostics go to stderr and leave --json output intact.
	printDiagnostics(g, ws)

	requested, err := upgrade.ParseLevel(opts.level)
	if err != nil {
		return exitWith(ExitError, err)
	}

	var explicit *version.Version
	if opts.to != "" {
		if len(opts.selectors) != 1 {
			return exitWith(ExitError, fmt.Errorf("--to requires exactly one --provider"))
		}
		if explicit, err = version.NewVersion(opts.to); err != nil {
			return exitWith(ExitError, fmt.Errorf("--to %q is not a valid version: %w", opts.to, err))
		}
	}

	if opts.nextMajor {
		if requested != upgrade.LevelMajor {
			return exitWith(ExitError,
				fmt.Errorf("--next-major requires --level major (got %q)", opts.level))
		}
		if explicit != nil {
			return exitWith(ExitError, fmt.Errorf("--next-major cannot be combined with --to"))
		}
	}
	if opts.modules, err = prepareModuleFilters(ws.inv, opts.modules, g.stderr()); err != nil {
		return exitWith(ExitError, err)
	}

	if opts.lockMirror, err = resolveLockMirror(ws, opts); err != nil {
		return exitWith(ExitError, err)
	}

	platforms := opts.platforms
	if len(platforms) == 0 {
		platforms = ws.cfg.Platforms
	}
	vs := fetchVersions(ctx, ws.reg, eligibleProviders(ws, opts.selectors, opts.modules), platforms)
	statuses := collectStatuses(ws, vs, opts.selectors, opts.modules, g.includePrerelease)

	plan, err := buildPlan(ws, statuses, vs, requested, explicit, opts.pinUnconstrained, opts.raiseFloor, opts.nextMajor, opts.exactLevel, g.includePrerelease)
	if err != nil {
		return exitWith(ExitError, err)
	}

	result["plan"] = plan
	result["pull_request"] = pullrequest.Build(plan, nil, nil)
	if len(plan.Actions) == 0 {
		// Emit the same shape as every other outcome. A caller that parses
		// this output should not have to branch on whether there was
		// anything to do.
		if err := writePullRequestBody(opts.prBody, pullrequest.Build(plan, nil, nil)); err != nil {
			return exitWith(ExitError, err)
		}
		if g.jsonOut {
			return nil
		}
		fmt.Fprintln(g.stdout(), "Nothing to upgrade.")
		printSkipped(g, plan)
		return nil
	}

	if !opts.allowMajor {
		if blocked := majorActions(plan); len(blocked) > 0 {
			return exitWith(ExitError, fmt.Errorf(
				"refusing to apply %d major upgrade(s) without --allow-major:\n%s\n\nMajor upgrades may contain breaking changes. Review the provider's upgrade guide before re-running with --allow-major.",
				len(blocked), strings.Join(blocked, "\n")))
		}
	}

	if !g.jsonOut {
		printPlan(g, plan, opts.raiseFloor)
	}

	if opts.dryRun {
		if err := writePullRequestBody(opts.prBody, pullrequest.Build(plan, nil, nil)); err != nil {
			return exitWith(ExitError, err)
		}
		if g.jsonOut {
			return nil
		}
		fmt.Fprintln(g.stdout(), "\nDry run: no files were modified.")
		return nil
	}

	// Resolve the binary before touching any file: discovering it is missing
	// after the rewrite would leave constraints edited and locks stale.
	if !opts.skipInit {
		if _, err := newRunner(ws, g); err != nil {
			return exitWith(ExitError, err)
		}
	}

	edits := buildEdits(plan)
	changedFiles, err := applyEdits(edits)
	if err != nil {
		return exitWith(ExitError, err)
	}

	if opts.skipInit {
		reportedFiles := relativeFiles(ws.root, changedFiles)
		result["changed_files"] = reportedFiles
		result["pull_request"] = pullrequest.Build(plan, nil, reportedFiles)
		if err := writePullRequestBody(opts.prBody, pullrequest.Build(plan, nil, reportedFiles)); err != nil {
			return exitWith(ExitError, err)
		}
		if !g.jsonOut {
			printChangedFiles(g, changedFiles)
		}
		fmt.Fprintln(g.stderr(),
			"\nWarning: --skip-init was set, so .terraform.lock.hcl is now out of step with the constraints. Run `terraform init -upgrade` before committing.")
		return nil
	}

	lockSnapshots, err := snapshotLockFiles(ws.inv.AffectedRoots(plan.ModuleDirs()))
	if err != nil {
		reportedFiles := relativeFiles(ws.root, changedFiles)
		result["changed_files"] = reportedFiles
		result["pull_request"] = pullrequest.Build(plan, nil, reportedFiles)
		return exitWith(ExitError, err)
	}
	modules := pullrequest.ModuleVersions{Changes: []pullrequest.ModuleChange{}, NoBaseline: []string{}}
	runs, runErr := applyTerraform(ctx, g, ws, plan, opts, changedFiles, &modules)
	result["terraform_runs"] = runs
	result["module_versions"] = modules
	runCode := ExitValidationFailed
	if runErr != nil && !errors.Is(runErr, errTerraformReported) {
		runCode = ExitError
	}
	lockChanges, lockErr := changedLockFiles(lockSnapshots)
	if lockErr != nil {
		runErr = errors.Join(runErr, lockErr)
		runCode = ExitError
	}
	changedFiles = append(changedFiles, lockChanges...)
	reportedFiles := relativeFiles(ws.root, changedFiles)
	result["changed_files"] = reportedFiles

	pr := pullrequest.BuildWithModules(plan, toPullRequestRuns(runs), reportedFiles, modules)
	result["pull_request"] = pr
	if err := writePullRequestBody(opts.prBody, pr); err != nil {
		return exitWith(ExitError, err)
	}

	if !g.jsonOut {
		printChangedFiles(g, changedFiles)
		printRuns(g, runs)
		printModuleVersions(g, modules)
	}

	if runErr != nil {
		return exitWith(runCode, runErr)
	}
	if !g.jsonOut {
		fmt.Fprintln(g.stdout(), "\nUpgrade complete. Review the diff, including .terraform.lock.hcl, before committing.")
	}
	return nil
}

// checkMirrorFlags refuses mirror flags providers lock cannot use. It needs no
// workspace, so it runs before recovery, which may rewrite files.
func checkMirrorFlags(opts *upgradeOptions) error {
	if opts.fsMirror != "" && opts.netMirror != "" {
		return fmt.Errorf("--fs-mirror cannot be combined with --net-mirror; providers lock accepts a single mirror")
	}
	if opts.netMirror != "" {
		if err := config.CheckNetMirror(opts.netMirror); err != nil {
			return fmt.Errorf("--net-mirror: %w", err)
		}
	}
	return nil
}

// resolveLockMirror picks the mirror for providers lock. A mirror flag replaces
// whatever the configuration names, as --platforms does. Filesystem paths are
// made absolute because providers lock runs inside each root module: a flag is
// read from the working directory, a configured path from the repository root.
// The flags themselves were already vetted by checkMirrorFlags.
func resolveLockMirror(ws *workspace, opts *upgradeOptions) (terraform.LockMirror, error) {
	switch {
	case opts.fsMirror != "":
		path, err := filepath.Abs(opts.fsMirror)
		return terraform.LockMirror{FS: path}, err
	case opts.netMirror != "":
		return terraform.LockMirror{Net: opts.netMirror}, nil
	}

	mirror := terraform.LockMirror{FS: ws.cfg.Terraform.FSMirror, Net: ws.cfg.Terraform.NetMirror}
	if mirror.FS != "" && !filepath.IsAbs(mirror.FS) {
		mirror.FS = filepath.Join(ws.root, filepath.FromSlash(mirror.FS))
	}
	return mirror, nil
}

func buildPlan(ws *workspace, statuses []*upgrade.Status, vs versionSet, requested upgrade.Level, explicit *version.Version, pin, raiseFloor, nextMajor, exactLevel, includePrerelease bool) (*upgrade.Plan, error) {
	opts := upgrade.PlanOptions{
		Level:             requested,
		ExactLevel:        exactLevel,
		IncludePrerelease: includePrerelease,
		Explicit:          explicit,
		PinUnconstrained:  pin,
		RaiseFloor:        raiseFloor,
		NextMajorOnly:     nextMajor,
	}

	// Shared modules are planned before the roots that call them, so that a
	// ceiling this run raises is judged as it will stand afterwards rather than
	// reported as blocking the very upgrade that lifts it.
	planned := map[*upgrade.Status]plannedStatus{}
	rewrites := map[string]string{}
	for _, st := range statuses {
		mod := findModule(ws, st.Module)
		if mod == nil || mod.IsRoot {
			continue
		}
		result, err := planStatus(ws, mod, st, vs, opts)
		if err != nil {
			return nil, err
		}
		planned[st] = result
		if result.action != nil && result.action.Constraint.Changed {
			rewrites[localConstraintKey(mod.Dir, st.Address)] = result.action.Constraint.New
		}
	}
	var deps map[string]rootDeps
	if len(rewrites) > 0 {
		deps = withLocalRewrites(loadDependencyConstraints(ws), rewrites)
	}

	plan := &upgrade.Plan{}
	for _, st := range statuses {
		mod := findModule(ws, st.Module)
		if mod == nil {
			continue
		}
		result, done := planned[st]
		if !done {
			if deps != nil {
				st = buildStatus(ws, deps, vs, mod, st.Requirement, includePrerelease)
			}
			var err error
			if result, err = planStatus(ws, mod, st, vs, opts); err != nil {
				return nil, err
			}
		}
		if result.action != nil {
			plan.Actions = append(plan.Actions, *result.action)
		} else {
			plan.Skipped = append(plan.Skipped, result.skip)
		}
	}
	return plan, nil
}

// plannedStatus is the outcome of planning one requirement: an action, or the
// reason there is none.
type plannedStatus struct {
	action *upgrade.Action
	skip   upgrade.Skip
}

func planStatus(ws *workspace, mod *model.Module, st *upgrade.Status, vs versionSet, opts upgrade.PlanOptions) (plannedStatus, error) {
	skip := func(reason string) plannedStatus {
		return plannedStatus{skip: upgrade.Skip{
			Module: st.Module, LocalName: st.LocalName, Source: st.Source, Address: st.Address,
			Reason: reason,
		}}
	}

	explicit := opts.Explicit
	if capped, ok := ws.cfg.MaxLevelFor(st.Address); ok {
		if capped == "none" {
			return skip("pinned by .provlift.yaml (max_level: none)"), nil
		}
		configured, err := upgrade.ParseLevel(capped)
		if err != nil {
			return plannedStatus{}, err
		}
		if configured < opts.Level {
			opts.Level = configured
		}
	}
	if explicit != nil && !containsVersion(vs.Get(st.Address), explicit) && containsPublishedVersion(vs.Published(st.Address), explicit) {
		return plannedStatus{}, fmt.Errorf("%s is published for %s but does not support the requested platforms", explicit, st.Source)
	}
	if st.Error != "" {
		return plannedStatus{}, fmt.Errorf("registry lookup failed for %s in %s: %s", st.Source, st.Module, st.Error)
	}
	if explicit != nil && !containsVersion(vs.Get(st.Address), explicit) {
		return plannedStatus{}, fmt.Errorf("%s is not a published version of %s", explicit, st.Source)
	}

	action, reason, err := upgrade.PlanAction(mod, st, vs.Get(st.Address), opts)
	if err != nil {
		return plannedStatus{}, fmt.Errorf("planning %s in %s: %w; update the constraint by hand", st.Source, st.Module, err)
	}
	if action == nil {
		// A child module that needs no constraint edit has nothing of its
		// own to refresh: the roots that call it own the lock files, and
		// they are planned from their own requirements.
		return skip(reason), nil
	}
	return plannedStatus{action: action}, nil
}

func containsVersion(candidates []*version.Version, target *version.Version) bool {
	for _, candidate := range candidates {
		if candidate.Equal(target) {
			return true
		}
	}
	return false
}

func containsPublishedVersion(candidates []registry.Version, target *version.Version) bool {
	for _, candidate := range candidates {
		if candidate.Version.Equal(target) {
			return true
		}
	}
	return false
}

func findModule(ws *workspace, relDir string) *model.Module {
	for _, m := range ws.inv.Modules {
		if m.RelDir == relDir {
			return m
		}
	}
	return nil
}

func majorActions(plan *upgrade.Plan) []string {
	var out []string
	for _, a := range plan.Actions {
		if a.Breaking {
			out = append(out, fmt.Sprintf("  %s: %s %s -> %s", a.Module, a.Source, a.From, a.To))
		}
	}
	return out
}

// toPullRequestRuns narrows the Terraform results to what a description needs.
func toPullRequestRuns(runs []runRecord) []pullrequest.TerraformRun {
	out := make([]pullrequest.TerraformRun, 0, len(runs))
	for _, r := range runs {
		out = append(out, pullrequest.TerraformRun{Module: r.Module, Command: r.Command, OK: r.OK})
	}
	return out
}

// writePullRequestBody saves the description where a CI job can pick it up.
func writePullRequestBody(path string, pr pullrequest.Request) error {
	if path == "" {
		return nil
	}
	if err := os.WriteFile(path, []byte(pr.Body), 0o644); err != nil {
		return fmt.Errorf("writing pull request body to %s: %w", path, err)
	}
	return nil
}

func buildEdits(plan *upgrade.Plan) []hcledit.Edit {
	var edits []hcledit.Edit
	for _, a := range plan.Actions {
		if !a.Constraint.Changed {
			continue
		}
		edits = append(edits, hcledit.Edit{Requirement: a.Requirement, New: a.Constraint.New})
	}
	return edits
}

func applyEdits(edits []hcledit.Edit) ([]string, error) {
	if len(edits) == 0 {
		return nil, nil
	}
	results, err := hcledit.Apply(edits)
	if err != nil {
		return nil, err
	}
	var changed []string
	for _, r := range results {
		if r.Changed {
			changed = append(changed, r.File)
		}
	}
	return changed, nil
}

func relativeFiles(root string, files []string) []string {
	out := make([]string, 0, len(files))
	for _, file := range files {
		rel, err := filepath.Rel(root, file)
		if err != nil {
			rel = file
		}
		out = append(out, filepath.ToSlash(rel))
	}
	return out
}

type lockFileSnapshot struct {
	content []byte
	exists  bool
}

func snapshotLockFiles(roots []*model.Module) (map[string]lockFileSnapshot, error) {
	snapshots := make(map[string]lockFileSnapshot, len(roots))
	for _, root := range roots {
		path := filepath.Join(root.Dir, lockfile.Name)
		content, err := os.ReadFile(path)
		if err != nil && !os.IsNotExist(err) {
			return nil, fmt.Errorf("reading lock file before Terraform in %s: %w", root.RelDir, err)
		}
		snapshots[path] = lockFileSnapshot{content: content, exists: err == nil}
	}
	return snapshots, nil
}

func changedLockFiles(snapshots map[string]lockFileSnapshot) ([]string, error) {
	var changed []string
	for path, before := range snapshots {
		after, err := os.ReadFile(path)
		if err != nil && !os.IsNotExist(err) {
			return changed, fmt.Errorf("reading lock file after Terraform at %s: %w", path, err)
		}
		exists := err == nil
		if exists != before.exists || !bytes.Equal(after, before.content) {
			changed = append(changed, path)
		}
	}
	sort.Strings(changed)
	return changed, nil
}

func printPlan(g *globalOptions, plan *upgrade.Plan, raiseFloor bool) {
	report.Section(g.stdout(), "Planned upgrades")

	rows := make([][]string, 0, len(plan.Actions))
	for _, a := range plan.Actions {
		constraint := "unchanged"
		if a.Constraint.Changed {
			constraint = fmt.Sprintf("%s -> %s", a.Constraint.Old, a.Constraint.New)
		}
		flag := ""
		if a.Breaking {
			flag = "BREAKING"
		}
		level := a.Level.String()
		if a.ConstraintOnly {
			level = "floor"
			flag = "constraint only"
		}
		rows = append(rows, []string{a.Module, model.DisplaySource(a.Address), a.From, a.To, level, constraint, flag})
	}
	_ = report.Table(g.stdout(),
		[]string{"MODULE", "SOURCE", "FROM", "TO", "LEVEL", "CONSTRAINT", "NOTE"}, rows)

	if raiseFloor {
		// --raise-floor asked for the constraint to move, so a constraint left
		// unchanged needs its reason shown rather than just "unchanged".
		for _, a := range plan.Actions {
			if !a.Constraint.Changed && a.Constraint.Reason != "" {
				fmt.Fprintf(g.stdout(), "\n%s in %s: %s\n", model.DisplaySource(a.Address), a.Module, a.Constraint.Reason)
			}
		}
	}

	if plan.HasBreaking() {
		fmt.Fprintln(g.stdout(), "\nSome upgrades cross a breaking boundary. Check the provider's upgrade guide:")
		seen := map[string]bool{}
		for _, a := range plan.Actions {
			if !a.Breaking || seen[a.Source] {
				continue
			}
			seen[a.Source] = true
			fmt.Fprintf(g.stdout(), "  %s\n", upgradeGuideURL(a))
		}
	}
	printSkipped(g, plan)
}

// upgradeGuideURL points at the provider's registry documentation. Providers do
// not expose upgrade guides through a common API, so the CLI links to the
// canonical docs page rather than guessing at a guide path.
func upgradeGuideURL(a upgrade.Action) string {
	return fmt.Sprintf("%s (%s -> %s): https://%s/providers/%s/%s/%s/docs",
		a.Source, a.From, a.To,
		a.Address.Hostname.String(), a.Address.Namespace, a.Address.Type, a.To)
}

func printSkipped(g *globalOptions, plan *upgrade.Plan) {
	if len(plan.Skipped) == 0 {
		return
	}
	report.Section(g.stdout(), "Skipped")
	rows := make([][]string, 0, len(plan.Skipped))
	for _, s := range plan.Skipped {
		rows = append(rows, []string{s.Module, model.DisplaySource(s.Address), s.Reason})
	}
	_ = report.Table(g.stdout(), []string{"MODULE", "SOURCE", "REASON"}, rows)
}

func printChangedFiles(g *globalOptions, files []string) {
	if len(files) == 0 {
		fmt.Fprintln(g.stdout(), "\nNo constraint changes were needed; only the lock file moves.")
		return
	}
	report.Section(g.stdout(), "Modified files")
	for _, f := range files {
		fmt.Fprintf(g.stdout(), "  %s\n", f)
	}
}
