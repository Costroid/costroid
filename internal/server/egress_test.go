package server

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"github.com/santhosh-tekuri/jsonschema/v6"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Costroid/costroid/internal/cli"
	"github.com/Costroid/costroid/internal/egress"
	"github.com/Costroid/costroid/internal/focus"
	"github.com/Costroid/costroid/internal/oidc"
	"github.com/Costroid/costroid/internal/store"
)

const issuerHost = "127.0.0.1"

// failingSink stands in for a database that is away.
type failingSink struct{}

func (failingSink) AddEgress(context.Context, []egress.Count) error {
	return errors.New("the database is away")
}

// gated puts a service's key reads behind a list.
func (w *world) gated(allow []string, sink egress.Sink) *egress.Counters {
	counters := egress.NewCounters(sink, func() time.Time { return testNow }, slog.New(slog.DiscardHandler))
	gate := egress.New(allow, counters)
	w.server.Verifier = oidc.NewVerifier(audience, gate.Client(5*time.Second), func() time.Time { return testNow },
		oidc.Issuer{Kind: oidc.GitHubActions, URL: w.issuer.URL(), JWKSURL: w.issuer.URL() + "/keys", Host: "github.com"})
	return counters
}

func TestATokenFromAnIssuerOffTheListAnswersATypedRefusal(t *testing.T) {
	w, ledger := memoryWorld(t)
	w.gated(nil, failingSink{})
	got := w.rows(w.token(), lines(t, row("req-1", focus.Output, 100))).want(t, http.StatusServiceUnavailable)
	if got.code(t) != codeEgress {
		t.Fatalf("%s", got.body)
	}
	for _, want := range []string{issuerHost, egress.Flag} {
		if !strings.Contains(string(got.body), want) {
			t.Errorf("the refusal lacks %q: %s", want, got.body)
		}
	}
	if len(ledger.rows) != 0 {
		t.Fatal("a refused request stored rows")
	}
	if hits := w.issuer.Hits(); hits != 0 {
		t.Fatalf("%d requests reached the issuer", hits)
	}
}

func TestEveryPathTakingATokenAnswersTheSameRefusal(t *testing.T) {
	w, _ := memoryWorld(t)
	w.gated(nil, failingSink{})
	for _, request := range []struct{ method, path string }{
		{http.MethodPost, "/v1/rows"},
		{http.MethodPost, "/otlp/v1/logs"},
		{http.MethodPost, "/otlp/v1/traces"},
		{http.MethodGet, "/v1/pulls/1?repo=" + acmeRepo},
	} {
		got := w.send(request.method, request.path, w.token(), "application/json", []byte(`{}`))
		if got.status != http.StatusServiceUnavailable || got.code(t) != codeEgress {
			t.Errorf("%s %s: %d %s", request.method, request.path, got.status, got.body)
		}
	}
}

func TestATokenFromAListedIssuerIsVerified(t *testing.T) {
	w, ledger := memoryWorld(t)
	w.gated([]string{issuerHost}, failingSink{})
	w.rows(w.token(), lines(t, row("req-1", focus.Output, 100))).want(t, http.StatusOK)
	if run := ledger.only(t, tenantA).Row.Tags[store.TagWorkflowRunID]; run != acmeRun {
		t.Fatalf("run %q", run)
	}
}

func TestAnIngestKeyNeedsNothingOnTheList(t *testing.T) {
	w, ledger := memoryWorld(t)
	w.gated(nil, failingSink{})
	w.rows(acmeKey, lines(t, row("req-1", focus.Output, 100))).want(t, http.StatusOK)
	ledger.only(t, tenantA)
}

func TestATokenNamingNoTrustedIssuerAnswersAsAnyBadToken(t *testing.T) {
	w, _ := memoryWorld(t)
	w.gated(nil, failingSink{})
	claims := w.claims()
	claims["iss"] = "https://nobody.example"
	got := w.rows(w.issuer.Token(claims), lines(t, row("req-1", focus.Output, 100))).want(t, http.StatusUnauthorized)
	if got.code(t) != codeAuth {
		t.Fatalf("%s", got.body)
	}
}

type logEntry struct {
	Message string   `json:"msg"`
	Level   string   `json:"level"`
	Hosts   []string `json:"hosts"`
	Issuer  string   `json:"issuer"`
	Host    string   `json:"host"`
}

func logged(t *testing.T, allow []string, gitlabHosts ...string) []logEntry {
	t.Helper()
	var buffer bytes.Buffer
	gate := egress.New(allow, failingCounters())
	logAllowlist(slog.New(slog.NewJSONHandler(&buffer, nil)), gate, issuersOf(serveOptions{gitlabHosts: gitlabHosts}))
	var entries []logEntry
	decoder := json.NewDecoder(&buffer)
	for decoder.More() {
		var entry logEntry
		if err := decoder.Decode(&entry); err != nil {
			t.Fatal(err)
		}
		entries = append(entries, entry)
	}
	return entries
}

func failingCounters() *egress.Counters {
	return egress.NewCounters(failingSink{}, time.Now, slog.New(slog.DiscardHandler))
}

func TestServeLogsTheListOnceAndNamesEachIssuerItMisses(t *testing.T) {
	entries := logged(t, nil, "gitlab.example")
	if len(entries) != 4 || entries[0].Message != "egress allowlist" || len(entries[0].Hosts) != 0 {
		t.Fatalf("%+v", entries)
	}
	var missed []string
	for _, entry := range entries[1:] {
		if entry.Level != "WARN" {
			t.Errorf("%+v", entry)
		}
		missed = append(missed, entry.Host)
	}
	want := []string{"token.actions.githubusercontent.com", "gitlab.com", "gitlab.example"}
	if !slices.Equal(missed, want) {
		t.Fatalf("named %v, want %v", missed, want)
	}
}

func TestServeNamesAHostOnce(t *testing.T) {
	if entries := logged(t, nil, "gitlab.com"); len(entries) != 3 {
		t.Fatalf("%+v", entries)
	}
}

func TestServeNamesNoIssuerTheListCovers(t *testing.T) {
	entries := logged(t, []string{"TOKEN.actions.githubusercontent.com.", "gitlab.com"})
	if len(entries) != 1 || !slices.Equal(entries[0].Hosts, []string{"gitlab.com", "token.actions.githubusercontent.com"}) {
		t.Fatalf("%+v", entries)
	}
}

func TestServeReadsTheFlagAndTheVariable(t *testing.T) {
	t.Setenv(envEgress, "gitlab.com, token.actions.githubusercontent.com")
	var stderr bytes.Buffer
	options, err := parseServe([]string{"--egress-allow", "gitlab.example", "--egress-allow", "gitlab.internal"}, &stderr)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"gitlab.example", "gitlab.internal", "gitlab.com", "token.actions.githubusercontent.com"}
	if !slices.Equal(options.egressAllow, want) {
		t.Fatalf("%v", options.egressAllow)
	}
}

func TestServeAllowsNothingByDefault(t *testing.T) {
	t.Setenv(envEgress, "")
	var stderr bytes.Buffer
	options, err := parseServe(nil, &stderr)
	if err != nil {
		t.Fatal(err)
	}
	if len(options.egressAllow) != 0 {
		t.Fatalf("%v", options.egressAllow)
	}
}

func TestServeRefusesAWildcardOnTheList(t *testing.T) {
	long := base64.StdEncoding.EncodeToString(make([]byte, minPseudonymKeyBytes))
	t.Setenv(envDatabase, "postgres://x")
	t.Setenv(envAudience, "aud")
	t.Setenv(envPseudonym, long)
	for name, setup := range map[string]func() []string{
		"flag":     func() []string { t.Setenv(envEgress, ""); return []string{"--egress-allow", "*.github.com"} },
		"variable": func() []string { t.Setenv(envEgress, "*.github.com"); return nil },
	} {
		t.Run(name, func(t *testing.T) {
			var stderr bytes.Buffer
			if code := Main(context.Background(), setup(), new(bytes.Buffer), &stderr); code != cli.ExitCouldNot {
				t.Fatalf("exit %d", code)
			}
			if !strings.Contains(stderr.String(), "wildcard") {
				t.Fatalf("stderr %q", stderr.String())
			}
		})
	}
}

func TestTheReportPrintsOneLinePerDayHostAndOutcome(t *testing.T) {
	var out bytes.Buffer
	err := printEgress(&out, []egress.Count{
		{Day: "2026-11-02", Host: "gitlab.com", Outcome: egress.Refused, Count: 3},
		{Day: "2026-11-01", Host: "token.actions.githubusercontent.com", Outcome: egress.Completed, Count: 12},
	})
	if err != nil {
		t.Fatal(err)
	}
	want := `DAY         HOST                                 OUTCOME    COUNT
2026-11-02  gitlab.com                           refused    3
2026-11-01  token.actions.githubusercontent.com  completed  12
`
	if out.String() != want {
		t.Fatalf("%q", out.String())
	}
}

func TestAnEmptyReportSaysSo(t *testing.T) {
	var out bytes.Buffer
	if err := printEgress(&out, nil); err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(out.String()) != noEgress {
		t.Fatalf("%q", out.String())
	}
}

func TestEgressNeedsACommandItKnows(t *testing.T) {
	var stdout, stderr bytes.Buffer
	for _, args := range [][]string{nil, {"delete"}} {
		if code := EgressMain(context.Background(), args, &stdout, &stderr); code != cli.ExitUsage {
			t.Errorf("%v: exit %d", args, code)
		}
	}
	if code := EgressMain(context.Background(), []string{"--help"}, &stdout, &stderr); code != 0 {
		t.Errorf("help: exit %d", code)
	}
}

func TestPostgresARefusedDestinationReachesTheReport(t *testing.T) {
	w := postgresWorld(t)
	ledger, err := store.Open(context.Background(), w.db.AppURL)
	if err != nil {
		t.Fatal(err)
	}
	defer ledger.Close()
	counters := w.gated(nil, ledger)
	w.rows(w.token(), lines(t, row("req-1", focus.Output, 100))).want(t, http.StatusServiceUnavailable)
	w.rows(w.token(), lines(t, row("req-2", focus.Output, 100))).want(t, http.StatusServiceUnavailable)
	if err := counters.Flush(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Setenv(envDatabase, w.db.AppURL)
	var stdout, stderr bytes.Buffer
	if code := EgressMain(context.Background(), []string{"report", "--json"}, &stdout, &stderr); code != 0 {
		t.Fatalf("exit %d: %s", code, stderr.String())
	}
	var report struct {
		Counters []egress.Count `json:"counters"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &report); err != nil {
		t.Fatal(err)
	}
	want := []egress.Count{{Day: "2026-11-02", Host: issuerHost, Outcome: egress.Refused, Count: 2}}
	if !slices.Equal(report.Counters, want) {
		t.Fatalf("%+v", report.Counters)
	}
	if strings.Contains(stdout.String(), "/keys") {
		t.Fatalf("a path reached the report: %s", stdout.String())
	}
}

func TestPostgresAnEmptyReportIsAnEmptyList(t *testing.T) {
	w := postgresWorld(t)
	t.Setenv(envDatabase, w.db.AppURL)
	var stdout, stderr bytes.Buffer
	if code := EgressMain(context.Background(), []string{"report", "--json"}, &stdout, &stderr); code != 0 {
		t.Fatalf("exit %d: %s", code, stderr.String())
	}
	if strings.TrimSpace(stdout.String()) != `{"schema":"costroid.egress/0.1","counters":[]}` {
		t.Fatalf("%q", stdout.String())
	}
	stdout.Reset()
	if code := EgressMain(context.Background(), []string{"report"}, &stdout, &stderr); code != 0 {
		t.Fatalf("exit %d: %s", code, stderr.String())
	}
	if strings.TrimSpace(stdout.String()) != noEgress {
		t.Fatalf("%q", stdout.String())
	}
}

// TestTheReportsObjectNamesItsSchemaAndHoldsToIt holds what egress report
// --json prints: one object on one line that opens with its name, the
// counters in the order they were read, and an empty list, never null, when
// no call has been recorded. The JSON Schema beside the commands' tests
// accepts it, and rejects it under another name, without one, and with an
// outcome a counter cannot have.
func TestTheReportsObjectNamesItsSchemaAndHoldsToIt(t *testing.T) {
	var out bytes.Buffer
	err := printEgressObject(&out, []egress.Count{
		{Day: "2026-11-02", Host: "gitlab.com", Outcome: egress.Refused, Count: 3},
		{Day: "2026-11-01", Host: "token.actions.githubusercontent.com", Outcome: egress.Completed, Count: 12},
	})
	want := `{"schema":"costroid.egress/0.1","counters":[{"day":"2026-11-02","host":"gitlab.com","outcome":"refused","count":3},{"day":"2026-11-01","host":"token.actions.githubusercontent.com","outcome":"completed","count":12}]}` + "\n"
	if err != nil || out.String() != want {
		t.Fatalf("%v: %q, want %q", err, out.String(), want)
	}
	var none bytes.Buffer
	if err := printEgressObject(&none, nil); err != nil || none.String() != `{"schema":"costroid.egress/0.1","counters":[]}`+"\n" {
		t.Fatalf("no counters: %v: %q", err, none.String())
	}

	file, err := os.Open(filepath.Join("..", "..", "cmd", "costroid", "testdata", "objects", "egress.schema.json"))
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	document, err := jsonschema.UnmarshalJSON(file)
	if err != nil {
		t.Fatal(err)
	}
	compiler := jsonschema.NewCompiler()
	if err := compiler.AddResource("egress.schema.json", document); err != nil {
		t.Fatal(err)
	}
	schema, err := compiler.Compile("egress.schema.json")
	if err != nil {
		t.Fatal(err)
	}
	valid := func(object string) error {
		instance, err := jsonschema.UnmarshalJSON(strings.NewReader(object))
		if err != nil {
			return err
		}
		return schema.Validate(instance)
	}
	for _, object := range []string{want, none.String()} {
		if err := valid(object); err != nil {
			t.Errorf("the schema rejects what the report prints: %v\n%s", err, object)
		}
	}
	for name, object := range map[string]string{
		"another name":       strings.Replace(want, egressSchema, "costroid.other/9.9", 1),
		"no name":            strings.Replace(want, `"schema"`, `"name"`, 1),
		"an unknown outcome": strings.Replace(want, `"refused"`, `"dropped"`, 1),
	} {
		if valid(object) == nil {
			t.Errorf("the schema accepts the object with %s", name)
		}
	}
}
