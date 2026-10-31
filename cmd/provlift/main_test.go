package main

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime/debug"
	"strings"
	"testing"
)

// processModeEnv makes the test binary act as provlift itself, or as a
// Terraform that fails every command, so exit statuses can be observed on a
// real process.
const processModeEnv = "PROVLIFT_MAIN_TEST_MODE"

func TestMain(m *testing.M) {
	switch os.Getenv(processModeEnv) {
	case "provlift":
		// Terraform started by this process is the failing impersonation.
		os.Setenv(processModeEnv, "terraform")
		main()
		os.Exit(0)
	case "terraform":
		os.Exit(1)
	}
	os.Exit(m.Run())
}

func TestVersionFromBuildInfo(t *testing.T) {
	tests := []struct {
		name    string
		stamped string
		info    *debug.BuildInfo
		want    string
	}{
		{"tagged go install", "dev", &debug.BuildInfo{Main: debug.Module{Version: "v0.0.2"}}, "v0.0.2"},
		{"release stamp takes precedence", "v0.0.3", &debug.BuildInfo{Main: debug.Module{Version: "v0.0.2"}}, "v0.0.3"},
		{"local build", "dev", &debug.BuildInfo{Main: debug.Module{Version: "(devel)"}}, "dev"},
		{"no build metadata", "dev", nil, "dev"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := versionFromBuildInfo(tc.stamped, tc.info); got != tc.want {
				t.Errorf("versionFromBuildInfo(%q) = %q, want %q", tc.stamped, got, tc.want)
			}
		})
	}
}

func TestProcessExitCodes(t *testing.T) {
	// Two roots locking the same provider at different versions: drift to
	// report, and modules for validate to run Terraform in. Nothing here
	// needs the network.
	repo := t.TempDir()
	for dir, locked := range map[string]string{"envs/a": "4.65.0", "envs/b": "4.81.0"} {
		files := map[string]string{
			"main.tf":             "terraform {\n  required_providers {\n    azurerm = {\n      source  = \"hashicorp/azurerm\"\n      version = \"~> 4.0\"\n    }\n  }\n}\n",
			".terraform.lock.hcl": "provider \"registry.terraform.io/hashicorp/azurerm\" {\n  version = \"" + locked + "\"\n}\n",
		}
		for name, content := range files {
			path := filepath.Join(repo, filepath.FromSlash(dir), name)
			if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
				t.Fatal(err)
			}
		}
	}

	for _, scenario := range []struct {
		name      string
		arguments []string
		wantCode  int
		wantError bool
	}{
		{"success", []string{"version"}, 0, false},
		{"error", []string{"list", "--no-such-flag"}, 1, true},
		{"work available", []string{"-C", repo, "drift", "--exit-code"}, 2, false},
		{"validation failed", []string{"-C", repo, "validate", "--terraform-binary", os.Args[0]}, 3, true},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			cmd := exec.Command(os.Args[0], scenario.arguments...)
			cmd.Env = append(os.Environ(), processModeEnv+"=provlift")
			var stderr bytes.Buffer
			cmd.Stderr = &stderr
			err := cmd.Run()

			code := 0
			var exit *exec.ExitError
			if errors.As(err, &exit) {
				code = exit.ExitCode()
			} else if err != nil {
				t.Fatal(err)
			}
			if code != scenario.wantCode {
				t.Fatalf("exit code = %d, want %d\n%s", code, scenario.wantCode, stderr.String())
			}
			// Exit 2 signals work, not failure, so it carries no error line.
			if got := strings.Contains(stderr.String(), "Error:"); got != scenario.wantError {
				t.Errorf("stderr reports an error = %t, want %t\n%s", got, scenario.wantError, stderr.String())
			}
		})
	}
}
