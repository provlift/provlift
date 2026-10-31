package registry

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/provlift/provlift/internal/model"
)

// withScheme lets the tests drive the client against httptest's HTTP server.
func withScheme(scheme string) Option {
	return func(c *Client) { c.scheme = scheme }
}

type testRegistry struct {
	server    *httptest.Server
	host      string
	authSeen  chan string
	hitCount  *int64
	versions  string
	failTimes *int64
}

func newTestRegistry(t *testing.T, versionsJSON string) *testRegistry {
	t.Helper()

	reg := &testRegistry{
		authSeen:  make(chan string, 16),
		hitCount:  new(int64),
		versions:  versionsJSON,
		failTimes: new(int64),
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/terraform.json", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"providers.v1":"/v1/providers/"}`)
	})
	mux.HandleFunc("/v1/providers/", func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt64(reg.hitCount, 1)
		reg.authSeen <- r.Header.Get("Authorization")

		if remaining := atomic.LoadInt64(reg.failTimes); remaining > 0 {
			atomic.AddInt64(reg.failTimes, -1)
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		if !strings.HasSuffix(r.URL.Path, "/versions") {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if !strings.Contains(r.URL.Path, "/hashicorp/azurerm/") {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, reg.versions)
	})

	reg.server = httptest.NewServer(mux)
	t.Cleanup(reg.server.Close)
	reg.host = strings.TrimPrefix(reg.server.URL, "http://")
	return reg
}

func (r *testRegistry) client(opts ...Option) *Client {
	base := []Option{withScheme("http"), WithCredentials(NoCredentials{})}
	return New(append(base, opts...)...)
}

func (r *testRegistry) address(t *testing.T, name string) model.Address {
	t.Helper()
	addr, err := model.ParseAddress(r.host + "/" + name)
	if err != nil {
		t.Fatalf("ParseAddress: %v", err)
	}
	return addr
}

const azurermVersions = `{
  "id": "hashicorp/azurerm",
  "versions": [
    {"version": "3.116.0", "protocols": ["5.0"]},
    {"version": "4.0.0", "protocols": ["5.0"]},
    {"version": "3.117.0", "protocols": ["5.0"]},
    {"version": "4.1.0-beta1", "protocols": ["5.0"]},
    {"version": "not-a-version", "protocols": ["5.0"]}
  ]
}`

func TestVersionsDiscoversAndSorts(t *testing.T) {
	reg := newTestRegistry(t, azurermVersions)
	client := reg.client()

	got, err := client.Versions(context.Background(), reg.address(t, "hashicorp/azurerm"))
	if err != nil {
		t.Fatalf("Versions: %v", err)
	}

	var list []string
	for _, v := range got {
		list = append(list, v.Version.String())
	}

	// Unparsable entries are dropped rather than failing the whole lookup, and
	// the result is sorted oldest first.
	want := []string{"3.116.0", "3.117.0", "4.0.0", "4.1.0-beta1"}
	if len(list) != len(want) {
		t.Fatalf("got %v, want %v", list, want)
	}
	for i := range want {
		if list[i] != want[i] {
			t.Fatalf("got %v, want %v", list, want)
		}
	}
}

func TestVersionsAreCached(t *testing.T) {
	reg := newTestRegistry(t, azurermVersions)
	client := reg.client()
	addr := reg.address(t, "hashicorp/azurerm")

	for i := 0; i < 3; i++ {
		if _, err := client.Versions(context.Background(), addr); err != nil {
			t.Fatalf("Versions: %v", err)
		}
	}
	if got := atomic.LoadInt64(reg.hitCount); got != 1 {
		t.Errorf("registry was queried %d times, want 1", got)
	}
}

func TestVersionsUnknownProvider(t *testing.T) {
	reg := newTestRegistry(t, azurermVersions)
	client := reg.client()

	_, err := client.Versions(context.Background(), reg.address(t, "acme/nonexistent"))
	if !errors.Is(err, ErrProviderNotFound) {
		t.Errorf("err = %v, want ErrProviderNotFound", err)
	}
}

func TestVersionsRetriesServerErrors(t *testing.T) {
	reg := newTestRegistry(t, azurermVersions)
	atomic.StoreInt64(reg.failTimes, 1)

	client := reg.client()
	if _, err := client.Versions(context.Background(), reg.address(t, "hashicorp/azurerm")); err != nil {
		t.Fatalf("Versions should have retried past a 500: %v", err)
	}
}

func TestVersionsSendsBearerToken(t *testing.T) {
	reg := newTestRegistry(t, azurermVersions)
	client := reg.client(WithCredentials(StaticCredentials{strings.ToLower(reg.host): "s3cret"}))

	if _, err := client.Versions(context.Background(), reg.address(t, "hashicorp/azurerm")); err != nil {
		t.Fatalf("Versions: %v", err)
	}

	select {
	case got := <-reg.authSeen:
		if got != "Bearer s3cret" {
			t.Errorf("Authorization = %q, want \"Bearer s3cret\"", got)
		}
	default:
		t.Fatal("no request reached the registry")
	}
}

func TestResolveServicePathRejectsSchemeDowngrade(t *testing.T) {
	reg := newTestRegistry(t, azurermVersions)
	client := New()

	if _, err := client.resolveServicePath(reg.host, "https://"+reg.host+"/v1/providers/"); err != nil {
		t.Fatalf("same configured scheme should be accepted: %v", err)
	}
	if _, err := client.resolveServicePath(reg.host, "http://other.example/v1/providers/"); err == nil {
		t.Fatal("expected an HTTP endpoint to be rejected by an HTTPS client")
	}
}

func TestRegistryRejectsCrossHostDiscoveryAndRedirect(t *testing.T) {
	const secret = "registry-secret-never-leak"
	var received atomic.Int64
	attacker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "" {
			received.Add(1)
		}
		fmt.Fprint(w, azurermVersions)
	}))
	defer attacker.Close()
	for _, redirect := range []bool{false, true} {
		t.Run(fmt.Sprint("redirect=", redirect), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == discoveryPath {
					if redirect {
						fmt.Fprint(w, `{"providers.v1":"/v1/providers/"}`)
					} else {
						fmt.Fprintf(w, `{"providers.v1":%q}`, attacker.URL+"/v1/providers/")
					}
					return
				}
				http.Redirect(w, r, attacker.URL+"/v1/providers/", http.StatusFound)
			}))
			defer server.Close()
			host := strings.TrimPrefix(server.URL, "http://")
			addr, err := model.ParseAddress(host + "/hashicorp/azurerm")
			if err != nil {
				t.Fatal(err)
			}
			client := New(withScheme("http"), WithCredentials(StaticCredentials{host: secret}))
			if _, err := client.Versions(context.Background(), addr); err == nil {
				t.Fatal("expected cross-host endpoint to be rejected")
			}
			if got := received.Load(); got != 0 {
				t.Fatalf("token sent to other host %d times", got)
			}
		})
	}
}

func TestRegistryRejectsOversizedResponse(t *testing.T) {
	reg := newTestRegistry(t, strings.Repeat("x", maxResponseBytes+1))
	_, err := reg.client().Versions(context.Background(), reg.address(t, "hashicorp/azurerm"))
	if err == nil || !strings.Contains(err.Error(), "exceeds 16 MiB") {
		t.Fatalf("oversized response error = %v", err)
	}
}

func TestResolveServicePathRejectsNetworkPathHost(t *testing.T) {
	client := New()
	if _, err := client.resolveServicePath("registry.example", "//attacker.example/v1/providers/"); err == nil {
		t.Fatal("expected a network-path service endpoint to be rejected")
	}
}

func TestEnvVarSuffix(t *testing.T) {
	tests := []struct {
		host string
		want string
	}{
		{"app.terraform.io", "app_terraform_io"},
		{"registry.terraform.io", "registry_terraform_io"},
		{"my-registry.example.com", "my__registry_example_com"},
	}

	for _, tc := range tests {
		if got := EnvVarSuffix(tc.host); got != tc.want {
			t.Errorf("EnvVarSuffix(%q) = %q, want %q", tc.host, got, tc.want)
		}
	}
}

func TestDefaultCredentialsPrefersEnvironment(t *testing.T) {
	t.Setenv("TF_TOKEN_app_terraform_io", "from-env")

	creds := DefaultCredentials()
	got, ok := creds.Token("app.terraform.io")
	if !ok || got != "from-env" {
		t.Errorf("Token() = %q, %v; want \"from-env\", true", got, ok)
	}
}
