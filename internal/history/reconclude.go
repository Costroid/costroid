package history

import (
	"context"
	"errors"
	"time"

	"github.com/Costroid/costroid/internal/comment"
	"github.com/Costroid/costroid/internal/scm"
)

// ErrConcluded reports a newest costroid check that already concludes as an
// approval would make it. Nothing is posted.
var ErrConcluded = errors.New("history: the newest costroid check already concludes so")

// Rechecker is what Reconclude needs from the host: the commit's check runs,
// and a check run to post and to name.
type Rechecker interface {
	scm.History
	PostCheck(ctx context.Context, check scm.CheckRun) (int64, error)
	UpdateCheck(ctx context.Context, id int64, check scm.CheckRun) error
}

// Reconcluded is what Reconclude did.
type Reconcluded struct {
	// Previous is the newest check run on the head, which carries the report
	// in full.
	Previous int64
	// Conclusion is the conclusion the approval gives the check.
	Conclusion string
	// CheckRun is the check run posted, 0 when none was.
	CheckRun int64
	// Unnamed says why the posted check run does not name itself in its own
	// result object, nil when it does. The check keeps the object it was
	// posted with.
	Unnamed error
}

// Reconclude posts the newest costroid check on head again with the
// conclusion approval gives it: neutral for an approved failure, the policy's
// verdict when approval is nil. It then names the new check run in its own
// result object. When the newest check already concludes so, it posts nothing
// and answers ErrConcluded with Previous and Conclusion set. A head with no
// costroid check answers ErrNoCheck. now is read when the check is posted.
func Reconclude(ctx context.Context, host Rechecker, head string, approval *comment.Approval, now func() time.Time) (Reconcluded, error) {
	newest, err := Newest(ctx, host, head)
	if err != nil {
		return Reconcluded{}, err
	}
	block := newest.Block
	done := Reconcluded{Previous: newest.CheckRun, Conclusion: block.ApprovedConclusion(approval)}
	if block.Conclusion != nil && *block.Conclusion == done.Conclusion {
		return done, ErrConcluded
	}
	render := func(id *int64) (comment.CheckOutput, error) {
		named := block
		if id != nil {
			named.CheckRunID = id
		}
		return comment.Reconclude(named, approval, newest.CheckRun)
	}
	output, err := render(nil)
	if err != nil {
		return Reconcluded{}, err
	}
	done.CheckRun, err = host.PostCheck(ctx, scm.CheckRun{
		Name: comment.CheckName, HeadSHA: head, Conclusion: done.Conclusion,
		Title: output.Title, Summary: output.Summary, Text: output.Text, Completed: now(),
	})
	if err != nil {
		return Reconcluded{}, err
	}
	if output, err = render(&done.CheckRun); err == nil {
		err = host.UpdateCheck(ctx, done.CheckRun, scm.CheckRun{Title: output.Title, Summary: output.Summary, Text: output.Text})
	}
	done.Unnamed = err
	return done, nil
}
