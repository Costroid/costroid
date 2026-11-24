package server

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/Costroid/costroid/internal/focus"
	"github.com/Costroid/costroid/internal/store"
)

// ingested posts rows under bearer and reads the answer.
func (w *world) ingested(t *testing.T, bearer string, rows ...map[string]any) ingestAnswer {
	t.Helper()
	var receipt ingestAnswer
	if err := json.Unmarshal(w.rows(bearer, lines(t, rows...)).want(t, http.StatusOK).body, &receipt); err != nil {
		t.Fatal(err)
	}
	return receipt
}

// viewOf reads pull request pr of the fixtures' repository.
func (w *world) viewOf(t *testing.T, pr string) pullRequestView {
	t.Helper()
	var view pullRequestView
	if err := json.Unmarshal(w.send(http.MethodGet, "/v1/pulls/"+pr+"?repo="+acmeRepo, acmeKey, "", nil).want(t, http.StatusOK).body, &view); err != nil {
		t.Fatal(err)
	}
	return view
}

// contestedOn reads the count of contested rows on pull request pr of the
// fixtures' repository.
func (w *world) contestedOn(t *testing.T, pr string) int64 {
	t.Helper()
	return w.viewOf(t, pr).ContestedRows
}

// runRowsOnTheKey are the fixtures' run's three rows on its attested key.
func runRowsOnTheKey() []map[string]any {
	return []map[string]any{
		credentialed(row("req-1", focus.Output, 1000)), credentialed(row("req-1", focus.InputUncached, 4000)),
		credentialed(row("req-2", focus.Output, 10)),
	}
}

// TestTheEntranceNamesARowAnotherRunAlsoStated sends the fixtures' run's three
// rows, and a run of another pull request of the repository states two of
// their ids, before the run and after it. Each answer counts the two rows as
// contested, accepted all the same, and each pull request's view counts them.
// The run's third row, which no other run stated, is not contested. An ingest
// key that states the run's rows again is answered with three duplicates,
// each outranked and none contested, and moves no view's count.
func TestTheEntranceNamesARowAnotherRunAlsoStated(t *testing.T) {
	onBothSpendWorlds(t, func(t *testing.T, open func(*testing.T) *world) {
		for name, otherFirst := range map[string]bool{"the other run stated them first": true, "the other run stated them after": false} {
			t.Run(name, func(t *testing.T) {
				w := open(t)
				claims := w.claims()
				claims["run_id"], claims["ref"] = otherRun, "refs/pull/"+otherPull+"/merge"
				other := w.issuer.Token(claims)
				own := runRowsOnTheKey()
				sends := []func() ingestAnswer{
					func() ingestAnswer { return w.ingested(t, w.token(), own...) },
					func() ingestAnswer { return w.ingested(t, other, clone(own[0]), clone(own[1])) },
				}
				if otherFirst {
					sends[0], sends[1] = sends[1], sends[0]
				}
				if first := sends[0](); first.Contested != 0 {
					t.Fatalf("the first statement of the rows: %+v, want none contested", first)
				}
				if later := sends[1](); later.Contested != 2 || later.Duplicates != 0 {
					t.Fatalf("the later statement of two of the rows: %+v, want both accepted and contested", later)
				}
				for _, pr := range []string{acmePull, otherPull} {
					if got := w.contestedOn(t, pr); got != 2 {
						t.Fatalf("pull request %s counts %d contested rows, want 2", pr, got)
					}
				}
				if again := w.ingested(t, acmeKey, own...); again.Duplicates != 3 || again.Contested != 0 || again.Outranked != 3 {
					t.Fatalf("an ingest key's statement of the run's rows: %+v, want three duplicates, each outranked, none contested", again)
				}
				if got := w.contestedOn(t, acmePull); got != 2 {
					t.Fatalf("after the ingest key's duplicates the pull request counts %d contested rows, want 2", got)
				}
			})
		}
	})
}

// TestATeamThatSendsByTwoPathsReadsItsRowsOutrankedNotContested sends the
// fixtures' run's rows first with an ingest key, claiming the run's pull
// request, as telemetry sent with a key does, then with the run's own token.
// The run's request is accepted whole, and its answer counts the three rows
// outranked and none contested. The pull request reads the run's rows alone,
// eligible for its budget, with three outranked rows and no contested one.
func TestATeamThatSendsByTwoPathsReadsItsRowsOutrankedNotContested(t *testing.T) {
	onBothSpendWorlds(t, func(t *testing.T, open func(*testing.T) *world) {
		w := open(t)
		claims := runRowsOnTheKey()
		for _, r := range claims {
			tags(r)[store.TagRepo], tags(r)[store.TagPR] = acmeRepo, acmePull
		}
		if first := w.ingested(t, acmeKey, claims...); first.Accepted != 3 || first.Outranked != 0 || first.Contested != 0 {
			t.Fatalf("the ingest key's rows: %+v, want three accepted, none met", first)
		}
		if own := w.ingested(t, w.token(), runRowsOnTheKey()...); own.Accepted != 3 || own.Outranked != 3 || own.Contested != 0 {
			t.Fatalf("the run's rows: %+v, want three accepted and outranked, none contested", own)
		}
		view := w.viewOf(t, acmePull)
		rows := int64(0)
		for _, line := range view.Lines {
			if line.RunIdentity != identityToken || !line.BudgetEligible {
				t.Fatalf("the pull request reads a line the run did not verify: %+v", view.Lines)
			}
			rows += line.Rows
		}
		if rows != 3 || view.OutrankedRows != 3 || view.ContestedRows != 0 {
			t.Fatalf("the pull request reads %d rows, %d outranked and %d contested, want 3, 3 and 0", rows, view.OutrankedRows, view.ContestedRows)
		}
	})
}
