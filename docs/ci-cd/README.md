# CI/CD Workflows

## 11. CI/CD usage

Because constraints and the lock file are both committed, the resulting diff
goes through normal code review — which is what HashiCorp recommends for the
dependency lock file.

The complete CI workflow examples live under
[`examples/workflows`](../../examples/workflows). The command-specific CLI reference
in section 6 is the single source for `provlift` invocations; the CI section only
describes when to use each command.

### 11.1 Recommended pipeline stages

| Stage | Command | Gate |
|---|---|---|
| Consistency | `provlift drift --json --exit-code` | Offline; flags modules that disagree |
| Report | `provlift check --json --exit-code` | Informational; exit `2` means work exists |
| Auto-patch | `provlift upgrade --level patch` | Raise a PR from the resulting diff |
| Verify | `provlift validate --plan --backend` | Must pass before merge |

### 11.2 Notes for CI

- Provide a registry token via `TF_TOKEN_<host>` only if private registries are
  in use; the public Terraform and OpenTofu registries need none.
- For OpenTofu repositories, install OpenTofu in the job (for example with
  `opentofu/setup-opentofu`) and set `terraform.binary: tofu` in
  `.provlift.yaml` so every stage uses it.
- Keep `--backend=false` (the default) for the upgrade stage so the job does not
  need state access.
- Run `provlift validate --plan` in a job that *does* have cloud credentials.
- Pin the `platforms` list in `.provlift.yaml` so lock files generated in CI match
  those generated on workstations.

### 11.3 Raising pull requests automatically

`provlift` produces the change *and* the description of the change; the pull
request itself is raised by whatever runs it. The tool never asks for a token
and never talks to a forge, which keeps credentials in the workflow where they
belong and keeps the tool usable outside GitHub.

Every `upgrade --json` run emits a `pull_request` object:

```json
{
  "pull_request": {
    "branch": "provlift/hashicorp-aws-5.82.0",
    "title": "Upgrade hashicorp/aws in 7 modules, up to 5.82.0",
    "labels": ["dependencies", "terraform", "provider-minor"],
    "body": "## Changes\n..."
  }
}
```

`--pr-body <file>` writes the body to a file so it can be handed straight to
`gh pr create --body-file` or `create-pull-request`'s `body-path`.

A ready-made workflow is in
[examples/workflows/provider-upgrades.yml](../../examples/workflows/provider-upgrades.yml).
Copy it into a Terraform repository as `.github/workflows/provider-upgrades.yml`.

**One pull request per provider.** The workflow first runs a dry run across the
whole repository, then fans out over the providers that actually have an
upgrade. A patch bump to `random` can therefore be reviewed and merged
without waiting for a contentious `aws` or `azurerm` major.

The workflow passes `--exact-level`, so its stages are genuinely progressive:
patch upgrades are raised first, then minor upgrades, then major upgrades. On a
scheduled run, `auto` tries those exact stages in order and selects the first
one that has work. After the pull request is merged, the next scheduled run
sees the new current version and advances to the next stage. The major stage
also passes `--next-major`, so a provider several major lines behind advances
one major series per reviewed pull request.

Scheduled runs do not authorize major upgrades. To reach the major stage,
manually dispatch `auto` or `major` with `allow_major: true`. Direct `patch`,
`minor`, and `major` dispatches select only that exact stage. The auto sequence
selects the first stage with work across the repository, not independently per
provider. Discovery and the execution step both gate empty plans; PR creation
also requires files changed by the executed upgrade.

The ordinary `--level minor` form permits patch plus minor and now selects the
next release within that budget; `--exact-level` is the explicit choice when
those stages must stay separate.

**The branch name carries the target version**, so:

- re-running an unchanged upgrade updates the open pull request instead of
  opening a duplicate;
- a newer release produces a new branch, rather than silently rewriting an open
  pull request to mean something it was not reviewed as.

**Discovery asks the planner, not the report.** It would be tempting to derive
the provider list in `jq` from `provlift check --json`. Don't. `check` reports the
newest version that *exists*; only the planner knows which of those are actually
reachable. Suppose `google` has a newer patch published, but every module in
the repository is already at the ceiling a shared dependency imposes — deriving
the list from `check` raises a pull request that changes nothing. Running
`upgrade --dry-run --json` and reading `.plan.actions[].source` asks the same
code that will perform the upgrade.

For this reason `upgrade --json` always emits the same shape —
`dry_run`, `plan`, `changed_files`, `terraform_runs`, `pull_request` — including
when there is nothing to do, when an upgrade is refused, and when execution fails.
`changed_files` contains repository-relative constraint edits and lock files
whose contents differ after Terraform runs; locks restored after failure are
excluded. Live Terraform progress, errors, and the stale-lock warning from
`--skip-init` remain on stderr, leaving stdout as one JSON response.

The description explains itself to a reviewer: what moved and to which version,
which constraints were rewritten, which Terraform commands ran and whether they
passed, and — importantly — which modules were *not* upgraded and which
dependency caps them. That last section is what turns "why is this still on
3.117.1?" from an investigation into a sentence.

### 11.4 Shared module repositories need a different workflow

Do not copy §11.3's workflow into a shared module repository such as
`terraform-aws-vpc` or `terraform-azurerm-aks`. A shared module is not a root
configuration, and the Dependabot-shaped question does not apply to it:

- its lock file is gitignored, correctly — the lock belongs to the root
  configuration that consumes the module, not to the module;
- its constraints are open lower bounds such as `>= 3.85.0`, which already
  permit every newer release, so there is nothing to bump.

Run the upgrade job on such a module and the only files it writes are lock
files that git ignores. The pull request is empty. It will be empty every week.

Two different questions are worth asking of a module:

| Question | Operation | Outcome |
|---|---|---|
| Does it still work with the newest provider? | `upgrade` + `validate` | A status check |
| Does it *forbid* the newest provider? | Widen the ceiling, then validate | A pull request |

The second is the one that matters to everyone else, because a ceiling inside a
widely-consumed module caps the provider for every repository downstream of it.
Use [examples/workflows/provider-compatibility.yml](../../examples/workflows/provider-compatibility.yml).

The compatibility example tests one provider, named by the `provider` dispatch
input or the `PROVLIFT_PROVIDER` repository variable (for example
`hashicorp/aws`); the run fails if neither is set or the provider is not
declared in the repository. It validates
the next eligible release at the requested stage, not necessarily the newest
published version. Major runs require `allow_major: true`. A successful run
creates a PR only when the plan changes a provider constraint; generated or
updated lock files alone do not enable PR creation, even in example roots that
commit their locks. Explicit `raise_floor` requests are also constraint changes.

**Raising the floor is a breaking change.** `--raise-floor` moves
`>= 3.85.0` to `>= 3.117.1`, which excludes every consumer still on an older
provider. That deserves a major release of the module, so the workflow leaves it
off by default and puts it behind an explicit dispatch input.

**Vendor the module tree before checking.** A ceiling declared inside a
dependency is invisible until that dependency is on disk. On a fresh checkout of
a module that consumes two other shared modules:

```
MODULE   SOURCE             CONSTRAINT            UPGRADE  BLOCKED
.        hashicorp/azurerm  >= 4.11.0, <= 4.19.0  major    yes
```

After `terraform get`:

```
MODULE   SOURCE             CONSTRAINT            UPGRADE  BLOCKED
.        hashicorp/azurerm  >= 4.11.0, <= 4.19.0  major    dep

hashicorp/azurerm in . is capped at 4.19.0, not by this repository.
  imposed by:  terraform-azurerm-regions v2.0.0
               terraform-azurerm-resource-group v3.0.0
```

`yes` says "fix it here". `dep` says "you cannot fix it here" and names who can.
Skip the vendoring step and the workflow tells you the first thing, which is
wrong. `terraform get` fetches modules only, not providers, so it is cheap and
needs no cloud credentials.

---

### 11.5 Automation acceptance checks

Run from the repository root:

```sh
go test ./examples/workflows ./internal/upgrade ./internal/pullrequest ./internal/cli -count=1
```

The workflow tests execute the YAML shell steps using fake provlift and Terraform
commands. They cover weekly/manual triggers, direct stages, progressive
selection, explicit major acknowledgement, provider deduplication, deterministic
branch wiring, empty-plan handling, validation failure, and compatibility PR
gating. Planner tests cover one-major-at-a-time selection; PR tests cover
versions, constraints, verification, labels, and dependency-blocked modules.

These are workflow templates: copy the appropriate example into the consumer's
`.github/workflows` directory on its default branch to enable scheduled runs.
Pin `PROVLIFT_VERSION` to a reviewed release, configure private repository/module
access, and allow GitHub Actions to create pull requests in that repository.

Local tests do not prove live GitHub PR creation or labels. When adopting a
workflow, dispatch it manually once and verify a PR is created per provider,
reruns reuse its provider/target branch, no-change runs create no PR, and
shared-module compatibility produces no lock-only PR. The workflows open PRs;
they never merge them or apply infrastructure.
