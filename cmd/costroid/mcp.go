package main

import (
	"context"
	"errors"
	"flag"
	"io"
	"os"
	"strings"

	"github.com/Costroid/costroid/internal/cli"
	"github.com/Costroid/costroid/internal/mcpserver"
	"github.com/Costroid/costroid/internal/policy"
	"github.com/Costroid/costroid/internal/status"
)

// ledgerTokenVariable holds the bearer ledger_pull sends to the ledger.
const ledgerTokenVariable = "COSTROID_LEDGER_TOKEN"

var errMCPArguments = errors.New("mcp takes no argument")

var mcpHelp = cli.Page{
	Command: "mcp",
	Summary: `mcp answers the Model Context Protocol on stdin and stdout, so an agent can
ask what its run has cost while it works.`,
	Usage: []string{"costroid mcp [--repo OWNER/NAME] [--ledger URL]"},
	Examples: []string{
		"costroid mcp",
		"costroid mcp --repo example/api --ledger https://ledger.example",
	},
	Flags: []cli.Row{
		{"--repo OWNER/NAME", "the repository as owner/name"},
		{"--ledger URL", "the URL of a costroid serve"},
	},
	Sections: []cli.Section{
		{Heading: "Tools", Body: []cli.Block{
			cli.Text("Its four tools only read:"),
			cli.Rows{
				{"run_status", `the status file costroid run writes: the spend so far,
the cap, what is left and what holds it`},
				{"pull_result", `the result object of the newest costroid check run on a
pull request's head commit, as costroid result reads it`},
				{"policy_check", `the verdict costroid policy gives on a rows file and a
costroid.yml`},
				{"ledger_pull", "GET /v1/pulls/{pr} on the costroid serve --ledger names"},
			},
			cli.Text("Every amount in an answer is a decimal string beside its currency."),
		}},
		{Heading: "What each tool reaches", Body: []cli.Block{cli.Text(`pull_result reads GitHub with GITHUB_TOKEN, and the repository from --repo,
else GITHUB_REPOSITORY, else the GitHub repository the git remote origin
names, or the only remote. ledger_pull sends COSTROID_LEDGER_TOKEN, an ingest
key or a CI job's OIDC token, as its bearer, and asks for
github.com/OWNER/NAME unless the agent names another repository. --ledger must
be https, or http on a loopback address. The agent names a pull request and
never a host, so no token goes anywhere the command line did not name.`)}},
		{Heading: "In Claude Code", Body: []cli.Block{
			cli.Text("To add it to Claude Code:"),
			cli.Lines{"claude mcp add costroid -- costroid mcp"},
		}},
	},
	Environment: []cli.Block{cli.Rows{
		{"GITHUB_TOKEN", "the token pull_result reads GitHub with"},
		{"GITHUB_REPOSITORY", "the repository, when --repo is not given"},
		{ledgerTokenVariable, "the bearer ledger_pull sends"},
		{status.FileVariable, "the status file run_status reads"},
	}},
	Exits: []cli.Row{
		{"0", "the client closed the session"},
		cli.UsageExit,
		{"125", "costroid could not read --ledger, or the session failed"},
	},
	Docs: "agents",
}.String()

func mcpCommand(ctx context.Context, args []string, std stdio) int {
	set := flag.NewFlagSet("mcp", flag.ContinueOnError)
	repository := set.String("repo", os.Getenv("GITHUB_REPOSITORY"), "the repository as owner/name")
	ledgerURL := set.String("ledger", "", "the URL of a costroid serve")
	if err := parseFlags(set, args, mcpHelp, std.stdout); errors.Is(err, flag.ErrHelp) {
		return 0
	} else if err != nil {
		return cli.Fail(std.stderr, cli.Mistaken("mcp", err))
	}
	if set.NArg() > 0 {
		return cli.Fail(std.stderr, cli.Mistaken("mcp", errMCPArguments))
	}
	options := mcpserver.Options{
		Version: version,
		Getenv:  os.Getenv,
		Result:  resultTarget{repository: *repository}.newest,
		Policy:  policyAnswer,
	}
	if *ledgerURL != "" {
		ledger, err := mcpserver.NewLedger(*ledgerURL, os.Getenv(ledgerTokenVariable), ledgerRepository(*repository))
		if err != nil {
			return cli.Fail(std.stderr, err)
		}
		options.Ledger = ledger
	}
	err := mcpserver.Serve(ctx, options, std.stdin, std.stdout)
	if err != nil && !errors.Is(err, context.Canceled) {
		return cli.Fail(std.stderr, err)
	}
	return 0
}

// ledgerRepository names a GitHub repository as the ledger keys it.
func ledgerRepository(repository string) string {
	if repository == "" {
		return ""
	}
	return "github.com/" + strings.ToLower(repository)
}

// policyAnswer gives the verdict costroid policy gives, by reading the same
// flags through the same parser.
func policyAnswer(ctx context.Context, request mcpserver.PolicyRequest) (policy.Verdict, error) {
	args := []string{"--rows", request.Rows}
	for _, option := range []struct{ name, value string }{
		{"--config", request.Config},
		{"--workflow-added", request.WorkflowAdded},
		{"--at", request.At},
	} {
		if option.value != "" {
			args = append(args, option.name, option.value)
		}
	}
	flags, err := parsePolicy(args, io.Discard, io.Discard)
	if err != nil {
		return policy.Verdict{}, err
	}
	return offlineVerdict(ctx, flags)
}
