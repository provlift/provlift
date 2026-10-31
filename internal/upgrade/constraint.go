package upgrade

import (
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"

	version "github.com/hashicorp/go-version"
)

// ErrUnsupportedConstraint is returned when a constraint is too complex to
// rewrite safely. The CLI surfaces this as an actionable message rather than
// guessing at the author's intent and silently widening the constraint.
var ErrUnsupportedConstraint = errors.New("unsupported version constraint shape")

// ErrDowngrade is returned when the requested version is below the constraint's
// lower bound.
var ErrDowngrade = errors.New("target version is below the existing lower bound")

// Shape is the structural form of a version constraint, used to preserve the
// author's original style when rewriting.
type Shape int

const (
	ShapeEmpty Shape = iota
	// ShapeExact pins one version: "6.0.1" or "= 6.0.1".
	ShapeExact
	// ShapePessimistic is the "~>" operator.
	ShapePessimistic
	// ShapeLowerBound is a single ">=" or ">" term.
	ShapeLowerBound
	// ShapeRange pairs a lower bound with an upper bound.
	ShapeRange
	// ShapeUnsupported covers "!=", multiple disjoint ranges and anything else
	// this tool declines to rewrite.
	ShapeUnsupported
)

func (s Shape) String() string {
	switch s {
	case ShapeEmpty:
		return "empty"
	case ShapeExact:
		return "exact"
	case ShapePessimistic:
		return "pessimistic"
	case ShapeLowerBound:
		return "lower-bound"
	case ShapeRange:
		return "range"
	default:
		return "unsupported"
	}
}

var termPattern = regexp.MustCompile(`^(>=|<=|!=|~>|>|<|=)?\s*(.+)$`)

type term struct {
	op      string
	version *version.Version
	// segments is the precision as written, which rewrites preserve. Terraform
	// reads a one-segment "~> 6" as "~> 6.0"; see pessimisticPrecision.
	segments int
}

func parseTerms(constraint string) ([]term, error) {
	var out []term
	for _, raw := range strings.Split(constraint, ",") {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			continue
		}

		m := termPattern.FindStringSubmatch(raw)
		if m == nil {
			return nil, fmt.Errorf("%w: cannot parse term %q", ErrUnsupportedConstraint, raw)
		}

		literal := strings.TrimSpace(m[2])
		v, err := version.NewVersion(literal)
		if err != nil {
			return nil, fmt.Errorf("%w: %q is not a version", ErrUnsupportedConstraint, literal)
		}

		op := m[1]
		if op == "" {
			op = "="
		}
		out = append(out, term{op: op, version: v, segments: countSegments(literal)})
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("%w: %q has no terms", ErrUnsupportedConstraint, constraint)
	}
	return out, nil
}

func countSegments(literal string) int {
	// Trim any prerelease/metadata suffix before counting dots.
	if i := strings.IndexAny(literal, "-+"); i >= 0 {
		literal = literal[:i]
	}
	n := strings.Count(literal, ".") + 1
	if n > 3 {
		n = 3
	}
	return n
}

// DetectShape classifies a constraint string.
func DetectShape(constraint string) Shape {
	if strings.TrimSpace(constraint) == "" {
		return ShapeEmpty
	}
	terms, err := parseTerms(constraint)
	if err != nil {
		return ShapeUnsupported
	}

	switch len(terms) {
	case 1:
		switch terms[0].op {
		case "=":
			return ShapeExact
		case "~>":
			return ShapePessimistic
		case ">=", ">":
			return ShapeLowerBound
		default:
			return ShapeUnsupported
		}
	case 2:
		var lower, upper []term
		for _, t := range terms {
			switch t.op {
			case ">=", ">":
				lower = append(lower, t)
			case "<", "<=":
				upper = append(upper, t)
			}
		}
		if len(lower) != 1 || len(upper) != 1 {
			return ShapeUnsupported
		}
		// A range whose bounds cross admits nothing. Moving it would replace
		// the author's intent with a guess, so it is refused like any other
		// shape the tool cannot reason about.
		lo, hi := lower[0], upper[0]
		if lo.version.GreaterThan(hi.version) ||
			(lo.version.Equal(hi.version) && (lo.op != ">=" || hi.op != "<=")) {
			return ShapeUnsupported
		}
		return ShapeRange
	default:
		return ShapeUnsupported
	}
}

// Satisfies follows Terraform's prerelease rule: only exact constraints select prereleases.
func Satisfies(constraint string, v *version.Version) (bool, error) {
	if strings.TrimSpace(constraint) == "" {
		return !IsPrerelease(v), nil
	}
	cs, err := newConstraint(constraint)
	if err != nil {
		return false, fmt.Errorf("%w: %q: %v", ErrUnsupportedConstraint, constraint, err)
	}
	if IsPrerelease(v) {
		for _, raw := range strings.Split(constraint, ",") {
			if DetectShape(raw) != ShapeExact {
				return false, nil
			}
		}
	}
	return cs.Check(v), nil
}

// newConstraint parses constraint the way Terraform reads it. go-version gives
// a one-segment "~> 6" no upper bound at all, whereas Terraform reads it as
// "~> 6.0", which is ">= 6.0, < 7.0". Every version.Constraints built from
// user text must come from here.
func newConstraint(constraint string) (version.Constraints, error) {
	terms := strings.Split(constraint, ",")
	for i, raw := range terms {
		m := termPattern.FindStringSubmatch(strings.TrimSpace(raw))
		if m == nil || m[1] != "~>" {
			continue
		}
		literal := strings.TrimSpace(m[2])
		if countSegments(literal) != 1 {
			continue
		}
		// Keep any prerelease or metadata suffix after the added segment.
		cut := len(literal)
		if j := strings.IndexAny(literal, "-+"); j >= 0 {
			cut = j
		}
		terms[i] = "~> " + literal[:cut] + ".0" + literal[cut:]
	}
	return version.NewConstraint(strings.Join(terms, ","))
}

// Rewrite is the outcome of reconciling a constraint with a target version.
type Rewrite struct {
	Old     string `json:"old"`
	New     string `json:"new"`
	Changed bool   `json:"changed"`
	Shape   string `json:"shape"`
	// Reason explains why the constraint was or was not modified, and is shown
	// verbatim in reports.
	Reason string `json:"reason"`
}

// RewriteConstraint reconciles an existing constraint with target.
//
// The guiding rule is minimum diff: if the existing constraint already permits
// target, it is left untouched and only the lock file moves. A constraint is
// only rewritten when it would otherwise block the upgrade, and the rewrite
// preserves the author's original operator style.
func RewriteConstraint(existing string, target *version.Version) (Rewrite, error) {
	return RewriteConstraintPinned(existing, target, false)
}

// RewriteConstraintPinned behaves like RewriteConstraint but, when pin is true,
// also adds a constraint to a requirement that declares none. That is a
// deliberate policy change rather than an upgrade, so it is opt-in.
func RewriteConstraintPinned(existing string, target *version.Version, pin bool) (Rewrite, error) {
	shape := DetectShape(existing)
	res := Rewrite{Old: existing, New: existing, Shape: shape.String()}
	if IsPrerelease(target) {
		if shape == ShapeEmpty && !pin {
			return res, fmt.Errorf("%w: Terraform prereleases require an exact pin; use --pin-unconstrained to add one", ErrUnsupportedConstraint)
		}
		if shape == ShapeUnsupported {
			return res, fmt.Errorf("%w: %q must be updated by hand", ErrUnsupportedConstraint, existing)
		}
		if allowed, err := Satisfies(existing, target); err == nil && allowed {
			res.Reason = fmt.Sprintf("constraint %q already allows %s", existing, target)
			return res, nil
		}
		if shape == ShapeLowerBound || shape == ShapeRange {
			terms, err := parseTerms(existing)
			if err != nil {
				return res, err
			}
			for _, term := range terms {
				if (term.op == ">=" || term.op == ">") && target.LessThan(term.version) {
					return res, fmt.Errorf("%w: lower bound is %s but the target is %s", ErrDowngrade, term.version, target)
				}
			}
		}
		res.New = "= " + target.String()
		if shape == ShapeExact && !strings.ContainsAny(existing, "=<>~") {
			res.New = target.String()
		}
		res.Changed = res.New != res.Old
		res.Reason = "Terraform requires an exact pin to select a prerelease"
		return res, nil
	}

	if shape == ShapeEmpty {
		if !pin {
			res.Reason = "no version constraint is declared; Terraform will accept any version"
			return res, nil
		}
		seg := segments(target)
		res.New = fmt.Sprintf("~> %d.%d", seg[0], seg[1])
		res.Changed = true
		res.Reason = "added a constraint to a previously unconstrained provider"
		return res, nil
	}

	ok, err := Satisfies(existing, target)
	if err != nil {
		return res, err
	}
	if ok {
		res.Reason = fmt.Sprintf("constraint %q already allows %s", existing, target)
		return res, nil
	}

	terms, err := parseTerms(existing)
	if err != nil {
		return res, err
	}

	switch shape {
	case ShapeExact:
		res.New = formatTerm(existing, terms[0].op, target, terms[0].segments)
		res.Reason = "exact pin moved to the target version"

	case ShapePessimistic:
		res.New = "~> " + pessimisticOperand(terms[0], target)
		res.Reason = "pessimistic constraint retargeted at the new version series"

	case ShapeLowerBound:
		// Reaching here means target is below the floor.
		return res, fmt.Errorf("%w: %q requires at least %s but the target is %s",
			ErrDowngrade, existing, terms[0].version, target)

	case ShapeRange:
		rewritten, err := rewriteRange(terms, target)
		if err != nil {
			return res, err
		}
		res.New = rewritten
		res.Reason = "range moved so the target version falls inside it"

	default:
		return res, fmt.Errorf("%w: %q must be updated by hand", ErrUnsupportedConstraint, existing)
	}

	res.Changed = res.New != res.Old
	return res, nil
}

// pessimisticOperand keeps the precision the author chose. A two-segment
// "~> 6.0" tracks a whole major series, so it becomes "~> 7.0" rather than
// narrowing to the target's minor. A one-segment "~> 6" means the same to
// Terraform and becomes "~> 7".
func pessimisticOperand(t term, target *version.Version) string {
	seg := segments(target)
	switch t.segments {
	case 1:
		return fmt.Sprintf("%d", seg[0])
	case 2:
		return fmt.Sprintf("%d.0", seg[0])
	default:
		return fmt.Sprintf("%d.%d.%d", seg[0], seg[1], seg[2])
	}
}

func rewriteRange(terms []term, target *version.Version) (string, error) {
	var lower, upper term
	for _, t := range terms {
		switch t.op {
		case ">=", ">":
			lower = t
		case "<", "<=":
			upper = t
		}
	}

	if target.LessThan(lower.version) {
		return "", fmt.Errorf("%w: lower bound is %s but the target is %s",
			ErrDowngrade, lower.version, target)
	}

	lowerOperator := lower.op
	if lowerOperator == ">" {
		lowerOperator = ">="
	}
	newLower := fmt.Sprintf("%s %s", lowerOperator, formatVersion(target, lower.segments))

	var newUpper string
	if upper.op == "<=" {
		newUpper = fmt.Sprintf("<= %s", target.String())
	} else {
		newUpper = fmt.Sprintf("< %s", formatVersion(NextMajor(target), upper.segments))
	}

	return newLower + ", " + newUpper, nil
}

func formatTerm(original, op string, v *version.Version, segs int) string {
	// Preserve a bare "6.0.1" pin rather than turning it into "= 6.0.1".
	if op == "=" && !strings.ContainsAny(original, "=<>~") {
		return v.String()
	}
	if op == "=" {
		return op + " " + v.String()
	}
	return op + " " + formatVersion(v, segs)
}

func formatVersion(v *version.Version, segs int) string {
	if IsPrerelease(v) {
		return v.String()
	}
	s := segments(v)
	switch segs {
	case 1:
		return fmt.Sprintf("%d", s[0])
	case 2:
		return fmt.Sprintf("%d.%d", s[0], s[1])
	default:
		return fmt.Sprintf("%d.%d.%d", s[0], s[1], s[2])
	}
}

// NextMajor returns the first version of the next major series.
func NextMajor(v *version.Version) *version.Version {
	s := segments(v)
	next, _ := version.NewVersion(fmt.Sprintf("%d.0.0", s[0]+1))
	return next
}

// RaiseConstraintFloor rewrites the lower bound of a constraint to target, even
// when the existing constraint would already admit it.
//
// A shared module publishes a supported floor rather than a pinned version, so
// "upgrading" one means declaring a newer minimum that has actually been
// tested. That changes the module's contract with its consumers, which is why
// it is opt-in rather than part of the normal minimum-diff behaviour.
func RaiseConstraintFloor(existing string, target *version.Version) (Rewrite, error) {
	shape := DetectShape(existing)
	res := Rewrite{Old: existing, New: existing, Shape: shape.String()}
	if IsPrerelease(target) && shape != ShapeExact {
		return res, fmt.Errorf("%w: Terraform requires exact prerelease pins, not supported floors; use --to %s without --raise-floor", ErrUnsupportedConstraint, target)
	}

	if shape == ShapeEmpty {
		res.New = ">= " + target.String()
		res.Changed = true
		res.Reason = "declared a supported minimum where none existed"
		return res, nil
	}

	terms, err := parseTerms(existing)
	if err != nil {
		return res, err
	}

	switch shape {
	case ShapeExact:
		res.New = formatTerm(existing, terms[0].op, target, terms[0].segments)
		res.Reason = "exact pin moved to the target version"

	case ShapeLowerBound:
		op := terms[0].op
		if op == ">" {
			op = ">="
		}
		res.New = op + " " + formatVersion(target, terms[0].segments)
		res.Reason = "supported minimum raised to the target version"

	case ShapePessimistic:
		if seg := segments(target); terms[0].segments == 1 && (seg[1] != 0 || seg[2] != 0) {
			// "~> 6" admits all of 6.x, but its one segment can only name a
			// whole major version as the floor: "~> 7" for 7.1.0 would still
			// admit 7.0.0, below the requested minimum.
			res.Reason = fmt.Sprintf(
				"%q can only declare a whole major version as its floor, not %s; use an explicit range such as %q",
				existing, target, fmt.Sprintf(">= %s, < %d.0.0", target, seg[0]+1))
			return res, nil
		}
		if terms[0].segments < 3 {
			current := segments(terms[0].version)
			targetSegments := segments(target)
			if current[0] == targetSegments[0] && current[1] == targetSegments[1] {
				seg := segments(target)
				res.Reason = fmt.Sprintf(
					"%q cannot declare a floor of %s without also narrowing its ceiling; use an explicit range such as %q",
					existing, target, fmt.Sprintf(">= %s, < %d.0.0", target, seg[0]+1))
				return res, nil
			}
		}
		res.New = "~> " + formatVersion(target, terms[0].segments)
		res.Reason = "pessimistic constraint retargeted at the new version"

	case ShapeRange:
		rewritten, err := raiseRangeFloor(terms, target)
		if err != nil {
			return res, err
		}
		res.New = rewritten
		res.Reason = "range floor raised to the target version"

	default:
		return res, fmt.Errorf("%w: %q must be updated by hand", ErrUnsupportedConstraint, existing)
	}

	res.Changed = res.New != res.Old
	if !res.Changed {
		if shape == ShapePessimistic && terms[0].segments != 1 {
			seg := segments(target)
			// "~> 1.2" admits all of 1.x. Rewriting it to "~> 1.2.34" would
			// also drop the ceiling to < 1.3.0, which is a different promise.
			res.Reason = fmt.Sprintf(
				"%q cannot declare a floor of %s without also narrowing its ceiling; use an explicit range such as %q",
				existing, target, fmt.Sprintf(">= %s, < %d.0.0", target, seg[0]+1))
		} else {
			res.Reason = fmt.Sprintf("floor is already %s", target)
		}
	}
	return res, nil
}

// raiseRangeFloor moves the lower bound up, and pushes the upper bound out only
// when the target would otherwise fall outside it.
func raiseRangeFloor(terms []term, target *version.Version) (string, error) {
	var lower, upper term
	for _, t := range terms {
		switch t.op {
		case ">=", ">":
			lower = t
		case "<", "<=":
			upper = t
		}
	}

	lowerOperator := lower.op
	if lowerOperator == ">" {
		lowerOperator = ">="
	}
	newLower := fmt.Sprintf("%s %s", lowerOperator, formatVersion(target, lower.segments))

	newUpper := fmt.Sprintf("%s %s", upper.op, formatVersion(upper.version, upper.segments))
	if !target.LessThan(upper.version) {
		if upper.op == "<=" {
			newUpper = fmt.Sprintf("<= %s", target.String())
		} else {
			newUpper = fmt.Sprintf("< %s", formatVersion(NextMajor(target), upper.segments))
		}
	}

	return newLower + ", " + newUpper, nil
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

// normalTerm is one term of a constraint in two forms: as the author spelled
// it, and as a key that is identical for every spelling Terraform reads the
// same way.
type normalTerm struct {
	text    string
	key     string
	version *version.Version
}

// normalizeTerms splits a constraint into its terms. The key compares terms by
// meaning rather than spelling, so ">=4.16" matches ">= 4.16.0", "6.0.1"
// matches "= 6.0.1", and "~> 1" matches "~> 1.0". That matters because
// Terraform rewrites constraints into its own spelling in the lock file.
func normalizeTerms(constraint string) []normalTerm {
	var out []normalTerm
	for _, raw := range strings.Split(constraint, ",") {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			continue
		}
		m := termPattern.FindStringSubmatch(raw)
		if m == nil {
			out = append(out, normalTerm{text: raw, key: raw})
			continue
		}

		op := m[1]
		if op == "" {
			op = "="
		}
		literal := strings.TrimSpace(m[2])
		t := normalTerm{text: op + " " + literal, key: op + " " + literal}
		if v, err := version.NewVersion(literal); err == nil {
			t.version = v
			t.key = op + " " + canonicalOperand(op, v, countSegments(literal))
		}
		out = append(out, t)
	}
	return out
}

// canonicalOperand renders v so that operands Terraform treats as equal
// render identically. Only "~>" depends on how many segments were written:
// "~> 1.2" and "~> 1.2.0" admit different versions.
func canonicalOperand(op string, v *version.Version, segs int) string {
	n := 3
	if op == "~>" {
		n = pessimisticPrecision(segs)
	}
	seg := segments(v)
	parts := make([]string, n)
	for i := range parts {
		parts[i] = strconv.Itoa(seg[i])
	}
	s := strings.Join(parts, ".")
	if pre := v.Prerelease(); pre != "" {
		s += "-" + pre
	}
	return s
}

// pessimisticPrecision is the number of segments Terraform reads from a "~>"
// operand: it treats "~> 1" as "~> 1.0".
func pessimisticPrecision(segs int) int {
	return maxInt(segs, 2)
}

// CanonicalConstraint renders a constraint so that constraints Terraform reads
// the same way render identically, whatever their spelling or term order:
// "6.0.1" and "= 6.0.1", or ">= 4, < 5" and "< 5.0.0, >= 4.0.0". It is for
// comparison, not display.
func CanonicalConstraint(constraint string) string {
	terms := normalizeTerms(constraint)
	sort.SliceStable(terms, func(i, j int) bool {
		a, b := terms[i].version, terms[j].version
		switch {
		case a != nil && b != nil && !a.Equal(b):
			return a.LessThan(b)
		case (a == nil) != (b == nil):
			// Anything that is not a version sorts last.
			return a != nil
		default:
			return terms[i].key < terms[j].key
		}
	})

	seen := map[string]bool{}
	out := make([]string, 0, len(terms))
	for _, t := range terms {
		if !seen[t.key] {
			seen[t.key] = true
			out = append(out, t.key)
		}
	}
	return strings.Join(out, ", ")
}

// ExternalConstraint returns the terms present in merged but not declared
// locally.
//
// Terraform records the merged constraints of the whole module tree in the
// lock file. Subtracting what this repository declares leaves the terms
// imposed by its dependencies — typically a ceiling inside a shared module
// that no file in this repository mentions. Terms are compared by meaning,
// because the lock file records ">= 4.65" as ">= 4.65.0".
func ExternalConstraint(merged, local string) string {
	if strings.TrimSpace(merged) == "" {
		return ""
	}

	declared := map[string]bool{}
	for _, t := range normalizeTerms(local) {
		declared[t.key] = true
	}

	var out []string
	for _, t := range normalizeTerms(merged) {
		if !declared[t.key] {
			out = append(out, t.text)
		}
	}
	return strings.Join(out, ", ")
}

// FilterSatisfying returns the candidates permitted by constraint. An empty
// constraint permits everything.
func FilterSatisfying(candidates []*version.Version, constraint string) []*version.Version {
	if strings.TrimSpace(constraint) == "" {
		return candidates
	}

	out := make([]*version.Version, 0, len(candidates))
	for _, c := range candidates {
		if ok, err := Satisfies(constraint, c); err == nil && ok {
			out = append(out, c)
		}
	}
	return out
}
