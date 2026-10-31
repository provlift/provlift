package model

import "testing"

func TestDefaultRegistryHost(t *testing.T) {
	tests := []struct {
		binary string
		want   string
	}{
		{"terraform", "registry.terraform.io"},
		{"/usr/local/bin/terraform", "registry.terraform.io"},
		{"tofu", "registry.opentofu.org"},
		{"tofu.exe", "registry.opentofu.org"},
		{"/opt/opentofu/bin/tofu", "registry.opentofu.org"},
		{"", "registry.terraform.io"},
	}
	for _, tc := range tests {
		if got := DefaultRegistryHost(tc.binary); got != tc.want {
			t.Errorf("DefaultRegistryHost(%q) = %q, want %q", tc.binary, got, tc.want)
		}
	}
}

func TestParseAddressOnOpenTofuRegistry(t *testing.T) {
	tests := []struct {
		in   string
		want string
	}{
		{"aws", "registry.opentofu.org/hashicorp/aws"},
		{"hashicorp/aws", "registry.opentofu.org/hashicorp/aws"},
		{"integrations/github", "registry.opentofu.org/integrations/github"},
		{"registry.opentofu.org/hashicorp/aws", "registry.opentofu.org/hashicorp/aws"},
		// An explicit host is never rewritten.
		{"registry.terraform.io/hashicorp/aws", "registry.terraform.io/hashicorp/aws"},
		{"tf.example.com/platform/internal", "tf.example.com/platform/internal"},
	}
	for _, tc := range tests {
		t.Run(tc.in, func(t *testing.T) {
			got, err := ParseAddressOn(tc.in, OpenTofuRegistryHost)
			if err != nil {
				t.Fatalf("ParseAddressOn(%q): %v", tc.in, err)
			}
			if got.String() != tc.want {
				t.Errorf("= %q, want %q", got.String(), tc.want)
			}
		})
	}
}

func TestDisplaySourceOmitsBothPublicRegistries(t *testing.T) {
	for _, in := range []string{"registry.terraform.io/hashicorp/aws", "registry.opentofu.org/hashicorp/aws"} {
		addr, err := ParseAddress(in)
		if err != nil {
			t.Fatal(err)
		}
		if got := DisplaySource(addr); got != "hashicorp/aws" {
			t.Errorf("DisplaySource(%q) = %q, want hashicorp/aws", in, got)
		}
	}
}
