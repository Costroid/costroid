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
- **`costroid-server` (loopback web UI/API, since this milestone) — 5 binary archives:** Linux gnu
  `x86_64`/`aarch64`, macOS `x86_64`/`aarch64`, Windows `x86_64` — **no musl** (it need not
  static-link: it links only `tiny_http`, no libdbus/GTK/TLS/async-runtime) — + crates.io
  (`cargo install costroid-server`). **No npm/Homebrew/script installers** (archives only, mirroring
  the bar). The server binds `127.0.0.1` only and makes no outbound network. Needs no extra apt
  headers (no GTK/libdbus), so `precise-builds` keeps the release runners libdbus-free.
- **Integrity:** every artifact (both binaries) gets a SHA-256 checksum + a keyless GitHub
  build-provenance attestation (Actions OIDC). **Not** OS code-signed yet — first run shows the
  macOS "unidentified developer" / Windows SmartScreen prompt. See [SECURITY.md](SECURITY.md).
- **Toolchain ≥ 1.92** (the bar's MSRV; CLI/libs keep their 1.88 MSRV — a newer build toolchain
  does not change a published MSRV).

**Deferred:** macOS notarization, Windows Authenticode, Scoop, MSI. **SBOM generation + OS
code-signing are out of scope for this milestone** (tracked in [SECURITY.md](SECURITY.md)).

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
`CARGO_REGISTRY_TOKEN` set, from the **same commit** as the tag. The ladder is a topological order
of the actual `Cargo.toml` dep graph — a `#[test]` (`apps/cli/tests/publish_ladder_topo.rs`) parses
both this ladder and the graph and fails if they ever drift (`store` before `power` is arbitrary;
both before `costroid`; `server` after `core`+`store`; `bar` last):

```
costroid-focus → costroid-providers → costroid-core → costroid-config → costroid-connect → costroid-store → costroid-power → costroid → costroid-server → costroid-bar
```

```bash
cargo publish -p costroid-focus
cargo publish -p costroid-providers   # recent cargo waits for the index between each
cargo publish -p costroid-core
cargo publish -p costroid-config
cargo publish -p costroid-connect
cargo publish -p costroid-store
cargo publish -p costroid-power
cargo publish -p costroid
cargo publish -p costroid-server
cargo publish -p costroid-bar
```

Notes:
- Verified email required before the first publish; versions are permanent (yank-only).
- **PKG-3 — reserve the three NEW crate names early.** `costroid-power`, `costroid-store`, and
  `costroid-server` are not yet on crates.io. Before the first real release, **reserve each name**
  with a name-hold/placeholder publish (an empty-ish `0.0.0` or a `cargo owner`-style hold) so the
  ladder can't be blocked at release by a name being unavailable. crates.io is first-come — do this
  ahead of the tag, not during it.
- Bundled assets must live inside the crate (`costroid-core`'s pricing JSON; `costroid-bar`'s
  JetBrains Mono in `apps/bar/assets/`; `costroid-power`'s `profiles/`+`models/` JSON **and their
  `.sha256` sidecars**); cargo only packages files under the crate dir.
- Each crate ships the root `README.md` via `readme.workspace = true` (`costroid-bar` /
  `costroid-store` / `costroid-server` carry their own). **Pre-publish gate (PKG-1, runnable before
  anything is on crates.io):** `cargo package --workspace` then `cargo publish --dry-run --workspace`
  — the **workspace** forms, because a per-package `cargo publish --dry-run -p <crate>` cannot
  resolve unpublished siblings (and the published `costroid-focus 0.6.0` has drifted from local: M1
  added fields with no version bump). Use `cargo package -p <crate> --list` **only** to confirm each
  crate's bundled assets are present (e.g. `costroid-power`'s `profiles/`+`models/` JSON + `.sha256`).
- **PKG-2 — publish ONLY at the bumped workspace version.** The real per-package `cargo publish` above
  runs **in ladder order at the bumped version** — never at the current `0.6.0`. The **version bump
  is what re-trues the drifted ladder** (the published `costroid-focus 0.6.0` differs from local
  because M1 added fields with no bump), so a publish at `0.6.0` would fail/mismatch; bump first
  ([Cutting a release](#cutting-a-release) step 2), then publish.

## One-time maintainer setup

- **Local Linux build deps:** `sudo apt-get install -y libdbus-1-dev libsecret-1-dev` (the workspace
  build links `costroid-connect`'s keychain backend; the default `costroid` binary does not).
- **Repos:** `Costroid/costroid` + `Costroid/homebrew-tap` (initialized with a README, not empty).
- **Actions secrets:** `HOMEBREW_TAP_TOKEN` (cross-repo write to the tap), `NPM_TOKEN` (publish
  `costroid`), `CARGO_REGISTRY_TOKEN` (crates.io, verified-email account).
- **Actions permissions:** `contents: write`, `id-token: write`, `attestations: write`.
