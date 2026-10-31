package registry

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/hclparse"

	"github.com/provlift/provlift/internal/hclutil"
)

// CredentialSource supplies a bearer token for a registry hostname.
type CredentialSource interface {
	// Token returns the token for host. Implementations must never log or
	// otherwise emit the returned value.
	Token(host string) (string, bool)
}

// EnvVarSuffix converts a hostname to the suffix Terraform uses for its
// TF_TOKEN_* environment variables: dots become underscores and dashes become
// double underscores.
func EnvVarSuffix(host string) string {
	s := strings.ReplaceAll(strings.ToLower(host), "-", "__")
	return strings.ReplaceAll(s, ".", "_")
}

// DefaultCredentials reads tokens the same way Terraform does, so operators do
// not need to configure this tool separately. Precedence matches Terraform:
// TF_TOKEN_* wins over the CLI configuration file.
func DefaultCredentials() CredentialSource {
	return &defaultCredentials{}
}

type defaultCredentials struct {
	once sync.Once
	file map[string]string
}

func (d *defaultCredentials) Token(host string) (string, bool) {
	if v := os.Getenv("TF_TOKEN_" + EnvVarSuffix(host)); v != "" {
		return v, true
	}

	d.once.Do(func() { d.file = loadCredentialFiles() })

	if v, ok := d.file[strings.ToLower(host)]; ok && v != "" {
		return v, true
	}
	return "", false
}

// StaticCredentials is an in-memory CredentialSource, used by tests and by
// callers that manage tokens themselves.
type StaticCredentials map[string]string

// Token implements CredentialSource.
func (s StaticCredentials) Token(host string) (string, bool) {
	v, ok := s[strings.ToLower(host)]
	return v, ok
}

// NoCredentials disables all authentication.
type NoCredentials struct{}

// Token implements CredentialSource.
func (NoCredentials) Token(string) (string, bool) { return "", false }

func loadCredentialFiles() map[string]string {
	out := map[string]string{}

	// The JSON store written by `terraform login` is read first so the
	// hand-edited CLI config can override it, matching Terraform's ordering.
	for host, token := range readCredentialsJSON() {
		out[host] = token
	}
	for host, token := range readCLIConfig() {
		out[host] = token
	}
	return out
}

func readCredentialsJSON() map[string]string {
	dir := terraformDataDir()
	if dir == "" {
		return nil
	}

	raw, err := os.ReadFile(filepath.Join(dir, "credentials.tfrc.json"))
	if err != nil {
		return nil
	}

	var doc struct {
		Credentials map[string]struct {
			Token string `json:"token"`
		} `json:"credentials"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		return nil
	}

	out := map[string]string{}
	for host, entry := range doc.Credentials {
		if entry.Token != "" {
			out[strings.ToLower(host)] = entry.Token
		}
	}
	return out
}

var credentialsBlockSchema = &hcl.BodySchema{
	Blocks: []hcl.BlockHeaderSchema{
		{Type: "credentials", LabelNames: []string{"host"}},
	},
}

var credentialsTokenSchema = &hcl.BodySchema{
	Attributes: []hcl.AttributeSchema{{Name: "token"}},
}

func readCLIConfig() map[string]string {
	path := cliConfigPath()
	if path == "" {
		return nil
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		return nil
	}

	f, diags := hclparse.NewParser().ParseHCL(raw, path)
	if diags.HasErrors() {
		return nil
	}

	content, _, _ := f.Body.PartialContent(credentialsBlockSchema)

	out := map[string]string{}
	for _, block := range content.Blocks {
		if len(block.Labels) == 0 {
			continue
		}
		body, _, _ := block.Body.PartialContent(credentialsTokenSchema)
		attr, ok := body.Attributes["token"]
		if !ok {
			continue
		}
		token, err := hclutil.String(attr.Expr)
		if err != nil || token == "" {
			continue
		}
		out[strings.ToLower(block.Labels[0])] = token
	}
	return out
}

func cliConfigPath() string {
	if v := os.Getenv("TF_CLI_CONFIG_FILE"); v != "" {
		return v
	}
	if runtime.GOOS == "windows" {
		if appData := os.Getenv("APPDATA"); appData != "" {
			return filepath.Join(appData, "terraform.rc")
		}
		return ""
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".terraformrc")
}

func terraformDataDir() string {
	if runtime.GOOS == "windows" {
		if appData := os.Getenv("APPDATA"); appData != "" {
			return filepath.Join(appData, "terraform.d")
		}
		return ""
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".terraform.d")
}
