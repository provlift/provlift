package model

import "testing"

func TestParseAddress(t *testing.T) {
	tests := []struct {
		in   string
		want string
	}{
		{"azurerm", "registry.terraform.io/hashicorp/azurerm"},
		{"hashicorp/azurerm", "registry.terraform.io/hashicorp/azurerm"},
		{"integrations/github", "registry.terraform.io/integrations/github"},
		{"mongodb/mongodbatlas", "registry.terraform.io/mongodb/mongodbatlas"},
		{"registry.terraform.io/hashicorp/azurerm", "registry.terraform.io/hashicorp/azurerm"},
		{"tf.example.com/platform/internal", "tf.example.com/platform/internal"},
	}

	for _, tc := range tests {
		t.Run(tc.in, func(t *testing.T) {
			got, err := ParseAddress(tc.in)
			if err != nil {
				t.Fatalf("ParseAddress(%q): %v", tc.in, err)
			}
			if got.String() != tc.want {
				t.Errorf("= %q, want %q", got.String(), tc.want)
			}
		})
	}
}

func TestParseAddressRejectsGarbage(t *testing.T) {
	for _, in := range []string{"", "   ", "a/b/c/d"} {
		if _, err := ParseAddress(in); err == nil {
			t.Errorf("ParseAddress(%q) should have failed", in)
		}
	}
}

func TestMatchesSelector(t *testing.T) {
	azurerm, err := ParseAddress("hashicorp/azurerm")
	if err != nil {
		t.Fatal(err)
	}
	github, err := ParseAddress("integrations/github")
	if err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name     string
		addr     Address
		selector string
		want     bool
	}{
		{"bare type", azurerm, "azurerm", true},
		{"bare type is case-insensitive", azurerm, "AzureRM", true},
		{"bare type mismatch", azurerm, "github", false},
		{"namespace and type", github, "integrations/github", true},
		{"wrong namespace", github, "hashicorp/github", false},
		{"full address", azurerm, "registry.terraform.io/hashicorp/azurerm", true},
		{"full address wrong host", azurerm, "tf.example.com/hashicorp/azurerm", false},
		{"empty selector never matches", azurerm, "", false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := MatchesSelector(tc.addr, tc.selector); got != tc.want {
				t.Errorf("MatchesSelector(%s, %q) = %v, want %v",
					tc.addr.String(), tc.selector, got, tc.want)
			}
		})
	}
}

func TestSelectorPolicy(t *testing.T) {
	tests := []struct {
		name     string
		address  string
		selector string
		allow    bool
		deny     bool
	}{
		{"bare same source", "hashicorp/azurerm", "azurerm", true, true},
		{"bare other namespace", "contoso/azurerm", "azurerm", true, true},
		{"namespaced same source", "hashicorp/azurerm", "hashicorp/azurerm", true, true},
		{"namespaced other namespace", "contoso/azurerm", "hashicorp/azurerm", false, true},
		{"full same source", "registry.terraform.io/hashicorp/azurerm", "registry.terraform.io/hashicorp/azurerm", true, true},
		{"full other host", "registry.example.com/hashicorp/azurerm", "registry.terraform.io/hashicorp/azurerm", false, true},
		{"different type", "hashicorp/azuread", "hashicorp/azurerm", false, false},
		{"invalid selector", "hashicorp/azurerm", "bad/a/b/c", false, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			addr, err := ParseAddress(tc.address)
			if err != nil {
				t.Fatal(err)
			}
			if got := MatchesSelector(addr, tc.selector); got != tc.allow {
				t.Errorf("allow = %v, want %v", got, tc.allow)
			}
			if got := MatchesDenySelector(addr, tc.selector); got != tc.deny {
				t.Errorf("deny = %v, want %v", got, tc.deny)
			}
		})
	}
}

func TestDisplaySource(t *testing.T) {
	tests := []struct {
		name   string
		source string
		want   string
	}{
		{
			name:   "public registry host is omitted",
			source: "hashicorp/azurerm",
			want:   "hashicorp/azurerm",
		},
		{
			name:   "explicit public host is still omitted",
			source: "registry.terraform.io/integrations/github",
			want:   "integrations/github",
		},
		{
			// Where a provider comes from matters when it is not the public
			// registry, so the host must stay visible.
			name:   "private registry host is preserved",
			source: "tf.example.com/platform/internal",
			want:   "tf.example.com/platform/internal",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			addr, err := ParseAddress(tc.source)
			if err != nil {
				t.Fatalf("ParseAddress(%q): %v", tc.source, err)
			}
			if got := DisplaySource(addr); got != tc.want {
				t.Errorf("DisplaySource(%s) = %q, want %q", tc.source, got, tc.want)
			}
		})
	}
}
