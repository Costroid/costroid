**No figure (artifact_refused): the rows artifact held files besides the rows and the run object, so this report read none of it and the check fails.**

The `costroid-rows` artifact held 2 entries besides the rows and the run object: `notes.txt` and `extra/rows.jsonl`.

An approval does not change this check: find the step in the agent's job that wrote those entries, as [the troubleshooting page](https://docs.costroid.com/troubleshooting#installing-the-binary) says.
