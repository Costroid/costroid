<!--
SPDX-License-Identifier: Apache-2.0
Copyright 2026 The Costroid Authors
-->

# Security policy

## Supported versions

Costroid is pre-1.0. Security fixes are provided on a best-effort basis for the
latest minor line. Users should update to the newest patch release promptly.

| Version | Supported |
| --- | --- |
| Latest minor line | Best effort |
| Older minor lines | No |

## Support window

Costroid is actively maintained. Security maintenance will not end without at
least six months of advance public notice in this repository and in the release
notes. When Costroid reaches 1.0, this section will be replaced by a
per-minor-line support table with defined support windows.

## Report a vulnerability privately

Do not open a public issue for a suspected vulnerability. Use GitHub Private
Vulnerability Reporting at
<https://github.com/Costroid/costroid/security/advisories/new>. If that channel
is unavailable, email `security@costroid.com` with a concise description,
affected versions, reproduction steps, and impact. Do not include credentials,
customer billing data, or raw AI prompt or response content.

We will acknowledge a report within five business days on a best-effort basis,
triage it, coordinate a fix and release with the reporter, and publish an
advisory after users have had a reasonable opportunity to update. Timelines vary
with severity, exploitability, and fix complexity. Please keep the report
private until coordinated disclosure.

If we become aware of a vulnerability in Costroid that is being actively
exploited, we will, in addition to the coordinated-disclosure process above,
report it to the applicable coordinating authorities in line with the
actively-exploited-vulnerability reporting obligations of the EU CRA
(Regulation (EU) 2024/2847), which apply from 11 September 2026.

Deployment hardening, including authentication and reverse-proxy TLS guidance,
is documented in [docs/security.md](docs/security.md).

## Verify release artifacts

Release archives, the CycloneDX 1.6 source SBOM, and checksums are published
together. The checksums are signed keylessly in GitHub Actions, and release
artifacts receive GitHub build-provenance attestations. Verify them with:

```sh
cosign verify-blob \
  --bundle checksums.txt.sigstore.json \
  --certificate-identity-regexp 'https://github.com/Costroid/costroid/.github/workflows/release.yml@.*' \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com \
  checksums.txt
sha256sum --check checksums.txt
gh attestation verify <artifact> --repo Costroid/costroid
```

`gh attestation verify` requires GitHub CLI 2.49.0 or newer. If that
subcommand is unavailable, upgrade `gh`; meanwhile, the `cosign verify-blob`
and `sha256sum --check` steps above still verify the signed checksums and each
artifact's digest, but do not replace provenance verification.

The container images published to `ghcr.io/costroid/costroid` are signed and attested the same way. Verify an image with:

```sh
cosign verify \
  --certificate-identity-regexp '^https://github\.com/Costroid/costroid/\.github/workflows/release\.yml@refs/tags/v.*$' \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com \
  ghcr.io/costroid/costroid:<version>
gh attestation verify oci://ghcr.io/costroid/costroid:<version> --repo Costroid/costroid
```

The `oci://` form of `gh attestation verify` also requires GitHub CLI 2.49.0
or newer. With an older `gh`, verify the image's provenance and SBOM
attestations directly with cosign:

```sh
cosign verify-attestation \
  --certificate-identity-regexp '^https://github\.com/Costroid/costroid/\.github/workflows/release\.yml@refs/tags/v.*$' \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com \
  --type https://slsa.dev/provenance/v1 \
  ghcr.io/costroid/costroid:<version>
```

Repeat with `--type cyclonedx` to verify the SBOM attestation.

The SBOM catalogs the Go and pnpm source dependency graphs, including the
frontend embedded in the binary. DuckDB C static libraries are outside Syft's
Go cataloger and govulncheck's reach; this release process does not analyze that
C-side vulnerability surface.

This posture consists of SBOM-attested, signed releases with a coordinated
vulnerability-disclosure policy, aligned with the EU CRA (Regulation (EU)
2024/2847) Annex I vulnerability-handling expectations, ahead of the 2026/2027
obligation deadlines.
