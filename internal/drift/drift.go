// Package drift finds inconsistencies between modules in a repository.
//
// It answers questions that no single module can: is the same provider locked
// at different versions in different places, is the same provider constrained
// differently, and which root modules pin nothing at all.
//
// All analysis is offline; it uses only what is already on disk.
package drift

import (
	"fmt"
	"sort"
	"strings"

	version "github.com/hashicorp/go-version"

	"github.com/provlift/provlift/internal/model"
	"github.com/provlift/provlift/internal/upgrade"
)

// VersionUse records the modules sharing one locked version.
type VersionUse struct {
	Version string   `json:"version"`
	Modules []string `json:"modules"`
}

// VersionSkew is one provider locked at more than one version.
type VersionSkew struct {
	Source  string        `json:"source"`
	Address model.Address `json:"-"`
	Uses    []VersionUse  `json:"versions"`

	// Oldest and Newest bound the spread, and Span classifies how far apart
	// they are so a major split can be spotted immediately.
	Oldest string `json:"oldest"`
	Newest string `json:"newest"`
	Span   string `json:"span"`
}

// ConstraintUse records the modules sharing one constraint.
type ConstraintUse struct {
	Constraint string   `json:"constraint"`
	Modules    []string `json:"modules"`
}

// ConstraintDivergence is one provider declared with more than one constraint.
type ConstraintDivergence struct {
	Source  string          `json:"source"`
	Address model.Address   `json:"-"`
	Uses    []ConstraintUse `json:"constraints"`
}

// Unpinned is a root module that declares providers but records no lock file,
// so nothing fixes the versions it will install.
type Unpinned struct {
	Module    string   `json:"module"`
	Providers []string `json:"providers"`
}

// Report is the result of an analysis.
type Report struct {
	Skew       []VersionSkew          `json:"version_skew"`
	Divergence []ConstraintDivergence `json:"constraint_divergence"`
	Unpinned   []Unpinned             `json:"unpinned_modules"`
}

// HasFindings reports whether anything at all was found.
func (r *Report) HasFindings() bool {
	return len(r.Skew) > 0 || len(r.Divergence) > 0 || len(r.Unpinned) > 0
}

// Count returns the total number of findings.
func (r *Report) Count() int {
	return len(r.Skew) + len(r.Divergence) + len(r.Unpinned)
}

// Finding kinds, as named by --fail-on.
const (
	KindSkew       = "skew"
	KindDivergence = "divergence"
	KindUnpinned   = "unpinned"
)

// Kinds lists every finding kind, in report order.
var Kinds = []string{KindSkew, KindDivergence, KindUnpinned}

// ParseKinds reads a list of finding kinds. An empty list means all of them.
func ParseKinds(names []string) (map[string]bool, error) {
	kinds := map[string]bool{}
	for _, name := range names {
		name = strings.ToLower(strings.TrimSpace(name))
		switch name {
		case KindSkew, KindDivergence, KindUnpinned:
			kinds[name] = true
		default:
			return nil, fmt.Errorf("unknown finding kind %q (want %s)", name, strings.Join(Kinds, ", "))
		}
	}
	if len(kinds) == 0 {
		for _, k := range Kinds {
			kinds[k] = true
		}
	}
	return kinds, nil
}

// HasFindingsOf reports whether there is at least one finding of the given kinds.
func (r *Report) HasFindingsOf(kinds map[string]bool) bool {
	return (kinds[KindSkew] && len(r.Skew) > 0) ||
		(kinds[KindDivergence] && len(r.Divergence) > 0) ||
		(kinds[KindUnpinned] && len(r.Unpinned) > 0)
}

// Options controls which checks run.
type Options struct {
	// Selectors limits the analysis to matching providers.
	Selectors []string
	// Ignored, when set, drops providers that configuration excludes
	// (providers.ignore), as list and check do.
	Ignored func(model.Address) bool
	// Module, when set, limits the analysis to the modules it accepts.
	Module func(*model.Module) bool
}

func (o Options) provider(addr model.Address) bool {
	if o.Ignored != nil && o.Ignored(addr) {
		return false
	}
	return matches(addr, o.Selectors)
}

func (o Options) module(mod *model.Module) bool {
	return o.Module == nil || o.Module(mod)
}

// Analyze inspects an inventory for cross-module inconsistencies.
func Analyze(inv *model.Inventory, opts Options) *Report {
	return &Report{
		Skew:       findVersionSkew(inv, opts),
		Divergence: findConstraintDivergence(inv, opts),
		Unpinned:   findUnpinned(inv, opts),
	}
}

func matches(addr model.Address, selectors []string) bool {
	if len(selectors) == 0 {
		return true
	}
	for _, s := range selectors {
		if model.MatchesSelector(addr, s) {
			return true
		}
	}
	return false
}

// findVersionSkew groups locked versions by provider. Only modules that own a
// lock file can contribute, because only they record a selection.
func findVersionSkew(inv *model.Inventory, opts Options) []VersionSkew {
	type entry struct {
		addr     model.Address
		byModule map[string][]string // version -> modules
	}

	providers := map[string]*entry{}

	for _, mod := range inv.Modules {
		if !mod.HasLockFile || !opts.module(mod) {
			continue
		}
		for _, lock := range mod.Locks {
			if !opts.provider(lock.Address) {
				continue
			}
			key := lock.Address.String()
			e, ok := providers[key]
			if !ok {
				e = &entry{addr: lock.Address, byModule: map[string][]string{}}
				providers[key] = e
			}
			e.byModule[lock.Version] = append(e.byModule[lock.Version], mod.RelDir)
		}
	}

	var out []VersionSkew
	for _, e := range providers {
		if len(e.byModule) < 2 {
			continue
		}

		uses := make([]VersionUse, 0, len(e.byModule))
		for v, mods := range e.byModule {
			sort.Strings(mods)
			uses = append(uses, VersionUse{Version: v, Modules: mods})
		}
		sortVersionUses(uses)

		skew := VersionSkew{
			Source:  e.addr.String(),
			Address: e.addr,
			Uses:    uses,
			Oldest:  uses[0].Version,
			Newest:  uses[len(uses)-1].Version,
		}
		skew.Span = spanBetween(skew.Oldest, skew.Newest)
		out = append(out, skew)
	}

	sort.Slice(out, func(i, j int) bool { return out[i].Source < out[j].Source })
	return out
}

// sortVersionUses orders by semantic version where possible, falling back to
// string order for anything unparsable.
func sortVersionUses(uses []VersionUse) {
	sort.Slice(uses, func(i, j int) bool {
		a, errA := version.NewVersion(uses[i].Version)
		b, errB := version.NewVersion(uses[j].Version)
		if errA != nil || errB != nil {
			return uses[i].Version < uses[j].Version
		}
		return a.LessThan(b)
	})
}

// spanBetween describes the distance between the oldest and newest locked
// version, so a split across a major boundary stands out from a patch drift.
func spanBetween(oldest, newest string) string {
	a, errA := version.NewVersion(oldest)
	b, errB := version.NewVersion(newest)
	if errA != nil || errB != nil {
		return "unknown"
	}

	as, bs := a.Segments(), b.Segments()
	switch {
	case len(as) > 0 && len(bs) > 0 && as[0] != bs[0]:
		return "major"
	case len(as) > 1 && len(bs) > 1 && as[1] != bs[1]:
		return "minor"
	default:
		return "patch"
	}
}

func findConstraintDivergence(inv *model.Inventory, opts Options) []ConstraintDivergence {
	type entry struct {
		addr    model.Address
		byC     map[string][]string // normalized constraint -> modules
		display map[string]string   // normalized constraint -> text as written
	}

	providers := map[string]*entry{}

	for _, mod := range inv.Modules {
		if !opts.module(mod) {
			continue
		}
		for _, req := range mod.Requirements {
			if !opts.provider(req.Address) {
				continue
			}
			key := req.Address.String()
			e, ok := providers[key]
			if !ok {
				e = &entry{addr: req.Address, byC: map[string][]string{}, display: map[string]string{}}
				providers[key] = e
			}

			constraint := NormalizeConstraint(req.Constraint)
			written := strings.TrimSpace(req.Constraint)
			if constraint == "" {
				constraint, written = "(none)", "(none)"
			}
			if _, ok := e.display[constraint]; !ok {
				e.display[constraint] = written
			}
			e.byC[constraint] = append(e.byC[constraint], mod.RelDir)
		}
	}

	var out []ConstraintDivergence
	for _, e := range providers {
		if len(e.byC) < 2 {
			continue
		}

		uses := make([]ConstraintUse, 0, len(e.byC))
		for c, mods := range e.byC {
			sort.Strings(mods)
			uses = append(uses, ConstraintUse{Constraint: e.display[c], Modules: mods})
		}
		sort.Slice(uses, func(i, j int) bool { return uses[i].Constraint < uses[j].Constraint })

		out = append(out, ConstraintDivergence{
			Source:  e.addr.String(),
			Address: e.addr,
			Uses:    uses,
		})
	}

	sort.Slice(out, func(i, j int) bool { return out[i].Source < out[j].Source })
	return out
}

func findUnpinned(inv *model.Inventory, opts Options) []Unpinned {
	var out []Unpinned

	for _, mod := range inv.Modules {
		// Only root modules own a lock file; a child module having none is
		// normal and not a finding.
		if !mod.IsRoot || mod.HasLockFile || !opts.module(mod) {
			continue
		}

		var providers []string
		for _, req := range mod.Requirements {
			if opts.provider(req.Address) {
				providers = append(providers, model.DisplaySource(req.Address))
			}
		}
		if len(providers) == 0 {
			continue
		}
		sort.Strings(providers)
		out = append(out, Unpinned{Module: mod.RelDir, Providers: providers})
	}

	sort.Slice(out, func(i, j int) bool { return out[i].Module < out[j].Module })
	return out
}

// NormalizeConstraint makes constraints that Terraform reads the same way
// comparable, so ">=4.16" and ">= 4.16.0", "6.0.1" and "= 6.0.1", or terms
// listed in another order are not reported as a difference.
func NormalizeConstraint(raw string) string {
	return upgrade.CanonicalConstraint(raw)
}
