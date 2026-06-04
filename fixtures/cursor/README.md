# Cursor Fixtures

Cursor is **detect-and-defer** in Phase 1. The Cursor CLI keeps **no** token usage,
cost, or quota on disk — those are live server RPCs (`api2.cursor.sh`), surfaced in
Phase 2. The only local signals Costroid reads are *presence* and the *selected
model + logged-in flag*, both from `cli-config.json`. Costroid **never** reads chat
content (`chats/*/store.db`), the code-tracking DB, or the auth token (`auth.json`).

These fixtures are synthetic and path-injected (never real user data):

- `home/.cursor/cli-config.json` — the real config schema with **placeholder** PII
  (`user@example.com`, fictional ids). `selectedModel.modelId` / `model.modelId` /
  `model.displayName` give the model; the presence of an `authInfo` object is the
  logged-in signal (its contents are never surfaced).
- `home/.cursor/auth.json` — a placeholder secret that discovery must **never** open.
- `home/.cursor/chats/dummy-session/store.db` — plain text, not SQLite; present only
  to assert discovery never enumerates the chat stores.
- `garbled/.cursor/cli-config.json` — invalid JSON: the install is still "present",
  but the model degrades to unknown (never guessed), never an error or panic.

Missing quota/usage is reported as `unavailable — live (Phase 2)`, not guessed.
