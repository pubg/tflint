## Shared credentials for plugin installation

This PUBG fork prerelease extends `v0.64.0-pubg.2` with shared authentication for GitHub and GitLab plugin installation. Configuration inheritance and plugin installation now reuse the same credential-file lookup.

The release tag is `v0.64.0-pubg.3`. The executable reports `0.64.0+pubg.3`, preserving compatibility with existing Terraform-style version constraints.

### Authentication order

1. Existing host-specific and global `GITHUB_TOKEN` or `GITLAB_TOKEN` environment variables. GitLab's matching `CI_JOB_TOKEN` support is also preserved.
2. A matching token in `~/.terraform.d/credentials.tfrc.json`, sent as Bearer authentication.
3. Matching credentials in `~/.netrc`, sent as HTTP Basic authentication. `NETRC` can override this path.

For example, this credential can read both an inherited GitLab configuration and GitLab plugin release assets:

```json
{
  "credentials": {
    "gitlab.example.com": { "token": "<access-token>" }
  }
}
```

For GitHub.com, use `github.com` as the credential key; TFLint applies it to requests to `api.github.com`. Existing host-specific environment-token behavior is unchanged, including empty GitHub host overrides.

If a selected credential is rejected, TFLint does not retry with another credential source. File credentials are sent only over HTTPS to the source's API host and are not forwarded to another host or port. Invalid credential files report errors without including their contents.

See the [plugin authentication guide](https://github.com/pubg/tflint/blob/v0.64.0-pubg.3/docs/user-guide/plugins.md#shared-credential-files) and [configuration inheritance guide](https://github.com/pubg/tflint/blob/v0.64.0-pubg.3/docs/user-guide/config.md#inherit_from).

### Downloads

- Native ZIP archives for macOS, Linux, and Windows are attached. Verify downloads against `checksums.txt`.
- The container image is `ghcr.io/pubg/tflint:0.64.0-pubg.3`, for `linux/amd64` and `linux/arm64`.
- Verify the checksum-file build attestation with `gh attestation verify checksums.txt --repo pubg/tflint`.

```sh
docker run --rm -v "$PWD:/data" ghcr.io/pubg/tflint:0.64.0-pubg.3 --version
```

These changes have not been merged upstream. Tests cover credential precedence, authentication failures, redirect isolation, and complete GitLab installation against a simulated server. File-based authentication against a live internal GitLab instance has not been verified.
