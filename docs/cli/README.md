# CLI Reference

## 6. CLI reference

| Command | Purpose | Network | Writes files |
|---|---|---|---|
| `provlift list` | Inventory of declared providers | No | No |
| `provlift check` | Available upgrades, classified | Registry only | No |
| `provlift drift` | Inconsistencies between modules | No | No |
| `provlift upgrade` | Apply upgrades | Registry + providers | Yes |
| `provlift validate` | `fmt -check` + `validate` (+ optional `plan`) | Providers | No |
| `provlift version` | Build information | No | No |

### 6.1 Global flags

Available on every command.

| Flag | Default | Description |
|---|---|---|
| `-C`, `--dir` | `.` | Repository directory to scan |
| `--json` | `false` | Emit machine-readable JSON instead of a table |
| `--exclude` | none | Glob of directories to skip (repeatable; a comma is part of the glob, as in `modules/{a,b}`) |
| `--terraform-binary` | `terraform` | Executable to drive, for example `tofu` for OpenTofu; also `PROVLIFT_TERRAFORM_BIN` |
| `--timeout` | `15m` | Timeout for a single Terraform command |
| `--include-prerelease` | `false` | Consider prerelease provider versions |

Use the command-specific sections below for complete invocations and examples.
Cobra also supplies `provlift help <command>` and `provlift completion <shell>`
for shell autocompletion.

### 6.2 `provlift list`

Fully offline. Shows every `required_providers` entry, the module it lives in,
whether that module is a root or a child, and the exact file and line.

```
MODULE           KIND   NAME    SOURCE               CONSTRAINT     LOCKED   DECLARED AT
.                root   aws     hashicorp/aws        >= 5.0, < 6.0  5.82.2   versions.tf:13
.                root   github  integrations/github  ~> 6.0         6.2.1    versions.tf:8
modules/network  child  aws     hashicorp/aws        ~> 5.0         -        versions.tf:5
```

**Columns**

- `KIND` — `root` if Terraform can initialise the directory, `child` if another
  module calls it via a local `source`.
- `SOURCE` — the provider address. The public Terraform registry hostname is
  omitted; any other registry keeps its host, for example
  `tf.example.com/platform/internal`.
- `CONSTRAINT` — the declared `version` argument; `-` means none is declared.
- `LOCKED` — the version recorded in `.terraform.lock.hcl`; `-` means the module
  has never been initialised.
- `DECLARED AT` — the file and line to edit by hand if needed, relative to the
  module in the `MODULE` column.

**Flags**

| Flag | Default | Description |
|---|---|---|
| `--provider` | all | Only show these providers (repeatable) |
| `--roots-only` | `false` | Only show root modules |
| `--show-ignored` | `false` | Include providers excluded by configuration |

**Examples**

```powershell
# Everything in the repository
provlift list

# One provider, root modules only
provlift list --provider aws --roots-only

# Include providers excluded by .provlift.yaml, marked "(ignored)"
provlift list --show-ignored

# Machine-readable inventory, with fully qualified addresses
provlift list --json
```

### 6.3 `provlift check`

Compares the current version against the registry and classifies the gap.

```
MODULE  SOURCE               CONSTRAINT     CURRENT  PATCH  MINOR    LATEST  UPGRADE  BLOCKED
.       hashicorp/aws        >= 5.0, < 6.0  5.82.2   -      5.100.0  6.20.0  major    yes
.       integrations/github  ~> 6.0         6.2.1    6.2.3  6.13.0   6.13.0  minor    -
```

`BLOCKED` means a newer version exists but the declared constraint forbids it.

Use `--exit-code` in CI to exit `2` when upgrades are available.

**Columns**

- `CURRENT` — the locked version. Where a module has no lock file this is the
  **constraint floor** instead, suffixed `(floor)`; see [section 7.2](../upgrade/README.md#72-current-version).
- `PATCH` / `MINOR` — the newest version reachable at that level.
- `LATEST` — the newest published version overall.
- `UPGRADE` — the level of the newest version overall.
- `BLOCKED` — what stands in the way of the newest version:
  - `yes` — this repository's own constraint forbids it.
  - `dep` — a **dependency** forbids it. Constraints intersect across the whole
    module tree, so a ceiling inside a shared module binds this repository
    even though nothing here declares it. The blocking modules and their
    pinned versions are named below the table.

**Flags**

| Flag | Default | Description |
|---|---|---|
| `--provider` | all | Only check these providers (repeatable) |
| `--module` | all | Only check these module paths (repeatable, glob) |
| `--level` | `major` | Highest level to count: `patch`, `minor` or `major` |
| `--exit-code` | `false` | Exit `2` when upgrades are available, for CI gating |

**Examples**

```powershell
# Everything that could be upgraded
provlift check

# Only count patch upgrades, and fail CI if any exist
provlift check --level patch --exit-code

# One provider across one subtree
provlift check --provider hashicorp/google --module "platform/*"

# A provider from a private registry, by its full address
provlift check --provider tf.example.com/platform/internal

# Machine-readable, for a dashboard or a scheduled job
provlift check --json
```

### 6.4 `provlift drift`

Compares modules against each other rather than against a registry. Entirely
offline: no network, no credentials.

It reports three kinds of inconsistency:

| Finding | Meaning |
|---|---|
| **Version skew** | The same provider is locked at different versions in different modules |
| **Constraint divergence** | The same provider is constrained differently in different modules |
| **Unpinned root modules** | A root module records no `.terraform.lock.hcl`, so nothing fixes what it installs |

Only **root** modules are reported as unpinned. A shared module deliberately
has no lock file and is never flagged — see
[which modules should commit a lock file](../upgrade/README.md#which-modules-should-commit-a-lock-file).

Version skew is usually the one that matters. Two environments sharing the same
configuration and the same constraint can still run different provider
versions, purely because they were initialised at different times. Nothing in
the configuration reveals that.

**Flags**

| Flag | Default | Description |
|---|---|---|
| `--provider` | all | Only report on these providers (repeatable) |
| `--exit-code` | `false` | Exit `2` when inconsistencies are found |

**Output**

```
Version skew
------------
SOURCE            RANGE             SPAN   VERSIONS
hashicorp/google  5.44.0 .. 6.12.0  MAJOR  3

  hashicorp/google
    5.44.0  envs/staging
            envs/prod-eu
    6.8.0   envs/prod-us
    6.12.0  envs/dev
```

`SPAN` classifies the distance between the oldest and newest locked version, so
a split across a major boundary stands out from routine patch drift.

Constraint divergence ignores differences Terraform does not see: `>=4.16` and
`>= 4.16.0`, `6.0.1` and `= 6.0.1`, or `>= 4, < 5` and `< 5, >= 4` are treated
as the same constraint. Each is shown as one of the modules wrote it.

**Examples**

```powershell
# Everything
provlift drift

# One provider
provlift drift --provider google

# Fail a CI job when modules disagree (exit 2)
provlift drift --exit-code

# Machine-readable findings
provlift drift --json
```

### 6.5 `provlift upgrade`

Plans and applies upgrades, then hands the lock file to Terraform (or OpenTofu).

**Flags**

| Flag | Default | Description |
|---|---|---|
| `--provider` | all | Only upgrade these providers (repeatable) |
| `--module` | all | Only upgrade these module paths (repeatable, glob) |
| `--level` | `patch` | Highest level to apply: `patch`, `minor` or `major` |
| `--exact-level` | `false` | Pick the next published release at exactly the selected level; use for progressive stages |
| `--to` | none | Upgrade to this exact version; requires a single `--provider` |
| `--allow-major` | `false` | Acknowledge that major upgrades may break |
| `--next-major` | `false` | Select the first published release in the next major series |
| `--pin-unconstrained` | `false` | Add a constraint to providers that declare none |
| `--raise-floor` | `false` | Raise the constraint's supported minimum (see [7.2](../upgrade/README.md#72-current-version)) |
| `--dry-run` | `false` | Show the plan without changing any file |
| `--skip-init` | `false` | Do not run Terraform; leaves the lock file stale |
| `--skip-validate` | `false` | Do not run `terraform validate` afterwards |
| `--backend` | `false` | Initialise the backend (needs state access and credentials) |
| `--platforms` | from config | Platforms to record lock hashes for |
| `--fmt` | `true` | Run `terraform fmt` on modified directories |
| `--pr-body` | none | Write a pull request description to this file (see [11.3](../ci-cd/README.md#113-raising-pull-requests-automatically)) |

**Examples**

```powershell
# Preview only, change nothing
provlift upgrade --level patch --dry-run

# Next minor version of one provider
provlift upgrade --provider aws --level minor --exact-level

# Deliberate major upgrade
provlift upgrade --provider integrations/github --level major --exact-level --allow-major

# Cross a multi-major gap one series at a time
provlift upgrade --provider azurerm --level major --allow-major --next-major

# Exact version
provlift upgrade --provider hashicorp/google --to 6.12.0 --level minor

# Limit the blast radius to one subtree
provlift upgrade --level patch --module "envs/*"

# Lock for Linux CI only, to avoid downloading every platform
provlift upgrade --level patch --platforms linux_amd64

# Raise a shared module's supported minimum
provlift upgrade --provider aws --level major --exact-level --allow-major --raise-floor

# Add a constraint to a provider that declares none
provlift upgrade --provider kubernetes --level minor --pin-unconstrained

# Drive OpenTofu instead of Terraform
provlift upgrade --level patch --terraform-binary tofu

# Edit constraints only; leaves the lock file stale, so use with care
provlift upgrade --level patch --skip-init

# Regenerate the lock file but skip validation
provlift upgrade --level patch --skip-validate

# Initialise the backend, when state access is genuinely needed
provlift upgrade --level patch --backend

# Leave formatting alone
provlift upgrade --level patch --fmt=false

# Machine-readable result for CI
provlift upgrade --level patch --json
```

To preview the next patch, minor, and major stage for one root module in
another repository (replace the paths as needed):

```bash
provlift upgrade -C ../infra-live --module envs/prod \
  --provider aws --level patch --exact-level --dry-run
provlift upgrade -C ../infra-live --module envs/prod \
  --provider aws --level minor --exact-level --dry-run
provlift upgrade -C ../infra-live --module envs/prod \
  --provider aws --level major --exact-level --allow-major --dry-run
```

Each dry run starts from the same lock version. To advance patch → minor →
major, apply and review one stage before running the next. A real upgrade
temporarily pins the planned provider and all other locked providers during
Terraform init, then removes the override. It refuses and restores the
original lock if Terraform selects an unplanned version.

**Output**

```
Planned upgrades
----------------
MODULE  SOURCE               FROM   TO     LEVEL  CONSTRAINT        NOTE
.       integrations/github  6.2.1  6.2.3  patch  unchanged         -
.       hashicorp/random     2.3.1  3.0.0  major  ~> 2.0 -> ~> 3.0  BREAKING

Skipped
-------
MODULE  SOURCE             REASON
.       hashicorp/azurerm  already at the newest patch-level version (4.81.0)
```

`CONSTRAINT: unchanged` is the common and desirable case: the declared
constraint already permitted the new version, so only the lock file moves.

The `Skipped` table states a reason for every provider that was not upgraded,
so a run that does nothing still explains itself.

**Live upgrade feedback**

While Terraform runs, provlift writes stage starts, Terraform's human-readable
output, and stage completion or failure with elapsed time to stderr. For example:

```text
Running Terraform (provider downloads may take several minutes)
  [.] terraform init -upgrade ...
Initializing provider plugins...
- Installing hashicorp/azurerm v4.58.0...
  [.] init -upgrade: complete (12.4s)
  [.] terraform providers lock ...
  [.] providers lock: complete (8.2s)
  [.] terraform validate ...
  [.] validate: complete (1.1s)
```

This feedback is also available with `--json`; stdout remains one machine-readable
provlift response, so `provlift upgrade --json > result.json` still works. Terraform's
validation JSON is captured for diagnostics rather than streamed into progress.
Dry runs and `--skip-init` do not start Terraform and produce no Terraform progress.

### 6.6 `provlift validate`

Runs `terraform fmt -check` and `terraform validate` in each root module.

**Flags**

| Flag | Default | Description |
|---|---|---|
| `--module` | all | Only validate these module paths (repeatable, glob) |
| `--plan` | `false` | Also run `terraform plan` |
| `--backend` | `false` | Initialise the backend during validation |
| `--no-init` | `false` | Skip init; assumes `.terraform` is already populated |

`terraform plan` is opt-in because it needs backend access, state and live
provider credentials, and it reaches out to real infrastructure. See
[section 7.7](../upgrade/README.md#77-validation-split).

**Examples**

```powershell
# Format check and validate every root module
provlift validate

# One subtree only
provlift validate --module "envs/*"

# Validate with OpenTofu
provlift validate --terraform-binary tofu

# Full verification, in a job that has credentials
provlift validate --plan --backend

# Reuse an existing .terraform directory
provlift validate --no-init

# Machine-readable results
provlift validate --json
```

### 6.7 `provlift version`

Prints the build version, commit, build date and Go toolchain.

```powershell
provlift version
provlift version --json
```

---

---

## 10. Exit codes

| Code | Meaning |
|---|---|
| `0` | Success |
| `1` | Error, or a refused/unsupported operation (including a missing or timed-out Terraform, or a lock result `upgrade` refuses) |
| `2` | `check --exit-code`: upgrades are available; `drift --exit-code`: inconsistencies found |
| `3` | Terraform reported a failure (`fmt`, `init`, `providers lock`, `validate` or `plan`) |

---
