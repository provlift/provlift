package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

type releaseStep struct {
	ID   string         `yaml:"id"`
	Name string         `yaml:"name"`
	Run  string         `yaml:"run"`
	Uses string         `yaml:"uses"`
	With map[string]any `yaml:"with"`
}

type releaseWorkflow struct {
	On struct {
		Push struct {
			Tags []string `yaml:"tags"`
		} `yaml:"push"`
	} `yaml:"on"`
	Jobs map[string]struct {
		Needs any           `yaml:"needs"`
		Steps []releaseStep `yaml:"steps"`
	} `yaml:"jobs"`
}

func readReleaseWorkflow(t *testing.T) releaseWorkflow {
	t.Helper()
	content, err := os.ReadFile(filepath.Join("..", "..", ".github", "workflows", "release.yml"))
	if err != nil {
		t.Fatal(err)
	}
	var workflow releaseWorkflow
	if err := yaml.Unmarshal(content, &workflow); err != nil {
		t.Fatal(err)
	}
	return workflow
}

// TestReleasePipelineOrder pins the shape of the release: a v* tag is verified,
// then built into archives, then checksummed, then the built binary must
// report the tag, and only then is anything published.
func TestReleasePipelineOrder(t *testing.T) {
	workflow := readReleaseWorkflow(t)
	if !slices.Contains(workflow.On.Push.Tags, "v*") {
		t.Fatalf("release must run on v* tags, got %v", workflow.On.Push.Tags)
	}
	verify := workflow.Jobs["verify"].Steps
	if slices.IndexFunc(verify, func(s releaseStep) bool { return strings.Contains(s.Run, "go test") }) < 0 ||
		slices.IndexFunc(verify, func(s releaseStep) bool { return strings.Contains(s.Run, "is not a semantic version") }) < 0 {
		t.Fatal("verify job must re-run the tests and reject a non-semver tag")
	}
	if needs, _ := workflow.Jobs["release"].Needs.(string); needs != "verify" {
		t.Fatalf("release must wait for verify, needs=%v", workflow.Jobs["release"].Needs)
	}

	steps := workflow.Jobs["release"].Steps
	build := slices.IndexFunc(steps, func(s releaseStep) bool { return strings.Contains(s.Run, "go build") })
	checksums := slices.IndexFunc(steps, func(s releaseStep) bool { return strings.Contains(s.Run, "> checksums.txt") })
	confirm := slices.IndexFunc(steps, func(s releaseStep) bool {
		return strings.Contains(s.Run, "version)") && strings.Contains(s.Run, `grep -q "${GITHUB_REF_NAME}"`)
	})
	publish := slices.IndexFunc(steps, func(s releaseStep) bool { return strings.HasPrefix(s.Uses, "softprops/action-gh-release@") })
	if build < 0 || checksums < build || confirm <= checksums || publish <= confirm {
		t.Fatalf("release steps out of order: build=%d checksums=%d confirm=%d publish=%d", build, checksums, confirm, publish)
	}
	if !strings.Contains(steps[build].Run, `version="${GITHUB_REF_NAME}"`) {
		t.Fatal("binaries must be stamped with the tag being released")
	}
	if !strings.Contains(steps[checksums].Run, "sha256sum ./*.tar.gz ./*.zip > checksums.txt") {
		t.Fatal("checksums must cover every published archive")
	}
	files, _ := steps[publish].With["files"].(string)
	for _, want := range []string{"dist/*.tar.gz", "dist/*.zip", "dist/checksums.txt"} {
		if !strings.Contains(files, want) {
			t.Fatalf("release does not publish %s: %q", want, files)
		}
	}
}

// TestReleaseLdflagsStampTheVersion builds the binary with the exact linker
// flags the release uses and checks it reports the tag, so renaming a stamped
// variable cannot silently ship binaries that say "dev".
func TestReleaseLdflagsStampTheVersion(t *testing.T) {
	if testing.Short() {
		t.Skip("builds the binary")
	}
	goBinary, err := exec.LookPath("go")
	if err != nil {
		t.Skip("go toolchain not on PATH")
	}
	workflow := readReleaseWorkflow(t)
	steps := workflow.Jobs["release"].Steps
	build := slices.IndexFunc(steps, func(s releaseStep) bool { return strings.Contains(s.Run, "go build") })
	if build < 0 {
		t.Fatal("release has no build step")
	}
	match := regexp.MustCompile(`-ldflags "([^"]+)"`).FindStringSubmatch(steps[build].Run)
	if match == nil {
		t.Fatal("release build passes no -ldflags")
	}
	const tag = "v9.8.7-rc.1"
	ldflags := strings.NewReplacer("${version}", tag, "${commit}", "0123abc", "${date}", "2026-01-02T03:04:05Z").Replace(match[1])
	if strings.Contains(ldflags, "$") {
		t.Fatalf("unexpected shell variable in release ldflags: %s", ldflags)
	}

	binary := filepath.Join(t.TempDir(), "provlift")
	if runtime.GOOS == "windows" {
		binary += ".exe"
	}
	command := exec.Command(goBinary, "build", "-trimpath", "-ldflags", ldflags, "-o", binary, ".")
	command.Env = append(os.Environ(), "CGO_ENABLED=0")
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("go build with release ldflags failed: %v\n%s", err, output)
	}
	output, err := exec.Command(binary, "version").CombinedOutput()
	if err != nil {
		t.Fatalf("provlift version failed: %v\n%s", err, output)
	}
	for _, want := range []string{tag, "0123abc", "2026-01-02T03:04:05Z"} {
		if !strings.Contains(string(output), want) {
			t.Fatalf("binary does not report %s: %s", want, output)
		}
	}
}
