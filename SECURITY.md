# Security policy

Costroid is a local-first, secure-by-design tool, and we take security seriously. This document explains how to report a vulnerability, what's in scope, the security guarantees Costroid is built to uphold, and how releases are kept trustworthy.

## Supported versions

Costroid's current release line is **0.3.x**. Only the **latest release line** receives security fixes; older lines will not be patched unless stated otherwise.

| Version | Security fixes |
| --- | --- |
| Latest release line (0.3.x) | Yes |
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
- The library crates: `costroid-core`, `costroid-focus`, `costroid-providers`, and the off-by-default `costroid-connect` (the network/credential boundary — feature-gated and off by default; today it holds the OS-keychain credential store and a non-secret "what is linked" index, and still makes no network call).
- Release artifacts and installers, including the **integrity of the bundled pricing data** and the release/signing pipeline.

**Out of scope:**

- The separate, future **web platform** — it will live in its own repository with its own security policy.
- The **AI providers' own services, APIs, and accounts** (Claude Code, Codex, Cursor, etc.) — report those to the respective vendors.
- Vulnerabilities in **third-party dependencies** that should be reported upstream — though please **do tell us** if Costroid is materially affected so we can update or mitigate.
- General hardening suggestions that aren't actual vulnerabilities, social engineering of maintainers, and issues that require an already-compromised host (see Threat model).

## Security model

These are the commitments Costroid is designed around. They follow directly from its local-first, secure-by-design principles.

- **Local-first.** Costroid reads data already on your machine. **The default, local-only build makes no network calls at all** — this is enforced by an `strace` offline-acceptance test and a forbidden-crates test. Any network access is confined to the optional `costroid-connect` crate, which is behind a Cargo feature and off by default — and today that crate contains **no network code at all** (it holds only the keychain credential store; the two-tier forbidden-crates test and the `strace` harness prove no networking crate is even linked). When the network client lands (T9/T10, v0.4.0), every call will additionally require an explicit, user-initiated `connect` action to a provider endpoint you authorized. (The offline-acceptance and forbidden-crates tests are two-tier and resolved per shipped target: the default/local-only build forbids any networking/TLS/keychain crate and must not even link `costroid-connect`; for `--features connect` builds, the sanctioned `ureq`/`rustls`/`keyring` trio is the **allowlist cap** — today only `keyring` is actually linked, and the test asserts it is; `ureq`/`rustls` arrive with the T9 HTTP client — while async runtimes, OpenSSL, other HTTP clients, and all telemetry stay forbidden.) Until T9/T10 land the dynamic connect-action `strace` test (today an explicit stub in the harness), the feature-on no-network guarantee rests on the static per-target resolved-graph test plus the T8 feature-on `strace` baseline: a normal `--features connect` run attempts no network and writes no secret or file residue to `$HOME`.
- **No telemetry.** There is no telemetry by default. Any future update check is opt-in, clearly disclosed, and individually disableable.
- **Your data never leaves your device.** Usage and cost data are processed locally and are not transmitted anywhere.
- **Secrets live only in the OS keychain.** API keys and tokens are stored solely via the system keychain (macOS Keychain, Windows Credential Manager, Linux Secret Service) — the keychain-backed credential store now lives in `costroid-connect` (off by default). Secrets are held in memory as redacted secret strings and are **never** written to disk, configuration files, or logs.
- **No backend.** Credentials flow strictly between your device and the provider. **There is no Costroid server** in this product, and nothing is proxied through one.
- **Authentication source ladder.** Costroid prefers the safest source that yields each datum, and **only tiers 0–3 are ever built**: (0) **local artifacts** already on disk; (1) **sanctioned push/hook** — Claude Code's `statusLine` push (an Anthropic-built extension point that hands Costroid the live `rate_limits` block locally, with zero token reuse and zero API tokens), captured into a **no-secret local cache** at `${XDG_STATE_HOME:-$HOME/.local/state}/costroid/claude-rate-limits.json` (created by `costroid setup-statusline`) holding **only** two `used_percentage` values (0–100), two `resets_at` stamps, and one capture time — **no token, prompt, or credential** — and parsed defensively (out-of-range/poisoned values → "unavailable", never a crash); (2) **sanctioned OAuth** (the provider's own first-class third-party OAuth, e.g. GitHub; planned/deferred); (3) **your own API key** entered by you for a provider's usage API (Anthropic / OpenAI / Gemini), to reconcile the local estimate against the real bill. Tiers 2–3 store secrets **only** in the OS keychain. (4) **Never** reuse any credential, session, or token against a non-sanctioned, undocumented, or internal endpoint, and **never read browser cookies** — that is the account-ban path and a Terms-of-Service violation; where it would be the only route, the datum stays **unavailable**, never fetched. A provider with no sanctioned source (Cursor today) is detect-only, and its usage/quota stays "unavailable."
- **Sanctioned sources only; your credentials are your responsibility.** Costroid reads only local artifacts and provider-sanctioned channels (the ladder above) and never reuses a credential or session against a non-sanctioned, undocumented, or internal endpoint. If you connect your own API key or a sanctioned login (optional, off by default), you remain responsible for your use of those credentials and for complying with each provider's terms of service.
- **Verifiable releases.** Release artifacts are published with SHA-256 checksums and keyless GitHub build-provenance attestations, so you can verify their origin and integrity (see below). OS code-signing (macOS notarization, Windows Authenticode) is not yet in place — planned for a later release.
- **Dependency hygiene.** Costroid is Apache-2.0 and uses permissively-licensed dependencies only (no copyleft). CI enforces this with `cargo-deny`: the license + bans checks run offline against the committed lockfile, and RustSec advisory scanning runs in a dedicated online CI job.
- **Auditing the lockfile.** `Cargo.lock` is an all-features union, so it contains phantom entries (e.g. `zbus` and `async-io`, from keyring's unused async Secret Service path) that are **not** in any shipped binary's resolved dependency graph. The forbidden-crates test (`apps/cli/tests/offline.rs`) resolves the real graph per target with `cargo metadata --filter-platform` across all six shipped triples, proving those entries are absent from what actually ships.
- **Untrusted input.** Provider log files **and the local rate-limits cache** are treated as untrusted input and parsed defensively; malformed, out-of-range, or poisoned data degrades to "unavailable" and is handled gracefully, never crashing unsafely or executing anything.

## Threat model

**What Costroid protects:** the confidentiality of your usage and cost data and your credentials — both stay on your machine — and the integrity and provenance of official release binaries (via checksums and build attestations).

**What Costroid does not protect against:** a compromised host. If your machine, OS user, or account is already compromised, an attacker may be able to read the same local files and keychain entries Costroid does; that is outside what this tool can defend. We also cannot vouch for the upstream AI tools whose logs Costroid reads — we parse their output defensively, but we don't control it.

**Not a security property:** local cost figures are **estimates** (your tokens × current prices), not authoritative billing. Don't rely on them for anything security- or compliance-critical; reconcile against the provider's invoice.

## Build and release integrity

Releases are produced by an automated GitHub Actions pipeline. Every artifact is published with a SHA-256 checksum and a keyless GitHub build-provenance attestation (Actions OIDC — no private signing keys), establishing that it was built by Costroid's CI from this repository.

> **Note on OS code-signing.** Current release binaries are **not** OS-code-signed: there is no Apple Developer ID notarization (macOS) or Authenticode signature (Windows) yet, so first run may show an "unidentified developer" (macOS) or SmartScreen (Windows) prompt. Notarization and Authenticode are planned for a later release; provenance attestations and checksums are the integrity mechanism today.

You can verify a downloaded artifact by checking its published checksum and verifying its attestation with the GitHub CLI:

```bash
gh attestation verify <downloaded-file> --repo Costroid/costroid
```

If verification fails, do not run the artifact, and please report it through the private channel above.