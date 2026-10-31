package hclutil

import (
	"errors"
	"testing"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/hclsyntax"
)

// exprFor parses a single expression so the helpers can be exercised against
// real HCL rather than hand-built syntax trees.
func exprFor(t *testing.T, src string) hcl.Expression {
	t.Helper()

	f, diags := hclsyntax.ParseConfig([]byte("value = "+src+"\n"), "test.hcl", hcl.InitialPos)
	if diags.HasErrors() {
		t.Fatalf("parsing %q: %s", src, diags.Error())
	}

	attrs, diags := f.Body.JustAttributes()
	if diags.HasErrors() {
		t.Fatalf("reading attributes: %s", diags.Error())
	}
	return attrs["value"].Expr
}

func TestString(t *testing.T) {
	tests := []struct {
		src  string
		want string
	}{
		{`"~> 6.0"`, "~> 6.0"},
		{`">= 3.47.0, < 4.0.0"`, ">= 3.47.0, < 4.0.0"},
		{`"integrations/github"`, "integrations/github"},
		// Numbers convert cleanly, which matters for a bare version such as 2.
		{`2`, "2"},
	}

	for _, tc := range tests {
		t.Run(tc.src, func(t *testing.T) {
			got, err := String(exprFor(t, tc.src))
			if err != nil {
				t.Fatalf("String: %v", err)
			}
			if got != tc.want {
				t.Errorf("= %q, want %q", got, tc.want)
			}
		})
	}
}

func TestStringRejectsNonStaticExpressions(t *testing.T) {
	// Terraform configuration may build values at runtime. Those must be
	// reported, never guessed at, because the tool would otherwise rewrite a
	// constraint it does not understand.
	tests := []string{
		`var.provider_version`,
		`local.version`,
		`"${var.major}.0"`,
		`null`,
	}

	for _, src := range tests {
		t.Run(src, func(t *testing.T) {
			_, err := String(exprFor(t, src))
			if err == nil {
				t.Fatalf("expected an error for %q", src)
			}

			var notStatic *ErrNotStatic
			if !errors.As(err, &notStatic) {
				t.Errorf("err = %T(%v), want *ErrNotStatic", err, err)
			}
		})
	}
}

func TestStringRejectsWrongTypes(t *testing.T) {
	if _, err := String(exprFor(t, `["a", "b"]`)); err == nil {
		t.Error("expected an error when converting a list to a string")
	}
}

func TestStringSlice(t *testing.T) {
	got, err := StringSlice(exprFor(t, `["h1:aaa=", "zh:bbb"]`))
	if err != nil {
		t.Fatalf("StringSlice: %v", err)
	}
	if len(got) != 2 || got[0] != "h1:aaa=" || got[1] != "zh:bbb" {
		t.Errorf("= %v, want [h1:aaa= zh:bbb]", got)
	}
}

func TestStringSliceEmptyList(t *testing.T) {
	got, err := StringSlice(exprFor(t, `[]`))
	if err != nil {
		t.Fatalf("StringSlice: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("= %v, want empty", got)
	}
}

func TestStringSliceRejectsNonList(t *testing.T) {
	if _, err := StringSlice(exprFor(t, `"not a list"`)); err == nil {
		t.Error("expected an error when converting a string to a list")
	}
}

func TestErrNotStaticMessage(t *testing.T) {
	err := &ErrNotStatic{Detail: "because"}
	if err.Error() == "" {
		t.Error("ErrNotStatic must produce a message")
	}
}
