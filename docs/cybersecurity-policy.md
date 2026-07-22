<!--
SPDX-License-Identifier: Apache-2.0
Copyright 2026 The Costroid Authors
-->

# Costroid cybersecurity policy

This is Costroid's written cybersecurity policy. It documents, in one place,
how the project develops software securely, how it handles vulnerabilities
from report to disclosure, how it reports outward when something is actively
exploited, and how each statement here can be checked against the repository.
The policy is versioned in Git next to the code it governs; changes to it are
recorded in the [history](#history) section.

## Position under the EU Cyber Resilience Act

Costroid is open-source software, licensed under Apache-2.0 and distributed
free of charge. The project claims no classification under the EU Cyber
Resilience Act (Regulation (EU) 2024/2847): it does not present itself as a
manufacturer or as an open-source software steward under that regulation, and
it claims no CRA compliance, conformity assessment, certification, or CE
marking.

What it does is voluntary. This policy adopts the practices that Article 24 of
the CRA expects of open-source software stewards: a documented, verifiable
cybersecurity policy, effective vulnerability handling, and sharing of
information about discovered vulnerabilities. For outbound notifications it
commits to the voluntary reporting route of Article 15, which by its own terms
creates no additional obligations for a voluntary reporter (Article 15(5)).
The intent is that users and downstream integrators get the same security
practice from Costroid regardless of how the project is ever classified.

## Scope

This policy covers the software built from the
[Costroid/costroid](https://github.com/Costroid/costroid) repository: the
`costroid` binary, the web dashboard embedded in it, and the container image
published to `ghcr.io/costroid/costroid`. Only tagged releases are supported.
The supported-versions table and the support window (security maintenance
does not end without at least six months of advance public notice) are in
[SECURITY.md](../SECURITY.md).

The security model this policy defends is documented in the
[threat model](../docs-site/src/content/docs/security/threat-model.md) and the
[deployment security guide](security.md). Its core commitments: Costroid
handles cost and usage metadata only, and never ingests, stores, logs, caches,
or transmits prompt or response content from AI sources (the Cardinal Rule);
it binds to loopback by default; and it refuses to start serving without an
explicit authentication decision. With optional outbound features
unconfigured, the core sends nothing. The natural-language `ask` command is
off unless the operator configures a model endpoint. The operator chooses that
endpoint; when enabled, the command sends only the user's question, the static
plan schema, and discovered provider names, tag keys, currency codes, and
business-metric names. It never sends cost amounts, quantities, or store rows.

Reports about the following are out of scope as vulnerabilities:

- Deployments that explicitly opted out of the documented guardrails, such as
  serving with `--no-auth` on an exposed bind. The in-scope question is
  whether Costroid's guardrails can be bypassed, not whether a deployment
  chose to disable them.
- The deployer's reverse proxy, TLS termination, host, and operating system.
- The synthetic demo dataset and the public demo site; they contain no real
  data.
- Findings that presuppose an attacker who already controls the host or the
  operating-system user running Costroid. The threat model's residual-risks
  section describes this boundary, including the opt-in at-rest encryption.

When in doubt, report privately anyway. We would rather triage a non-issue
than miss a real one.

## Secure development practices

- **Secure by default.** The server binds to loopback unless the deployer
  explicitly chooses an exposed address, and it refuses to start without an
  authentication decision. Disabling authentication requires an explicit flag
  and prints a loud warning.
- **Least privilege and secret hygiene.** Connector credentials are
  least-privilege and read-only; secrets are supplied via files or the
  environment, never on the command line, and are never logged. Stored
  connector credentials live in an encrypted local vault.
- **AI-source content stays outside the data path.** Structural checks in CI
  enforce that prompt and response content from AI sources is never ingested,
  stored, logged, cached, or transmitted. The optional natural-language
  command has a separate, explicit boundary: it is off until an operator
  chooses an endpoint, and its exact outbound fields are tested. The threat
  model documents both boundaries and their limits.
- **Every change is checked by CI.** Every push to `main` runs a pipeline that
  verifies generated code is in sync, runs the linters, runs the full Go and
  web test suites, builds the binary, and runs the Windows suite. Contributions
  arriving as pull requests are additionally checked for a Developer
  Certificate of Origin sign-off. Stated precisely, because the distinction
  matters to anyone relying on this page: the project is at present maintained
  by a single author who commits directly to `main`, so these pipelines are
  detectors that report on what has landed, not gates that block a merge, and
  external code review is not part of the current process. Making them
  blocking, via required status checks, is planned and will be described here
  once it is true rather than before.
- **Known-vulnerability scanning.** Every CI run scans the Go module graph for
  reachable known vulnerabilities with `govulncheck`; a reachable finding
  fails the build.

## Dependencies and supply chain

Dependency versions are pinned by the Go module files and the pnpm lockfile,
so a build resolves exactly the dependency set recorded in the repository.
Each release publishes a
CycloneDX 1.6 source SBOM covering the Go and pnpm dependency graphs;
[SECURITY.md](../SECURITY.md) states the SBOM's honest limit (DuckDB's C-side
static libraries are outside the cataloger's reach).

If we discover a vulnerability in a dependency, we report it upstream to that
project. If a vulnerability in Costroid affects other projects we know of, we
share what they need to assess their exposure as part of coordinated
disclosure.

## Release integrity

Release checksums are signed keylessly in GitHub Actions, release artifacts
carry build-provenance attestations, and the container images are signed and
carry provenance and SBOM attestations. The verification commands are in
[SECURITY.md](../SECURITY.md). Only artifacts published through this release
process are Costroid releases; nothing else should be treated as one.

## Reporting a vulnerability

The reporting channels, and what to include, are documented in
[SECURITY.md](../SECURITY.md): GitHub Private Vulnerability Reporting is the
primary channel, and `security@costroid.com` works without a GitHub account.
The process commitments:

- We acknowledge reports within five business days on a best-effort basis.
- Every report gets a human answer, including reports we assess as invalid,
  out of scope, or automatically generated; those may be closed with a short
  explanation, and we keep a record of the decision.
- Whether something is a vulnerability is decided by the maintainers after
  triage; if we disagree with a reporter, we say why.
- We credit reporters in the advisory unless they prefer otherwise. There is
  no bug bounty; we say so here to set expectations honestly.
- We ask reporters to keep the report private until a coordinated disclosure,
  and we extend the same courtesy: we do not disclose reporter details.

Contributors who find a vulnerability while working on Costroid are asked to
use the same private channels rather than a public issue or pull request.

## Handling, remediation, and disclosure

Triage prioritizes by impact on the confidentiality, integrity, and
availability of billing data and of the deployments running Costroid. Fixes
land on the supported release line and ship as a release the moment they are
ready; we do not sit on completed security fixes.

Every resolved vulnerability is published as a GitHub Security Advisory at
<https://github.com/Costroid/costroid/security/advisories>, with a CVE
identifier requested where applicable, after users have had a reasonable
opportunity to update. Advisories plus release notes are how users learn of a
vulnerability and of the version that fixes it.

## Outbound reporting to EU authorities

If we become aware of a vulnerability in Costroid that is being actively
exploited, or of a compromise of the project's development or release
infrastructure that could affect shipped artifacts, then in addition to the
coordinated-disclosure process above we will notify the EU coordinating
authorities (a CSIRT designated as coordinator and ENISA) on a voluntary
basis under Article 15 of Regulation (EU) 2024/2847.

Mechanics: these notifications will go through the CRA Single Reporting
Platform operated by ENISA once it is live; the platform is announced to be
operational by 11 September 2026, and its address will be published on
[ENISA's Single Reporting Platform page](https://www.enisa.europa.eu/topics/product-security-and-certification/single-reporting-platform-srp).
Until then, or if the platform is unavailable, the fallback is direct contact
with the relevant national CSIRT via the
[EU CSIRTs Network](https://csirtsnetwork.eu/). We use the staged structure of
the CRA's Article 14 as guidance for such notifications: an early warning
without undue delay, a fuller notification as facts firm up, and a closing
report once a fix or mitigation is available. Users are informed in parallel
through the advisory process above; an outbound report never replaces telling
our users.

## Cooperation with authorities

If a market surveillance authority sends the project a reasoned request, we
will respond and provide this policy and the evidence behind it.

## Verifiability

This policy is intended to be checkable, not aspirational. Each operational
claim maps to public evidence:

| Claim | Evidence |
| --- | --- |
| CI gates (generated-code guard, lint, tests, build) | [`.github/workflows/ci.yml`](../.github/workflows/ci.yml) |
| Reachable-vulnerability scanning | the `govulncheck` step in [`.github/workflows/ci.yml`](../.github/workflows/ci.yml) |
| DCO sign-off enforcement | [`.github/workflows/dco.yml`](../.github/workflows/dco.yml) |
| Signed, SBOM-attested releases | [`.github/workflows/release.yml`](../.github/workflows/release.yml) and the verification commands in [SECURITY.md](../SECURITY.md) |
| Cardinal Rule structural enforcement | the [threat model](../docs-site/src/content/docs/security/threat-model.md)'s "Structural enforcement" section |
| Disclosure process and support window | [SECURITY.md](../SECURITY.md) |
| Published advisories | <https://github.com/Costroid/costroid/security/advisories> |

One honest limit, stated as in the threat model: Costroid has not yet had a
third-party security audit. The claims above are backed by the code and its
CI checks, not by an external review.

## History

| Version | Date | Change |
| --- | --- | --- |
| 1.0 | 2026-07-20 | Initial policy. |
