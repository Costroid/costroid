package main

import (
	"context"
	"fmt"
	"io"
	"time"

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
	fmt.Fprintf(stderr, "costroid: check run %d, conclusion %s\n", id, conclusion)
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

// inputCheck renders a report's check from its input. Each sentence in after
// ends the summary as a paragraph of its own.
func inputCheck(input comment.Input, after ...string) checkRender {
	return func(id *int64) (comment.CheckOutput, error) {
		if id != nil {
			input.CheckRunID, input.IDInCheck = id, true
		}
		output, err := comment.Check(input)
		if err != nil {
			return comment.CheckOutput{}, err
		}
		for _, sentence := range after {
			output.Summary += "\n\n" + sentence
		}
		return output, nil
	}
}

// postInputCheck posts a report's check and names it, and returns the input
// as posted: with the check run's id, and IDInCheck when the check names
// itself. A check that is not posted ends as a line on stderr.
func postInputCheck(ctx context.Context, host scm.Reporter, input comment.Input, stderr io.Writer, after ...string) comment.Input {
	render := inputCheck(input, after...)
	id, err := postCheck(ctx, host, input.HeadSHA, input.Conclusion(), render, stderr)
	if err != nil {
		fmt.Fprintf(stderr, "costroid: check not posted: %v\n", err)
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
	rowsPath string
	state    comment.State
	earlier  *policy.Lifetime
	baseline *policy.Baseline
	measured int
}

// decideVerdict evaluates the policy. The rows come from the agent's job, so
// the trust path is unverified. A policy that cannot run leaves no verdict,
// and the check concludes neutral.
func decideVerdict(ctx context.Context, host scm.Reporter, request verdictRequest, stderr io.Writer) *policy.Verdict {
	ctx, cancel := context.WithTimeout(ctx, settingsTimeout)
	defer cancel()
	usage, err := reportUsage(request.rowsPath, request.state)
	if err != nil {
		fmt.Fprintf(stderr, "costroid: warning: %v\n", err)
	}
	verdict, err := policy.Evaluate(ctx, policy.Request{
		Usage:                usage,
		Trust:                policy.Unverified,
		Config:               policy.ReadConfig(ctx, host, request.commits),
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
	return &verdict
}
