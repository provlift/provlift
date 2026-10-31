# Troubleshooting and Limitations

## 12. Troubleshooting

### 12.1 `terraform binary not found: "terraform" is not on PATH`

- **Cause** — Terraform is not installed, or not on the `PATH` of this shell.
- **Fix** — Install Terraform, or point at it explicitly:
  `provlift upgrade --terraform-binary /opt/terraform/bin/terraform` (or
  `"C:\tools\terraform.exe"` on Windows), or set `PROVLIFT_TERRAFORM_BIN`.
  If the repository uses OpenTofu, use `--terraform-binary tofu` or set
  `terraform.binary: tofu` in `.provlift.yaml`.
- `provlift list` and `provlift drift` do not need Terraform at all.

### 12.2 `registry <host> rejected the request (HTTP 401)`

- **Cause** — A private registry requires a token that is not configured.
- **Fix** — Run `terraform login <host>`, or export
  `TF_TOKEN_<host>` (dots become `_`, dashes become `__`).
- See [section 9](../authentication/README.md#9-authentication).

### 12.3 `provider not found in registry`

- **Cause** — The `source` address is wrong, or the provider lives on a host
  that does not implement the registry protocol.
- **Fix** — Confirm the address on the registry. Remember that a bare local name
  implies the `hashicorp` namespace, so `github = "~> 6.0"` resolves to
  `hashicorp/github`, not `integrations/github`. Add an explicit `source`.
- A source without a hostname is looked up on `registry.terraform.io` under
  Terraform and on `registry.opentofu.org` under OpenTofu
  (`--terraform-binary tofu` / `terraform.binary: tofu`). If a provider is
  published on only one of those registries, either run `provlift` with the
  tool the repository actually uses, or write the full address including the
  host in `source`.

### 12.4 `unsupported version constraint shape`

- **Cause** — The constraint uses `!=` or multiple disjoint ranges, which the
  tool will not rewrite because the author's intent is ambiguous.
- **Fix** — Edit that one constraint by hand. `provlift list` prints the exact
  file and line. Re-run afterwards.
- This is deliberate: silently widening such a constraint is more dangerous than
  refusing.

### 12.5 `current version is unknown; run terraform init first`

- **Cause** — The module has no `.terraform.lock.hcl` and no version constraint,
  so there is no basis for classifying an upgrade.
- **Fix** — Run `terraform init` in that module, or add a constraint, or use
  `--pin-unconstrained` to have the tool add one.

### 12.6 `<file> changed since it was scanned; re-run the command`

- **Cause** — The file was modified between the scan and the write, so the
  recorded byte range no longer matches.
- **Fix** — Re-run the command. This guard exists to prevent a stale offset
  corrupting a file.

### 12.7 Upgrade refused with `--allow-major` not set

- **Cause** — Expected behaviour: the plan contains a major upgrade.
- **Fix** — Read the linked provider upgrade guide, then re-run with
  `--allow-major`. Prefer upgrading one provider at a time; to select the next
  **major** rather than a nearer patch or minor, use
  `provlift upgrade --provider azurerm --level major --exact-level --allow-major`.

### 12.8 `terraform validate` fails after a major upgrade

- **Cause** — The new provider version removed or renamed schema the
  configuration still uses. This is the failure the tool exists to surface.
- **Fix** — Apply the provider's migration guide. To undo, review `git diff`
  and restore only the tool-generated constraint and lock-file changes after
  confirming those files had no pre-existing edits. `--to` does not downgrade.

### 12.9 CI reports lock file checksum errors

- **Cause** — The lock file records hashes only for the platform that generated
  it.
- **Fix** — Ensure `platforms` in `.provlift.yaml` lists every platform in use.
  `provlift upgrade` runs `terraform providers lock` for all of them.

### 12.10 `check` shows upgrades but `upgrade` says "Nothing to upgrade"

- **Cause** — `upgrade` defaults to `--level patch`, while `check` defaults to
  `--level major`. A `providers.max_level` rule may also cap the provider.
- **Fix** — Pass an explicit `--level`, and check the `Skipped` table, which
  states the reason for every provider that was not upgraded.

### 12.11 Commands are slow or time out

- **Cause** — `terraform init -upgrade` downloads provider binaries, and
  `providers lock` fetches one archive per platform.
- **Fix** — Raise `--timeout`, narrow the run with `--module` or `--provider`,
  or reduce `--platforms` for local runs.

### 12.12 Providers appear that this repository does not own

- **Cause** — Vendored, backup or example configuration under the scan root.
- **Fix** — Add `exclude` globs in `.provlift.yaml`. `.git`, `.terraform` and
  `node_modules` are skipped automatically, but directories such as `.backup`
  are not.

### 12.13 `check` shows `(floor)` instead of a real version

- **Cause** — The module has no `.terraform.lock.hcl`, so nothing has been
  selected. The figure shown is the lowest version the constraint admits.
- **Fix** — Nothing, if this is a shared module; that is the correct reading.
  For a root configuration it means the module has never been initialised, or
  its lock file is not committed. Run `terraform init` and commit the result.

### 12.14 `drift` reports constraint divergence that looks intentional

- **Cause** — Different modules genuinely need different constraints.
- **Fix** — Divergence is reported, not judged. Use `--provider` to narrow the
  report, or treat the output as a review checklist rather than a failure.
  Cosmetic differences such as `>=4.16.0` versus `>= 4.16.0` are normalised
  away and never reported.

### 12.15 `recovering interrupted writes: refusing to restore <file>.tf.provlift-backup`

- **Cause** — `upgrade` found a `.tf.provlift-backup` file that provlift has
  no record of creating, or one whose content no longer matches what it
  recorded. provlift records each backup it makes in the user cache directory
  and restores only those, so an interrupted write is resumed only on the
  machine and account that made it. Nothing is changed when this error occurs.
- **Fix** — Inspect the named file. If it is unexpected, delete it and re-run.
  If it holds content you need, copy it over the `.tf` file by hand first.
  A backup inside an excluded directory is ignored.

---

---

## 14. Known limitations

- `.tf.json` configuration is reported but not edited; a warning is emitted.
- Provider requirements built from variables or `for` expressions cannot be
  statically resolved and are reported as diagnostics.
- Constraints using `!=` or multiple disjoint ranges are refused, by design.
- `terraform plan` is not run by default, so runtime-only breaks surface in CI.
- Provider upgrade guides are linked, not parsed; there is no cross-provider API
  for them.
- Terragrunt-generated configuration is not evaluated.

---
