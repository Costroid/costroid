package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"

	"github.com/Costroid/costroid/internal/cicontext"
	"github.com/Costroid/costroid/internal/comment"
	"github.com/Costroid/costroid/internal/policy"
	"github.com/Costroid/costroid/internal/scm"
)

const (
	// maxFollowUpRows caps the rows file a follow-up reads. A fork's workflow
	// wrote it, so its size is the fork's choice.
	maxFollowUpRows = 8 << 20
	followUpTrigger = "workflow_run"
)

const forkNoRowsSentence = "Reason: `" + string(comment.ForkNoRows) + "`. This pull request comes from a fork, and its workflow run left no rows. " +
	"The recipes run no agent on a fork's pull request. A neutral check does not block a merge."

// forkCapSentence ends the summary of a check the cap held at neutral.
const forkCapSentence = "This pull request comes from a fork, and the fork's own run wrote these rows. " +
	"Its check concludes neutral at most, so a rule that fails here warns and does not block a merge."

var errNotGitHubActions = errors.New("follow-up refused: GitHub Actions was not detected")

// finishedRun is the part of a workflow_run event the follow-up reads. GitHub
// writes the event, so the head commit and the head repository are GitHub's,
// never the pull request's.
type finishedRun struct {
	ID             int64  `json:"id"`
	Event          string `json:"event"`
	HeadSHA        string `json:"head_sha"`
	HeadBranch     string `json:"head_branch"`
	Path           string `json:"path"`
	HeadRepository *struct {
		FullName string `json:"full_name"`
		Owner    struct {
			Login string `json:"login"`
		} `json:"owner"`
	} `json:"head_repository"`
}

// followUpFlags are what follow-up is given.
type followUpFlags struct {
	rows string
	// ingested says this job wrote the rows with costroid ingest-run from the
	// finished run's artifact, for a workflow with no report job of its own.
	ingested bool
}

func parseFollowUp(args []string, stdout, stderr io.Writer) (followUpFlags, error) {
	set := flag.NewFlagSet("follow-up", flag.ContinueOnError)
	set.SetOutput(stderr)
	rows := set.String("rows", "", "the rows file the finished run kept, read as data")
	ingested := set.Bool("ingested", false, "the rows are ones costroid ingest-run wrote in this job, for a workflow with no report job")
	if err := parseFlags(set, args, followUpHelp, stdout, stderr); err != nil {
		return followUpFlags{}, err
	}
	if *rows == "" {
		return followUpFlags{}, errors.New("follow-up needs --rows")
	}
	return followUpFlags{rows: *rows, ingested: *ingested}, nil
}

// followUpCommand posts the costroid check for a fork's pull request from a
// workflow_run job, which runs in the base repository with its token. It reads
// no code and runs nothing from the finished run: the rows are data, and the
// settings come from the default branch. With --ingested it also posts for a
// branch of this repository, because a workflow such as gh-aw's has no report
// job of its own, and it reads the run's object beside the rows, which this
// job's ingest-run wrote. Like report, it exits 0 once it finds GitHub Actions,
// so a failed post never fails the job.
func followUpCommand(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	flags, err := parseFollowUp(args, stdout, stderr)
	if errors.Is(err, flag.ErrHelp) {
		return 0
	}
	if err != nil {
		fmt.Fprintf(stderr, "costroid: %v\n", err)
		return exitFailure
	}
	if cicontext.Detect(os.Getenv) != cicontext.GitHubActions {
		fmt.Fprintf(stderr, "costroid: %v\n", errNotGitHubActions)
		return exitFailure
	}
	repository := os.Getenv("GITHUB_REPOSITORY")
	run, head, err := finishedPullRun(os.Getenv("GITHUB_EVENT_NAME"), os.Getenv("GITHUB_EVENT_PATH"), repository, flags.ingested)
	if err != nil {
		fmt.Fprintf(stderr, "costroid: not posted: %v\n", err)
		return 0
	}
	fork := !strings.EqualFold(run.HeadRepository.FullName, repository)
	input := followUpInput(flags, fork, stderr)
	input.HeadSHA, input.RunURL = head, finishedRunURL(run.ID)
	pull := pullContext{head: head, base: os.Getenv("GITHUB_SHA")}
	host, err := hostFromEnvironment(pull)
	if err != nil {
		fmt.Fprintf(stderr, "costroid: not posted: %v\n", err)
		publish(input, false, stdout, stderr)
		return 0
	}
	confirm := confirmForkPull
	if !fork {
		confirm = confirmBranchPull
	}
	if _, err := confirm(ctx, host, run, repository); err != nil {
		fmt.Fprintf(stderr, "costroid: not posted: %v\n", err)
		return 0
	}
	input = postFollowUp(ctx, host, pull, run.Path, input, flags.rows, fork, stderr)
	publish(input, false, stdout, stderr)
	return 0
}

// followUpInput is what the rows say. A fork's missing rows file is a fork's
// run that left none. Rows ingest-run wrote in this job come with the run's
// object, as report reads it: the reason, the agent's error and the agent's
// own figure.
func followUpInput(flags followUpFlags, fork bool, stderr io.Writer) comment.Input {
	rows, err := readFollowUpRows(flags.rows)
	if err != nil {
		fmt.Fprintf(stderr, "costroid: warning: %v\n", err)
	}
	if fork && rows.reason == comment.NoRowsFile {
		rows.reason = comment.ForkNoRows
	}
	input := comment.Input{
		Ledger:     rows.ledger,
		State:      rows.state,
		Reason:     rows.reason,
		ReasonLine: rows.line,
	}
	if flags.ingested {
		stated := readRunReason(flags.rows, stderr)
		input.RunReason = runReasonFor(rows.state, stated.RunReason, stderr)
		input.ReaderLine = stated.ReaderError
		input.AgentError = agentErrorFor(stated.AgentError, stderr)
		input.AgentStated = agentStatedFor(stated.AgentStated, stderr)
	}
	return input
}

// confirmBranchPull binds the head commit to the one open pull request from
// the branch of this repository the finished run names, whose head is that
// commit, and returns its number.
func confirmBranchPull(ctx context.Context, host scm.Reporter, run finishedRun, repository string) (int, error) {
	heads, ok := host.(scm.ForkHeads)
	if !ok {
		return 0, errors.New("this host cannot find a branch's pull request")
	}
	if run.HeadBranch == "" || run.HeadRepository.Owner.Login == "" {
		return 0, errors.New("the finished run names no head branch and owner to find its pull request by")
	}
	ctx, cancel := context.WithTimeout(ctx, settingsTimeout)
	defer cancel()
	pulls, err := heads.OpenPullsFrom(ctx, run.HeadRepository.Owner.Login, run.HeadBranch)
	if err != nil {
		return 0, fmt.Errorf("the branch's open pull requests could not be read: %w", err)
	}
	var matched []int
	for _, pull := range pulls {
		if pull.HeadSHA == run.HeadSHA && strings.EqualFold(pull.HeadRepository, repository) {
			matched = append(matched, pull.Number)
		}
	}
	if len(matched) != 1 {
		return 0, fmt.Errorf("%d open pull requests from %s have the head commit as their head, and the check needs exactly 1", len(matched), run.HeadBranch)
	}
	return matched[0], nil
}

// confirmForkPull binds the head commit to the one open pull request from the
// fork's branch the finished run names. A fork can push any commit it can read
// to a branch of its own, such as the head of a branch here, and a check on
// that commit would land on someone else's change. So a commit that heads a
// branch of this repository gets nothing, and so does an answer that cannot
// be read. The lookup is by branch, because GitHub's list of a commit's pull
// requests leaves out a fork's. It returns the pull request's number.
func confirmForkPull(ctx context.Context, host scm.Reporter, run finishedRun, repository string) (int, error) {
	heads, ok := host.(scm.ForkHeads)
	if !ok {
		return 0, errors.New("this host cannot find a fork's pull request")
	}
	fork := run.HeadRepository.FullName
	if run.HeadBranch == "" || run.HeadRepository.Owner.Login == "" {
		return 0, errors.New("the finished run names no head branch and owner to find its pull request by")
	}
	ctx, cancel := context.WithTimeout(ctx, settingsTimeout)
	defer cancel()
	pulls, err := heads.OpenPullsFrom(ctx, run.HeadRepository.Owner.Login, run.HeadBranch)
	if err != nil {
		return 0, fmt.Errorf("the fork's open pull requests could not be read: %w", err)
	}
	var matched []int
	for _, pull := range pulls {
		if pull.HeadSHA == run.HeadSHA && strings.EqualFold(pull.HeadRepository, fork) && !strings.EqualFold(pull.HeadRepository, repository) {
			matched = append(matched, pull.Number)
		}
	}
	if len(matched) != 1 {
		return 0, fmt.Errorf("%d open pull requests from %s have the head commit as their head, and the check needs exactly 1", len(matched), fork)
	}
	branches, err := heads.BranchesAt(ctx, run.HeadSHA)
	if err != nil {
		return 0, fmt.Errorf("the branches at the head commit could not be read: %w", err)
	}
	if len(branches) > 0 {
		return 0, errors.New("the head commit is the head of a branch of this repository")
	}
	return matched[0], nil
}

// finishedPullRun reads the finished run and returns the head commit of its
// pull request. A branch of this repository gets its check from the report
// job of its own workflow, so the follow-up posts nothing for it, unless the
// rows were ingested here for a workflow that has no report job.
func finishedPullRun(eventName, eventPath, repository string, branches bool) (finishedRun, string, error) {
	if eventName != followUpTrigger {
		return finishedRun{}, "", errors.New("follow-up runs on the workflow_run event alone")
	}
	run, err := readFinishedRun(eventPath)
	if err != nil {
		return run, "", err
	}
	switch {
	case run.Event != "pull_request":
		return run, "", errors.New("the finished run was not started by a pull request")
	case run.HeadRepository == nil || run.HeadRepository.FullName == "":
		return run, "", errors.New("the finished run names no head repository")
	case !branches && strings.EqualFold(run.HeadRepository.FullName, repository):
		return run, "", errors.New("the pull request is from a branch of this repository, and its own report job posts the check")
	case !headPattern.MatchString(run.HeadSHA):
		return run, "", errors.New("no head commit: the event file names none")
	}
	return run, run.HeadSHA, nil
}

func readFinishedRun(path string) (finishedRun, error) {
	if path == "" {
		return finishedRun{}, cicontext.ErrNoEventFile
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return finishedRun{}, fmt.Errorf("read the event file: %w", err)
	}
	var event struct {
		WorkflowRun *finishedRun `json:"workflow_run"`
	}
	if err := json.Unmarshal(data, &event); err != nil {
		return finishedRun{}, fmt.Errorf("parse the event file: %w", err)
	}
	if event.WorkflowRun == nil {
		return finishedRun{}, errors.New("the event file holds no workflow_run")
	}
	return *event.WorkflowRun, nil
}

// readFollowUpRows refuses a file past the cap before it reads a byte, then
// reads the rest as report does: every string from the rows passes the
// comment's allowlist and length cap.
func readFollowUpRows(path string) (rowsReading, error) {
	info, err := os.Lstat(path)
	if err == nil && info.Mode().IsRegular() && info.Size() > maxFollowUpRows {
		return rowsReading{state: comment.Unreadable, reason: comment.UnreadRowsFile},
			fmt.Errorf("the rows file holds %d bytes, over the follow-up's cap of %d, so it was not read", info.Size(), maxFollowUpRows)
	}
	return readReportRows(path)
}

// finishedRunURL links the run the agent's workflow made, not the follow-up's.
func finishedRunURL(id int64) string {
	server, repository := os.Getenv("GITHUB_SERVER_URL"), os.Getenv("GITHUB_REPOSITORY")
	if server == "" || repository == "" || id <= 0 {
		return ""
	}
	return strings.TrimRight(server, "/") + "/" + repository + "/actions/runs/" + strconv.FormatInt(id, 10)
}

// postFollowUp posts the check alone, and names the check run in its own
// result object as report does. The pull request's number is not in a fork's
// workflow_run event, and the check is what a required check reads. A fork's
// verdict is held at neutral; a branch's stands as report's does.
func postFollowUp(ctx context.Context, host scm.Reporter, pull pullContext, workflow string, input comment.Input, rowsPath string, fork bool, stderr io.Writer) comment.Input {
	ctx, cancel := context.WithTimeout(ctx, reportTimeout)
	defer cancel()
	input.Amounts = resolveAmounts(ctx, host, pull.base, stderr)
	// The settings come from the default branch's commit, the one the
	// follow-up runs from, and the warn-only period starts from the agent's
	// workflow file on that branch.
	verdict := decideVerdict(ctx, host, verdictRequest{
		commits: policy.Commits{Base: pull.base, Head: pull.head, PullRequest: true},
		start: func(ctx context.Context) policy.Start {
			return finishedWorkflowStart(ctx, host, workflow, pull.base, stderr)
		},
		rowsPath: rowsPath,
		state:    input.State,
	}, stderr)
	capped := fork && capAtNeutral(verdict)
	input.Verdict = verdict
	return postInputCheck(ctx, host, input, stderr, forkSentences(input, capped)...)
}

// capAtNeutral holds a fork's verdict at neutral and reports whether it moved
// it. The recipes run no agent on a fork's pull request, so a fork's rows are
// the fork's own statement: they neither block a merge nor pass a budget.
func capAtNeutral(verdict *policy.Verdict) bool {
	if verdict == nil || verdict.Conclusion == policy.Neutral {
		return false
	}
	verdict.Conclusion = policy.Neutral
	return true
}

// finishedWorkflowStart reads the history of the workflow file the finished
// run came from. The event's path can carry @ref, which the history does not
// need.
func finishedWorkflowStart(ctx context.Context, host scm.Reporter, workflow, base string, stderr io.Writer) policy.Start {
	history, ok := host.(policy.History)
	if !ok {
		return policy.Start{}
	}
	repository := os.Getenv("GITHUB_REPOSITORY")
	file, _, _ := strings.Cut(workflow, "@")
	path, err := policy.WorkflowPath(repository+"/"+file+"@"+base, repository)
	if err != nil {
		fmt.Fprintf(stderr, "costroid: warning: %v\n", err)
		return policy.Start{}
	}
	start, err := policy.ReadStart(ctx, history, path, base)
	if err != nil {
		fmt.Fprintf(stderr, "costroid: warning: %v\n", err)
	}
	return start
}

// forkSentences end the summary of a fork's check: the reason when the fork's
// run left no rows, and the cap when it held the verdict at neutral.
func forkSentences(input comment.Input, capped bool) []string {
	var sentences []string
	if input.Reason == comment.ForkNoRows {
		sentences = append(sentences, forkNoRowsSentence)
	}
	if capped {
		sentences = append(sentences, forkCapSentence)
	}
	return sentences
}
