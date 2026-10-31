// Package drift finds inconsistencies between modules in a repository.
//
// It answers questions that no single module can: is the same provider locked
// at different versions in different places, is the same provider constrained
// differently, and which root modules pin nothing at all.
//
// All analysis is offline; it uses only what is already on disk.
package drift

import (
	"regexp"
	"sort"
	"strings"

	version "github.com/hashicorp/go-version"

	"github.com/provlift/provlift/internal/model"
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

// Options controls which checks run.
type Options struct {
	// Selectors limits the analysis to matching providers.
	Selectors []string
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
		if !mod.HasLockFile {
			continue
		}
		for _, lock := range mod.Locks {
			if !matches(lock.Address, opts.Selectors) {
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
		addr model.Address
		byC  map[string][]string // constraint -> modules
	}

	providers := map[string]*entry{}

	for _, mod := range inv.Modules {
		for _, req := range mod.Requirements {
			if !matches(req.Address, opts.Selectors) {
				continue
			}
			key := req.Address.String()
			e, ok := providers[key]
			if !ok {
				e = &entry{addr: req.Address, byC: map[string][]string{}}
				providers[key] = e
			}

			constraint := NormalizeConstraint(req.Constraint)
			if constraint == "" {
				constraint = "(none)"
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
			uses = append(uses, ConstraintUse{Constraint: c, Modules: mods})
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
		if !mod.IsRoot || mod.HasLockFile {
			continue
		}

		var providers []string
		for _, req := range mod.Requirements {
			if matches(req.Address, opts.Selectors) {
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

var operatorPattern = regexp.MustCompile(`^(>=|<=|!=|~>|>|<|=)\s*(.+)$`)

// NormalizeConstraint makes cosmetically different constraints comparable, so
// ">=4.16.0" and ">= 4.16.0" are not reported as a difference.
func NormalizeConstraint(raw string) string {
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))

	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		if m := operatorPattern.FindStringSubmatch(p); m != nil {
			p = m[1] + " " + strings.TrimSpace(m[2])
		}
		out = append(out, p)
	}
	return strings.Join(out, ", ")
}
