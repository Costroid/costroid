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

The SBOM catalogs the Go and pnpm source dependency graphs, including the
frontend embedded in the binary. DuckDB C static libraries are outside Syft's
Go cataloger and govulncheck's reach; this release process does not analyze that
C-side vulnerability surface.

This posture consists of SBOM-attested, signed releases with a coordinated
vulnerability-disclosure policy, aligned with the EU CRA (Regulation (EU)
2024/2847) Annex I vulnerability-handling expectations, ahead of the 2026/2027
obligation deadlines.
