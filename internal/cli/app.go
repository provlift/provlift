// Package cli wires the command surface.
package cli

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/bmatcuk/doublestar/v4"
	version "github.com/hashicorp/go-version"
	"github.com/spf13/cobra"

	"github.com/provlift/provlift/internal/config"
	"github.com/provlift/provlift/internal/discovery"
	"github.com/provlift/provlift/internal/model"
	"github.com/provlift/provlift/internal/registry"
)

// Exit codes. Distinct codes let CI branch on "work to do" without parsing
// output.
const (
	ExitOK                = 0
	ExitError             = 1
	ExitUpgradesAvailable = 2
	ExitValidationFailed  = 3
)

// ExitCodeError carries a process exit code alongside an error.
type ExitCodeError struct {
	Code int
	Err  error
}

func (e *ExitCodeError) Error() string {
	if e.Err == nil {
		return fmt.Sprintf("exit status %d", e.Code)
	}
	return e.Err.Error()
}

func (e *ExitCodeError) Unwrap() error { return e.Err }

func exitWith(code int, err error) error { return &ExitCodeError{Code: code, Err: err} }

// globalOptions are the flags shared by every command.
type globalOptions struct {
	dir               string
	jsonOut           bool
	excludes          []string
	tfBinary          string
	timeout           time.Duration
	includePrerelease bool

	out io.Writer
	err io.Writer

	// registryClient overrides the registry used by a command. It is nil in
	// normal use and set by tests to avoid network access.
	registryClient *registry.Client
}

func (g *globalOptions) bind(cmd *cobra.Command) {
	f := cmd.PersistentFlags()
	f.StringVarP(&g.dir, "dir", "C", ".", "repository directory to scan")
	f.BoolVar(&g.jsonOut, "json", false, "emit machine-readable JSON instead of a table")
	// An array, not a slice: a comma belongs to a brace glob such as
	// "modules/{a,b}" and must not split it into two broken patterns.
	f.StringArrayVar(&g.excludes, "exclude", nil, "glob of directories to skip (repeatable)")
	f.StringVar(&g.tfBinary, "terraform-binary", "", "Terraform executable to use (default: terraform, or PROVLIFT_TERRAFORM_BIN)")
	f.DurationVar(&g.timeout, "timeout", 15*time.Minute, "timeout for a single Terraform command")
	f.BoolVar(&g.includePrerelease, "include-prerelease", false, "consider prerelease provider versions")
}

// workspace is the resolved state every command operates on.
type workspace struct {
	root     string
	cfg      *config.Config
	inv      *model.Inventory
	reg      *registry.Client
	warnings io.Writer
}

func (g *globalOptions) open() (*workspace, error) {
	root, err := filepath.Abs(g.dir)
	if err != nil {
		return nil, fmt.Errorf("resolving --dir: %w", err)
	}

	cfg, err := config.Load(root)
	if err != nil {
		return nil, err
	}
	if g.tfBinary != "" {
		cfg.Terraform.Binary = g.tfBinary
	} else if cfg.Terraform.Binary == "terraform" && os.Getenv("PROVLIFT_TERRAFORM_BIN") != "" {
		cfg.Terraform.Binary = os.Getenv("PROVLIFT_TERRAFORM_BIN")
	}

	excludes := append(append([]string{}, cfg.Exclude...), g.excludes...)

	inv, err := discovery.Scan(discovery.Options{
		Root:         root,
		ExcludeGlobs: excludes,
		RegistryHost: model.DefaultRegistryHost(cfg.Terraform.Binary),
	})
	if err != nil {
		return nil, err
	}
	warnAmbiguousSelectors(g.stderr(), inv, cfg.Providers.Ignore, true)
	for selector, level := range cfg.Providers.MaxLevel {
		warnAmbiguousSelectors(g.stderr(), inv, []string{selector}, level == "none")
	}

	reg := g.registryClient
	if reg == nil {
		reg = registry.New()
	}
	return &workspace{root: root, cfg: cfg, inv: inv, reg: reg, warnings: g.stderr()}, nil
}

func warnAmbiguousSelectors(out io.Writer, inv *model.Inventory, selectors []string, deny bool) {
	sort.Strings(selectors)
	for _, selector := range selectors {
		namespaces := map[string]bool{}
		for _, mod := range inv.Modules {
			for _, req := range mod.Requirements {
				if (deny && model.MatchesDenySelector(req.Address, selector)) || (!deny && model.MatchesSelector(req.Address, selector)) {
					namespaces[req.Address.Namespace] = true
				}
			}
		}
		if len(namespaces) > 1 {
			fmt.Fprintf(out, "warning: provider selector %q matches %d namespaces in this repository\n", selector, len(namespaces))
		}
	}
}

func (g *globalOptions) stdout() io.Writer {
	if g.out != nil {
		return g.out
	}
	return os.Stdout
}

func (g *globalOptions) stderr() io.Writer {
	if g.err != nil {
		return g.err
	}
	return os.Stderr
}

// versionSet holds registry lookup results keyed by provider address.
type versionSet struct {
	versions map[string][]*version.Version
	errs     map[string]error
}

// Get returns the known versions for addr; a missing entry yields nil, which
// downstream code treats as "nothing to upgrade to".
func (v versionSet) Get(addr model.Address) []*version.Version { return v.versions[addr.String()] }

// Err returns the lookup error for addr, if any.
func (v versionSet) Err(addr model.Address) error { return v.errs[addr.String()] }

// fetchVersions queries the registry for each distinct provider concurrently.
// A failure for one provider never aborts the others: it is recorded and
// reported per row, because a private registry being unreachable should not
// hide public provider results.
func fetchVersions(ctx context.Context, reg *registry.Client, addrs []model.Address, platforms []string) versionSet {
	const maxConcurrent = 6

	out := versionSet{
		versions: make(map[string][]*version.Version, len(addrs)),
		errs:     make(map[string]error, len(addrs)),
	}

	var mu sync.Mutex
	var wg sync.WaitGroup
	sem := make(chan struct{}, maxConcurrent)

	for _, addr := range addrs {
		wg.Add(1)
		go func(addr model.Address) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()

			versions, err := reg.Versions(ctx, addr)

			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				out.errs[addr.String()] = err
				return
			}
			parsed := make([]*version.Version, 0, len(versions))
			for _, v := range versions {
				if !supportsPlatforms(v.Platforms, platforms) {
					continue
				}
				parsed = append(parsed, v.Version)
			}
			if len(parsed) == 0 {
				out.errs[addr.String()] = fmt.Errorf("no published %s version supports the requested platforms", addr.ForDisplay())
				return
			}
			sort.Slice(parsed, func(i, j int) bool { return parsed[i].LessThan(parsed[j]) })
			out.versions[addr.String()] = parsed
		}(addr)
	}
	wg.Wait()
	return out
}

func supportsPlatforms(available []registry.Platform, requested []string) bool {
	if len(requested) == 0 || len(available) == 0 {
		return true
	}
	set := make(map[string]bool, len(available))
	for _, p := range available {
		set[p.OS+"_"+p.Arch] = true
	}
	for _, p := range requested {
		if !set[p] {
			return false
		}
	}
	return true
}

func eligibleProviders(ws *workspace, selectors, modules []string) []model.Address {
	warnAmbiguousSelectors(ws.warnings, ws.inv, selectors, false)
	seen := map[string]bool{}
	var out []model.Address
	for _, mod := range ws.inv.Modules {
		if !moduleMatches(mod.RelDir, modules) {
			continue
		}
		for _, req := range mod.Requirements {
			if !selectorsMatch(req.Address, selectors) || ws.cfg.IsIgnored(req.Address) || seen[req.Address.String()] {
				continue
			}
			seen[req.Address.String()] = true
			out = append(out, req.Address)
		}
	}
	return out
}

// selectorsMatch reports whether addr matches any selector, or whether no
// selectors were supplied at all.
func selectorsMatch(addr model.Address, selectors []string) bool {
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

// moduleMatches filters modules by slash-separated doublestar globs. A bare
// directory matches that directory and all of its descendants.
func moduleMatches(rel string, filters []string) bool {
	if len(filters) == 0 {
		return true
	}
	for _, filter := range filters {
		filter = strings.TrimSuffix(strings.TrimPrefix(filepath.ToSlash(filter), "./"), "/")
		if rel == filter || strings.HasPrefix(rel, filter+"/") {
			return true
		}
		if ok, err := doublestar.Match(filter, rel); err == nil && ok {
			return true
		}
	}
	return false
}
