package cli

import (
	"fmt"
	"strings"

	version "github.com/hashicorp/go-version"
	"github.com/spf13/cobra"

	"github.com/provlift/provlift/internal/depgraph"
	"github.com/provlift/provlift/internal/model"
	"github.com/provlift/provlift/internal/report"
	"github.com/provlift/provlift/internal/upgrade"
)

// collectStatuses reconciles every matching requirement against the registry.
func collectStatuses(ws *workspace, vs versionSet, selectors, modules []string, includePrerelease bool) []*upgrade.Status {
	deps := loadDependencyConstraints(ws)

	var out []*upgrade.Status
	for _, mod := range ws.inv.Modules {
		if !moduleMatches(mod.RelDir, modules) {
			continue
		}
		for _, req := range mod.Requirements {
			if !selectorsMatch(req.Address, selectors) || ws.cfg.IsIgnored(req.Address) {
				continue
			}
			st := upgrade.BuildStatus(mod, req, vs.Get(req.Address), includePrerelease)
			applyDependencyConstraints(ws, deps, mod, req, st, vs.Get(req.Address))

			if err := vs.Err(req.Address); err != nil {
				st.Error = err.Error()
			}
			out = append(out, st)
		}
	}
	return out
}

// rootDeps records what is known about one root module's dependencies.
type rootDeps struct {
	// vendored is true when Terraform has installed the module tree, making
	// reqs a complete account of the constraints dependencies impose.
	vendored bool
	reqs     []depgraph.Requirement
}

// loadDependencyConstraints reads the modules each root module has vendored.
// Those declarations are the authoritative source of a provider ceiling that
// no file in this repository mentions.
func loadDependencyConstraints(ws *workspace) map[string]rootDeps {
	out := map[string]rootDeps{}
	for _, root := range ws.inv.RootModules() {
		entry := rootDeps{}
		if depgraph.Vendored(root.Dir) {
			if reqs, err := depgraph.LoadRegistry(root.Dir, model.DefaultRegistryHost(ws.cfg.Terraform.Binary)); err == nil {
				entry.vendored = true
				entry.reqs = reqs
			}
		}
		out[root.Dir] = entry
	}
	return out
}

// applyDependencyConstraints attributes any externally imposed ceiling to the
// module that declares it, falling back to the merged constraints recorded in
// the lock file only where the module tree has not been vendored.
func applyDependencyConstraints(
	ws *workspace,
	deps map[string]rootDeps,
	mod *model.Module,
	req model.ProviderRequirement,
	st *upgrade.Status,
	candidates []*version.Version,
) {
	// A child module owns no lock file and vendors nothing itself, so its
	// ceiling comes from the roots that consume it.
	roots := []*model.Module{mod}
	if !mod.IsRoot {
		roots = ws.inv.AffectedRoots([]string{mod.Dir})
	}

	vendored := false
	var matched []depgraph.Requirement
	for _, root := range roots {
		entry := deps[root.Dir]
		vendored = vendored || entry.vendored
		matched = append(matched, depgraph.For(entry.reqs, req.Address)...)
	}

	if len(matched) > 0 {
		st.SetExternalConstraint(depgraph.Constraint(matched), candidates)
		st.ExternalSources = depgraph.Sources(matched)
		return
	}

	if vendored {
		// The dependencies are fully known and none constrains this provider.
		// A lock file may still record constraints from an earlier state of
		// this repository, which are not a dependency's doing.
		st.SetExternalConstraint("", candidates)
		return
	}

	if st.ExternalConstraint != "" {
		return
	}

	// Nothing vendored: recover what we can from the lock files instead.
	seen := map[string]bool{}
	var terms []string
	for _, root := range roots {
		lock, ok := root.Lock(req.Address)
		if !ok {
			continue
		}
		for _, term := range strings.Split(upgrade.ExternalConstraint(lock.Constraints, req.Constraint), ",") {
			term = strings.TrimSpace(term)
			if term != "" && !seen[term] {
				seen[term] = true
				terms = append(terms, term)
			}
		}
	}
	if len(terms) > 0 {
		st.SetExternalConstraint(strings.Join(terms, ", "), candidates)
	}
}

func pluralModules(n int) string {
	if n == 1 {
		return "that module"
	}
	return "those modules"
}

func newCheckCommand(g *globalOptions) *cobra.Command {
	var (
		selectors []string
		modules   []string
		exitCode  bool
		level     string
	)

	cmd := &cobra.Command{
		Use:   "check",
		Short: "Report the provider upgrades available in a repository",
		Long: `Compares the provider version currently selected in each module against the
versions published by the provider's registry, and classifies the gap as a
patch, minor or major upgrade.

The current version is read from .terraform.lock.hcl when the module has been
initialised. Otherwise it falls back to the lowest version the declared
constraint allows, and the source is reported so the difference is visible.

"BLOCKED" marks a provider where a newer version exists but the declared
constraint forbids it; ` + "`provlift upgrade`" + ` will rewrite the constraint in that case.

Read-only: this command never modifies files.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ws, err := g.open()
			if err != nil {
				return exitWith(ExitError, err)
			}

			maxLevel := upgrade.LevelMajor
			if level != "" {
				maxLevel, err = upgrade.ParseLevel(level)
				if err != nil {
					return exitWith(ExitError, err)
				}
			}

			vs := fetchVersions(cmd.Context(), ws.reg, eligibleProviders(ws, selectors, modules), ws.cfg.Platforms)
			statuses := collectStatuses(ws, vs, selectors, modules, g.includePrerelease)

			upgradable := 0
			for _, st := range statuses {
				if st.Available != upgrade.LevelNone && maxLevel.Allows(st.Available) {
					upgradable++
				}
			}

			if g.jsonOut {
				if err := writeJSON(g, map[string]any{
					"root":               ws.inv.Root,
					"providers":          statuses,
					"upgrades_available": upgradable,
					"diagnostics":        ws.inv.Diagnostics,
				}); err != nil {
					return exitWith(ExitError, err)
				}
			} else {
				if len(statuses) == 0 {
					fmt.Fprintln(g.stdout(), "No Terraform provider requirements found.")
					printDiagnostics(g, ws)
					return nil
				}

				rows := make([][]string, 0, len(statuses))
				for _, st := range statuses {
					available := st.Available.String()
					if st.Error != "" {
						available = "lookup failed"
					} else if !maxLevel.Allows(st.Available) && st.Available != upgrade.LevelNone {
						available = st.Available.String() + " (above --level)"
					}
					blocked := ""
					if st.Blocked {
						blocked = "yes"
					}
					if st.BlockedByDependency {
						blocked = "dep"
					}
					current := st.Current
					if st.CurrentSource == upgrade.SourceFloor {
						current += " (floor)"
					}
					rows = append(rows, []string{
						st.Module, model.DisplaySource(st.Address), st.Constraint, current,
						st.LatestPatch, st.LatestMinor, st.Latest, available, blocked,
					})
				}
				if err := report.Table(g.stdout(),
					[]string{"MODULE", "SOURCE", "CONSTRAINT", "CURRENT", "PATCH", "MINOR", "LATEST", "UPGRADE", "BLOCKED"},
					rows); err != nil {
					return exitWith(ExitError, err)
				}

				for _, st := range statuses {
					if st.Error != "" {
						fmt.Fprintf(g.stderr(), "\nCould not query %s: %s\n", st.Source, st.Error)
					}
				}

				// A ceiling inside a dependency is invisible in this repository's
				// own files, so name what imposes it.
				for _, st := range statuses {
					if !st.BlockedByDependency {
						continue
					}
					fmt.Fprintf(g.stdout(), "\n%s in %s is capped at %s, not by this repository.\n",
						model.DisplaySource(st.Address), st.Module, st.LatestReachable)
					fmt.Fprintf(g.stdout(), "  constraint:  %s\n", st.ExternalConstraint)

					if len(st.ExternalSources) == 0 {
						fmt.Fprintf(g.stdout(), "  imposed by:  a dependency (run `terraform init` to identify it)\n")
						continue
					}
					for i, src := range st.ExternalSources {
						label := "  imposed by: "
						if i > 0 {
							label = "              "
						}
						fmt.Fprintf(g.stdout(), "%s %s\n", label, src)
					}
					fmt.Fprintf(g.stdout(), "  Release %s with a wider ceiling to lift it.\n",
						pluralModules(len(st.ExternalSources)))
				}

				fmt.Fprintf(g.stdout(), "\n%d of %d provider requirement(s) have an upgrade available.\n",
					upgradable, len(statuses))
				printDiagnostics(g, ws)
			}

			if exitCode && upgradable > 0 {
				return exitWith(ExitUpgradesAvailable, nil)
			}
			return nil
		},
	}

	f := cmd.Flags()
	f.StringSliceVar(&selectors, "provider", nil, "only check these providers (repeatable)")
	f.StringSliceVar(&modules, "module", nil, "only check these module paths (repeatable, glob)")
	f.StringVar(&level, "level", "major", "highest upgrade level to count: patch, minor or major")
	f.BoolVar(&exitCode, "exit-code", false, "exit 2 when upgrades are available, for CI gating")
	return cmd
}
