# Security policy

Costroid is a local-first, secure-by-design tool. This explains how to report a vulnerability, what's in scope, and the guarantees Costroid upholds.

## Supported versions

The current release line is **0.6.x**. Only the latest line gets security fixes.

| Version | Security fixes |
| --- | --- |
| Latest line (0.6.x) | Yes |
| Older releases | No (upgrade) |
| Unreleased `main` | Best-effort |

## Reporting a vulnerability

Report privately — **do not** open a public issue/PR/discussion for a vulnerability.

- **Preferred:** GitHub private vulnerability reporting — Security tab → Advisories → **Report a vulnerability**.
- **Or email:** **costroid@protonmail.com**.

Include where you can: affected version/commit, description, repro steps, impact, and any PoC. We aim to acknowledge within a few days and credit reporters in the advisory unless you prefer otherwise.

**Coordinated disclosure.** Give us a reasonable chance to fix before disclosing; we'll coordinate timing with you.

**Safe harbor.** Good-faith research within the scope below is authorized — we won't pursue legal action against researchers who avoid privacy violations, data destruction, and service disruption, only touch systems/data they own, and allow time to respond.

## Scope

**In scope:** the `costroid` CLI/TUI and the `costroid-bar` egui taskbar GUI; the library crates `costroid-core`, `costroid-focus`, `costroid-providers`, the read-only `costroid-config` (the `[budget]`/`[alerts]` TOML schema), and the off-by-default `costroid-connect` (the network/credential boundary); and the release artifacts/installers (incl. bundled-pricing integrity and the signing pipeline).

**Out of scope:** the separate future **web platform** (own repo/policy); the **AI providers' own services and accounts** (report to the vendor); **third-party dependencies** (report upstream — but do tell us if Costroid is materially affected); and non-vulnerability hardening suggestions or issues that require an already-compromised host.

## Security model

- **Local-first, zero-network default.** The default build makes **no network calls** and does not even link `costroid-connect` — enforced by an `strace` offline-acceptance test and a two-tier forbidden-crates test (default tier forbids any networking/TLS/keychain crate; `--features connect` tier caps the allowlist at the sanctioned `ureq`/`rustls`/`keyring` trio).
- **Network only on explicit action.** Under `--features connect`, a call happens **only** on a user-initiated `connect` / `connections --check` / `reconcile`, and only as a **read-only HTTPS GET** to the one authorized provider host (blocking `ureq` + `rustls`; HTTPS-only, GET-only, host fixed at construction, redirects/proxies disabled, off-host requests refused before any I/O). Proven by a network-namespace fail-closed test.
- **No telemetry.** None by default; any future update check is opt-in, disclosed, and disableable.
- **Your data never leaves your device.** Usage and cost data are processed locally.
- **Secrets only in the OS keychain** (macOS Keychain / Windows Credential Manager / Linux Secret Service), via `keyring`. Held in memory as redacted secret strings; **never** written to disk, config, or logs.
- **No backend.** Credentials flow strictly device↔provider; there is no Costroid server.
- **Authentication source ladder** (only tiers 0–3 ever built): (0) local artifacts on disk; (1) sanctioned push/hook — Claude Code's `statusLine` `rate_limits` capture into a no-secret local cache; (2) sanctioned OAuth (e.g. GitHub; deferred); (3) your own API key for a provider usage API (Anthropic / OpenAI). (4) **Never** reuse a credential, session, or cookie against an undocumented or internal endpoint — that datum stays "unavailable." A provider with no sanctioned source (Cursor today) is detect-only.
- **Transport trust — no certificate pinning.** Connections validate TLS against your **OS trust store** (`rustls` + `rustls-native-certs`); Costroid does **not** pin certificates. A host whose trust store holds an attacker-planted or corporate-MITM root could intercept a `connect`/`reconcile` request — which carries an org-wide admin key. Connect only on a host whose trust store you control, and prefer a dedicated, instantly-revocable key. SPKI pinning is possible future hardening.
- **Verifiable releases.** Artifacts ship with SHA-256 checksums and keyless GitHub build-provenance attestations. OS code-signing (macOS notarization, Windows Authenticode) is not yet in place.
- **Dependency hygiene.** Apache-2.0 with permissive deps only (no copyleft), enforced by `cargo-deny` (licenses + bans offline; RustSec advisories in a dedicated online job).
- **Untrusted input.** Provider logs and the rate-limits cache are parsed defensively; malformed/out-of-range/poisoned data degrades to "unavailable" and never crashes.

**Legal note.** The connection flows passed a **maintainer legal self-attestation** (accepted 2026-06-16): a risk-acceptance for an open-source, local-first tool using the user's own key against documented first-party endpoints — **not** a legal opinion from counsel. Professional review is advised before commercializing.

## Threat model

**Protects:** the confidentiality of your usage/cost data and credentials (both stay local) and the integrity/provenance of official release binaries.

**Does not protect against:** an already-compromised host (an attacker can read the same files and keychain entries Costroid does); the upstream AI tools whose logs we parse defensively but don't control; or TLS interception by an attacker-planted/MITM root in the OS trust store (no cert pinning) — treat a connected admin key as a root credential.

**Not a security property:** local cost figures are **estimates** (tokens × prices), not authoritative billing — reconcile against the provider invoice.

## Build and release integrity

Releases are built by an automated GitHub Actions pipeline; every artifact carries a SHA-256 checksum and a keyless build-provenance attestation (Actions OIDC, no private keys). Binaries are **not** yet OS-code-signed, so first run may show an "unidentified developer" (macOS) or SmartScreen (Windows) prompt — provenance and checksums are today's integrity mechanism.

Verify a download with the GitHub CLI:

```bash
gh attestation verify <downloaded-file> --repo Costroid/costroid
```

If verification fails, do not run the artifact, and report it via the private channel above.
