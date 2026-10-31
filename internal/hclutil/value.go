// Package hclutil holds small helpers for pulling literal values out of HCL
// expressions. Terraform configuration may contain expressions that cannot be
// resolved statically (variables, locals, functions); these helpers report that
// case as an error rather than guessing.
package hclutil

import (
	"fmt"

	"github.com/hashicorp/hcl/v2"
	"github.com/zclconf/go-cty/cty"
	"github.com/zclconf/go-cty/cty/convert"
)

// ErrNotStatic indicates an expression depends on values only known at
// Terraform runtime, so the CLI must not rewrite or interpret it.
type ErrNotStatic struct {
	Detail string
}

func (e *ErrNotStatic) Error() string {
	return "value is not a static literal: " + e.Detail
}

// String evaluates expr with no variable scope and converts the result to a
// string.
func String(expr hcl.Expression) (string, error) {
	val, diags := expr.Value(nil)
	if diags.HasErrors() {
		return "", &ErrNotStatic{Detail: diags.Error()}
	}
	return stringFromValue(val)
}

// StringSlice evaluates expr and converts the result to a []string.
func StringSlice(expr hcl.Expression) ([]string, error) {
	val, diags := expr.Value(nil)
	if diags.HasErrors() {
		return nil, &ErrNotStatic{Detail: diags.Error()}
	}
	if val.IsNull() || !val.IsKnown() {
		return nil, &ErrNotStatic{Detail: "null or unknown list"}
	}

	converted, err := convert.Convert(val, cty.List(cty.String))
	if err != nil {
		return nil, fmt.Errorf("expected a list of strings: %w", err)
	}

	var out []string
	for it := converted.ElementIterator(); it.Next(); {
		_, ev := it.Element()
		s, err := stringFromValue(ev)
		if err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, nil
}

func stringFromValue(val cty.Value) (string, error) {
	if !val.IsKnown() {
		return "", &ErrNotStatic{Detail: "value is not known until apply"}
	}
	if val.IsNull() {
		return "", &ErrNotStatic{Detail: "value is null"}
	}

	converted, err := convert.Convert(val, cty.String)
	if err != nil {
		return "", fmt.Errorf("expected a string: %w", err)
	}
	return converted.AsString(), nil
}
