package cli

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/provlift/provlift/internal/model"
	"github.com/provlift/provlift/internal/report"
)

func writeJSON(g *globalOptions, v any) error {
	return report.JSON(g.stdout(), v)
}

func newListCommand(g *globalOptions) *cobra.Command {
	var (
		selectors  []string
		rootsOnly  bool
		showHidden bool
		core       bool
	)

	cmd := &cobra.Command{
		Use:   "list",
		Short: "List the Terraform providers declared in a repository",
		Long: `Scans every Terraform directory under --dir and reports each provider
requirement: its local name, fully qualified source, version constraint, the
version currently recorded in .terraform.lock.hcl, and the file and line the
constraint is declared on.

--core lists each module's Terraform/OpenTofu version instead: the
required_version it declares and the version named by the nearest
.terraform-version or .opentofu-version (at or above the module, within
--dir). When one directory holds both files, the configured binary decides:
.opentofu-version for tofu, .terraform-version otherwise. The JSON output
always carries this as "modules".

This command is entirely offline; it never contacts a registry.`,
		Example: `  # Providers in every module
  provlift list

  # Terraform/OpenTofu versions: required_version and version files
  provlift list --core`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ws, err := g.open()
			if err != nil {
				return exitWith(ExitError, err)
			}

			type row struct {
				Module     string `json:"module"`
				IsRoot     bool   `json:"is_root"`
				LocalName  string `json:"local_name"`
				Source     string `json:"source"`
				Constraint string `json:"constraint"`
				Locked     string `json:"locked"`
				Declared   string `json:"declared_at"`
				Ignored    bool   `json:"ignored"`

				// Table-only forms; JSON keeps the fully qualified values.
				displaySource string
				displayFile   string
			}

			var rows []row
			for _, mod := range ws.inv.Modules {
				if rootsOnly && !mod.IsRoot {
					continue
				}
				for _, req := range mod.Requirements {
					if !selectorsMatch(req.Address, selectors) {
						continue
					}
					ignored := ws.cfg.IsIgnored(req.Address)
					if ignored && !showHidden {
						continue
					}

					locked := ""
					if l, ok := mod.Lock(req.Address); ok {
						locked = l.Version
					}
					rows = append(rows, row{
						Module:     mod.RelDir,
						IsRoot:     mod.IsRoot,
						LocalName:  req.LocalName,
						Source:     req.Source,
						Constraint: req.Constraint,
						Locked:     locked,
						Declared:   fmt.Sprintf("%s:%d", req.ConstraintPos.File, req.ConstraintPos.Line),
						Ignored:    ignored,

						displaySource: model.DisplaySource(req.Address),
						displayFile: fmt.Sprintf("%s:%d",
							fileWithinModule(mod.RelDir, req.ConstraintPos.File), req.ConstraintPos.Line),
					})
				}
			}

			type coreRow struct {
				Module          string `json:"module"`
				IsRoot          bool   `json:"is_root"`
				RequiredVersion string `json:"required_version"`
				VersionFile     string `json:"version_file"`
				VersionFilePath string `json:"version_file_path"`
			}
			coreRows := []coreRow{}
			for _, mod := range ws.inv.Modules {
				if rootsOnly && !mod.IsRoot {
					continue
				}
				coreRows = append(coreRows, coreRow{
					Module:          mod.RelDir,
					IsRoot:          mod.IsRoot,
					RequiredVersion: mod.RequiredVersion,
					VersionFile:     mod.VersionFile,
					VersionFilePath: mod.VersionFilePath,
				})
			}

			if g.jsonOut {
				return writeJSON(g, map[string]any{
					"root":        ws.inv.Root,
					"providers":   rows,
					"modules":     coreRows,
					"diagnostics": ws.inv.Diagnostics,
				})
			}

			if core {
				if len(coreRows) == 0 {
					fmt.Fprintln(g.stdout(), "No Terraform modules found.")
					printDiagnostics(g, ws)
					return nil
				}
				table := make([][]string, 0, len(coreRows))
				for _, r := range coreRows {
					kind := "child"
					if r.IsRoot {
						kind = "root"
					}
					file := r.VersionFile
					if file != "" {
						file = fmt.Sprintf("%s (%s)", r.VersionFile, r.VersionFilePath)
					}
					table = append(table, []string{r.Module, kind, r.RequiredVersion, file})
				}
				if err := report.Table(g.stdout(),
					[]string{"MODULE", "KIND", "REQUIRED_VERSION", "VERSION FILE"}, table); err != nil {
					return exitWith(ExitError, err)
				}
				printDiagnostics(g, ws)
				return nil
			}

			if len(rows) == 0 {
				fmt.Fprintln(g.stdout(), "No Terraform provider requirements found.")
				printDiagnostics(g, ws)
				return nil
			}

			table := make([][]string, 0, len(rows))
			for _, r := range rows {
				kind := "child"
				if r.IsRoot {
					kind = "root"
				}
				name := r.LocalName
				if r.Ignored {
					name += " (ignored)"
				}
				table = append(table, []string{
					r.Module, kind, name, r.displaySource, r.Constraint, r.Locked, r.displayFile,
				})
			}
			if err := report.Table(g.stdout(),
				[]string{"MODULE", "KIND", "NAME", "SOURCE", "CONSTRAINT", "LOCKED", "DECLARED AT"},
				table); err != nil {
				return exitWith(ExitError, err)
			}

			printDiagnostics(g, ws)
			return nil
		},
	}

	f := cmd.Flags()
	f.StringSliceVar(&selectors, "provider", nil, "only show these providers (repeatable)")
	f.BoolVar(&rootsOnly, "roots-only", false, "only show root modules")
	f.BoolVar(&showHidden, "show-ignored", false, "include providers excluded by configuration")
	f.BoolVar(&core, "core", false, "list each module's required_version and version file instead of providers")
	return cmd
}

// fileWithinModule trims the module path from a repository-relative file path,
// because the module is already shown in its own column.
func fileWithinModule(moduleRel, file string) string {
	if moduleRel == "" || moduleRel == "." {
		return file
	}
	return strings.TrimPrefix(file, moduleRel+"/")
}

func printDiagnostics(g *globalOptions, ws *workspace) {
	if len(ws.inv.Diagnostics) == 0 {
		return
	}

	fmt.Fprintf(g.stderr(), "\n%d issue(s) found while scanning:\n", len(ws.inv.Diagnostics))
	for _, d := range ws.inv.Diagnostics {
		location := d.Pos.File
		if d.Pos.Line > 0 {
			location = fmt.Sprintf("%s:%d", d.Pos.File, d.Pos.Line)
		}
		fmt.Fprintf(g.stderr(), "  [%s] %s: %s\n", d.Severity, location, d.Summary)
		if d.Detail != "" {
			fmt.Fprintf(g.stderr(), "          %s\n", d.Detail)
		}
	}
}
