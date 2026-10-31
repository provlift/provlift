// Package config loads optional repository-level settings from .provlift.yaml.
//
// Every field is optional; a repository with no config file behaves sensibly.
package config

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/provlift/provlift/internal/model"
)

// FileName is the config file looked for in the scan root.
const FileName = ".provlift.yaml"

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
	} `yaml:"terraform"`

	Providers ProviderRules `yaml:"providers"`
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

// DefaultPlatforms covers the machines this team builds and runs on.
var DefaultPlatforms = []string{
	"windows_amd64",
	"linux_amd64",
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

	raw, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return Default(), nil
	}
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", path, err)
	}

	cfg := Default()
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
	if cfg.Terraform.Binary == "" {
		cfg.Terraform.Binary = "terraform"
	}
	if err := cfg.validate(); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return cfg, nil
}

func (c *Config) validate() error {
	if c.Terraform.Binary != "terraform" && c.Terraform.Binary != "tofu" {
		return fmt.Errorf("terraform.binary must be terraform or tofu; use --terraform-binary for an explicit executable")
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
