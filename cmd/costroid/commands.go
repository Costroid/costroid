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
	// help is what costroid help <name> prints. A command without one answers
	// help itself.
	help string
	run  runner
}

type runner func(ctx context.Context, args []string, std stdio) int

type stdio struct {
	stdin          io.Reader
	stdout, stderr io.Writer
}

var commands = []command{
	{name: "run", short: "costroid run --budget-usd N --out rows.jsonl -- <agent command>", help: runHelp, run: runCommand},
	{name: "ingest-run", help: ingestRunHelp, run: func(_ context.Context, args []string, std stdio) int { return ingestRunCommand(args, std.stderr) }},
	{name: "report", short: "costroid report --rows rows.jsonl", help: reportHelp, run: withOutputs(reportCommand)},
	{name: "follow-up", help: followUpHelp, run: withOutputs(followUpCommand)},
	{name: "reconcile", help: reconcileHelp, run: reconcileWithProvider},
	{name: "policy", help: policyHelp, run: withOutputs(policyCommand)},
	{name: "validate", short: "costroid validate [costroid.yml]", help: validateHelp, run: withoutContext(validateCommand)},
	{name: "lint-recipes", help: lintRecipesHelp, run: withoutContext(lintRecipes)},
	{name: "gateway", run: withOutputs(gatewayCommand)},
	{name: "serve", run: func(ctx context.Context, args []string, std stdio) int { return server.Main(ctx, args, std.stderr) }},
	{name: "keys", run: withOutputs(server.KeysMain)},
	{name: "egress", run: withOutputs(server.EgressMain)},
	{name: "version", help: versionHelp, run: versionCommand},
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

const usageWidth = 80

func (shortUsage) String() string {
	var text strings.Builder
	text.WriteString("costroid runs a coding agent under a budget and records what it cost.\n\nUsage:\n")
	var others []string
	for _, entry := range commands {
		if entry.short == "" {
			others = append(others, entry.name)
			continue
		}
		fmt.Fprintf(&text, "  %s\n", entry.short)
	}
	text.WriteString("  costroid help <command>\n  costroid --version\n\n")
	text.WriteString("The agent command is claude -p or codex exec. Costroid adds the flags it needs.\n\n")
	text.WriteString(wrap("Other commands: "+strings.Join(others, ", ")+".", usageWidth))
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
func helpCommand(ctx context.Context, args []string, std stdio) int {
	if len(args) == 0 {
		fmt.Fprint(std.stdout, usage)
		return 0
	}
	entry, ok := lookup(args[0])
	if !ok || len(args) > 1 {
		return unknownCommand(strings.Join(args, " "), std.stderr)
	}
	if entry.help == "" {
		return entry.run(ctx, []string{"--help"}, std)
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

const ciHelp = `Inside GitHub Actions or GitLab CI, run and ingest-run state the repository, the
pull request, the head commit and the CI run on every row, and run sets the
vcs.* and cicd.* attributes through OTEL_RESOURCE_ATTRIBUTES for the agent. These
are claims. Rows stay unverified and unattributed until a ledger checks them
against the job's OIDC token.
`

const jsonHelp = `--json prints one result object on stdout when the command ends: figures per
currency and basis, token counts by class, grades, the cap and what is left or
over, and a reason when there are no figures. run then sends the agent's
stream to stderr, and its object says why it wrote no rows when it wrote none.
report prints the object its check run carries, byte for byte. run prints none
when it exits 125 before it creates the rows file. docs/agents.md describes the
object.
`

const resultFileHelp = `run and ingest-run write the same object beside the rows file they created,
whether or not --json is given and whatever their exit: --out rows.jsonl gives
rows.run.json. report reads the run's reason for an empty rows file from it,
and a reader that refused the agent's output adds its first error line.
`

const ingestRunHelp = `Usage:
  costroid ingest-run --execution-file F --out rows.jsonl [--billing-account ID] [--credential ID]

ingest-run writes the rows of a claude-code-action run from the action's
execution_file output, after the action ended. No budget held that run, so the
grade reads none.

` + accountHelp + `
` + ciHelp + `
` + resultFileHelp

const reportHelp = `Usage:
  costroid report --rows rows.jsonl [--budget-usd N] [--installation-id ID] [--json]

report posts from CI, and selects the host from the job's variables. On a pull
request it reads costroid.yml at the head of the base branch when it runs, and
at the event's base commit when that head cannot be read. On GitHub
Actions it posts one check run and one sticky comment, with GITHUB_TOKEN and the
GITHUB_* variables, then writes the step's outputs and a job summary. The
summary withholds amounts and token counts where the comment does. On GitLab CI
it posts one sticky merge request note, with COSTROID_GITLAB_TOKEN and the CI_*
variables, and no check. It exits 0 whatever the host answers, so a failed post
never fails the job. Outside both it refuses and exits 125. --budget-usd
restates the run's cap, which rows do not record.
--installation-id puts the GitHub App installation on the account link;
COSTROID_INSTALLATION_ID does the same. GitLab's link names the project id.

` + jsonHelp + `
` + resultFileHelp

const followUpHelp = `Usage:
  costroid follow-up --rows rows.jsonl

follow-up posts the check for a fork's pull request from a workflow_run job,
which holds the base repository's token. It reads the finished run's rows as
data, up to 8 MiB, and costroid.yml at the default branch's commit. It posts no
comment, and nothing for a branch of the repository itself, whose report job
posts the check. Like report, it exits 0 whatever the host answers, and outside
GitHub Actions it refuses and exits 125.
`

const reconcileHelp = `Usage:
  costroid reconcile --rows rows.jsonl --from D --to D [--credential ID] [--out FILE] [--reports DIR --as-of T]

reconcile compares rows with the provider's usage and cost reports, per
credential per UTC day. Tokens must match within 1% by class. List-price dollars
are compared within 10%, and the residual is split into named causes.

It runs where the admin key lives, and it reads the key from
ANTHROPIC_ADMIN_KEY or OPENAI_ADMIN_KEY. The key never leaves the process.
--reports reads saved report files from a directory instead, and needs no key;
--as-of states when they were saved. docs/reconcile.md names the files.

A row's credential is its SubAccountId. --credential names one for rows where
that is null. --out keeps one row per source, credential and day, and a rerun
replaces it. Rerun through the eighth day after month end, when restating stops.

Exit codes of reconcile:
  0                 no token miss
  1                 a token miss, which is a defect
  125               costroid could not read the rows or a report
`

const policyHelp = `Usage:
  costroid policy --rows rows.jsonl [--config costroid.yml] [--workflow-added T] [--at T] [--json]

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

const checkExitHelp = `validate and lint-recipes exit 0 when every file passes, 1 when one is rejected
and 125 when they cannot read it.
`

const validateHelp = `Usage:
  costroid validate [costroid.yml]

` + checkExitHelp

const lintRecipesHelp = `Usage:
  costroid lint-recipes <paths>

` + checkExitHelp

const versionHelp = `Usage:
  costroid version
  costroid --version
`
