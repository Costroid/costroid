# Costroid — Roadmap

Forward-looking only. For what shipped, see [CHANGELOG.md](../CHANGELOG.md); for the technical canon, [ARCHITECTURE.md](ARCHITECTURE.md); for the manual, [CLAUDE.md](../CLAUDE.md).

## 1. Status

**Feature-complete at v0.6.0** — the build plan (Steps 0–6: cost lane → Claude `statusLine` quota + generalized quota model → connections → analytical tabs + alerts → egui taskbar) is **DONE**. All 7 crates publish to crates.io.

## 2. Non-blocking fast-follows

- **Bar tabs** — the taskbar (`costroid-bar`) cockpit defers its **Trends / Models / History / Frontier** tabs; the core already computes each.
- **Tray field-verification** — the macOS/Windows tray paths compile but are **not** field-verified; verify them, then ship the GUI through **npm / Homebrew** (today the bar is binary archives + `cargo install costroid-bar` only).

## 3. Deferred — discovery-gated provider adapters

Three providers ship today: Claude Code, Codex (full), Cursor (detect-only — cost/quota `unavailable`). The rest are **never built speculatively** — each waits on a live-install discovery confirming a sanctioned source.

| Provider | ToS constraint | Unlock |
|---|---|---|
| **Cursor live quota** | Server-side only; no sanctioned per-user source (`/statusline` carries no quota field; Admin API is team/enterprise-only). **Never** reuse a local session against `api2.cursor.sh`. | A documented per-user API/OAuth, or a quota field added to its `/statusline`. |
| **GitHub Copilot** | AI Credits ($ pool + overage) since 2026-06. ToS-safe path = the user's **own classic PAT / `gh` OAuth** → the documented `…/billing/ai_credit/usage` endpoint; **user-billed only**. **Never** the internal `copilot_internal` endpoint. | A live-install check on a personal plan confirming a 200 + the exact JSON shape. |
| **Antigravity CLI** | $ lane: a Gemini key reads inference only — not usage/billing — so it is **not own-key-implementable**. Compute-effort quota has **no sanctioned source** → `unavailable`. | A documented Gemini usage/billing API (or OAuth/BigQuery export), and a quota payload in a Hook/status bar. |
| **Gemini (own-key)** | No sanctioned static-key usage API → `ApiVendor::Gemini` renders `unavailable`. | An OAuth- or BigQuery-export-class billing path (post-own-key "Gemini advanced" connector). |

## 4. The auth source ladder

Use the highest-safety source that exists; if the only path violates the terms, the datum is `unavailable` — never fetched. Only tiers 0–3 are ever built.

- **0 Local artifacts** — provider logs on disk (today's default).
- **1 Sanctioned push/hook** — Claude Code's `statusLine` `rate_limits` capture.
- **2 Sanctioned OAuth** — a provider's first-class OAuth (e.g. GitHub; deferred).
- **3 Your own API key** — Anthropic/OpenAI usage/billing APIs, your admin key.
- **4 Never** — reuse any credential, session, or cookie against an undocumented/internal endpoint (the ToS line).

## 5. Unbuilt

`costroid-mcp` is intentionally not built; the crates.io name is left unclaimed.
