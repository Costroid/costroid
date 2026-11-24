package main

import (
	"context"
	"fmt"
	"io"
	"time"

	"github.com/Costroid/costroid/internal/cli"
	"github.com/Costroid/costroid/internal/comment"
	"github.com/Costroid/costroid/internal/policy"
	"github.com/Costroid/costroid/internal/scm"
)

// checkRender renders the output of the check a flow posts. id is the check
// run's id once the host has assigned it, and nil before; the result object
// names the check run only when id is set.
type checkRender func(id *int64) (comment.CheckOutput, error)

// postCheck posts the costroid check on head. It runs before the comment,
// because the comment names the check run's id.
func postCheck(ctx context.Context, host scm.Reporter, head, conclusion string, render checkRender, stderr io.Writer) (*int64, error) {
	output, err := render(nil)
	if err != nil {
		return nil, err
	}
	id, err := host.PostCheck(ctx, scm.CheckRun{
		Name:       comment.CheckName,
		HeadSHA:    head,
		Conclusion: conclusion,
		Title:      output.Title,
		Summary:    output.Summary,
		Text:       output.Text,
		Completed:  time.Now(),
	})
	if err != nil {
		return nil, err
	}
	cli.Say(stderr, "costroid: check run %d, conclusion %s\n", id, conclusion)
	return &id, nil
}

// nameCheck writes the check run's id into its own result object. GitHub
// assigns the id when the check is created, so it takes a second request. The
// check keeps the object it was created with when that request fails.
func nameCheck(ctx context.Context, host scm.Reporter, id int64, render checkRender, stderr io.Writer) bool {
	output, err := render(&id)
	if err == nil {
		err = host.UpdateCheck(ctx, id, scm.CheckRun{Title: output.Title, Summary: output.Summary, Text: output.Text})
	}
	if err != nil {
		fmt.Fprintf(stderr, "costroid: warning: check run %d does not name itself: %v\n", id, err)
		return false
	}
	return true
}

// inputCheck renders a report's check from its input.
func inputCheck(input comment.Input) checkRender {
	return func(id *int64) (comment.CheckOutput, error) {
		if id != nil {
			input.CheckRunID, input.IDInCheck = id, true
		}
		return comment.Check(input)
	}
}

// postInputCheck posts a report's check and names it, and returns the input
// as posted: with the check run's id, and IDInCheck when the check names
// itself. A check that is not posted ends as a line on stderr.
func postInputCheck(ctx context.Context, host scm.Reporter, input comment.Input, stderr io.Writer) comment.Input {
	render := inputCheck(input)
	id, err := postCheck(ctx, host, input.HeadSHA, input.Conclusion(), render, stderr)
	if err != nil {
		fmt.Fprintf(stderr, "costroid: check not posted: %s\n", cli.Plain(err))
		return input
	}
	input.CheckRunID = id
	input.IDInCheck = nameCheck(ctx, host, *id, render, stderr)
	return input
}

// verdictRequest is what a flow holds a run's rows to. commits names where
// the settings are read, and start reads when the warn-only period began.
// earlier is what the pull request's earlier runs cost, nil when it was not
// read, and then the pull request budget does not run. baseline is what the
// agent's earlier runs cost, nil when they were not read, and then the
// increase rules do not run. measured counts the pull request's reports with
// a measured figure, and at the twentieth the warn-only period ends.
type verdictRequest struct {
	commits  policy.Commits
	start    func(context.Context) policy.Start
	rows     rowsFile
	state    comment.State
	earlier  *policy.Lifetime
	baseline *policy.Baseline
	measured int
	// offHead says the job reads nothing at the head, as a follow-up never
	// does, so it does not compare the pack with the head's.
	offHead bool
}

// decideVerdict evaluates the policy. The rows come from the agent's job, so
// the trust path is unverified. The customer pack costroid.yml names is read
// at the commit costroid.yml was read at. The pull request's head is read only
// to say whether it changes the pack, and never by a job that keeps off it. A
// policy that cannot run leaves no verdict, and the check concludes neutral.
func decideVerdict(ctx context.Context, host scm.Reporter, request verdictRequest, stderr io.Writer) *policy.Verdict {
	ctx, cancel := context.WithTimeout(ctx, settingsTimeout)
	defer cancel()
	usage, err := reportUsage(request.rows, request.state)
	if err != nil {
		fmt.Fprintf(stderr, "costroid: warning: %v\n", err)
	}
	config := policy.ReadConfig(ctx, host, request.commits)
	pack := policy.ReadPack(ctx, host, config)
	verdict, err := policy.Evaluate(ctx, policy.Request{
		Usage:                usage,
		Trust:                policy.Unverified,
		Config:               config,
		Pack:                 pack,
		Start:                request.start(ctx),
		At:                   time.Now(),
		Baseline:             request.baseline,
		Lifetime:             request.earlier,
		MeasuredPullRequests: request.measured,
	})
	if err != nil {
		fmt.Fprintf(stderr, "costroid: warning: %v\n", err)
		return nil
	}
	if problem := verdict.ConfigProblem(); problem != nil {
		fmt.Fprintf(stderr, "costroid: warning: %v\n", problem)
	}
	warnPack(verdict, stderr)
	if !request.offHead {
		verdict.PackChange = pack.ChangedAtHead(ctx, host, config, request.commits.Head)
	}
	return &verdict
}

// warnPack names on stderr why the customer pack did not run to its end, as
// the job's log is where a reader of the pack looks.
func warnPack(verdict policy.Verdict, stderr io.Writer) {
	if problem := verdict.PackProblem(); problem != nil {
		fmt.Fprintf(stderr, "costroid: warning: %v\n", problem)
	}
}
