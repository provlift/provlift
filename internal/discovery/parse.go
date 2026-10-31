package discovery

import (
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/hclparse"

	"github.com/provlift/provlift/internal/hclutil"
	"github.com/provlift/provlift/internal/model"
)

// PartialContent is used throughout so that unrelated blocks (resource,
// variable, backend, cloud, provider_meta, ...) never cause a scan to fail.
var (
	fileSchema = &hcl.BodySchema{
		Blocks: []hcl.BlockHeaderSchema{
			{Type: "terraform"},
			{Type: "module", LabelNames: []string{"name"}},
		},
	}

	terraformSchema = &hcl.BodySchema{
		Attributes: []hcl.AttributeSchema{{Name: "required_version"}},
		Blocks:     []hcl.BlockHeaderSchema{{Type: "required_providers"}},
	}

	moduleSchema = &hcl.BodySchema{
		Attributes: []hcl.AttributeSchema{{Name: "source"}},
	}
)

type parsedFile struct {
	registryHost       string
	requiredVersion    string
	requirements       []model.ProviderRequirement
	localModuleSources []string
}

// RequirementsIn parses the required_providers declared directly in dir,
// ignoring subdirectories. It is used to read modules Terraform has vendored
// into .terraform/modules, which the ordinary scan deliberately skips.
func RequirementsIn(dir string) []model.ProviderRequirement {
	return RequirementsInRegistry(dir, "")
}

// RequirementsInRegistry is RequirementsIn with the registry that sources
// without a hostname resolve against; see Options.RegistryHost.
func RequirementsInRegistry(dir, registryHost string) []model.ProviderRequirement {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}

	parser := hclparse.NewParser()
	var out []model.ProviderRequirement

	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".tf") {
			continue
		}
		parsed, _ := parseFile(parser, dir, filepath.Join(dir, e.Name()), registryHost)
		if parsed != nil {
			out = append(out, parsed.requirements...)
		}
	}
	return out
}

func parseFile(parser *hclparse.Parser, root, path, registryHost string) (*parsedFile, []model.Diagnostic) {
	src, err := os.ReadFile(path)
	if err != nil {
		return nil, []model.Diagnostic{{
			Severity: "warning",
			Summary:  "could not read file",
			Detail:   err.Error(),
			Pos:      model.SourcePos{File: relTo(root, path)},
		}}
	}

	f, diags := parser.ParseHCL(src, path)
	if diags.HasErrors() {
		return nil, []model.Diagnostic{{
			Severity: "error",
			Summary:  "could not parse Terraform file",
			Detail:   diags.Error(),
			Pos:      model.SourcePos{File: relTo(root, path)},
		}}
	}

	out := &parsedFile{registryHost: registryHost}
	var found []model.Diagnostic

	content, _, _ := f.Body.PartialContent(fileSchema)
	for _, block := range content.Blocks {
		switch block.Type {
		case "terraform":
			found = append(found, out.readTerraformBlock(root, block, src)...)
		case "module":
			if src, ok := localModuleSource(block); ok {
				out.localModuleSources = append(out.localModuleSources, src)
			}
		}
	}
	return out, found
}

func (p *parsedFile) readTerraformBlock(root string, block *hcl.Block, src []byte) []model.Diagnostic {
	content, _, _ := block.Body.PartialContent(terraformSchema)

	if attr, ok := content.Attributes["required_version"]; ok {
		if v, err := hclutil.String(attr.Expr); err == nil {
			p.requiredVersion = v
		}
	}

	var diags []model.Diagnostic
	for _, rp := range content.Blocks {
		if rp.Type != "required_providers" {
			continue
		}
		attrs, attrDiags := rp.Body.JustAttributes()
		if attrDiags.HasErrors() {
			diags = append(diags, model.Diagnostic{
				Severity: "error",
				Summary:  "could not read required_providers",
				Detail:   attrDiags.Error(),
				Pos:      pos(root, rp.DefRange),
			})
			continue
		}
		for name, attr := range attrs {
			req, err := requirementFromAttr(root, name, attr, src, p.registryHost)
			if err != nil {
				diags = append(diags, model.Diagnostic{
					Severity: "error",
					Summary:  fmt.Sprintf("could not read provider requirement %q", name),
					Detail:   err.Error(),
					Pos:      pos(root, attr.Range),
				})
				continue
			}
			p.requirements = append(p.requirements, req)
		}
	}
	return diags
}

func requirementFromAttr(root, localName string, attr *hcl.Attribute, src []byte, registryHost string) (model.ProviderRequirement, error) {
	req := model.ProviderRequirement{
		LocalName:     localName,
		ConstraintPos: pos(root, attr.Range),
		FilePath:      attr.Range.Filename,
		SourceHash:    sha256.Sum256(src),
	}

	pairs, mapDiags := hcl.ExprMap(attr.Expr)
	if mapDiags.HasErrors() {
		// Pre-0.13 shorthand: github = "~> 6.0". The source is always implied.
		constraint, err := hclutil.String(attr.Expr)
		if err != nil {
			return req, fmt.Errorf("expected an object or a version string: %w", err)
		}
		addr, err := model.ParseAddressOn(localName, registryHost)
		if err != nil {
			return req, err
		}
		req.Address = addr
		req.Source = addr.String()
		req.Constraint = constraint
		req.ImpliedSource = true
		req.ConstraintPos = pos(root, attr.Expr.Range())
		req.VersionRange = byteRange(attr.Expr.Range())
		return req, nil
	}

	req.IsObject = true
	objectRange := byteRange(attr.Expr.Range())
	req.ObjectRange = objectRange
	if objectRange != nil && objectRange.End <= len(src) {
		req.ObjectText = string(src[objectRange.Start:objectRange.End])
	}
	if len(pairs) == 0 {
		// Insert immediately before the closing brace of an empty object.
		req.InsertPoint = attr.Expr.Range().End.Byte - 1
	}

	var source string
	for _, pair := range pairs {
		key, err := hclutil.String(pair.Key)
		if err != nil {
			return req, fmt.Errorf("unreadable argument name: %w", err)
		}

		if end := pair.Value.Range().End.Byte; end > req.InsertPoint {
			req.InsertPoint = end
		}

		switch key {
		case "source":
			if source, err = hclutil.String(pair.Value); err != nil {
				return req, fmt.Errorf("source: %w", err)
			}
		case "version":
			if req.Constraint, err = hclutil.String(pair.Value); err != nil {
				return req, fmt.Errorf("version: %w", err)
			}
			req.ConstraintPos = pos(root, pair.Value.Range())
			req.VersionRange = byteRange(pair.Value.Range())
		}
	}

	if source == "" {
		req.ImpliedSource = true
		source = localName
	}
	addr, err := model.ParseAddressOn(source, registryHost)
	if err != nil {
		return req, err
	}
	req.Address = addr
	req.Source = addr.String()
	return req, nil
}

func byteRange(r hcl.Range) *model.ByteRange {
	return &model.ByteRange{Start: r.Start.Byte, End: r.End.Byte}
}

// localModuleSource returns the source of a module block when it points at a
// path inside this repository. Registry and VCS sources are versioned
// independently and are out of scope for provider upgrades.
func localModuleSource(block *hcl.Block) (string, bool) {
	content, _, _ := block.Body.PartialContent(moduleSchema)
	attr, ok := content.Attributes["source"]
	if !ok {
		return "", false
	}
	src, err := hclutil.String(attr.Expr)
	if err != nil {
		return "", false
	}
	if strings.HasPrefix(src, "./") || strings.HasPrefix(src, "../") {
		return src, true
	}
	return "", false
}
