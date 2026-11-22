# Read a command's exit code

Every `costroid` command ends in one of the ways below, and each way has one exit code. A script or a job can tell an answer that is a failure from a command line that is wrong, and both from work Costroid could not do. `costroid help <command>` prints the codes of one command.

| Code | How the command ended | For example |
| ---- | --------------------- | ----------- |
| 0 | It did its work, and a verdict is `success` or `neutral` | `costroid policy` on a run within its budget |
| 1 | It did its work, and the answer is a failure | A `failure` verdict of `costroid policy`, a finding of `costroid lint` or `costroid validate`, a miss of `costroid reconcile`, a failed check of `costroid doctor` |
| 2 | The command line is wrong | No command, a command or a subcommand that does not exist, a flag the command does not have, a flag without its value, a value of the wrong form, a required flag or argument left out, an argument too many |
| 124 | `costroid run` stopped the agent at the cap | A run whose cost reached `--budget-usd` |
| 125 | Costroid could not do its work | A file that is not there or cannot be read, a host or a service that did not answer, a directory that is no repository, a command that needs a CI job and has none |

A command that exits 2 or 125 says why on standard error. The first line opens with `costroid:` and says what is wrong. Where a line follows it, the last one says what to do next: it names a command, a file or a page of this documentation. [`troubleshooting.md`](troubleshooting.md) lists the lines.

## Ctrl-C is exit 130

A command that Ctrl-C ends exits 130, whatever it was doing, as a shell reports a process that SIGINT ended. `costroid run` stops the agent first and writes the rows of what it spent, and `costroid serve` finishes its requests and exits 0. [`output.md`](output.md#ctrl-c) says more.

## `costroid run` exits with the agent's own code

`costroid run` passes the agent's exit code through, so under `run` every code below 124 is the agent's: 0 when the agent finished, and whatever the agent chose when it stopped itself. Costroid's own codes there are 124 and 125. A wrong command line of `run` itself exits 125, where every other command exits 2, so that a caller never takes an agent's 2 for Costroid's.

## `costroid report --exit-on-verdict` has a code of its own

On GitLab CI, `costroid report --exit-on-verdict` ends with the verdict: 0 on `success`, 1 on `failure`, and 3 on `neutral` or when the report could not reach GitLab. The GitLab template allows the job to fail on 3 and on no other code, as [`gitlab.md`](gitlab.md) describes. Without the flag, and on GitHub Actions, `report` exits 0 once it finds a CI system, whatever the host answers.

## A service that refuses is exit 1 on four commands

`costroid export`, `costroid merged`, `costroid connect` and `costroid emit-otlp` exit 1 when Costroid Platform or the endpoint took the request and refused it, and the line names the refusal's code. They exit 125 when the service could not be reached.

## The codes of each command

| Command | `0` | `1` | `2` | `3` | the agent's own | `124` | `125` |
| --- | --- | --- | --- | --- | --- | --- | --- |
| `costroid demo` | yes |  | yes |  |  |  | yes |
| `costroid lint` | yes | a high finding, or a medium one under `--strict` | yes |  |  |  | yes |
| `costroid init` | yes | the recipe this release ships breaks a rule, a defect in Costroid | yes |  |  |  | yes |
| `costroid doctor` | yes | a check that fails | yes |  |  |  | yes |
| `costroid run` |  |  |  |  | yes | yes | yes |
| `costroid policy` | yes | a `failure` verdict | yes |  |  |  | yes |
| `costroid validate` | yes | a file that breaks the schema | yes |  |  |  | yes |
| `costroid version` | yes |  | yes |  |  |  | yes |
| `costroid report` | yes | a `failure` verdict, under `--exit-on-verdict` on GitLab CI | yes | a `neutral` verdict, or GitLab not reached, under `--exit-on-verdict` |  |  | yes |
| `costroid ingest-run` | yes |  | yes |  |  |  | yes |
| `costroid follow-up` | yes |  | yes |  |  |  | yes |
| `costroid approve` | yes |  | yes |  |  |  | yes |
| `costroid review` | yes |  | yes |  |  |  | yes |
| `costroid result` | yes | no `costroid` check is on the head commit yet | yes |  |  |  | yes |
| `costroid mcp` | yes |  | yes |  |  |  | yes |
| `costroid lint-recipes` | yes | a rejected line | yes |  |  |  | yes |
| `costroid export` | yes | the platform refused, the batch failed, or the format refused rows | yes |  |  |  | yes |
| `costroid merged` | yes | the platform refused a fact | yes |  |  |  | yes |
| `costroid connect` | yes | the platform refused | yes |  |  |  | yes |
| `costroid emit-otlp` | yes | the endpoint refused spans | yes |  |  |  | yes |
| `costroid reconcile` | yes | a token miss or a price miss | yes |  |  |  | yes |
| `costroid serve` | yes |  | yes |  |  |  | yes |
| `costroid keys` | yes |  | yes |  |  |  | yes |
| `costroid egress` | yes |  | yes |  |  |  | yes |
| `costroid gateway` | yes |  | yes |  |  |  | yes |
| `costroid app` | yes |  | yes |  |  |  | yes |
