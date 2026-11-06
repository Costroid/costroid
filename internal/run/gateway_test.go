package run

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Costroid/costroid/internal/cicontext"
	"github.com/Costroid/costroid/internal/decimal"
	"github.com/Costroid/costroid/internal/gateway"
)

const (
	gatewayKey     = "sk-run-key-for-tests"
	oneRequest     = "one-request"
	oneRequestID   = "00000000-0000-4000-8000-000000000001"
	claimedKey     = "apikey_01SharedTeamKey"
	providerKey    = "sk-ant-provider-key"
	gatewayRun     = "16873409921"
	keyInfoHolding = `{"info":{"spend":0.00060589,"max_budget":0.01,"expires":"2099-01-01T00:00:00+00:00","max_parallel_requests":1,"metadata":{"costroid_run_id":"16873409921","costroid_max_request_tokens":"20000"}}}`
)

// fakeGateway answers the two routes the agent's job calls: its key's own
// info and the public readiness probe.
func fakeGateway(t *testing.T, keyInfo, readiness string) *gateway.Session {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/key/info":
			if r.Header.Get("Authorization") != "Bearer "+gatewayKey {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			w.Write([]byte(keyInfo))
		case "/health/readiness":
			w.Write([]byte(readiness))
		}
	}))
	t.Cleanup(server.Close)
	return &gateway.Session{
		Client: gateway.NewClient(server.URL), Key: gatewayKey,
		Wait: time.Second, Every: 20 * time.Millisecond, Poll: 10 * time.Millisecond, Settle: time.Second, Stable: 50 * time.Millisecond,
	}
}

func gatewayRunWith(t *testing.T, session *gateway.Session) (outcome, string) {
	t.Helper()
	log := installClaudeRun(t, oneRequest, oneRequestID)
	t.Setenv("ANTHROPIC_API_KEY", providerKey)
	var rows, stdout, stderr bytes.Buffer
	result, err := Run(context.Background(), Options{
		Argv:       []string{agentNamed(t, "claude"), "-p", "work"},
		Budget:     decimal.MustParse("0.01"),
		Credential: claimedKey,
		CI:         cicontext.Context{Run: gatewayRun},
		Rows:       &rows,
		Stdin:      strings.NewReader(""),
		Stdout:     &stdout,
		Stderr:     &stderr,
		Catalog:    pinned(t),
		Gateway:    session,
	})
	return outcome{result: result, stdout: stdout.String(), stderr: stderr.String(), err: err, rows: parseRows(t, rows.Bytes())}, log
}

func TestAGatewayKeyGradesGatewayPrecheckWithTheKeyIDAndTheBound(t *testing.T) {
	got, log := gatewayRunWith(t, fakeGateway(t, keyInfoHolding, `{"status":"healthy","db":"connected"}`))
	if got.err != nil || len(got.rows) == 0 {
		t.Fatalf("run: %v, %d rows, %s", got.err, len(got.rows), got.stderr)
	}
	for _, row := range got.rows {
		if tagOf(row, "costroid/enforcement_grade") != "gateway-precheck" {
			t.Errorf("grade %q: %v", tagOf(row, "costroid/enforcement_grade"), row["ChargeDescription"])
		}
		if row["SubAccountId"] != gateway.KeyID(gatewayKey) {
			t.Errorf("SubAccountId %v, want the key's id, never the key or a claimed credential", row["SubAccountId"])
		}
		// One request in flight and one not yet counted, each of 20000 tokens
		// at claude-haiku-5-5's highest price, output at USD 0.50 per million.
		if row["x_CostroidOvershootBound"] != "0.02" {
			t.Errorf("bound %v", row["x_CostroidOvershootBound"])
		}
		if tagOf(row, "costroid/attribution_grade") != "unattributed" {
			t.Errorf("rows the agent's job wrote read %q", tagOf(row, "costroid/attribution_grade"))
		}
	}
	env, err := os.ReadFile(log + ".anthropic")
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(string(env), "\n")
	if slices.Contains(lines, "ANTHROPIC_API_KEY="+providerKey) || !slices.Contains(lines, "ANTHROPIC_AUTH_TOKEN="+gatewayKey) {
		t.Errorf("the agent's credentials: %q", lines)
	}
	if strings.Contains(got.stdout+got.stderr, gatewayKey) {
		t.Error("the run printed the key")
	}
	if slices.ContainsFunc(got.result.Report.Warnings, func(w string) bool { return strings.Contains(w, "the gateway counted") }) {
		t.Errorf("a gateway that counted the rows' own cost was warned about: %q", got.result.Report.Warnings)
	}
}

// The recorded run's rows come to USD 0.00060589 at the catalog. A gateway
// that counted less held a budget in prices lower than the rows'.
func TestAGatewayThatCountsAnotherFigureIsNamedBesideTheRows(t *testing.T) {
	lower := strings.Replace(keyInfoHolding, `"spend":0.00060589`, `"spend":0.0004651`, 1)
	got, _ := gatewayRunWith(t, fakeGateway(t, lower, `{"status":"healthy","db":"connected"}`))
	if got.err != nil {
		t.Fatal(got.err)
	}
	want := "the gateway counted USD 0.0004651 on the run's key, and the rows, priced at the catalog, come to USD 0.00060589"
	if !slices.ContainsFunc(got.result.Report.Warnings, func(w string) bool { return strings.Contains(w, want) }) {
		t.Fatalf("warnings %q", got.result.Report.Warnings)
	}
	if got.result.ListTotal.String() != "0.00060589" {
		t.Errorf("the gateway's figure reached the rows: %s", got.result.ListTotal)
	}
	for _, row := range got.rows {
		if cost, _ := row["ListCost"].(string); strings.Contains(cost, "0.0004651") {
			t.Errorf("a row carries the gateway's figure: %v", row)
		}
	}
}

func TestAGatewayWhoseStoreFailsRecordsNone(t *testing.T) {
	got, _ := gatewayRunWith(t, fakeGateway(t, keyInfoHolding, `{"status":"healthy","db":"disconnected"}`))
	if got.err != nil || len(got.rows) == 0 {
		t.Fatalf("run: %v, %s", got.err, got.stderr)
	}
	for _, row := range got.rows {
		description, _ := row["ChargeDescription"].(string)
		if tagOf(row, "costroid/enforcement_grade") != "none" || !strings.Contains(description, "store") {
			t.Errorf("grade %q: %s", tagOf(row, "costroid/enforcement_grade"), description)
		}
		if row["x_CostroidOvershootBound"] != nil {
			t.Errorf("a none row states a bound: %v", row["x_CostroidOvershootBound"])
		}
	}
}

func TestAGatewayKeyOverTheRunsBudgetRecordsNone(t *testing.T) {
	over := strings.Replace(keyInfoHolding, `"max_budget":0.01`, `"max_budget":5`, 1)
	got, _ := gatewayRunWith(t, fakeGateway(t, over, `{"status":"healthy","db":"connected"}`))
	if got.err != nil || tagOf(got.rows[0], "costroid/enforcement_grade") != "none" {
		t.Fatalf("run: %v, grade %q", got.err, tagOf(got.rows[0], "costroid/enforcement_grade"))
	}
}

func TestAKeyTheGatewayNeverRegistersStopsTheRunBeforeTheAgent(t *testing.T) {
	session := fakeGateway(t, keyInfoHolding, `{"db":"connected"}`)
	session.Key = "sk-never-registered"
	got, log := gatewayRunWith(t, session)
	if got.err == nil || len(got.rows) != 0 {
		t.Fatalf("run: %v, %d rows", got.err, len(got.rows))
	}
	if _, err := os.Stat(log + ".starts"); err == nil {
		t.Error("the agent started without a registered key")
	}
}

// A credential the caller only states is the rows' credential as before, and
// the grade it gets is the agent's own.
func TestAClaimedCredentialStaysAsStated(t *testing.T) {
	installClaudeRun(t, oneRequest, oneRequestID)
	var rows bytes.Buffer
	_, err := Run(context.Background(), Options{
		Argv: []string{agentNamed(t, "claude"), "-p", "work"}, Budget: decimal.MustParse("5"), Credential: claimedKey,
		Rows: &rows, Stdin: strings.NewReader(""), Stdout: &bytes.Buffer{}, Stderr: &bytes.Buffer{}, Catalog: pinned(t),
	})
	if err != nil {
		t.Fatal(err)
	}
	row := parseRows(t, rows.Bytes())[0]
	if row["SubAccountId"] != claimedKey || tagOf(row, "costroid/enforcement_grade") != "agent-flag" || row["x_CostroidOvershootBound"] != nil {
		t.Fatalf("row %v", row)
	}
}

// keyInfoWith is a holding key with another budget and largest request.
func keyInfoWith(budget, requestTokens string) string {
	return strings.NewReplacer(`"max_budget":0.01`, `"max_budget":`+budget, `"costroid_max_request_tokens":"20000"`, `"costroid_max_request_tokens":"`+requestTokens+`"`).Replace(keyInfoHolding)
}

// The recorded run's rows come to USD 0.00060589. On a key of USD 0.0001 that
// is an overshoot of USD 0.00050589.
func TestAnOvershootWithinTheBoundKeepsGatewayPrecheck(t *testing.T) {
	got, _ := gatewayRunWith(t, fakeGateway(t, keyInfoWith("0.0001", "20000"), `{"status":"healthy","db":"connected"}`))
	if got.err != nil {
		t.Fatal(got.err)
	}
	for _, row := range got.rows {
		if tagOf(row, "costroid/enforcement_grade") != "gateway-precheck" || row["x_CostroidOvershootBound"] != "0.02" {
			t.Fatalf("an overshoot of USD 0.00050589 under a bound of USD 0.02 reads %q, bound %v", tagOf(row, "costroid/enforcement_grade"), row["x_CostroidOvershootBound"])
		}
	}
	if slices.ContainsFunc(got.result.Report.Warnings, func(w string) bool { return strings.Contains(w, "above the bound") }) {
		t.Errorf("warnings %q", got.result.Report.Warnings)
	}
}

// With a largest request of 100 tokens the bound is 2 × 100 × USD 0.50 per
// million, USD 0.0001, and the run passed its key by more.
func TestAnOvershootAboveTheBoundRecordsNone(t *testing.T) {
	got, _ := gatewayRunWith(t, fakeGateway(t, keyInfoWith("0.0001", "100"), `{"status":"healthy","db":"connected"}`))
	if got.err != nil {
		t.Fatal(got.err)
	}
	want := "an overshoot of USD 0.00050589, above the bound of USD 0.0001"
	if !slices.ContainsFunc(got.result.Report.Warnings, func(w string) bool { return strings.Contains(w, want) }) {
		t.Fatalf("warnings %q", got.result.Report.Warnings)
	}
	if got.result.Enforcement.Grade != "none" {
		t.Errorf("the result reads %q", got.result.Enforcement.Grade)
	}
	for _, row := range got.rows {
		description, _ := row["ChargeDescription"].(string)
		if tagOf(row, "costroid/enforcement_grade") != "none" || !strings.Contains(description, "more than the overshoot bound of USD 0.0001") {
			t.Errorf("grade %q: %s", tagOf(row, "costroid/enforcement_grade"), description)
		}
		if row["x_CostroidOvershootBound"] != nil {
			t.Errorf("a none row states a bound: %v", row["x_CostroidOvershootBound"])
		}
	}
}
