package scm_test

import (
	"context"
	"testing"

	"github.com/Costroid/costroid/internal/scm"
	"github.com/Costroid/costroid/internal/scm/scmtest"
)

func TestHasCommentFindsOnlyTheTokensOwnAndWritesNothing(t *testing.T) {
	stub := scmtest.New(t)
	host := adapter(t, stub)
	stub.Comments = []scmtest.Comment{{ID: 1, Body: marker + " planted by a person", Login: "octocat"}}
	if found, err := host.HasComment(context.Background(), 7, marker); err != nil || found {
		t.Fatalf("found %v, error %v", found, err)
	}
	stub.Comments = append(stub.Comments, scmtest.Comment{ID: 2, Body: marker + " old", Login: "github-actions[bot]"})
	if found, err := host.HasComment(context.Background(), 7, marker); err != nil || !found {
		t.Fatalf("found %v, error %v", found, err)
	}
	if writes := stub.Writes(); len(writes) != 0 {
		t.Fatalf("writes = %v", writes)
	}
	var _ scm.CommentFinder = host
}
