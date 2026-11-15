# Frequently Asked Questions & Tool Comparison

This guide compares `provlift` with other dependency-management and Terraform tools, explains how to run them together, and answers common operational questions.

---

## 1. Tool comparison

Teams managing Terraform and OpenTofu dependencies frequently ask how `provlift` compares to general-purpose dependency bots ([Renovate](https://docs.renovatebot.com/modules/manager/terraform/), [Dependabot](https://docs.github.com/en/code-security/dependabot/dependabot-version-updates/configuration-options-for-the-dependabot.yml-file#package-ecosystem)) or specialized CLI formatters ([tfupdate](https://github.com/minamijoyo/tfupdate)).

| Capability | `provlift` | [Renovate](https://docs.renovatebot.com/modules/manager/terraform/) | [Dependabot](https://docs.github.com/en/code-security/dependabot/) | [tfupdate](https://github.com/minamijoyo/tfupdate) |
|---|---|---|---|---|
| **Primary focus** | Provider upgrades & constraint resolution | Universal dependency management | GitHub-native dependency alerts & PRs | Terraform CLI and provider version string rewriting |
| **Dependency-ceiling awareness** | **Yes** — scans local and downloaded module trees to avoid proposing versions blocked by dependencies | No — evaluates dependencies individually without whole-tree constraint checking | No — evaluates files individually | No — rewrites versions blindly to match CLI flags |
| **Constraint shape handling** | **Preserves style** — adjusts `~>`, `>=`, or `= ` with minimal surgical diffs | Can update ranges or pin versions | Can update ranges or pin versions | Regex/AST replacement according to flag style |
| **Multi-platform lock files** | **Yes** — runs `providers lock` for every configured platform (`darwin_arm64`, `linux_amd64`, etc.) | Optional (requires running container with Terraform/OpenTofu configured) | Single platform or basic lock generation | No — does not manage `.terraform.lock.hcl` |
| **Root vs shared module semantics** | **Yes** — updates lock files for root modules and adjusts constraint floors for shared modules | Treats all Terraform blocks similarly | Treats all Terraform blocks similarly | Treats all files matching glob identically |
| **Module version upgrades** | No (strictly scoped to providers) | **Yes** (Terraform registry and Git module sources) | **Yes** (Terraform registry and Git module sources) | **Yes** (Terraform module sources) |
| **Forge integration** | CLI / CI-driven — outputs Git branches, commit messages, and PR markdown; no forge API tokens required | Deep GitHub/GitLab/Bitbucket bot integration with hosted automation | GitHub-native automated bot PRs | CLI-only |
| **Offline drift detection** | **Yes** — `provlift drift` detects constraint divergence, missing locks, and skew without network | No | No | No |
| **OpenTofu support** | **Native** — reads `.provlift.yaml` or `--terraform-binary tofu`, resolves to OpenTofu registry | Supported via configuration | Limited / Terraform-centric | Terraform syntax |

---

## 2. Running alongside Renovate or Dependabot

`provlift` is deliberately designed to complement existing tools rather than replace them entirely. While Renovate and Dependabot excel at tracking and updating Terraform **module** versions, `provlift` provides provider-specific constraint solving, dependency ceiling inspection, and multi-platform checksum generation.

### Running alongside Renovate

When using Renovate to upgrade Terraform modules, configure Renovate to ignore provider dependencies so that `provlift` can handle provider upgrades without conflicting pull requests.

In your `renovate.json` (or `.github/renovate.json5`):

```json
{
  "$schema": "https://docs.renovatebot.com/renovate-schema.json",
  "extends": [
    "config:recommended"
  ],
  "packageRules": [
    {
      "description": "Disable provider updates in Renovate; provlift manages provider constraints and multi-platform locks",
      "matchDatasources": ["terraform-provider"],
      "enabled": false
    }
  ]
}
```

Renovate continues to detect and submit pull requests for Terraform modules (datasource `terraform-module`), while `provlift` manages provider updates via scheduled CI workflows or local CLI runs.

### Running alongside Dependabot

Dependabot uses `package-ecosystem: "terraform"` to check both Terraform providers and modules. If you want Dependabot to manage only module versions (or security advisories):

In your `.github/dependabot.yml`:

```yaml
version: 2
updates:
  - package-ecosystem: "terraform"
    directory: "/"
    schedule:
      interval: "weekly"
    ignore:
      # Ignore registry providers so provlift can manage constraint resolution and lock hashes
      # Provider coordinates follow the pattern <namespace>/<provider-name>
      - dependency-name: "hashicorp/*"
      - dependency-name: "integrations/*"
      - dependency-name: "cloudflare/*"
```

Alternatively, if you prefer `provlift` to manage all Terraform dependency pull requests via a CI action (see [CI/CD workflows](../ci-cd/README.md)), you can omit the `terraform` ecosystem from Dependabot entirely.

---

## 3. Why aren't modules upgraded?

`provlift` manages **provider** dependencies only. It does not update module versions (`source = "..."` and `version = "..."` within `module` blocks).

This design decision is intentional:

1. **Semantic differences**: A provider is an engine plugin running under Terraform's RPC protocol. Upgrading a provider from `5.1.0` to `5.2.0` rarely changes resource schemas. In contrast, changing a module from `v2.0.0` to `v3.0.0` or even `v2.1.0` often changes input variables, outputs, and sub-resource definitions that require manual code refactoring.
2. **Whole-repository safety**: Automating module bumps without reviewing the module's input arguments can silently lead to invalid Terraform plans or resource recreation.
3. **Specialized tool division**: Tools like [Renovate](https://docs.renovatebot.com/modules/manager/terraform/) are already built to parse git tags, semantic release notes, and module registries. `provlift` focuses on what those tools miss: cross-module provider constraint ceilings and multi-platform `.terraform.lock.hcl` generation.

---

## 4. Common questions

### Does `provlift` need cloud credentials? Does it run `apply`?

**No.** `provlift` never runs `apply` and does not need credentials for AWS, Azure, Google Cloud, Kubernetes, or other infrastructure targets.

- `provlift list` and `provlift drift` are fully offline: they parse HCL files on disk and require neither network nor Terraform.
- `provlift check` queries the public or private provider registry HTTP APIs to read release lists.
- `provlift upgrade` and `provlift validate` run `terraform init -backend=false`, `terraform providers lock`, and `terraform validate`. They do not connect to remote state backends or cloud provider APIs.

See [Prerequisites](../installation/README.md#2-prerequisites) and [Authentication](../authentication/README.md) for private registry tokens.

### Why did `upgrade` pick a version below `LATEST`?

When `provlift check` shows a newer version available (e.g. `LATEST: 5.40.0`), `provlift upgrade` might stop at an earlier version (e.g. `5.25.0`) for several reasons:

1. **Dependency ceilings**: A local shared module or a downloaded external module in `.terraform/modules` specifies an upper bound constraint like `< 5.26.0`. `provlift` detects this ceiling and refuses to plan a version that would cause `terraform init` to fail.
2. **Upgrade budget flags**: Flags like `--level patch`, `--level minor`, or `--exact-level` constrain the upgrade scope.
3. **Major version guardrails**: `provlift` never upgrades across major version boundaries unless `--allow-major` or `--next-major` is explicitly specified.

See [Dependency ceilings](../architecture/README.md#dependency-ceilings), [Incremental upgrades](../upgrade/README.md#incremental-upgrades) and [Major upgrades](../upgrade/README.md#78-major-upgrades).

### Why is there no lock file in my shared module?

In Terraform and OpenTofu's architecture, `.terraform.lock.hcl` is only created and evaluated for **root configurations** (directories containing backend configuration and state where `terraform init` is run).

Shared or reusable modules (modules called via `module { source = "..." }`) define constraints in `required_providers` blocks, but they do not maintain their own `.terraform.lock.hcl`. When you run `provlift upgrade`, lock files are created or updated only in discovered root modules.

See [Root vs shared modules](../upgrade/README.md) and [Troubleshooting](../troubleshooting/README.md).
