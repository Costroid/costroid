package server

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"maps"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/Costroid/costroid/internal/catalog"
	"github.com/Costroid/costroid/internal/focus"
	"github.com/Costroid/costroid/internal/oidc"
	"github.com/Costroid/costroid/internal/oidc/oidctest"
	"github.com/Costroid/costroid/internal/store"
	"github.com/Costroid/costroid/internal/store/storetest"
)

const (
	audience   = "https://costroid.test"
	acmeRepo   = "github.com/acme/widgets"
	acmeRun    = "16873409921"
	acmeSHA    = "9f2c6e1ab47d03f58c1e7726d0b4a93e5f618c2d"
	acmeHead   = "1d4e7a90c3b25f8866a0e4d17c92b35fa8e6d071"
	acmePull   = "1423"
	goldenRows = "testdata/claude-code-2.1.316-subagent.rows.jsonl"
	goldenOTLP = "../adapter/claudecode/testdata/2.1.316/subagent/otel-api-requests.jsonl"
	// goldenTotal is Claude Code's own total_cost_usd on the recorded run,
	// which the catalog reproduces. See docs/adapters.md.
	goldenTotal = "0.001619725"
	acmeKey     = "costroid_ik_acme-test-key"
	betaKey     = "costroid_ik_beta-test-key"
	testMaster  = "a master key used by these tests only"
)

var testNow = time.Date(2026, 11, 2, 9, 0, 0, 0, time.UTC)

func TestMain(m *testing.M) {
	os.Exit(storetest.Main(m))
}

func tenantID(t testing.TB, text string) store.TenantID {
	t.Helper()
	id, err := store.ParseTenantID(text)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

// memoryLedger is the store's contract without Postgres, so the edge's rules
// run in the short suite too.
type memoryLedger struct {
	owners map[string]store.TenantID
	keys   map[string]store.TenantID
	rows   map[store.TenantID]map[string]store.Record
	drops  map[store.TenantID]map[string]int64
}

func newMemoryLedger() *memoryLedger {
	return &memoryLedger{
		owners: map[string]store.TenantID{}, keys: map[string]store.TenantID{},
		rows: map[store.TenantID]map[string]store.Record{}, drops: map[store.TenantID]map[string]int64{},
	}
}

func (m *memoryLedger) TenantForOwner(_ context.Context, host, owner string) (store.TenantID, error) {
	if id, ok := m.owners[host+":"+owner]; ok {
		return id, nil
	}
	return store.TenantID{}, store.ErrNotFound
}

func (m *memoryLedger) TenantForKey(_ context.Context, sum string) (store.TenantID, error) {
	if id, ok := m.keys[sum]; ok {
		return id, nil
	}
	return store.TenantID{}, store.ErrNotFound
}

func (m *memoryLedger) Ingest(_ context.Context, tenant store.TenantID, batch store.Batch) (store.Receipt, error) {
	if m.rows[tenant] == nil {
		m.rows[tenant] = map[string]store.Record{}
		m.drops[tenant] = map[string]int64{}
	}
	var receipt store.Receipt
	for _, record := range batch.Records {
		if _, seen := m.rows[tenant][record.Row.RowID]; seen {
			receipt.Duplicates++
			continue
		}
		m.rows[tenant][record.Row.RowID] = record
		receipt.Inserted++
	}
	for field, count := range batch.Drops {
		m.drops[tenant][field] += count
	}
	return receipt, nil
}

func (m *memoryLedger) PullRequest(context.Context, store.TenantID, string, string) ([]store.Line, error) {
	return nil, nil
}

// world is a service, its stub issuer and its tokens.
type world struct {
	t      testing.TB
	issuer *oidctest.Issuer
	server *Server
	http   *httptest.Server
}

func newWorld(t testing.TB, ledger Ledger) *world {
	t.Helper()
	pinned, err := catalog.Load()
	if err != nil {
		t.Fatal(err)
	}
	issuer := oidctest.New(t)
	verifier := oidc.NewVerifier(audience, issuer.Server.Client(), func() time.Time { return testNow },
		oidc.Issuer{Kind: oidc.GitHubActions, URL: issuer.URL(), JWKSURL: issuer.URL() + "/keys", Host: "github.com"})
	service := New(Config{
		Ledger: ledger, Verifier: verifier, Catalog: pinned, PseudonymKey: []byte(testMaster),
		Now: func() time.Time { return testNow }, Logger: slog.New(slog.DiscardHandler),
	})
	server := httptest.NewServer(service.Handler())
	t.Cleanup(server.Close)
	return &world{t: t, issuer: issuer, server: service, http: server}
}

func (w *world) claims() map[string]any {
	return map[string]any{
		"iss": w.issuer.URL(), "aud": audience, "exp": testNow.Add(5 * time.Minute).Unix(),
		"repository": "acme/widgets", "repository_owner": "acme", "run_id": acmeRun, "run_attempt": "1",
		"sha": acmeSHA, "actor": "octo-dev", "event_name": "pull_request", "ref": "refs/pull/" + acmePull + "/merge",
	}
}

func (w *world) token() string {
	return w.issuer.Token(w.claims())
}

// tokenFor is a valid token for another repository under another owner.
func (w *world) tokenFor(owner, repo string) string {
	claims := w.claims()
	claims["repository"] = owner + "/" + repo
	claims["repository_owner"] = owner
	return w.issuer.Token(claims)
}

type answer struct {
	status int
	body   []byte
}

func (w *world) send(method, path, bearer, contentType string, body []byte, headers ...string) answer {
	w.t.Helper()
	request, err := http.NewRequest(method, w.http.URL+path, bytes.NewReader(body))
	if err != nil {
		w.t.Fatal(err)
	}
	if bearer != "" {
		request.Header.Set("Authorization", "Bearer "+bearer)
	}
	if contentType != "" {
		request.Header.Set("Content-Type", contentType)
	}
	for i := 0; i+1 < len(headers); i += 2 {
		request.Header.Set(headers[i], headers[i+1])
	}
	response, err := w.http.Client().Do(request)
	if err != nil {
		w.t.Fatal(err)
	}
	defer func() { _ = response.Body.Close() }()
	data, err := io.ReadAll(response.Body)
	if err != nil {
		w.t.Fatal(err)
	}
	return answer{status: response.StatusCode, body: data}
}

func (w *world) rows(bearer string, body []byte) answer {
	w.t.Helper()
	return w.send(http.MethodPost, "/v1/rows", bearer, "application/x-ndjson", body)
}

func (a answer) code(t testing.TB) string {
	t.Helper()
	var refusal struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(a.body, &refusal); err != nil {
		t.Fatalf("status %d, body %s", a.status, a.body)
	}
	return refusal.Error.Code
}

func (a answer) want(t testing.TB, status int) answer {
	t.Helper()
	if a.status != status {
		t.Fatalf("status %d, want %d: %s", a.status, status, a.body)
	}
	return a
}

// row is a CLI row for one request and class, as a mutable map.
func row(id string, class focus.TokenClass, tokens int) map[string]any {
	return map[string]any{
		"BilledCost": "0", "BillingAccountId": "unknown", "BillingCurrency": "USD",
		"ChargeCategory": "Usage", "ChargeDescription": "claude-opus-5-5 output tokens", "ChargeFrequency": "Usage-Based",
		"ChargePeriodStart": "2026-11-01T10:00:00Z", "ChargePeriodEnd": "2026-11-01T10:00:01Z",
		"ConsumedQuantity": fmt.Sprint(tokens), "ConsumedUnit": "Tokens",
		"ContractedCost": "0", "ContractedUnitPrice": "0", "EffectiveCost": "0", "ListCost": "0", "ListUnitPrice": "0",
		"SkuId":      "anthropic/claude-opus-5-5/" + string(class),
		"SkuMeter":   string(class),
		"SkuPriceId": "anthropic:claude-opus-5-5:global:standard:all:" + string(class),
		"Tags": map[string]any{
			focus.TagAgent: "claude-code", focus.TagModel: "claude-opus-5-5", focus.TagCostSource: "agent_stream",
			focus.TagCostBasis: "measured", focus.TagAttributionGrade: "unattributed", focus.TagEnforcementGrade: "agent-flag",
			focus.TagRunIdentity: "unverified", focus.TagSessionID: "session-1", store.TagCommitSHA: acmeHead,
		},
		"x_CostroidRowId": focus.RowIdentity(id, class),
	}
}

func tags(r map[string]any) map[string]any {
	value, _ := r["Tags"].(map[string]any)
	return value
}

func lines(t testing.TB, rows ...map[string]any) []byte {
	t.Helper()
	var buffer bytes.Buffer
	encoder := json.NewEncoder(&buffer)
	for _, r := range rows {
		if err := encoder.Encode(r); err != nil {
			t.Fatal(err)
		}
	}
	return buffer.Bytes()
}

// statingHead adds to recorded rows the head a wrapper would state.
func statingHead(t testing.TB, body []byte) []byte {
	t.Helper()
	var rows []map[string]any
	for line := range bytes.Lines(body) {
		var r map[string]any
		if err := json.Unmarshal(line, &r); err != nil {
			t.Fatal(err)
		}
		tags(r)[store.TagCommitSHA] = acmeHead
		rows = append(rows, r)
	}
	return lines(t, rows...)
}

func clone(r map[string]any) map[string]any {
	copied := maps.Clone(r)
	copied["Tags"] = maps.Clone(tags(r))
	return copied
}

func readFile(t testing.TB, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

// claudeLogs wraps OTLP/JSON log attributes for one Claude Code resource.
func claudeLogs(t testing.TB, resource map[string]string, records ...map[string]string) []byte {
	t.Helper()
	encode := func(pairs map[string]string) []map[string]any {
		list := make([]map[string]any, 0, len(pairs))
		for key, value := range pairs {
			list = append(list, map[string]any{"key": key, "value": map[string]any{"stringValue": value}})
		}
		return list
	}
	entries := make([]map[string]any, 0, len(records))
	for _, record := range records {
		entries = append(entries, map[string]any{
			"timeUnixNano": "1793523600000000000",
			"body":         map[string]any{"stringValue": "claude_code.api_request"},
			"attributes":   encode(record),
		})
	}
	body, err := json.Marshal(map[string]any{"resourceLogs": []any{map[string]any{
		"resource":  map[string]any{"attributes": encode(resource)},
		"scopeLogs": []any{map[string]any{"logRecords": entries}},
	}}})
	if err != nil {
		t.Fatal(err)
	}
	return body
}

// goldenLogs turns the recorded run's four api_request events into OTLP.
func goldenLogs(t testing.TB, resource map[string]string) []byte {
	t.Helper()
	var records []map[string]string
	for line := range strings.Lines(string(readFile(t, goldenOTLP))) {
		var fixture map[string]any
		if err := json.Unmarshal([]byte(line), &fixture); err != nil {
			t.Fatal(err)
		}
		record := map[string]string{"event.name": "api_request", "model": "claude-haiku-5-5", "session.id": "00000000-0000-4000-8000-000000000002"}
		for key, value := range fixture {
			record[key] = fmt.Sprint(value)
		}
		records = append(records, record)
	}
	return claudeLogs(t, resource, records...)
}
