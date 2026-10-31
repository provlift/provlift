package model

import (
	"fmt"
	"path/filepath"
	"strings"

	tfaddr "github.com/hashicorp/terraform-registry-address"
	svchost "github.com/hashicorp/terraform-svchost"
)

// Address identifies a provider by its fully qualified source address,
// e.g. registry.terraform.io/hashicorp/azurerm.
type Address = tfaddr.Provider

// OpenTofuRegistryHost is the public registry OpenTofu resolves a source
// against when the source names no hostname.
const OpenTofuRegistryHost = "registry.opentofu.org"

// DefaultRegistryHost returns the registry a source without a hostname means
// for the given executable: OpenTofu's registry for a "tofu" binary, otherwise
// Terraform's. The executable may be a bare name or a path.
func DefaultRegistryHost(binary string) string {
	name := strings.TrimSuffix(strings.ToLower(filepath.Base(binary)), ".exe")
	if strings.HasPrefix(name, "tofu") {
		return OpenTofuRegistryHost
	}
	return tfaddr.DefaultProviderRegistryHost.String()
}

// ParseAddress accepts the forms Terraform accepts in a `source` argument:
// "azurerm", "hashicorp/azurerm" or "registry.terraform.io/hashicorp/azurerm".
// A bare type is resolved against the public registry's hashicorp namespace,
// mirroring Terraform's own implied-namespace behaviour.
func ParseAddress(raw string) (Address, error) {
	return ParseAddressOn(raw, "")
}

// ParseAddressOn is ParseAddress with the registry that a source without a
// hostname resolves against. An empty host means Terraform's public registry.
// A source that names its host keeps it: OpenTofu honours an explicit
// registry.terraform.io just as Terraform honours a private registry.
func ParseAddressOn(raw, defaultHost string) (Address, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return Address{}, fmt.Errorf("empty provider source address")
	}

	host := tfaddr.DefaultProviderRegistryHost
	if defaultHost != "" {
		h, err := svchost.ForComparison(defaultHost)
		if err != nil {
			return Address{}, fmt.Errorf("invalid registry host %q: %w", defaultHost, err)
		}
		host = h
	}

	if !strings.Contains(raw, "/") {
		addr, err := tfaddr.ParseProviderPart(raw)
		if err != nil {
			return Address{}, fmt.Errorf("invalid provider type %q: %w", raw, err)
		}
		return tfaddr.NewProvider(host, "hashicorp", addr), nil
	}

	addr, err := tfaddr.ParseProviderSource(raw)
	if err != nil {
		return Address{}, fmt.Errorf("invalid provider source %q: %w", raw, err)
	}
	if strings.Count(raw, "/") == 1 {
		addr.Hostname = host
	}
	return addr, nil
}

// MatchesSelector reports whether addr is the provider the user meant by
// selector, which may be a bare type, a namespace/type pair or a full address.
// Matching is deliberately permissive so `--provider azurerm` behaves as
// operators expect, while a fully qualified selector stays exact.
func MatchesSelector(addr Address, selector string) bool {
	selector = strings.TrimSpace(selector)
	if selector == "" {
		return false
	}

	switch strings.Count(selector, "/") {
	case 0:
		return strings.EqualFold(addr.Type, selector)
	case 1:
		parts := strings.SplitN(selector, "/", 2)
		return strings.EqualFold(addr.Namespace, parts[0]) &&
			strings.EqualFold(addr.Type, parts[1])
	default:
		parsed, err := tfaddr.ParseProviderSource(selector)
		if err != nil {
			return false
		}
		return addr.Equals(parsed)
	}
}

// MatchesDenySelector matches the type across namespaces and hosts. A deny
// written for one source must not leave a same-type source eligible for work.
func MatchesDenySelector(addr Address, selector string) bool {
	selector = strings.TrimSpace(selector)
	if selector == "" {
		return false
	}
	parsed, err := ParseAddress(selector)
	return err == nil && strings.EqualFold(addr.Type, parsed.Type)
}

// DisplaySource renders an address for a human-readable table, omitting the
// Terraform or OpenTofu public registry hostname. A private registry keeps its
// host, because that distinction changes where the provider comes from and
// must stay visible.
//
// Machine-readable output always uses the fully qualified address instead.
func DisplaySource(addr Address) string {
	if addr.Hostname == tfaddr.DefaultProviderRegistryHost || addr.Hostname.String() == OpenTofuRegistryHost {
		return addr.Namespace + "/" + addr.Type
	}
	return addr.String()
}
