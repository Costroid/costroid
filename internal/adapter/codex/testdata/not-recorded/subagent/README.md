# Not a recording

No run made this file. It is the rollout file of a subagent thread that the recorded `exec` run's thread starts, in the line shapes of `0.164.0-alpha.11.1/exec/rollout.jsonl`.

Its `session_meta` carries `thread_source` `subagent` and a `session_id` naming the parent thread, as the rollout files Codex 0.147.0-alpha.6.5 left on one machine did on 2026-11-04. No 0.164.0-alpha.11.1 recording holds a subagent, so those two fields are unverified at this version. Tests replace `@parent` with the parent thread's id.

The subagent makes one `gpt-5.6-luna` request: 12000 input tokens, 8192 of them cached, and 300 output tokens, 64 of them reasoning. At the pinned catalog's 0.20, 0.02 and 1.20 USD per million that is 0.00128544 USD.

Replace it with a recorded subagent run when one exists.
