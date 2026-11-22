package scm_test

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/Costroid/costroid/internal/scm"
	"github.com/Costroid/costroid/internal/scm/scmtest"
)

const receiptMarker = "<!-- costroid:receipt -->"

var posted = time.Date(2026, 11, 20, 16, 42, 7, 0, time.UTC)

// TestOnlyThePullRequestsAuthorsReceiptIsRead lists a pull request whose
// comments open with the receipt's marker, posted by its author, by another
// person, by an App and by the token's own account, and by the author without
// the marker. Only the author's receipts come back, with the id, the body and
// both times the host states. An author whose login is an App's, or the
// token's own, has none.
func TestOnlyThePullRequestsAuthorsReceiptIsRead(t *testing.T) {
	stub := scmtest.New(t)
	host := adapter(t, stub)
	stub.Comments = []scmtest.Comment{
		{ID: 1, Body: receiptMarker + "\nfirst", Login: "octocat", CreatedAt: posted, UpdatedAt: posted},
		{ID: 2, Body: receiptMarker + "\nstranger", Login: "mallory", CreatedAt: posted, UpdatedAt: posted},
		{ID: 3, Body: receiptMarker + "\nan app", Login: "helper[bot]", CreatedAt: posted, UpdatedAt: posted},
		{ID: 4, Body: receiptMarker + "\nthe token's own", Login: "github-actions[bot]", CreatedAt: posted, UpdatedAt: posted},
		{ID: 5, Body: "Thanks. " + receiptMarker, Login: "octocat", CreatedAt: posted, UpdatedAt: posted},
		{ID: 6, Body: receiptMarker + "\nsecond", Login: "OctoCat", CreatedAt: posted.Add(time.Hour), UpdatedAt: posted.Add(2 * time.Hour)},
	}
	found, err := host.AuthorComments(context.Background(), 7, "octocat", receiptMarker)
	if err != nil {
		t.Fatal(err)
	}
	ids := make([]int64, len(found))
	for index, comment := range found {
		ids[index] = comment.ID
	}
	if !slices.Equal(ids, []int64{1, 6}) {
		t.Fatalf("the author's receipts are comments %v, want 1 and 6", ids)
	}
	if last := found[1]; last.Body != receiptMarker+"\nsecond" || !last.CreatedAt.Equal(posted.Add(time.Hour)) || !last.UpdatedAt.Equal(posted.Add(2*time.Hour)) {
		t.Errorf("comment 6 reads %+v", last)
	}
	for _, author := range []string{"helper[bot]", "github-actions[bot]", ""} {
		if found, err := host.AuthorComments(context.Background(), 7, author, receiptMarker); err != nil || len(found) != 0 {
			t.Errorf("author %q: %d comments, %v", author, len(found), err)
		}
	}
	if writes := stub.Writes(); len(writes) != 0 {
		t.Errorf("writes = %v", writes)
	}
}

// TestAReceiptIsNeverTakenForCostroidsComment puts the token's own sticky
// comment and a person's receipt on one pull request, with the author's own
// copy of the sticky comment's marker: the sticky comment is found by its
// marker and edited, the receipt is never found or edited in its place, and
// the reverse: the author's receipts never include a comment that opens with
// the sticky comment's marker.
func TestAReceiptIsNeverTakenForCostroidsComment(t *testing.T) {
	stub := scmtest.New(t)
	host := adapter(t, stub)
	stub.Comments = []scmtest.Comment{
		{ID: 1, Body: receiptMarker + "\nthe author's", Login: "octocat", CreatedAt: posted, UpdatedAt: posted},
		{ID: 2, Body: marker + "\nthe author's copy", Login: "octocat", CreatedAt: posted, UpdatedAt: posted},
		{ID: 3, Body: receiptMarker + "\nin the token's own", Login: "github-actions[bot]"},
	}
	if found, err := host.HasComment(context.Background(), 7, marker); err != nil || found {
		t.Fatalf("a receipt or a person's comment was taken for the sticky comment: %v, %v", found, err)
	}
	if err := host.UpsertComment(context.Background(), 7, marker, marker+"\nreport"); err != nil {
		t.Fatal(err)
	}
	if err := host.UpsertComment(context.Background(), 7, marker, marker+"\nreport again"); err != nil {
		t.Fatal(err)
	}
	bodies := map[int64]string{}
	sticky := 0
	for _, comment := range stub.Comments {
		bodies[comment.ID] = comment.Body
		if comment.Body == marker+"\nreport again" {
			sticky++
		}
	}
	if sticky != 1 || len(stub.Comments) != 4 || bodies[1] != receiptMarker+"\nthe author's" ||
		bodies[2] != marker+"\nthe author's copy" || bodies[3] != receiptMarker+"\nin the token's own" {
		t.Errorf("%d sticky comments among %d; the comments read %v", sticky, len(stub.Comments), bodies)
	}
	found, err := host.AuthorComments(context.Background(), 7, "octocat", receiptMarker)
	if err != nil || len(found) != 1 || found[0].ID != 1 {
		t.Errorf("the author's receipts: %+v, %v", found, err)
	}
	var _ interface {
		PullOrigin(context.Context, int) (scm.PullOrigin, error)
	} = host
}

// TestPullOriginNamesTheAuthorAndTheHeadBranch reads a pull request from a
// branch of the repository and from a fork.
func TestPullOriginNamesTheAuthorAndTheHeadBranch(t *testing.T) {
	stub := scmtest.New(t)
	stub.Author, stub.HeadBranch, stub.HeadRepository = "octocat", "fix/retry-backoff", "example/widgets"
	origin, err := adapter(t, stub).PullOrigin(context.Background(), 7)
	if err != nil || origin != (scm.PullOrigin{Author: "octocat", Branch: "fix/retry-backoff"}) {
		t.Errorf("the repository's branch: %+v, %v", origin, err)
	}
	for _, fork := range []string{"octocat/widgets", ""} {
		stub.HeadRepository = fork
		if origin, err := adapter(t, stub).PullOrigin(context.Background(), 7); err != nil || !origin.Fork {
			t.Errorf("head repository %q: %+v, %v", fork, origin, err)
		}
	}
}
