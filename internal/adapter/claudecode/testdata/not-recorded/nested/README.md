# Not a recording

No run made this file. It is the session file a second `claude -p` leaves when a hook or a plugin starts one inside a run: one user line, one request and its response, in the line shapes of the recorded `2.1.316/subagent/session.jsonl`, plus the `cwd` field that the recordings cut because it holds a path.

Tests fill four placeholders: `@session` is the session id, `@request` the request id, `@start` and `@end` the first and last timestamps. The working directory reads `/work`. The request is 1000 input and 200 output tokens of `claude-haiku-5-5`, 0.0002 USD at the pinned catalog's 0.10 and 0.50 USD per million.

Replace it with a recorded hook run when one exists.
