package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/Costroid/costroid/internal/cli"
	"github.com/Costroid/costroid/internal/reconcile"
	"github.com/Costroid/costroid/internal/server"
)

// command is one entry of the dispatch table. A new command is a file with its
// function and help, and one line here.
type command struct {
	name string
	// where is the group the bare call lists the command under.
	where group
	// does is the command's line in the bare call.
	does string
	// help is what costroid help <name> prints.
	help string
	run  runner
}

// group is where a command runs, or what it speaks to.
type group int

const (
	onYourMachine group = iota
	inAWorkflowJob
	toAPlatform
	forALedger
)

// groupNames head the groups of the bare call. It sets them two abreast, in
// this order.
var groupNames = []string{"On your machine", "In a workflow job", "To a platform or a provider", "For a ledger of your own"}

type runner func(ctx context.Context, args []string, std stdio) int

type stdio struct {
	stdin          io.Reader
	stdout, stderr io.Writer
}

// commands lists each group's commands in the order the bare call prints
// them. The first group opens with what a first reader meets: what costs
// nothing, what adds the check to a repository, then a run of one's own.
var commands = []command{
	{name: "demo", where: onYourMachine, does: "print an example comment", help: demoHelp, run: withOutputs(demoCommand)},
	{name: "lint", where: onYourMachine, does: "judge agent workflows", help: lintHelp, run: withoutContext(lintCommand)},
	{name: "init", where: onYourMachine, does: "write the workflow", help: initHelp, run: withoutContext(initCommand)},
	{name: "doctor", where: onYourMachine, does: "check a repository", help: doctorHelp, run: withOutputs(doctorCommand)},
	{name: "run", where: onYourMachine, does: "run an agent under a cap", help: runHelp, run: runCommand},
	{name: "policy", where: onYourMachine, does: "give the verdict offline", help: policyHelp, run: withOutputs(policyCommand)},
	{name: "validate", where: onYourMachine, does: "check costroid.yml", help: validateHelp, run: validateCommand},
	{name: "version", where: onYourMachine, does: "print the release", help: versionHelp, run: versionCommand},
	{name: "report", where: inAWorkflowJob, does: "post the check and comment", help: reportHelp, run: withOutputs(reportCommand)},
	{name: "ingest-run", where: inAWorkflowJob, does: "write rows after a run", help: ingestRunHelp, run: withoutContext(ingestRunCommand)},
	{name: "follow-up", where: inAWorkflowJob, does: "post a fork's check", help: followUpHelp, run: withOutputs(followUpCommand)},
	{name: "approve", where: inAWorkflowJob, does: "post an approved check", help: approveHelp, run: withOutputs(approveCommand)},
	{name: "review", where: inAWorkflowJob, does: "post the agent's answer", help: reviewHelp, run: withOutputs(reviewCommand)},
	{name: "result", where: inAWorkflowJob, does: "print a check's result", help: resultHelp, run: withOutputs(resultCommand)},
	{name: "mcp", where: inAWorkflowJob, does: "answer an agent over MCP", help: mcpHelp, run: mcpCommand},
	{name: "lint-recipes", where: inAWorkflowJob, does: "check Costroid's recipes", help: lintRecipesHelp, run: withoutContext(lintRecipes)},
	{name: "export", where: toAPlatform, does: "send or convert rows", help: exportHelp, run: exportCommand},
	{name: "merged", where: toAPlatform, does: "send merge facts", help: mergedHelp, run: mergedCommand},
	{name: "connect", where: toAPlatform, does: "bind an owner", help: connectHelp, run: connectCommand},
	{name: "emit-otlp", where: toAPlatform, does: "send rows as OTLP spans", help: emitOTLPHelp, run: emitOTLPCommand},
	{name: "reconcile", where: toAPlatform, does: "compare with a provider", help: reconcileHelp, run: reconcileWithProvider},
	{name: "serve", where: forALedger, does: "run the ingest service", help: server.ServeUsage, run: func(ctx context.Context, args []string, std stdio) int { return server.Main(ctx, args, std.stderr) }},
	{name: "keys", where: forALedger, does: "issue and revoke keys", help: server.KeysUsage, run: withOutputs(server.KeysMain)},
	{name: "egress", where: forALedger, does: "report outbound calls", help: server.EgressUsage, run: withOutputs(server.EgressMain)},
	{name: "gateway", where: forALedger, does: "give a run its own key", help: gatewayUsage, run: withOutputs(gatewayCommand)},
	{name: "app", where: forALedger, does: "register the GitHub App", help: appHelp, run: appCommand},
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

// usage prints as the bare call, which its String method builds from the
// table.
var usage bareCall

type bareCall struct{}

// String gives one line of purpose, every command once under its group with
// what it does, a command that needs nothing set up, how to read a command's
// help, and where the documentation is.
func (bareCall) String() string {
	var text strings.Builder
	text.WriteString("Costroid is the cost check for pull requests.\n")
	// blocks holds one more than the groups when their count is odd, so the
	// last group has an empty block beside it.
	blocks := make([][]string, len(groupNames)+len(groupNames)%2)
	for where, name := range groupNames {
		blocks[where] = groupLines(group(where), name)
	}
	left := 0
	for where := 0; where < len(blocks); where += 2 {
		for _, line := range blocks[where] {
			left = max(left, len(line))
		}
	}
	for where := 0; where < len(blocks); where += 2 {
		text.WriteString("\n")
		for i := range max(len(blocks[where]), len(blocks[where+1])) {
			text.WriteString(strings.TrimRight(fmt.Sprintf("%-*s  %s", left, line(blocks[where], i), line(blocks[where+1], i)), " ") + "\n")
		}
	}
	text.WriteString("\nExample, with nothing set up:  costroid demo\n")
	text.WriteString("Help for one command:          costroid help <command>\n")
	text.WriteString("Documentation:                 " + cli.Documentation + "\n")
	return text.String()
}

// groupLines gives a group's heading, then each of its commands beside what
// it does.
func groupLines(where group, heading string) []string {
	widest := 0
	for _, entry := range commands {
		if entry.where == where {
			widest = max(widest, len(entry.name))
		}
	}
	lines := []string{heading}
	for _, entry := range commands {
		if entry.where == where {
			lines = append(lines, fmt.Sprintf("  %-*s  %s", widest, entry.name, entry.does))
		}
	}
	return lines
}

// line answers the line at index i, empty past the last.
func line(lines []string, i int) string {
	if i < len(lines) {
		return lines[i]
	}
	return ""
}

// The line a command that succeeded ends on names the command a person runs
// next. demo ends on the verdict sentence, so its line stands above that.
const (
	nextAfterDemo     = "Next: costroid init writes the workflow that posts this on your pull requests."
	nextAfterInit     = "Next: costroid doctor checks this repository before its first pull request."
	nextAfterDoctor   = "Next: open a pull request, and its first run posts Costroid's comment."
	nextAfterValidate = "Next: costroid policy --rows rows.jsonl gives the verdict this file gives a run."
)

// standardInput is the path that names standard input where a command reads a
// file.
const standardInput = "-"

// readNamed reads the file at path, or stdin when path names it.
func readNamed(path string, stdin io.Reader) ([]byte, error) {
	if path == standardInput {
		return io.ReadAll(stdin)
	}
	return os.ReadFile(path)
}

// usageWidth is the widest line a help page or a closing screen may print.
const usageWidth = cli.Width

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

// parseFlags parses args with set. Help asked for after other flags prints on
// stdout, as costroid <name> -h does. A flag the set refuses comes back as the
// lines that say so, and nothing is printed for it here.
func parseFlags(set *flag.FlagSet, args []string, help string, stdout io.Writer) error {
	err := cli.Parse(set, args)
	if errors.Is(err, flag.ErrHelp) {
		fmt.Fprint(stdout, help)
	}
	return err
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

// unknownCommand names the command as it was typed and the nearest one the
// table holds, and runs neither.
func unknownCommand(name string, stderr io.Writer) int {
	names := make([]string, len(commands))
	for i, entry := range commands {
		names[i] = entry.name
	}
	near := cli.NearestLine("command", "", cli.Nearest(name, names))
	if near == "" {
		near = "No command is near it."
	}
	fmt.Fprintf(stderr, "costroid: unknown command %q\n%s\nRun costroid help to list the commands.\n", name, near)
	return exitUsage
}

// accountFlagRows and rowFlagRows are the flags run and ingest-run share.
var accountFlagRows = []cli.Row{
	{"--billing-account ID", "the account the run is billed to"},
	{"--credential ID", "the id of the key, workspace or project the run spends on"},
}

// accountEnvironment is what stands in for the account flags.
var accountEnvironment = []cli.Row{
	{billingAccountVariable, "the billing account, when --billing-account is not given"},
	{credentialVariable, "the credential's id, when --credential is not given"},
}

// ciEnvironment is what a CI system says about the job that runs the command.
var ciEnvironment = cli.Row{"GITHUB_*, CI_*", "what GitHub Actions or GitLab CI sets about the job"}

const accountHelp cli.Text = `--billing-account fills BillingAccountId on every row. Without it, the
COSTROID_BILLING_ACCOUNT variable does. With neither, the rows read unknown.

--credential fills SubAccountId: the id of the key, workspace or project the run
spends on, never the key itself. Without it, COSTROID_CREDENTIAL does. With
neither, the column is null and reconcile asks for one.`

const ciHelp cli.Text = `Inside GitHub Actions or GitLab CI, run and ingest-run state the repository,
the pull request, the head commit and the CI run on every row, and run sets the
vcs.* and cicd.* attributes through OTEL_RESOURCE_ATTRIBUTES for the agent.
These are claims. Rows stay unverified and unattributed until a ledger checks
them against the job's OIDC token.`

const resultFileHelp cli.Text = `run and ingest-run write the same object beside the rows file they created,
whether or not --json is given and whatever their exit: --out rows.jsonl gives
rows.run.json. report reads the run's reason for an empty rows file from it,
and a reader that refused the agent's output adds its first error line.`

const statusFileHelp cli.Text = `While the agent works, run writes a status file: what the run has spent so
far, its cap, what is left and what holds it. It is COSTROID_STATUS_FILE, or
costroid-status.json in RUNNER_TEMP when that is unset; with neither set, run
writes none. docs/agents.md describes the file.`

var ingestRunHelp = cli.Page{
	Command: "ingest-run",
	Summary: `ingest-run writes the rows of an agent's run after the run ended, from what
the run left at PATH.`,
	Usage: []string{
		"costroid ingest-run --from KIND=PATH --out rows.jsonl [--since T]",
		"                    [--billing-account ID] [--credential ID]",
		"                    [--tag KEY=VALUE]... [--focus-profile strict]",
		"costroid ingest-run --from execution-file=F --out rows.jsonl --cap-usd N",
		"                    [--claude-args TEXT]",
		"costroid ingest-run --from focus=FILE --out rows.jsonl [--source NAME]",
		"                    [--optional] [--billing-account ID]",
	},
	Examples: []string{
		"costroid ingest-run --from execution-file=execution.json --out rows.jsonl",
		"costroid ingest-run --from execution-file=execution.json --out rows.jsonl \\",
		"    --cap-usd 5",
		"costroid ingest-run --from codex-home=codex-home --out rows.jsonl \\",
		"    --since 2026-11-21T09:00:00Z",
		"costroid ingest-run --from focus=infrastructure.csv --out rows.jsonl \\",
		"    --optional",
	},
	Flags: slices.Concat([]cli.Row{
		{"--from KIND=PATH", "the source to read"},
		{"--execution-file F", "the execution_file output of anthropics/claude-code-action"},
		{"--codex-home D", "the codex-home given to openai/codex-action"},
		{"--out FILE", "the file the FOCUS rows go to, as JSON lines"},
		{"--since T", "when the job started, in RFC 3339"},
		{"--cap-usd N", "the cap the workflow held the run to, in USD"},
		{"--claude-args TEXT", "the claude_args the claude-code-action step was given"},
		{"--source NAME", "with focus, what the estimate came from, in costroid/cost_source"},
		{"--optional", "with focus, add no rows and say why when the file is not there"},
	}, accountFlagRows, rowFlagRows),
	Sections: []cli.Section{
		{Heading: "Sources", Body: []cli.Block{
			cli.Text("KIND is one of:"),
			cli.Rows{
				{"execution-file", "the execution_file output of anthropics/claude-code-action"},
				{"codex-home", "the codex-home given to openai/codex-action"},
				{"gh-aw", "the directory a gh-aw run's usage artifact was downloaded to"},
				{"focus", "a FOCUS cost file, as CSV, that a step of the workflow wrote"},
			},
			cli.Text(`--since, with codex-home, is when the job started, in RFC 3339: a file last
written before it belongs to an earlier job and is not read. --execution-file F
and --codex-home D read as --from execution-file=F and --from codex-home=D.
ingest-run reads one source. Costroid did not start the run, so the grade reads
none unless the workflow states a cap.`),
		}},
		{Heading: "A cap the workflow states", Body: []cli.Block{cli.Text(`--cap-usd, with execution-file, is the cap the workflow gave Claude Code as
--max-budget-usd, and --claude-args the step's claude_args. The grade reads
agent-flag when the run stopped at that cap, or ended under it with
claude_args stating it; otherwise it reads none, with the reason. The result
object states the cap.`)}},
		{Heading: "A FOCUS cost file", Body: []cli.Block{cli.Text(`--from focus=FILE adds one row per row of FILE after the rows the rows file
holds, each with the basis estimate, and leaves the run's object as it is. The
figure is read from ListCost, or else from the one of EffectiveCost,
ContractedCost and BilledCost the file holds. Every row needs BillingCurrency,
ChargePeriodStart and ChargePeriodEnd. The file's Tags are dropped, and
--credential is not read: SubAccountId is the file's. --source names the
estimate's origin, infrastructure unless given. --optional adds no rows and
says why when FILE is not there. docs/infrastructure.md describes the file.`)}},
		{Heading: "The rows", Body: []cli.Block{accountHelp, rowFlagsHelp, ciHelp}},
		{Heading: "The result object", Body: []cli.Block{resultFileHelp}},
	},
	Environment: []cli.Block{cli.Rows(append(slices.Clone(accountEnvironment), ciEnvironment))},
	Exits: []cli.Row{
		{"0", "the rows and the result object are written"},
		cli.UsageExit,
		{"125", "costroid could not read the source or write the rows"},
	},
	Docs: "existing-workflow",
}.String()

var reportHelp = cli.Page{
	Command: "report",
	Summary: "report posts from CI, and selects the host from the job's variables.",
	Usage: []string{
		"costroid report --rows rows.jsonl [--budget-usd N] [--installation-id ID]",
		"                [--agent-result RESULT] [--json] [--exit-on-verdict]",
		"costroid report --preview --rows rows.jsonl",
		"costroid report --recorded RUN_ID,DATE --rows rows.jsonl",
	},
	Examples: []string{
		"costroid report --rows rows.jsonl",
		"costroid report --preview --rows rows.jsonl",
		"costroid report --rows rows.jsonl --exit-on-verdict",
		"costroid report --recorded 19283746501,2026-11-07 --rows rows.jsonl",
	},
	Flags: []cli.Row{
		{"--rows FILE", "the rows file costroid run wrote"},
		{"--budget-usd N", "the run's cap in USD, read only when the run left no result object"},
		{"--installation-id ID", "the GitHub App installation, github:<id> or the bare number; GitLab uses the project id"},
		{"--agent-result RESULT", "the agent job's result: success, failure, cancelled or skipped"},
		{"--json", "print the result object on stdout"},
		{"--exit-on-verdict", "on GitLab CI, exit 1 on a failure verdict and 3 on a neutral one"},
		{"--preview", "print the comment as Markdown and post nothing"},
		{"--recorded RUN_ID,DATE", "the rows are the recorded run RUN_ID's from DATE, not the pull request's"},
	},
	Sections: []cli.Section{
		{Heading: "What it posts", Body: []cli.Block{cli.Text(`On a pull
request it reads costroid.yml at the head of the base branch when it runs, and
at the event's base commit when that head cannot be read. On GitHub
Actions it posts one check run and one sticky comment, with GITHUB_TOKEN and the
GITHUB_* variables, then writes the step's outputs and a job summary. The
summary withholds amounts and token counts where the comment does. On GitLab CI
it posts one sticky merge request note, with COSTROID_GITLAB_TOKEN and the CI_*
variables, and no check. It exits 0 whatever the host answers, so a failed post
never fails the job. Outside both it refuses and exits 125.`)}},
		{Heading: "The cap and the account link", Body: []cli.Block{cli.Text(`The run's cap
comes from its result object; --budget-usd is read only when there is none.
--installation-id puts the GitHub App installation, github:<id> or the bare
number, on the account link; COSTROID_INSTALLATION_ID does the same. A value
in neither form is skipped with a warning. GitLab's link names the project id.`)}},
		{Heading: "The verdict as an exit code", Body: []cli.Block{cli.Text(`--exit-on-verdict, on GitLab CI alone, ends the report with the verdict's exit
code instead of 0: 1 on failure, 3 on neutral, 0 on success. On GitHub Actions
the check is the gate, and the flag is ignored with a warning.`)}},
		{Heading: "The agent's job", Body: []cli.Block{cli.Text(`--agent-result is the agent job's result as CI states it: success, failure,
cancelled or skipped. Without it, COSTROID_AGENT_RESULT does. When the job was
skipped and left no rows, the comment says the agent did not run on the pull
request, in place of a report with no rows.`)}},
		{Heading: "A preview", Body: []cli.Block{cli.Text(`--preview prints, as Markdown, the comment report would post for the rows, and
reads and posts nothing on any host. Its verdict holds the rows to costroid.yml
in the working directory, as costroid policy does. Standard output is the
comment alone. The last line, on standard error, is the verdict sentence the
comment opens with.`)}},
		{Heading: "A recorded run", Body: []cli.Block{cli.Text(`--recorded says the rows are a recorded run's, not the pull request's: CI run
RUN_ID's, recorded on DATE, written YYYY-MM-DD. The check, the comment and the
job summary say so in the line under their first, without the head commit, and
the result object names the run under recorded. The pull request's total neither
reads these figures nor counts them. On the workflow_run event of a fork's
pull request, whose own run holds a read-only token, report posts the check
and the comment there, on the one open pull request follow-up would find.`)}},
		{Heading: "The result object", Body: []cli.Block{cli.Text(`--json prints on stdout the result object the check run carries, byte for
byte: the conclusion, figures per currency and basis, token counts by class,
grades, the cap and what is left or over, and a reason when there are no
figures. docs/agents.md describes the object.`), resultFileHelp}},
	},
	Environment: []cli.Block{cli.Rows{
		{"GITHUB_TOKEN", "the token the check run and the comment are posted with"},
		{gitLabTokenVariable, "the token the merge request note is posted with"},
		{installationVariable, "the installation, when --installation-id is not given"},
		{agentResultVariable, "the agent job's result, when --agent-result is not given"},
		{platformVariable, "Costroid Platform's origin, for the account link"},
		ciEnvironment,
	}},
	Exits: []cli.Row{
		{"0", "the report ran, whatever the host answered, or --preview printed the comment"},
		{"1", "with --exit-on-verdict on GitLab CI, the verdict is failure"},
		cli.UsageExit,
		{"3", "with --exit-on-verdict on GitLab CI, the verdict is neutral, or the report could not reach GitLab"},
		{"125", "neither GitHub Actions nor GitLab CI was detected, or --preview could not build the comment"},
	},
	Docs: "policy",
}.String()

var followUpHelp = cli.Page{
	Command: "follow-up",
	Summary: `follow-up posts the check for a fork's pull request from a workflow_run job,
which holds the base repository's token.`,
	Usage: []string{
		"costroid follow-up --rows rows.jsonl",
		"costroid follow-up --ingested --rows rows.jsonl",
	},
	Examples: []string{
		"costroid follow-up --rows rows.jsonl",
		"costroid follow-up --ingested --rows rows.jsonl",
	},
	Flags: []cli.Row{
		{"--rows FILE", "the rows file the finished run kept, read as data"},
		{"--ingested", "the rows are ones costroid ingest-run wrote in this job, for a workflow with no report job"},
	},
	Sections: []cli.Section{
		{Heading: "What it reads and posts", Body: []cli.Block{cli.Text(`It reads the finished run's rows as
data, up to 8 MiB, and costroid.yml at the default branch's commit. It posts no
comment, and nothing for a branch of the repository itself, whose report job
posts the check.`)}},
		{Heading: "Rows this job ingested", Body: []cli.Block{cli.Text(`--ingested says this job wrote the rows with costroid ingest-run from the
finished run's artifact, for a workflow with no report job of its own, such as
gh-aw's. follow-up then posts for a branch of the repository as well, and reads
the run's object beside the rows. A fork's check still concludes neutral at
most.`)}},
	},
	Environment: []cli.Block{cli.Rows{
		{"GITHUB_TOKEN", "the base repository's token, which the check is posted with"},
		{"GITHUB_*", "what GitHub Actions sets about the workflow_run job"},
	}},
	Exits: []cli.Row{
		{"0", "follow-up ran, whatever the host answered"},
		cli.UsageExit,
		{"125", "GitHub Actions was not detected"},
	},
	Docs: "security",
}.String()

var reconcileHelp = cli.Page{
	Command: "reconcile",
	Summary: `reconcile compares rows with the provider's usage and cost reports, per
credential per UTC day.`,
	Usage: []string{
		"costroid reconcile --rows rows.jsonl --from D --to D [--credential ID]",
		"                   [--upstream-credential SOURCE=ID]",
		"                   [--out FILE] [--reports DIR --as-of T]",
		"                   [--export [--installation-id ID]]",
	},
	Examples: []string{
		"costroid reconcile --rows rows.jsonl --from 2026-11-01 --to 2026-11-20",
		"costroid reconcile --rows rows.jsonl --from 2026-11-01 --to 2026-11-01 \\",
		"    --reports saved --as-of 2026-11-04T00:00:00Z",
		"costroid reconcile --rows rows.jsonl --from 2026-11-01 --to 2026-11-20 \\",
		"    --out reconciliation.jsonl --export",
	},
	Flags: []cli.Row{
		{"--rows FILE", "the FOCUS rows, as JSON lines"},
		{"--from D", "the first UTC day, YYYY-MM-DD"},
		{"--to D", "the last UTC day, YYYY-MM-DD"},
		{"--credential ID", "the credential of rows whose SubAccountId is null"},
		{"--upstream-credential SOURCE=ID", "the provider key a gateway holds, which rows drawn on its run keys are compared under"},
		{"--out FILE", "the file reconciliation rows are kept in, one per source, credential and day"},
		{"--reports DIR", "a directory of saved provider reports, read in place of the provider"},
		{"--as-of T", "when the saved reports were saved, as a UTC instant"},
		{"--export", "send the rows --out keeps to Costroid Platform, by integration/v0.6 or v0.5"},
		{"--installation-id ID", "with --export, the installation the job runs under"},
	},
	Sections: []cli.Section{
		{Heading: "What counts as a match", Body: []cli.Block{cli.Text(`Tokens must match within 1% by class. List-price dollars
are compared within 10%, and the residual is split into named causes.`)}},
		{Heading: "The provider's reports", Body: []cli.Block{cli.Text(`It runs where the admin key lives, and it reads the key from
ANTHROPIC_ADMIN_KEY or OPENAI_ADMIN_KEY. The key never leaves the process.
--reports reads saved report files from a directory instead, and needs no key;
--as-of states when they were saved. docs/reconcile.md names the files.`)}},
		{Heading: "Credentials and days", Body: []cli.Block{cli.Text(`A row's credential is its SubAccountId. --credential names one for rows where
that is null. A gateway's rows name its run keys, which the provider never
sees: --upstream-credential names the key the gateway sends with, once per
source, such as anthropic=apikey_01Example. --out keeps one row per source,
credential and day, and a rerun replaces it. Rerun through the eighth day after
month end, when restating stops. Every amount is a decimal string in USD, and
a figure past 30 places or 64 characters stops its day, unrounded.

A run that crosses midnight UTC is compared over the days it spans, for tokens
and dollars, on the first day's row. A later day's row keeps its own prices
and carries no tokens or dollars.`)}},
		{Heading: "Sending to Costroid Platform", Body: []cli.Block{cli.Text(`--export sends the rows this run wrote to --out to the Costroid Platform
COSTROID_PLATFORM_URL names, under the integration token in
COSTROID_PLATFORM_TOKEN, by integration/v0.6, or by v0.5 to a platform that
states no later version, and names the version on standard error. A platform
that states only an earlier version gets nothing. The rows go in bodies cut at
the limits GET /quotas states, a span's rows in one body. On GitHub Actions
with id-token: write the job's run token goes with them, beside the
installation --installation-id names. A server error sends the same body again
every 2 s for up to 60 s. On 402, 409 INSTALLATION_NOT_BOUND or 429 reconcile
warns and keeps the rows. The admin key never goes to the platform.`)}},
	},
	Environment: []cli.Block{cli.Rows{
		{anthropicKeyVariable, "Anthropic's admin key"},
		{openAIKeyVariable, "OpenAI's admin key"},
		{platformVariable, "with --export, the Costroid Platform the rows go to"},
		{platformTokenVariable, "with --export, the integration token"},
	}},
	Exits: []cli.Row{
		{"0", "no token miss and no price miss"},
		{"1", "a token miss or a price miss, each a defect"},
		cli.UsageExit,
		{"125", "costroid could not read the rows or a report, a figure stopped its day, or the rows could not be sent"},
	},
	Docs: "reconcile",
}.String()

var policyHelp = cli.Page{
	Command: "policy",
	Summary: `policy gives, offline, the verdict the check gives: it holds the rows to the
policy keys of the costroid.yml named, which should be the base branch's.`,
	Usage: []string{
		"costroid policy --rows rows.jsonl [--config costroid.yml] [--pack FILE]",
		"                [--history earlier.jsonl] [--baseline merged.jsonl]",
		"                [--workflow-added T] [--at T] [--json]",
	},
	Examples: []string{
		"costroid policy --rows rows.jsonl",
		"costroid policy --rows rows.jsonl --config base.yml",
		"costroid policy --rows rows.jsonl --config base.yml --pack base-policy.rego",
		"costroid policy --rows rows.jsonl --config base.yml --history earlier.jsonl \\",
		"    --baseline merged.jsonl",
	},
	Flags: []cli.Row{
		{"--rows FILE", "the rows file costroid run wrote"},
		{"--config FILE", "costroid.yml as the base branch holds it"},
		{"--pack FILE", "the policy pack costroid.yml names, as the base branch holds it"},
		{"--history FILE", "the result objects of the pull request's earlier check runs, one per line"},
		{"--baseline FILE", "the result objects of the check runs on the merged pull requests' heads, one per line"},
		{"--workflow-added T", "when the workflow reached the base branch, as RFC 3339"},
		{"--at T", "the time of the check, as RFC 3339; now when empty"},
		{"--json", "print the verdict as the machine block carries it"},
	},
	Sections: []cli.Section{
		{Heading: "The policy pack", Body: []cli.Block{cli.Text(`--pack names the file of the policy pack its policy_pack names, as the base
branch holds it. Without --pack, the path policy_pack names is read from the
directory that holds the costroid.yml named.`)}},
		{Heading: "Earlier runs and the baseline", Body: []cli.Block{cli.Text(`--history names the result objects of the pull request's earlier check runs,
one per line, each with its check_run_id: the pull request's total is read
from them as the report reads it. --baseline names, in the same form, the
result objects of the check runs on the heads of the pull requests merged
into the base branch: the agent's baseline is read from them. --history
without --baseline is refused when a rule reads a baseline.`)}},
		{Heading: "The warn-only period", Body: []cli.Block{cli.Text(`--workflow-added
and --at state when the workflow reached the base branch and when the check
ran, both as RFC 3339. Without --workflow-added the warn-only period has not
ended.`)}},
		{Heading: "What it prints", Body: []cli.Block{cli.Text(`The last line is the verdict sentence, the title the check gets. --json
prints the verdict as the check's machine block carries it.`)}},
	},
	Exits: []cli.Row{
		{"0", "success or neutral"},
		{"1", "failure"},
		cli.UsageExit,
		{"125", "costroid could not read the rows or the file"},
	},
	Docs: "policy",
}.String()

var validateHelp = cli.Page{
	Command: "validate",
	Summary: `validate checks costroid.yml, or the file named, against the schema this
release ships, costroid.schema.json: every key and every value must be one
the schema allows.`,
	Usage:    []string{"costroid validate [costroid.yml]"},
	Examples: []string{"costroid validate", "costroid validate base.yml"},
	Sections: []cli.Section{
		{Heading: "What it checks and prints", Body: []cli.Block{cli.Text(`A policy amount is a plain decimal, quoted or not: 5, 5.0 and "2.50" are
valid, and an amount written another way, such as 1e3, is a problem. An
approval.expires_days that YAML reads as a float, such as 7.0, is a problem
too, because the check refuses it. validate
prints one line per problem, or valid. With no file named and no costroid.yml
here, it says the defaults apply.

With - in place of a file, validate reads standard input.`)}},
	},
	Exits: []cli.Row{
		{"0", "the file is valid, or there is none and the defaults apply"},
		{"1", "the file breaks the schema"},
		cli.UsageExit,
		{"125", "costroid could not read the file"},
	},
	Docs: "configuration",
}.String()

var lintRecipesHelp = cli.Page{
	Command: "lint-recipes",
	Summary: `lint-recipes holds the CI recipes Costroid ships, and its action, to their
security rules: no pull_request_target trigger, every action pinned by commit
SHA, explicit permissions, an agent's job that holds read access to the code
and no OIDC token, agent telemetry that carries no prompt or response, and no
expression inside a script.`,
	Usage:    []string{"costroid lint-recipes <paths>"},
	Examples: []string{"costroid lint-recipes recipes action/action.yml"},
	Sections: []cli.Section{
		{Heading: "What else it holds, and where it prints", Body: []cli.Block{cli.Text(`When the working directory holds a plugin
marketplace, it holds the marketplace and its plugins to their rules too. Each
rejected line goes to stderr. costroid lint judges your own workflows instead.`)}},
	},
	Exits: []cli.Row{
		{"0", "every file passes"},
		{"1", "a line is rejected"},
		cli.UsageExit,
		{"125", "costroid could not read a path"},
	},
	Docs: "recipes",
}.String()

var versionHelp = cli.Page{
	Command:  "version",
	Summary:  "version prints this binary's release and the price catalog it carries.",
	Usage:    []string{"costroid version", "costroid --version"},
	Examples: []string{"costroid version"},
	Exits: []cli.Row{
		{"0", "printed"},
		cli.UsageExit,
		{"125", "costroid could not load its catalog"},
	},
	Docs: "install",
}.String()

var demoHelp = cli.Page{
	Command: "demo",
	Summary: `demo prints the comment Costroid posts on a pull request, built from the rows of
a recorded run, as plain text for a terminal.`,
	Usage: []string{"costroid demo [--state STATE] [--markdown]"},
	Examples: []string{
		"costroid demo",
		"costroid demo --state capped",
		"costroid demo --markdown",
	},
	Flags: []cli.Row{
		{"--state STATE", "the comment to show: " + strings.Join(demoStates, ", ")},
		{"--markdown", "print the comment as the Markdown report posts"},
	},
	Sections: []cli.Section{
		{Heading: "What it prints", Body: []cli.Block{cli.Text(`It ends on the verdict sentence
the comment opens with. It reads nothing and posts nothing.`)}},
		{Heading: "The comments", Body: []cli.Block{
			cli.Text("--state picks the comment:"),
			cli.Rows{
				{"neutral", "a recorded claude-code-action run that nothing held to a budget (the default)"},
				{"capped", "a recorded Codex run that costroid stopped at a USD 0.001 cap, and what it spent past it"},
				{"failure", "the claude-code-action run over a USD 0.01 run budget, after the seven days in which a broken rule only warns"},
				{"skipped", "a pull request on which the workflow skipped the agent"},
			},
			cli.Text(`--markdown prints the comment as the Markdown report posts, with the machine
block the check run carries.`),
		}},
	},
	Exits: []cli.Row{
		{"0", "printed"},
		cli.UsageExit,
		{"125", "costroid could not build the comment"},
	},
	Docs: "comment-example",
}.String()
