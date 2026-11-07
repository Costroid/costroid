# Not recordings

No run of Claude Code wrote these files. They hold session-file shapes the reader must survive, built by hand from the recorded `2.1.316` and `2.1.319` files. Every content field holds a sentinel.

- `every-shape.jsonl`: lines of types the reader passes over, with `message` as an object, text, `null` and a number; lines with no type it can read; an `api-request` with no time; a response stated with integral floats and counts as text; a response with no timestamp; and a last line cut off inside a response, with no line break after it.
- `one-hour-cache.jsonl` and `one-hour-cache-stream.jsonl`: a run on an API key whose cache writes state no lifetime, as Claude Code reports them at times. With `ENABLE_PROMPT_CACHING_1H` set, those writes are one-hour writes. The recording workflow's `claude-1h-cache` scenario will replace this pair when it is recorded.
