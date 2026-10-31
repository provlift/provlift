package upgrade

import (
	"testing"

	version "github.com/hashicorp/go-version"
)

func mustVersion(t *testing.T, s string) *version.Version {
	t.Helper()
	v, err := version.NewVersion(s)
	if err != nil {
		t.Fatalf("NewVersion(%q): %v", s, err)
	}
	return v
}

func TestClassify(t *testing.T) {
	tests := []struct {
		name string
		from string
		to   string
		want Level
	}{
		{"patch bump", "5.1.2", "5.1.3", LevelPatch},
		{"minor bump", "5.1.2", "5.2.0", LevelMinor},
		{"major bump", "5.1.2", "6.0.0", LevelMajor},
		{"minor bump skipping patch", "5.1.2", "5.4.9", LevelMinor},
		{"identical", "5.1.2", "5.1.2", LevelNone},
		{"downgrade is never an upgrade", "5.1.3", "5.1.2", LevelNone},
		{"major downgrade is never an upgrade", "6.0.0", "5.9.9", LevelNone},
		{"short version is padded", "6.0", "6.0.1", LevelPatch},
		{"prerelease to release is a patch", "1.2.3-rc1", "1.2.3", LevelPatch},
		{"zero major minor bump", "0.5.0", "0.6.0", LevelMinor},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := Classify(mustVersion(t, tc.from), mustVersion(t, tc.to))
			if got != tc.want {
				t.Errorf("Classify(%s, %s) = %s, want %s", tc.from, tc.to, got, tc.want)
			}
		})
	}
}

func TestIsBreaking(t *testing.T) {
	tests := []struct {
		from string
		to   string
		want bool
	}{
		{"5.1.2", "5.1.3", false},
		{"5.1.2", "5.2.0", false},
		{"5.1.2", "6.0.0", true},
		// Below 1.0.0 semver allows breaking changes in a minor bump, so a
		// major-only check would wrongly call this safe.
		{"0.5.0", "0.6.0", true},
		{"0.5.0", "0.5.1", false},
	}

	for _, tc := range tests {
		t.Run(tc.from+"->"+tc.to, func(t *testing.T) {
			got := IsBreaking(mustVersion(t, tc.from), mustVersion(t, tc.to))
			if got != tc.want {
				t.Errorf("IsBreaking(%s, %s) = %v, want %v", tc.from, tc.to, got, tc.want)
			}
		})
	}
}

func TestLevelAllows(t *testing.T) {
	tests := []struct {
		budget Level
		actual Level
		want   bool
	}{
		{LevelPatch, LevelPatch, true},
		{LevelPatch, LevelMinor, false},
		{LevelPatch, LevelMajor, false},
		{LevelMinor, LevelPatch, true},
		{LevelMinor, LevelMinor, true},
		{LevelMinor, LevelMajor, false},
		{LevelMajor, LevelMajor, true},
		{LevelMajor, LevelNone, false},
	}

	for _, tc := range tests {
		if got := tc.budget.Allows(tc.actual); got != tc.want {
			t.Errorf("%s.Allows(%s) = %v, want %v", tc.budget, tc.actual, got, tc.want)
		}
	}
}

func TestParseLevel(t *testing.T) {
	for _, s := range []string{"patch", "minor", "major"} {
		if _, err := ParseLevel(s); err != nil {
			t.Errorf("ParseLevel(%q) returned error: %v", s, err)
		}
	}
	if _, err := ParseLevel("gigantic"); err == nil {
		t.Error("ParseLevel(\"gigantic\") should have failed")
	}
}

func TestHighestRespectsLevel(t *testing.T) {
	candidates := []*version.Version{
		mustVersion(t, "5.1.2"),
		mustVersion(t, "5.1.7"),
		mustVersion(t, "5.4.0"),
		mustVersion(t, "6.2.0"),
	}
	current := mustVersion(t, "5.1.2")

	tests := []struct {
		level Level
		want  string
	}{
		{LevelPatch, "5.1.7"},
		{LevelMinor, "5.4.0"},
		{LevelMajor, "6.2.0"},
	}

	for _, tc := range tests {
		t.Run(tc.level.String(), func(t *testing.T) {
			got := Highest(candidates, current, tc.level)
			if got == nil || got.String() != tc.want {
				t.Errorf("Highest(level=%s) = %v, want %s", tc.level, got, tc.want)
			}
		})
	}
}

func TestHighestReturnsNilWhenCurrent(t *testing.T) {
	candidates := []*version.Version{mustVersion(t, "5.1.2")}
	if got := Highest(candidates, mustVersion(t, "5.1.2"), LevelMajor); got != nil {
		t.Errorf("Highest() = %v, want nil when already newest", got)
	}
}

func TestHighestSatisfying(t *testing.T) {
	candidates := []*version.Version{
		mustVersion(t, "6.0.0"),
		mustVersion(t, "6.9.9"),
		mustVersion(t, "7.1.0"),
	}
	got := HighestSatisfying(candidates, "~> 6.0")
	if got == nil || got.String() != "6.9.9" {
		t.Errorf("HighestSatisfying(~> 6.0) = %v, want 6.9.9", got)
	}
}
