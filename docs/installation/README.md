# Installation

## 2. Prerequisites

| Requirement | Version | Needed for |
|---|---|---|
| Terraform **or** OpenTofu | Terraform 1.0+ or any OpenTofu release, on `PATH` | `upgrade` and `validate` |
| Registry network access | — | `check` and `upgrade` |
| Go | 1.26+ | `go install` or building from source only |

- `provlift list` and `provlift drift` are fully offline: they need neither
  Terraform nor network access.
- OpenTofu is supported. Set `terraform.binary: tofu` in
  [`.provlift.yaml`](../configuration/README.md), or pass
  `--terraform-binary tofu`.
- Cloud credentials (AWS, Azure, Google Cloud, GitHub, …) are **not** required
  except for an opt-in `terraform plan` or backend initialisation.

---

## 3. Installation

Pick one of the three methods below. Every method produces a single static
binary named `provlift` (`provlift.exe` on Windows).

### 3.1 Install with Go

```bash
go install github.com/provlift/provlift/cmd/provlift@latest
```

To pin a specific release, for example in CI:

```bash
go install github.com/provlift/provlift/cmd/provlift@v0.1.0
```

`provlift version` reads the release tag from Go build metadata when installed
with `go install ...@<tag>`. A local build from a clone reports `dev`.

### 3.2 Put the Go bin directory on `PATH`

`go install` writes the binary to `GOBIN` when set, otherwise to
`$(go env GOPATH)/bin`. Skip this step if that directory is already on `PATH`.

**macOS or Linux**

```bash
export PATH="$PATH:$(go env GOPATH)/bin"
case "${SHELL##*/}" in
  zsh) rc="${ZDOTDIR:-$HOME}/.zshrc" ;;
  bash) rc="$HOME/.bashrc" ;;
  *) rc="$HOME/.profile" ;;
esac
echo 'export PATH="$PATH:$(go env GOPATH)/bin"' >> "$rc"
source "$rc"
command -v provlift
```

**Windows PowerShell**

```powershell
$env:Path += ";$(go env GOPATH)\bin"
[Environment]::SetEnvironmentVariable("Path", $env:Path, "User")
Get-Command provlift
```

**Git Bash on Windows**

```bash
export PATH="$PATH:$(cygpath -u "$(go env GOPATH)")/bin"
echo 'export PATH="$PATH:$(cygpath -u "$(go env GOPATH)")/bin"' >> ~/.bashrc
source ~/.bashrc
which provlift
```

Do not append the raw Windows value from `go env GOPATH` directly in Git Bash;
that creates a malformed path containing backslashes.

If `GOBIN` is explicitly configured, add `$(go env GOBIN)` instead of
`$(go env GOPATH)/bin`.

### 3.3 Install from a GitHub Release archive

Prebuilt binaries for Linux, macOS and Windows are attached to every release on
the [GitHub Releases](https://github.com/provlift/provlift/releases) page. No Go
toolchain is needed. Assets are named like:

```text
provlift_v0.1.0_linux_amd64.tar.gz
provlift_v0.1.0_linux_arm64.tar.gz
provlift_v0.1.0_darwin_amd64.tar.gz
provlift_v0.1.0_darwin_arm64.tar.gz
provlift_v0.1.0_windows_amd64.zip
checksums.txt
```

Verify the download against `checksums.txt`, extract `provlift` or
`provlift.exe` into a directory on `PATH`, then run `provlift version`. Release
binaries have the tag, commit and build date stamped in.

```bash
version=v0.1.0
archive="provlift_${version}_linux_amd64.tar.gz"
curl -fsSLO "https://github.com/provlift/provlift/releases/download/${version}/${archive}"
curl -fsSLO "https://github.com/provlift/provlift/releases/download/${version}/checksums.txt"
sha256sum -c --ignore-missing checksums.txt
tar -xzf "$archive"
sudo install "provlift_${version}_linux_amd64/provlift" /usr/local/bin/provlift
```

On macOS, replace `sha256sum -c --ignore-missing checksums.txt` with
`shasum -a 256 -c --ignore-missing checksums.txt`.

For Windows PowerShell, download the matching `.zip` and `checksums.txt` into
the same directory, then verify the ZIP before extracting it:

```powershell
$archive = "provlift_v0.1.0_windows_amd64.zip"
$entry = Get-Content checksums.txt | Where-Object { $_.EndsWith(" $archive") }
if (@($entry).Count -ne 1) { throw "Missing or duplicate checksum for $archive" }
$expected = ($entry -split "\s+")[0]
$actual = (Get-FileHash -Algorithm SHA256 $archive).Hash
if ($actual -ne $expected) { throw "SHA-256 mismatch for $archive" }
Write-Host "SHA-256 verified: $archive"
```

### 3.4 Build from a clone

```bash
git clone https://github.com/provlift/provlift.git
cd provlift
mkdir -p bin
go build -o bin/provlift ./cmd/provlift
```

On Windows, use `go build -o bin/provlift.exe ./cmd/provlift` instead. To try
unreleased changes without building, run `go run ./cmd/provlift <command>` from
the clone.

### 3.5 Verify

```bash
provlift version
terraform version   # or: tofu version
```

### 3.6 Uninstall

`provlift` does not have an installer or an `uninstall` command. Remove the
binary from the location used when installing it. First locate any copies on
`PATH` (there may be more than one):

```bash
type -a provlift                                          # macOS, Linux, Git Bash
```

```powershell
Get-Command provlift -All | Select-Object -ExpandProperty Source   # PowerShell
```

**Installed with `go install` (section 3.1):** Go puts the binary in `GOBIN`
when set, otherwise in `GOPATH/bin`. Remove only `provlift`, not the whole Go
bin directory.

For macOS or Linux:

```bash
go_bin="$(go env GOBIN)"
[ -n "$go_bin" ] || go_bin="$(go env GOPATH)/bin"
rm -- "$go_bin/provlift"
```

For Windows PowerShell:

```powershell
$goBin = go env GOBIN
if (-not $goBin) { $goBin = Join-Path (go env GOPATH) 'bin' }
Remove-Item -LiteralPath (Join-Path $goBin 'provlift.exe')
```

For Git Bash on Windows:

```bash
go_bin="$(go env GOBIN)"
[ -n "$go_bin" ] || go_bin="$(go env GOPATH)/bin"
rm -- "$(cygpath -u "$go_bin")/provlift.exe"
```

**Installed from a GitHub Release (section 3.3):** Delete the `provlift` or
`provlift.exe` file you extracted into a directory on `PATH`. Check the path
reported by `type -a provlift` or `Get-Command provlift -All` before deleting
it; the release archive and extracted folder may also be deleted if no longer
needed.

**Built from a clone (section 3.4):** Delete `bin/provlift` (or
`bin/provlift.exe` on Windows) from the clone. If you copied that binary to a
directory on `PATH`, remove that copy as well.

Open a new shell and rerun the lookup command above to check for another
installation. Do not remove `GOPATH/bin` from `PATH` unless you are certain no
other Go tools use it.

### 3.7 Shell completion

`provlift` generates shell completion scripts for Bash, Zsh, Fish, and PowerShell via `provlift completion <shell>`.

Flags like `--exact-level`, `--next-major`, `--pin-unconstrained`, `--raise-floor`, and `--include-prerelease` can be auto-completed directly in your terminal.

#### Bash

Bash completion requires the `bash-completion` package to be installed on your system.

To load completions in your current shell session:

```bash
source <(provlift completion bash)
```

To load completions for every new session, execute once:

- **Linux:**
  ```bash
  provlift completion bash | sudo tee /etc/bash_completion.d/provlift > /dev/null
  ```
- **macOS (with Homebrew):**
  ```bash
  provlift completion bash > $(brew --prefix)/etc/bash_completion.d/provlift
  ```

#### Zsh

If completion is not already enabled in your Zsh environment, add the `compinit` initialization to your `~/.zshrc`:

```zsh
autoload -U compinit; compinit
```

To load completions in your current shell session:

```zsh
source <(provlift completion zsh)
```

To load completions for every new session, execute once:

- **Linux:**
  ```zsh
  provlift completion zsh > "${fpath[1]}/_provlift"
  ```
- **macOS (with Homebrew):**
  ```zsh
  provlift completion zsh > $(brew --prefix)/share/zsh/site-functions/_provlift
  ```

#### Fish

To load completions in your current shell session:

```fish
provlift completion fish | source
```

To load completions for every new session, execute once:

```fish
provlift completion fish > ~/.config/fish/completions/provlift.fish
```

#### PowerShell

To load completions in your current shell session:

```powershell
provlift completion powershell | Out-String | Invoke-Expression
```

To load completions for every new session, append the snippet above to your PowerShell profile (`$PROFILE`):

```powershell
if (-not (Test-Path $PROFILE)) {
    New-Item -ItemType File -Path $PROFILE -Force
}
Add-Content -Path $PROFILE -Value 'provlift completion powershell | Out-String | Invoke-Expression'
```

---
