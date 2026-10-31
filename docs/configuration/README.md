# Configuration

## 8. Configuration

Optional `.provlift.yaml` in the repository root:

```yaml
exclude:
  - archive
  - "**/examples"

platforms:
  - windows_amd64
  - linux_amd64
  - darwin_arm64

terraform:
  binary: terraform   # set to "tofu" for OpenTofu

providers:
  ignore:
    - hashicorp/azuread

  max_level:
    hashicorp/aws: minor        # patch | minor | major | none
    hashicorp/google: patch
    hashicorp/kubernetes: none  # report it, never upgrade it
```

Unknown keys are rejected, so a typo cannot silently disable a safety rule.
For the same reason the file must hold a single YAML document; an empty file
means defaults.

Exclusion patterns use slash-separated repository-relative paths. `*` matches
within one path segment, while `**` matches zero or more directory levels.
For example, `modules/**` excludes the root `modules` tree; `**/modules/**`
also excludes module trees nested under environment folders. A matched directory
and its descendants are skipped during both discovery and backup recovery.
The `.git`, `.terraform`, and `node_modules` directories are always skipped.

### 8.1 Keys

| Key | Type | Default | Purpose |
|---|---|---|---|
| `exclude` | list of globs | none | Directories to skip, relative to the root |
| `platforms` | list of `os_arch` | `windows_amd64`, `linux_amd64`, `darwin_amd64`, `darwin_arm64` | Platforms to record lock hashes for |
| `terraform.binary` | string | `terraform` | Executable to drive: `terraform` or `tofu` |
| `providers.ignore` | list of selectors | none | Providers the tool must never touch |
| `providers.max_level` | map of selector to level | none | Per-provider cap: `patch`, `minor`, `major` or `none` |

A provider selector may be a bare type (`aws`), a namespace and type
(`hashicorp/aws`) or a full address (`registry.terraform.io/hashicorp/aws`,
`tf.example.com/platform/internal`).

`max_level` is the mechanism for holding a provider back while a migration is
scheduled, without removing it from reporting.

### 8.2 OpenTofu

Set `terraform.binary: tofu` (or pass `--terraform-binary tofu`, or set
`PROVLIFT_TERRAFORM_BIN=tofu`) to drive OpenTofu. Every Terraform command in
this documentation then runs as the equivalent `tofu` command, and provider
sources written without a hostname — `aws` or `hashicorp/aws` — resolve to
`registry.opentofu.org`, matching OpenTofu's own default. Sources that name a
host, such as `registry.terraform.io/hashicorp/aws` or a private registry, keep
that host.

Repository configuration accepts only the names `terraform` and `tofu`. To run
an executable at a specific path, pass `--terraform-binary` or set
`PROVLIFT_TERRAFORM_BIN`; see the
[architecture guide](../architecture/README.md#terraform-executable-policy) for
how these interact.

---
