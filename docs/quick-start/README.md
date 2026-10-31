# Quick Start

## 4. Quick start

### 4.1 First run

Run each step from the root of a Terraform or OpenTofu repository. If the
repository uses OpenTofu, set `terraform.binary: tofu` in
[`.provlift.yaml`](../configuration/README.md) once (or pass
`--terraform-binary tofu` to every command), so that registry lookups and
lock-file generation both follow OpenTofu.

1. **Inventory** what is declared — offline, no credentials. See
   [`provlift list`](../cli/README.md#62-provlift-list).

   ```sh
   provlift list
   ```

2. **Check** which upgrades the registry offers, and which are blocked by a
   constraint. See [`provlift check`](../cli/README.md#63-provlift-check).

   ```sh
   provlift check
   ```

3. **Look for disagreement** between modules — offline. See
   [`provlift drift`](../cli/README.md#64-provlift-drift).

   ```sh
   provlift drift
   ```

4. **Preview, then apply** an upgrade. See
   [`provlift upgrade`](../cli/README.md#65-provlift-upgrade).

   ```sh
   provlift upgrade --level minor --dry-run
   provlift upgrade --level minor
   ```

5. **Review the diff**, including `.terraform.lock.hcl`, then open a PR.

### 4.2 Guided walkthrough

For a complete evaluation sequence, use the canonical command examples in
[section 6](../cli/README.md#6-cli-reference). The reference keeps every executable `provlift`
invocation together by command, including read-only inspection, dry runs,
progressive upgrades, validation, JSON output and CI exit codes.

For a safe evaluation, work from a clean throwaway branch, start with `list`,
`check` and `drift`, preview upgrades before applying them, review both the
configuration and lock-file diff, then run `validate`. Shared modules and root
configurations have different lock-file policies; see [section 11.4](../ci-cd/README.md#114-shared-module-repositories-need-a-different-workflow).

---
