// Package discovery scans a repository for Terraform configuration and builds
// an inventory of every provider requirement and lock entry it finds.
package discovery

import (
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"github.com/bmatcuk/doublestar/v4"
	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/hclparse"

	"github.com/provlift/provlift/internal/lockfile"
	"github.com/provlift/provlift/internal/model"
)

// skipDirs are never descended into. `.terraform` holds Terraform's own cache
// of downloaded child modules, which would otherwise be reported as first-class
// configuration in this repository.
var skipDirs = map[string]bool{
	".git":         true,
	".terraform":   true,
	".terragrunt":  true,
	"node_modules": true,
	".idea":        true,
	".vscode":      true,
}

// Options controls a scan.
type Options struct {
	// Root is the directory to scan.
	Root string

	// ExcludeGlobs are matched against each directory's path relative to Root,
	// using doublestar semantics on slash-separated paths.
	ExcludeGlobs []string

	// RegistryHost is the registry a provider source without a hostname
	// resolves against. Empty means Terraform's public registry; OpenTofu
	// resolves the same source against registry.opentofu.org, and its lock
	// files record that host.
	RegistryHost string
}

// Scan walks Options.Root and returns the provider inventory.
func Scan(opts Options) (*model.Inventory, error) {
	root, err := filepath.Abs(opts.Root)
	if err != nil {
		return nil, fmt.Errorf("resolving root %q: %w", opts.Root, err)
	}
	info, err := os.Stat(root)
	if err != nil {
		return nil, fmt.Errorf("reading root %q: %w", opts.Root, err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("root %q is not a directory", opts.Root)
	}

	inv := &model.Inventory{Root: root}
	parser := hclparse.NewParser()

	// childRefs accumulates directories referenced by a local `module` source.
	// They are resolved to root modules only after the whole tree is walked,
	// because a child may be discovered before its caller.
	childRefs := map[string]bool{}
	byDir := map[string]*model.Module{}

	walkErr := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			inv.Diagnostics = append(inv.Diagnostics, model.Diagnostic{
				Severity: "warning",
				Summary:  "could not read path",
				Detail:   err.Error(),
				Pos:      model.SourcePos{File: relTo(root, path)},
			})
			return nil
		}
		if !d.IsDir() {
			return nil
		}
		if path != root && ShouldSkipDir(root, path, opts.ExcludeGlobs) {
			return filepath.SkipDir
		}

		mod, refs, diags := scanDir(parser, root, path, opts.RegistryHost)
		inv.Diagnostics = append(inv.Diagnostics, diags...)
		if mod == nil {
			return nil
		}

		byDir[path] = mod
		inv.Modules = append(inv.Modules, mod)
		for _, ref := range refs {
			childRefs[ref] = true
		}
		return nil
	})
	if walkErr != nil {
		return nil, walkErr
	}

	for dir, mod := range byDir {
		mod.IsRoot = !childRefs[dir]
	}

	sort.Slice(inv.Modules, func(i, j int) bool {
		return inv.Modules[i].RelDir < inv.Modules[j].RelDir
	})
	return inv, nil
}

// ShouldSkipDir applies the same built-in and configured exclusions to scans
// and pre-scan recovery, so excluded files are never modified by recovery.
func ShouldSkipDir(root, dir string, excludes []string) bool {
	if skipDirs[filepath.Base(dir)] {
		return true
	}
	rel := filepath.ToSlash(relTo(root, dir))
	for _, glob := range excludes {
		glob = strings.TrimSuffix(filepath.ToSlash(glob), "/")
		for candidate := rel; candidate != "."; candidate = path.Dir(candidate) {
			if matched, err := doublestar.Match(glob, candidate); err == nil && matched {
				return true
			}
		}
	}
	return false
}

// scanDir parses the Terraform files directly inside dir. It returns nil when
// the directory holds no Terraform configuration.
func scanDir(parser *hclparse.Parser, root, dir, registryHost string) (*model.Module, []string, []model.Diagnostic) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, nil, []model.Diagnostic{{
			Severity: "warning",
			Summary:  "could not list directory",
			Detail:   err.Error(),
			Pos:      model.SourcePos{File: relTo(root, dir)},
		}}
	}

	var tfFiles []string
	var diags []model.Diagnostic
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		switch {
		case strings.HasSuffix(name, ".tf.json"):
			diags = append(diags, model.Diagnostic{
				Severity: "warning",
				Summary:  "JSON configuration is not supported",
				Detail:   "provider requirements in .tf.json files are ignored; edit them manually",
				Pos:      model.SourcePos{File: relTo(root, filepath.Join(dir, name))},
			})
		case strings.HasSuffix(name, ".tf"):
			tfFiles = append(tfFiles, filepath.Join(dir, name))
		}
	}
	if len(tfFiles) == 0 {
		return nil, nil, diags
	}
	sort.Strings(tfFiles)

	mod := &model.Module{
		Dir:    dir,
		RelDir: filepath.ToSlash(relTo(root, dir)),
	}

	var childRefs []string
	seenLocal := map[string]string{}

	for _, file := range tfFiles {
		parsed, fileDiags := parseFile(parser, root, file, registryHost)
		diags = append(diags, fileDiags...)
		if parsed == nil {
			continue
		}

		if parsed.requiredVersion != "" {
			mod.RequiredVersion = parsed.requiredVersion
		}
		for _, req := range parsed.requirements {
			if prev, dup := seenLocal[req.LocalName]; dup {
				diags = append(diags, model.Diagnostic{
					Severity: "warning",
					Summary:  fmt.Sprintf("provider %q declared more than once", req.LocalName),
					Detail:   "first declaration at " + prev + " wins; remove the duplicate",
					Pos:      req.ConstraintPos,
				})
				continue
			}
			seenLocal[req.LocalName] = fmt.Sprintf("%s:%d", req.ConstraintPos.File, req.ConstraintPos.Line)
			mod.Requirements = append(mod.Requirements, req)
		}
		for _, src := range parsed.localModuleSources {
			childRefs = append(childRefs, filepath.Clean(filepath.Join(dir, filepath.FromSlash(src))))
		}
	}

	locks, hasLock, err := lockfile.Read(dir)
	if err != nil {
		diags = append(diags, model.Diagnostic{
			Severity: "error",
			Summary:  "could not read dependency lock file",
			Detail:   err.Error(),
			Pos:      model.SourcePos{File: filepath.ToSlash(relTo(root, filepath.Join(dir, lockfile.Name)))},
		})
	}
	mod.Locks = locks
	mod.HasLockFile = hasLock
	if hasLock {
		mod.LockFilePath = filepath.Join(dir, lockfile.Name)
	}
	mod.Children = childRefs

	sort.Slice(mod.Requirements, func(i, j int) bool {
		return mod.Requirements[i].LocalName < mod.Requirements[j].LocalName
	})
	return mod, childRefs, diags
}

// relTo returns path relative to root, slash-separated so that reported
// locations read the same on every operating system.
func relTo(root, path string) string {
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return path
	}
	return filepath.ToSlash(rel)
}

func pos(root string, r hcl.Range) model.SourcePos {
	return model.SourcePos{
		File: filepath.ToSlash(relTo(root, r.Filename)),
		Line: r.Start.Line,
	}
}
