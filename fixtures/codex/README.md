# Codex Fixtures

These fixtures are synthetic but mirror the verified local Codex shape:

- `golden-gpt55-*.jsonl` — golden-cost rollouts pinning Codex API-spend math (cache-heavy and small cases) for `codex_golden_cost.rs`;
- session rollouts live under `~/.codex/sessions/YYYY/MM/DD/*.jsonl`;
- `state_threads.json` (a JSON stand-in for Codex's real on-disk `state_5.sqlite` thread
  table) holds rows that point at rollout paths and include model, provider, cwd,
  timestamps, and token totals;
- rollout JSONL entries use `timestamp`, `type`, and `payload`;
- token usage appears under `payload.info.last_token_usage`;
- quota windows appear under `payload.rate_limits.primary` and
  `payload.rate_limits.secondary`.
