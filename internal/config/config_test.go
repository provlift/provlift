package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/provlift/provlift/internal/model"
)

func writeConfig(t *testing.T, content string) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, FileName), []byte(content), 0o644); err != nil {
		t.Fatalf("writing config: %v", err)
	}
	return dir
}

func TestLoadMissingFileReturnsDefaults(t *testing.T) {
	cfg, err := Load(t.TempDir())
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	if cfg.Terraform.Binary != "terraform" {
		t.Errorf("Terraform.Binary = %q, want terraform", cfg.Terraform.Binary)
	}
	if len(cfg.Platforms) != len(DefaultPlatforms) {
		t.Errorf("Platforms = %v, want the default set", cfg.Platforms)
	}
	if len(cfg.Exclude) != 0 {
		t.Errorf("Exclude = %v, want empty", cfg.Exclude)
	}
}

func TestLoadValidConfig(t *testing.T) {
	dir := writeConfig(t, `
exclude:
  - archive
  - "**/examples"

platforms:
  - linux_amd64
  - darwin_arm64

terraform:
  binary: tofu

providers:
  ignore:
    - hashicorp/azuread
  max_level:
    hashicorp/azurerm: minor
`)

	cfg, err := Load(dir)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	if cfg.Terraform.Binary != "tofu" {
		t.Errorf("Terraform.Binary = %q, want tofu", cfg.Terraform.Binary)
	}
	if len(cfg.Platforms) != 2 {
		t.Errorf("Platforms = %v, want 2 entries", cfg.Platforms)
	}
	if len(cfg.Exclude) != 2 {
		t.Errorf("Exclude = %v, want 2 entries", cfg.Exclude)
	}
}

func TestLoadAppliesDefaultsForOmittedKeys(t *testing.T) {
	dir := writeConfig(t, "exclude:\n  - archive\n")

	cfg, err := Load(dir)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	if len(cfg.Platforms) != len(DefaultPlatforms) {
		t.Errorf("Platforms = %v, want defaults when omitted", cfg.Platforms)
	}
	if cfg.Terraform.Binary != "terraform" {
		t.Errorf("Terraform.Binary = %q, want the default", cfg.Terraform.Binary)
	}
}

func TestLoadEmptyFileReturnsDefaults(t *testing.T) {
	for _, content := range []string{"", "# settings are added as they are needed\n"} {
		cfg, err := Load(writeConfig(t, content))
		if err != nil {
			t.Fatalf("Load(%q): %v", content, err)
		}
		if cfg.Terraform.Binary != "terraform" || len(cfg.Platforms) != len(DefaultPlatforms) {
			t.Errorf("Load(%q) = %+v, want defaults", content, cfg)
		}
	}
}

func TestLoadRejectsBadConfig(t *testing.T) {
	// A typo must fail loudly: silently ignoring an unknown key could disable a
	// safety rule the author believed was active.
	tests := []struct {
		name        string
		content     string
		wantMessage string
	}{
		{
			name:        "unknown top-level key",
			content:     "excludes:\n  - archive\n",
			wantMessage: "excludes",
		},
		{
			name:        "misspelled nested key",
			content:     "providers:\n  ignored:\n    - azurerm\n",
			wantMessage: "ignored",
		},
		{
			name:        "typo in a second YAML document",
			content:     "exclude:\n  - archive\n---\nprovider:\n  ignore:\n    - azurerm\n",
			wantMessage: "only one YAML document",
		},
		{
			name:        "platform not in os_arch form",
			content:     "platforms:\n  - linux\n",
			wantMessage: "os_arch",
		},
		{
			name:        "invalid max_level value",
			content:     "providers:\n  max_level:\n    azurerm: enormous\n",
			wantMessage: "max_level",
		},
		{
			name:        "invalid ignore selector",
			content:     "providers:\n  ignore:\n    - bad/a/b/c\n",
			wantMessage: "invalid providers.ignore selector",
		},
		{
			name:        "invalid max_level selector",
			content:     "providers:\n  max_level:\n    bad/a/b/c: none\n",
			wantMessage: "invalid providers.max_level selector",
		},
		{
			name:        "malformed YAML",
			content:     "platforms: [unclosed\n",
			wantMessage: "parsing",
		},
		{
			name:        "repository cannot nominate an executable path",
			content:     "terraform:\n  binary: ../tools/terraform\n",
			wantMessage: "--terraform-binary",
		},
		{
			name:        "repository cannot nominate an arbitrary command",
			content:     "terraform:\n  binary: attacker\n",
			wantMessage: "terraform.binary",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			dir := writeConfig(t, tc.content)

			_, err := Load(dir)
			if err == nil {
				t.Fatal("expected an error")
			}
			if !strings.Contains(err.Error(), tc.wantMessage) {
				t.Errorf("error %q does not mention %q", err, tc.wantMessage)
			}
		})
	}
}

func TestIsIgnored(t *testing.T) {
	cfg := Default()
	cfg.Providers.Ignore = []string{"azuread", "integrations/github"}

	tests := []struct {
		source string
		want   bool
	}{
		{"hashicorp/azuread", true},
		{"integrations/github", true},
		{"hashicorp/azurerm", false},
		{"contoso/azuread", true},
		// A deny for integrations/github also covers other namespaces.
		{"hashicorp/github", true},
	}

	for _, tc := range tests {
		t.Run(tc.source, func(t *testing.T) {
			addr, err := model.ParseAddress(tc.source)
			if err != nil {
				t.Fatal(err)
			}
			if got := cfg.IsIgnored(addr); got != tc.want {
				t.Errorf("IsIgnored(%s) = %v, want %v", tc.source, got, tc.want)
			}
		})
	}
}

func TestMaxLevelFor(t *testing.T) {
	cfg := Default()
	cfg.Providers.MaxLevel = map[string]string{
		"hashicorp/azurerm": "minor",
		"azuread":           "none",
	}

	tests := []struct {
		source    string
		wantLevel string
		wantFound bool
	}{
		{"hashicorp/azurerm", "minor", true},
		{"hashicorp/azuread", "none", true},
		{"integrations/github", "", false},
	}

	for _, tc := range tests {
		t.Run(tc.source, func(t *testing.T) {
			addr, err := model.ParseAddress(tc.source)
			if err != nil {
				t.Fatal(err)
			}
			level, found := cfg.MaxLevelFor(addr)
			if found != tc.wantFound || level != tc.wantLevel {
				t.Errorf("MaxLevelFor(%s) = (%q, %v), want (%q, %v)",
					tc.source, level, found, tc.wantLevel, tc.wantFound)
			}
		})
	}
}

func TestMaxLevelForPrefersSpecificSelector(t *testing.T) {
	cfg := Default()
	cfg.Providers.MaxLevel = map[string]string{
		"azurerm":           "patch",
		"hashicorp/azurerm": "minor",
	}
	addr, err := model.ParseAddress("hashicorp/azurerm")
	if err != nil {
		t.Fatal(err)
	}
	if level, ok := cfg.MaxLevelFor(addr); !ok || level != "minor" {
		t.Fatalf("MaxLevelFor = (%q, %v), want (minor, true)", level, ok)
	}
}

func TestMaxLevelNoneOverridesSpecificAllow(t *testing.T) {
	cfg := Default()
	cfg.Providers.MaxLevel = map[string]string{
		"hashicorp/azurerm": "none",
		"contoso/azurerm":   "major",
	}
	for _, source := range []string{"hashicorp/azurerm", "contoso/azurerm"} {
		addr, err := model.ParseAddress(source)
		if err != nil {
			t.Fatal(err)
		}
		if level, ok := cfg.MaxLevelFor(addr); !ok || level != "none" {
			t.Errorf("MaxLevelFor(%s) = (%q, %v), want (none, true)", source, level, ok)
		}
	}
}

func TestMaxLevelAcceptsAllValidLevels(t *testing.T) {
	for _, level := range []string{"patch", "minor", "major", "none"} {
		dir := writeConfig(t, "providers:\n  max_level:\n    azurerm: "+level+"\n")
		if _, err := Load(dir); err != nil {
			t.Errorf("max_level %q was rejected: %v", level, err)
		}
	}
}
