package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/Costroid/costroid/internal/reconcile"
	"github.com/Costroid/costroid/internal/server"
)

// command is one entry of the dispatch table. A new command is a file with its
// function and help, and one line here.
type command struct {
	name string
	// short is the command's line in the short usage. A command without one is
	// named under other commands.
	short string
	// ledger names the command under ledger commands in the short usage: it
	// runs or feeds a ledger of one's own.
	ledger bool
	// help is what costroid help <name> prints.
	help string
	run  runner
}

type runner func(ctx context.Context, args []string, std stdio) int

type stdio struct {
	stdin          io.Reader
	stdout, stderr io.Writer
}

var commands = []command{
	{name: "run", short: "costroid run --budget-usd N -- <agent command>", help: runHelp, run: runCommand},
	{name: "ingest-run", help: ingestRunHelp, run: func(_ context.Context, args []string, std stdio) int { return ingestRunCommand(args, std.stderr) }},
	{name: "report", short: "costroid report --rows rows.jsonl", help: reportHelp, run: withOutputs(reportCommand)},
	{name: "demo", short: "costroid demo", help: demoHelp, run: withOutputs(demoCommand)},
	{name: "init", help: initHelp, run: withoutContext(initCommand)},
	{name: "doctor", help: doctorHelp, run: withOutputs(doctorCommand)},
	{name: "follow-up", help: followUpHelp, run: withOutputs(followUpCommand)},
	{name: "approve", help: approveHelp, run: withOutputs(approveCommand)},
	{name: "reconcile", help: reconcileHelp, run: reconcileWithProvider},
	{name: "export", help: exportHelp, run: exportCommand},
	{name: "connect", help: connectHelp, run: connectCommand},
	{name: "review", help: reviewHelp, run: withOutputs(reviewCommand)},
	{name: "policy", help: policyHelp, run: withOutputs(policyCommand)},
	{name: "validate", short: "costroid validate [costroid.yml]", help: validateHelp, run: withoutContext(validateCommand)},
	{name: "lint", short: "costroid lint [--strict] [paths]", help: lintHelp, run: withoutContext(lintCommand)},
	{name: "lint-recipes", help: lintRecipesHelp, run: withoutContext(lintRecipes)},
	{name: "gateway", ledger: true, help: gatewayUsage, run: withOutputs(gatewayCommand)},
	{name: "serve", ledger: true, help: server.ServeUsage, run: func(ctx context.Context, args []string, std stdio) int { return server.Main(ctx, args, std.stderr) }},
	{name: "keys", ledger: true, help: server.KeysUsage, run: withOutputs(server.KeysMain)},
	{name: "egress", ledger: true, help: server.EgressUsage, run: withOutputs(server.EgressMain)},
	{name: "version", help: versionHelp, run: versionCommand},
	{name: "result", help: resultHelp, run: withOutputs(resultCommand)},
	{name: "mcp", help: mcpHelp, run: mcpCommand},
	{name: "emit-otlp", help: emitOTLPHelp, run: emitOTLPCommand},
}

func withOutputs(fn func(ctx context.Context, args []string, stdout, stderr io.Writer) int) runner {
	return func(ctx context.Context, args []string, std stdio) int { return fn(ctx, args, std.stdout, std.stderr) }
}

func withoutContext(fn func(args []string, stdout, stderr io.Writer) int) runner {
	return func(_ context.Context, args []string, std stdio) int { return fn(args, std.stdout, std.stderr) }
}

// reconcileWithProvider reads the admin key from the environment and asks the
// provider's API.
func reconcileWithProvider(ctx context.Context, args []string, std stdio) int {
	return reconcileCommand(ctx, args, os.Getenv, reconcile.NewClient(), time.Now, std.stdout, std.stderr)
}

func lookup(name string) (command, bool) {
	for _, entry := range commands {
		if entry.name == name {
			return entry, true
		}
	}
	return command{}, false
}

// usage prints as the short usage. The flag sets of report, policy and
// follow-up print it. It is a type and not a string because the text lists the
// table, and the table's functions read usage.
var usage shortUsage

type shortUsage struct{}

// usageWidth is the widest line any help page may print.
const usageWidth = 80

func (shortUsage) String() string {
	var text strings.Builder
	text.WriteString("costroid runs a coding agent under a budget and records what it cost.\n\nUsage:\n")
	var others, ledger []string
	for _, entry := range commands {
		switch {
		case entry.ledger:
			ledger = append(ledger, entry.name)
		case entry.short == "":
			others = append(others, entry.name)
		default:
			fmt.Fprintf(&text, "  %s\n", entry.short)
		}
	}
	text.WriteString("  costroid help <command>\n  costroid --version\n\n")
	text.WriteString("The agent command is claude -p or codex exec. Costroid adds the flags it needs.\n\n")
	text.WriteString(wrap("Other commands: "+strings.Join(others, ", ")+".", usageWidth))
	text.WriteString(wrap("Ledger commands: "+strings.Join(ledger, ", ")+".", usageWidth))
	return text.String()
}

// wrap breaks text into lines of at most width bytes at its spaces.
func wrap(text string, width int) string {
	var lines []string
	line := ""
	for _, word := range strings.Fields(text) {
		if line != "" && len(line)+1+len(word) > width {
			lines = append(lines, line)
			line = ""
		}
		if line != "" {
			line += " "
		}
		line += word
	}
	return strings.Join(append(lines, line), "\n") + "\n"
}

func isHelpFlag(arg string) bool {
	return arg == "-h" || arg == "-help" || arg == "--help"
}

// helpCommand answers costroid help, with or without a command's name.
func helpCommand(args []string, std stdio) int {
	if len(args) == 0 {
		fmt.Fprint(std.stdout, usage)
		return 0
	}
	entry, ok := lookup(args[0])
	if !ok || len(args) > 1 {
		return unknownCommand(strings.Join(args, " "), std.stderr)
	}
	fmt.Fprint(std.stdout, entry.help)
	return 0
}

func unknownCommand(name string, stderr io.Writer) int {
	fmt.Fprintf(stderr, "costroid: unknown command %q\nRun costroid help to list the commands.\n", name)
	return exitFailure
}

const accountHelp = `--billing-account fills BillingAccountId on every row. Without it, the
COSTROID_BILLING_ACCOUNT variable does. With neither, the rows read unknown.

--credential fills SubAccountId: the id of the key, workspace or project the run
spends on, never the key itself. Without it, COSTROID_CREDENTIAL does. With
neither, the column is null and reconcile asks for one.
`

const ciHelp = `Inside GitHub Actions or GitLab CI, run and ingest-run state the repository,
the pull request, the head commit and the CI run on every row, and run sets the
vcs.* and cicd.* attributes through OTEL_RESOURCE_ATTRIBUTES for the agent.
These are claims. Rows stay unverified and unattributed until a ledger checks
them against the job's OIDC token.
`

const resultFileHelp = `run and ingest-run write the same object beside the rows file they created,
whether or not --json is given and whatever their exit: --out rows.jsonl gives
rows.run.json. report reads the run's reason for an empty rows file from it,
and a reader that refused the agent's output adds its first error line.
`

const statusFileHelp = `While the agent works, run writes a status file: what the run has spent so
far, its cap, what is left and what holds it. It is COSTROID_STATUS_FILE, or
costroid-status.json in RUNNER_TEMP when that is unset; with neither set, run
writes none. docs/agents.md describes the file.
`

const ingestRunHelp = `Usage:
  costroid ingest-run --from KIND=PATH --out rows.jsonl [--since T]
                      [--billing-account ID] [--credential ID]
  costroid ingest-run --from execution-file=F --out rows.jsonl --cap-usd N
                      [--claude-args TEXT]

ingest-run writes the rows of an agent's run after the run ended, from what
the run left at PATH. KIND is one of:

  execution-file  the execution_file output of anthropics/claude-code-action
  codex-home      the codex-home given to openai/codex-action
  gh-aw           the directory a gh-aw run's usage artifact was downloaded to

--since, with codex-home, is when the job started, in RFC 3339: a file last
written before it belongs to an earlier job and is not read. --execution-file F
and --codex-home D read as --from execution-file=F and --from codex-home=D.
ingest-run reads one source. Costroid did not start the run, so the grade reads
none unless the workflow states a cap.

--cap-usd, with execution-file, is the cap the workflow gave Claude Code as
--max-budget-usd, and --claude-args the step's claude_args. The grade reads
agent-flag when the run stopped at that cap, or ended under it with
claude_args stating it; otherwise it reads none, with the reason. The result
object states the cap.

` + accountHelp + `
` + ciHelp + `
` + resultFileHelp + `
Exit codes of ingest-run:
  0                 the rows and the result object are written
  125               costroid could not read the flags or the source, or write
                    the rows
`

const reportHelp = `Usage:
  costroid report --rows rows.jsonl [--budget-usd N] [--installation-id ID]
                  [--agent-result RESULT] [--json] [--exit-on-verdict]
  costroid report --preview --rows rows.jsonl

report posts from CI, and selects the host from the job's variables. On a pull
request it reads costroid.yml at the head of the base branch when it runs, and
at the event's base commit when that head cannot be read. On GitHub
Actions it posts one check run and one sticky comment, with GITHUB_TOKEN and the
GITHUB_* variables, then writes the step's outputs and a job summary. The
summary withholds amounts and token counts where the comment does. On GitLab CI
it posts one sticky merge request note, with COSTROID_GITLAB_TOKEN and the CI_*
variables, and no check. It exits 0 whatever the host answers, so a failed post
never fails the job. Outside both it refuses and exits 125. The run's cap
comes from its result object; --budget-usd is read only when there is none.
--installation-id puts the GitHub App installation, github:<id> or the bare
number, on the account link; COSTROID_INSTALLATION_ID does the same. A value
in neither form is skipped with a warning. GitLab's link names the project id.
--exit-on-verdict, on GitLab CI alone, ends the report with the verdict's exit
code instead of 0: 1 on failure, 3 on neutral, 0 on success. On GitHub Actions
the check is the gate, and the flag is ignored with a warning.

--agent-result is the agent job's result as CI states it: success, failure,
cancelled or skipped. Without it, COSTROID_AGENT_RESULT does. When the job was
skipped and left no rows, the comment says the agent did not run on the pull
request, in place of a report with no rows.

--preview prints, as Markdown, the comment report would post for the rows, and
reads and posts nothing on any host. Its verdict holds the rows to costroid.yml
in the working directory, as costroid policy does.

--json prints on stdout the result object the check run carries, byte for
byte: the conclusion, figures per currency and basis, token counts by class,
grades, the cap and what is left or over, and a reason when there are no
figures. docs/agents.md describes the object.

` + resultFileHelp + `
Exit codes of report:
  0                 the report ran, whatever the host answered, or --preview
                    printed the comment
  1                 with --exit-on-verdict on GitLab CI, the verdict is failure
  3                 with --exit-on-verdict on GitLab CI, the verdict is
                    neutral, or the report could not reach GitLab
  125               the flags could not be read, neither GitHub Actions nor
                    GitLab CI was detected, or --preview could not build the
                    comment
`

const followUpHelp = `Usage:
  costroid follow-up --rows rows.jsonl
  costroid follow-up --ingested --rows rows.jsonl

follow-up posts the check for a fork's pull request from a workflow_run job,
which holds the base repository's token. It reads the finished run's rows as
data, up to 8 MiB, and costroid.yml at the default branch's commit. It posts no
comment, and nothing for a branch of the repository itself, whose report job
posts the check.

--ingested says this job wrote the rows with costroid ingest-run from the
finished run's artifact, for a workflow with no report job of its own, such as
gh-aw's. follow-up then posts for a branch of the repository as well, and reads
the run's object beside the rows. A fork's check still concludes neutral at
most.

Exit codes of follow-up:
  0                 follow-up ran, whatever the host answered
  125               GitHub Actions was not detected, or the flags could not be
                    read
`

const reconcileHelp = `Usage:
  costroid reconcile --rows rows.jsonl --from D --to D [--credential ID]
                     [--upstream-credential SOURCE=ID]
                     [--out FILE] [--reports DIR --as-of T]

reconcile compares rows with the provider's usage and cost reports, per
credential per UTC day. Tokens must match within 1% by class. List-price dollars
are compared within 10%, and the residual is split into named causes.

It runs where the admin key lives, and it reads the key from
ANTHROPIC_ADMIN_KEY or OPENAI_ADMIN_KEY. The key never leaves the process.
--reports reads saved report files from a directory instead, and needs no key;
--as-of states when they were saved. docs/reconcile.md names the files.

A row's credential is its SubAccountId. --credential names one for rows where
that is null. A gateway's rows name its run keys, which the provider never
sees: --upstream-credential names the key the gateway sends with, once per
source, such as anthropic=apikey_01Example. --out keeps one row per source,
credential and day, and a rerun replaces it. Rerun through the eighth day after
month end, when restating stops.

Exit codes of reconcile:
  0                 no token miss and no price miss
  1                 a token miss or a price miss, each a defect
  125               costroid could not read the rows or a report
`

const policyHelp = `Usage:
  costroid policy --rows rows.jsonl [--config costroid.yml]
                  [--workflow-added T] [--at T] [--json]

policy gives, offline, the verdict the check gives: it holds the rows to the
policy keys of the costroid.yml named, which should be the base branch's.
--workflow-added and --at state when the workflow reached the base branch and
when the check ran, both as RFC 3339. Without --workflow-added the warn-only
period has not ended. --json prints the verdict as the check's machine block
carries it.

Exit codes of policy:
  0                 success or neutral
  1                 failure
  125               costroid could not read the rows or the file
`

const validateHelp = `Usage:
  costroid validate [costroid.yml]

validate checks costroid.yml, or the file named, against the schema this
release ships, costroid.schema.json: every key and every value must be one
the schema allows. A policy amount or approval.expires_days that YAML reads as
a float, such as 5.0, is a problem too, because the check refuses it. validate
prints one line per problem, or valid. With no file named and no costroid.yml
here, it says the defaults apply.

Exit codes of validate:
  0                 the file is valid, or there is none and the defaults apply
  1                 the file breaks the schema
  125               costroid could not read the file
`

const lintRecipesHelp = `Usage:
  costroid lint-recipes <paths>

lint-recipes holds the CI recipes Costroid ships, and its action, to their
security rules: no pull_request_target trigger, every action pinned by commit
SHA, explicit permissions, an agent's job that holds read access to the code
and no OIDC token, agent telemetry that carries no prompt or response, and no
expression inside a script. When the working directory holds a plugin
marketplace, it holds the marketplace and its plugins to their rules too. Each
rejected line goes to stderr. costroid lint judges your own workflows instead.

Exit codes of lint-recipes:
  0                 every file passes
  1                 a line is rejected
  125               costroid could not read a path
`

const versionHelp = `Usage:
  costroid version
  costroid --version

version prints this binary's release and the price catalog it carries.

Exit codes of version:
  0                 printed
  125               costroid could not load its catalog
`

const demoHelp = `Usage:
  costroid demo [--state STATE] [--markdown]

demo prints the comment Costroid posts on a pull request, built from the rows of
a recorded run, as plain text for a terminal. It reads nothing and posts
nothing.

--state picks the comment:
  neutral           a recorded claude-code-action run that nothing held to a
                    budget (the default)
  capped            a recorded Codex run that costroid stopped at a USD 0.001
                    cap, and what it spent past it
  failure           the claude-code-action run over a USD 0.01 run budget, after
                    the seven days in which a broken rule only warns
  skipped           a pull request on which the workflow skipped the agent

--markdown prints the comment as the Markdown report posts, with the machine
block the check run carries.

Exit codes of demo:
  0                 printed
  125               an argument or an unknown --state was given, or costroid
                    could not build the comment
`
