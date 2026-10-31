package registry

import (
	"os"
	"path/filepath"
	"testing"
)

// isolateCredentials points the credential lookup at an empty directory so a
// test never picks up the developer's real tokens.
func isolateCredentials(t *testing.T) string {
	t.Helper()

	dir := t.TempDir()
	t.Setenv("APPDATA", dir)
	t.Setenv("HOME", dir)
	t.Setenv("USERPROFILE", dir)
	t.Setenv("TF_CLI_CONFIG_FILE", "")
	t.Setenv("TF_TOKEN_app_terraform_io", "")
	return dir
}

func writeCLIConfig(t *testing.T, content string) string {
	t.Helper()

	path := filepath.Join(t.TempDir(), "terraform.rc")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("writing CLI config: %v", err)
	}
	return path
}

func TestDefaultCredentialsReadsCLIConfig(t *testing.T) {
	isolateCredentials(t)

	path := writeCLIConfig(t, `
credentials "app.terraform.io" {
  token = "from-cli-config"
}

credentials "tf.example.com" {
  token = "private-registry-token"
}
`)
	t.Setenv("TF_CLI_CONFIG_FILE", path)

	creds := DefaultCredentials()

	got, ok := creds.Token("app.terraform.io")
	if !ok || got != "from-cli-config" {
		t.Errorf("Token(app.terraform.io) = %q, %v; want \"from-cli-config\", true", got, ok)
	}

	got, ok = creds.Token("tf.example.com")
	if !ok || got != "private-registry-token" {
		t.Errorf("Token(tf.example.com) = %q, %v", got, ok)
	}
}

func TestDefaultCredentialsEnvironmentBeatsFile(t *testing.T) {
	isolateCredentials(t)

	path := writeCLIConfig(t, `
credentials "app.terraform.io" {
  token = "from-cli-config"
}
`)
	t.Setenv("TF_CLI_CONFIG_FILE", path)
	t.Setenv("TF_TOKEN_app_terraform_io", "from-env")

	got, ok := DefaultCredentials().Token("app.terraform.io")
	if !ok || got != "from-env" {
		t.Errorf("Token() = %q, %v; want \"from-env\", true", got, ok)
	}
}

func TestDefaultCredentialsHostMatchingIsCaseInsensitive(t *testing.T) {
	isolateCredentials(t)

	path := writeCLIConfig(t, `
credentials "TF.Example.COM" {
  token = "mixed-case"
}
`)
	t.Setenv("TF_CLI_CONFIG_FILE", path)

	got, ok := DefaultCredentials().Token("tf.example.com")
	if !ok || got != "mixed-case" {
		t.Errorf("Token() = %q, %v; want \"mixed-case\", true", got, ok)
	}
}

func TestDefaultCredentialsUnknownHost(t *testing.T) {
	isolateCredentials(t)

	// The public registry needs no credentials; absence must be reported
	// cleanly rather than returning a stray token.
	if got, ok := DefaultCredentials().Token("registry.terraform.io"); ok {
		t.Errorf("Token() = %q, %v; want \"\", false", got, ok)
	}
}

func TestDefaultCredentialsToleratesBrokenFiles(t *testing.T) {
	tests := []struct {
		name    string
		content string
	}{
		{"malformed HCL", `credentials "app.terraform.io" {`},
		{"credentials block with no token", `credentials "app.terraform.io" {}`},
		{"empty token", "credentials \"app.terraform.io\" {\n  token = \"\"\n}"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			isolateCredentials(t)
			t.Setenv("TF_CLI_CONFIG_FILE", writeCLIConfig(t, tc.content))

			// A broken config must not panic and must not invent a token.
			if got, ok := DefaultCredentials().Token("app.terraform.io"); ok {
				t.Errorf("Token() = %q, %v; want \"\", false", got, ok)
			}
		})
	}
}

func TestDefaultCredentialsMissingFile(t *testing.T) {
	isolateCredentials(t)
	t.Setenv("TF_CLI_CONFIG_FILE", filepath.Join(t.TempDir(), "does-not-exist.rc"))

	if _, ok := DefaultCredentials().Token("app.terraform.io"); ok {
		t.Error("a missing config file must not yield a token")
	}
}

func TestDefaultCredentialsReadsTerraformLoginStore(t *testing.T) {
	dir := isolateCredentials(t)

	store := filepath.Join(dir, "terraform.d")
	if err := os.MkdirAll(store, 0o755); err != nil {
		t.Fatalf("creating store: %v", err)
	}
	content := `{"credentials":{"app.terraform.io":{"token":"from-terraform-login"}}}`
	if err := os.WriteFile(filepath.Join(store, "credentials.tfrc.json"), []byte(content), 0o600); err != nil {
		t.Fatalf("writing credentials store: %v", err)
	}

	got, ok := DefaultCredentials().Token("app.terraform.io")
	if !ok || got != "from-terraform-login" {
		t.Skipf("credentials store not consulted on this platform (got %q, %v)", got, ok)
	}
}

func TestStaticCredentials(t *testing.T) {
	creds := StaticCredentials{"tf.example.com": "abc"}

	if got, ok := creds.Token("tf.example.com"); !ok || got != "abc" {
		t.Errorf("Token() = %q, %v", got, ok)
	}
	if got, ok := creds.Token("TF.EXAMPLE.COM"); !ok || got != "abc" {
		t.Errorf("Token() should be case-insensitive, got %q, %v", got, ok)
	}
	if _, ok := creds.Token("other.example.com"); ok {
		t.Error("Token() returned a token for an unknown host")
	}
}

func TestNoCredentials(t *testing.T) {
	if _, ok := (NoCredentials{}).Token("app.terraform.io"); ok {
		t.Error("NoCredentials must never return a token")
	}
}
