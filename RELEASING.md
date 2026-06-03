# Releasing Costroid

This is the runbook for cutting a Costroid release. The release **infrastructure** is
configured and dry-run-verified in the repo (M7); the **actual** publish is a deliberate,
human-triggered action and is intentionally not automated to run on a normal push.

> **Nothing in this repo publishes by itself.** The release workflow
> ([.github/workflows/release.yml](.github/workflows/release.yml)) runs only on a pushed
> version tag (`v*`); on pull requests it runs `dist plan` and nothing else. No `cargo publish`,
> `npm publish`, tag, or GitHub Release happens until a maintainer performs the steps below.

---

## What the first release ships

`v0.1.0` ships the `costroid` binary for six targets, with four installers, via
[cargo-dist](https://github.com/axodotdev/cargo-dist) (binary `dist`, configured in
[dist-workspace.toml](dist-workspace.toml)):

- **Targets:** `x86_64`/`aarch64` Linux (gnu) · `x86_64` Linux (musl, fully static) ·
  `x86_64`/`aarch64` macOS · `x86_64` Windows.
- **Installers:** shell (`curl | sh`), PowerShell (`irm | iex`), Homebrew (tap), npm, plus
  crates.io — `cargo install costroid` (from source) and `cargo binstall costroid` (prebuilt,
  pulled off the GitHub Release; resolves via the crates.io index).
- **Integrity & provenance:** every artifact gets a SHA-256 checksum and a keyless GitHub
  build-provenance attestation (Actions OIDC — no certificates, no secrets).

**Not in v0.1.0** (deferred): macOS notarization / Windows Authenticode code-signing, Scoop, MSI,
the `costroid-mcp` crate (Phase 4).

---

## Prerequisites the maintainer sets up once

These are **not** done by this repo's automation — a human with org access must do them before
the first real release, or release-time jobs will fail.

1. **GitHub repos under the `Costroid` org:**
   - `Costroid/costroid` (this repo).
   - `Costroid/homebrew-tap` — an (empty) repo cargo-dist pushes the generated formula into.
2. **GitHub Actions secrets** (repo → Settings → Secrets and variables → Actions):
   - `HOMEBREW_TAP_TOKEN` — a token with write access to `Costroid/homebrew-tap` (the default
     `GITHUB_TOKEN` cannot push cross-repo). Required by the `publish-homebrew-formula` job.
   - `NPM_TOKEN` — an npm automation token with publish rights to the `costroid` package.
     Required by the `publish-npm` job (so `npx costroid` / `npm i costroid` resolve).
3. **Actions permissions:** the release workflow declares `contents: write`, `id-token: write`,
   and `attestations: write`. Ensure the repo's Actions settings permit them (attestations and
   OIDC are on by default for public repos).
4. **crates.io publishing:** a `CARGO_REGISTRY_TOKEN` (crates.io API token) on an account with a
   **verified email**, used by the manual `cargo publish` steps — see the crates.io section below.

---

## Cutting a release (the deliberate human steps)

1. Make sure `main` is green: the full gate (`fmt` / `clippy -D warnings` / `test`), the FOCUS
   conformance job, the license (`cargo deny`) job, and the offline-acceptance job all pass.
2. Confirm the version. The workspace is versioned in lockstep via
   [Cargo.toml](Cargo.toml) `[workspace.package].version`. For `0.1.0` it is already set.
3. Sanity-check the plan locally (no publish):
   ```bash
   dist plan                       # prints the artifacts/installers that will be built
   dist build --artifacts=local    # builds the host artifacts + checksums into target/distrib/
   ```
4. **Tag and push** — this is the trigger that starts the real release:
   ```bash
   git tag v0.1.0
   git push origin v0.1.0
   ```
   The `Release` workflow then: builds every target, attests + checksums each artifact, creates
   the GitHub Release with all assets, pushes the Homebrew formula to the tap, and publishes the
   npm package. The shell/PowerShell/cargo-binstall installers resolve from the GitHub Release.

To release a fix, bump the version and push the new `vX.Y.Z` tag.

---

## crates.io publish

v0.1.0 is published to crates.io — all four crates (`costroid-focus`, `costroid-providers`,
`costroid-core`, `costroid`) — so `cargo install costroid` and `cargo binstall costroid` both work.

Publish future versions in **dependency order** (each crate must be on crates.io before its
dependents), with `CARGO_REGISTRY_TOKEN` configured:

```
costroid-focus  →  costroid-providers  →  costroid-core  →  costroid (cli)
```

```bash
cargo publish -p costroid-focus
cargo publish -p costroid-providers   # recent cargo waits for the index between each
cargo publish -p costroid-core
cargo publish -p costroid
```

Gotchas (learned shipping v0.1.0):
- **A verified email** on the crates.io account is required before the first publish.
- **Bundled assets must live inside the crate.** `costroid-core` `include_str!`s its pricing JSON;
  it lives at `crates/costroid-core/pricing/pricing.v1.json` (not the workspace root) — cargo only
  packages files under the crate dir, so a standalone verify build fails otherwise. Keep any new
  bundled data inside its crate.
- **Validate before publishing:** `cargo package -p <crate> --list` (any crate), and
  `cargo publish --dry-run -p <crate>` (full verify — works once the crate's siblings are already
  on crates.io).
- Versions are permanent (yank-only) — publish deliberately, in order.

**`costroid-mcp`** does not exist yet (Phase 4). Its crates.io name is intentionally left
unclaimed; we do not publish a placeholder.

---

## Signing status and how to upgrade it

- **Today (free):** keyless GitHub build-provenance attestations + SHA-256 checksums. Verify with
  `gh attestation verify <file> --repo Costroid/costroid`. This gives supply-chain provenance and
  integrity, but is **not** OS code-signing — first run shows an "unidentified developer"
  (macOS) / SmartScreen (Windows) prompt.
- **To remove the OS warnings later (paid):** add macOS notarization (Apple Developer ID,
  ~$99/yr) and Windows Authenticode (an EV code-signing cert with HSM/cloud signing, e.g. SSL.com
  eSigner, ~$200–300/yr), then enable cargo-dist's macOS/Windows signing config and add the
  corresponding secrets. These are config toggles on top of the existing pipeline.
