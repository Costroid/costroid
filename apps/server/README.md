# costroid-server

The local HTTP API + web UI for [Costroid](https://github.com/Costroid/costroid) — a
**loopback-only** (`127.0.0.1`) server over the local cost ledger. It makes **no outbound
network call**: it binds loopback only and reads data through `costroid-core`.

> **M0 scaffold.** Today it binds loopback, answers `/healthz` + a placeholder index, and exits
> cleanly. The three real views — **timeline**, **comparison**, **break-even** — and the embedded
> static assets (Maud + htmx + uPlot) land at **M5**.

## Why a separate binary

`tiny_http` (and `axum`/`hyper`/`tokio`) are name-banned in the `costroid` CLI / `costroid-bar`
offline gate so those binaries stay byte-for-byte no-network. The server therefore lives in its
own binary with its **own reviewed per-binary allowlist** (`SERVER_ALLOWED` in
`apps/cli/tests/offline.rs`) and a **runtime loopback-only proof** (`scripts/offline_acceptance.sh`).
It is never linked into `costroid` or `costroid-bar`.

## Usage

```text
costroid-server [serve]       # bind 127.0.0.1:7878 and serve (default)
costroid-server --self-check  # prove loopback-bind + no egress, then exit
costroid-server --help
```

The server's guarantee is **loopback-bind, no egress** — a `127.0.0.1` listen creates an
`AF_INET` socket, so it is "no egress," not "zero sockets." The bind address is constructed only
from `127.0.0.1`, so the server cannot bind a routable interface by construction.
