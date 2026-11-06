# Verify a release

Every release carries its binaries, `SHA256SUMS`, an SPDX SBOM and one Sigstore bundle per file. Costroid's release workflow signs each file with cosign, keyless, under its own GitHub Actions identity. The image is signed the same way. Releases carry no GitHub artifact attestation, so `gh attestation verify` has nothing to check.

The commands below check `1.0.0-alpha.7`. Replace the version in every line for another release.

## The checksum

```sh
curl -fsSLO https://github.com/Costroid/costroid/releases/download/v1.0.0-alpha.7/costroid-linux-amd64
curl -fsSLO https://github.com/Costroid/costroid/releases/download/v1.0.0-alpha.7/SHA256SUMS
grep ' costroid-linux-amd64$' SHA256SUMS | sha256sum --check
```

It prints `costroid-linux-amd64: OK`. A checksum proves the download matches `SHA256SUMS`, and nothing about who wrote `SHA256SUMS`. The signature proves that.

## The signature

With cosign 3 or later:

```sh
curl -fsSLO https://github.com/Costroid/costroid/releases/download/v1.0.0-alpha.7/SHA256SUMS.sigstore.json
cosign verify-blob --bundle SHA256SUMS.sigstore.json \
  --certificate-identity https://github.com/Costroid/costroid/.github/workflows/release.yml@refs/tags/v1.0.0-alpha.7 \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com SHA256SUMS
```

The identity is the release workflow at the release's tag, so a file signed by any other workflow, branch or repository fails. A signed `SHA256SUMS` and a matching checksum together cover the binary. Each binary also has a bundle of its own, `costroid-linux-amd64.sigstore.json`, for the same command on the binary.

## The image

The release notes name the image by digest. Check that digest, never a tag, because a tag can move:

```sh
cosign verify ghcr.io/costroid/costroid@sha256:<digest> \
  --certificate-identity https://github.com/Costroid/costroid/.github/workflows/release.yml@refs/tags/v1.0.0-alpha.7 \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com
```

Image signatures need cosign 3. The release workflow also attaches the image's SPDX SBOM to the image as a signed attestation of type `spdxjson`.

## What the action checks

The action pinned by its commit installs the release whose tag points at that commit. It reads the commit from the path the runner checked it out to, `_actions/Costroid/costroid/<commit>/action`, which holds it even when another action calls this one, and falls back to `github.action_ref`. Before it trusts `SHA256SUMS`, it runs `cosign verify-blob` with the identity above for that tag and with `--certificate-github-workflow-sha` set to that commit, so the signature must come from a release built from the commit the workflow pinned. It then checks the binary against its line, and installs nothing when either check fails. cosign itself comes from `sigstore/cosign-installer`, pinned by SHA in the action as in the release workflow.

Passing `version` and `sha256` to the action skips the signature, and the action checks the binary against the SHA-256 you gave.

The sources for these commands, read on 2026-11-04: Sigstore's [verify page](https://docs.sigstore.dev/cosign/verifying/verify/) for `verify-blob --bundle` and `verify`, and its [CI quickstart](https://docs.sigstore.dev/quickstart/quickstart-ci/) for the identity of a GitHub Actions workflow.
