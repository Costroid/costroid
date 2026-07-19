---
title: Getting started
description: Verify a release, run Costroid, ingest an export, or build from source.
---

<!-- SPDX-License-Identifier: Apache-2.0 -->
<!-- Copyright 2026 The Costroid Authors -->

## Download Costroid

Download the archive for your platform and the accompanying verification files from [GitHub Releases](https://github.com/Costroid/costroid/releases). After unpacking the archive, make `costroid` executable and place it on your `PATH`, or invoke it as `./costroid`.

```sh
chmod +x costroid
```

## Install with the script

On Linux or macOS, download and verify a release in one command:

```sh
curl -fsSL https://raw.githubusercontent.com/Costroid/costroid/main/scripts/install.sh | sh
```

The script detects your operating system and architecture, downloads the matching release archive, verifies its SHA-256 against the published `checksums.txt` (and the cosign signature when [cosign](https://github.com/sigstore/cosign) is installed) before extracting, and installs `costroid` to `~/.local/bin`. Two environment variables adjust it: set `COSTROID_VERSION` to pin a release such as `v0.1.0` (the default is the latest), and `COSTROID_INSTALL_DIR` to change the install directory (the default is `~/.local/bin`).

The script is fetched over HTTPS from this repository, so its trust is transport trust; the script itself is not signature-verified, only the release archive it downloads is checksum-verified and cosign-verified. If you would rather inspect it first, read [`scripts/install.sh`](https://github.com/Costroid/costroid/blob/main/scripts/install.sh), or use the manual download and verification steps below instead.

Windows users should download the archive from [GitHub Releases](https://github.com/Costroid/costroid/releases) and verify it manually with the [Verify the release](#verify-the-release) steps below.

## Verify the release

Release archives, the CycloneDX source SBOM, checksums, signature bundle, and build-provenance attestations are published together. From the directory containing those files, run:

```sh
cosign verify-blob \
  --bundle checksums.txt.sigstore.json \
  --certificate-identity-regexp 'https://github.com/Costroid/costroid/.github/workflows/release.yml@.*' \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com \
  checksums.txt
sha256sum --check checksums.txt
gh attestation verify <artifact> --repo Costroid/costroid
```

`gh attestation verify` requires GitHub CLI 2.49.0 or newer. The first two commands still verify the signed checksums and artifact digests when that subcommand is unavailable, but they do not replace provenance verification.

## Run the demo

Start an isolated dashboard populated with synthetic data:

```sh
costroid demo
```

Open [http://localhost:8080](http://localhost:8080). Demo mode is read-only and does not read your normal data directory, credential store, or connectors.

## Serve your own data

For local, single-user use, start Costroid on its default loopback address with authentication explicitly disabled:

```sh
costroid serve --no-auth
```

Open [http://localhost:8080](http://localhost:8080). When `COSTROID_ADDR` is unset, the default is `127.0.0.1:8080`.

:::caution[Do not expose an unauthenticated server]
Use `--no-auth` only with a loopback bind for local, single-user access. Both `:8080` and `0.0.0.0:8080` listen on all interfaces. Before using either public bind, configure one of the authentication modes and a TLS-terminating reverse proxy described in [Security & deployment](/security/).
:::

## Run with a container

Each release publishes prebuilt multi-architecture images (`linux/amd64` and `linux/arm64`) to the GitHub Container Registry at `ghcr.io/costroid/costroid`. The default command runs the demo, so this needs no data or configuration:

```sh
docker run --rm -p 8080:8080 ghcr.io/costroid/costroid:latest
```

Open `http://localhost:8080`. The demo is synthetic and read-only; its store is written to an ephemeral directory inside the container and removed on exit, so no volume is needed.

To serve your own data instead, mount a volume for the store, pass an auth token, and run `serve`:

```sh
printf '%s' "$COSTROID_TOKEN" > token
docker run --rm -p 8080:8080 \
  -v costroid-data:/data \
  -v "$PWD/token:/run/secrets/costroid-token:ro" \
  -e COSTROID_AUTH_TOKEN_FILE=/run/secrets/costroid-token \
  ghcr.io/costroid/costroid:latest serve
```

The image binds `0.0.0.0:8080` inside the container (via `COSTROID_ADDR`) and `serve` fails closed if no authentication is configured. For Kubernetes manifests and image verification, see the [Operations guide](/guides/operations/#running-in-a-container); for a network-exposed deployment behind a reverse proxy, see [Security & deployment](/security/).

## Ingest your first export

:::caution[Manual ingest and the scheduled alternative]
The embedded store allows a single process at a time. Stop `costroid serve`
before running a manual `costroid ingest` or `costroid metrics import`, then
restart it after the command finishes. For unattended refreshes without
stopping the dashboard, configure sources and run `costroid serve --sync`; the
in-process scheduler shares serve's open store. See
[Scheduled ingestion](/guides/operations/#scheduled-ingestion).
:::

For a local AWS FOCUS export:

```sh
costroid ingest --connector aws-focus --path <your-focus-export.csv.gz>
```

For a generic FOCUS or CSV export, declare its FOCUS version explicitly:

```sh
costroid ingest --connector focus-csv --path <export.csv> --focus-version 1.2
```

Run `costroid ingest -h` for every connector and flag. Provider credentials can be stored through stdin in the encrypted credential store:

```sh
costroid credentials init
costroid credentials set <slot>
```

## Build from source

Install Go, Node.js LTS, pnpm, and DuckDB, then build the dashboard and single binary:

```sh
git clone https://github.com/Costroid/costroid.git
cd costroid
pnpm install
make build
```

The binary is written to `bin/costroid`:

```sh
./bin/costroid demo
# or, for loopback-only local use
./bin/costroid serve --no-auth
```
