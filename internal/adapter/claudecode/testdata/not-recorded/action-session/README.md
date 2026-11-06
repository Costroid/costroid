# Not a recording

No run left this file. It is the session file Claude Code writes on the runner during the recorded `claude-code-action` run in `2.1.316/execution-file/`, built from that run's own messages, because the runner's files were not kept.

Each line is one `claude-opus-5-5` assistant message of the recorded execution file, in the line shapes of `2.1.316/subagent/session.jsonl`, with a synthetic request id. Input, cache read and cache write counts are the recorded ones. The last request's 251 output tokens are the result's own figure for its last turn. The first three requests' output counts are synthetic: the stream holds placeholders, and these three sum to the remaining 365 of the result's 616. The 35 thinking tokens sit on the last request.

The file holds no line for `claude-haiku-5-5`. The recorded result counts 1219 input and 18 output tokens of that model, and no message on the recorded stream names it. That the runner's session file lacked it too is inferred from the warning every such run printed, not recorded.

Replace it with the runner's own session file when a run keeps one.
