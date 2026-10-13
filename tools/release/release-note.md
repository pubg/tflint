## Configuration inheritance prerelease

This prerelease is based on upstream TFLint 0.64.0 and adds configuration inheritance with HTTPS credential file support.

The release tag is `v0.64.0-inherit.1`. The executable reports `0.64.0+inherit.1`, using build metadata to identify the fork while preserving compatibility with existing `required_version` constraints.

### Inherit shared configuration

```hcl
config {
  inherit_from = "https://example.com/tflint/v1/base.hcl"
}

rule "terraform_naming_convention" {
  enabled = false
}
```

- Inherit one local file or HTTP(S) URL per configuration, with recursive inheritance in HCL or JSON.
- Override inherited attributes in the current file, including attributes in matching `plugin` and `rule` blocks. Explicit `false`, empty lists, and empty maps override inherited values.
- Resolve relative references against the declaring file or remote URL. Detect cycles and limit inheritance to 32 files.
- Enforce each file's `required_version` constraint. Remote requests have a 30-second timeout and a 1 MiB response limit.
- For HTTPS requests, read a host's Bearer token from `~/.terraform.d/credentials.tfrc.json`, falling back to HTTP Basic credentials from `~/.netrc`. `NETRC` overrides the netrc path. Credentials are resolved for each request host, including redirects.

See the [configuration documentation](https://github.com/pubg/tflint/blob/v0.64.0-inherit.1/docs/user-guide/config.md#inherit_from) for merge rules, Windows paths, and credential behavior. This iteration supports these two credential files; it does not load Terraform CLI configuration, `TF_TOKEN_*`, or credential helpers.

### Downloads

- Native ZIP archives for macOS, Linux, and Windows are attached. Verify downloads against `checksums.txt`.
- The container image is `ghcr.io/pubg/tflint:0.64.0-inherit.1`, for `linux/amd64` and `linux/arm64`.
- Verify the checksum-file build attestation with `gh attestation verify checksums.txt --repo pubg/tflint`.

```sh
docker run --rm -v "$PWD:/data" ghcr.io/pubg/tflint:0.64.0-inherit.1 --version
```

This prerelease contains the inheritance changes from `feat/config-inherit-from`; the separate GitLab plugin installation changes in `v0.64.0-pubg.1` are not included.
