<!--
SPDX-License-Identifier: Apache-2.0
Copyright 2026 The Costroid Authors
-->

# Securing your deployment

Costroid serves a cost dashboard and a JSON API over plain HTTP. This page
explains how to run it safely. Two rules do the heavy lifting:

1. **It binds to loopback by default** — nothing off your machine can reach it
   until you say so.
2. **It refuses to start without an authentication decision** — you pick a mode,
   or you explicitly opt out.

Costroid terminates no TLS and handles cost & usage **metadata only** — it never
stores prompt or response content (the Cardinal Rule).

## The default: loopback only

`costroid serve` listens on `127.0.0.1:8080` by default. That address is only
reachable from the same host, so a fresh install is not exposed to your network.

To expose it, set the listen address explicitly — that explicit choice **is** the
opt-in to a public bind:

```
costroid serve --addr 0.0.0.0:8080 ...      # or COSTROID_ADDR=0.0.0.0:8080
```

Precedence is `--addr` > `$COSTROID_ADDR` > the loopback default.

## Authentication: pick exactly one mode

`serve` will not start unless authentication is configured. Configure **exactly
one** of the following (configuring two, or combining a mode with `--no-auth`, is
a hard error).

Only requests to the data API under `/api/` are authenticated. `/healthz` (for
health probes) and the static dashboard shell (`/`, its assets) are always
reachable without credentials — they carry no billing data. In a forward-auth
deployment, gate the dashboard shell at your reverse proxy.

### 1. Bearer token

Every API request must send `Authorization: Bearer <token>` (the scheme is
case-insensitive). Provide the token from a **file** (preferred) or, more weakly,
an environment value — never on the command line (argv is world-readable via
`/proc/<pid>/cmdline` and `ps`, so there is no `--auth-token` value flag).

```
# Preferred: a file (matches Docker secrets / systemd LoadCredential=)
costroid serve --auth-token-file /run/secrets/costroid-token
#   or:  COSTROID_AUTH_TOKEN_FILE=/run/secrets/costroid-token costroid serve

# Weaker: a direct environment value. It leaks to child processes,
# `docker inspect`, and core dumps (CWE-214). Prefer the file source.
COSTROID_AUTH_TOKEN='...' costroid serve
```

Precedence is `--auth-token-file` > `$COSTROID_AUTH_TOKEN_FILE` >
`$COSTROID_AUTH_TOKEN`. Exactly one trailing newline is trimmed from a token file
(so `printf %s` and a plain `echo >` both work); an empty token, or an explicitly
named file that cannot be read, is a startup error. The token is compared in
constant time and is never written to the access log.

A request:

```
curl -H "Authorization: Bearer $TOKEN" http://127.0.0.1:8080/api/v1/costs/daily
```

### 2. Trusted-header / forward-auth

Run Costroid **behind a trusted reverse proxy on a trusted network** and let the
proxy authenticate. The proxy sets an identity header on each proxied request;
Costroid honors it **only when the request's real TCP peer is a trusted proxy**.

```
costroid serve \
  --auth-trusted-header X-WEBAUTH-USER \
  --auth-trusted-proxies 127.0.0.0/8,::1/128
```

- `--auth-trusted-header` (or `$COSTROID_AUTH_TRUSTED_HEADER`) names the identity
  header. Its presence enables the mode; the empty default keeps it off.
  **`X-WEBAUTH-USER` is the recommended value** (the shared default of Grafana and
  Gitea/Forgejo). `Remote-User` (Authelia) and `X-Forwarded-User` (oauth2-proxy)
  are common alternatives.
- `--auth-trusted-proxies` (or `$COSTROID_AUTH_TRUSTED_PROXIES`) is the allowlist
  of proxy peer CIDRs, defaulting to loopback (`127.0.0.0/8,::1/128`) for a proxy
  on the same host. List your proxy's real address(es).

**The guardrail (do not weaken it).** The identity header is trusted **only** if
the real TCP peer (`RemoteAddr`) is inside `--auth-trusted-proxies`. A request
from any other peer has its identity header **ignored** and is rejected with
`401`. Costroid never derives the trust decision from a client-supplied header
such as `X-Forwarded-For`. Setting `--auth-trusted-proxies` to an all-addresses
range (`0.0.0.0/0` or `::/0`) is **refused at startup** — trusting every client
would let anyone send the identity header and impersonate any user (this is the
class of Gitea CVE-2026-20896, where a shipped `TRUSTED_PROXIES=*` allowed
`X-WEBAUTH-USER: admin` from any client). As defense-in-depth, Costroid strips
the trusted header before the request reaches its handlers.

Your proxy must therefore (a) authenticate the user, (b) set the identity header
itself, and (c) **strip any client-supplied copy** of that header before
proxying. Sketch (oauth2-proxy / Authelia in front of Costroid):

```
# nginx: authenticate via a subrequest, then forward the identity as X-WEBAUTH-USER
location / {
    auth_request /oauth2/auth;                      # oauth2-proxy or Authelia
    auth_request_set $user $upstream_http_x_auth_request_user;
    proxy_set_header X-WEBAUTH-USER $user;           # the header Costroid trusts
    proxy_pass http://127.0.0.1:8080;                # Costroid on loopback
}
```

Keep Costroid bound to loopback (or a private interface the proxy alone can
reach) so no client can bypass the proxy and hit Costroid directly.

### 3. No authentication (explicit opt-out)

`--no-auth` is the only way to serve the API unauthenticated. It prints a loud
warning at startup, escalated when the bind is not loopback:

```
costroid serve --no-auth                    # loopback, for local single-user use
```

Anyone who can reach the address can then read all billing data. Use it only on a
loopback bind for local, single-user use — never on a network-exposed address.

## Scheduled ingestion process posture

`costroid serve --sync` changes the long-running serve process from a store
reader into both a store reader and a connector runner. It reads the encrypted
credential vault, so the D32 credential key file must be readable by the serve
user. Keep that key file outside the data directory, permission it narrowly,
and never put key material in `sources.json`.

Scheduled AWS and Azure connectors use their ambient SDK credential chains.
Those identities must be present in the serve process environment. Short-lived
or SSO credentials may expire while serve is running, so monitor
`GET /api/v1/sync/status` and renew the ambient session when a run fails.
Scheduled connectors also make outbound requests from the serve process; apply
the same egress restrictions and least-privilege read-only permissions used for
manual ingest.

Each scheduled AI run calls the vendor Admin APIs again, so a shorter interval
multiplies Admin-key API traffic. Anthropic's Admin key cannot be scoped below
full organization admin. Prefer generous intervals, protect the D32 key file,
and use the status endpoint instead of increasing frequency merely to check
whether a source is healthy. Scheduler logs contain source metadata and
connector errors, never credential material or AI prompt or response content.

## TLS

Costroid terminates **no TLS itself**. For any non-loopback deployment, put it
behind a reverse proxy (nginx, Caddy, Traefik, …) that terminates TLS and
forwards to Costroid over loopback. In forward-auth mode that same proxy is the
trusted proxy.

## Access log

`serve` writes one structured JSON line per request to stderr:
`method`, `path` (the path only — never the query string or any header), `status`,
`duration_ms`, `remote` (peer IP), `authn` (`ok`/`denied`/`disabled`/`exempt`),
`auth_mode`, and, for forward-auth, the authenticated `user`. It is an
operational log, not a compliance audit trail, and it never contains a token, the
`Authorization` header, or request bodies.
