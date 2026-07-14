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

## Ingest your first export

:::caution[Stop the server before ingesting]
The embedded store allows a single process at a time. Stop `costroid serve` before running `costroid ingest` or `costroid metrics import`. Restart the server after the command finishes.
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
