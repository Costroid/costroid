package main

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"maps"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/Costroid/costroid/internal/cicontext"
	"github.com/Costroid/costroid/internal/cli"
	"github.com/Costroid/costroid/internal/decimal"
	"github.com/Costroid/costroid/internal/gateway"
	"github.com/Costroid/costroid/internal/policy"
	"github.com/Costroid/costroid/internal/scm"
)

// gatewayGroup is what costroid gateway prints with no subcommand.
var gatewayGroup = cli.Group{
	Command: "gateway",
	Summary: "gateway gives one CI run its own key on a LiteLLM proxy.",
	Subcommands: []cli.Row{
		{"keygen", "draw a sealing key pair"},
		{"public-key", "print the sealing key's public half"},
		{"request", "draw the run's key and seal it"},
		{"mint", "register the run's key on the gateway"},
		{"close", "delete the run's key"},
	},
	Example: []string{"costroid gateway keygen --out sealing-key"},
}

var gatewayUsage = cli.Page{
	Command: "gateway",
	Summary: gatewayGroup.Summary,
	Usage: []string{
		"costroid gateway keygen --out FILE",
		"costroid gateway public-key",
		"costroid gateway request --out DIR",
		"costroid gateway mint --url URL --budget-usd N --lifetime-minutes N",
		"                      --max-request-tokens N [--max-parallel-requests N]",
		"                      [--repository-daily-cap-usd N]",
		"                      [--author-daily-cap-usd N] [--request FILE]",
		"                      [--for-triggering-run]",
		"costroid gateway close --url URL [--retry-after DURATION]",
		"                       [--for-triggering-run]",
	},
	Examples: []string{
		"costroid gateway keygen --out sealing-key",
		"costroid gateway public-key",
		"costroid gateway request --out gateway",
		"costroid gateway mint --url https://gateway.example --budget-usd 5 \\",
		"    --lifetime-minutes 30 --max-request-tokens 200000",
		"costroid gateway mint --for-triggering-run --url https://gateway.example \\",
		"    --budget-usd 5 --lifetime-minutes 30 --max-request-tokens 200000",
		"costroid gateway close --for-triggering-run --url https://gateway.example",
	},
	Flags: []cli.Row{
		{"--out FILE", "with keygen, the file the private half goes to; it must not exist"},
		{"--out DIR", "with request, the directory the key and the sealed request go to"},
		{"--url URL", "the gateway's URL"},
		{"--budget-usd N", "the run's budget in USD, the key's max_budget"},
		{"--lifetime-minutes N", "the key's lifetime: the agent job's timeout"},
		{"--max-request-tokens N", "the most tokens one request may hold, input and output together"},
		{"--max-parallel-requests N", "the requests the key may have in flight at once"},
		{"--repository-daily-cap-usd N", "the repository's budget per day, as a team's"},
		{"--author-daily-cap-usd N", "the author's budget per day, as a user's"},
		{"--request FILE", "a sealed request file, in place of this run's artifact"},
		{"--retry-after DURATION", "when no key is found, how long to wait for a mint that may still be running before deleting once more; the recipe runs close after mint, so it needs none"},
		{"--for-triggering-run", "mint or close the key of the run a workflow_run event follows, in place of this run's"},
	},
	Sections: []cli.Section{
		{Heading: "keygen and public-key", Body: []cli.Block{cli.Text(`keygen draws a sealing key pair at random. It writes the private half to FILE,
which must not exist, for this user alone, and prints the public half. Store
FILE's content as the secret COSTROID_GATEWAY_SEALING_KEY of the environment
the mint job names, store the public half as the repository variable
COSTROID_GATEWAY_PUBLIC_KEY, then delete FILE. Nothing about the pair comes
from the admin key.

public-key prints the public half of the sealing key in
COSTROID_GATEWAY_SEALING_KEY, to check the repository variable against it.`)}},
		{Heading: "request", Body: []cli.Block{cli.Text(`request runs in the agent's job. It draws the run's key and writes it to
DIR/key for this user alone. It writes DIR/costroid-gateway-request.json, the
key sealed to COSTROID_GATEWAY_PUBLIC_KEY for this repository, run and
attempt. It prints the key's id, never the key.`)}},
		{Heading: "mint", Body: []cli.Block{cli.Text(`mint runs in a job that holds COSTROID_GATEWAY_SEALING_KEY and
COSTROID_GATEWAY_ADMIN_KEY and never runs the agent. It opens the request, from
--request or from this run's artifact costroid-gateway-request-<attempt> with
GITHUB_TOKEN, and registers the key with the budget, an expiry, the
parallel-request limit, and the repository, pull request, head commit, run and
largest request in its metadata. It prints the key's id. When
COSTROID_INGEST_URL is set, it then attests the key to that ledger under the
job's OIDC token, for COSTROID_INGEST_AUDIENCE, which needs id-token: write.
Only an attested key lets the run's rows read exact. A ledger that takes an attestation only from a run of a protected default branch refuses mint's own run: mint then warns, and the key still holds the run to its budget.
--repository-daily-cap-usd puts the key in a team for the repository with that
budget per day, and --author-daily-cap-usd in a user for the pull request's
author with that budget per day. The pull request, its head commit and its
author come from the event file GITHUB_EVENT_PATH names. When mint cannot read
it, it warns and names the file: the key then names none of the three, and no
author's budget applies. mint refuses a key the gateway already holds.`)}},
		{Heading: "mint and close for the triggering run", Body: []cli.Block{cli.Text(`With --for-triggering-run, mint and close run in a workflow that workflow_run
starts, which runs the default branch's workflow file, so a pull request
cannot edit them. Only a key such a run attests, from a protected branch, lets
the rows read exact. The key is the one the run the event follows sealed: the
run, its attempt and its head commit come from the event file, never from
GITHUB_RUN_ID. mint refuses a run that pull_request did not start and a run
on a fork's head, reads the request from that run's artifact, and ends with no
key when the run ends without one. With GITHUB_TOKEN it reads the pull
request's author, and policy.run_budget_usd from costroid.yml at the pull
request's base, which then sets the key's budget in place of --budget-usd. An
event that names more than one pull request names none: mint warns, and the
key carries the repository's cap alone. close runs once that run has
completed, and attests the gateway's count only once it has settled. On a
default branch that GitHub's token states is not protected, the ledger
refuses both attestations: mint and close warn and go on, and the rows read
windowed.

When COSTROID_PLATFORM_URL is set as well, mint and close state the key and
its count to Costroid Platform by integration/v0.7, beside any ledger, under
the integration token in COSTROID_PLATFORM_TOKEN and the installation in
COSTROID_PLATFORM_INSTALLATION_ID. The token needs identity:read and
credentials:write. Each statement goes under the job's own OIDC token, for an
audience that names the tenant, the key, the run the key is for and its
attempt. A platform that answers 404 NOT_FOUND does not offer the statement,
and a platform that refuses the run because its branch is not protected
gives the ledger's warning: mint and close warn and go on. Any other refusal
names its code and the contract's sentence, and the step exits 1, as a
ledger's refusal does; a platform or a ledger that could not be reached exits
125. 409 INSTALLATION_NOT_BOUND warns and goes on: the rows read windowed
there until costroid connect binds the installation. A server error is sent
again under the same token every 2 s, or after Retry-After, while the send
would start within 150 s, three sends under the contract's Retry-After of
60 s. Without
--for-triggering-run, mint and close state nothing to Costroid Platform, and
warn when it is named.`)}},
		{Heading: "close", Body: []cli.Block{cli.Text(`close deletes the run's key by its alias, and prints deleted, or absent when no
key was left. Run it when the job ends, also on failure. When it finds no key,
mint may still be waiting for the request, so close waits --retry-after, no
time by default, and deletes once more. The recipe runs close after mint, so
it waits none. When COSTROID_INGEST_URL is set,
close first reads what the gateway counted on the key, waiting up to 30 seconds
for the figure to settle, and after the delete attests it to that ledger under
the job's OIDC token, for COSTROID_INGEST_AUDIENCE, which needs id-token: write.
Only an attested count lets a budget on the run's rows pass. A ledger that takes a count only from a run of a protected default branch refuses close's own run, and close warns. A figure that has
not settled by then is not attested, and close warns: the ledger keeps the
first figure it takes for a key.`)}},
		{Heading: "The gateway's URL", Body: []cli.Block{cli.Text("The gateway's URL must be https, except on localhost and 127.0.0.1.")}},
	},
	Environment: []cli.Block{cli.Rows{
		{sealingKeyVariable, "the private half of the sealing key, for public-key and mint"},
		{publicKeyVariable, "the public half, which request seals the key to"},
		{adminKeyVariable, "the gateway's admin key, for mint and close"},
		{gatewayURLVar, "the gateway's URL, when --url is not given"},
		{ingestURLVariable, "the ledger mint and close attest to"},
		{ingestAudienceVariable, "the audience of the OIDC token they attest under"},
		{platformVariable, "the Costroid Platform mint and close state the key to, with --for-triggering-run"},
		{platformTokenVariable, "its integration token"},
		{platformInstallationVariable, "the installation the statements come through, such as github-owner:7204411"},
		{"GITHUB_TOKEN", "the token mint reads this run's artifact with"},
		{"GITHUB_EVENT_PATH", "the event file mint reads the pull request from"},
		{"GITHUB_EVENT_NAME", "workflow_run, with --for-triggering-run"},
		{"GITHUB_*", "what GitHub Actions sets about the run"},
	}},
	Exits: []cli.Row{
		{"0", "done"},
		{"1", "a ledger or Costroid Platform refused a statement, and the line names its code"},
		cli.UsageExit,
		{"125", "not done, and the line on stderr says why"},
	},
	Docs: "gateway",
}.String()

const (
	adminKeyVariable   = "COSTROID_GATEWAY_ADMIN_KEY"
	sealingKeyVariable = "COSTROID_GATEWAY_SEALING_KEY"
	publicKeyVariable  = "COSTROID_GATEWAY_PUBLIC_KEY"
	gatewayURLVar      = "COSTROID_GATEWAY_URL"
	gatewayKeyFileVar  = "COSTROID_GATEWAY_KEY_FILE"
	// The ingest job's variables. mint attests to the same ledger.
	ingestURLVariable      = "COSTROID_INGEST_URL"
	ingestAudienceVariable = "COSTROID_INGEST_AUDIENCE"
	keyFileName            = "key"
	requestArtifact        = "costroid-gateway-request-"
	artifactWait           = 10 * time.Minute
	artifactEvery          = 5 * time.Second
	defaultParallel        = 1
	gatewayKeyWait         = 10 * time.Minute
	gatewayProbeEvery      = 2 * time.Second
	gatewayPoll            = 2 * time.Second
	// LiteLLM writes spend to its store every 10 to 15 seconds by default:
	// PROXY_BATCH_WRITE_AT, 10, in litellm/constants.py, plus a random 0 to 5
	// drawn at startup in initialize_scheduled_background_jobs in
	// litellm/proxy/proxy_server.py, at v1.108.1. The settle bound covers two
	// writes.
	gatewaySpendSettle = 30 * time.Second
	gatewaySpendStable = 12 * time.Second
)

func gatewayCommand(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprint(stdout, gatewayGroup)
		return exitUsage
	}
	var err error
	switch args[0] {
	case "keygen":
		err = gatewayKeygen(args[1:], stdout, stderr)
	case "public-key":
		err = gatewayPublicKey(args[1:], stdout, stderr)
	case "request":
		err = gatewayRequest(args[1:], stdout, stderr)
	case "mint":
		err = gatewayMint(ctx, args[1:], stdout, stderr)
	case "close":
		err = gatewayClose(ctx, args[1:], stdout, stderr)
	case "help", "-h", "--help":
		fmt.Fprint(stdout, gatewayUsage)
		return 0
	default:
		err = gatewayGroup.Unknown(args[0])
	}
	switch {
	case errors.Is(err, flag.ErrHelp):
		return 0
	case errors.Is(err, errStatementRefused):
		return exitRejected
	case refusedByLedger(err):
		cli.Explain(stderr, err)
		return exitRejected
	case err != nil:
		return cli.Fail(stderr, err)
	}
	return 0
}

func adminKey() (string, error) {
	key := os.Getenv(adminKeyVariable)
	if key == "" {
		return "", fmt.Errorf("%s is not set", adminKeyVariable)
	}
	return key, nil
}

// sealingKey reads the sealing key's private half. A job set up before keygen
// has none, and is told to run it.
func sealingKey() (string, error) {
	key := os.Getenv(sealingKeyVariable)
	if key == "" {
		return "", fmt.Errorf("%s is not set: run costroid gateway keygen, store the private half as that environment secret and the public half as the variable %s", sealingKeyVariable, publicKeyVariable)
	}
	return key, nil
}

func gatewayKeygen(args []string, stdout, stderr io.Writer) error {
	set := flag.NewFlagSet("gateway keygen", flag.ContinueOnError)
	out := set.String("out", "", "the file the private half goes to; it must not exist")
	if err := parseFlags(set, args, gatewayUsage, stdout, stderr); err != nil {
		return err
	}
	if err := cli.NoArgument(set); err != nil {
		return err
	}
	if *out == "" {
		return cli.Mistaken("gateway", errors.New("gateway keygen needs --out: the private half goes to a file, never to the terminal"))
	}
	private, public, err := gateway.NewSealingKey()
	if err != nil {
		return err
	}
	file, err := os.OpenFile(*out, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return fmt.Errorf("write the sealing key: %w", err)
	}
	if _, err := file.WriteString(private + "\n"); err != nil {
		file.Close()
		return fmt.Errorf("write the sealing key: %w", err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("write the sealing key: %w", err)
	}
	fmt.Fprintln(stdout, public)
	return nil
}

func gatewayPublicKey(args []string, stdout, stderr io.Writer) error {
	set := flag.NewFlagSet("gateway public-key", flag.ContinueOnError)
	if err := parseFlags(set, args, gatewayUsage, stdout, stderr); err != nil {
		return err
	}
	if err := cli.NoArgument(set); err != nil {
		return err
	}
	private, err := sealingKey()
	if err != nil {
		return err
	}
	public, err := gateway.PublicKey(strings.TrimSpace(private))
	if err != nil {
		return err
	}
	fmt.Fprintln(stdout, public)
	return nil
}

func gatewayRequest(args []string, stdout, stderr io.Writer) error {
	set := flag.NewFlagSet("gateway request", flag.ContinueOnError)
	out := set.String("out", "", "the directory the key and the sealed request go to")
	if err := parseFlags(set, args, gatewayUsage, stdout, stderr); err != nil {
		return err
	}
	if err := cli.NoArgument(set); err != nil {
		return err
	}
	public := os.Getenv(publicKeyVariable)
	if *out == "" || public == "" {
		needs := fmt.Errorf("gateway request needs --out and %s, the public half costroid gateway keygen printed", publicKeyVariable)
		if *out == "" {
			return cli.Mistaken("gateway", needs)
		}
		return needs
	}
	binding, err := gateway.ReadBinding(os.Getenv)
	if err != nil {
		return err
	}
	request, key, err := gateway.NewRequest(strings.TrimSpace(public), binding)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(*out, 0o700); err != nil {
		return fmt.Errorf("create %s: %w", *out, err)
	}
	if err := os.WriteFile(filepath.Join(*out, keyFileName), []byte(key), 0o600); err != nil {
		return fmt.Errorf("write the key: %w", err)
	}
	data, err := json.Marshal(request)
	if err != nil {
		return fmt.Errorf("encode the request: %w", err)
	}
	if err := os.WriteFile(filepath.Join(*out, gateway.RequestFile), append(data, '\n'), 0o644); err != nil {
		return fmt.Errorf("write the request: %w", err)
	}
	fmt.Fprintln(stdout, request.KeyID)
	return nil
}

type mintFlags struct {
	url, request  string
	budget        decimal.Decimal
	lifetime      time.Duration
	parallel      int
	requestTokens int64
	caps          gateway.DailyCaps
	triggering    bool
}

func parseMint(args []string, stdout, stderr io.Writer) (mintFlags, error) {
	set := flag.NewFlagSet("gateway mint", flag.ContinueOnError)
	address := set.String("url", os.Getenv(gatewayURLVar), "the gateway's URL")
	budget := set.String("budget-usd", "", "the run's budget in USD, the key's max_budget")
	minutes := set.Int("lifetime-minutes", 0, "the key's lifetime: the agent job's timeout")
	parallel := set.Int("max-parallel-requests", defaultParallel, "the requests the key may have in flight at once")
	tokens := set.Int64("max-request-tokens", 0, "the most tokens one request may hold, input and output together")
	repository := set.String("repository-daily-cap-usd", "", "the repository's budget per day, as a team's")
	author := set.String("author-daily-cap-usd", "", "the author's budget per day, as a user's")
	request := set.String("request", "", "a sealed request file, in place of this run's artifact")
	triggering := set.Bool("for-triggering-run", false, "mint the key of the run a workflow_run event follows")
	if err := parseFlags(set, args, gatewayUsage, stdout, stderr); err != nil {
		return mintFlags{}, err
	}
	if err := cli.NoArgument(set); err != nil {
		return mintFlags{}, err
	}
	if *address == "" || *budget == "" || *minutes <= 0 || *tokens <= 0 {
		return mintFlags{}, cli.Mistaken("gateway", errors.New("gateway mint needs --url, --budget-usd, --lifetime-minutes and --max-request-tokens"))
	}
	if err := gateway.CheckURL(*address); err != nil {
		return mintFlags{}, err
	}
	amount, err := decimal.Parse(*budget)
	if err != nil {
		return mintFlags{}, cli.Mistaken("gateway", fmt.Errorf("--budget-usd: %w", err))
	}
	flags := mintFlags{
		url: *address, request: *request, budget: amount,
		lifetime: time.Duration(*minutes) * time.Minute, parallel: *parallel, requestTokens: *tokens, triggering: *triggering,
	}
	if flags.caps.Repository, err = dailyCap("repository-daily-cap-usd", *repository); err != nil {
		return mintFlags{}, cli.Mistaken("gateway", err)
	}
	if flags.caps.Author, err = dailyCap("author-daily-cap-usd", *author); err != nil {
		return mintFlags{}, cli.Mistaken("gateway", err)
	}
	return flags, nil
}

// dailyCap reads one cap. Empty sets none.
func dailyCap(name, text string) (*decimal.Decimal, error) {
	if text == "" {
		return nil, nil
	}
	amount, err := decimal.Parse(text)
	if err != nil || amount.Sign() <= 0 {
		return nil, fmt.Errorf("--%s must be a decimal above 0", name)
	}
	return &amount, nil
}

func gatewayMint(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	flags, err := parseMint(args, stdout, stderr)
	if err != nil {
		return err
	}
	admin, err := adminKey()
	if err != nil {
		return err
	}
	sealing, err := sealingKey()
	if err != nil {
		return err
	}
	target, err := readMintTarget(ctx, flags, stderr)
	if errors.Is(err, gateway.ErrNoRequest) {
		cli.Say(stderr, "run %s ended with no sealed request, so no key is minted\n", target.run.Run)
		return nil
	}
	if err != nil {
		return err
	}
	mint := gateway.Mint{
		Client: gateway.NewClient(flags.url), AdminKey: admin, SealingKey: strings.TrimSpace(sealing), Run: target.run, Request: target.request,
		Budget: target.budget, Lifetime: flags.lifetime, MaxParallel: flags.parallel, MaxRequestTokens: flags.requestTokens,
		PullRequest: target.pullRequest, Head: target.head, Author: target.author, Caps: flags.caps,
	}
	id, err := mint.Register(ctx)
	if err != nil {
		return err
	}
	fmt.Fprintln(stdout, id)
	client := asking(&http.Client{Timeout: 30 * time.Second}, stderr)
	warned, ledger := attestKey(ctx, client, gateway.AttestationFor(id, target.run, target.head), flags.triggering, stderr)
	stating, err := platformFor(flags.triggering, client, keyWrite, stderr)
	if stating != nil {
		stating.warned = warned
		err = stating.stateKey(ctx, id, target.run, target.head)
	}
	return both(stderr, ledger, err)
}

// mintTarget is the run a key is minted for, its sealed request, and what the
// key's budget and metadata read from it.
type mintTarget struct {
	run                       gateway.Binding
	request                   gateway.Request
	head, pullRequest, author string
	budget                    decimal.Decimal
}

// readMintTarget reads the run a key is for: this run, or with
// --for-triggering-run the run a workflow_run event follows.
func readMintTarget(ctx context.Context, flags mintFlags, stderr io.Writer) (mintTarget, error) {
	if flags.triggering {
		return readTriggeringTarget(ctx, flags, stderr)
	}
	binding, err := gateway.ReadBinding(os.Getenv)
	if err != nil {
		return mintTarget{}, err
	}
	request, err := readMintRequest(ctx, flags.request, binding, stderr)
	if err != nil {
		return mintTarget{run: binding}, err
	}
	ci, author := mintEvent(os.Getenv, stderr)
	return mintTarget{run: binding, request: request, head: ci.Head, pullRequest: ci.PullRequest, author: author, budget: flags.budget}, nil
}

// readTriggeringTarget reads the run a workflow_run event follows. The run,
// its attempt and its head come from the event; the request from that run's
// artifact. With one pull request and GITHUB_TOKEN, the author is the pull
// request's, and the budget is policy.run_budget_usd at its base when
// costroid.yml sets one there. Anything it cannot read leaves the author
// out, or the budget at --budget-usd, with a warning.
func readTriggeringTarget(ctx context.Context, flags mintFlags, stderr io.Writer) (mintTarget, error) {
	run, err := gateway.ReadTriggeringRun(os.Getenv)
	if err != nil {
		return mintTarget{}, err
	}
	target := mintTarget{run: run.Binding, head: run.Head, pullRequest: run.PullRequest, budget: flags.budget}
	target.request, err = readMintRequest(ctx, flags.request, run.Binding, stderr)
	if err != nil {
		return target, err
	}
	if run.PullRequest == "" {
		fmt.Fprintf(stderr, "costroid: warning: the event names %d pull requests, so the key names none, no author's budget applies, and the key's budget is --budget-usd\n", run.PullRequests)
		return target, nil
	}
	host, err := triggeringHost()
	if err != nil {
		fmt.Fprintf(stderr, "costroid: warning: %v, so no author's budget applies and the key's budget is --budget-usd\n", err)
		return target, nil
	}
	number, _ := strconv.Atoi(run.PullRequest)
	if pull, err := host.PullRequest(ctx, number); err != nil {
		fmt.Fprintf(stderr, "costroid: warning: pull request %s could not be read, so no author's budget applies: %v\n", run.PullRequest, err)
	} else {
		target.author = pull.Author
	}
	target.budget = baseBudget(ctx, host, run.Base, flags.budget, stderr)
	return target, nil
}

// triggeringHost reads the repository through GITHUB_TOKEN, outside any
// service's egress allowlist, so its client is a plain one.
func triggeringHost() (*scm.GitHub, error) {
	token := os.Getenv("GITHUB_TOKEN")
	if token == "" {
		return nil, errors.New("GITHUB_TOKEN is not set")
	}
	return scm.NewGitHub(scm.GitHubOptions{
		API:        os.Getenv("GITHUB_API_URL"),
		Repository: os.Getenv("GITHUB_REPOSITORY"),
		Tokens:     scm.StaticToken(token),
		Client:     &http.Client{Timeout: 30 * time.Second},
	})
}

// runBudgetKey is the policy key whose amount a run is held to.
const runBudgetKey = "run_budget_usd"

// baseBudget is policy.run_budget_usd in costroid.yml at the pull request's
// base, the file a pull request cannot change, or fallback when the base sets
// none or cannot be read.
func baseBudget(ctx context.Context, host *scm.GitHub, base string, fallback decimal.Decimal, stderr io.Writer) decimal.Decimal {
	config := policy.ReadConfig(ctx, host, policy.Commits{Base: base, PullRequest: true})
	if config.Unread != nil {
		fmt.Fprintf(stderr, "costroid: warning: the key's budget is --budget-usd, because costroid.yml at the base could not be read: %v\n", config.Unread)
		return fallback
	}
	if !config.Found {
		return fallback
	}
	settings, err := policy.ParseSettings(config.Data)
	if err != nil {
		fmt.Fprintf(stderr, "costroid: warning: the key's budget is --budget-usd, because costroid.yml at the base does not parse: %v\n", err)
		return fallback
	}
	if budget, set := settings.Amounts[runBudgetKey]; set {
		return budget
	}
	return fallback
}

// unprotectedKey and unprotectedCount say what a ledger's refusal of a job
// that is not a protected run's means, and what to do, for a job of the
// agent's own workflow. unprotectedBranchKey and unprotectedBranchCount say
// it for a run of the gateway recipe on a default branch that GitHub's token
// states is not protected, as on a private repository on GitHub Free. The key
// is registered, holds the run to its budget and is deleted either way.
const (
	unprotectedKey         = "costroid: warning: the ledger takes an attestation only from a run of a protected default branch, so this run's rows read windowed: mint and close the run's key with recipes/github-gateway.yml, as docs/gateway.md shows\n"
	unprotectedCount       = "costroid: warning: the ledger takes a gateway count only from a run of a protected default branch, so no budget on this run's rows passes on it: mint and close the run's key with recipes/github-gateway.yml, as docs/gateway.md shows\n"
	unprotectedBranchKey   = "costroid: warning: this run is on the default branch and GitHub's token says the branch is not protected, so the rows of the run it serves read windowed: protect the default branch, which a private repository can on GitHub Pro or Team and not on GitHub Free, as docs/gateway.md says\n"
	unprotectedBranchCount = "costroid: warning: this run is on the default branch and GitHub's token says the branch is not protected, so no budget on the rows of the run it serves passes on the gateway's count: protect the default branch, which a private repository can on GitHub Pro or Team and not on GitHub Free, as docs/gateway.md says\n"
)

// warnUnprotected prints the warning for a ledger's refusal of a run no
// protected branch holds, the one for a run of the gateway recipe or the one
// for a job of the agent's own workflow.
func warnUnprotected(stderr io.Writer, triggering bool, branch, own string) {
	if triggering {
		fmt.Fprint(stderr, branch)
		return
	}
	fmt.Fprint(stderr, own)
}

// attestKey tells the ledger the key was minted for a run. Without a ledger
// there is nothing to tell. A failure fails the step, and the key stays
// registered for the agent: a refusal the ledger answered exits 1, and a
// ledger that could not be reached 125. A ledger that takes only a protected
// run's word refuses a job of the agent's own workflow, and a run of a default
// branch no rule protects: that refusal alone warns and goes on, so the run
// completes at the windowed grade, the true reading. It reports whether it
// gave that warning.
func attestKey(ctx context.Context, client *http.Client, attestation gateway.Attestation, triggering bool, stderr io.Writer) (bool, error) {
	ingest := os.Getenv(ingestURLVariable)
	if ingest == "" {
		return false, nil
	}
	target, err := gateway.CredentialsURL(ingest)
	if err != nil {
		return false, err
	}
	token, err := gateway.ActionsToken(ctx, client, os.Getenv, os.Getenv(ingestAudienceVariable))
	if err != nil {
		return false, err
	}
	err = gateway.Attest(ctx, client, target, token, attestation)
	if gateway.Unprotected(err) {
		warnUnprotected(stderr, triggering, unprotectedBranchKey, unprotectedKey)
		return true, nil
	}
	if err != nil {
		return false, ledgerFailure(fmt.Errorf("the key is registered and not attested, so the run's rows cannot read exact: %w", err))
	}
	cli.Say(stderr, "attested to the ledger\n")
	return false, nil
}

// ledgerRefusal is a statement the ledger took and refused. The step names
// it and exits 1, as for Costroid Platform's refusal.
type ledgerRefusal struct{ error }

func (r ledgerRefusal) Unwrap() error { return r.error }

// ledgerFailure marks err a ledgerRefusal when the ledger answered it, and
// leaves it as it is when the ledger could not be reached.
func ledgerFailure(err error) error {
	var refused *gateway.Refusal
	if errors.As(err, &refused) {
		return ledgerRefusal{err}
	}
	return err
}

func readMintRequest(ctx context.Context, path string, binding gateway.Binding, stderr io.Writer) (gateway.Request, error) {
	if path != "" {
		file, err := os.Open(path)
		if err != nil {
			return gateway.Request{}, err
		}
		defer file.Close()
		return gateway.ReadRequest(file)
	}
	token := os.Getenv("GITHUB_TOKEN")
	if token == "" {
		return gateway.Request{}, errors.New("gateway mint reads this run's artifact with GITHUB_TOKEN, and it is not set; or pass --request")
	}
	artifacts := gateway.Artifacts{
		API:        cmp.Or(os.Getenv("GITHUB_API_URL"), "https://api.github.com"),
		Repository: os.Getenv("GITHUB_REPOSITORY"),
		Run:        binding.Run,
		Token:      token,
		HTTP:       asking(&http.Client{Timeout: 30 * time.Second}, stderr),
		Every:      artifactEvery,
	}
	waitCtx, cancel := context.WithTimeout(ctx, artifactWait)
	defer cancel()
	return artifacts.Wait(waitCtx, requestArtifact+binding.Attempt)
}

// mintEvent reads the pull request, its head and its author from the run's
// event file. A file that cannot be read leaves all three out of the key and
// lets no author's budget apply, so mint warns, naming the file, and goes on:
// the key still holds the run to its own budget.
func mintEvent(getenv func(string) string, stderr io.Writer) (cicontext.Context, string) {
	path := getenv("GITHUB_EVENT_PATH")
	ci, err := cicontext.Read(getenv)
	if err != nil {
		fmt.Fprintf(stderr, "costroid: warning: the event file %q could not be read, so the key names no pull request, head commit or author, and no author's budget applies: %v\n", path, err)
		return ci, ""
	}
	return ci, eventAuthor(path)
}

// eventAuthor reads the pull request's author from the event file, or nothing.
func eventAuthor(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	var event struct {
		PullRequest struct {
			User struct {
				Login string `json:"login"`
			} `json:"user"`
		} `json:"pull_request"`
	}
	if json.Unmarshal(data, &event) != nil {
		return ""
	}
	return event.PullRequest.User.Login
}

func gatewayClose(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	set := flag.NewFlagSet("gateway close", flag.ContinueOnError)
	address := set.String("url", os.Getenv(gatewayURLVar), "the gateway's URL")
	retry := set.Duration("retry-after", 0, "when no key is found, how long to wait for a mint that may still be running before deleting once more; the recipe runs close after mint, so it needs none")
	triggering := set.Bool("for-triggering-run", false, "close the key of the run a workflow_run event follows")
	if err := parseFlags(set, args, gatewayUsage, stdout, stderr); err != nil {
		return err
	}
	if err := cli.NoArgument(set); err != nil {
		return err
	}
	if *address == "" {
		return cli.Mistaken("gateway", errors.New("gateway close needs --url"))
	}
	if err := gateway.CheckURL(*address); err != nil {
		return err
	}
	admin, err := adminKey()
	if err != nil {
		return err
	}
	binding, err := closingRun(*triggering)
	if err != nil {
		return err
	}
	counter := gateway.Counter{
		Client: gateway.NewClient(*address), AdminKey: admin, Run: binding,
		Poll: gatewayPoll, Stable: gatewaySpendStable, Settle: gatewaySpendSettle,
	}
	return closeKey(ctx, counter, *retry, &http.Client{Timeout: 30 * time.Second}, *triggering, stdout, stderr)
}

// closingRun is the run whose key close deletes: this run, or the run a
// workflow_run event follows.
func closingRun(triggering bool) (gateway.Binding, error) {
	if !triggering {
		return gateway.ReadBinding(os.Getenv)
	}
	run, err := gateway.ReadTriggeringRun(os.Getenv)
	return run.Binding, err
}

// closeKey deletes the run's key, and when a ledger or Costroid Platform is
// named, reads the key's spend before the delete and states it to each after,
// through client. triggering says the key is the run's a workflow_run event
// follows, the one run whose count Costroid Platform takes.
func closeKey(ctx context.Context, counter gateway.Counter, retry time.Duration, client *http.Client, triggering bool, stdout, stderr io.Writer) error {
	ledger := os.Getenv(ingestURLVariable) != ""
	counted := ledger || triggering && os.Getenv(platformVariable) != ""
	count, unread := readCount(ctx, counter, counted, stderr)
	deleted, err := gateway.Close(ctx, counter.Client, counter.AdminKey, counter.Run)
	if err == nil && !deleted {
		cli.Say(stderr, "no key yet: waiting %s for a late mint, then deleting once more\n", retry)
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(retry):
		}
		count, unread = readCount(ctx, counter, counted, stderr)
		deleted, err = gateway.Close(ctx, counter.Client, counter.AdminKey, counter.Run)
	}
	if err != nil {
		return err
	}
	if deleted {
		fmt.Fprintln(stdout, "deleted")
	} else {
		fmt.Fprintln(stdout, "absent")
	}
	if unread != nil {
		return fmt.Errorf("the key's spend was not read before the delete, so the ledger holds no gateway count for this run: %w", unread)
	}
	stating, err := platformFor(triggering, client, countWrite, stderr)
	if count == nil {
		return err
	}
	var attested error
	var warned bool
	if ledger {
		warned, attested = attestSpend(ctx, client, *count, counter.Run, triggering, stderr)
	}
	if stating != nil {
		stating.warned = warned
		err = stating.stateSpend(ctx, *count, counter.Run)
	}
	return both(stderr, attested, err)
}

// readCount reads what the gateway counted on the run's key, when a ledger or
// Costroid Platform is named to attest it to. It answers nil when neither is
// named or no key is there. A ledger keeps the first figure it takes for a key
// and never a later one, and so does the platform, so a figure that may not
// have settled is not attested: close warns and sends none.
func readCount(ctx context.Context, counter gateway.Counter, counted bool, stderr io.Writer) (*gateway.Count, error) {
	if !counted {
		return nil, nil
	}
	count, found, err := counter.Settled(ctx)
	if errors.Is(err, gateway.ErrUnsettled) {
		fmt.Fprintf(stderr, "costroid: warning: %v. A ledger keeps the first figure it takes for a key, so close attests none, and no budget on the run's rows can pass on the gateway's count\n", err)
		return nil, nil
	}
	if err != nil || !found {
		return nil, err
	}
	return &count, nil
}

// attestSpend tells the ledger what the gateway counted on the run's key. A
// failure fails the step, as attestKey's does. The key is deleted by then. A
// refusal of a run no protected branch holds warns and goes on, as attestKey's
// does, and it reports whether it gave that warning.
func attestSpend(ctx context.Context, client *http.Client, count gateway.Count, binding gateway.Binding, triggering bool, stderr io.Writer) (bool, error) {
	target, err := gateway.SpendURL(os.Getenv(ingestURLVariable), count.KeyID)
	if err != nil {
		return false, err
	}
	token, err := gateway.ActionsToken(ctx, client, os.Getenv, os.Getenv(ingestAudienceVariable))
	if err != nil {
		return false, err
	}
	attestation := gateway.SpendFor(count, binding)
	err = gateway.AttestSpend(ctx, client, target, token, attestation)
	if gateway.Unprotected(err) {
		warnUnprotected(stderr, triggering, unprotectedBranchCount, unprotectedCount)
		return true, nil
	}
	if err != nil {
		return false, ledgerFailure(fmt.Errorf("the key is deleted and the gateway's count is not attested, so no budget on the run's rows can pass on it: %w", err))
	}
	cli.Say(stderr, "attested the gateway's count of USD %s to the ledger\n", attestation.Spend)
	return false, nil
}

// Costroid Platform takes a key's attestation and its count by
// integration/v0.7, from the run that workflow_run started, under the tenant's
// integration token, the installation and the run's own ID token.
const (
	platformInstallationVariable = "COSTROID_PLATFORM_INSTALLATION_ID"
	// notMounted is the code of the one answer a platform gives for a path
	// it does not mount: a version it does not speak, or a closed door.
	notMounted = "NOT_FOUND"
)

// statementPace is how often a statement Costroid Platform answered with a
// server error, or never answered, is sent again, and for how long. The
// platform answers INSTALLATION_PROOF_KEYS_UNAVAILABLE with a Retry-After of
// 60 s and asks for the same token again inside its window of 300 s, so the
// bound is 150 s: the write goes three times at that pace, and half the
// window is left.
var statementPace = verdictPace{interval: 2 * time.Second, limit: 150 * time.Second}

// retryAfterSecond is one second of a Retry-After.
var retryAfterSecond = time.Second

// errStatementRefused is a statement Costroid Platform took and refused. Its
// line is printed already, and the step exits 1.
var errStatementRefused = errors.New("the statement was refused by Costroid Platform")

// credentialWrite is one of the two writes mint and close make to Costroid
// Platform, with what each prints.
type credentialWrite struct {
	// what names the write in a refusal's line.
	what string
	// workflowRun warns that a run workflow_run did not start states none.
	workflowRun string
	// notOffered warns of a platform that does not mount the write.
	notOffered string
	// unprotected warns of the refusal of a run no protected branch holds,
	// in the words of the ledger's.
	unprotected string
	// unstated says what a write that could not be made leaves undone.
	unstated string
	// sentences are the contract's sentences for each refusal of the write.
	sentences map[string]string
}

// statementSentences are the contract's sentences for the refusals both
// writes share, and keyWrite and countWrite add the two that differ.
var statementSentences = map[string]string{
	"AUTH_TOKEN_INVALID":                  "The token is absent, unknown, expired or revoked",
	"AUTH_TOKEN_SCOPE":                    "The token holds no scope of this version, or lacks credentials:write",
	"RUN_TOKEN_OWNER_MISMATCH":            "The installation is not a GitHub one, or the run token names another owner than the installation",
	"INSTALLATION_PROOF_KEYS_UNAVAILABLE": "GitHub's key set cannot be read. Retry inside the run token's window",
	"CREDENTIAL_ATTESTING_RUN_USED":       "This attesting repository, run and attempt holds another key, or this key with another run, attempt or head",
	"CREDENTIAL_ATTESTED_ELSEWHERE":       "The key is held by another attesting run. The first one stands",
	"CREDENTIAL_NOT_ATTESTED":             "The key holds no attestation for this run and attempt, so no figure is taken",
	"CREDENTIAL_SPEND_HELD":               "The key holds another decimal string",
}

var (
	keyWrite = credentialWrite{
		what:        "the key's attestation",
		workflowRun: "costroid: warning: Costroid Platform takes a key's attestation only from a run that workflow_run started, so mint stated none to it: mint and close the run's key with recipes/github-gateway.yml, as docs/gateway.md shows\n",
		notOffered:  "costroid: warning: Costroid Platform answered 404 NOT_FOUND to the key's attestation: it speaks no integration/v0.7, or its integration paths are shut, so the rows of the run the key serves read windowed there\n",
		unprotected: unprotectedBranchKey,
		unstated:    "the key is registered and not attested to Costroid Platform, so the run's rows cannot read exact there",
		sentences: withSentences(map[string]string{
			"TOOL_INPUT_INVALID": "The key id in the path is malformed, a header is missing or malformed, or the body is not an attestation of this version",
			"RUN_TOKEN_INVALID":  "The run token is not valid for this attestation's audience",
		}),
	}
	countWrite = credentialWrite{
		what:        "the gateway's count",
		workflowRun: "costroid: warning: Costroid Platform takes a gateway count only from a run that workflow_run started, so close stated none to it: mint and close the run's key with recipes/github-gateway.yml, as docs/gateway.md shows\n",
		notOffered:  "costroid: warning: Costroid Platform answered 404 NOT_FOUND to the gateway's count: it speaks no integration/v0.7, or its integration paths are shut, so no budget on the rows of the run the key serves passes on the count there\n",
		unprotected: unprotectedBranchCount,
		unstated:    "the key is deleted and the gateway's count is not stated to Costroid Platform, so no budget on the run's rows can pass on it there",
		sentences: withSentences(map[string]string{
			"TOOL_INPUT_INVALID": "The key id in the path is malformed, a header is missing or malformed, or the body is not a spend of this version",
			"RUN_TOKEN_INVALID":  "The run token is not valid for this spend's audience",
		}),
	}
)

// withSentences is the sentences both writes share, with a write's own.
func withSentences(own map[string]string) map[string]string {
	sentences := maps.Clone(statementSentences)
	maps.Copy(sentences, own)
	return sentences
}

// keyPlatform is Costroid Platform as mint and close state a key to it.
type keyPlatform struct {
	target       platform
	installation string
	pace         verdictPace
	// second is how long one second of Retry-After waits.
	second time.Duration
	// warned says a ledger already gave the warning for a run no protected
	// branch holds, which the platform's same refusal does not repeat.
	warned bool
	stderr io.Writer
}

// platformFor reads the Costroid Platform COSTROID_PLATFORM_URL names, its
// integration token and the installation the statements come through. It
// answers nil when no platform is named, and when the run is not one
// workflow_run started, which warns: the platform takes no other run's
// statement. The client follows no redirect, so the tokens reach only the
// origin named.
func platformFor(triggering bool, client *http.Client, write credentialWrite, stderr io.Writer) (*keyPlatform, error) {
	address := os.Getenv(platformVariable)
	switch {
	case address == "":
		return nil, nil
	case !triggering:
		fmt.Fprint(stderr, write.workflowRun)
		return nil, nil
	}
	origin, err := platformOrigin(address)
	if err != nil {
		return nil, errors.New("COSTROID_PLATFORM_URL must be an https origin, such as https://app.costroid.com, or http on a loopback address")
	}
	token := os.Getenv(platformTokenVariable)
	if !integrationTokenPattern.MatchString(token) {
		return nil, errors.New("COSTROID_PLATFORM_TOKEN is not an integration token, which starts costroid_it_: create one on Costroid Platform")
	}
	installation, err := exportInstallation(os.Getenv(platformInstallationVariable))
	if err != nil {
		return nil, errors.New("COSTROID_PLATFORM_INSTALLATION_ID must name the installation Costroid Platform bound, such as github-owner:7204411, as recipes/github-gateway.yml sets it")
	}
	following := *client
	following.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &keyPlatform{
		target:       platform{client: &following, origin: origin, token: token},
		installation: installation,
		pace:         statementPace,
		second:       retryAfterSecond,
		stderr:       stderr,
	}, nil
}

// stateKey attests to the platform that the key is for the run at the head.
func (k *keyPlatform) stateKey(ctx context.Context, keyID string, run gateway.Binding, head string) error {
	statement, err := gateway.NewKeyStatement(keyID, run, head)
	if err != nil {
		return fmt.Errorf("%s: %w", keyWrite.unstated, err)
	}
	audience := func(tenant string) string { return gateway.KeyAudience(tenant, keyID, run) }
	return k.state(ctx, keyWrite, "/credentials/"+keyID, audience, statement, "")
}

// stateSpend states to the platform what the gateway counted on the run's key.
func (k *keyPlatform) stateSpend(ctx context.Context, count gateway.Count, run gateway.Binding) error {
	statement, err := gateway.NewSpendStatement(count, run)
	if err != nil {
		return fmt.Errorf("%s: %w", countWrite.unstated, err)
	}
	audience := func(tenant string) string { return gateway.SpendAudience(tenant, count.KeyID, run) }
	return k.state(ctx, countWrite, "/credentials/"+count.KeyID+"/spend", audience, statement, statement.Spend)
}

// state reads the tenant from GET /identity of integration/v0.7, asks GitHub
// for the job's ID token under the write's audience, sends the write, and
// prints what came of it. It answers nil when the platform took the write,
// and when it does not mount the path or refused a run no protected branch
// holds, each of which warns and lets the step go on; errStatementRefused
// when it refused the write otherwise; and the error when the platform or
// GitHub could not be reached.
func (k *keyPlatform) state(ctx context.Context, write credentialWrite, path string, audience func(string) string, body any, spend string) error {
	data, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("%s: %w", write.unstated, err)
	}
	tenant, err := k.target.identityUnder(ctx, contractV07)
	if err == nil {
		k.target.under(contractV07)
		var token string
		if token, err = gateway.ActionsToken(ctx, k.target.client, os.Getenv, audience(tenant)); err != nil {
			return fmt.Errorf("%s: %w", write.unstated, err)
		}
		var status int
		if status, data, err = k.put(ctx, write, path, data, token); err == nil {
			k.taken(status, data, spend)
			return nil
		}
	}
	return k.refused(write, err)
}

// put sends the write, and sends it again under the same token on a server
// error or a lost connection, after the seconds Retry-After names or the
// pace's interval, whichever is longer, while the send after the wait would
// start inside the pace's limit. The token lives 300 s and the limit is
// 150 s, so a write sent again is inside its window, and the contract answers
// the same write the same way.
func (k *keyPlatform) put(ctx context.Context, write credentialWrite, path string, body []byte, token string) (int, []byte, error) {
	deadline := time.Now().Add(k.pace.limit)
	for {
		request, err := k.target.request(ctx, http.MethodPut, path, bytes.NewReader(body))
		if err != nil {
			return 0, nil, err
		}
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("Costroid-Installation-Id", k.installation)
		request.Header.Set("Costroid-Run-Token", token)
		response, data, err := k.target.do(request, write.what)
		var refused *refusal
		switch {
		case err == nil:
			return response.StatusCode, data, nil
		case errors.As(err, &refused) && refused.status < http.StatusInternalServerError:
			return 0, nil, err
		}
		wait := k.pace.interval
		if errors.As(err, &refused) {
			if seconds, parsed := strconv.Atoi(refused.retryAfter); parsed == nil && time.Duration(seconds)*k.second > wait {
				wait = time.Duration(seconds) * k.second
			}
		}
		if time.Now().Add(wait).After(deadline) {
			return 0, nil, err
		}
		select {
		case <-ctx.Done():
			return 0, nil, err
		case <-time.After(wait):
		}
	}
}

// statedBy is the part of a stored attestation or spend that names the run
// that stated it.
type statedBy struct {
	AttestedBy struct {
		Ref string `json:"ref"`
	} `json:"attested_by"`
}

// branch is the branch of the run that stated a write, as the platform read
// it from that run's token, or empty when the answer names none git admits.
func (s statedBy) branch() string {
	name, found := strings.CutPrefix(s.AttestedBy.Ref, "refs/heads/")
	if !found || !branchName.MatchString(name) {
		return ""
	}
	return s.AttestedBy.Ref
}

// taken prints a write the platform took: 201 stored it, and 200 held it
// already. spend is the figure a count stated, and empty for an attestation.
func (k *keyPlatform) taken(status int, data []byte, spend string) {
	var stored statedBy
	by := ""
	if json.Unmarshal(data, &stored) == nil && stored.branch() != "" {
		by = stored.branch()
	}
	switch {
	case spend == "" && status == http.StatusCreated && by != "":
		cli.Say(k.stderr, "attested to Costroid Platform by a run of %s\n", by)
	case spend == "" && status == http.StatusCreated:
		cli.Say(k.stderr, "attested to Costroid Platform\n")
	case spend == "" && by != "":
		cli.Say(k.stderr, "Costroid Platform held this attestation already, from a run of %s, so nothing changed\n", by)
	case spend == "":
		cli.Say(k.stderr, "Costroid Platform held this attestation already, so nothing changed\n")
	case status == http.StatusCreated && by != "":
		cli.Say(k.stderr, "attested the gateway's count of USD %s to Costroid Platform by a run of %s\n", spend, by)
	case status == http.StatusCreated:
		cli.Say(k.stderr, "attested the gateway's count of USD %s to Costroid Platform\n", spend)
	case by != "":
		cli.Say(k.stderr, "Costroid Platform held the gateway's count of USD %s already, from a run of %s, so nothing changed\n", spend, by)
	default:
		cli.Say(k.stderr, "Costroid Platform held the gateway's count of USD %s already, so nothing changed\n", spend)
	}
}

// refused prints a write the platform did not take, and answers the error the
// step ends on. A platform that does not mount the path, a refusal of a run
// no protected branch holds, and one of an installation the tenant has not
// bound, warn and end on none. Every other refusal is
// printed with its code and the contract's sentence for it, and anything
// else is an error of the job's.
func (k *keyPlatform) refused(write credentialWrite, err error) error {
	var refused *refusal
	if !errors.As(err, &refused) {
		return fmt.Errorf("%s: %w", write.unstated, err)
	}
	sentence, known := write.sentences[refused.code]
	switch {
	case refused.status == http.StatusNotFound && refused.code == notMounted:
		fmt.Fprint(k.stderr, write.notOffered)
		return nil
	case refused.status == http.StatusForbidden && refused.code == gateway.CodeRunUnprotected:
		if !k.warned {
			fmt.Fprint(k.stderr, write.unprotected)
		}
		return nil
	case refused.unbound():
		fmt.Fprintf(k.stderr, "costroid: warning: %v. The tenant holds no binding of %s, so the rows of the run the key serves read windowed there until it is bound: run costroid connect, as docs/connect.md shows\n", err, k.installation)
		return nil
	case known && refused.what == write.what:
		fmt.Fprintf(k.stderr, "costroid: %v. %s. Nothing was stored\n", err, sentence)
	default:
		fmt.Fprintf(k.stderr, "costroid: %v\n", err)
	}
	return errStatementRefused
}

// both is the error a step ends on that told a ledger and Costroid Platform
// of a key: the worse of the two, with the other explained first. A service
// that could not be reached is worse than one that refused. A refusal the
// platform answered is printed already.
func both(stderr io.Writer, ledger, platform error) error {
	switch {
	case ledger == nil:
		return platform
	case platform == nil:
		return ledger
	case refusedByLedger(ledger) && !errors.Is(platform, errStatementRefused):
		cli.Explain(stderr, ledger)
		return platform
	case !errors.Is(platform, errStatementRefused):
		cli.Explain(stderr, platform)
	}
	return ledger
}

// refusedByLedger reports a statement a ledger took and refused.
func refusedByLedger(err error) bool {
	var refused ledgerRefusal
	return errors.As(err, &refused)
}
