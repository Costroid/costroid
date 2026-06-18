# Releasing Costroid

This is the runbook for cutting a Costroid release. The release **infrastructure** was
configured and dry-run-verified for v0.1.0 (the release-infrastructure milestone, PR #1) and has
cut every release since (latest tag: v0.4.0); the **actual** publish is a deliberate,
human-triggered action and is intentionally not automated to run on a normal push.

> **Nothing in this repo publishes by itself.** The release workflow
> ([.github/workflows/release.yml](.github/workflows/release.yml)) runs only on a pushed
> version tag (`v*`); on pull requests it runs `dist plan` and nothing else. No `cargo publish`,
> `npm publish`, tag, or GitHub Release happens until a maintainer performs the steps below.

---

## What a release ships

Every release (v0.1.0 onward) ships the `costroid` CLI binary for six targets, with four installers,
via [cargo-dist](https://github.com/axodotdev/cargo-dist) (binary `dist`, configured in
[dist-workspace.toml](dist-workspace.toml) + each app's `[package.metadata.dist]`):

- **`costroid` (the CLI) — targets:** `x86_64`/`aarch64` Linux (gnu) · `x86_64` Linux (musl, fully
  static) · `x86_64`/`aarch64` macOS · `x86_64` Windows.
- **`costroid` — installers:** shell (`curl | sh`), PowerShell (`irm | iex`), Homebrew (tap), npm,
  plus crates.io — `cargo install costroid` (from source) and `cargo binstall costroid` (prebuilt,
  pulled off the GitHub Release; resolves via the crates.io index).
- **`costroid-bar` (the egui taskbar, since v0.6.0):** downloadable binary **archives** for 5 targets
  (gnu x86_64/aarch64, macOS x86_64/aarch64, Windows x86_64 — **no musl**, the GTK3 tray cannot
  static-link) + crates.io (`cargo install costroid-bar`). **No npm/Homebrew/script installers** —
  those stay CLI-only until the macOS/Windows tray matrix is field-verified (per
  `docs/PRODUCT-PLAN.md` §12.29). Its Linux build runners install the GTK3/xdo/AppIndicator dev
  headers (auto, from `[package.metadata.dist.dependencies.apt]`).
- **Integrity & provenance:** every artifact (both binaries) gets a SHA-256 checksum and a keyless
  GitHub build-provenance attestation (Actions OIDC — no certificates, no secrets).

**Not in any release yet** (deferred): macOS notarization / Windows Authenticode code-signing, Scoop, MSI,
the `costroid-mcp` crate (deferred/speculative — see `docs/PRODUCT-PLAN.md`,
which governs scope and build sequencing).

**Joining the publish order at their roadmap steps:** `costroid-connect` is now a workspace member
(skeleton landed in T7), built only behind `apps/cli`'s off-by-default `connect` feature. T8 gave it
its first behavior — the OS-keychain credential store (`CredentialStore` / `ConnectionRegistry` /
`ApiVendor`) — and its first deps (`keyring` + `secrecy` + serde/serde_json/thiserror); the T9a HTTP
client brought in `ureq` + `rustls` (+ `rustls-native-certs`). **T9b (the per-provider usage-API
adapters) gave it its first internal dependency, `costroid-core`** (the adapters parse into
`costroid-core::vendor_report`; it needs **no** `costroid-focus`), so it **publishes after
`costroid-core`** in the ladder — before `costroid` (the CLI), which depends on it via the `connect`
feature. It was first published in the v0.4.0 cut (T10b). **Since v0.6.0 (Step 6) the workspace has a
SEVENTH crate, `costroid-bar`** (the egui taskbar; binary `costroid-bar`; depends on `costroid-core`
+ `costroid-config`, and on `costroid-connect` only under its own off-by-default `connect` feature).
It also publishes to crates.io (`cargo install costroid-bar`), **last** in the ladder, and a new
shared **`costroid-config`** crate (the read-only `[budget]`/`[alerts]` schema both apps consume,
extracted in T20) publishes after `costroid-core`. See `docs/PRODUCT-PLAN.md` for the sequencing.

**`costroid-bar` ships differently from the CLI (v0.6.0, T21).** It produces downloadable binary
**archives** (5 dynamically-linked targets — gnu x86_64/aarch64, macOS x86_64/aarch64, Windows
x86_64; **no musl**, since the GTK3 tray stack cannot static-link) + crates.io, but **no
npm/Homebrew/script installers** — those stay CLI-only (`costroid`) until the macOS/Windows tray
matrix is field-verified (`docs/PRODUCT-PLAN.md` §12.29). This is configured in
`apps/bar/Cargo.toml`'s `[package.metadata.dist]` (`installers = []`, the target subset, and the
Linux GTK3/xdo/AppIndicator `apt` system deps for the build runners). The release **toolchain must be
≥ 1.92** (the bar's MSRV; the CLI/library crates keep their 1.88 MSRV promise — a newer build
toolchain does not change a published MSRV).

---

## Prerequisites the maintainer sets up once

These are **not** done by this repo's automation — a human with org access must do them before
the first real release, or release-time jobs will fail.

0. **Local build deps (Linux, since T8):** the workspace build links `costroid-connect`'s Linux
   keychain backend (keyring's sync Secret Service → C libdbus), so a local `cargo build/test
   --workspace` or `dist build` needs the C dev libs: `sudo apt-get install -y libdbus-1-dev
   libsecret-1-dev` (CI installs these in the pre-pr and offline-acceptance jobs). The default
   `costroid` binary never links the keychain, but the full workspace build does.

1. **GitHub repos under the `Costroid` org:**
   - `Costroid/costroid` (this repo).
   - `Costroid/homebrew-tap` — a repo **initialized with a README (not completely empty)**, into
     which cargo-dist pushes the generated formula. A *completely* empty repo breaks the
     `publish-homebrew-formula` checkout.
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

1. Make sure `main` is green: the full gate (`fmt` / `clippy -D warnings` / `test`, including the
   `--features connect` build + clippy that links the keychain crate), the MSRV check, the FOCUS
   conformance job, the license (`cargo deny`, run `--all-features` for the connect-on pass) job,
   the online advisories job, and the offline-acceptance job all pass.
2. Confirm the version. The workspace is versioned in lockstep via
   [Cargo.toml](Cargo.toml) `[workspace.package].version`. Bump it to the new `X.Y.Z` (and
   refresh `Cargo.lock` — see the mechanics below) in a committed change before tagging.
3. Sanity-check the plan locally (no publish):
   ```bash
   dist plan                       # prints the artifacts/installers that will be built
   dist build --artifacts=local    # builds the host artifacts + checksums into target/distrib/
   ```
4. **Tag and push** — this is the trigger that starts the real release:
   ```bash
   git tag vX.Y.Z
   git push origin vX.Y.Z
   ```
   The `Release` workflow then: builds every target, attests + checksums each artifact, creates
   the GitHub Release with all assets, pushes the Homebrew formula to the tap, and publishes the
   npm package. The shell/PowerShell/cargo-binstall installers resolve from the GitHub Release.

To release a fix, bump the version and push the new `vX.Y.Z` tag.

### Release mechanics to know

- **The tag version must match the manifest exactly.** cargo-dist requires the pushed `vX.Y.Z` tag
  to equal `[workspace.package].version` in [Cargo.toml](Cargo.toml). To rehearse the pipeline with
  a release candidate, bump the version on a throwaway branch and tag `vX.Y.Z-rc.N` from it. The
  Homebrew and npm publish jobs are **skipped on prereleases**, so an rc exercises build + attest +
  checksum + GitHub Release only — not the publish legs.
- **A version bump must also refresh `Cargo.lock`** (the workspace crates' entries change) — commit
  both `Cargo.toml` and `Cargo.lock`.
- **Tag and `cargo publish` from the same commit.** Cut the GitHub Release tag and publish to
  crates.io (below) from the *same* commit, so the release binary and the crates.io source agree.

---

## crates.io publish

Costroid publishes to crates.io as **seven** crates since v0.6.0 — `costroid-focus`,
`costroid-providers`, `costroid-core`, `costroid-config`, `costroid-connect`, `costroid`, and
`costroid-bar` — so `cargo install costroid` / `cargo binstall costroid` (the CLI) and
`cargo install costroid-bar` (the taskbar) all work. (`costroid-config` joined in v0.6.0/T20 — the
shared read-only `[budget]`/`[alerts]` schema both apps consume; it depends only on `costroid-core`,
so it publishes after it. `costroid-bar` joined in v0.6.0/T18 — the egui taskbar; it depends on
`costroid-core` + `costroid-config` (and on `costroid-connect` only under its off-by-default
`connect` feature), so it publishes **last**.)

Publish in **dependency order** (each crate must be on crates.io before its dependents),
with `CARGO_REGISTRY_TOKEN` configured:

```
costroid-focus → costroid-providers → costroid-core → costroid-config → costroid-connect → costroid (cli) → costroid-bar
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

> The order grows as members gain publishable behavior (per `docs/PRODUCT-PLAN.md`).
> `costroid-bar` is published with its bundled JetBrains Mono asset (`apps/bar/assets/`, inside the
> crate dir — verify with `cargo package -p costroid-bar --list | grep -i jetbrains`); it carries its
> own `README.md` (not the workspace root), so it shows that on crates.io.

Gotchas (learned shipping v0.1.0):
- **A verified email** on the crates.io account is required before the first publish.
- **`dist build` builds the whole workspace — keep `precise-builds = true` (learned shipping v0.4.0).**
  Even though the shipped `costroid` binary is connect-OFF (no `costroid-connect`/`keyring`/`libdbus`),
  a default `dist build` compiles every workspace member, so it tried to build `costroid-connect →
  keyring → libdbus-sys` and **failed on the CI Linux runners** (`Package dbus-1 was not found` — the
  runners have no `libdbus-1-dev`). A *local* `dist build` dry-run does **not** catch this (a dev box
  has libdbus). `precise-builds = true` in `dist-workspace.toml` makes dist build only `-p costroid`
  (connect-OFF), so the runners need no system libs. Do not remove it. (If a connect-ON artifact is
  ever shipped, it would instead need `[dist.dependencies]` apt = `libdbus-1-dev`, `libsecret-1-dev`.)
- **`dist build --artifacts=local` on a single host can't cross-compile the other-OS targets (learned shipping v0.5.0).**
  Run bare on a Linux dev box it errors `Cross-compilation from x86_64-unknown-linux-gnu to
  aarch64-apple-darwin is not supported` — `--artifacts=local` tries every target the config lists. This
  is **harmless** (CI builds each target on its own native runner). For a real local sanity build, pin the
  host target: `dist build --artifacts=local --target x86_64-unknown-linux-gnu`. The macOS/Windows
  archives are only ever produced in CI.
- **Every crate needs a `readme` to show one on crates.io (learned shipping v0.5.0).** crates.io only
  auto-detects a README in the crate's OWN dir; with a workspace-root README and no `readme` field, every
  crate published with **no README**. Fixed via `readme = "README.md"` in `[workspace.package]` +
  `readme.workspace = true` on each crate — cargo then packages the root README into every crate (verify
  with `cargo package -p <crate> --list | grep README.md`). cargo-dist also bundles that README into the
  per-target archives and the npm package.
- **Bundled assets must live inside the crate.** `costroid-core` `include_str!`s its pricing JSON;
  it lives at `crates/costroid-core/pricing/pricing.v1.json` (not the workspace root) — cargo only
  packages files under the crate dir, so a standalone verify build fails otherwise. Keep any new
  bundled data inside its crate.
- **Validate before publishing:** `cargo package -p <crate> --list` (any crate), and
  `cargo publish --dry-run -p <crate>` (full verify — works once the crate's siblings are already
  on crates.io).
- Versions are permanent (yank-only) — publish deliberately, in order.

**`costroid-mcp`** does not exist yet (deferred/speculative — see
`docs/PRODUCT-PLAN.md`, which governs scope and sequencing). Its crates.io name
is intentionally left unclaimed; we do not publish a placeholder.

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
