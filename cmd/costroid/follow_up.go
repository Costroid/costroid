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

func parseFollowUp(args []string, stderr io.Writer) (string, error) {
	set := flag.NewFlagSet("follow-up", flag.ContinueOnError)
	set.SetOutput(stderr)
	set.Usage = func() { fmt.Fprint(stderr, usage) }
	rows := set.String("rows", "", "the rows file the finished run kept, read as data")
	if err := set.Parse(args); err != nil {
		return "", err
	}
	if *rows == "" {
		return "", errors.New("follow-up needs --rows")
	}
	return *rows, nil
}

// followUpCommand posts the costroid check for a fork's pull request from a
// workflow_run job, which runs in the base repository with its token. It reads
// no code and runs nothing from the finished run: the rows are data, and the
// settings come from the default branch. Like report, it exits 0 once it finds
// GitHub Actions, so a failed post never fails the job.
func followUpCommand(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	rowsPath, err := parseFollowUp(args, stderr)
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
	run, head, err := forkRun(os.Getenv("GITHUB_EVENT_NAME"), os.Getenv("GITHUB_EVENT_PATH"), os.Getenv("GITHUB_REPOSITORY"))
	if err != nil {
		fmt.Fprintf(stderr, "costroid: not posted: %v\n", err)
		return 0
	}
	rows, err := readFollowUpRows(rowsPath)
	if err != nil {
		fmt.Fprintf(stderr, "costroid: warning: %v\n", err)
	}
	// Only a fork's run reaches here, so a missing rows file is a fork's.
	if rows.reason == comment.NoRowsFile {
		rows.reason = comment.ForkNoRows
	}
	input := comment.Input{
		Ledger:     rows.ledger,
		State:      rows.state,
		Reason:     rows.reason,
		ReasonLine: rows.line,
		HeadSHA:    head,
		RunURL:     finishedRunURL(run.ID),
	}
	pull := pullContext{head: head, base: os.Getenv("GITHUB_SHA")}
	host, err := hostFromEnvironment(pull)
	if err != nil {
		fmt.Fprintf(stderr, "costroid: not posted: %v\n", err)
		publish(input, false, stdout, stderr)
		return 0
	}
	if err := confirmForkPull(ctx, host, run, os.Getenv("GITHUB_REPOSITORY")); err != nil {
		fmt.Fprintf(stderr, "costroid: not posted: %v\n", err)
		return 0
	}
	input = postFollowUp(ctx, host, pull, run.Path, input, rowsPath, stderr)
	publish(input, false, stdout, stderr)
	return 0
}

// confirmForkPull binds the head commit to the one open pull request from the
// fork's branch the finished run names. A fork can push any commit it can read
// to a branch of its own, such as the head of a branch here, and a check on
// that commit would land on someone else's change. So a commit that heads a
// branch of this repository gets nothing, and so does an answer that cannot
// be read. The lookup is by branch, because GitHub's list of a commit's pull
// requests leaves out a fork's.
func confirmForkPull(ctx context.Context, host scm.Reporter, run finishedRun, repository string) error {
	heads, ok := host.(scm.ForkHeads)
	if !ok {
		return errors.New("this host cannot find a fork's pull request")
	}
	fork := run.HeadRepository.FullName
	if run.HeadBranch == "" || run.HeadRepository.Owner.Login == "" {
		return errors.New("the finished run names no head branch and owner to find its pull request by")
	}
	ctx, cancel := context.WithTimeout(ctx, settingsTimeout)
	defer cancel()
	pulls, err := heads.OpenPullsFrom(ctx, run.HeadRepository.Owner.Login, run.HeadBranch)
	if err != nil {
		return fmt.Errorf("the fork's open pull requests could not be read: %w", err)
	}
	matched := 0
	for _, pull := range pulls {
		if pull.HeadSHA == run.HeadSHA && strings.EqualFold(pull.HeadRepository, fork) && !strings.EqualFold(pull.HeadRepository, repository) {
			matched++
		}
	}
	if matched != 1 {
		return fmt.Errorf("%d open pull requests from %s have the head commit as their head, and the check needs exactly 1", matched, fork)
	}
	branches, err := heads.BranchesAt(ctx, run.HeadSHA)
	if err != nil {
		return fmt.Errorf("the branches at the head commit could not be read: %w", err)
	}
	if len(branches) > 0 {
		return errors.New("the head commit is the head of a branch of this repository")
	}
	return nil
}

// forkRun reads the finished run and returns the head commit of its fork's
// pull request. A branch of this repository gets its check from the report
// job of its own workflow, so the follow-up posts nothing for it.
func forkRun(eventName, eventPath, repository string) (finishedRun, string, error) {
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
	case strings.EqualFold(run.HeadRepository.FullName, repository):
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

// postFollowUp posts the check alone. The pull request's number is not in a
// fork's workflow_run event, and the check is what a required check reads.
func postFollowUp(ctx context.Context, host scm.Reporter, pull pullContext, workflow string, input comment.Input, rowsPath string, stderr io.Writer) comment.Input {
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
	capped := capAtNeutral(verdict)
	input.Verdict = verdict
	id, err := postCheck(ctx, host, input.HeadSHA, input.Conclusion(), inputCheck(input, forkSentences(input, capped)...), stderr)
	if err != nil {
		fmt.Fprintf(stderr, "costroid: check not posted: %v\n", err)
	}
	input.CheckRunID = id
	return input
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
