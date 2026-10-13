## PUBG fork release

This prerelease is based on TFLint 0.64.0 and includes GitLab Releases support from [PR #1](https://github.com/pubg/tflint/pull/1) and the review fixes in [PR #2](https://github.com/pubg/tflint/pull/2).

The executable reports `0.64.0+pubg.1`. Build metadata identifies the fork while preserving compatibility with existing `required_version` constraints.

### GitLab plugin installation

```hcl
plugin "company" {
  enabled     = true
  source_type = "gitlab"
  source      = "gitlab.example.com/group/subgroup/tflint-ruleset-company"
  version     = "0.1.0"
}
```

Run `tflint --init` to install the plugin from its `v0.1.0` release. Configure `GITLAB_TOKEN_<host>` or `GITLAB_TOKEN` for private projects. GitLab CI can use `CI_JOB_TOKEN` when `CI_SERVER_URL` matches the source host. SHA-256 checksums are always verified; configure a PGP signing key for publisher verification.

### Downloads

- Native ZIP archives are attached for macOS, Linux, and Windows. Check the archive against `checksums.txt` before installing it.
- The container image is `ghcr.io/pubg/tflint:0.64.0-pubg.1`, for `linux/amd64` and `linux/arm64`.
- Verify the checksum-file build attestation with `gh attestation verify checksums.txt --repo pubg/tflint`.

```sh
docker run --rm -v "$PWD:/data" ghcr.io/pubg/tflint:0.64.0-pubg.1 --version
```

The GitLab installation path was tested with a local HTTPS server, including token isolation, checksums, and PGP signatures. A live GitLab installation has not yet been verified. These changes have not been merged upstream.
