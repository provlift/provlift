# Authentication

## 9. Authentication

The public Terraform registry and the OpenTofu registry need no credentials.
For a private registry, tokens are read exactly the way Terraform reads them,
so no separate configuration is needed:

1. `TF_TOKEN_<host>` (dots → `_`, dashes → `__`), e.g. `TF_TOKEN_app_terraform_io`
   or `TF_TOKEN_tf_example_com`
2. `credentials` blocks in the CLI config (`%APPDATA%\terraform.rc` / `~/.terraformrc`, or `TF_CLI_CONFIG_FILE`)
3. `credentials.tfrc.json` written by `terraform login`

OpenTofu also honours `TF_TOKEN_<host>` and `TF_CLI_CONFIG_FILE`, so those two
are the simplest way to share one token between OpenTofu and `provlift`.
`provlift` does not read OpenTofu-specific config files such as `.tofurc`.

Tokens are only ever sent as a `Bearer` header to the host they belong to, and
are never logged or included in any output.

Provider *cloud* credentials (AWS, Azure, Google Cloud, GitHub, …) are not
needed for discovery, checking or upgrading — only for `terraform plan` or an
opt-in `--backend` initialisation.

---
