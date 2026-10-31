// Package depgraph reads the modules Terraform has vendored into
// .terraform/modules and reports the provider constraints they declare.
//
// Provider constraints intersect across the whole module tree, so a ceiling
// declared inside a shared module binds every configuration that depends on
// it. Those declarations live outside the repository being scanned, which is
// why they have to be read from the vendored copies.
package depgraph

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/provlift/provlift/internal/discovery"
	"github.com/provlift/provlift/internal/model"
)

// manifestPath is where Terraform records the modules it has installed.
const manifestPath = ".terraform/modules/modules.json"

// Requirement is one provider constraint declared by one vendored module.
type Requirement struct {
	// Module is the module call key, e.g. "azure_region".
	Module string `json:"module"`

	// Source is the raw source address, e.g. a git URL with a ref.
	Source string `json:"source"`

	// Ref is the version the source is pinned to, when the address carries
	// one. It is what has to change to lift the constraint.
	Ref string `json:"ref,omitempty"`

	Address    model.Address `json:"-"`
	Constraint string        `json:"constraint"`
}

// Label renders the module and its pinned version for reporting.
func (r Requirement) Label() string {
	name := r.Module
	if short := shortSource(r.Source); short != "" {
		name = short
	}
	if r.Ref != "" {
		return name + " " + r.Ref
	}
	return name
}

// Vendored reports whether Terraform has recorded a module manifest for
// rootDir.
//
// The distinction matters: where a manifest exists the vendored modules are a
// complete account of what the dependencies require, so a provider absent from
// Load's result genuinely has no external constraint. Without one, nothing is
// known and a weaker source has to be consulted.
func Vendored(rootDir string) bool {
	_, err := os.Stat(filepath.Join(rootDir, filepath.FromSlash(manifestPath)))
	return err == nil
}

// Load reports the provider constraints declared by modules vendored beneath
// rootDir. A module tree that has never been initialised yields no results and
// no error: there is simply nothing recorded yet.
func Load(rootDir string) ([]Requirement, error) {
	return LoadRegistry(rootDir, "")
}

// LoadRegistry is Load with the registry that sources without a hostname
// resolve against, so vendored requirements match the root module's.
func LoadRegistry(rootDir, registryHost string) ([]Requirement, error) {
	raw, err := os.ReadFile(filepath.Join(rootDir, filepath.FromSlash(manifestPath)))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}

	var manifest struct {
		Modules []struct {
			Key    string `json:"Key"`
			Source string `json:"Source"`
			Dir    string `json:"Dir"`
		} `json:"Modules"`
	}
	if err := json.Unmarshal(raw, &manifest); err != nil {
		return nil, err
	}

	var out []Requirement
	for _, entry := range manifest.Modules {
		// The empty key is the root module itself, and local sources are
		// already covered by the ordinary scan.
		if entry.Key == "" || isLocalSource(entry.Source) {
			continue
		}

		dir := entry.Dir
		if !filepath.IsAbs(dir) {
			dir = filepath.Join(rootDir, filepath.FromSlash(dir))
		}

		for _, req := range discovery.RequirementsInRegistry(dir, registryHost) {
			if req.Constraint == "" {
				continue
			}
			out = append(out, Requirement{
				Module:     entry.Key,
				Source:     entry.Source,
				Ref:        refOf(entry.Source),
				Address:    req.Address,
				Constraint: req.Constraint,
			})
		}
	}

	sort.Slice(out, func(i, j int) bool {
		if out[i].Address.String() != out[j].Address.String() {
			return out[i].Address.String() < out[j].Address.String()
		}
		return out[i].Module < out[j].Module
	})
	return out, nil
}

// For returns the requirements that apply to one provider.
func For(reqs []Requirement, addr model.Address) []Requirement {
	var out []Requirement
	for _, r := range reqs {
		if r.Address.Equals(addr) {
			out = append(out, r)
		}
	}
	return out
}

// Constraint combines the given requirements into a single constraint string,
// dropping duplicates.
func Constraint(reqs []Requirement) string {
	seen := map[string]bool{}
	var terms []string

	for _, r := range reqs {
		for _, term := range strings.Split(r.Constraint, ",") {
			term = strings.TrimSpace(term)
			if term != "" && !seen[term] {
				seen[term] = true
				terms = append(terms, term)
			}
		}
	}
	return strings.Join(terms, ", ")
}

// Sources lists the distinct modules behind the given requirements.
func Sources(reqs []Requirement) []string {
	seen := map[string]bool{}
	var out []string
	for _, r := range reqs {
		label := r.Label()
		if !seen[label] {
			seen[label] = true
			out = append(out, label)
		}
	}
	sort.Strings(out)
	return out
}

func isLocalSource(source string) bool {
	return source == "" || strings.HasPrefix(source, "./") || strings.HasPrefix(source, "../")
}

// refOf extracts the pinned version from a source address, covering the
// `?ref=` form used by git sources and the trailing `//` form used elsewhere.
func refOf(source string) string {
	if i := strings.Index(source, "?ref="); i >= 0 {
		ref := source[i+len("?ref="):]
		if j := strings.IndexAny(ref, "&#"); j >= 0 {
			ref = ref[:j]
		}
		return ref
	}
	return ""
}

// shortSource reduces a source address to a repository name.
func shortSource(source string) string {
	s := source
	if i := strings.Index(s, "?"); i >= 0 {
		s = s[:i]
	}
	s = strings.TrimSuffix(s, ".git")
	s = strings.TrimSuffix(s, "/")

	if i := strings.LastIndex(s, "/"); i >= 0 {
		return s[i+1:]
	}
	return ""
}
