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
  # fs_mirror: /srv/terraform-providers               # air-gapped runners: one of these
  # net_mirror: https://mirror.example.com/providers/

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

`platforms` decides which machines can use the lock file without changing it.
Any platform left out gets its hash added by the first `terraform init` that
runs there, which shows up as lock-file churn, or as a checksum error when
providers come from a mirror. Each platform listed costs one provider package
download per upgrade, so a repository that never runs on, say, ARM Linux can
list only what it uses.

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
| `platforms` | list of `os_arch` | `windows_amd64`, `linux_amd64`, `linux_arm64`, `darwin_amd64`, `darwin_arm64` | Platforms to record lock hashes for |
| `terraform.binary` | string | `terraform` | Executable to drive: `terraform` or `tofu` |
| `terraform.fs_mirror` | path | none | Filesystem mirror for `providers lock`; relative to the repository root |
| `terraform.net_mirror` | HTTPS URL | none | Network mirror for `providers lock` |
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
The precedence is **CLI flag > environment variable > repository config >
default**. In particular, `PROVLIFT_TERRAFORM_BIN` overrides both supported
repository values (`terraform` and `tofu`) so CI can enforce its trusted binary.

### 8.3 Provider mirrors and air-gapped runners

`terraform init` installs providers through the `provider_installation` block in
the Terraform or OpenTofu CLI configuration, so a runner that can only reach a
mirror initialises normally. `terraform providers lock` does not use those
mirrors: it fetches packages from each provider's origin registry unless it is
told otherwise. Without a mirror setting, an upgrade on such a runner therefore
fails at the lock step and restores the original lock file.

Set `terraform.fs_mirror` (a directory) or `terraform.net_mirror` (an HTTPS base
URL), or pass `--fs-mirror` / `--net-mirror` to `provlift upgrade`, and provlift
passes it to `providers lock` as `-fs-mirror` or `-net-mirror`. Only one may be
set, as the command accepts a single installation method. A flag replaces the
configured mirror. A relative `fs_mirror` is read from the repository root and a
relative `--fs-mirror` from the working directory; both are passed as absolute
paths because `providers lock` runs inside each root module.

The mirror must hold packages for every platform in `platforms`, or the lock
step fails for the missing ones. provlift's own version lookups (`check` and
upgrade planning) still query the registry API directly; mirror support for
those is tracked in [#20](https://github.com/provlift/provlift/issues/20).

---
