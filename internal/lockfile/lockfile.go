// Package lockfile reads .terraform.lock.hcl, the file Terraform writes to
// record exactly which provider versions it selected. Constraints say what is
// acceptable; this file says what is actually in use.
//
// This package is read-only by design. Writing the lock file correctly requires
// per-platform hashes that only Terraform can produce, so the CLI always
// delegates mutations to `terraform init -upgrade` / `terraform providers lock`.
package lockfile

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/hclparse"

	"github.com/provlift/provlift/internal/hclutil"
	"github.com/provlift/provlift/internal/model"
)

// Name is the fixed filename Terraform uses.
const Name = ".terraform.lock.hcl"

var fileSchema = &hcl.BodySchema{
	Blocks: []hcl.BlockHeaderSchema{
		{Type: "provider", LabelNames: []string{"source"}},
	},
}

var providerSchema = &hcl.BodySchema{
	Attributes: []hcl.AttributeSchema{
		{Name: "version"},
		{Name: "constraints"},
		{Name: "hashes"},
	},
}

// Read parses the lock file in dir. A missing file is not an error: it simply
// means the module has never been initialised.
func Read(dir string) (map[string]model.ProviderLock, bool, error) {
	path := filepath.Join(dir, Name)

	src, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("reading %s: %w", path, err)
	}

	locks, err := parse(path, src)
	if err != nil {
		return nil, true, err
	}
	return locks, true, nil
}

func parse(path string, src []byte) (map[string]model.ProviderLock, error) {
	f, diags := hclparse.NewParser().ParseHCL(src, path)
	if diags.HasErrors() {
		return nil, fmt.Errorf("parsing %s: %s", path, diags.Error())
	}

	content, diags := f.Body.Content(fileSchema)
	if diags.HasErrors() {
		return nil, fmt.Errorf("reading %s: %s", path, diags.Error())
	}

	locks := map[string]model.ProviderLock{}
	for _, block := range content.Blocks {
		lock, err := decodeProviderBlock(block)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", path, err)
		}
		locks[lock.Address.String()] = lock
	}
	return locks, nil
}

func decodeProviderBlock(block *hcl.Block) (model.ProviderLock, error) {
	source := block.Labels[0]

	addr, err := model.ParseAddress(source)
	if err != nil {
		return model.ProviderLock{}, err
	}

	content, diags := block.Body.Content(providerSchema)
	if diags.HasErrors() {
		return model.ProviderLock{}, fmt.Errorf("provider %q: %s", source, diags.Error())
	}

	lock := model.ProviderLock{Address: addr, Source: addr.String()}

	if attr, ok := content.Attributes["version"]; ok {
		if lock.Version, err = hclutil.String(attr.Expr); err != nil {
			return model.ProviderLock{}, fmt.Errorf("provider %q version: %w", source, err)
		}
	}
	if attr, ok := content.Attributes["constraints"]; ok {
		if lock.Constraints, err = hclutil.String(attr.Expr); err != nil {
			return model.ProviderLock{}, fmt.Errorf("provider %q constraints: %w", source, err)
		}
	}
	if attr, ok := content.Attributes["hashes"]; ok {
		if lock.Hashes, err = hclutil.StringSlice(attr.Expr); err != nil {
			return model.ProviderLock{}, fmt.Errorf("provider %q hashes: %w", source, err)
		}
	}

	if lock.Version == "" {
		return model.ProviderLock{}, fmt.Errorf("provider %q has no version in lock file", source)
	}
	return lock, nil
}
