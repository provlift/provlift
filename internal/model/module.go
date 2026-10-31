package model

import (
	"crypto/sha256"
	"sort"
)

// SourcePos records where a declaration lives so the CLI can report an exact
// file and line for every constraint it reads or rewrites.
type SourcePos struct {
	File string `json:"file"`
	Line int    `json:"line"`
}

// ByteRange is a half-open byte span within a file. Edits are applied by
// splicing these spans, which leaves every other byte (comments, alignment,
// unusual formatting) exactly as the author wrote it.
type ByteRange struct {
	Start int
	End   int
}

// ProviderRequirement is a single entry inside a required_providers block.
type ProviderRequirement struct {
	// LocalName is the key in required_providers. It is NOT the provider
	// identity; two modules may bind different sources to the same local name.
	LocalName string `json:"local_name"`

	Address Address `json:"-"`
	Source  string  `json:"source"`

	// Constraint is the raw version constraint string, empty when the block
	// omits `version` (Terraform then accepts any version).
	Constraint string `json:"constraint,omitempty"`

	// ConstraintPos points at the `version` argument when present, otherwise at
	// the requirement block itself, which is where a new argument gets inserted.
	ConstraintPos SourcePos `json:"position"`

	// ImpliedSource is true when the block omitted `source` and the address was
	// derived from the local name.
	ImpliedSource bool `json:"implied_source"`

	// FilePath is the absolute path of the file declaring this requirement.
	FilePath   string            `json:"-"`
	SourceHash [sha256.Size]byte `json:"-"`

	// VersionRange spans the quoted version literal, including its quotes. It is
	// nil when the requirement declares no version argument.
	VersionRange *ByteRange `json:"-"`

	// InsertPoint is the byte offset at which a new `version` argument can be
	// added, valid only when the requirement uses the object form.
	InsertPoint int        `json:"-"`
	ObjectRange *ByteRange `json:"-"`
	ObjectText  string     `json:"-"`

	// IsObject distinguishes the modern object form from the pre-0.13 shorthand
	// `github = "~> 6.0"`.
	IsObject bool `json:"-"`
}

// ProviderLock is one `provider` block from a .terraform.lock.hcl file.
type ProviderLock struct {
	Address     Address  `json:"-"`
	Source      string   `json:"source"`
	Version     string   `json:"version"`
	Constraints string   `json:"constraints,omitempty"`
	Hashes      []string `json:"hashes,omitempty"`
}

// Module is one directory containing Terraform configuration.
type Module struct {
	// Dir is absolute; RelDir is relative to the scanned root and is what gets
	// shown to users.
	Dir    string `json:"-"`
	RelDir string `json:"dir"`

	// IsRoot is false when another module in the scan references this directory
	// via a local `source = "./..."`. Only root modules own a lock file, so only
	// they can be initialised and validated.
	IsRoot bool `json:"is_root"`

	// Children holds absolute directories of local modules this one calls.
	Children []string `json:"-"`

	RequiredVersion string `json:"required_terraform_version,omitempty"`

	Requirements []ProviderRequirement `json:"providers"`

	HasLockFile  bool                    `json:"has_lock_file"`
	LockFilePath string                  `json:"-"`
	Locks        map[string]ProviderLock `json:"locks,omitempty"`
}

// Lock returns the recorded selection for addr, if the module has one.
func (m *Module) Lock(addr Address) (ProviderLock, bool) {
	l, ok := m.Locks[addr.String()]
	return l, ok
}

// Requirement returns the required_providers entry for addr, if present.
func (m *Module) Requirement(addr Address) (ProviderRequirement, bool) {
	for _, r := range m.Requirements {
		if r.Address.Equals(addr) {
			return r, true
		}
	}
	return ProviderRequirement{}, false
}

// Inventory is the result of scanning a repository.
type Inventory struct {
	Root    string    `json:"root"`
	Modules []*Module `json:"modules"`

	// Diagnostics collects non-fatal problems (unparsable files, unresolvable
	// sources) so a single bad directory never silently drops the whole scan.
	Diagnostics []Diagnostic `json:"diagnostics,omitempty"`
}

// Diagnostic is a non-fatal problem encountered during a scan.
type Diagnostic struct {
	Severity string    `json:"severity"`
	Summary  string    `json:"summary"`
	Detail   string    `json:"detail,omitempty"`
	Pos      SourcePos `json:"position,omitempty"`
}

// RootModules returns only the modules that can be initialised by Terraform.
func (inv *Inventory) RootModules() []*Module {
	var out []*Module
	for _, m := range inv.Modules {
		if m.IsRoot {
			out = append(out, m)
		}
	}
	return out
}

// AffectedRoots returns the root modules that must be re-initialised after the
// given directories change. A directory that is itself a root is returned
// directly; a child module resolves to every root that transitively calls it,
// since each of those roots owns its own lock file.
func (inv *Inventory) AffectedRoots(changed []string) []*Module {
	target := map[string]bool{}
	for _, dir := range changed {
		target[dir] = true
	}

	byDir := make(map[string]*Module, len(inv.Modules))
	for _, m := range inv.Modules {
		byDir[m.Dir] = m
	}

	var reaches func(dir string, seen map[string]bool) bool
	reaches = func(dir string, seen map[string]bool) bool {
		if target[dir] {
			return true
		}
		if seen[dir] {
			return false
		}
		seen[dir] = true

		mod, ok := byDir[dir]
		if !ok {
			return false
		}
		for _, child := range mod.Children {
			if reaches(child, seen) {
				return true
			}
		}
		return false
	}

	var out []*Module
	for _, m := range inv.Modules {
		if m.IsRoot && reaches(m.Dir, map[string]bool{}) {
			out = append(out, m)
		}
	}
	return out
}

// DistinctProviders lists every provider address found anywhere in the scan,
// in stable display order.
func (inv *Inventory) DistinctProviders() []Address {
	seen := map[string]Address{}
	for _, m := range inv.Modules {
		for _, r := range m.Requirements {
			seen[r.Address.String()] = r.Address
		}
		for _, l := range m.Locks {
			seen[l.Address.String()] = l.Address
		}
	}

	out := make([]Address, 0, len(seen))
	for _, a := range seen {
		out = append(out, a)
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].String() < out[j].String()
	})
	return out
}
