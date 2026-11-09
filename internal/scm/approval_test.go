package scm_test

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"github.com/Costroid/costroid/internal/scm"
	"github.com/Costroid/costroid/internal/scm/scmtest"
)

func approvalStub(t *testing.T) (*scmtest.Stub, *scm.GitHub) {
	t.Helper()
	stub := scmtest.New(t)
	stub.Head = sampleSHA
	stub.Author = "author"
	stub.Permission = "write"
	return stub, adapter(t, stub)
}

func request() scm.ApprovalRequest {
	return scm.ApprovalRequest{PullRequest: 7, Approver: "reviewer", HeadSHA: sampleSHA}
}

func TestAWriterApprovesTheHeadAndIsRecorded(t *testing.T) {
	_, host := approvalStub(t)
	at := newClock().now()
	approval, err := scm.Approve(context.Background(), host, request(), at)
	if err != nil {
		t.Fatal(err)
	}
	if approval != (scm.Approval{PullRequest: 7, Approver: "reviewer", HeadSHA: sampleSHA, At: at}) {
		t.Errorf("approval = %+v", approval)
	}
}

func TestAnAdministratorHoldsAWriteRole(t *testing.T) {
	stub, host := approvalStub(t)
	stub.Permission = "admin"
	if _, err := scm.Approve(context.Background(), host, request(), newClock().now()); err != nil {
		t.Error(err)
	}
}

func TestARoleBelowWriteIsRefused(t *testing.T) {
	for _, permission := range []string{"read", "none", "", "triage"} {
		stub, host := approvalStub(t)
		stub.Permission = permission
		if _, err := scm.Approve(context.Background(), host, request(), newClock().now()); !errors.Is(err, scm.ErrNoWriteRole) {
			t.Errorf("%q: err = %v", permission, err)
		}
	}
}

func TestTheAuthorIsRefusedByDefaultInAnyCase(t *testing.T) {
	_, host := approvalStub(t)
	for _, approver := range []string{"author", "AUTHOR", "Author"} {
		ask := request()
		ask.Approver = approver
		if _, err := scm.Approve(context.Background(), host, ask, newClock().now()); !errors.Is(err, scm.ErrAuthorApproval) {
			t.Errorf("%s: err = %v", approver, err)
		}
	}
}

func TestAnAppsAccountOrNoAccountCannotApprove(t *testing.T) {
	_, host := approvalStub(t)
	for approver, refusal := range map[string]error{"helper[bot]": scm.ErrBotApproval, "": scm.ErrNoApprover, "<script>": scm.ErrNoApprover} {
		ask := request()
		ask.Approver = approver
		if _, err := scm.Approve(context.Background(), host, ask, newClock().now()); !errors.Is(err, refusal) {
			t.Errorf("%q: err = %v", approver, err)
		}
	}
}

func TestTheAuthorPassesOnlyWhenAllowed(t *testing.T) {
	_, host := approvalStub(t)
	ask := request()
	ask.Approver = "author"
	ask.AllowAuthor = true
	if _, err := scm.Approve(context.Background(), host, ask, newClock().now()); err != nil {
		t.Error(err)
	}
}

func TestAnApprovalOfAnOlderCommitIsRefused(t *testing.T) {
	stub, host := approvalStub(t)
	stub.Head = movedSHA
	if _, err := scm.Approve(context.Background(), host, request(), newClock().now()); !errors.Is(err, scm.ErrStaleHead) {
		t.Errorf("err = %v", err)
	}
}

func TestAnApprovalLapsesOnANewPush(t *testing.T) {
	stub, host := approvalStub(t)
	approval, err := scm.Approve(context.Background(), host, request(), newClock().now())
	if err != nil {
		t.Fatal(err)
	}
	if !approval.Holds(stub.Head) {
		t.Error("the approval does not hold its own head")
	}
	stub.Head = movedSHA
	if approval.Holds(stub.Head) {
		t.Error("the approval held across a push")
	}
}

func TestAHostThatCannotAnswerRefusesTheApproval(t *testing.T) {
	for _, path := range []string{"GET /pulls/", "GET /collaborators/"} {
		stub, host := approvalStub(t)
		stub.Fail[path] = http.StatusBadGateway
		if _, err := scm.Approve(context.Background(), host, request(), newClock().now()); err == nil {
			t.Errorf("%s failed and the approval stood", path)
		}
	}
}

func TestTheApproveEventBecomesARequestAndNothingElseDoes(t *testing.T) {
	event, err := scm.ParseEvent(scm.EventCheckRun, []byte(approveBody()))
	if err != nil {
		t.Fatal(err)
	}
	ask, err := scm.RequestFrom(event)
	if err != nil {
		t.Fatal(err)
	}
	if ask != (scm.ApprovalRequest{PullRequest: 7, Approver: "octocat", HeadSHA: sampleSHA}) {
		t.Errorf("request = %+v", ask)
	}
	other := event
	other.RequestedAction = "explain"
	fork := event
	fork.PullRequest = 0
	pull, err := scm.ParseEvent(scm.EventPullRequest, []byte(pullRequestBody(newClock().now())))
	if err != nil {
		t.Fatal(err)
	}
	for _, refused := range []scm.Event{other, fork, pull} {
		if _, err := scm.RequestFrom(refused); !errors.Is(err, scm.ErrNotAnApproval) {
			t.Errorf("%+v: err = %v", refused, err)
		}
	}
}
