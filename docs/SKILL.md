---
name: provlift-probe
description: >-
  Audit and probe the `provlift` Terraform provider-upgrade CLI. Use when asked to
  verify, test, red-team, or review whether provlift behaves as documented —
  including provider selection, constraint rewriting, lock-file handling,
  dependency ceilings, credential handling, and generated pull-request metadata.
  Covers safe read-only probes and destructive write probes that must be run on a
  throwaway branch.
---

# Probing `provlift`

This skill exists to **falsify** claims, not confirm them. Each section states a
claim the tool makes, the probe that tests it, and what a pass looks like. Report
a finding only when observed behaviour contradicts the stated claim.

Read `docs/architecture/README.md` alongside this file. Section 7 there lists known
limitations — re-reporting one of those is a duplicate, not a discovery.

## Safety rules

1. **Never run write probes against a repository you care about.** Use a
   throwaway branch and confirm `git status --short` is clean first.
2. `list`, `check`, `drift`, `version` are read-only. `upgrade` and
   `validate --plan` are not.
3. `upgrade` runs Terraform, which downloads and executes provider plugins.
4. Never print token values. Assert on presence and destination host only.
5. Prefer `--dry-run` and `--skip-init` to isolate behaviour.

## Setup

```bash
provlift version
terraform version
cd /path/to/a-terraform-repo
git checkout -b probe/provlift
git status --short          # must be empty
```

Add `--json` to any command for machine-readable output.

---

## Claim group A — Read-only commands are genuinely read-only

**Claim:** `list`, `check`, and `drift` never modify files. `list` needs neither
network nor Terraform.

```bash
git status --short > /tmp/before
provlift list && provlift check && provlift drift
git status --short > /tmp/after
diff /tmp/before /tmp/after        # expect: no difference
```

Stronger probe — prove `list` is offline:

```bash
provlift list --json   # with network disabled, or Terraform removed from PATH
```

**Pass:** no file changes; `list` succeeds with no network and no Terraform.
**Finding if:** any file is touched, or `list` fails without network.

---

## Claim group B — Minimum diff

**Claim (I2):** a constraint is rewritten only when it would otherwise block the
target version. **Claim (I3):** edits preserve comments, alignment, and ordering.

```bash
provlift upgrade --level patch --skip-init --fmt=false
git diff
```

**Pass:** only `version = "..."` literals change. Comments, blank lines, and
alignment of neighbouring arguments are byte-identical. A constraint that already
admits the target is untouched.

**Finding if:** a comment moves or disappears, alignment is reformatted, an
unrelated requirement is rewritten, or a non-blocking constraint changes.

Reset: `git checkout -- .`

---

## Claim group C — Level semantics

**Claim:** `--level` is a budget (`minor` permits patch *and* minor).
`--exact-level` restricts to exactly that level. `--next-major` advances one major
series. Major upgrades require `--allow-major`.

```bash
provlift upgrade --level patch --exact-level --dry-run --json
provlift upgrade --level minor --exact-level --dry-run --json
provlift upgrade --level major --dry-run                       # expect refusal
provlift upgrade --level major --allow-major --next-major --dry-run --json
```

**Pass:**
- `--exact-level patch` never plans a minor or major action.
- `--exact-level minor` never plans a major action.
- The third command exits non-zero and names `--allow-major`.
- `--next-major` targets the next major series, not the newest overall.

**Finding if:** a major action is applied without `--allow-major`, or an exact
level plans an action of a different level.

Note: a `0.x` minor bump is classified **breaking** and is also gated by
`--allow-major`. That is intentional.

---

## Claim group D — Provider selection *(highest-value area)*

**Claim:** `--provider` and `providers.ignore` select the intended provider.

Allow selectors retain their specified address precision. Deny rules
(`ignore` and `max_level: none`) cover the provider type across hosts and
namespaces; test both paths.

Construct a fixture declaring two providers with the same type in different
namespaces:

```hcl
terraform {
  required_providers {
    a = { source = "hashicorp/azurerm", version = ">= 3.0.0" }
    b = { source = "contoso/azurerm",   version = ">= 1.0.0" }
  }
}
```

Probe 1 — selector breadth:

```bash
provlift check --provider azurerm --json
```

Expected (documented): **both** providers appear. A bare selector matches on type
only, ignoring namespace and host.

Probe 2 — fail-closed ignore rule:

```yaml
# .provlift.yaml
providers:
  ignore:
    - hashicorp/azurerm
```

```bash
provlift check --json
```

Expected: **both** `hashicorp/azurerm` and `contoso/azurerm` are excluded.
The CLI warns that this selector spans two namespaces.

Probe 3 — same test against `max_level`:

```yaml
providers:
  max_level:
    hashicorp/azurerm: none
```

Expected: `none` applies to **both** sources and overrides less restrictive caps.

These are the intended behaviors. A genuine finding would be:
- an **ignored** provider still reaching the network (violates `eligibleProviders`), or
- a **bare** selector failing to match something it should, or
- `none` failing to override another cap (other caps choose the most specific
  matching selector).

Verify the network claim directly:

```bash
# with providers.ignore set, capture egress or use an unreachable private host
provlift check --json
```

**Pass:** no request is made for an ignored provider.

---

## Claim group E — Lock-file handling

**Claim (I1):** the tool never writes `.terraform.lock.hcl`; Terraform regenerates it.

```bash
provlift upgrade --level patch --json | jq '.terraform_runs[].command'
```

**Pass:** the sequence is `fmt` → `init -upgrade` → `providers lock` → `validate`,
and `providers lock` receives one `-platform=` per configured platform.

**Finding if:** the lock file changes without a `providers lock` run, or
`providers lock` is passed an unsupported flag.

Failure handling:

```bash
# make `terraform providers lock` fail, then:
provlift upgrade --level patch --json | jq '.terraform_runs'
```

**Pass:** `validate` does **not** run after a failed lock refresh, and the command
exits non-zero. Validating against a stale lock would be a real finding.

---

## Claim group F — Dependency ceilings

**Claim:** a ceiling inside a consumed module is respected and attributed.

```bash
rm -rf .terraform
provlift check --provider azurerm          # BLOCKED = dep  (from the lock file, module not named)
terraform get
provlift check --provider azurerm          # BLOCKED = dep  (names the module)
```

**Pass:** the ceiling is respected in both cases; only the attribution differs.
**Finding if:** a version forbidden by a dependency is ever planned or applied.

---

## Claim group G — Idempotency and stale-scan safety

**Claim (I8):** a second run plans nothing; a stale scan is refused.

```bash
provlift upgrade --level patch
provlift upgrade --level patch --dry-run --json | jq '.plan.actions | length'   # expect 0
```

Stale-scan probe — the test changes the file **after discovery**, before Apply:

```bash
go test ./internal/hcledit -run '^TestApplyRejectsUnrelatedChangeSinceScan$' -count=1
```

**Pass:** the command refuses with a "changed since it was scanned" error rather
than splicing at a stale offset.

**Finding if:** a `version` argument is inserted into an unrelated expression, or
a second identical run plans further changes.

---

## Claim group H — Atomicity

**Claim:** multi-file edits are transactional; a failure restores earlier files.

```bash
go test ./internal/hcledit -run '^TestApplySecondFileFailureRollsBack$' -count=1
```

The test injects a failure on the second write after the first succeeds. A
read-only target file cannot reliably force this path: atomic replacement
depends on its parent directory's permissions, not the file's write bit.

**Pass:** the first file is restored byte-for-byte and no write artifacts remain.

Also confirm no `.provlift-backup` or `.provlift-*.tmp` file survives a successful run.

**Claim:** only backups provlift recorded are restored; any other backup stops
the run before a file changes.

```bash
go test ./internal/hcledit -run '^TestRecover' -count=1
```

**Pass:** an interrupted replacement is restored, while a backup with no
journal entry, or one that differs from it, fails with an error naming the
file and leaves every file byte-for-byte unchanged.

---

## Claim group I — Credentials

**Claim:** tokens go only to the host they belong to, are never logged, and
service discovery cannot redirect them.

```bash
TF_TOKEN_registry_terraform_io=dummy provlift check --json 2>&1 | grep -i dummy   # expect no match
```

Adversarial discovery probes — stand up a fake registry returning:

| `providers.v1` value | Expected |
|---|---|
| `http://same.host/v1/providers/` | **Rejected** (scheme downgrade) |
| `//attacker.example/v1/providers/` | **Rejected** (network-path host change) |
| `https://attacker.example/v1/providers/` | **Rejected** (cross-host absolute endpoint) |
| a 1 GiB body | Read capped at 16 MiB |

**Finding if:** the original host's token is sent to a different host, a downgrade
is accepted, or a token appears in any output stream.

---

## Claim group J — Output contract

**Claim (I7):** every `upgrade --json` result (including dry runs, no-ops and
errors) has the same top-level keys. Error outcomes also exit nonzero and print
`Error:` on stderr.

```bash
provlift upgrade --level patch --dry-run --json           | jq 'keys'
provlift upgrade --level patch --json --skip-init | jq 'keys'  # use a throwaway fixture
```

**Pass:** both successful results return `dry_run`, `plan`, `changed_files`,
`terraform_runs`, `pull_request`. `changed_files` entries are repository-relative.
A failed or refused operation instead exits nonzero and may have no JSON body.

Exit codes:

```bash
provlift check --exit-code;  echo $?    # 2 when upgrades exist, else 0
provlift drift --exit-code;  echo $?    # 2 when modules disagree
```

| Code | Meaning |
|---|---|
| 0 | Success |
| 1 | Error or refused operation |
| 2 | Upgrades available / drift found |
| 3 | Terraform validation failed |

---

## Claim group K — Pull-request metadata

**Claim:** single-provider branch names carry the target version so re-runs
update rather than duplicate; the CLI never talks to a forge.

```bash
provlift upgrade --level patch --exact-level --pr-body /tmp/body.md --dry-run --json \
  | jq '.pull_request | {branch, title, labels}'
```

**Pass:**
- A single-provider branch is deterministic for a given target
  (`provlift/<provider>-<version>`); a different target produces a different branch.
- Multi-provider plans use `provlift/providers-<level>-<count>` and can collide;
  do not rely on them for unattended pull requests.
- Breaking changes carry a `breaking-change` label.
- The body names modules held back by a dependency (ordinary skips are omitted).
- No network call is made to any forge, and no forge token is read.

**Finding if:** the branch omits the version when a single target exists, or the
tool attempts forge authentication.

---

## Claim group L — Execution surface

**Claim (R1):** repository config allows only `terraform` or `tofu` by name;
operator-controlled flag and environment overrides can select another binary.

```yaml
terraform:
  binary: ./not-really-terraform  # rejected during config loading
```

```bash
go test ./internal/config ./internal/cli -run 'TestLoadRejectsBadConfig|TestOpenUsesEnvironmentBinaryUnlessFlagOverrides' -count=1
```

**Pass:** a repository-supplied executable path is refused. The flag wins over
`PROVLIFT_TERRAFORM_BIN`, which is honored when the config selects the default
`terraform`. To test actual subprocess execution, use a non-dry-run invocation
with a fake executable; `--dry-run` never constructs a Terraform runner.

---

## Reporting

For each finding, provide:

1. **Claim** — quoted from this file or `docs/architecture/README.md`.
2. **Probe** — exact commands and fixture.
3. **Observed** — verbatim output, secrets redacted.
4. **Expected** — per the claim.
5. **Impact** — concretely: wrong provider upgraded, stale lock validated, token
   sent off-host, repository left partially modified.
6. **Status** — new, previously documented, or fixed with a regression test.

Rank by impact on these outcomes, in order:

1. Credential sent to an unintended host.
2. A provider version forbidden by a constraint or dependency gets applied.
3. Repository left in a partially modified state.
4. Validation reported green against a stale lock file.
5. The wrong provider is selected or an ignore rule is bypassed.
6. Output or documentation inaccuracy.
