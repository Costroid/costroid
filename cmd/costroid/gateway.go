package main

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/Costroid/costroid/internal/cicontext"
	"github.com/Costroid/costroid/internal/decimal"
	"github.com/Costroid/costroid/internal/gateway"
)

const gatewayUsage = `costroid gateway gives one CI run its own key on a LiteLLM proxy.

Usage:
  costroid gateway public-key
  costroid gateway request --out DIR
  costroid gateway mint --url URL --budget-usd N --lifetime-minutes N --max-request-tokens N [--max-parallel-requests N]
                        [--repository-daily-cap-usd N] [--author-daily-cap-usd N] [--request FILE]
  costroid gateway close --url URL

public-key prints the public half of the sealing key, which comes from the
admin key in COSTROID_GATEWAY_ADMIN_KEY. Store it as a repository variable.

request runs in the agent's job. It draws the run's key, writes it to DIR/key
for this user alone, and writes DIR/` + gateway.RequestFile + `, the key sealed to
COSTROID_GATEWAY_PUBLIC_KEY for this repository, run and attempt. It prints the
key's id, never the key.

mint runs in a job that holds COSTROID_GATEWAY_ADMIN_KEY and never runs the
agent. It opens the request, from --request or from this run's artifact
costroid-gateway-request-<attempt> with GITHUB_TOKEN, and registers the key
with the budget, an expiry, the parallel-request limit, and the repository,
pull request, head commit, run and largest request in its metadata. It prints
the key's id.
--repository-daily-cap-usd puts the key in a team for the repository with that
budget per day, and --author-daily-cap-usd in a user for the pull request's
author with that budget per day.

close deletes the run's key by its alias, and prints deleted, or absent when no
key was left. Run it when the job ends, also on failure.

Exit codes: 0 done, 125 not done.
`

const (
	adminKeyVariable  = "COSTROID_GATEWAY_ADMIN_KEY"
	publicKeyVariable = "COSTROID_GATEWAY_PUBLIC_KEY"
	gatewayURLVar     = "COSTROID_GATEWAY_URL"
	gatewayKeyFileVar = "COSTROID_GATEWAY_KEY_FILE"
	keyFileName       = "key"
	requestArtifact   = "costroid-gateway-request-"
	artifactWait      = 10 * time.Minute
	artifactEvery     = 5 * time.Second
	defaultParallel   = 1
	gatewayKeyWait    = 10 * time.Minute
	gatewayProbeEvery = 2 * time.Second
)

func gatewayCommand(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprint(stderr, gatewayUsage)
		return exitFailure
	}
	var err error
	switch args[0] {
	case "public-key":
		err = gatewayPublicKey(stdout)
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
		err = fmt.Errorf("unknown gateway command %q", args[0])
	}
	if errors.Is(err, flag.ErrHelp) {
		return 0
	}
	if err != nil {
		fmt.Fprintf(stderr, "costroid: %v\n", err)
		return exitFailure
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

func gatewayPublicKey(stdout io.Writer) error {
	admin, err := adminKey()
	if err != nil {
		return err
	}
	public, err := gateway.PublicKey(admin)
	if err != nil {
		return err
	}
	fmt.Fprintln(stdout, public)
	return nil
}

func gatewayFlags(name string, stderr io.Writer) *flag.FlagSet {
	set := flag.NewFlagSet("gateway "+name, flag.ContinueOnError)
	set.SetOutput(stderr)
	set.Usage = func() { fmt.Fprint(stderr, gatewayUsage) }
	return set
}

func gatewayRequest(args []string, stdout, stderr io.Writer) error {
	set := gatewayFlags("request", stderr)
	out := set.String("out", "", "the directory the key and the sealed request go to")
	if err := set.Parse(args); err != nil {
		return err
	}
	public := os.Getenv(publicKeyVariable)
	if *out == "" || public == "" {
		return fmt.Errorf("gateway request needs --out and %s", publicKeyVariable)
	}
	binding, err := gateway.ReadBinding(os.Getenv)
	if err != nil {
		return err
	}
	request, key, err := gateway.NewRequest(public, binding)
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
}

func parseMint(args []string, stderr io.Writer) (mintFlags, error) {
	set := gatewayFlags("mint", stderr)
	address := set.String("url", os.Getenv(gatewayURLVar), "the gateway's URL")
	budget := set.String("budget-usd", "", "the run's budget in USD, the key's max_budget")
	minutes := set.Int("lifetime-minutes", 0, "the key's lifetime: the agent job's timeout")
	parallel := set.Int("max-parallel-requests", defaultParallel, "the requests the key may have in flight at once")
	tokens := set.Int64("max-request-tokens", 0, "the most tokens one request may hold, input and output together")
	repository := set.String("repository-daily-cap-usd", "", "the repository's budget per day, as a team's")
	author := set.String("author-daily-cap-usd", "", "the author's budget per day, as a user's")
	request := set.String("request", "", "a sealed request file, in place of this run's artifact")
	if err := set.Parse(args); err != nil {
		return mintFlags{}, err
	}
	if *address == "" || *budget == "" || *minutes <= 0 || *tokens <= 0 {
		return mintFlags{}, errors.New("gateway mint needs --url, --budget-usd, --lifetime-minutes and --max-request-tokens")
	}
	amount, err := decimal.Parse(*budget)
	if err != nil {
		return mintFlags{}, fmt.Errorf("--budget-usd: %w", err)
	}
	flags := mintFlags{
		url: *address, request: *request, budget: amount,
		lifetime: time.Duration(*minutes) * time.Minute, parallel: *parallel, requestTokens: *tokens,
	}
	if flags.caps.Repository, err = dailyCap("repository-daily-cap-usd", *repository); err != nil {
		return mintFlags{}, err
	}
	if flags.caps.Author, err = dailyCap("author-daily-cap-usd", *author); err != nil {
		return mintFlags{}, err
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
	flags, err := parseMint(args, stderr)
	if err != nil {
		return err
	}
	admin, err := adminKey()
	if err != nil {
		return err
	}
	binding, err := gateway.ReadBinding(os.Getenv)
	if err != nil {
		return err
	}
	request, err := readMintRequest(ctx, flags.request, binding)
	if err != nil {
		return err
	}
	ci, _ := cicontext.Read(os.Getenv)
	mint := gateway.Mint{
		Client: gateway.NewClient(flags.url), AdminKey: admin, Run: binding, Request: request,
		Budget: flags.budget, Lifetime: flags.lifetime, MaxParallel: flags.parallel, MaxRequestTokens: flags.requestTokens,
		PullRequest: ci.PullRequest, Head: ci.Head, Author: eventAuthor(os.Getenv("GITHUB_EVENT_PATH")), Caps: flags.caps,
	}
	id, err := mint.Register(ctx)
	if err != nil {
		return err
	}
	fmt.Fprintln(stdout, id)
	return nil
}

func readMintRequest(ctx context.Context, path string, binding gateway.Binding) (gateway.Request, error) {
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
		HTTP:       &http.Client{Timeout: 30 * time.Second},
		Every:      artifactEvery,
	}
	waitCtx, cancel := context.WithTimeout(ctx, artifactWait)
	defer cancel()
	return artifacts.Wait(waitCtx, requestArtifact+binding.Attempt)
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
	set := gatewayFlags("close", stderr)
	address := set.String("url", os.Getenv(gatewayURLVar), "the gateway's URL")
	if err := set.Parse(args); err != nil {
		return err
	}
	if *address == "" {
		return errors.New("gateway close needs --url")
	}
	admin, err := adminKey()
	if err != nil {
		return err
	}
	binding, err := gateway.ReadBinding(os.Getenv)
	if err != nil {
		return err
	}
	deleted, err := gateway.Close(ctx, gateway.NewClient(*address), admin, binding)
	if err != nil {
		return err
	}
	if deleted {
		fmt.Fprintln(stdout, "deleted")
	} else {
		fmt.Fprintln(stdout, "absent")
	}
	return nil
}
