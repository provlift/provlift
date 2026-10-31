package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/provlift/provlift/internal/config"
)

func TestUpgradeNeverWritesLockFileItself(t *testing.T) {
	// Lock content belongs to Terraform. Without a Terraform run the lock must
	// be byte-identical, and after one it must hold exactly what Terraform
	// wrote, with nothing added or rewritten by the CLI afterwards.
	tests := []struct {
		name      string
		args      []string
		terraform bool
		wantErr   bool
	}{
		{"dry run", []string{"--dry-run"}, false, false},
		{"constraint rewrite with --skip-init", []string{"--skip-init"}, false, false},
		{"refused major", nil, false, true},
		{"full run", nil, true, false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t, nil)
			logPath := useFakeTerraform(t, h)
			snapshot := filepath.Join(t.TempDir(), "written-by-terraform.hcl")
			t.Setenv(fakeLockSnapshot, snapshot)
			lockPath := filepath.Join(h.dir, ".terraform.lock.hcl")

			arguments := []string{"--provider", "azurerm", "--to", "5.6.0", "--level", "major"}
			if !tc.wantErr {
				arguments = append(arguments, "--allow-major")
			}
			err := h.run(t, newUpgradeCommand(h.opts), append(arguments, tc.args...)...)
			if (err != nil) != tc.wantErr {
				t.Fatalf("upgrade error = %v, want error: %t", err, tc.wantErr)
			}

			got, err := os.ReadFile(lockPath)
			if err != nil {
				t.Fatal(err)
			}
			if !tc.terraform {
				if string(got) != fixtureLock {
					t.Errorf("lock changed without Terraform running:\n%s", got)
				}
				if calls := readInvocations(t, logPath); len(calls) != 0 {
					t.Errorf("Terraform ran unexpectedly:\n%s", strings.Join(calls, "\n"))
				}
				return
			}

			written, err := os.ReadFile(snapshot)
			if err != nil {
				t.Fatalf("Terraform never wrote the lock: %v", err)
			}
			if string(got) == fixtureLock {
				t.Fatal("expected Terraform to have moved the lock")
			}
			if string(got) != string(written) {
				t.Errorf("lock differs from what Terraform wrote:\ngot:\n%s\nTerraform wrote:\n%s", got, written)
			}
		})
	}
}

func TestUpgradeLocksConfiguredPlatforms(t *testing.T) {
	// providers lock must receive each platform once, taken from --platforms
	// when given, otherwise from .provlift.yaml, otherwise the defaults.
	tests := []struct {
		name   string
		config string
		flag   string
		want   []string
	}{
		{"defaults", "", "", config.DefaultPlatforms},
		{"repository configuration", "platforms:\n  - linux_amd64\n  - darwin_arm64\n", "", []string{"linux_amd64", "darwin_arm64"}},
		{"flag overrides configuration", "platforms:\n  - linux_amd64\n  - darwin_arm64\n", "windows_amd64", []string{"windows_amd64"}},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			files := map[string]string{}
			if tc.config != "" {
				files[".provlift.yaml"] = tc.config
			}
			h := newHarness(t, files)
			logPath := useFakeTerraform(t, h)

			arguments := []string{"--provider", "azurerm", "--to", "5.6.0", "--level", "major", "--allow-major"}
			if tc.flag != "" {
				arguments = append(arguments, "--platforms", tc.flag)
			}
			if err := h.run(t, newUpgradeCommand(h.opts), arguments...); err != nil {
				t.Fatalf("upgrade: %v", err)
			}

			calls := readInvocations(t, logPath)
			at := indexOfCall(calls, "providers lock")
			if at < 0 {
				t.Fatalf("providers lock was never run:\n%s", strings.Join(calls, "\n"))
			}
			var got []string
			for _, argument := range strings.Fields(calls[at]) {
				if platform, ok := strings.CutPrefix(argument, "-platform="); ok {
					got = append(got, platform)
				}
			}
			if strings.Join(got, ",") != strings.Join(tc.want, ",") {
				t.Errorf("providers lock platforms = %v, want %v", got, tc.want)
			}
		})
	}
}
