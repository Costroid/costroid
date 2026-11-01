package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/Costroid/costroid/internal/focus"
	"github.com/Costroid/costroid/internal/store"
	"github.com/Costroid/costroid/internal/store/storetest"
)

// ledgerWorld is the service on real Postgres with two tenants.
type ledgerWorld struct {
	*world
	db   storetest.Database
	a, b store.TenantID
}

func postgresWorld(t *testing.T) *ledgerWorld {
	t.Helper()
	db := storetest.New(t)
	a := db.Tenant(t, "acme", []store.Binding{{Host: "github.com", Owner: "acme"}}, store.KeyDigest(acmeKey))
	b := db.Tenant(t, "beta", []store.Binding{{Host: "github.com", Owner: "beta"}}, store.KeyDigest(betaKey))
	ledger, err := store.Open(context.Background(), db.AppURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(ledger.Close)
	return &ledgerWorld{world: newWorld(t, ledger), db: db, a: a, b: b}
}

func (w *ledgerWorld) view(t *testing.T, bearer, repo, pr string) pullRequestView {
	t.Helper()
	got := w.send(http.MethodGet, "/v1/pulls/"+pr+"?repo="+repo, bearer, "", nil).want(t, http.StatusOK)
	var view pullRequestView
	if err := json.Unmarshal(got.body, &view); err != nil {
		t.Fatal(err)
	}
	return view
}

func (w *ledgerWorld) count(t *testing.T) int {
	t.Helper()
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, w.db.SuperURL)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close(ctx) }()
	var count int
	if err := conn.QueryRow(ctx, `SELECT count(*) FROM focus_rows`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}

func TestPostgresThePerPullRequestViewIsDerivedFromTheRows(t *testing.T) {
	w := postgresWorld(t)
	w.rows(w.token(), statingHead(t, readFile(t, goldenRows))).want(t, http.StatusOK)
	view := w.view(t, w.token(), acmeRepo, acmePull)
	if !view.Budget.Evidence || len(view.Budget.Spend) != 1 {
		t.Fatalf("%+v", view.Budget)
	}
	if got := view.Budget.Spend[0]; got.Currency != "USD" || got.ListCost != goldenTotal {
		t.Fatalf("%+v", got)
	}
	if len(view.Totals) != 1 || view.Totals[0].ListCost != goldenTotal || view.Totals[0].Label != "List-equivalent" {
		t.Fatalf("%+v", view.Totals)
	}
	rows := int64(0)
	for _, line := range view.Lines {
		rows += line.Rows
		if line.Label != "List-equivalent" || line.AttributionGrade != "exact" || line.RunIdentity != "token_verified" {
			t.Fatalf("%+v", line)
		}
		if line.EffectiveCost != "0" {
			t.Fatalf("a list-equivalent line shows an incurred cost: %+v", line)
		}
	}
	if rows != 19 {
		t.Fatalf("%d rows", rows)
	}
}

func TestPostgresABillingAccountRoundTrips(t *testing.T) {
	w := postgresWorld(t)
	stated := row("req-1", focus.Output, 100)
	stated["BillingAccountId"] = "acct-1"
	silent := row("req-2", focus.Output, 100)
	delete(silent, "BillingAccountId")
	w.rows(w.token(), lines(t, stated, silent)).want(t, http.StatusOK)
	view := w.view(t, w.token(), acmeRepo, acmePull)
	var accounts []string
	for _, line := range view.Lines {
		accounts = append(accounts, line.BillingAccountID)
	}
	if !slices.Equal(accounts, []string{"acct-1", "unknown"}) {
		t.Fatalf("the view reads %v", accounts)
	}
	if len(view.Totals) != 1 {
		t.Fatalf("an account split a total: %+v", view.Totals)
	}
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, w.db.SuperURL)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close(ctx) }()
	var column, document string
	err = conn.QueryRow(ctx, `SELECT billing_account_id, document->>'BillingAccountId' FROM focus_rows WHERE row_id = $1`,
		stated["x_CostroidRowId"]).Scan(&column, &document)
	if err != nil {
		t.Fatal(err)
	}
	if column != "acct-1" || document != "acct-1" {
		t.Fatalf("stored %q and %q", column, document)
	}
}

func TestPostgresTheMergeCommitAndTheStatedHeadKeepTheirOwnColumns(t *testing.T) {
	w := postgresWorld(t)
	w.rows(w.token(), lines(t, row("req-1", focus.Output, 100))).want(t, http.StatusOK)
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, w.db.SuperURL)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close(ctx) }()
	var head, token, basis, tag string
	err = conn.QueryRow(ctx, `SELECT commit_sha, token_commit_sha, head_basis, document->'Tags'->>'costroid/commit_sha' FROM focus_rows`).
		Scan(&head, &token, &basis, &tag)
	if err != nil {
		t.Fatal(err)
	}
	if head != acmeHead || tag != acmeHead || token != acmeSHA || basis != store.HeadStated {
		t.Fatalf("head %s, tag %s, token %s, basis %s", head, tag, token, basis)
	}
}

func TestPostgresARetriedBatchChangesNothing(t *testing.T) {
	w := postgresWorld(t)
	body := readFile(t, goldenRows)
	first := w.rows(w.token(), body).want(t, http.StatusOK)
	second := w.rows(w.token(), body).want(t, http.StatusOK)
	var a, b ingestAnswer
	if err := json.Unmarshal(first.body, &a); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(second.body, &b); err != nil {
		t.Fatal(err)
	}
	if a.Accepted != 19 || a.Duplicates != 0 || b.Accepted != 0 || b.Duplicates != 19 {
		t.Fatalf("%+v then %+v", a, b)
	}
	if got := w.view(t, w.token(), acmeRepo, acmePull).Budget.Spend[0].ListCost; got != goldenTotal {
		t.Fatalf("spend %s after a retry", got)
	}
}

func TestPostgresTheOTLPPathCollapsesOntoTheResultStreamsRows(t *testing.T) {
	w := postgresWorld(t)
	w.rows(w.token(), readFile(t, goldenRows)).want(t, http.StatusOK)
	body := goldenLogs(t, map[string]string{"service.name": "claude-code"})
	w.send(http.MethodPost, "/otlp/v1/logs", w.token(), "application/json", body).want(t, http.StatusOK)
	if got := w.count(t); got != 19 {
		t.Fatalf("%d rows", got)
	}
}

func TestPostgresATenantCannotReadAnotherTenantsPullRequest(t *testing.T) {
	w := postgresWorld(t)
	w.rows(w.token(), readFile(t, goldenRows)).want(t, http.StatusOK)
	if view := w.view(t, betaKey, acmeRepo, acmePull); len(view.Lines) != 0 {
		t.Fatalf("beta read acme's rows: %+v", view)
	}
	if view := w.view(t, acmeKey, acmeRepo, acmePull); len(view.Lines) == 0 {
		t.Fatal("acme lost its own rows")
	}
}

func TestPostgresAForgedIngestCannotReachAnotherTenant(t *testing.T) {
	w := postgresWorld(t)
	w.rows(w.token(), readFile(t, goldenRows)).want(t, http.StatusOK)

	forged := row("req-forged", focus.Output, 100)
	tags(forged)[store.TagRepo] = acmeRepo
	tags(forged)[store.TagPR] = acmePull
	tags(forged)[focus.TagRunIdentity] = "token_verified"
	tags(forged)[focus.TagAttributionGrade] = "exact"
	forged["tenant_id"] = w.a.String()

	betaToken := w.tokenFor("beta", "widgets")
	got := w.rows(betaToken, lines(t, forged)).want(t, http.StatusForbidden)
	if got.code(t) != codeJoinKey {
		t.Fatalf("code %s", got.code(t))
	}
	view := w.view(t, acmeKey, acmeRepo, acmePull)
	if spend := view.Budget.Spend[0].ListCost; spend != goldenTotal {
		t.Fatalf("spend moved to %s", spend)
	}
}

func TestPostgresAKeyHolderInOneTenantWritesOnlyToThatTenant(t *testing.T) {
	w := postgresWorld(t)
	forged := row("req-forged", focus.Output, 100)
	tags(forged)[store.TagRepo] = acmeRepo
	tags(forged)[store.TagPR] = acmePull
	w.rows(betaKey, lines(t, forged)).want(t, http.StatusOK)
	if view := w.view(t, acmeKey, acmeRepo, acmePull); len(view.Lines) != 0 {
		t.Fatalf("a beta key wrote into acme: %+v", view)
	}
}

func TestPostgresUnattributedRowsNeverReachABudget(t *testing.T) {
	w := postgresWorld(t)
	claimed := row("req-unverified", focus.Output, 1000000)
	tags(claimed)[store.TagRepo] = acmeRepo
	tags(claimed)[store.TagPR] = acmePull
	tags(claimed)[focus.TagAttributionGrade] = "exact"
	tags(claimed)[focus.TagRunIdentity] = "token_verified"
	w.rows(acmeKey, lines(t, claimed)).want(t, http.StatusOK)

	view := w.view(t, acmeKey, acmeRepo, acmePull)
	if view.Budget.Evidence || len(view.Budget.Spend) != 0 {
		t.Fatalf("%+v", view.Budget)
	}
	if len(view.Lines) != 1 || view.Lines[0].BudgetEligible || view.Lines[0].AttributionGrade != "unattributed" {
		t.Fatalf("%+v", view.Lines)
	}
	if len(view.Totals) != 1 || view.Totals[0].ListCost == "0" {
		t.Fatalf("the reading left out an unverified row: %+v", view.Totals)
	}

	w.rows(w.token(), readFile(t, goldenRows)).want(t, http.StatusOK)
	view = w.view(t, acmeKey, acmeRepo, acmePull)
	if !view.Budget.Evidence || view.Budget.Spend[0].ListCost != goldenTotal {
		t.Fatalf("the unattributed row moved a budget: %+v", view.Budget)
	}
}

func TestPostgresNoAddressOrContentIsStoredAnywhere(t *testing.T) {
	w := postgresWorld(t)
	body := telemetryWithContent(t, map[string]string{"user.email": "dev@example.com"})
	w.send(http.MethodPost, "/otlp/v1/logs", w.token(), "application/json", body).want(t, http.StatusOK)
	w.send(http.MethodPost, "/otlp/v1/logs", acmeKey, "application/json", body).want(t, http.StatusOK)
	r := row("req-content", focus.Output, 100)
	r["prompt"] = sentinel
	tags(r)["user.email"] = "dev@example.com"
	w.rows(acmeKey, lines(t, r)).want(t, http.StatusOK)

	ctx := context.Background()
	conn, err := pgx.Connect(ctx, w.db.SuperURL)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close(ctx) }()
	for _, table := range []string{"focus_rows", "ingest_drops", "tenants", "tenant_bindings", "ingest_keys"} {
		var dump string
		query := fmt.Sprintf(`SELECT coalesce(string_agg(to_jsonb(t)::text, ' '), '') FROM %s t`, table)
		if err := conn.QueryRow(ctx, query).Scan(&dump); err != nil {
			t.Fatal(err)
		}
		if strings.Contains(dump, sentinel) || strings.Contains(strings.ToLower(dump), "example.com") {
			t.Fatalf("%s holds content or an address", table)
		}
	}
	var dropped int64
	err = conn.QueryRow(ctx, `SELECT coalesce(sum(count), 0) FROM ingest_drops WHERE field = 'gen_ai.input.messages'`).Scan(&dropped)
	if err != nil {
		t.Fatal(err)
	}
	if dropped != 2 {
		t.Fatalf("counted %d drops", dropped)
	}
}

func TestPostgresTheActorIsStoredAsTheLoginOrThePseudonym(t *testing.T) {
	w := postgresWorld(t)
	body := telemetryWithContent(t, map[string]string{"user.email": "dev@example.com"})
	w.send(http.MethodPost, "/otlp/v1/logs", w.token(), "application/json", body).want(t, http.StatusOK)
	second := telemetryWithContent(t, map[string]string{"user.email": "dev@example.com", "request_id": "req_2"})
	w.send(http.MethodPost, "/otlp/v1/logs", acmeKey, "application/json", second).want(t, http.StatusOK)
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, w.db.SuperURL)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close(ctx) }()
	rows, err := conn.Query(ctx, `SELECT DISTINCT actor FROM focus_rows ORDER BY actor`)
	if err != nil {
		t.Fatal(err)
	}
	actors, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		t.Fatal(err)
	}
	if len(actors) != 2 || !strings.HasPrefix(actors[0], "hmac:") || actors[1] != "octo-dev" {
		t.Fatalf("%v", actors)
	}
}
