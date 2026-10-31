# Development

## 15. Development

This page describes how the codebase is tested and released. For how to
propose a change, see [CONTRIBUTING.md](../../CONTRIBUTING.md).

### 15.1 Common tasks

```bash
go build ./...
go test ./... -count=1
go vet ./...
gofmt -l .
```

Run the CLI without installing it:

```bash
go run ./cmd/provlift list -C path/to/terraform-repo
```

### 15.2 Test strategy

| Layer | Approach |
|---|---|
| Version classification and constraint rewriting | Table-driven unit tests, including 0.x and prerelease cases |
| Repository discovery | Fixture repository under `internal/discovery/testdata` covering root vs child modules, legacy shorthand, missing versions and the `.terraform` cache |
| File editing | Temporary fixtures asserting byte preservation, rollback, recovery of recorded interrupted writes, refusal of unrecorded backups, and stale-scan rejection |
| Registry client | Local HTTP server exercising service discovery, version listing and auth headers |
| Workflow scripts | Executable Bash steps with fake provlift/Terraform commands; matrix selection, major acknowledgement, branch reuse and vendoring order |

Every rewritten constraint is asserted to actually admit its target version, so
a rewrite cannot pass by producing something merely well-formed.

The package layout is documented in [the architecture guide](../architecture/README.md#3-package-layout).

### 15.3 Adding support for another provider

Nothing to do. If Terraform or OpenTofu can install it, `provlift` can
inventory, classify and upgrade it — whether it comes from the public Terraform
registry, the OpenTofu registry or a private registry. Only a provider hosted
somewhere that does not implement the registry protocol would need new code.

### 15.4 This repository's own CI

[.github/workflows/ci.yml](../../.github/workflows/ci.yml) runs on every push and
pull request:

| Job | Gate |
|---|---|
| Format and vet | `gofmt`, `go mod tidy`, `go vet` |
| Go unit and package tests | `go test -race ./...`, coverage ≥ 80% and uploaded coverage report |
| Cross-compile | linux, darwin and windows on amd64 and arm64 |
| Lint workflows | `actionlint` over `.github/workflows` *and* `examples/workflows` |
| End to end | A real upgrade against the real registry and real Terraform |

The race detector runs on every commit because CGO is free on Linux runners.

The end-to-end job exists because the unit tests use a fake registry and a fake
Terraform, which means they cannot catch a change in either. It upgrades a
throwaway module, then asserts the comment in the edited file survived, that
Terraform produced a lock file, that every Terraform command exited zero, and
that running the upgrade a second time plans nothing further.

The `examples/workflows` files are linted because users copy them verbatim into
their own repositories, so a broken one is shipped breakage.

Local workflow tests prove branch plumbing, not that GitHub creates or updates
a pull request. Before relying on a workflow change, run it in a test
repository and confirm the resulting pull requests.

### 15.5 Releasing

Releases are automated from `main` by
[release-please.yml](../../.github/workflows/release-please.yml). The workflow reads
[Conventional Commits](https://www.conventionalcommits.org/) after each merge
and opens or updates a release PR. When that release PR is merged, Release
Please creates the next tag and GitHub release; the tag then triggers
[release.yml](../../.github/workflows/release.yml), which verifies the tag and
publishes the platform binaries.

The workflow needs a repository secret named `RELEASE_PLEASE_TOKEN`. It must be
a fine-grained token with repository access for contents, pull requests, and
actions, all with read/write permission. The built-in `GITHUB_TOKEN` cannot be
used here because events it creates cannot start the downstream publishing
workflow.

While the project is below `v1.0.0`, version progression is:

| Commit type | Version change |
|---|---|
| `fix:` | patch, for example `v0.1.0` -> `v0.1.1` |
| `feat:` | minor, for example `v0.1.1` -> `v0.2.0` |
| `BREAKING CHANGE:` or `!` before `v1.0.0` | minor, for example `v0.2.0` -> `v0.3.0` |
| `BREAKING CHANGE:` or `!` from `v1.0.0` onward | major, for example `v1.0.0` -> `v2.0.0` |

Release Please uses `.release-please-manifest.json` as its version baseline. Do
not manually create a tag for a version already managed by Release Please. For
an emergency manual release, use a **new** semver tag only after confirming it
is not already managed by Release Please.

[.github/workflows/release.yml](../../.github/workflows/release.yml) re-runs the test
suite against the tag — a tag is permanent and people will install from it, so
it is not enough that the commit was green when it was pushed — then builds
every platform with `-trimpath` and stamps the version, commit and build date in
through `-ldflags`. It asserts the built binary reports the tag before
publishing, attaches SHA-256 checksums, and marks any tag containing a hyphen
(`v1.2.0-rc.1`) as a pre-release so it does not become "latest".

Consumers install a tagged release with:

```bash
go install github.com/provlift/provlift/cmd/provlift@v0.1.0
```

Pin that version in the `PROVLIFT_VERSION` of the example workflows rather than
tracking `main`, so a change to this tool cannot alter a repository's pull
requests without its maintainers choosing it.

---
