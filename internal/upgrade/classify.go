package upgrade

import (
	"fmt"

	version "github.com/hashicorp/go-version"
)

// Level classifies the distance between two provider versions.
type Level int

const (
	// LevelNone means the candidate is not an upgrade.
	LevelNone Level = iota
	LevelPatch
	LevelMinor
	LevelMajor
)

// ParseLevel converts a CLI flag value to a Level.
func ParseLevel(s string) (Level, error) {
	switch s {
	case "patch":
		return LevelPatch, nil
	case "minor":
		return LevelMinor, nil
	case "major":
		return LevelMajor, nil
	default:
		return LevelNone, fmt.Errorf("unknown upgrade level %q (want patch, minor or major)", s)
	}
}

func (l Level) String() string {
	switch l {
	case LevelPatch:
		return "patch"
	case LevelMinor:
		return "minor"
	case LevelMajor:
		return "major"
	default:
		return "none"
	}
}

// MarshalText makes Level render as a readable string in JSON output.
func (l Level) MarshalText() ([]byte, error) { return []byte(l.String()), nil }

// Allows reports whether an upgrade of level other is within the budget the
// user asked for: --level minor permits patch and minor, but not major.
func (l Level) Allows(other Level) bool { return other <= l && other != LevelNone }

// Classify compares two versions by their major/minor/patch segments.
//
// Build metadata is ignored, matching semver. A candidate that is equal to or
// older than the current version returns LevelNone; downgrades are never
// treated as upgrades.
func Classify(from, to *version.Version) Level {
	if from == nil || to == nil {
		return LevelNone
	}
	if !to.GreaterThan(from) {
		return LevelNone
	}

	f, t := segments(from), segments(to)
	switch {
	case t[0] != f[0]:
		return LevelMajor
	case t[1] != f[1]:
		return LevelMinor
	default:
		// Differing patch numbers, or the same numbers with a prerelease
		// difference such as 1.2.3-rc1 -> 1.2.3.
		return LevelPatch
	}
}

// IsBreaking reports whether an upgrade may contain breaking changes under
// semver. Below 1.0.0 the minor segment carries breaking changes, which is
// exactly where a naive major-only check would let a break through.
func IsBreaking(from, to *version.Version) bool {
	if from == nil || to == nil {
		return false
	}
	f, t := segments(from), segments(to)
	if t[0] != f[0] {
		return true
	}
	return f[0] == 0 && t[1] != f[1]
}

// segments returns exactly three numeric segments, padding short versions such
// as "6.0" with zeroes.
func segments(v *version.Version) [3]int {
	var out [3]int
	for i, s := range v.Segments() {
		if i > 2 {
			break
		}
		out[i] = s
	}
	return out
}

// IsPrerelease reports whether v carries a prerelease tag such as -rc1. These
// are excluded from upgrade candidates unless explicitly requested.
func IsPrerelease(v *version.Version) bool { return v.Prerelease() != "" }
