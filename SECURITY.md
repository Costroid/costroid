# Security policy

Costroid is a local-first, secure-by-design tool, and we take security seriously. This document explains how to report a vulnerability, what's in scope, the security guarantees Costroid is built to uphold, and how releases are kept trustworthy.

## Supported versions

Costroid is in **early development and has not shipped a release yet**, so there are no released versions to support today. Once releases begin, only the **latest release line** will receive security fixes; older lines will not be patched unless stated otherwise.

| Version | Security fixes |
| --- | --- |
| Latest release (once releases begin) | Yes |
| Older releases | No (upgrade to the latest) |
| Unreleased `main` | Best-effort |

## Reporting a vulnerability

**Please report security issues privately. Do not open a public issue, pull request, or discussion for a vulnerability** — public disclosure before a fix puts users at risk.

Preferred channel — **GitHub private vulnerability reporting:**

1. Go to the repository's **Security** tab.
2. Open **Advisories** and click **Report a vulnerability**.
3. Fill in the private report form. The maintainers are notified, and the discussion and any fix happen privately within GitHub until an advisory is published.

If that button isn't available, or you'd rather use email, contact **costroid@protonmail.com**.

**Please include**, where you can: the affected version or commit, a clear description, step-by-step reproduction, the impact, any proof-of-concept, and a suggested fix if you have one.

**What to expect.** Costroid is a small, early-stage project, so timelines are **best-effort**: we aim to acknowledge a report within a few days, keep you updated as we investigate, and coordinate the timing of any public disclosure with you. We're grateful for reports and will **credit reporters** in the published advisory unless you'd prefer to remain anonymous.

**Coordinated disclosure.** Please give us a reasonable opportunity to investigate and release a fix before disclosing publicly. We'll work with you on the details and timing.

**Safe harbor.** We consider security research conducted in good faith and within the scope below to be authorized. We will not pursue or support legal action against researchers who act in good faith, avoid privacy violations, data destruction, and service disruption, only interact with systems and data they own or have permission to test, and give us reasonable time to respond before disclosure.

## Scope

**In scope:**

- The `costroid` CLI and TUI.
- The library crates: `costroid-core`, `costroid-focus`, `costroid-providers`, `costroid-mcp`.
- The tray app (`apps/bar`) once it ships.
- Release artifacts and installers, including the **integrity of the bundled pricing data** and the release/signing pipeline.

**Out of scope:**

- The separate, future **web platform** — it will live in its own repository with its own security policy.
- The **AI providers' own services, APIs, and accounts** (Claude Code, Codex, Cursor, etc.) — report those to the respective vendors.
- Vulnerabilities in **third-party dependencies** that should be reported upstream — though please **do tell us** if Costroid is materially affected so we can update or mitigate.
- General hardening suggestions that aren't actual vulnerabilities, social engineering of maintainers, and issues that require an already-compromised host (see Threat model).

## Security model

These are the commitments Costroid is designed around. They follow directly from its local-first, secure-by-design principles.

- **Local-first.** Costroid reads data already on your machine. **In Phase 1 it makes no network calls at all.**
- **No telemetry.** There is no telemetry by default. Any future update check is opt-in, clearly disclosed, and individually disableable.
- **Your data never leaves your device.** Usage and cost data are processed locally and are not transmitted anywhere.
- **Secrets live only in the OS keychain.** When optional login arrives (Phase 2), tokens are stored solely via the system keychain (macOS Keychain, Windows Credential Manager, Linux Secret Service). They are **never** written to disk, configuration files, or logs.
- **No backend.** Credentials flow strictly between your device and the provider. **There is no Costroid server** in this product, and nothing is proxied through one.
- **Authentication tiers.** Phase 1 uses local logs only (no credentials). Phase 2 adds reuse of an existing local session and an optional OAuth login (keychain-stored). Reading browser cookies, if ever offered, is a clearly-disclosed, off-by-default last resort.
- **Signed, verifiable releases.** Release artifacts are signed and published with checksums and build attestations (see below).
- **Dependency hygiene.** Costroid is Apache-2.0 and uses permissively-licensed dependencies only (no copyleft), with dependency advisory scanning in CI.
- **Untrusted input.** Provider log files are treated as untrusted input and parsed defensively; malformed data should be handled gracefully, never crash unsafely or execute anything.

## Threat model

**What Costroid protects:** the confidentiality of your usage and cost data and your credentials — both stay on your machine — and the integrity and authenticity of official release binaries (via signing and attestations).

**What Costroid does not protect against:** a compromised host. If your machine, OS user, or account is already compromised, an attacker may be able to read the same local files and keychain entries Costroid does; that is outside what this tool can defend. We also cannot vouch for the upstream AI tools whose logs Costroid reads — we parse their output defensively, but we don't control it.

**Not a security property:** local cost figures are **estimates** (your tokens × current prices), not authoritative billing. Don't rely on them for anything security- or compliance-critical; reconcile against the provider's invoice.

## Build and release integrity

Releases are produced by an automated, signed pipeline:

- **macOS** builds are signed with an Apple Developer ID and notarized.
- **Windows** builds are signed with Authenticode.
- **All artifacts** are published with checksums and GitHub build attestations.

Once releases exist, you can verify a downloaded artifact by checking its published checksum and verifying its attestation with the GitHub CLI:

```bash
gh attestation verify <downloaded-file> --repo Costroid/costroid
```

If verification fails, do not run the artifact, and please report it through the private channel above.