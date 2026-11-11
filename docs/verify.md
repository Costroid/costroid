# Verify a release

Every release carries its binaries, `SHA256SUMS`, an SPDX SBOM and one Sigstore bundle per file. Costroid's release workflow signs each file with cosign, keyless, under its own GitHub Actions identity. The image is signed the same way. Each release also carries SLSA build provenance for each file and for the image, as GitHub artifact attestations. 1.0.0-alpha.23 and earlier releases carry none, so `gh attestation verify` fails on them.

Each release also carries `NOTICE`, which lists the modules the binaries link and their licences, and `LICENSE`. `SHA256SUMS` names both, each is signed and attested like a binary, and the image holds the same two files in `/usr/share/doc/costroid/`.

The commands below check `1.0.0-alpha.23`. Replace the version in every line for another release.

## The checksum

```sh
curl -fsSLO https://github.com/Costroid/costroid/releases/download/v1.0.0-alpha.23/costroid-linux-amd64
curl -fsSLO https://github.com/Costroid/costroid/releases/download/v1.0.0-alpha.23/SHA256SUMS
grep ' costroid-linux-amd64$' SHA256SUMS | sha256sum --check
```

It prints `costroid-linux-amd64: OK`. A checksum proves the download matches `SHA256SUMS`, and nothing about who wrote `SHA256SUMS`. The signature proves that.

## The signature

With cosign 3 or later:

```sh
curl -fsSLO https://github.com/Costroid/costroid/releases/download/v1.0.0-alpha.23/SHA256SUMS.sigstore.json
cosign verify-blob --bundle SHA256SUMS.sigstore.json \
  --certificate-identity https://github.com/Costroid/costroid/.github/workflows/release.yml@refs/tags/v1.0.0-alpha.23 \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com SHA256SUMS
```

The identity is the release workflow at the release's tag, so a file signed by any other workflow, branch or repository fails. A signed `SHA256SUMS` and a matching checksum together cover the binary. Each binary also has a bundle of its own, `costroid-linux-amd64.sigstore.json`, for the same command on the binary.

## The image

The release notes name the image by digest. Check that digest, never a tag, because a tag can move:

```sh
cosign verify ghcr.io/costroid/costroid@sha256:<digest> \
  --certificate-identity https://github.com/Costroid/costroid/.github/workflows/release.yml@refs/tags/v1.0.0-alpha.23 \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com
```

Image signatures need cosign 3. The release workflow also attaches the image's SPDX SBOM to the image as a signed attestation of type `spdxjson`.

## The provenance

A signature says who signed a file. Provenance also says how it was built: an attestation in the SLSA provenance format names the repository, the workflow, the tag, the commit and the run that produced the file. The release workflow attests each binary, the SBOM and `SHA256SUMS`, and the image by its digest. The attestations are signed with a Sigstore certificate issued to the workflow, like the cosign signatures, and GitHub stores them beside the repository.

With the GitHub CLI signed in, in the directory that holds the download:

```sh
gh attestation verify costroid-linux-amd64 --repo Costroid/costroid
```

The command hashes the file, fetches the attestations for that digest from GitHub, and checks each one's signature and certificate. It exits 0 only when an attestation of the SLSA provenance type covers the file's SHA-256 digest and was signed by a workflow in `Costroid/costroid`. A file changed after the build has another digest, and no attestation covers it.

`--repo` accepts any workflow in the repository. To accept only the release workflow, at the tag you downloaded, set `VERSION` to that release and add two flags:

```sh
gh attestation verify costroid-linux-amd64 --repo Costroid/costroid \
  --signer-workflow Costroid/costroid/.github/workflows/release.yml \
  --source-ref "refs/tags/v$VERSION"
```

The image is checked by the digest the release notes name. The GitHub CLI reads the image from the registry, and its help asks that you be signed in to that registry first:

```sh
gh attestation verify oci://ghcr.io/costroid/costroid@sha256:<digest> --repo Costroid/costroid
```

The attestations are made in the job that signs, from the files the build jobs hand over, so they record the release workflow's run rather than the build job alone. Costroid's GitHub action does not read them. It installs a release only after the cosign check on `SHA256SUMS`, described below. Provenance is for a check by hand.

## A retried signature

The release workflow tries each `cosign` call up to three times. Every attempt signs afresh, with a new key and certificate, so it writes a new entry to Rekor, Sigstore's transparency log. An upload that times out can still write its entry, and cosign's own retry of that upload then fails with HTTP 409 because the entry exists. The workflow's next attempt sends a different entry, so it never meets that conflict.

The earlier entry stays in the log and belongs to no bundle the release carries. The checks on this page read the bundles and the image's signatures, so an extra entry changes none of them. An image whose signing ran more than once carries more than one signature, and `cosign verify` passes when one of them matches the identity.

## What the action checks

The action pinned by its commit installs the release whose tag points at that commit. It reads the commit from the path the runner checked it out to, `_actions/Costroid/costroid/<commit>/action`, which holds it even when another action calls this one, and falls back to `github.action_ref`. Before it trusts `SHA256SUMS`, it runs `cosign verify-blob` with the identity above for that tag and with `--certificate-github-workflow-sha` set to that commit, so the signature must come from a release built from the commit the workflow pinned. It then checks the binary against its line, and installs nothing when either check fails. cosign itself comes from `sigstore/cosign-installer`, pinned by SHA in the action as in the release workflow.

Passing `version` and `sha256` to the action skips the signature, and the action checks the binary against the SHA-256 you gave.

The sources for these commands, read on 2026-11-04: Sigstore's [verify page](https://docs.sigstore.dev/cosign/verifying/verify/) for `verify-blob --bundle` and `verify`, and its [CI quickstart](https://docs.sigstore.dev/quickstart/quickstart-ci/) for the identity of a GitHub Actions workflow.

The sources for the provenance, read on 2026-11-07: the help text of [`gh attestation verify`](https://github.com/cli/cli/blob/v2.103.3/pkg/cmd/attestation/verify/verify.go) at GitHub CLI v2.103.3, the README of [`actions/attest`](https://github.com/actions/attest/blob/v4.2.1/README.md) at v4.2.1, which `actions/attest-build-provenance` v4.2.2 calls, and the [predicate it writes](https://github.com/actions/toolkit/blob/main/packages/attest/src/provenance.ts).
