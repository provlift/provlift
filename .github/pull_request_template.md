## Summary

<!-- What does this change and why? Link the issue it addresses, e.g. "Fixes #123". -->

## Type of change

<!-- The PR title should be a Conventional Commit, e.g. "fix(upgrade): keep comments when ..." -->

- [ ] `fix` — bug fix
- [ ] `feat` — new capability
- [ ] `docs` — documentation only
- [ ] Other (`test`, `refactor`, `perf`, `chore`, `ci`, `build`)
- [ ] Breaking change (title uses `!` and the description explains the migration)

## How was this tested?

<!-- Commands run, new or updated tests, and any manual check against a real Terraform/OpenTofu repository. -->

## Checklist

- [ ] `gofmt -l .` prints nothing
- [ ] `go vet ./...` and `go test ./...` pass
- [ ] Tests cover the change (bug fixes include a regression test)
- [ ] Documentation under `docs/` is updated for any user-visible change
- [ ] The change stays provider-neutral and works with both Terraform and OpenTofu
