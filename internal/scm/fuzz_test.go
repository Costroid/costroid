package scm

import (
	"bytes"
	"context"
	"encoding/base64"
	"strings"
	"testing"
	"time"
)

// FuzzDecodeFile reads the content a host's files API answers. What it
// decodes is the content a host encoded, and a refusal names the file.
func FuzzDecodeFile(f *testing.F) {
	content := []byte("comment:\n  show_cost: false\npolicy:\n  run_budget_usd: \"5\"\n")
	encoded := base64.StdEncoding.EncodeToString(content)
	f.Add("costroid.yml", encoded)
	f.Add("costroid.yml", encoded[:60]+"\n"+encoded[60:]+"\n")
	f.Add("costroid.yml", "Y29z\r\ndHJvaWQ=")
	f.Add("a b.yml", "====")
	f.Fuzz(func(t *testing.T, path, encoded string) {
		decoded, err := decodeFile(path, encoded)
		if err != nil {
			if !strings.Contains(err.Error(), path) {
				t.Fatalf("the refusal does not name %q: %v", path, err)
			}
			return
		}
		if len(decoded) > len(encoded)*3/4 {
			t.Fatalf("%d bytes decoded from %d", len(decoded), len(encoded))
		}
		again, err := decodeFile(path, base64.StdEncoding.EncodeToString(decoded))
		if err != nil || !bytes.Equal(again, decoded) {
			t.Fatalf("the content does not survive its own encoding: %v", err)
		}
	})
}

// writers is the roles a fuzzed review is held to: every account can write
// but reader.
type writers struct{}

func (writers) CanWrite(_ context.Context, login string) (bool, error) {
	return login != "reader", nil
}

const (
	fuzzHead  = "9f2c6e1ab47d03f58c1e7726d0b4a93e5f618c2d"
	otherHead = "1111111111111111111111111111111111111111"
	author    = "author"
)

var (
	approvalNow     = time.Date(2026, 11, 7, 12, 0, 0, 0, time.UTC)
	fuzzReviewers   = []string{author, "Author", "maintainer", "Maintainer", "reader", "costroid[bot]", "", "-x"}
	fuzzStates      = []string{ReviewApproved, "approved", ReviewChangesRequested, ReviewCommented, ReviewDismissed, ReviewPending, "dismissed", "APPROVE"}
	fuzzCommits     = []string{fuzzHead, otherHead, "", strings.ToUpper(fuzzHead)}
	fuzzApprovalAge = time.Hour
)

// reviewsOf reads four bytes as one review: who, what state, on which commit,
// and how many hours before now. The order of the bytes is the order the
// host lists the reviews in.
func reviewsOf(data []byte) []Review {
	var reviews []Review
	for index := 0; index+4 <= len(data); index += 4 {
		review := Review{
			ID:       int64(index/4 + 1),
			Reviewer: fuzzReviewers[int(data[index])%len(fuzzReviewers)],
			State:    fuzzStates[int(data[index+1])%len(fuzzStates)],
			CommitID: fuzzCommits[int(data[index+2])%len(fuzzCommits)],
		}
		if data[index+3] != 0xff {
			review.SubmittedAt = approvalNow.Add(-time.Duration(data[index+3]) * time.Minute)
		}
		reviews = append(reviews, review)
	}
	return reviews
}

// standing is each reviewer's latest review that is not a comment, the way a
// host counts a reviewer's say.
func standing(reviews []Review) map[string]Review {
	latest := map[string]Review{}
	for _, review := range reviews {
		if state := strings.ToUpper(review.State); state == ReviewCommented || state == ReviewPending {
			continue
		}
		key := strings.ToLower(review.Reviewer)
		if held, found := latest[key]; !found || newer(review, held) {
			latest[key] = review
		}
	}
	return latest
}

// counts says whether one review approves the head under the rules, stated
// apart from CountReview so the two can disagree.
func counts(review Review, rules ApprovalRules) bool {
	age := approvalNow.Sub(review.SubmittedAt)
	return strings.EqualFold(review.State, ReviewApproved) && loginPattern.MatchString(review.Reviewer) &&
		!strings.HasSuffix(review.Reviewer, "[bot]") && (rules.AllowAuthor || !strings.EqualFold(review.Reviewer, author)) &&
		review.CommitID == fuzzHead && !review.SubmittedAt.IsZero() && age < rules.Lasts && review.Reviewer != "reader"
}

// FuzzApprovalOf holds approval by review to its rules over any list of
// reviews a host can answer: an approval is a reviewer's standing review, it
// approves the head itself, it is recent, it is not the author's unless the
// repository allows it, and it comes from an account that can write. When an
// approving standing review exists, an approval is found.
func FuzzApprovalOf(f *testing.F) {
	f.Add([]byte{2, 0, 0, 5}, false)
	f.Add([]byte{0, 0, 0, 5}, false)
	f.Add([]byte{0, 0, 0, 5}, true)
	f.Add([]byte{2, 0, 0, 5, 3, 2, 0, 3}, false)
	f.Add([]byte{2, 0, 0, 5, 2, 3, 0, 1}, false)
	f.Add([]byte{2, 0, 1, 5, 4, 0, 0, 1, 5, 1, 0, 1, 2, 1, 0, 0xff}, false)
	f.Fuzz(func(t *testing.T, data []byte, allowAuthor bool) {
		reviews := reviewsOf(data)
		rules := ApprovalRules{AllowAuthor: allowAuthor, Lasts: fuzzApprovalAge}
		pull := PullRequest{Number: 7, HeadSHA: fuzzHead, Author: author}
		approval, err := ApprovalOf(context.Background(), writers{}, rules, pull, reviews, approvalNow)
		latest := standing(reviews)
		approves := false
		for _, review := range latest {
			approves = approves || counts(review, rules)
		}
		if err != nil {
			if approves {
				t.Fatalf("a standing review approves the head and none was found: %v, %+v", err, reviews)
			}
			return
		}
		review := latest[strings.ToLower(approval.Approver)]
		if review.ID != approval.ReviewID || !counts(review, rules) {
			t.Fatalf("approved by %+v, whose standing review is %+v", approval, review)
		}
		if approval.HeadSHA != fuzzHead || approval.PullRequest != pull.Number {
			t.Fatalf("approved %+v for pull request %+v", approval, pull)
		}
	})
}
