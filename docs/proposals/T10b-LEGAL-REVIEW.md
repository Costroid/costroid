# Costroid connection flows — legal-review brief (gates the v0.4.0 release / T10b)

> **STATUS: MAINTAINER SELF-ATTESTATION — accepted 2026-06-16 (see §10).** This is the review
> package for the ⛔ gate that PRODUCT-PLAN §3 (line ~162) and §12.10 (T10b) require **before
> v0.4.0 ships**: a review of the connection flows confirming they (a) hit only provider
> endpoints the user authorized, (b) store nothing outside the OS keychain, and (c) induce no
> Terms-of-Service violation. The maintainer has reviewed the flows against this brief and the
> §9 checklist and accepted them for a v0.4.0 release (§10). **This is a maintainer
> risk-acceptance, not a legal opinion from counsel** — see §10 for the limits and when counsel
> is advised. Every factual claim below is cited to the code on disk (the code is canon); a
> reviewer can verify each against the named file. Prepared 2026-06-15 (T10c, commit `3a9ff06`);
> hardening + acceptance 2026-06-16.

---

## 0. The question this review must answer

Costroid is a local-first developer tool. Through v0.3.0 it made **zero network calls**. The
0.4.0 line adds an **opt-in, off-by-default** "connections" feature: a user may connect their
**own** provider usage/billing API key so Costroid can reconcile its local cost *estimate*
against the provider's *billed* invoice. This is the first time the product touches the network
or handles a credential, so it is the point at which legal/ToS liability grows — hence the gate.

**The three things to confirm:**

1. **Authorized endpoints only.** Every network call goes to a documented, first-party provider
   endpoint the user explicitly authorized by connecting — never an undocumented, internal, or
   non-sanctioned endpoint, and never by reusing a session/cookie not minted for this purpose.
2. **Keychain-only secrets.** The user's key is stored only in the OS keychain, never on disk,
   in config, in logs, in process arguments, or in environment variables, and is never routed
   through any Costroid server (there is none).
3. **No ToS violation.** Each provider is accessed only via a channel that provider sanctions
   for programmatic usage/billing reads with the user's own key; where no such channel exists,
   Costroid fetches nothing and shows "unavailable."

---

## 1. Scope — what ships in 0.4.0, and what is deliberately *not* built

**Built and shipping (all opt-in, behind the `connect` Cargo feature, off by default):**

- `costroid connect <anthropic|openai>` — store the user's own admin API key (keychain).
- `costroid disconnect <vendor>` — delete the local keychain copy (local removal; to revoke the
  key itself, revoke it in the provider's console — see §4).
- `costroid connections [--check]` — list what is linked; `--check` re-validates over the network.
- `costroid reconcile [--vendor …] [--period …]` — fetch the billed-cost report and compare it
  to the local estimate.

**Explicitly NOT built (and the review should note these as out of scope / never-do):**

- **No OAuth.** Tier-2 sanctioned OAuth (e.g. GitHub) is reserved in the design but has no code.
- **No Gemini fetch.** Google exposes no sanctioned static-API-key usage/billing read API;
  Gemini resolves to "unavailable" with **no network call** (`crates/costroid-connect/src/lib.rs`
  `gemini_cost_report` → `NoSanctionedStaticKeyApi`).
- **No Cursor fetch.** Cursor is not even a billing vendor in the code; its rows are excluded
  from reconciliation. It stays detect-only (the local-log lane).
- **No internal/undocumented endpoints, ever** (see §5).
- **No Costroid server / backend.** Credentials flow strictly device ↔ provider.
- **No token-side reconciliation today.** (Note: the per-token *usage* endpoints are implemented
  in the adapter code but have **no production caller** — see §8, item 2.)

---

## 2. The network surface — every call the product can make

There is exactly **one** outbound-socket call site in the entire workspace: `AuthorizedClient::get`
in `crates/costroid-connect/src/http.rs`. All fetches funnel through it. A grep confirms no other
crate references any HTTP/socket library. Every request is an HTTP **GET**.

**The only user actions that touch the network** are `connect`, `connections --check`, and
`reconcile`. `disconnect`, plain `connections`, and **every** default-build command (`now`,
`trends`, `frontier`, `statusline`, `setup-statusline`, `export`, `--live`) make zero network
calls. The four connect-related commands are compiled only under `--features connect`; the
**default shipped binary does not even contain them**.

| User action | Provider | Host | Path | Method | What it reads |
|---|---|---|---|---|---|
| `connect anthropic` (validate the key) | Anthropic | `api.anthropic.com` | `/v1/organizations/me` | GET | Org **identity only** — zero billing/usage/cost data |
| `connect openai` (validate the key) | OpenAI | `api.openai.com` | `/v1/organization/costs` | GET | A cost probe over a completed-day window |
| `connections --check` | both | as above | `/v1/organizations/me` · `/v1/organization/costs` | GET | Re-validation of each *already-connected* vendor |
| `reconcile` (Anthropic) | Anthropic | `api.anthropic.com` | `/v1/organizations/cost_report` | GET | Billed cost per day/model |
| `reconcile` (OpenAI) | OpenAI | `api.openai.com` | `/v1/organization/costs` | GET | Billed cost per day/model |
| `connect gemini` / `reconcile` (Gemini) | — | — | — | **none** | Resolves to "unavailable" with no fetch |

All of these are **documented, first-party platform-admin endpoints** that the provider publishes
for reading an organization's own usage/billing with an admin API key. The pinned endpoints,
their auth, and the per-provider ToS analysis are recorded in the ⛔-signed-off
`docs/proposals/T9-PIN-PROPOSAL.md` (signed off 2026-06-10).

---

## 3. Transport guarantees (how the client is constrained)

The HTTP client (`crates/costroid-connect/src/http.rs`, `AuthorizedClient`) is deliberately
minimal and locked down. Each property is enforced in code and asserted by tests:

- **HTTPS-only.** Production constructors hardcode the `https` scheme; any non-HTTPS URL is
  rejected. (A plain-HTTP path exists *only* in a test-gated loopback helper, never in a real build.)
- **GET-only.** The `AuthorizedClient` wrapper's public surface exposes only a `get` method — no
  POST/PUT/DELETE is reachable through it (the wrapped `ureq` agent is general-purpose; the
  constraint is the wrapper's, by design).
- **Bound to one authorized host, refused before any I/O.** The allowed host is fixed at
  construction; every request first calls `ensure_authorized(url)`, which parses the URL by
  string-splitting (no DNS, no socket) and returns an error if the host, scheme, or shape doesn't
  match — *before* the request is built or sent. Off-host requests cannot leave the process.
- **Redirects disabled.** Max redirects = 0; any 3xx is a typed error, never followed (so a
  provider can't bounce the client to another host).
- **Proxies disabled.** The agent explicitly ignores `HTTP(S)_PROXY` (no silent re-routing through
  an intermediary).
- **OS-native trust roots** via `rustls-native-certs` — the system trust store, never a
  compiled-in CA bundle; an empty trust store is a hard error, never a silently-untrusting client.
  **No certificate pinning:** trust is the OS root store, so a host carrying an attacker-planted or
  corporate-MITM root could intercept a request (which bears the org-wide admin key). This is the
  standard native-roots residual; connect only on a host whose trust store you control and use a
  dedicated, revocable key. SPKI/leaf pinning for the two hardcoded hosts is a possible later
  hardening (deferred — it adds a rotation/brittleness burden for two TLS-1.3 endpoints).
- **Timeouts and a response-size cap** (10s connect / 30s overall; 8 MiB default body cap, raised to
  16 MiB for the OpenAI adapter) — bounded, not open-ended.
- **Bounded retry, then degrade — never a tight loop.** Auth failures (401/403) are *not* retried;
  rate-limit/server/not-found (429/5xx/404) get up to 3 retries with capped exponential backoff
  (`Retry-After` honored, capped at 30s) and then degrade to a typed "unavailable" outcome. The
  client itself does not retry — it classifies each failure into a typed error and the caller's
  bounded policy decides — so it cannot hammer the endpoint.

---

## 4. Credential lifecycle (entry → storage → wire → revoke)

**Entry.** The key is read from **stdin only** (`apps/cli/src/main.rs`, `read_admin_key`):
- never from a command-line argument (would leak to `ps` / shell history) and never from an
  environment variable (would leak to child processes);
- on an interactive terminal, via a hidden, no-echo prompt; when piped (e.g.
  `echo "$KEY" | costroid connect anthropic`), one line read from stdin.

**Validation before storage.** The key's class is checked by prefix (`sk-ant-admin…` for
Anthropic, `sk-admin-…` for OpenAI) and rejected *before any network call* if it's the wrong
class. The key is then validated against the provider and stored **only on success**. The two
vendors validate differently: Anthropic reads `/v1/organizations/me` (org **identity only**, zero
billing); OpenAI has no identity endpoint, so its connect-time probe reads `/v1/organization/costs`
over a completed-day window — i.e. it reads the user's **own billed cost** for that day (the user's
own data, by their own key).

**Storage — OS keychain only.** Stored via the `keyring` crate against the OS keychain (macOS
Keychain, Windows Credential Manager, Linux Secret Service), under service `costroid`, account
`apikey:<vendor>`. In memory it is wrapped in a redacting `SecretString`. **Costroid writes the
secret to no Costroid-controlled file, config, or log** — only the OS keychain holds it; the store's
`Debug` is redacted. A unit test (against the in-memory keyring mock) asserts a store round-trip
leaves Costroid's own filesystem untouched. (The real OS keychain backend persists to its own
OS-managed, access-controlled store by design — that is the keychain doing its job, outside
Costroid's filesystem.)

**On disk — only a non-secret index.** The single disk artifact a connection flow writes is
`connections.json` (under `${XDG_STATE_HOME:-$HOME/.local/state}/costroid/`). It contains **only**
the set of connected vendor slugs and an optional non-secret org label (name + id) — **no key
material**. A test asserts the file is exactly `{"connected":["anthropic", …]}` (plus labels) and
contains no `sk-…`.

**On the wire.** The key travels only as an auth header value
(Anthropic: `x-api-key` + `anthropic-version: 2023-06-01`; OpenAI: `Authorization: Bearer`),
never in a URL, query string, or log line. The auth-header type's `Debug` renders `<redacted>`;
tests assert no `sk-…` ever appears in a composed request line.

**Local removal (not server-side revocation).** `costroid disconnect <vendor>` deletes the local
keychain copy and drops the vendor from `connections.json` (both idempotent, no network). This
removes the key from the machine immediately, but it does **not** revoke the key at the provider —
the key stays valid until the user revokes it in the provider's own console. The connect-time
warning (below) tells the user to do exactly that if the machine may be compromised.

**No backend.** There is no Costroid server. Nothing is proxied; the key never reaches any
Costroid-operated host.

---

## 5. Per-provider ToS posture & the authorization ladder

Costroid chooses each data source by descending an explicit "authorization ladder," most-sanctioned
first, and **only tiers 0–3 are ever built**:

- **Tier 0 — local artifacts** on disk (the default lane; no network, no credential).
- **Tier 1 — sanctioned push/hook** (Claude Code's `statusLine`; local, no token reuse).
- **Tier 2 — sanctioned OAuth** (first-party; deferred, not built).
- **Tier 3 — the user's own API key** against the provider's documented usage/billing API. **This
  is the only network tier built**, and it is the subject of this review.
- **Tier 4 — NEVER.** Costroid never reuses a credential, session, or token against a
  non-sanctioned, undocumented, or internal endpoint, and never reads browser cookies. Where that
  would be the only route to a datum, the datum stays "unavailable," never fetched.

**Concrete tier-4 lines Costroid does not cross** (verified absent from the code; documented in
`docs/proposals/T9-PIN-PROPOSAL.md`):

- OpenAI's legacy/internal `/v1/usage` and `/v1/dashboard/billing/usage` (browser-session surfaces)
  — never used; only the documented `/v1/organization/*` platform-admin endpoints are.
- GitHub Copilot's internal `api.github.com/.../copilot_internal/user` — never used (Copilot is a
  deferred, discovery-gated adapter with no code).
- Antigravity's internal `GetUserStatus` RPC — never used (deferred, no code).
- Cursor's undocumented `api2.cursor.sh` RPC / reusing a local Cursor session — never used; Cursor
  stays detect-only.
- Google AI Studio dashboards / replaying an AI Studio browser session — never used; Gemini stays
  "unavailable."

**Key-class / blast-radius note.** For both Anthropic and OpenAI, the only usage/billing credential
an individual developer can mint today is an **organization-level admin key** — there is no
read-only, billing-scoped variant for these platform-admin endpoints. Such a key can do more than
read costs (e.g. manage members/keys), so an intercepted or leaked key ≈ org exposure. Costroid's
mitigations: it wrong-class-checks the key before any network, validates it before storing
(Anthropic identity-only; OpenAI a cost probe — §4), stores it only in the OS keychain, never
echoes it, and supports local removal (revoke server-side in the provider console).
**Resolving the prior open question:** as of this build, `costroid connect <anthropic|openai>` now
prints a blast-radius warning **at paste time** — that an admin key is organization-wide, and to use
a dedicated, instantly-revocable key and revoke it in the provider console if the machine is ever
compromised — satisfying the T9 pin §2.3/§6 "warn at paste time" requirement (shown for
anthropic/openai, not gemini; routed through the ASCII-folding emit so `--plain` stays pure ASCII;
covered by a Layer-1 test). The standing "your credentials are your responsibility" framing (§6)
remains in place as well.

---

## 6. User-responsibility framing (already in the shipped docs)

The README, LICENSE notice, and SECURITY.md already state: Costroid uses only local and
provider-sanctioned data sources and never reuses a credential against a non-sanctioned endpoint;
**if the user connects their own API key, they remain responsible for their use of those
credentials and for complying with each provider's terms of service.** The review should confirm
this framing is sufficient and correctly placed (it appears at first-connect-relevant surfaces:
README "Connections", SECURITY.md "Authentication source ladder", and the LICENSE footer).

---

## 7. Candid caveats / known limitations (disclosed, not hidden)

A complete review should be aware of these (all acknowledged in code/docs, none contradicting the
guarantees above):

1. **Memory zeroization is "minimize, not eliminate."** The key is held in `SecretString` (zeroized
   on drop), but a transient plaintext copy can briefly persist un-zeroized in heap memory due to
   Rust `String → Box<str>` reallocation and the HTTP library's non-zeroizing header value. The
   code is candid about this; it is a defense-in-depth limit, not a disk/log/network leak. The
   load-bearing guarantee (no secret to disk/config/log/network/server) holds.
2. **Token-usage endpoints are implemented but dormant.** The adapter code defines the per-token
   *usage* endpoints (`/v1/organizations/usage_report/messages`, `/v1/organization/usage/completions`)
   with working methods, but **no shipped command calls them** — only the cost endpoints and the
   identity probe are reachable by a user action today. A reviewer auditing "what could the binary
   GET" should count those two; a reviewer auditing "what does a user action trigger today" gets
   only the three rows in §2. (They are the same authorized, documented platform-admin endpoints.)
   *Optional hardening (post-review):* gate the dormant methods behind a Cargo feature so a future
   caller must consciously enable them — a compile-time tripwire on the dormancy.
3. **Release binaries are provenance-attested + checksummed but not yet OS-code-signed** (no macOS
   notarization / Windows Authenticode yet) — a distribution-integrity note, orthogonal to the
   connection-flow ToS question, already disclosed in SECURITY.md/README.
4. **SECURITY.md has been trued (this build).** Its §40/§54 no longer claim "nothing calls the
   client / no build performs a network call"; they now describe the shipped connect/reconcile
   network surface, the netns fail-closed enforcement, and the no-certificate-pinning trust model.
   (The same stale claim in the `costroid-connect` crate doc was trued too.)

---

## 8. How every claim above is enforced / independently verifiable

The reviewer does not have to take the prose on faith — the guarantees are mechanically tested in CI:

- **strace offline-acceptance** (`scripts/offline_acceptance.sh`): the default build runs every
  command under `strace` and asserts **no outbound (AF_INET) socket**; a `--features connect`
  baseline asserts a normal run still makes no network call and writes no `$HOME` residue; a
  **network-namespace ("fail-closed") test** runs `connect` with a fake key under `unshare --net`
  and asserts it cannot reach the host and leaves no secret/file residue, and that `disconnect`
  makes no network call.
- **Two-tier forbidden-crates test** (`apps/cli/tests/offline.rs`): resolves the real dependency
  graph per shipped target and asserts the default build links **no** networking/TLS/keychain/
  telemetry crate (and not `costroid-connect` at all); under `--features connect`, only the
  sanctioned `ureq`/`rustls`/`keyring` trio is admitted, with async runtimes, OpenSSL, other HTTP
  clients, and all telemetry still forbidden.
- **Loopback integration tests** (gated on a test-only feature): drive the real `connect`/`reconcile`
  command core against a local mock server + mock keychain and assert the request hits exactly the
  authorized path, the secret lands only in the (mock) keychain, the only disk file is the
  non-secret `connections.json`, and no `sk-…` appears anywhere on disk or on the wire.
- **`cargo-deny`** (`deny.toml`): permissive licenses only (no GPL/AGPL/LGPL/SSPL); `openssl`/
  `native-tls` banned outright; the `ureq`/`rustls`/`keyring` trio admitted only via the
  `costroid-connect` chain; unknown registries/git sources denied.

---

## 9. Sign-off checklist

- [x] **Endpoints** — every reachable call targets a documented, first-party provider endpoint the
      user authorized by connecting their own key (§2); no internal/undocumented endpoint is used (§5).
- [x] **Credentials** — the key is keychain-only, stdin-entered, header-only on the wire, never on
      disk/config/log/argv/env, never through a Costroid server; `disconnect` is local removal, with
      server-side revocation in the provider console (§4).
- [x] **ToS** — accessing each provider's documented platform-admin usage/billing endpoint with the
      user's own admin key, read-only (GET), is consistent with that provider's terms; deferred
      providers (Gemini, Cursor, Copilot, Antigravity) are correctly left "unavailable" (§1, §5).
- [x] **Admin-key blast radius** — the connect-time org-wide warning + dedicated-revocable-key
      recommendation is now shown at paste time (§5) and reads acceptably.
- [x] **Transport trust** — the OS-trust-store / no-certificate-pinning model is accepted as
      disclosed (§3); SPKI pinning deferred to a possible later release.
- [x] **Disclosures** — the candid caveats (§7) are accepted for a 0.4.0 release.

---

## 10. Sign-off (maintainer self-attestation)

**Accepted by the maintainer on 2026-06-16** for the v0.4.0 release. The maintainer has reviewed
the connection flows against this brief and the §9 checklist and accepts them: the flows reach only
the user's-own-key, read-only, documented first-party platform-admin endpoints (§2); store secrets
only in the OS keychain (§4); cross no tier-4 line (§5); and the candid caveats (§7) and the
OS-trust-store / no-pinning model (§3) are accepted as disclosed. The connect-time org-wide
blast-radius warning (§5) and the SECURITY.md truing (§7 item 4) requested by the review have
landed.

**Nature and limits of this sign-off.** This is a **maintainer risk-acceptance for an open-source,
local-first tool that uses the user's own API key against documented first-party endpoints** — the
ordinary way a solo OSS project clears this internal gate. **It is not a legal opinion from
qualified counsel.** A professional review is advisable before: commercializing or charging for the
tool, distributing it at scale, a 1.0 milestone, or adding any new credential type, OAuth, or a new
provider/endpoint. Each user who connects their own key remains responsible for their own use of
that credential and for complying with the relevant provider's terms (§6). Re-review this attestation
if any connection flow, endpoint, auth method, or stored-secret shape changes.

With this attestation, the ⛔ gate on T10b is cleared. T10b then cuts v0.4.0: version bump
0.3.0→0.4.0, the SECURITY.md true (done this build), `dist` dry-run, commit-then-tag, and the
crates.io publish ladder.
