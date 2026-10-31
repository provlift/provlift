// Package registry discovers available provider versions.
//
// Versions come from the provider registry protocol rather than from any
// hard-coded table, so a new provider needs no code change: if Terraform can
// install it, this package can enumerate it.
package registry

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"

	version "github.com/hashicorp/go-version"

	"github.com/provlift/provlift/internal/model"
)

const (
	defaultUserAgent = "provlift"
	discoveryPath    = "/.well-known/terraform.json"
	providersV1Key   = "providers.v1"
	maxResponseBytes = 16 << 20
)

// ErrProviderNotFound means the registry has no such provider. It is reported
// separately from transport failures so the CLI can tell "you typed the wrong
// name" apart from "the network is down".
var ErrProviderNotFound = errors.New("provider not found in registry")

// Version is one published release of a provider.
type Version struct {
	Version   *version.Version
	Protocols []string
	Platforms []Platform
}

// Platform is an os/arch pair a release was built for.
type Platform struct {
	OS   string `json:"os"`
	Arch string `json:"arch"`
}

// Client queries provider registries over the documented registry protocol.
type Client struct {
	httpClient *http.Client
	creds      CredentialSource
	userAgent  string

	// scheme is always https in production; tests point it at a local server.
	scheme string

	mu       sync.Mutex
	services map[string]string    // host -> providers.v1 base URL
	versions map[string][]Version // provider address -> cached versions
}

// Option customises a Client.
type Option func(*Client)

// WithHTTPClient overrides the HTTP client, primarily for tests.
func WithHTTPClient(hc *http.Client) Option {
	return func(c *Client) { c.httpClient = hc }
}

// WithCredentials overrides where registry tokens are read from.
func WithCredentials(src CredentialSource) Option {
	return func(c *Client) { c.creds = src }
}

// WithUserAgent sets the User-Agent header, which registries use for telemetry
// and abuse handling.
func WithUserAgent(ua string) Option {
	return func(c *Client) { c.userAgent = ua }
}

// New builds a Client with sensible defaults.
func New(opts ...Option) *Client {
	c := &Client{
		httpClient: &http.Client{Timeout: 30 * time.Second},
		creds:      DefaultCredentials(),
		userAgent:  defaultUserAgent,
		scheme:     "https",
		services:   map[string]string{},
		versions:   map[string][]Version{},
	}
	for _, opt := range opts {
		opt(c)
	}
	return c
}

// Versions returns every published version of addr, newest last.
func (c *Client) Versions(ctx context.Context, addr model.Address) ([]Version, error) {
	key := addr.String()

	c.mu.Lock()
	cached, ok := c.versions[key]
	c.mu.Unlock()
	if ok {
		return cached, nil
	}

	base, err := c.serviceURL(ctx, addr.Hostname.String())
	if err != nil {
		return nil, err
	}

	endpoint, err := url.JoinPath(base, addr.Namespace, addr.Type, "versions")
	if err != nil {
		return nil, fmt.Errorf("building versions URL for %s: %w", addr.ForDisplay(), err)
	}

	var payload struct {
		Versions []struct {
			Version   string     `json:"version"`
			Protocols []string   `json:"protocols"`
			Platforms []Platform `json:"platforms"`
		} `json:"versions"`
	}
	credentialHost, err := endpointHost(endpoint, c.scheme)
	if err != nil {
		return nil, err
	}
	if !strings.EqualFold(credentialHost, addr.Hostname.String()) {
		return nil, fmt.Errorf("provider service endpoint changed registry host")
	}
	if err := c.getJSON(ctx, endpoint, credentialHost, &payload); err != nil {
		return nil, fmt.Errorf("listing versions for %s: %w", addr.ForDisplay(), err)
	}

	out := make([]Version, 0, len(payload.Versions))
	for _, raw := range payload.Versions {
		v, err := version.NewVersion(raw.Version)
		if err != nil {
			// A registry entry we cannot parse must not poison the whole list.
			continue
		}
		out = append(out, Version{Version: v, Protocols: raw.Protocols, Platforms: raw.Platforms})
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("%w: %s", ErrProviderNotFound, addr.ForDisplay())
	}

	sort.Slice(out, func(i, j int) bool {
		return out[i].Version.LessThan(out[j].Version)
	})

	c.mu.Lock()
	c.versions[key] = out
	c.mu.Unlock()
	return out, nil
}

func endpointHost(endpoint, scheme string) (string, error) {
	u, err := url.Parse(endpoint)
	if err != nil || u.Host == "" {
		return "", fmt.Errorf("invalid provider service endpoint %q", endpoint)
	}
	if !strings.EqualFold(u.Scheme, scheme) {
		return "", fmt.Errorf("provider service endpoint must use HTTPS: %q", endpoint)
	}
	return u.Host, nil
}

// serviceURL resolves a hostname to its providers.v1 endpoint using remote
// service discovery, which is what allows private registries to work.
func (c *Client) serviceURL(ctx context.Context, host string) (string, error) {
	c.mu.Lock()
	cached, ok := c.services[host]
	c.mu.Unlock()
	if ok {
		return cached, nil
	}

	discoveryURL := c.scheme + "://" + host + discoveryPath

	var doc map[string]any
	if err := c.getJSON(ctx, discoveryURL, host, &doc); err != nil {
		return "", fmt.Errorf("service discovery for %s: %w", host, err)
	}

	raw, ok := doc[providersV1Key].(string)
	if !ok || raw == "" {
		return "", fmt.Errorf("host %s does not advertise a %s endpoint", host, providersV1Key)
	}

	resolved, err := c.resolveServicePath(host, raw)
	if err != nil {
		return "", err
	}

	c.mu.Lock()
	c.services[host] = resolved
	c.mu.Unlock()
	return resolved, nil
}

func (c *Client) resolveServicePath(host, raw string) (string, error) {
	ref, err := url.Parse(raw)
	if err != nil {
		return "", fmt.Errorf("host %s advertised an invalid %s value %q: %w", host, providersV1Key, raw, err)
	}
	if ref.IsAbs() {
		if !strings.EqualFold(ref.Scheme, c.scheme) {
			return "", fmt.Errorf("host %s advertised an insecure %s URL for %s", host, ref.Scheme, providersV1Key)
		}
		if !strings.EqualFold(ref.Host, host) || ref.User != nil {
			return "", fmt.Errorf("host %s advertised a service endpoint on another host", host)
		}
		return strings.TrimSuffix(ref.String(), "/"), nil
	}
	if ref.Host != "" || ref.User != nil {
		return "", fmt.Errorf("host %s advertised a service endpoint on another host", host)
	}
	base := &url.URL{Scheme: c.scheme, Host: host}
	return strings.TrimSuffix(base.ResolveReference(ref).String(), "/"), nil
}

func (c *Client) getJSON(ctx context.Context, endpoint, host string, out any) error {
	const attempts = 3

	var lastErr error
	for attempt := 1; attempt <= attempts; attempt++ {
		body, retryable, err := c.get(ctx, endpoint, host)
		if err == nil {
			if err := json.Unmarshal(body, out); err != nil {
				return fmt.Errorf("decoding response from %s: %w", endpoint, err)
			}
			return nil
		}
		lastErr = err
		if !retryable || ctx.Err() != nil {
			return err
		}

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Duration(attempt) * 500 * time.Millisecond):
		}
	}
	return lastErr
}

func (c *Client) get(ctx context.Context, endpoint, host string) (body []byte, retryable bool, err error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, false, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", c.userAgent)

	if c.creds != nil {
		if token, ok := c.creds.Token(host); ok && token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
	}

	client := *c.httpClient
	client.CheckRedirect = func(next *http.Request, via []*http.Request) error {
		if next.URL.Scheme != req.URL.Scheme || !strings.EqualFold(next.URL.Host, req.URL.Host) {
			return fmt.Errorf("registry redirect changed host or scheme")
		}
		if c.httpClient.CheckRedirect != nil {
			return c.httpClient.CheckRedirect(next, via)
		}
		if len(via) >= 10 {
			return fmt.Errorf("too many registry redirects")
		}
		return nil
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, true, fmt.Errorf("requesting %s: %w", endpoint, err)
	}
	defer resp.Body.Close()

	// Cap the read so a misbehaving endpoint cannot exhaust memory.
	body, err = io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes+1))
	if err != nil {
		return nil, true, fmt.Errorf("reading response from %s: %w", endpoint, err)
	}
	if len(body) > maxResponseBytes {
		return nil, false, fmt.Errorf("response from %s exceeds 16 MiB", endpoint)
	}

	switch {
	case resp.StatusCode == http.StatusOK:
		return body, false, nil
	case resp.StatusCode == http.StatusNotFound:
		return nil, false, fmt.Errorf("%w (%s)", ErrProviderNotFound, endpoint)
	case resp.StatusCode == http.StatusUnauthorized, resp.StatusCode == http.StatusForbidden:
		return nil, false, fmt.Errorf("registry %s rejected the request (HTTP %d); set a token via TF_TOKEN_%s or `terraform login`",
			host, resp.StatusCode, EnvVarSuffix(host))
	case resp.StatusCode == http.StatusTooManyRequests, resp.StatusCode >= 500:
		return nil, true, fmt.Errorf("registry %s returned HTTP %d", host, resp.StatusCode)
	default:
		return nil, false, fmt.Errorf("registry %s returned HTTP %d", host, resp.StatusCode)
	}
}
