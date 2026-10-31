package upgrade

import (
	"fmt"
	"strings"

	version "github.com/hashicorp/go-version"

	"github.com/provlift/provlift/internal/model"
)

// CurrentSource records where the "current version" came from, because the
// distinction matters: a locked version is what Terraform is actually using,
// while a resolved version is only what it would pick on the next init.
type CurrentSource string

const (
	// SourceLock means the version came from .terraform.lock.hcl.
	SourceLock CurrentSource = "lock"
	// SourceFloor means there is no lock entry, so the lowest version the
	// constraint admits was used. That floor is what a shared module actually
	// promises its consumers, and it is the thing such a module can raise.
	SourceFloor CurrentSource = "constraint-floor"
	// SourceUnknown means neither a lock entry nor a usable constraint exists.
	SourceUnknown CurrentSource = "unknown"
)

// Status is the reconciled view of one provider in one module.
type Status struct {
	Module    string        `json:"module"`
	LocalName string        `json:"local_name"`
	Source    string        `json:"source"`
	Address   model.Address `json:"-"`

	Constraint    string          `json:"constraint,omitempty"`
	ConstraintPos model.SourcePos `json:"position"`

	Current       string        `json:"current,omitempty"`
	CurrentSource CurrentSource `json:"current_source"`

	Latest        string `json:"latest,omitempty"`
	LatestPatch   string `json:"latest_patch,omitempty"`
	LatestMinor   string `json:"latest_minor,omitempty"`
	LatestAllowed string `json:"latest_within_constraint,omitempty"`

	// Available is the best upgrade level reachable ignoring the constraint.
	Available Level `json:"available_upgrade"`
	// Blocked is true when a newer version exists but the constraint forbids it.
	Blocked bool `json:"blocked_by_constraint"`

	// ExternalConstraint holds the terms imposed by dependencies rather than by
	// this repository, recovered from the merged constraints in the lock file.
	ExternalConstraint string `json:"external_constraint,omitempty"`

	// LatestReachable is the newest version a dependency's constraint permits,
	// which may be well below Latest.
	LatestReachable string `json:"latest_reachable,omitempty"`

	// ExternalSources names the modules that impose ExternalConstraint, so the
	// thing that has to be changed to lift it is identifiable.
	ExternalSources []string `json:"external_sources,omitempty"`

	// BlockedByDependency is true when a dependency's constraint, not this
	// repository's, is what forbids the newest version.
	BlockedByDependency bool `json:"blocked_by_dependency"`

	Error string `json:"error,omitempty"`

	// Requirement is the declaration this status was built from, retained so a
	// planned action can be turned into a byte-precise file edit.
	Requirement model.ProviderRequirement `json:"-"`

	current *version.Version
}

// CurrentVersion exposes the parsed current version for callers that need it.
func (s *Status) CurrentVersion() *version.Version { return s.current }

// SetExternalConstraint records a constraint imposed from outside this module
// and recomputes what it implies.
//
// A child module owns no lock file, so its ceiling has to be inherited from
// the root modules that consume it.
func (s *Status) SetExternalConstraint(constraint string, candidates []*version.Version) {
	s.ExternalConstraint = constraint
	s.ExternalSources = nil
	s.BlockedByDependency = false
	s.LatestReachable = ""

	if constraint == "" || len(candidates) == 0 {
		return
	}

	if v := HighestSatisfying(candidates, constraint); v != nil {
		s.LatestReachable = v.String()
	}
	if s.Latest != "" {
		if latest, err := version.NewVersion(s.Latest); err == nil {
			ok, err := Satisfies(constraint, latest)
			s.BlockedByDependency = err == nil && !ok
		}
	}
}

// BuildStatus reconciles one requirement against the versions a registry
// advertises.
func BuildStatus(mod *model.Module, req model.ProviderRequirement, available []*version.Version, includePrerelease bool) *Status {
	st := &Status{
		Module:        mod.RelDir,
		LocalName:     req.LocalName,
		Source:        req.Source,
		Address:       req.Address,
		Constraint:    req.Constraint,
		ConstraintPos: req.ConstraintPos,
		CurrentSource: SourceUnknown,
		Requirement:   req,
	}

	candidates := filterPrerelease(available, includePrerelease)

	if lock, ok := mod.Lock(req.Address); ok {
		if v, err := version.NewVersion(lock.Version); err == nil {
			st.current = v
			st.Current = v.String()
			st.CurrentSource = SourceLock
		}
		st.ExternalConstraint = ExternalConstraint(lock.Constraints, req.Constraint)
	}
	if st.current == nil {
		// With no lock file the module has selected nothing. What it does
		// declare is a supported floor, and comparing that against the newest
		// release is the question worth answering.
		if strings.TrimSpace(req.Constraint) != "" {
			if v := LowestSatisfying(candidates, req.Constraint); v != nil {
				st.current = v
				st.Current = v.String()
				st.CurrentSource = SourceFloor
			}
		}
	}

	if len(candidates) == 0 {
		return st
	}

	latest := candidates[len(candidates)-1]
	st.Latest = latest.String()

	if v := HighestSatisfying(candidates, req.Constraint); v != nil {
		st.LatestAllowed = v.String()
	}

	if st.current == nil {
		return st
	}

	if v := Highest(candidates, st.current, LevelPatch); v != nil {
		st.LatestPatch = v.String()
	}
	if v := Highest(candidates, st.current, LevelMinor); v != nil {
		st.LatestMinor = v.String()
	}

	st.Available = Classify(st.current, latest)
	if st.Available != LevelNone {
		allowed, err := Satisfies(req.Constraint, latest)
		st.Blocked = err == nil && !allowed

		if st.ExternalConstraint != "" {
			ok, err := Satisfies(st.ExternalConstraint, latest)
			st.BlockedByDependency = err == nil && !ok
			if v := HighestSatisfying(candidates, st.ExternalConstraint); v != nil {
				st.LatestReachable = v.String()
			}
		}
	}
	return st
}

// Highest returns the newest candidate whose distance from current is within
// level, or nil when there is nothing to upgrade to.
func Highest(candidates []*version.Version, current *version.Version, level Level) *version.Version {
	var best *version.Version
	for _, c := range candidates {
		l := Classify(current, c)
		if l == LevelNone || !level.Allows(l) {
			continue
		}
		if best == nil || c.GreaterThan(best) {
			best = c
		}
	}
	return best
}

// HighestExact returns the newest candidate classified at exactly level.
func HighestExact(candidates []*version.Version, current *version.Version, level Level) *version.Version {
	var best *version.Version
	for _, c := range candidates {
		if Classify(current, c) != level {
			continue
		}
		if best == nil || c.GreaterThan(best) {
			best = c
		}
	}
	return best
}

// Next selects the first published version newer than current within the level budget.
func Next(candidates []*version.Version, current *version.Version, level Level) *version.Version {
	var first *version.Version
	for _, candidate := range candidates {
		classified := Classify(current, candidate)
		if classified != LevelNone && level.Allows(classified) && (first == nil || candidate.LessThan(first)) {
			first = candidate
		}
	}
	return first
}

// NextExact selects the first published version classified at exactly level.
func NextExact(candidates []*version.Version, current *version.Version, level Level) *version.Version {
	var first *version.Version
	for _, candidate := range candidates {
		if Classify(current, candidate) == level && (first == nil || candidate.LessThan(first)) {
			first = candidate
		}
	}
	return first
}

// HighestSatisfying returns the newest candidate permitted by constraint.
func HighestSatisfying(candidates []*version.Version, constraint string) *version.Version {
	var best *version.Version
	for _, c := range candidates {
		ok, err := Satisfies(constraint, c)
		if err != nil || !ok {
			continue
		}
		if best == nil || c.GreaterThan(best) {
			best = c
		}
	}
	return best
}

// LowestSatisfying returns the oldest candidate permitted by constraint, which
// is the effective floor a module declares.
func LowestSatisfying(candidates []*version.Version, constraint string) *version.Version {
	var best *version.Version
	for _, c := range candidates {
		ok, err := Satisfies(constraint, c)
		if err != nil || !ok {
			continue
		}
		if best == nil || c.LessThan(best) {
			best = c
		}
	}
	return best
}

func filterPrerelease(in []*version.Version, include bool) []*version.Version {
	if include {
		return in
	}
	out := make([]*version.Version, 0, len(in))
	for _, v := range in {
		if !IsPrerelease(v) {
			out = append(out, v)
		}
	}
	return out
}

// Action is a single planned provider change in a single module.
type Action struct {
	Module    string        `json:"module"`
	ModuleDir string        `json:"-"`
	LocalName string        `json:"local_name"`
	Source    string        `json:"source"`
	Address   model.Address `json:"-"`

	From  string `json:"from"`
	To    string `json:"to"`
	Level Level  `json:"level"`

	// Breaking flags upgrades that semver says may break, including 0.x minor
	// bumps that a major-only check would miss.
	Breaking bool `json:"breaking"`

	// ConstraintOnly marks an action that changes no version, only the
	// declared constraint, bringing a stale floor up to what is installed.
	ConstraintOnly bool `json:"constraint_only,omitempty"`

	Constraint Rewrite `json:"constraint"`

	// Requirement locates the declaration to edit.
	Requirement model.ProviderRequirement `json:"-"`
}

// Plan is an ordered set of actions plus the reasons other providers were
// skipped, so a dry run explains everything it did not do.
type Plan struct {
	Actions []Action `json:"actions"`
	Skipped []Skip   `json:"skipped,omitempty"`
}

// Skip records a provider that was considered but not upgraded.
type Skip struct {
	Module    string        `json:"module"`
	LocalName string        `json:"local_name"`
	Source    string        `json:"source"`
	Address   model.Address `json:"-"`
	Reason    string        `json:"reason"`
}

// HasBreaking reports whether any planned action may break.
func (p *Plan) HasBreaking() bool {
	for _, a := range p.Actions {
		if a.Breaking {
			return true
		}
	}
	return false
}

// ModuleDirs returns the distinct module directories the plan touches.
func (p *Plan) ModuleDirs() []string {
	seen := map[string]bool{}
	var out []string
	for _, a := range p.Actions {
		if !seen[a.ModuleDir] {
			seen[a.ModuleDir] = true
			out = append(out, a.ModuleDir)
		}
	}
	return out
}

// PlanOptions controls how a single provider upgrade is planned.
type PlanOptions struct {
	// Level caps how far the upgrade may go.
	Level Level

	// ExactLevel selects only upgrades at Level instead of all upgrades up to it.
	ExactLevel bool

	IncludePrerelease bool

	// Explicit pins the target to an exact version, bypassing level selection
	// for the choice of version but not for the level check.
	Explicit *version.Version

	// PinUnconstrained adds a constraint where the requirement declares none.
	PinUnconstrained bool

	// RaiseFloor rewrites the constraint's lower bound to the target even when
	// the existing constraint would already admit it. This is how a shared
	// module declares a newer supported minimum.
	RaiseFloor bool

	// NextMajorOnly advances to the next major series rather than the newest,
	// so a multi-major gap is crossed one migration at a time.
	NextMajorOnly bool
}

// PlanAction decides what to do for one provider given a status and the
// requested options. A nil action with a non-empty reason means "nothing to do".
func PlanAction(mod *model.Module, st *Status, candidates []*version.Version, opts PlanOptions) (*Action, string, error) {
	if st.current == nil {
		return nil, "current version is unknown; run `terraform init` first or add a version constraint", nil
	}
	if opts.Explicit != nil && IsPrerelease(opts.Explicit) && !opts.IncludePrerelease {
		return nil, "prerelease targets require --include-prerelease", nil
	}

	// A dependency's constraint applies to the whole module tree, so a version
	// it forbids can never be installed however this repository is edited.
	usable := FilterSatisfying(candidates, st.ExternalConstraint)
	if !opts.IncludePrerelease {
		usable = filterPrerelease(usable, false)
	}

	if opts.Explicit != nil && st.ExternalConstraint != "" {
		ok, err := Satisfies(st.ExternalConstraint, opts.Explicit)
		if err == nil && !ok {
			return nil, fmt.Sprintf(
				"%s is forbidden by a dependency's constraint (%s); upgrade the dependency first",
				opts.Explicit, st.ExternalConstraint), nil
		}
	}

	target := opts.Explicit
	if target == nil && opts.NextMajorOnly {
		target = NextExact(usable, st.current, LevelMajor)
		if target == nil {
			return nil, fmt.Sprintf("no major version newer than %s is published%s",
				st.current, dependencyNote(st)), nil
		}
	}
	if target == nil {
		if opts.ExactLevel {
			target = NextExact(usable, st.current, opts.Level)
		} else {
			target = Next(usable, st.current, opts.Level)
		}
	}
	if target == nil {
		// Nothing newer to install, but the declared floor may still sit below
		// the version already in use. Saying so is the point of --raise-floor.
		note := ""
		if opts.RaiseFloor {
			action, why, err := constraintOnlyFloorRaise(mod, st)
			if err != nil || action != nil {
				return action, "", err
			}
			note = floorNote(why)
		}
		return nil, fmt.Sprintf("already at the newest %s-level version (%s)%s%s",
			opts.Level, st.current, dependencyNote(st), note), nil
	}

	actual := Classify(st.current, target)
	if actual == LevelNone {
		note := ""
		if opts.RaiseFloor && target.Equal(st.current) {
			action, why, err := constraintOnlyFloorRaise(mod, st)
			if err != nil || action != nil {
				return action, "", err
			}
			note = floorNote(why)
		}
		return nil, fmt.Sprintf("%s is not newer than the current version %s%s", target, st.current, note), nil
	}
	if !opts.Level.Allows(actual) {
		return nil, fmt.Sprintf("%s is a %s upgrade but --level is %s", target, actual, opts.Level), nil
	}
	if opts.ExactLevel && actual != opts.Level {
		return nil, fmt.Sprintf("%s is a %s upgrade but --exact-level requires %s", target, actual, opts.Level), nil
	}

	rewrite, err := RewriteConstraintPinned(st.Constraint, target, opts.PinUnconstrained)
	if opts.RaiseFloor {
		rewrite, err = RaiseConstraintFloor(st.Constraint, target)
	}
	if err != nil {
		return nil, "", err
	}

	// A root module still does real work when its constraint is unchanged,
	// because `init -upgrade` moves its lock file. A child module owns no lock
	// file, so with no constraint change there is nothing left to do.
	if !mod.IsRoot && !rewrite.Changed {
		note := ""
		if opts.RaiseFloor {
			note = floorNote(rewrite.Reason)
		}
		return nil, fmt.Sprintf(
			"nothing to change: %s is already permitted and a child module has no lock file of its own%s",
			target, note), nil
	}

	return &Action{
		Module:      mod.RelDir,
		ModuleDir:   mod.Dir,
		LocalName:   st.LocalName,
		Source:      st.Source,
		Address:     st.Address,
		From:        st.current.String(),
		To:          target.String(),
		Level:       actual,
		Breaking:    IsBreaking(st.current, target) || (opts.RaiseFloor && floorRaiseBreaking(st.Constraint, target)),
		Constraint:  rewrite,
		Requirement: st.Requirement,
	}, "", nil
}

// constraintOnlyFloorRaise brings a declared floor up to the version already
// in use. When the floor cannot move it returns no action and the reason why.
func constraintOnlyFloorRaise(mod *model.Module, st *Status) (*Action, string, error) {
	rewrite, err := RaiseConstraintFloor(st.Constraint, st.current)
	if err != nil {
		return nil, "", err
	}
	if !rewrite.Changed {
		return nil, rewrite.Reason, nil
	}
	return &Action{
		Module:         mod.RelDir,
		ModuleDir:      mod.Dir,
		LocalName:      st.LocalName,
		Source:         st.Source,
		Address:        st.Address,
		From:           st.current.String(),
		To:             st.current.String(),
		Level:          LevelNone,
		ConstraintOnly: true,
		Breaking:       floorRaiseBreaking(st.Constraint, st.current),
		Constraint:     rewrite,
		Requirement:    st.Requirement,
	}, "", nil
}

// floorNote appends why --raise-floor left a constraint alone, which would
// otherwise read as if there were simply nothing to do.
func floorNote(reason string) string {
	if reason == "" {
		return ""
	}
	return "; " + reason
}

func floorRaiseBreaking(constraint string, current *version.Version) bool {
	if strings.TrimSpace(constraint) == "" {
		return current != nil
	}
	terms, err := parseTerms(constraint)
	if err != nil || len(terms) == 0 {
		return false
	}
	for _, term := range terms {
		switch term.op {
		case ">=", ">", "~>", "=":
			return IsBreaking(term.version, current)
		}
	}
	return false
}

// dependencyNote explains a ceiling that comes from outside this repository,
// which is otherwise invisible in its configuration.
func dependencyNote(st *Status) string {
	if st.ExternalConstraint == "" {
		return ""
	}
	return fmt.Sprintf("; a dependency constrains this provider to %s", st.ExternalConstraint)
}
