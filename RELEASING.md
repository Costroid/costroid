# Releasing Costroid

Runbook for cutting a Costroid release. The product is feature-complete at **v0.6.0**.

> **Nothing publishes on a normal push.** The release workflow
> ([.github/workflows/release.yml](.github/workflows/release.yml)) runs only on a pushed
> version tag (`v*`); on PRs it runs `dist plan` and nothing else. No `cargo publish`,
> `npm publish`, tag, or GitHub Release happens until a maintainer runs the steps below.

## What ships

Via [cargo-dist](https://github.com/axodotdev/cargo-dist) (binary `dist`, config in
[dist-workspace.toml](dist-workspace.toml) + each app's `[package.metadata.dist]`):

- **`costroid` (CLI) — 6 targets:** Linux gnu `x86_64`/`aarch64` · Linux musl `x86_64` (static) ·
  macOS `x86_64`/`aarch64` · Windows `x86_64`. **Installers:** shell, PowerShell, Homebrew (tap),
  npm, + crates.io (`cargo install costroid` / `cargo binstall costroid`).
- **`costroid-bar` (egui taskbar, since v0.6.0) — 5 binary archives:** Linux gnu `x86_64`/`aarch64`,
  macOS `x86_64`/`aarch64`, Windows `x86_64` — **no musl** (GTK3 tray can't static-link) — +
  crates.io (`cargo install costroid-bar`). **No npm/Homebrew/script installers** until the
  macOS/Windows tray matrix is field-verified (those tray paths compile but are not verified). Linux
  build runners auto-install the GTK3/xdo/AppIndicator apt headers.
- **Integrity:** every artifact (both binaries) gets a SHA-256 checksum + a keyless GitHub
  build-provenance attestation (Actions OIDC). **Not** OS code-signed yet — first run shows the
  macOS "unidentified developer" / Windows SmartScreen prompt. See [SECURITY.md](SECURITY.md).
- **Toolchain ≥ 1.92** (the bar's MSRV; CLI/libs keep their 1.88 MSRV — a newer build toolchain
  does not change a published MSRV).

**Deferred:** macOS notarization, Windows Authenticode, Scoop, MSI.

## Cutting a release

1. **Green `main`:** full gate (`fmt` / `clippy -D warnings` / `test`, incl. the `--features connect`
   build) + MSRV + FOCUS conformance + `cargo deny` + offline-acceptance all pass.
2. **Bump version** in [Cargo.toml](Cargo.toml) `[workspace.package].version` to `X.Y.Z` (lockstep)
   and refresh `Cargo.lock`; commit both. The tag must equal this version exactly.
3. **Sanity-check locally** (no publish): `dist plan`, then
   `dist build --artifacts=local --target x86_64-unknown-linux-gnu`.
4. **Tag and push** (the trigger):
   ```bash
   git tag vX.Y.Z && git push origin vX.Y.Z
   ```
   CI builds every target, attests + checksums each artifact, creates the GitHub Release, pushes the
   Homebrew formula, and publishes npm. (Homebrew/npm are skipped on prereleases, so a `vX.Y.Z-rc.N`
   tag rehearses build + attest + Release only.)
5. **Verify:** `gh attestation verify <file> --repo Costroid/costroid`; confirm the GitHub Release
   assets, the Homebrew tap, and `cargo install costroid` / `costroid-bar` resolve `X.Y.Z`.

**Gotcha — keep `precise-builds = true` in [dist-workspace.toml](dist-workspace.toml):** the shipped
`costroid` binary is connect-OFF (no `costroid-connect`/`keyring`/`libdbus`), but a default
`dist build` compiles every workspace member and would fail on CI Linux runners (no `libdbus-1-dev`);
`precise-builds` builds only `-p costroid`. A local dry-run won't catch this (dev boxes have libdbus).

## crates.io publish

Publish in **dependency order** (each crate on crates.io before its dependents), with
`CARGO_REGISTRY_TOKEN` set, from the **same commit** as the tag:

```
costroid-focus → costroid-providers → costroid-core → costroid-config → costroid-connect → costroid → costroid-bar
```

```bash
cargo publish -p costroid-focus
cargo publish -p costroid-providers   # recent cargo waits for the index between each
cargo publish -p costroid-core
cargo publish -p costroid-config
cargo publish -p costroid-connect
cargo publish -p costroid
cargo publish -p costroid-bar
```

Notes:
- Verified email required before the first publish; versions are permanent (yank-only).
- Bundled assets must live inside the crate (`costroid-core`'s pricing JSON; `costroid-bar`'s
  JetBrains Mono in `apps/bar/assets/`); cargo only packages files under the crate dir.
- Each crate ships the root `README.md` via `readme.workspace = true` (`costroid-bar` carries its
  own). Validate with `cargo package -p <crate> --list` / `cargo publish --dry-run -p <crate>`.

## One-time maintainer setup

- **Local Linux build deps:** `sudo apt-get install -y libdbus-1-dev libsecret-1-dev` (the workspace
  build links `costroid-connect`'s keychain backend; the default `costroid` binary does not).
- **Repos:** `Costroid/costroid` + `Costroid/homebrew-tap` (initialized with a README, not empty).
- **Actions secrets:** `HOMEBREW_TAP_TOKEN` (cross-repo write to the tap), `NPM_TOKEN` (publish
  `costroid`), `CARGO_REGISTRY_TOKEN` (crates.io, verified-email account).
- **Actions permissions:** `contents: write`, `id-token: write`, `attestations: write`.
