# JSON Output Reference

Every `provlift` command supports `--json` to produce machine-readable output for CI/CD pipelines, agent automations, and custom tooling.

Version constraint operators (`~>`, `>=`, `<`, `<=`, `!=`) are rendered literally without HTML escaping (no `\u003e` or `\u003c`).

---

## Compatibility promise

The JSON schema follows semantic versioning:
- **Stable fields**: All top-level and nested fields documented on this page are considered public API. Existing keys and their semantic types will not be renamed or removed in minor or patch releases.
- **Additions**: New optional or informational fields may be added in minor releases. Consumers should ignore unrecognized keys.
- **Breaking changes**: Key removals, structural type changes, or semantic changes to existing fields will only occur in major version bumps (e.g., `1.0.0` or `2.0.0`).

---

## 1. `provlift list --json`

Reports all declared provider requirements across all discovered modules in the repository.

### Example

```json
{
  "diagnostics": null,
  "providers": [
    {
      "module": ".",
      "is_root": true,
      "local_name": "azurerm",
      "source": "registry.terraform.io/hashicorp/azurerm",
      "constraint": "~> 4.65",
      "locked": "4.65.0",
      "declared_at": "main.tf:10",
      "ignored": false
    },
    {
      "module": ".",
      "is_root": true,
      "local_name": "github",
      "source": "registry.terraform.io/integrations/github",
      "constraint": "~> 6.0",
      "locked": "6.2.1",
      "declared_at": "main.tf:5",
      "ignored": false
    },
    {
      "module": "modules/network",
      "is_root": false,
      "local_name": "azurerm",
      "source": "registry.terraform.io/hashicorp/azurerm",
      "constraint": ">= 4.0, < 5.0",
      "locked": "",
      "declared_at": "modules/network/main.tf:5",
      "ignored": false
    }
  ],
  "root": "/home/user/infrastructure"
}
```

### Field specification

| Field | Type | Description | Nullable / Optional |
|---|---|---|---|
| `root` | `string` | Absolute path of the repository root | Required |
| `providers` | `array` | List of provider requirements found (`null` if none) | Required / Nullable |
| `providers[].module` | `string` | Module directory relative to repository root (`.` for root) | Required |
| `providers[].is_root` | `boolean` | `true` if this module can be initialized by Terraform directly; `false` if called as a child module | Required |
| `providers[].local_name` | `string` | Local alias name of the provider in configuration | Required |
| `providers[].source` | `string` | Fully-qualified provider address | Required |
| `providers[].constraint` | `string` | Constraint string declared in `required_providers` (e.g. `~> 4.65`), or empty string if unconstrained | Required |
| `providers[].locked` | `string` | Currently locked version recorded in `.terraform.lock.hcl`, or empty string if unlocked | Required |
| `providers[].declared_at` | `string` | File and line of the declaration (`<file>:<line>`) | Required |
| `providers[].ignored` | `boolean` | `true` if the provider matches `providers.ignore` in repository policy | Required |
| `diagnostics` | `array` | Non-fatal scan diagnostics (`severity`, `summary`, `detail`, `position`), or `null` when none | Required / Nullable |

---

## 2. `provlift check --json`

Queries the provider registries to reconcile current versions against available releases.

### Example

```json
{
  "diagnostics": null,
  "providers": [
    {
      "module": ".",
      "local_name": "azurerm",
      "source": "registry.terraform.io/hashicorp/azurerm",
      "constraint": "~> 4.65",
      "position": {
        "file": "main.tf",
        "line": 10
      },
      "current": "4.65.0",
      "current_source": "lock",
      "latest": "5.6.0",
      "latest_minor": "4.81.0",
      "latest_within_constraint": "4.81.0",
      "available_upgrade": "major",
      "blocked_by_constraint": true,
      "blocked_by_dependency": false
    },
    {
      "module": ".",
      "local_name": "github",
      "source": "registry.terraform.io/integrations/github",
      "constraint": "~> 6.0",
      "position": {
        "file": "main.tf",
        "line": 5
      },
      "current": "6.2.1",
      "current_source": "lock",
      "latest": "6.13.0",
      "latest_patch": "6.2.3",
      "latest_minor": "6.13.0",
      "latest_within_constraint": "6.13.0",
      "available_upgrade": "minor",
      "blocked_by_constraint": false,
      "blocked_by_dependency": false
    }
  ],
  "root": "/home/user/infrastructure",
  "upgrades_available": 2
}
```

### Field specification

| Field | Type | Description | Nullable / Optional |
|---|---|---|---|
| `root` | `string` | Absolute path of the repository root | Required |
| `providers` | `array` | Reconciled upgrade status for each provider requirement (`null` if none) | Required / Nullable |
| `providers[].module` | `string` | Relative path of the module | Required |
| `providers[].local_name` | `string` | Local alias name | Required |
| `providers[].source` | `string` | Fully-qualified provider address | Required |
| `providers[].constraint` | `string` | Version constraint string | Optional (`omitempty`) |
| `providers[].position` | `object` | Source location: `file` (`string`) and `line` (`integer`) | Required |
| `providers[].current` | `string` | Currently resolved version (from lockfile or constraint floor) | Optional (`omitempty`) |
| `providers[].current_source` | `string` | How `current` was established: `"lock"`, `"constraint-floor"`, or `"unknown"` | Required |
| `providers[].latest` | `string` | Newest release available in the registry | Optional (`omitempty`) |
| `providers[].latest_patch` | `string` | Newest patch version in the same minor series | Optional (`omitempty`) |
| `providers[].latest_minor` | `string` | Newest minor version in the same major series | Optional (`omitempty`) |
| `providers[].latest_within_constraint` | `string` | Newest version that satisfies the declared constraint | Optional (`omitempty`) |
| `providers[].available_upgrade` | `string` | Best upgrade level reachable ignoring constraints: `"none"`, `"patch"`, `"minor"`, or `"major"` | Required |
| `providers[].blocked_by_constraint` | `boolean` | `true` if a newer version exists but is forbidden by the module's own constraint | Required |
| `providers[].external_constraint` | `string` | Constraint terms imposed by dependencies outside this module | Optional (`omitempty`) |
| `providers[].latest_reachable` | `string` | Newest version permitted by `external_constraint` | Optional (`omitempty`) |
| `providers[].external_sources` | `array` | Modules imposing `external_constraint` (`[]string`) | Optional (`omitempty`) |
| `providers[].blocked_by_dependency` | `boolean` | `true` if a dependency's constraint forbids the newest version | Required |
| `providers[].error` | `string` | Registry lookup error message if lookup failed | Optional (`omitempty`) |
| `upgrades_available` | `integer` | Count of requirements where an upgrade within `--level` is available | Required |
| `diagnostics` | `array` | Non-fatal scan diagnostics, or `null` when none | Required / Nullable |

---

## 3. `provlift drift --json`

Reports cross-module inconsistencies across version locks, constraints, and unpinned roots.

### Example

```json
{
  "count": 3,
  "diagnostics": null,
  "findings": {
    "version_skew": [
      {
        "source": "registry.terraform.io/hashicorp/azurerm",
        "versions": [
          {
            "version": "4.65.0",
            "modules": [
              "envs/staging"
            ]
          },
          {
            "version": "4.81.0",
            "modules": [
              "envs/prod"
            ]
          }
        ],
        "oldest": "4.65.0",
        "newest": "4.81.0",
        "span": "minor"
      }
    ],
    "constraint_divergence": [
      {
        "source": "registry.terraform.io/hashicorp/azurerm",
        "constraints": [
          {
            "constraint": ">= 4.0, < 5.0",
            "modules": [
              "envs/prod"
            ]
          },
          {
            "constraint": "~> 4.65",
            "modules": [
              "envs/sandbox",
              "envs/staging"
            ]
          }
        ]
      }
    ],
    "unpinned_modules": [
      {
        "module": "envs/sandbox",
        "providers": [
          "hashicorp/azurerm"
        ]
      }
    ]
  },
  "root": "/home/user/infrastructure"
}
```

### Field specification

| Field | Type | Description | Nullable / Optional |
|---|---|---|---|
| `root` | `string` | Absolute path of the repository root | Required |
| `findings` | `object` | Grouped drift findings | Required |
| `findings.version_skew` | `array` | Providers locked at different versions in different root modules (`null` if none) | Required / Nullable |
| `findings.version_skew[].source` | `string` | Fully-qualified provider address | Required |
| `findings.version_skew[].versions` | `array` | List of version usages: `version` (`string`) and `modules` (`[]string`) | Required |
| `findings.version_skew[].oldest` | `string` | Lowest locked version | Required |
| `findings.version_skew[].newest` | `string` | Highest locked version | Required |
| `findings.version_skew[].span` | `string` | Spread level: `"patch"`, `"minor"`, `"major"`, or `"unknown"` | Required |
| `findings.constraint_divergence` | `array` | Providers declared with differing constraints (`null` if none) | Required / Nullable |
| `findings.constraint_divergence[].source` | `string` | Fully-qualified provider address | Required |
| `findings.constraint_divergence[].constraints` | `array` | Constraint usages: `constraint` (`string`) and `modules` (`[]string`) | Required |
| `findings.unpinned_modules` | `array` | Root modules with declared providers but no `.terraform.lock.hcl` (`null` if none) | Required / Nullable |
| `findings.unpinned_modules[].module` | `string` | Relative module path | Required |
| `findings.unpinned_modules[].providers` | `array` | Display addresses of providers declared in the unpinned root (`[]string`) | Required |
| `count` | `integer` | Total number of drift findings | Required |
| `diagnostics` | `array` | Non-fatal scan diagnostics, or `null` when none | Required / Nullable |

---

## 4. `provlift upgrade --json`

Executes or plans (`--dry-run`) provider updates across HCL declarations and lock files, and emits the resulting plan, file edits, Terraform verification runs, module version changes, and pull request metadata.

### Example

```json
{
  "changed_files": [
    "main.tf",
    ".terraform.lock.hcl"
  ],
  "dry_run": false,
  "module_versions": {
    "changes": [],
    "no_baseline": [],
    "unreadable": []
  },
  "plan": {
    "actions": [
      {
        "module": ".",
        "local_name": "azurerm",
        "source": "registry.terraform.io/hashicorp/azurerm",
        "from": "4.65.0",
        "to": "5.6.0",
        "level": "major",
        "breaking": true,
        "constraint": {
          "old": "~> 4.65",
          "new": "~> 5.0",
          "changed": true,
          "shape": "pessimistic",
          "reason": "pessimistic constraint retargeted at the new version series"
        }
      }
    ]
  },
  "pull_request": {
    "branch": "provlift/hashicorp-azurerm-5.6.0",
    "title": "Upgrade hashicorp/azurerm to 5.6.0",
    "body": "## Changes\n\n| Module | Provider | From | To | Level | Constraint |\n|---|---|---|---|---|---|\n| `.` | `hashicorp/azurerm` | 4.65.0 | 5.6.0 | major | `~> 4.65` -> `~> 5.0` |\n\n## Breaking changes\n\nThis upgrade crosses a boundary where the provider may introduce breaking\nchanges. Review the upgrade guide before merging.\n\n- [hashicorp/azurerm 5.6.0 documentation](https://registry.terraform.io/providers/hashicorp/azurerm/5.6.0/docs)\n\n## Files changed\n\n- `main.tf`\n- `.terraform.lock.hcl`\n\nThe dependency lock files are regenerated by Terraform itself, so their\nchecksums cover every configured platform.\n\n## Verification\n\n| Module | Command | Result |\n|---|---|---|\n| `.` | `terraform fmt` | :white_check_mark: passed |\n| `.` | `terraform init -upgrade` | :white_check_mark: passed |\n| `.` | `terraform providers lock` | :white_check_mark: passed |\n| `.` | `terraform validate` | :white_check_mark: passed |\n\n---\n\nRaised by `provlift`. Version constraints were edited only where they would otherwise block the target version; everything else was left untouched.\n",
    "labels": [
      "dependencies",
      "terraform",
      "breaking-change",
      "provider-major"
    ]
  },
  "terraform_runs": [
    {
      "module": ".",
      "command": "fmt",
      "exit_code": 0,
      "ok": true
    },
    {
      "module": ".",
      "command": "init -upgrade",
      "exit_code": 0,
      "ok": true
    },
    {
      "module": ".",
      "command": "providers lock",
      "exit_code": 0,
      "ok": true
    },
    {
      "module": ".",
      "command": "validate",
      "exit_code": 0,
      "ok": true
    }
  ]
}
```

### Field specification

| Field | Type | Description | Nullable / Optional |
|---|---|---|---|
| `dry_run` | `boolean` | `true` when `--dry-run` was set and no files were modified | Required |
| `plan` | `object` | Planned actions and skipped providers | Required |
| `plan.actions` | `array` | Provider changes planned or applied (`[]` when none) | Required |
| `plan.actions[].module` | `string` | Relative path of the target module | Required |
| `plan.actions[].local_name` | `string` | Local alias name of the provider | Required |
| `plan.actions[].source` | `string` | Fully-qualified provider address | Required |
| `plan.actions[].from` | `string` | Previous version before the upgrade | Required |
| `plan.actions[].to` | `string` | Target version after the upgrade | Required |
| `plan.actions[].level` | `string` | Upgrade level: `"none"`, `"patch"`, `"minor"`, or `"major"` | Required |
| `plan.actions[].breaking` | `boolean` | `true` if the upgrade crosses a breaking boundary (major bump or `0.x` minor bump) | Required |
| `plan.actions[].constraint_only` | `boolean` | `true` when `--raise-floor` updated the constraint without changing the resolved version | Optional (`omitempty`) |
| `plan.actions[].constraint` | `object` | Constraint rewrite details | Required |
| `plan.actions[].constraint.old` | `string` | Original constraint string | Required |
| `plan.actions[].constraint.new` | `string` | Rewritten constraint string | Required |
| `plan.actions[].constraint.changed` | `boolean` | `true` if the constraint string was modified | Required |
| `plan.actions[].constraint.shape` | `string` | Structural form: `"empty"`, `"exact"`, `"pessimistic"`, `"lower-bound"`, `"range"`, or `"unsupported"` | Required |
| `plan.actions[].constraint.reason` | `string` | Explanation of why the constraint was or was not modified | Required |
| `plan.skipped` | `array` | Providers considered but not upgraded | Optional (`omitempty`) |
| `plan.skipped[].module` | `string` | Relative path of the module | Required |
| `plan.skipped[].local_name` | `string` | Local alias name of the provider | Required |
| `plan.skipped[].source` | `string` | Fully-qualified provider address | Required |
| `plan.skipped[].reason` | `string` | Explanation of why the provider was skipped | Required |
| `changed_files` | `array` | Repository-relative paths of all modified HCL and lock files (`[]` when none) | Required |
| `terraform_runs` | `array` | Terraform commands executed during the upgrade (`[]` on `--dry-run` or `--skip-init`) | Required |
| `terraform_runs[].module` | `string` | Relative path of the root module where the command ran | Required |
| `terraform_runs[].command` | `string` | Terraform subcommand executed (e.g. `"fmt"`, `"init -upgrade"`, `"providers lock"`, `"validate"`) | Required |
| `terraform_runs[].exit_code` | `integer` | Process exit code returned by Terraform | Required |
| `terraform_runs[].ok` | `boolean` | `true` if the command succeeded | Required |
| `terraform_runs[].detail` | `string` | Error or diagnostic summary when `ok` is `false` | Optional (`omitempty`) |
| `module_versions` | `object` | Module selection changes observed during `init -upgrade` | Required |
| `module_versions.changes` | `array` | Module calls whose resolved version changed during `init -upgrade` (`[]` when none) | Required |
| `module_versions.changes[].root` | `string` | Root module where `init -upgrade` ran | Required |
| `module_versions.changes[].module` | `string` | Module call key in `.terraform/modules/modules.json` | Required |
| `module_versions.changes[].source` | `string` | Module source address | Required |
| `module_versions.changes[].from` | `string` | Previously resolved module version | Required |
| `module_versions.changes[].to` | `string` | Newly resolved module version | Required |
| `module_versions.no_baseline` | `array` | Root modules calling versioned modules that had no prior `.terraform/modules/modules.json` (`[]string`) | Required |
| `module_versions.unreadable` | `array` | Root modules whose post-init module manifest could not be read (`[]` when none) | Required |
| `module_versions.unreadable[].root` | `string` | Root module path | Required |
| `module_versions.unreadable[].error` | `string` | Read or parse error message | Required |
| `pull_request` | `object` | Generated pull request metadata | Required |
| `pull_request.branch` | `string` | Deterministic branch name (e.g. `"provlift/hashicorp-azurerm-5.6.0"`) | Required |
| `pull_request.title` | `string` | Pull request title | Required |
| `pull_request.body` | `string` | Markdown pull request description | Required |
| `pull_request.labels` | `array` | Suggested pull request labels (`[]string`) | Required |

---

## 5. `provlift validate --json`

Runs formatting checks, initialization, validation, and optional planning across root modules.

### Example

```json
{
  "failures": [],
  "runs": [
    {
      "module": ".",
      "command": "fmt -check",
      "exit_code": 0,
      "ok": true
    },
    {
      "module": ".",
      "command": "init",
      "exit_code": 0,
      "ok": true
    },
    {
      "module": ".",
      "command": "validate",
      "exit_code": 0,
      "ok": true
    }
  ]
}
```

### Field specification

| Field | Type | Description | Nullable / Optional |
|---|---|---|---|
| `runs` | `array` | List of Terraform command executions (`null` if no root modules matched) | Required / Nullable |
| `runs[].module` | `string` | Relative path of the root module where the command ran | Required |
| `runs[].command` | `string` | Terraform subcommand executed (`"fmt -check"`, `"init"`, `"validate"`, or `"plan"`) | Required |
| `runs[].exit_code` | `integer` | Process exit code returned by Terraform | Required |
| `runs[].ok` | `boolean` | `true` if the command succeeded | Required |
| `runs[].detail` | `string` | Diagnostic or error summary when `ok` is `false` | Optional (`omitempty`) |
| `failures` | `array` | Failure summary strings (`"<module> (<command>): <detail>"`), or `[]` when all commands succeed | Required |

---

## 6. `provlift version --json`

Prints build and toolchain metadata.

### Example

```json
{
  "commit": "a1b2c3d",
  "date": "2026-11-08T22:00:00Z",
  "go": "go1.26.6",
  "os_arch": "windows_amd64",
  "version": "0.3.0"
}
```

### Field specification

| Field | Type | Description | Nullable / Optional |
|---|---|---|---|
| `version` | `string` | Semantic version of `provlift` (`"dev"` on local builds) | Required |
| `commit` | `string` | Git commit SHA stamped at build time (`"none"` on local builds) | Required |
| `date` | `string` | Build timestamp stamped at build time (`"unknown"` on local builds) | Required |
| `go` | `string` | Go toolchain version used to compile the binary (e.g. `"go1.26.6"`) | Required |
| `os_arch` | `string` | Target operating system and architecture (`"<GOOS>_<GOARCH>"`, e.g. `"windows_amd64"`, `"linux_amd64"`) | Required |
