// Package config loads optional repository-level settings from .provlift.yaml.
//
// Every field is optional; a repository with no config file behaves sensibly.
package config

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/bmatcuk/doublestar/v4"
	"gopkg.in/yaml.v3"

	"github.com/provlift/provlift/internal/model"
)

// FileName is the config file looked for in the scan root and its parents.
const FileName = ".provlift.yaml"

// AltFileName is the other common YAML extension. provlift does not read it,
// so finding one is an error rather than a silently skipped policy.
const AltFileName = ".provlift.yml"

// EnvVar names an explicit config file, like --config.
const EnvVar = "PROVLIFT_CONFIG"

// Config is the repository-level configuration.
type Config struct {
	// Exclude holds glob patterns of directories to skip, relative to the root.
	Exclude []string `yaml:"exclude"`

	// Platforms are the os_arch pairs recorded in the lock file. Defaulting to
	// a multi-platform set keeps lock files usable across developer machines
	// and CI runners.
	Platforms []string `yaml:"platforms"`

	// Terraform selects the binary to drive, allowing OpenTofu via "tofu".
	Terraform struct {
		Binary string `yaml:"binary"`

		// FSMirror and NetMirror direct `providers lock` to a provider mirror,
		// for runners that cannot reach the public registries. At most one may
		// be set. A relative FSMirror is read from the repository root.
		FSMirror  string `yaml:"fs_mirror"`
		NetMirror string `yaml:"net_mirror"`
	} `yaml:"terraform"`

	Providers ProviderRules `yaml:"providers"`

	explicitBinary string
	path           string
}

// Path returns the file the configuration was read from, or "" for defaults.
func (c *Config) Path() string {
	if c == nil {
		return ""
	}
	return c.path
}

// Dir returns the directory holding the config file, or "" for defaults.
// Relative paths in the file (exclude globs, terraform.fs_mirror) are read
// from here.
func (c *Config) Dir() string {
	if c.Path() == "" {
		return ""
	}
	return filepath.Dir(c.path)
}

// ExplicitBinary returns the binary configured in .provlift.yaml, or "" if not explicitly set.
func (c *Config) ExplicitBinary() string {
	if c == nil {
		return ""
	}
	return c.explicitBinary
}

// ProviderRules holds per-provider policy.
type ProviderRules struct {
	// Ignore lists providers the tool must never touch, by selector
	// ("azurerm", "hashicorp/azurerm" or a full address).
	Ignore []string `yaml:"ignore"`

	// MaxLevel caps the upgrade level per provider selector, for example
	// pinning a provider to patch-only until a migration is scheduled.
	MaxLevel map[string]string `yaml:"max_level"`
}

// DefaultPlatforms covers the machines Terraform commonly runs on: Windows and
// Linux workstations, Intel and Apple Silicon Macs, and x86 and ARM Linux CI
// runners and servers such as AWS Graviton. A platform missing here gets its
// hash added by the first init that runs on it, which is the lock-file churn
// locking every platform up front avoids.
var DefaultPlatforms = []string{
	"windows_amd64",
	"linux_amd64",
	"linux_arm64",
	"darwin_amd64",
	"darwin_arm64",
}

// Default returns the configuration used when no file is present.
func Default() *Config {
	c := &Config{Platforms: append([]string(nil), DefaultPlatforms...)}
	c.Terraform.Binary = "terraform"
	return c
}

// Load reads FileName from root. A missing file yields defaults.
func Load(root string) (*Config, error) {
	path := filepath.Join(root, FileName)
	if _, err := os.Stat(path); errors.Is(err, fs.ErrNotExist) {
		return Default(), nil
	}
	return LoadFile(path)
}

// Locate finds the config file for a scan of root. An explicit path (from
// --config or PROVLIFT_CONFIG) wins and must exist. Otherwise FileName is
// looked for in root and then each parent, up to and including the enclosing
// git repository's root (a directory holding .git), or the filesystem root
// outside a repository. "" means there is none and defaults apply.
func Locate(root, explicit string) (string, error) {
	if explicit != "" {
		path, err := filepath.Abs(explicit)
		if err != nil {
			return "", fmt.Errorf("resolving config file %s: %w", explicit, err)
		}
		info, err := os.Stat(path)
		if err != nil {
			return "", fmt.Errorf("config file %s: %w", explicit, err)
		}
		if info.IsDir() {
			return "", fmt.Errorf("config file %s is a directory", explicit)
		}
		return path, nil
	}
	dir, err := filepath.Abs(root)
	if err != nil {
		return "", fmt.Errorf("resolving %s: %w", root, err)
	}
	for {
		path := filepath.Join(dir, FileName)
		if _, err := os.Stat(path); err == nil {
			return path, nil
		} else if !errors.Is(err, fs.ErrNotExist) {
			return "", fmt.Errorf("reading %s: %w", path, err)
		}
		if alt := filepath.Join(dir, AltFileName); exists(alt) {
			return "", fmt.Errorf("found %s; provlift reads %s", alt, FileName)
		}
		parent := filepath.Dir(dir)
		if exists(filepath.Join(dir, ".git")) || parent == dir {
			return "", nil
		}
		dir = parent
	}
}

func exists(path string) bool {
	_, err := os.Lstat(path)
	return err == nil
}

// LoadFile reads the config file at path; "" yields defaults.
func LoadFile(path string) (*Config, error) {
	if path == "" {
		return Default(), nil
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", path, err)
	}

	cfg := &Config{Platforms: append([]string(nil), DefaultPlatforms...)}
	// KnownFields rejects typos rather than silently ignoring a misspelled key
	// that would otherwise disable a safety rule.
	dec := yaml.NewDecoder(strings.NewReader(string(raw)))
	dec.KnownFields(true)
	// An empty or comment-only file holds no document and means defaults.
	if err := dec.Decode(cfg); err != nil && !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("parsing %s: %w", path, err)
	}
	// Only the first document is decoded, so a second one would be ignored
	// as silently as an unknown key.
	if err := dec.Decode(&yaml.Node{}); !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("parsing %s: only one YAML document is supported", path)
	}

	if len(cfg.Platforms) == 0 {
		cfg.Platforms = append([]string(nil), DefaultPlatforms...)
	}
	cfg.explicitBinary = cfg.Terraform.Binary
	if cfg.Terraform.Binary == "" {
		cfg.Terraform.Binary = "terraform"
	}
	if err := cfg.validate(); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	cfg.path = path
	return cfg, nil
}

// ExcludesFor returns the exclude globs, which are written relative to the
// config file's directory, rewritten relative to root, the directory being
// scanned. root is normally that directory or below it. A glob that can only
// match outside root is dropped; one whose leading segments match root's path
// keeps the rest. A "**" segment can stand for any part of root's path, so the
// glob from there on applies as is.
func (c *Config) ExcludesFor(root string) []string {
	dir := c.Dir()
	if dir == "" || len(c.Exclude) == 0 {
		return append([]string(nil), c.Exclude...)
	}
	rel, err := filepath.Rel(dir, root)
	if err != nil || rel == "." {
		return append([]string(nil), c.Exclude...)
	}
	rel = filepath.ToSlash(rel)
	if rel == ".." || strings.HasPrefix(rel, "../") {
		// An explicit --config outside the scanned tree: there is no common
		// base, so the globs are read relative to root as before.
		return append([]string(nil), c.Exclude...)
	}
	prefix := strings.Split(rel, "/")
	var out []string
	for _, glob := range c.Exclude {
		if g, ok := rebase(strings.TrimSuffix(filepath.ToSlash(glob), "/"), prefix); ok {
			out = append(out, g)
		}
	}
	return out
}

func rebase(glob string, prefix []string) (string, bool) {
	segments := strings.Split(glob, "/")
	for i, seg := range segments {
		if seg == "**" {
			return strings.Join(segments[i:], "/"), true
		}
		if i == len(prefix) {
			return strings.Join(segments[i:], "/"), true
		}
		if matched, err := doublestar.Match(seg, prefix[i]); err != nil || !matched {
			return "", false
		}
	}
	// The glob names root itself or one of its parents, so the whole scan is
	// under an excluded directory.
	return "**", true
}

func (c *Config) validate() error {
	if c.Terraform.Binary != "terraform" && c.Terraform.Binary != "tofu" {
		return fmt.Errorf("terraform.binary must be terraform or tofu; use --terraform-binary for an explicit executable")
	}
	if c.Terraform.FSMirror != "" && c.Terraform.NetMirror != "" {
		return fmt.Errorf("terraform.fs_mirror and terraform.net_mirror cannot both be set; providers lock accepts a single mirror")
	}
	if c.Terraform.NetMirror != "" {
		if err := CheckNetMirror(c.Terraform.NetMirror); err != nil {
			return fmt.Errorf("terraform.net_mirror: %w", err)
		}
	}
	for _, selector := range c.Providers.Ignore {
		if _, err := model.ParseAddress(selector); err != nil {
			return fmt.Errorf("invalid providers.ignore selector %q: %w", selector, err)
		}
	}
	for _, p := range c.Platforms {
		if !strings.Contains(p, "_") {
			return fmt.Errorf("platform %q must be in os_arch form, e.g. linux_amd64", p)
		}
	}
	for selector, level := range c.Providers.MaxLevel {
		if _, err := model.ParseAddress(selector); err != nil {
			return fmt.Errorf("invalid providers.max_level selector %q: %w", selector, err)
		}
		switch level {
		case "patch", "minor", "major", "none":
		default:
			return fmt.Errorf("providers.max_level[%q] is %q; want patch, minor, major or none", selector, level)
		}
	}
	return nil
}

// CheckNetMirror reports whether u can serve the provider network mirror
// protocol, which Terraform and OpenTofu only speak over HTTPS.
func CheckNetMirror(u string) error {
	parsed, err := url.Parse(u)
	if err != nil {
		return fmt.Errorf("%q is not a valid URL: %w", u, err)
	}
	if parsed.Scheme != "https" || parsed.Host == "" {
		return fmt.Errorf("%q must be an https:// URL", u)
	}
	return nil
}

// IsIgnored reports whether addr is excluded by policy.
func (c *Config) IsIgnored(addr model.Address) bool {
	for _, selector := range c.Providers.Ignore {
		if model.MatchesDenySelector(addr, selector) {
			return true
		}
	}
	return false
}

// MaxLevelFor returns the configured cap for addr and whether one exists.
func (c *Config) MaxLevelFor(addr model.Address) (string, bool) {
	best := ""
	bestSpecificity := -1
	for selector, level := range c.Providers.MaxLevel {
		if level == "none" && model.MatchesDenySelector(addr, selector) {
			return "none", true
		}
		if model.MatchesSelector(addr, selector) {
			specificity := strings.Count(selector, "/")
			if specificity > bestSpecificity {
				best = level
				bestSpecificity = specificity
			}
		}
	}
	return best, bestSpecificity >= 0
}
