// Package pullrequest turns an executed upgrade plan into the branch name,
// title and description of a pull request.
//
// It deliberately performs no git or API work. A pull request is raised by
// whatever runs the tool, which keeps credentials out of the CLI and leaves it
// usable with any forge.
package pullrequest

import (
	"fmt"
	"sort"
	"strings"

	version "github.com/hashicorp/go-version"

	"github.com/provlift/provlift/internal/model"
	"github.com/provlift/provlift/internal/upgrade"
)

// branchPrefix namespaces generated branches so they are recognisable and easy
// to clean up in bulk.
const branchPrefix = "provlift/"

// TerraformRun is one Terraform command that was executed as part of the
// upgrade.
type TerraformRun struct {
	Module  string `json:"module"`
	Command string `json:"command"`
	OK      bool   `json:"ok"`
}

// Request is everything needed to raise a pull request.
type Request struct {
	Branch string   `json:"branch"`
	Title  string   `json:"title"`
	Body   string   `json:"body"`
	Labels []string `json:"labels"`
}

// Build renders a pull request for the given plan.
//
// The branch name is derived from the providers and versions involved rather
// than from the time of the run, so re-running the same upgrade updates the
// existing pull request instead of opening a second one.
func Build(plan *upgrade.Plan, runs []TerraformRun, changedFiles []string) Request {
	providers := distinctProviders(plan)

	req := Request{
		Branch: branchName(plan, providers),
		Title:  title(plan, providers),
		Labels: labels(plan),
	}
	req.Body = body(plan, providers, runs, changedFiles)
	return req
}

type providerChange struct {
	address model.Address
	display string
	// to is the target version when every module agrees on one, otherwise "".
	to string
	// highest is the newest version any module moves to. It names the branch,
	// so a later release produces a new branch rather than quietly rewriting
	// an open pull request to mean something else.
	highest string
	modules int
	level   upgrade.Level
	breakng bool
}

func distinctProviders(plan *upgrade.Plan) []providerChange {
	seen := map[string]*providerChange{}

	for _, a := range plan.Actions {
		key := a.Address.String()
		existing, ok := seen[key]
		if !ok {
			seen[key] = &providerChange{
				address: a.Address,
				display: model.DisplaySource(a.Address),
				to:      a.To,
				highest: a.To,
				modules: 1,
				level:   a.Level,
				breakng: a.Breaking,
			}
			continue
		}
		// Different modules can land on different versions; report the highest
		// level and flag breaking if any module crosses that boundary.
		existing.modules++
		if a.Level > existing.level {
			existing.level = a.Level
		}
		existing.breakng = existing.breakng || a.Breaking
		if a.To != existing.to {
			existing.to = ""
		}
		if newer(a.To, existing.highest) {
			existing.highest = a.To
		}
	}

	out := make([]providerChange, 0, len(seen))
	for _, p := range seen {
		out = append(out, *p)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].display < out[j].display })
	return out
}

// newer reports whether a is a higher version than b. Unparseable versions
// never win, so the branch name stays predictable.
func newer(a, b string) bool {
	av, err := version.NewVersion(a)
	if err != nil {
		return false
	}
	bv, err := version.NewVersion(b)
	if err != nil {
		return true
	}
	return av.GreaterThan(bv)
}

func branchName(plan *upgrade.Plan, providers []providerChange) string {
	switch len(providers) {
	case 0:
		return branchPrefix + "no-changes"
	case 1:
		p := providers[0]
		return branchPrefix + slug(p.display) + "-" + slug(p.highest)
	default:
		return fmt.Sprintf("%sproviders-%s-%d", branchPrefix, highestLevel(plan), len(providers))
	}
}

func title(plan *upgrade.Plan, providers []providerChange) string {
	switch len(providers) {
	case 0:
		return "No Terraform provider changes"
	case 1:
		p := providers[0]
		if constraintOnly(plan) {
			return fmt.Sprintf("Declare %s %s as the supported minimum", p.display, p.highest)
		}
		if p.to == "" {
			// Modules sit on different major lines, so no single target is
			// true for all of them. Saying "to 4.81.0" would be wrong for the
			// ones capped at 3.x.
			return fmt.Sprintf("Upgrade %s in %d modules, up to %s", p.display, p.modules, p.highest)
		}
		return fmt.Sprintf("Upgrade %s to %s", p.display, p.to)
	default:
		return fmt.Sprintf("Upgrade %d Terraform providers", len(providers))
	}
}

func labels(plan *upgrade.Plan) []string {
	out := []string{"dependencies", "terraform"}
	if plan.HasBreaking() {
		out = append(out, "breaking-change")
	}
	if level := highestLevel(plan); level != "none" {
		out = append(out, "provider-"+level)
	}
	return out
}

func body(plan *upgrade.Plan, providers []providerChange, runs []TerraformRun, changedFiles []string) string {
	var b strings.Builder

	if len(plan.Actions) == 0 {
		b.WriteString("No provider changes were required.\n")
		return b.String()
	}

	b.WriteString("## Changes\n\n")
	b.WriteString("| Module | Provider | From | To | Level | Constraint |\n")
	b.WriteString("|---|---|---|---|---|---|\n")
	for _, a := range plan.Actions {
		constraint := "unchanged"
		if a.Constraint.Changed {
			constraint = fmt.Sprintf("`%s` -> `%s`", a.Constraint.Old, a.Constraint.New)
		}
		level := a.Level.String()
		if a.ConstraintOnly {
			level = "floor"
		}
		fmt.Fprintf(&b, "| `%s` | `%s` | %s | %s | %s | %s |\n",
			a.Module, model.DisplaySource(a.Address), a.From, a.To, level, constraint)
	}

	if plan.HasBreaking() {
		b.WriteString("\n## Breaking changes\n\n")
		b.WriteString("This upgrade crosses a boundary where the provider may introduce breaking\n")
		b.WriteString("changes. Review the upgrade guide before merging.\n\n")
		seen := map[string]bool{}
		for _, a := range plan.Actions {
			if !a.Breaking || seen[a.Source] {
				continue
			}
			seen[a.Source] = true
			fmt.Fprintf(&b, "- [%s %s documentation](https://%s/providers/%s/%s/%s/docs)\n",
				model.DisplaySource(a.Address), a.To,
				a.Address.Hostname.String(), a.Address.Namespace, a.Address.Type, a.To)
		}
	}

	if len(changedFiles) > 0 {
		b.WriteString("\n## Files changed\n\n")
		for _, f := range changedFiles {
			fmt.Fprintf(&b, "- `%s`\n", f)
		}
		b.WriteString("\nThe dependency lock files are regenerated by Terraform itself, so their\n")
		b.WriteString("checksums cover every configured platform.\n")
	}

	if len(runs) > 0 {
		b.WriteString("\n## Verification\n\n")
		b.WriteString("| Module | Command | Result |\n")
		b.WriteString("|---|---|---|\n")
		for _, r := range runs {
			result := ":white_check_mark: passed"
			if !r.OK {
				result = ":x: **failed**"
			}
			fmt.Fprintf(&b, "| `%s` | `terraform %s` | %s |\n", r.Module, r.Command, result)
		}
	}

	if skipped := blockedByDependency(plan); len(skipped) > 0 {
		b.WriteString("\n## Not upgraded further\n\n")
		b.WriteString("These providers are held below their newest release by a constraint\n")
		b.WriteString("declared in a module this repository depends on:\n\n")
		for _, s := range skipped {
			fmt.Fprintf(&b, "- `%s` in `%s`: %s\n", model.DisplaySource(s.Address), s.Module, s.Reason)
		}
	}

	b.WriteString("\n---\n\nRaised by `provlift`. ")
	b.WriteString("Version constraints were edited only where they would otherwise block the ")
	b.WriteString("target version; everything else was left untouched.\n")
	return b.String()
}

func blockedByDependency(plan *upgrade.Plan) []upgrade.Skip {
	var out []upgrade.Skip
	for _, s := range plan.Skipped {
		if strings.Contains(s.Reason, "dependency") {
			out = append(out, s)
		}
	}
	return out
}

func constraintOnly(plan *upgrade.Plan) bool {
	for _, a := range plan.Actions {
		if !a.ConstraintOnly {
			return false
		}
	}
	return len(plan.Actions) > 0
}

func highestLevel(plan *upgrade.Plan) string {
	best := upgrade.LevelNone
	for _, a := range plan.Actions {
		if a.Level > best {
			best = a.Level
		}
	}
	return best.String()
}

// slug reduces a provider address to something safe for a branch name.
func slug(s string) string {
	s = strings.ToLower(s)
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '.', r == '-':
			b.WriteRune(r)
		default:
			b.WriteRune('-')
		}
	}
	return strings.Trim(b.String(), "-")
}
