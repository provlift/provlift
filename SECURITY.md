# Security Policy

## Supported versions

`provlift` is pre-1.0. Security fixes are made on `main` and shipped in the next
release; only the latest release is supported. Please upgrade before reporting
an issue against an older version.

## Reporting a vulnerability

**Please do not report security vulnerabilities through public GitHub issues,
discussions or pull requests.**

Report them privately through GitHub Security Advisories:

1. Go to https://github.com/provlift/provlift/security/advisories/new
   (or the repository's **Security** tab → **Report a vulnerability**).
2. Describe the issue, the affected version (`provlift version`), and the steps
   or configuration needed to reproduce it. Redact any real tokens.

You should receive an acknowledgement within a few days. We will keep you
informed while we investigate and fix the issue, coordinate a disclosure date
with you, and credit you in the advisory unless you prefer otherwise.

## Scope

Issues of particular interest, given what `provlift` does:

- A registry token being sent to a host other than the one it belongs to, or
  appearing in any output or log.
- Service discovery being redirected to a different host or downgraded from
  HTTPS.
- Repository content (for example `.provlift.yaml` or `.tf` files) causing
  `provlift` to execute an unexpected program or write outside the repository.
- A version forbidden by a constraint being applied, or a repository being left
  partially modified after a failure.

`provlift upgrade` and `provlift validate` run Terraform or OpenTofu, which
download and execute provider plugins. That behaviour is inherent to those
tools and is documented in the
[architecture guide](docs/architecture/README.md#6-security-model); it is not by
itself a vulnerability in `provlift`.
