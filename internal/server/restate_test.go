package server

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/Costroid/costroid/internal/decimal"
	"github.com/Costroid/costroid/internal/focus"
	"github.com/Costroid/costroid/internal/gateway"
	"github.com/Costroid/costroid/internal/store"
)

// otlpRequest is a provider request that reaches the service by telemetry,
// with its cache writes' lifetime assumed, as Claude Code's api_request event
// states them when it gives no split.
const otlpRequest = "req_otlp"

// anotherPull is a pull request of the same repository as the fixtures'.
const anotherPull = "1424"

// restateWorld is the service on Postgres, keeping the gateway's spend, with
// the fixtures' key attested for the fixtures' run and its spend counted.
type restateWorld struct {
	*ledgerWorld
}

func newRestateWorld(t *testing.T) restateWorld {
	t.Helper()
	w := postgresWorld(t)
	config := w.server.Config
	config.Spend = config.Ledger.(*store.Store)
	w.server = New(config)
	w.http = httptest.NewServer(w.server.Handler())
	t.Cleanup(w.http.Close)
	if err := w.attest(w.protectedToken(fixturesMintRun, nil), fixturesRun(gateway.KeyID(fixturesKey))); err != nil {
		t.Fatal(err)
	}
	w.attestSpend(w.closeToken(), gateway.KeyID(fixturesKey), fixturesSpend("0.5")).want(t, http.StatusNoContent)
	return restateWorld{w}
}

// runRows are the fixtures' run's rows on the attested key, as costroid run
// writes them.
func runRows() []map[string]any {
	first := credentialed(row("req-1", focus.Output, 1000))
	tags(first)[focus.TagTeam] = "platform"
	second := credentialed(row("req-1", focus.InputUncached, 4000))
	tags(second)[focus.TagTeam] = "platform"
	return []map[string]any{first, second, credentialed(row("req-2", focus.Output, 10))}
}

// haikuRow is the result stream's row of one class of a request that
// apiRequest describes.
func haikuRow(request string, class focus.TokenClass, tokens int) map[string]any {
	r := row(request, class, tokens)
	r["SkuId"] = "anthropic/claude-haiku-5-5/" + string(class)
	r["SkuPriceId"] = ""
	tags(r)[focus.TagModel] = "claude-haiku-5-5"
	return r
}

// writeRow is the result stream's row of a request's cache writes, which it
// states at class.
func writeRow(request string, class focus.TokenClass) map[string]any {
	return haikuRow(request, class, 100)
}

// streamedRequest is the result stream's rows of the request apiRequest
// describes, its cache writes stated at five minutes.
func streamedRequest(request string) []map[string]any {
	return []map[string]any{
		haikuRow(request, focus.InputUncached, 10), haikuRow(request, focus.Output, 2), writeRow(request, focus.CacheWrite5m),
	}
}

func (w restateWorld) telemetry(t *testing.T, bearer, request string) {
	t.Helper()
	body := claudeLogs(t, map[string]string{"service.name": "claude-code"}, apiRequest(request, false))
	w.send(http.MethodPost, "/otlp/v1/logs", bearer, "application/json", body).want(t, http.StatusOK)
}

// pullToken is the fixtures' run's token with change applied to its claims.
func (w restateWorld) pullToken(change func(map[string]any)) string {
	claims := w.claims()
	change(claims)
	return w.issuer.Token(claims)
}

// otherPullToken is a run of the same repository for anotherPull.
func (w restateWorld) otherPullToken() string {
	return w.pullToken(func(claims map[string]any) {
		claims["run_id"], claims["ref"] = "16873409999", "refs/pull/"+anotherPull+"/merge"
	})
}

// reversed writes a row as JSON with every object's keys in reverse order.
func reversed(t *testing.T, value any) []byte {
	t.Helper()
	object, isObject := value.(map[string]any)
	if !isObject {
		data, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		return data
	}
	keys := slices.Sorted(maps.Keys(object))
	slices.Reverse(keys)
	var b bytes.Buffer
	b.WriteByte('{')
	for i, key := range keys {
		if i > 0 {
			b.WriteByte(',')
		}
		b.Write(reversed(t, key))
		b.WriteByte(':')
		b.Write(reversed(t, object[key]))
	}
	b.WriteByte('}')
	return b.Bytes()
}

// state is every figure a reader of the pull request and the month sees, with
// the row count, each as the service or the ledger writes it. The service
// answers no month's total, so the month is summed from the rows directly.
func (w restateWorld) state(t *testing.T) string {
	t.Helper()
	view := w.send(http.MethodGet, "/v1/pulls/"+acmePull+"?repo="+acmeRepo, w.token(), "", nil).want(t, http.StatusOK)
	verdict := w.send(http.MethodPost, "/v1/pulls/"+acmePull+"/verdict?repo="+acmeRepo, w.token(), jsonType,
		verdictBody(t, prBudget, nil)).want(t, http.StatusOK)
	rows, usd, err := w.server.Config.Spend.GatewaySpend(context.Background(), w.a, acmeRepo, acmePull)
	if err != nil {
		t.Fatal(err)
	}
	return fmt.Sprintf("view %s\nverdict %s\nmonth %s\ngateway %d rows, USD %s\nrows %d\n",
		view.body, verdict.body, w.month(t), rows, usd, w.count(t))
}

func (w restateWorld) month(t *testing.T) string {
	t.Helper()
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, w.db.SuperURL)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close(ctx) }()
	var month string
	err = conn.QueryRow(ctx, `
		SELECT coalesce(string_agg(currency || ' ' || rows || ' ' || list || ' ' || effective, '; ' ORDER BY currency), '')
		FROM (
			SELECT billing_currency AS currency, count(*)::text AS rows,
				sum(list_cost)::text AS list, sum(effective_cost)::text AS effective
			FROM focus_rows
			WHERE charge_period_start >= '2026-11-01T00:00:00Z' AND charge_period_start < '2026-12-01T00:00:00Z'
			GROUP BY billing_currency
		) m`).Scan(&month)
	if err != nil {
		t.Fatal(err)
	}
	return month
}

// sendRun sends the run's rows, changed by change, in one request.
func (w restateWorld) sendRun(t *testing.T, bearer string, change func(map[string]any)) answer {
	t.Helper()
	rows := runRows()
	for _, r := range rows {
		change(r)
	}
	return w.rows(bearer, lines(t, rows...))
}

// restated is one restatement through the entrance: what it sends first, and
// what it sends again.
type restated struct {
	first func(t *testing.T, w restateWorld)
	again func(t *testing.T, w restateWorld)
}

func sendsRun(t *testing.T, w restateWorld) {
	w.sendRun(t, w.token(), func(map[string]any) {}).want(t, http.StatusOK)
}

// againWith sends the run's rows again under the fixtures' token, each changed
// by change.
func againWith(change func(map[string]any)) restated {
	return restated{first: sendsRun, again: func(t *testing.T, w restateWorld) {
		w.sendRun(t, w.token(), change).want(t, http.StatusOK)
	}}
}

// refusedAgain sends the run's rows again with change, which the entrance
// refuses whole.
func refusedAgain(change func(map[string]any)) restated {
	return restated{first: sendsRun, again: func(t *testing.T, w restateWorld) {
		if got := w.sendRun(t, w.token(), change).want(t, http.StatusUnprocessableEntity); got.code(t) != codeInvalidRow {
			t.Fatalf("code %s", got.code(t))
		}
	}}
}

func entranceRestatements() map[string]restated {
	return map[string]restated{
		"byte for byte":                   againWith(func(map[string]any) {}),
		"a tag's value":                   againWith(func(r map[string]any) { tags(r)[focus.TagSessionID] = "session-2" }),
		"a named tag's value":             againWith(func(r map[string]any) { tags(r)[focus.TagTeam] = "payments" }),
		"a tag added":                     againWith(func(r map[string]any) { tags(r)[focus.TagWorkItem] = "ENG-12" }),
		"a tag of the sender's own added": againWith(func(r map[string]any) { tags(r)["env"] = "prod" }),
		"a tag removed": againWith(func(r map[string]any) {
			delete(tags(r), focus.TagSessionID)
			delete(tags(r), focus.TagTeam)
		}),
		"the account":            againWith(func(r map[string]any) { r["BillingAccountId"] = "acct-2" }),
		"the account's name":     againWith(func(r map[string]any) { r["BillingAccountName"] = "Another name" }),
		"the sub-account":        againWith(func(r map[string]any) { r["SubAccountId"] = gateway.KeyID("sk-another-key") }),
		"the sub-account's name": againWith(func(r map[string]any) { r["SubAccountName"] = "Another key" }),
		"the order of the rows": {first: sendsRun, again: func(t *testing.T, w restateWorld) {
			rows := runRows()
			slices.Reverse(rows)
			w.rows(w.token(), lines(t, rows...)).want(t, http.StatusOK)
		}},
		"the order of the keys in a row": {first: sendsRun, again: func(t *testing.T, w restateWorld) {
			var body []byte
			for _, r := range runRows() {
				body = append(append(body, reversed(t, r)...), '\n')
			}
			w.rows(w.token(), body).want(t, http.StatusOK)
		}},
		"a token count with a leading zero": againWith(func(r map[string]any) { r["ConsumedQuantity"] = "0" + r["ConsumedQuantity"].(string) }),
		"a token count with a plus sign":    againWith(func(r map[string]any) { r["ConsumedQuantity"] = "+" + r["ConsumedQuantity"].(string) }),
		"a token count with a fraction":     refusedAgain(func(r map[string]any) { r["ConsumedQuantity"] = r["ConsumedQuantity"].(string) + ".0" }),
		"a token count in exponent form":    refusedAgain(func(r map[string]any) { r["ConsumedQuantity"] = r["ConsumedQuantity"].(string) + "e0" }),
		"the sender's costs written otherwise": againWith(func(r map[string]any) {
			for column := range costColumns {
				r[column] = "1.50e-2"
			}
		}),
		"one request per row": {first: sendsRun, again: func(t *testing.T, w restateWorld) {
			for _, r := range runRows() {
				w.rows(w.token(), lines(t, r)).want(t, http.StatusOK)
			}
		}},
		"the rows twice in one request": {first: sendsRun, again: func(t *testing.T, w restateWorld) {
			rows := runRows()
			w.rows(w.token(), lines(t, append(rows, runRows()...)...)).want(t, http.StatusOK)
		}},
		"the run attempt": {first: sendsRun, again: func(t *testing.T, w restateWorld) {
			token := w.pullToken(func(claims map[string]any) { claims["run_attempt"] = "2" })
			w.sendRun(t, token, func(map[string]any) {}).want(t, http.StatusOK)
		}},
		"a lower stated grade": againWith(func(r map[string]any) { tags(r)[focus.TagAttributionGrade] = gradeWindowed }),
		"a lower grade, sent with an ingest key": {first: sendsRun, again: func(t *testing.T, w restateWorld) {
			w.sendRun(t, acmeKey, func(map[string]any) {}).want(t, http.StatusOK)
		}},
		"a higher stated grade": {
			first: func(t *testing.T, w restateWorld) {
				w.sendRun(t, w.token(), func(r map[string]any) { tags(r)[focus.TagAttributionGrade] = gradeWindowed }).want(t, http.StatusOK)
			},
			again: func(t *testing.T, w restateWorld) {
				w.sendRun(t, w.token(), func(r map[string]any) { tags(r)[focus.TagAttributionGrade] = gradeExact }).want(t, http.StatusOK)
			},
		},
		"a cache write at another lifetime, by the result stream after telemetry": {
			first: func(t *testing.T, w restateWorld) {
				sendsRun(t, w)
				w.telemetry(t, w.token(), otlpRequest)
			},
			again: func(t *testing.T, w restateWorld) {
				w.rows(w.token(), lines(t, writeRow(otlpRequest, focus.CacheWrite5m))).want(t, http.StatusOK)
			},
		},
		"a cache write at another lifetime, by telemetry after the result stream": {
			first: func(t *testing.T, w restateWorld) {
				sendsRun(t, w)
				w.rows(w.token(), lines(t, streamedRequest(otlpRequest)...)).want(t, http.StatusOK)
			},
			again: func(t *testing.T, w restateWorld) {
				w.telemetry(t, w.token(), otlpRequest)
			},
		},
	}
}

// TestARestatedRowNeverCountsTwice sends a run's rows through POST /v1/rows,
// then sends them again once for each thing a restatement can change while
// the charge stays the same. After each, the pull request's view and verdict,
// the month's total, the gateway's count behind the budget and the row count
// are byte for byte what they were after the first.
func TestARestatedRowNeverCountsTwice(t *testing.T) {
	for name, tc := range entranceRestatements() {
		t.Run(name, func(t *testing.T) {
			w := newRestateWorld(t)
			tc.first(t, w)
			first := w.state(t)
			tc.again(t, w)
			if got := w.state(t); got != first {
				t.Fatalf("after the restatement:\n%s\nafter the first statement:\n%s", got, first)
			}
		})
	}
}

// storedRow is one statement of a row that stands, as ledger_rows reads it.
type storedRow struct {
	quantity, grade, identity, pr, credential string
	contested, outranked                      bool
}

// standing reads every statement of id that stands, in the order of their pull
// requests.
func (w restateWorld) standing(t *testing.T, id string) []storedRow {
	t.Helper()
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, w.db.SuperURL)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close(ctx) }()
	rows, err := conn.Query(ctx, `
		SELECT consumed_quantity::text, attribution_grade, run_identity, coalesce(pr, ''), coalesce(credential_basis, ''), contested, outranked
		FROM ledger_rows WHERE row_id = $1 ORDER BY pr`, id)
	if err != nil {
		t.Fatal(err)
	}
	var read []storedRow
	var r storedRow
	_, err = pgx.ForEachRow(rows, []any{&r.quantity, &r.grade, &r.identity, &r.pr, &r.credential, &r.contested, &r.outranked}, func() error {
		read = append(read, r)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return read
}

// listCost reads one row's list cost.
func (w restateWorld) listCost(t *testing.T, id string) decimal.Decimal {
	t.Helper()
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, w.db.SuperURL)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close(ctx) }()
	var list string
	if err := conn.QueryRow(ctx, `SELECT list_cost::text FROM focus_rows WHERE row_id = $1`, id).Scan(&list); err != nil {
		t.Fatal(err)
	}
	return decimal.MustParse(list)
}

// totals reads the pull request's list cost from its view, the month's list
// cost and the row count.
func (w restateWorld) totals(t *testing.T) (pullRequest, month decimal.Decimal, rows int) {
	t.Helper()
	view := w.view(t, w.token(), acmeRepo, acmePull)
	for _, total := range view.Totals {
		pullRequest = pullRequest.Add(decimal.MustParse(total.ListCost))
	}
	for _, figure := range strings.Split(w.month(t), "; ") {
		fields := strings.Fields(figure)
		month = month.Add(decimal.MustParse(fields[2]))
	}
	return pullRequest, month, w.count(t)
}

// TestADifferentChargeStillCounts holds the converse through the entrance: a
// row that is another charge adds its own list cost and one row to the pull
// request and the month, so the test above cannot pass on a ledger that keeps
// nothing it is sent twice.
func TestADifferentChargeStillCounts(t *testing.T) {
	for name, tc := range map[string]struct {
		first func(t *testing.T, w restateWorld)
		send  func(t *testing.T, w restateWorld) string
	}{
		"a row of another run": {first: sendsRun, send: func(t *testing.T, w restateWorld) string {
			token := w.pullToken(func(claims map[string]any) { claims["run_id"] = "16873409998" })
			r := row("req-3", focus.Output, 1000)
			w.rows(token, lines(t, r)).want(t, http.StatusOK)
			return r["x_CostroidRowId"].(string)
		}},
		"another line of the same run": {first: sendsRun, send: func(t *testing.T, w restateWorld) string {
			r := credentialed(row("req-1", focus.CacheRead, 1000))
			w.rows(w.token(), lines(t, r)).want(t, http.StatusOK)
			return r["x_CostroidRowId"].(string)
		}},
		"another amount": {first: sendsRun, send: func(t *testing.T, w restateWorld) string {
			r := credentialed(row("req-4", focus.Output, 2000))
			w.rows(w.token(), lines(t, r)).want(t, http.StatusOK)
			return r["x_CostroidRowId"].(string)
		}},
		"another request's cache write by the result stream after telemetry": {
			first: func(t *testing.T, w restateWorld) {
				sendsRun(t, w)
				w.telemetry(t, w.token(), otlpRequest)
			},
			send: func(t *testing.T, w restateWorld) string {
				r := writeRow("req_other", focus.CacheWrite5m)
				w.rows(w.token(), lines(t, r)).want(t, http.StatusOK)
				return r["x_CostroidRowId"].(string)
			},
		},
	} {
		t.Run(name, func(t *testing.T) {
			w := newRestateWorld(t)
			tc.first(t, w)
			pullBefore, monthBefore, rowsBefore := w.totals(t)
			added := w.listCost(t, tc.send(t, w))
			if added.Sign() <= 0 {
				t.Fatalf("the other charge is priced at %s", added)
			}
			pull, month, rows := w.totals(t)
			if pull.String() != pullBefore.Add(added).String() || month.String() != monthBefore.Add(added).String() || rows != rowsBefore+1 {
				t.Fatalf("pull request %s, month %s, %d rows; want %s, %s and %d rows",
					pull, month, rows, pullBefore.Add(added), monthBefore.Add(added), rowsBefore+1)
			}
		})
	}
}

// claimingThePull is r with the fixtures' repository and pull request stated
// as claims, as an ingest key's sender can state them.
func claimingThePull(r map[string]any) map[string]any {
	tags(r)[store.TagRepo], tags(r)[store.TagPR] = acmeRepo, acmePull
	return r
}

// TestWhichStatementOfARowStands holds the entrance to the ledger's rule.
// Within one run the first statement of a row id stands whole, and a later
// one is answered as a duplicate: a later token count, and two statements in
// one request, where the earlier line is the first. A statement sent with an
// ingest key never stands beside a run's, whichever came first, even when it
// claims the run's pull request: the pull request reads the run's statement,
// and the answer counts the row outranked, never contested. A run of another
// pull request never displaces the run's statement, whichever came first:
// each stands on its own pull request, and the answer and both views count it
// contested.
func TestWhichStatementOfARowStands(t *testing.T) {
	exact := storedRow{"1000.000000000000", gradeExact, identityToken, acmePull, store.CredentialMinted, false, false}
	contested := func(r storedRow) storedRow {
		r.contested = true
		return r
	}
	outranked := func(r storedRow) storedRow {
		r.outranked = true
		return r
	}
	otherPull := storedRow{"1000.000000000000", gradeWindowed, identityToken, anotherPull, store.CredentialStated, true, false}
	for name, tc := range map[string]struct {
		first, later func(t *testing.T, w restateWorld, r map[string]any) answer
		// receipt is what the last request answers, and kept the statements
		// the ledger holds of the row.
		receipt ingestAnswer
		kept    int
		want    []storedRow
	}{
		"a later token count": {
			first: func(t *testing.T, w restateWorld, r map[string]any) answer { return w.rows(w.token(), lines(t, r)) },
			later: func(t *testing.T, w restateWorld, r map[string]any) answer {
				r["ConsumedQuantity"] = "9999"
				return w.rows(w.token(), lines(t, r))
			},
			receipt: ingestAnswer{Duplicates: 1}, kept: 1,
			want: []storedRow{exact},
		},
		"a later token that grades it lower": {
			first: func(t *testing.T, w restateWorld, r map[string]any) answer { return w.rows(w.token(), lines(t, r)) },
			later: func(t *testing.T, w restateWorld, r map[string]any) answer {
				return w.rows(acmeKey, lines(t, claimingThePull(r)))
			},
			receipt: ingestAnswer{Duplicates: 1, Outranked: 1}, kept: 1,
			want: []storedRow{exact},
		},
		"a later token that grades it higher": {
			first: func(t *testing.T, w restateWorld, r map[string]any) answer {
				return w.rows(acmeKey, lines(t, claimingThePull(r)))
			},
			later:   func(t *testing.T, w restateWorld, r map[string]any) answer { return w.rows(w.token(), lines(t, r)) },
			receipt: ingestAnswer{Accepted: 1, Outranked: 1}, kept: 2,
			want: []storedRow{outranked(exact)},
		},
		"a later run of another pull request": {
			first: func(t *testing.T, w restateWorld, r map[string]any) answer { return w.rows(w.token(), lines(t, r)) },
			later: func(t *testing.T, w restateWorld, r map[string]any) answer {
				return w.rows(w.otherPullToken(), lines(t, r))
			},
			receipt: ingestAnswer{Accepted: 1, Contested: 1}, kept: 2,
			want: []storedRow{contested(exact), otherPull},
		},
		"an earlier run of another pull request": {
			first: func(t *testing.T, w restateWorld, r map[string]any) answer {
				return w.rows(w.otherPullToken(), lines(t, r))
			},
			later:   func(t *testing.T, w restateWorld, r map[string]any) answer { return w.rows(w.token(), lines(t, r)) },
			receipt: ingestAnswer{Accepted: 1, Contested: 1}, kept: 2,
			want: []storedRow{contested(exact), otherPull},
		},
		"two statements in one request": {
			first: func(t *testing.T, w restateWorld, r map[string]any) answer {
				later := clone(r)
				later["ConsumedQuantity"] = "9999"
				return w.rows(w.token(), lines(t, r, later))
			},
			receipt: ingestAnswer{Accepted: 1, Duplicates: 1}, kept: 1,
			want: []storedRow{exact},
		},
	} {
		t.Run(name, func(t *testing.T) {
			w := newRestateWorld(t)
			r := credentialed(row("req-1", focus.Output, 1000))
			id := r["x_CostroidRowId"].(string)
			last := tc.first(t, w, clone(r)).want(t, http.StatusOK)
			if tc.later != nil {
				last = tc.later(t, w, clone(r)).want(t, http.StatusOK)
			}
			var receipt ingestAnswer
			if err := json.Unmarshal(last.body, &receipt); err != nil {
				t.Fatal(err)
			}
			if receipt.Accepted != tc.receipt.Accepted || receipt.Duplicates != tc.receipt.Duplicates || receipt.Contested != tc.receipt.Contested ||
				receipt.Outranked != tc.receipt.Outranked {
				t.Fatalf("the last request: %+v, want %+v", receipt, tc.receipt)
			}
			if rows := w.count(t); rows != tc.kept {
				t.Fatalf("the ledger holds %d statements of the row, want %d", rows, tc.kept)
			}
			if got := w.standing(t, id); !slices.Equal(got, tc.want) {
				t.Fatalf("stands %+v, want %+v", got, tc.want)
			}
			for _, pull := range []string{acmePull, anotherPull} {
				view := w.view(t, acmeKey, acmeRepo, pull)
				i := slices.IndexFunc(tc.want, func(r storedRow) bool { return r.pr == pull })
				if holds := len(view.Lines) == 1 && view.Lines[0].Rows == 1; holds != (i >= 0) {
					t.Fatalf("pull request %s reads %+v, want the charge on it %t", pull, view.Lines, i >= 0)
				}
				if i >= 0 && (view.Lines[0].RunIdentity != tc.want[i].identity || (view.ContestedRows == 1) != tc.want[i].contested ||
					(view.OutrankedRows == 1) != tc.want[i].outranked) {
					t.Fatalf("pull request %s reads %+v with %d contested and %d outranked, want %+v",
						pull, view.Lines, view.ContestedRows, view.OutrankedRows, tc.want[i])
				}
			}
		})
	}
}
