package main

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"regexp"
	"time"

	"github.com/Costroid/costroid/internal/comment"
	"github.com/Costroid/costroid/internal/decimal"
	"github.com/Costroid/costroid/internal/scm"
)

const (
	configFile           = "costroid.yml"
	installationVariable = "COSTROID_INSTALLATION_ID"
	// reportTimeout bounds every request of one report together, so a stalled
	// host costs the job half a minute at most.
	reportTimeout = 30 * time.Second
	// settingsTimeout leaves the check time to post after a stalled read.
	settingsTimeout = 8 * time.Second
)

var headPattern = regexp.MustCompile(`^[0-9a-f]{40}([0-9a-f]{24})?$`)

type reportFlags struct {
	rows         string
	budget       *decimal.Decimal
	installation string
}

func parseReport(args []string, stderr io.Writer) (reportFlags, error) {
	set := flag.NewFlagSet("report", flag.ContinueOnError)
	set.SetOutput(stderr)
	set.Usage = func() { fmt.Fprint(stderr, usage) }
	rows := set.String("rows", "", "the rows file costroid run wrote")
	budget := set.String("budget-usd", "", "the budget the run was given, in USD")
	installation := set.String("installation-id", os.Getenv(installationVariable), "the GitHub App installation id")
	if err := set.Parse(args); err != nil {
		return reportFlags{}, err
	}
	if *rows == "" {
		return reportFlags{}, errors.New("report needs --rows")
	}
	flags := reportFlags{rows: *rows, installation: *installation}
	if *budget != "" {
		amount, err := decimal.Parse(*budget)
		if err != nil {
			return reportFlags{}, fmt.Errorf("--budget-usd: %w", err)
		}
		flags.budget = &amount
	}
	return flags, nil
}

// pullContext is what the CI environment says about the commit.
type pullContext struct {
	head        string
	base        string
	pullRequest int
}

// readPullContext prefers the event file. On a pull request GITHUB_SHA names
// the test merge commit, and the check belongs on the head.
func readPullContext() (pullContext, error) {
	pull := pullContext{head: os.Getenv("GITHUB_SHA")}
	path := os.Getenv("GITHUB_EVENT_PATH")
	if path == "" {
		return pull, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return pull, fmt.Errorf("read the event file: %w", err)
	}
	var event struct {
		PullRequest *struct {
			Number int `json:"number"`
			Head   struct {
				SHA string `json:"sha"`
			} `json:"head"`
			Base struct {
				SHA string `json:"sha"`
			} `json:"base"`
		} `json:"pull_request"`
	}
	if err := json.Unmarshal(data, &event); err != nil {
		return pull, fmt.Errorf("parse the event file: %w", err)
	}
	if event.PullRequest != nil {
		pull.pullRequest = event.PullRequest.Number
		pull.head = cmp.Or(event.PullRequest.Head.SHA, pull.head)
		pull.base = event.PullRequest.Base.SHA
	}
	return pull, nil
}

// settingsRef is the commit costroid.yml is read from. On a pull request that
// is the base, so a pull request cannot change its own setting.
func (p pullContext) settingsRef() string {
	if p.pullRequest > 0 {
		return p.base
	}
	return p.head
}

func readReportRows(path string) (comment.Ledger, comment.State, error) {
	file, err := os.Open(path)
	if err != nil {
		return comment.Ledger{}, comment.Unreadable, err
	}
	defer file.Close()
	ledger, err := comment.ReadRows(file)
	if err != nil {
		return comment.Ledger{}, comment.Unreadable, err
	}
	if ledger.Rows == 0 {
		return ledger, comment.NoRows, nil
	}
	return ledger, comment.Reported, nil
}

// reportCommand exits 0 once its flags parse. A report is never why a job
// fails, so every failure below ends as a line on stderr.
func reportCommand(ctx context.Context, args []string, stderr io.Writer) int {
	flags, err := parseReport(args, stderr)
	if errors.Is(err, flag.ErrHelp) {
		return 0
	}
	if err != nil {
		fmt.Fprintf(stderr, "costroid: %v\n", err)
		return exitFailure
	}
	ledger, state, err := readReportRows(flags.rows)
	if err != nil {
		fmt.Fprintf(stderr, "costroid: warning: %v\n", err)
	}
	pull, err := readPullContext()
	if err != nil {
		fmt.Fprintf(stderr, "costroid: warning: %v\n", err)
	}
	host, err := hostFromEnvironment(pull)
	if err != nil {
		fmt.Fprintf(stderr, "costroid: not posted: %v\n", err)
		return 0
	}
	ctx, cancel := context.WithTimeout(ctx, reportTimeout)
	defer cancel()
	input := comment.Input{
		Ledger:       ledger,
		State:        state,
		HeadSHA:      pull.head,
		Budget:       flags.budget,
		Installation: flags.installation,
	}
	input.Amounts = resolveAmounts(ctx, host, pull.settingsRef(), stderr)
	input.CheckRunID = postCheck(ctx, host, input, stderr)
	if pull.pullRequest > 0 {
		postComment(ctx, host, input, pull.pullRequest, stderr)
	}
	return 0
}

func hostFromEnvironment(pull pullContext) (scm.Host, error) {
	token := os.Getenv("GITHUB_TOKEN")
	if token == "" {
		return nil, errors.New("GITHUB_TOKEN is not set")
	}
	if !headPattern.MatchString(pull.head) {
		return nil, errors.New("no head commit: GITHUB_SHA and the event file name none")
	}
	return scm.NewGitHub(scm.GitHubOptions{
		API:        os.Getenv("GITHUB_API_URL"),
		Repository: os.Getenv("GITHUB_REPOSITORY"),
		Tokens:     scm.StaticToken(token),
	})
}

// postCheck runs before the comment, because the comment names its id.
func postCheck(ctx context.Context, host scm.Host, input comment.Input, stderr io.Writer) *int64 {
	output, err := comment.Check(input)
	if err != nil {
		fmt.Fprintf(stderr, "costroid: check not posted: %v\n", err)
		return nil
	}
	id, err := host.PostCheck(ctx, scm.CheckRun{
		Name:       comment.CheckName,
		HeadSHA:    input.HeadSHA,
		Conclusion: comment.Conclusion,
		Title:      output.Title,
		Summary:    output.Summary,
		Text:       output.Text,
		Completed:  time.Now(),
	})
	if err != nil {
		fmt.Fprintf(stderr, "costroid: check not posted: %v\n", err)
		return nil
	}
	fmt.Fprintf(stderr, "costroid: check run %d, conclusion %s\n", id, comment.Conclusion)
	return &id
}

// resolveAmounts applies decision L19. When the setting or the visibility
// cannot be read, a repository not known to be private withholds amounts. It
// spends part of the report's time at most, so the check still posts.
func resolveAmounts(ctx context.Context, host scm.Host, ref string, stderr io.Writer) comment.Amounts {
	ctx, cancel := context.WithTimeout(ctx, settingsTimeout)
	defer cancel()
	config, err := readConfig(ctx, host, ref)
	if err != nil {
		fmt.Fprintf(stderr, "costroid: warning: %s: %v\n", configFile, err)
	}
	if err == nil && config.ShowCost {
		return comment.AmountsShown
	}
	visibility, visibilityErr := host.Visibility(ctx)
	switch {
	case visibilityErr != nil:
		fmt.Fprintf(stderr, "costroid: warning: repository visibility: %v\n", visibilityErr)
		return comment.AmountsUnsettled
	case visibility == scm.Private:
		return comment.AmountsShown
	case err != nil:
		return comment.AmountsUnsettled
	}
	return comment.AmountsOff
}

func readConfig(ctx context.Context, host scm.Host, ref string) (comment.Config, error) {
	if !headPattern.MatchString(ref) {
		return comment.Config{}, errors.New("no commit to read it from")
	}
	data, err := host.ReadFile(ctx, configFile, ref)
	if errors.Is(err, scm.ErrNotFound) {
		return comment.ParseConfig(nil)
	}
	if err != nil {
		return comment.Config{}, err
	}
	return comment.ParseConfig(data)
}

func postComment(ctx context.Context, host scm.Host, input comment.Input, pullRequest int, stderr io.Writer) {
	body, err := comment.Comment(input)
	if err != nil {
		fmt.Fprintf(stderr, "costroid: comment not posted: %v\n", err)
		return
	}
	if err := host.UpsertComment(ctx, pullRequest, comment.Marker, body); err != nil {
		fmt.Fprintf(stderr, "costroid: comment not posted: %v\n", err)
		return
	}
	fmt.Fprintf(stderr, "costroid: comment on pull request %d\n", pullRequest)
}
