package cli

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/provlift/provlift/internal/drift"
	"github.com/provlift/provlift/internal/model"
	"github.com/provlift/provlift/internal/report"
)

func newDriftCommand(g *globalOptions) *cobra.Command {
	var (
		selectors []string
		exitCode  bool
	)

	cmd := &cobra.Command{
		Use:   "drift",
		Short: "Find provider inconsistencies between modules",
		Long: `Compares modules against each other rather than against a registry, and
reports three kinds of inconsistency:

  Version skew           the same provider locked at different versions
  Constraint divergence  the same provider constrained differently
  Unpinned modules       root modules that record no dependency lock file

Version skew is the one that usually matters. Two environments sharing the
same configuration and the same constraint can still be running different
provider versions, purely because they were initialised at different times.
Nothing in the configuration reveals that.

This command is entirely offline: it never contacts a registry and needs no
credentials.`,
		Example: `  # Everything
  provlift drift

  # One provider
  provlift drift --provider azurerm

  # Fail a CI job when modules disagree
  provlift drift --exit-code`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ws, err := g.open()
			if err != nil {
				return exitWith(ExitError, err)
			}

			result := drift.Analyze(ws.inv, drift.Options{Selectors: selectors})

			if g.jsonOut {
				if err := writeJSON(g, map[string]any{
					"root":        ws.inv.Root,
					"findings":    result,
					"count":       result.Count(),
					"diagnostics": ws.inv.Diagnostics,
				}); err != nil {
					return exitWith(ExitError, err)
				}
			} else {
				printDrift(g, result)
				printDiagnostics(g, ws)
			}

			if exitCode && result.HasFindings() {
				return exitWith(ExitUpgradesAvailable, nil)
			}
			return nil
		},
	}

	f := cmd.Flags()
	f.StringSliceVar(&selectors, "provider", nil, "only report on these providers (repeatable)")
	f.BoolVar(&exitCode, "exit-code", false, "exit 2 when inconsistencies are found, for CI gating")
	return cmd
}

func printDrift(g *globalOptions, result *drift.Report) {
	out := g.stdout()

	if !result.HasFindings() {
		fmt.Fprintln(out, "No inconsistencies found between modules.")
		return
	}

	if len(result.Skew) > 0 {
		report.Section(out, "Version skew")
		fmt.Fprintln(out, "The same provider is locked at different versions in different modules.")
		fmt.Fprintln(out)

		rows := make([][]string, 0, len(result.Skew))
		for _, s := range result.Skew {
			rows = append(rows, []string{
				model.DisplaySource(s.Address),
				fmt.Sprintf("%s .. %s", s.Oldest, s.Newest),
				strings.ToUpper(s.Span),
				fmt.Sprint(len(s.Uses)),
			})
		}
		_ = report.Table(out, []string{"SOURCE", "RANGE", "SPAN", "VERSIONS"}, rows)

		for _, s := range result.Skew {
			fmt.Fprintf(out, "\n  %s\n", model.DisplaySource(s.Address))

			width := 0
			for _, use := range s.Uses {
				if len(use.Version) > width {
					width = len(use.Version)
				}
			}

			for _, use := range s.Uses {
				for i, mod := range use.Modules {
					label := use.Version
					if i > 0 {
						label = ""
					}
					fmt.Fprintf(out, "    %-*s  %s\n", width, label, mod)
				}
			}
		}
	}

	if len(result.Divergence) > 0 {
		report.Section(out, "Constraint divergence")
		fmt.Fprintln(out, "The same provider is constrained differently in different modules.")

		for _, d := range result.Divergence {
			fmt.Fprintf(out, "\n  %s\n", model.DisplaySource(d.Address))
			for _, use := range d.Uses {
				fmt.Fprintf(out, "    %-22s %s\n", use.Constraint, strings.Join(use.Modules, ", "))
			}
		}
	}

	if len(result.Unpinned) > 0 {
		report.Section(out, "Unpinned root modules")
		fmt.Fprintln(out, "These root modules record no .terraform.lock.hcl, so nothing fixes the")
		fmt.Fprintln(out, "provider versions they will install.")
		fmt.Fprintln(out)

		rows := make([][]string, 0, len(result.Unpinned))
		for _, u := range result.Unpinned {
			rows = append(rows, []string{u.Module, strings.Join(u.Providers, ", ")})
		}
		_ = report.Table(out, []string{"MODULE", "PROVIDERS"}, rows)
	}

	fmt.Fprintf(out, "\n%d finding(s).\n", result.Count())
}
