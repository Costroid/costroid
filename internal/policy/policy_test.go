package policy

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/open-policy-agent/opa/v1/ast"

	"github.com/Costroid/costroid/internal/decimal"
	"github.com/Costroid/costroid/internal/scm"
)

const (
	head    = "9f2c6e1ab47d03f58c1e7726d0b4a93e5f618c2d"
	base    = "1111111111111111111111111111111111111111"
	session = "00000000-0000-4000-8000-000000000002"
)

var checkTime = time.Date(2026, 11, 6, 12, 0, 0, 0, time.UTC)

// line is one row as costroid run writes it, reduced to the columns the
// policy reads, with tags the job could have forged.
type line struct {
	currency, cost, credential string
	tags                       map[string]string
}

func rowsOf(t *testing.T, lines ...line) string {
	t.Helper()
	var out strings.Builder
	for _, l := range lines {
		tags := map[string]string{"costroid/pr": "7", "costroid/commit_sha": head, "costroid/session_id": session}
		for key, value := range l.tags {
			tags[key] = value
		}
		row := map[string]any{"BillingCurrency": l.currency, "ListCost": l.cost, "SubAccountId": nil, "Tags": tags}
		if l.credential != "" {
			row["SubAccountId"] = l.credential
		}
		encoded, err := json.Marshal(row)
		if err != nil {
			t.Fatal(err)
		}
		out.Write(encoded)
		out.WriteByte('\n')
	}
	return out.String()
}

func usageOf(t *testing.T, lines ...line) Usage {
	t.Helper()
	usage, err := ReadUsage(strings.NewReader(rowsOf(t, lines...)))
	if err != nil {
		t.Fatal(err)
	}
	return usage
}

func config(body string) ConfigFile {
	return ConfigFile{Data: []byte(body), Found: true}
}

func evaluate(t *testing.T, request Request) Verdict {
	t.Helper()
	if request.At.IsZero() {
		request.At = checkTime
	}
	verdict, err := Evaluate(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	return verdict
}

func result(t *testing.T, verdict Verdict, rule string) Result {
	t.Helper()
	for _, r := range verdict.Results {
		if r.Rule == rule {
			return r
		}
	}
	t.Fatalf("no %s result in %+v", rule, verdict.Results)
	return Result{}
}

func TestCapabilitiesRefuseNetwork(t *testing.T) {
	calls := map[string]string{
		"http.send":            `http.send({"method": "GET", "url": "https://example.com"})`,
		"net.lookup_ip_addr":   `net.lookup_ip_addr("example.com")`,
		"net.cidr_contains":    `net.cidr_contains("10.0.0.0/8", "10.0.0.1")`,
		"opa.runtime":          `opa.runtime()`,
		"time.now_ns":          `time.now_ns()`,
		"rand.intn":            `rand.intn("seed", 10)`,
		"uuid.rfc4122":         `uuid.rfc4122("seed")`,
		"io.jwt.decode_verify": `io.jwt.decode_verify("a.b.c", {"cert": ""})`,
		"trace":                `trace("x")`,
	}
	for name, call := range calls {
		t.Run(name, func(t *testing.T) {
			source := "package costroid\n\nverdict := " + call + "\n"
			_, err := load(context.Background(), "refused.rego", []byte(source))
			if err == nil || !strings.Contains(err.Error(), "undefined function") {
				t.Fatalf("a pack that calls %s loaded: %v", name, err)
			}
		})
	}
	capabilities, err := ast.LoadCapabilitiesJSON(bytes.NewReader(capabilitiesFile))
	if err != nil {
		t.Fatal(err)
	}
	if capabilities.AllowNet == nil || len(capabilities.AllowNet) != 0 {
		t.Errorf("allow_net is %v, not empty", capabilities.AllowNet)
	}
	pinned := ast.BuiltinMap
	for _, builtin := range capabilities.Builtins {
		known, found := pinned[builtin.Name]
		switch {
		case !found:
			t.Errorf("%s is not a built-in of the pinned OPA", builtin.Name)
		case known.Nondeterministic:
			t.Errorf("%s gives a different answer on each call", builtin.Name)
		case builtin.Name == "http.send" || builtin.Name == "opa.runtime" || builtin.Name == "time.now_ns" ||
			strings.HasPrefix(builtin.Name, "net.") || strings.HasPrefix(builtin.Name, "rand."):
			t.Errorf("%s is in the capabilities file", builtin.Name)
		}
	}
	if _, err := defaultPack(); err != nil {
		t.Fatalf("the default pack does not load under the file: %v", err)
	}
}

// files is a host that holds costroid.yml at two commits and records the
// commits it was asked for.
type files struct {
	held map[string]string
	read []string
}

func (f *files) ReadFile(_ context.Context, path, ref string) ([]byte, error) {
	f.read = append(f.read, ref)
	data, found := f.held[ref]
	if path != ConfigPath || !found {
		return nil, scm.ErrNotFound
	}
	return []byte(data), nil
}

func TestBaseBranchConfig(t *testing.T) {
	over := usageOf(t, line{"USD", "3", "key-1", nil})
	baseFile := "policy:\n  run_budget_usd: \"2\"\n  enforce: true\n"
	for name, headFile := range map[string]string{
		"the head raises the budget":    "policy:\n  run_budget_usd: \"1000\"\n  enforce: true\n",
		"the head deletes the budget":   "comment:\n  show_cost: true\n",
		"the head returns to warn-only": "policy:\n  run_budget_usd: \"2\"\n",
		"the head keeps the file":       baseFile,
	} {
		t.Run(name, func(t *testing.T) {
			host := &files{held: map[string]string{base: baseFile, head: headFile}}
			read := ReadConfig(context.Background(), host, Commits{Base: base, Head: head, PullRequest: true})
			if !reflect.DeepEqual(host.read, []string{base}) {
				t.Fatalf("read costroid.yml at %v", host.read)
			}
			verdict := evaluate(t, Request{Usage: over, Config: read})
			if verdict.Conclusion != Failure || result(t, verdict, RuleRunBudget).LimitUSD != "2" {
				t.Errorf("the head's file decided: %+v", verdict)
			}
			if verdict.ConfigRef != base || verdict.ConfigSHA256 != digest([]byte(baseFile)) || !verdict.ConfigFromBase {
				t.Errorf("the verdict does not name the base's file: %+v", verdict)
			}
		})
	}
	t.Run("a push reads its own commit", func(t *testing.T) {
		host := &files{held: map[string]string{head: baseFile}}
		read := ReadConfig(context.Background(), host, Commits{Head: head})
		if !reflect.DeepEqual(host.read, []string{head}) || read.FromBase || string(read.Data) != baseFile {
			t.Errorf("read %+v at %v", read, host.read)
		}
	})
	t.Run("a base without the file reads the defaults", func(t *testing.T) {
		host := &files{held: map[string]string{head: baseFile}}
		verdict := evaluate(t, Request{Usage: over, Config: ReadConfig(context.Background(), host, Commits{Base: base, Head: head, PullRequest: true})})
		if verdict.Config != ConfigAbsent || verdict.Conclusion != Neutral || len(verdict.Results) != 1 || verdict.Results[0].Rule != RuleAttribution {
			t.Errorf("%+v", verdict)
		}
	})
}

type failingFiles struct{}

func (failingFiles) ReadFile(context.Context, string, string) ([]byte, error) {
	return nil, errors.New("answered 500")
}

func TestAnUnreadBaseRunsNoRule(t *testing.T) {
	host := &files{held: map[string]string{"": "policy:\n  run_budget_usd: \"1\"\n"}}
	for name, read := range map[string]ConfigFile{
		"a host that fails": ReadConfig(context.Background(), failingFiles{}, Commits{Base: base, Head: head, PullRequest: true}),
		"no base commit":    ReadConfig(context.Background(), host, Commits{Head: head, PullRequest: true}),
	} {
		verdict := evaluate(t, Request{Usage: usageOf(t, line{"USD", "3", "key-1", nil}), Config: read})
		if verdict.Config != ConfigUnread || verdict.Conclusion != Neutral || len(verdict.Results) != 0 || verdict.ConfigProblem() == nil {
			t.Errorf("%s: %+v", name, verdict)
		}
	}
	if len(host.read) != 0 {
		t.Errorf("read costroid.yml at %q", host.read)
	}
}

func TestWarnOnlyPeriod(t *testing.T) {
	over := usageOf(t, line{"USD", "3", "key-1", nil})
	budget := config("policy:\n  run_budget_usd: \"2\"\n")
	enforced := config("policy:\n  run_budget_usd: \"2\"\n  enforce: true\n")
	cases := []struct {
		name       string
		config     ConfigFile
		start      Start
		conclusion string
		outcome    string
		period     string
		active     bool
	}{
		{"the first hour", budget, Start{At: checkTime.Add(-time.Hour)}, Neutral, Warn, PeriodRunning, true},
		{"one second before the end", budget, Start{At: checkTime.Add(-WarnOnlyPeriod + time.Second)}, Neutral, Warn, PeriodRunning, true},
		{"the end", budget, Start{At: checkTime.Add(-WarnOnlyPeriod)}, Failure, Fail, PeriodEnded, false},
		{"eight days on", budget, Start{At: checkTime.Add(-8 * 24 * time.Hour)}, Failure, Fail, PeriodEnded, false},
		{"a start not read", budget, Start{}, Neutral, Warn, PeriodStartUnknown, true},
		{"a workflow not on the base", budget, Start{NotOnBase: true}, Neutral, Warn, PeriodNotOnBase, true},
		{"enforce ends it early", enforced, Start{At: checkTime.Add(-time.Hour)}, Failure, Fail, PeriodEnforced, false},
		{"enforce with no start", enforced, Start{}, Failure, Fail, PeriodEnforced, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			verdict := evaluate(t, Request{Usage: over, Config: tc.config, Start: tc.start})
			got := result(t, verdict, RuleRunBudget)
			if verdict.Conclusion != tc.conclusion || got.Outcome != tc.outcome || got.Reason != ReasonOverLimit {
				t.Errorf("concluded %s with %+v", verdict.Conclusion, got)
			}
			if got.HeldByWarnOnly != (tc.outcome == Warn) {
				t.Errorf("held by warn-only: %v", got.HeldByWarnOnly)
			}
			if verdict.WarnOnly.Reason != tc.period || verdict.WarnOnly.Active != tc.active {
				t.Errorf("period %+v", verdict.WarnOnly)
			}
		})
	}
	for start, want := range map[string]string{
		"2026-10-30T12:00:01Z": Neutral,
		"2026-10-30T12:00:00Z": Failure,
	} {
		added, err := time.Parse(time.RFC3339, start)
		if err != nil {
			t.Fatal(err)
		}
		verdict := evaluate(t, Request{Usage: over, Config: budget, Start: Start{At: added}})
		if verdict.Conclusion != want || verdict.WarnOnly.Started != start {
			t.Errorf("a workflow added %s, checked %s: %s, %+v", start, checkTime.Format(time.RFC3339), verdict.Conclusion, verdict.WarnOnly)
		}
	}
	verdict := evaluate(t, Request{Usage: over, Config: budget, Start: Start{At: checkTime.Add(-time.Hour)}})
	if verdict.WarnOnly.Ends != "2026-11-13T11:00:00Z" {
		t.Errorf("the period ends %s, want 7 days after its start", verdict.WarnOnly.Ends)
	}
}

func TestVerdictSHAs(t *testing.T) {
	source, err := os.ReadFile("default.rego")
	if err != nil {
		t.Fatal(err)
	}
	packSum := sha256.Sum256(source)
	usage := usageOf(t, line{"USD", "1", "key-1", nil})
	first := config("policy:\n  run_budget_usd: \"2\"\n")
	second := config("policy:\n  run_budget_usd: \"2\" \n")
	one := evaluate(t, Request{Usage: usage, Config: first})
	two := evaluate(t, Request{Usage: usage, Config: second})
	none := evaluate(t, Request{Usage: usage})
	if one.PackSHA256 != hex.EncodeToString(packSum[:]) || two.PackSHA256 != one.PackSHA256 {
		t.Errorf("pack SHA-256 %s, want %x", one.PackSHA256, packSum)
	}
	firstSum := sha256.Sum256(first.Data)
	if one.ConfigSHA256 != hex.EncodeToString(firstSum[:]) {
		t.Errorf("config SHA-256 %s, want %x", one.ConfigSHA256, firstSum)
	}
	if two.ConfigSHA256 == one.ConfigSHA256 {
		t.Error("one more byte did not change the config SHA-256")
	}
	if none.ConfigSHA256 != "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855" || none.Config != ConfigAbsent {
		t.Errorf("a missing file: %s, %s", none.ConfigSHA256, none.Config)
	}
	encoded, err := json.Marshal(one)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"pack_sha256":"` + one.PackSHA256 + `"`, `"config_sha256":"` + one.ConfigSHA256 + `"`} {
		if !strings.Contains(string(encoded), want) {
			t.Errorf("the recorded verdict lacks %s: %s", want, encoded)
		}
	}
}

func TestUnattributedNeverSatisfiesBudget(t *testing.T) {
	under := "policy:\n  run_budget_usd: \"5\"\n  max_run_cost_usd: 4\n  max_increase_usd: \"5\"\n  max_increase_percent: 500\n" +
		"  pr_budget_usd: 50\n  models: [\"claude-*\"]\n  enforce: true\n"
	rules := []string{RuleRunBudget, RuleRunCostThreshold, RuleRunIncrease, RuleRunIncreasePercent, RulePullRequestBudget, RuleModelAllowlist}
	model := map[string]string{"costroid/model": "claude-sonnet-5-5"}
	forged := map[string]string{"costroid/model": "claude-sonnet-5-5", "costroid/run_identity": "token_verified", "costroid/attribution_grade": "exact"}
	verified := func(trust Trust) (*Baseline, *Lifetime) {
		return &Baseline{Runs: 12, MedianUSD: mustAmount(t, "1"), Trust: trust}, &Lifetime{USD: mustAmount(t, "2"), Trust: trust}
	}
	attributed := Usage{Rows: 1, Currencies: []CurrencyUsage{{Currency: "USD", ListCost: mustAmount(t, "1"), Rows: 1, Models: map[string]int{"claude-sonnet-5-5": 1}}}}
	cases := []struct {
		name    string
		trust   Trust
		usage   Usage
		earlier Trust
		reason  string
	}{
		{"rows the job wrote", Unverified, usageOf(t, line{"USD", "1", "key-1", model}), TokenVerified, ReasonUnverifiedRows},
		{"rows the job wrote that claim a token", Unverified, usageOf(t, line{"USD", "1", "key-1", forged}), TokenVerified, ReasonUnverifiedRows},
		{"rows a ledger left unattributed", TokenVerified, Usage{Rows: 2, Currencies: []CurrencyUsage{{Currency: "USD", ListCost: mustAmount(t, "1"), Rows: 2, UnattributedRows: 1}}}, TokenVerified, ReasonUnattributedRows},
		{"no rows", TokenVerified, Usage{}, TokenVerified, ReasonNoRows},
		{"an unknown trust", Trust("trusted"), attributed, TokenVerified, ReasonUnverifiedRows},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			baseline, lifetime := verified(tc.earlier)
			verdict := evaluate(t, Request{Usage: tc.usage, Trust: tc.trust, Config: config(under), Baseline: baseline, Lifetime: lifetime})
			if verdict.Conclusion == Success {
				t.Fatalf("concluded success: %+v", verdict)
			}
			for _, rule := range rules {
				if got := result(t, verdict, rule); got.Outcome != Unproven || got.Reason != tc.reason {
					t.Errorf("%s: %+v, want unproven for %s", rule, got, tc.reason)
				}
			}
		})
	}
	t.Run("earlier runs the agents' jobs wrote", func(t *testing.T) {
		baseline, lifetime := verified(Unverified)
		verdict := evaluate(t, Request{Usage: attributed, Trust: TokenVerified, Config: config(under), Baseline: baseline, Lifetime: lifetime})
		if verdict.Conclusion == Success {
			t.Fatalf("concluded success: %+v", verdict)
		}
		for _, rule := range []string{RuleRunIncrease, RuleRunIncreasePercent, RulePullRequestBudget} {
			if got := result(t, verdict, rule); got.Outcome != Unproven || got.Reason != ReasonUnverifiedRows {
				t.Errorf("%s: %+v, want unproven for unverified rows", rule, got)
			}
		}
	})
	t.Run("every row verified, attributed and measured", func(t *testing.T) {
		baseline, lifetime := verified(TokenVerified)
		verdict := evaluate(t, Request{Usage: attributed, Trust: TokenVerified, Evidence: GatewayMeasured, Config: config(under), Baseline: baseline, Lifetime: lifetime})
		if verdict.Conclusion != Success {
			t.Errorf("verified rows under every limit: %+v", verdict)
		}
		for _, rule := range rules {
			if got := result(t, verdict, rule); got.Outcome != Pass {
				t.Errorf("%s: %+v", rule, got)
			}
		}
	})
}

func TestOverALimitFailsOnEitherPath(t *testing.T) {
	limits := config("policy:\n  max_run_cost_usd: \"0.5\"\n  enforce: true\n")
	for _, trust := range []Trust{Unverified, TokenVerified} {
		usage := Usage{Rows: 1, Currencies: []CurrencyUsage{{Currency: "USD", ListCost: mustAmount(t, "0.6"), Rows: 1}}}
		verdict := evaluate(t, Request{Usage: usage, Trust: trust, Config: limits})
		got := result(t, verdict, RuleRunCostThreshold)
		if verdict.Conclusion != Failure || got.Outcome != Fail || got.LimitUSD != "0.5" || got.CostUSD != "0.6" {
			t.Errorf("%s: %+v", trust, verdict)
		}
	}
}

func TestAmountsCompareExactly(t *testing.T) {
	limits := config("policy:\n  run_budget_usd: \"0.100000000001\"\n  enforce: true\n")
	for _, tc := range []struct {
		cost    string
		outcome string
	}{
		{"0.100000000002", Fail},
		{"0.100000000001", Pass},
		{"0.1000000000009", Pass},
	} {
		usage := Usage{Rows: 1, Currencies: []CurrencyUsage{{Currency: "USD", ListCost: mustAmount(t, tc.cost), Rows: 1}}}
		verdict := evaluate(t, Request{Usage: usage, Trust: TokenVerified, Evidence: GatewayMeasured, Config: limits})
		if got := result(t, verdict, RuleRunBudget); got.Outcome != tc.outcome {
			t.Errorf("USD %s against 0.100000000001: %+v", tc.cost, got)
		}
	}
}

func TestAnotherCurrencyIsNeverHeldToAUSDBudget(t *testing.T) {
	usage := Usage{Rows: 2, Currencies: []CurrencyUsage{
		{Currency: "EUR", ListCost: mustAmount(t, "900"), Rows: 1},
		{Currency: "USD", ListCost: mustAmount(t, "1"), Rows: 1},
	}}
	verdict := evaluate(t, Request{Usage: usage, Trust: TokenVerified, Config: config("policy:\n  run_budget_usd: \"5\"\n")})
	if got := result(t, verdict, RuleRunBudget); got.Outcome != Unproven || got.Reason != ReasonOtherCurrency || got.CostUSD != "1" {
		t.Errorf("%+v", got)
	}
}

func mustAmount(t *testing.T, text string) decimal.Decimal {
	t.Helper()
	amount, err := decimal.Parse(text)
	if err != nil {
		t.Fatal(err)
	}
	return amount
}

func TestAttributionWarnsWhereExactWasPossible(t *testing.T) {
	cases := []struct {
		name string
		row  line
		warn bool
	}{
		{"a credential, a pull request and a head", line{"USD", "1", "key-1", nil}, true},
		{"no credential", line{"USD", "1", "", nil}, false},
		{"the session id as the credential", line{"USD", "1", session, nil}, false},
		{"no pull request", line{"USD", "1", "key-1", map[string]string{"costroid/pr": ""}}, false},
		{"no head", line{"USD", "1", "key-1", map[string]string{"costroid/commit_sha": ""}}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			verdict := evaluate(t, Request{Usage: usageOf(t, tc.row, tc.row)})
			if verdict.Conclusion != Neutral {
				t.Errorf("concluded %s", verdict.Conclusion)
			}
			if !tc.warn {
				if len(verdict.Results) != 0 || verdict.JoinableRows != 0 {
					t.Errorf("%+v", verdict)
				}
				return
			}
			if got := result(t, verdict, RuleAttribution); got.Outcome != Warn || got.Reason != ReasonJoinableRows || verdict.JoinableRows != 2 {
				t.Errorf("%+v, %d rows", got, verdict.JoinableRows)
			}
		})
	}
	t.Run("it never fails", func(t *testing.T) {
		verdict := evaluate(t, Request{Usage: usageOf(t, line{"USD", "1", "key-1", nil}), Config: config("policy:\n  enforce: true\n")})
		if got := result(t, verdict, RuleAttribution); got.Outcome != Warn || verdict.Conclusion != Neutral {
			t.Errorf("%+v", verdict)
		}
	})
}

func TestNoRuleConcludesNeutral(t *testing.T) {
	verdict := evaluate(t, Request{})
	if verdict.Conclusion != Neutral || len(verdict.Results) != 0 {
		t.Errorf("%+v", verdict)
	}
}

func TestSettingsRefuseWhatTheSchemaRefuses(t *testing.T) {
	for _, body := range []string{
		"policy:\n  run_budget_usd: 5.0\n",
		"policy:\n  run_budget_usd: -5\n",
		"policy:\n  run_budget_usd: 0.1\n",
		"policy:\n  pr_budget_usd: 1e3\n",
		"policy:\n  max_increase_percent: \"12%\"\n",
		"policy:\n  models: []\n",
		"policy:\n  models: [\"*\"]\n",
		"policy:\n  models: [\"claude-*-5\"]\n",
		"policy:\n  models: [\"`claude`\"]\n",
		"policy:\n  models: [codex, codex]\n",
		"policy:\n  models: codex\n",
		"policy:\n  run_budget_usd: \"-1\"\n",
		"policy:\n  run_budget_usd: \"1e3\"\n",
		"policy:\n  run_budget_usd: \"0.1234567890123\"\n",
		"policy:\n  max_run_cost_usd: \"\"\n",
		"policy:\n  enforce: \"yes\"\n",
		"policy:\n  run_budget: \"5\"\n",
		"policy: 5\n",
		"policy:\n",
		"policy: [\n",
	} {
		verdict := evaluate(t, Request{Usage: usageOf(t, line{"USD", "9", "", nil}), Config: config(body)})
		if verdict.Config != ConfigInvalid || verdict.Conclusion != Neutral || len(verdict.Results) != 0 {
			t.Errorf("%q: %+v", body, verdict)
		}
	}
	settings, err := ParseSettings([]byte("comment:\n  show_cost: false\npolicy:\n  run_budget_usd: \"5.00\"\n  max_run_cost_usd: \"0.000000000001\"\n  enforce: false\n"))
	if err != nil || settings.Amounts["run_budget_usd"].String() != "5" || settings.Amounts["max_run_cost_usd"].String() != "0.000000000001" || settings.Enforce {
		t.Errorf("%+v, %v", settings, err)
	}
}

func TestReadUsageRefusesABadRow(t *testing.T) {
	for _, rows := range []string{
		`{"BillingCurrency":"usd","ListCost":"1"}`,
		`{"BillingCurrency":"USD","ListCost":"-1"}`,
		`{"BillingCurrency":"USD","ListCost":"0.0000000000001"}`,
		`{"BillingCurrency":"USD","ListCost":1}`,
		`not json`,
	} {
		if _, err := ReadUsage(strings.NewReader(rows + "\n")); err == nil {
			t.Errorf("accepted %s", rows)
		}
	}
	usage := usageOf(t, line{"USD", "0.1", "", nil}, line{"USD", "0.2", "", nil}, line{"EUR", "3", "", nil})
	if usage.Rows != 3 || len(usage.Currencies) != 2 || usage.Currencies[1].ListCost.String() != "0.3" || usage.Currencies[1].UnattributedRows != 2 {
		t.Errorf("%+v", usage)
	}
}

// TestOfflineEqualsReport reads the same rows and the same costroid.yml the
// two ways: from a host at the base commit, as the report does, and from
// files, as costroid policy does.
func TestOfflineEqualsReport(t *testing.T) {
	directory := t.TempDir()
	rowsPath := filepath.Join(directory, "rows.jsonl")
	configPath := filepath.Join(directory, "base.yml")
	baseFile := "policy:\n  run_budget_usd: \"2\"\n  max_run_cost_usd: \"10\"\n"
	if err := os.WriteFile(rowsPath, []byte(rowsOf(t, line{"USD", "2.5", "key-1", nil}, line{"USD", "0.25", "key-1", nil})), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(configPath, []byte(baseFile), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, start := range []Start{{}, {NotOnBase: true}, {At: checkTime.Add(-time.Hour)}, {At: checkTime.Add(-30 * 24 * time.Hour)}} {
		report := evaluateRowsFile(t, rowsPath, ReadConfig(context.Background(), &files{held: map[string]string{base: baseFile}}, Commits{Base: base, Head: head, PullRequest: true}), start)
		offline := evaluateRowsFile(t, rowsPath, ConfigFromFile(configPath), start)
		if report.ConfigRef != base || offline.ConfigRef != "" || !report.ConfigFromBase || offline.ConfigFromBase {
			t.Errorf("report read %q from the base %v, offline %q", report.ConfigRef, report.ConfigFromBase, offline.ConfigRef)
		}
		report.ConfigRef, report.ConfigFromBase = "", false
		if !reflect.DeepEqual(report, offline) {
			t.Errorf("start %+v:\nreport  %+v\noffline %+v", start, report, offline)
		}
	}
	missing := ConfigFromFile(filepath.Join(directory, "none.yml"))
	if missing.Found || missing.Unread != nil {
		t.Errorf("a missing file: %+v", missing)
	}
}

func evaluateRowsFile(t *testing.T, path string, read ConfigFile, start Start) Verdict {
	t.Helper()
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	usage, err := ReadUsage(file)
	if err != nil {
		t.Fatal(err)
	}
	return evaluate(t, Request{Usage: usage, Trust: Unverified, Config: read, Start: start})
}

func TestWorkflowPathReadsTheWorkflowRef(t *testing.T) {
	path, err := WorkflowPath("acme/widgets/.github/workflows/costroid.yml@refs/pull/7/merge", "acme/widgets")
	if err != nil || path != ".github/workflows/costroid.yml" {
		t.Fatalf("%q, %v", path, err)
	}
	for _, ref := range []string{
		"",
		".github/workflows/costroid.yml@refs/heads/main",
		"other/repo/.github/workflows/costroid.yml@refs/heads/main",
		"acme/widgets/.github/workflows/costroid.yml",
		"acme/widgets/.github/workflows/../../secrets.yml@refs/heads/main",
		"acme/widgets/README.md@refs/heads/main",
	} {
		if _, err := WorkflowPath(ref, "acme/widgets"); err == nil {
			t.Errorf("accepted %q", ref)
		}
	}
}

// history answers FirstCommit with one date or one error.
type history struct {
	added time.Time
	err   error
	asked []string
}

func (h *history) FirstCommit(_ context.Context, path, ref string) (time.Time, error) {
	h.asked = append(h.asked, path+"@"+ref)
	return h.added, h.err
}

func TestReadStartKeepsThePeriodRunningUnlessItKnows(t *testing.T) {
	added := checkTime.Add(-time.Hour)
	cases := map[string]struct {
		history *history
		base    string
		want    Start
		err     bool
	}{
		"a commit on the base":  {&history{added: added}, base, Start{At: added}, false},
		"no commit on the base": {&history{err: scm.ErrNoCommit}, base, Start{NotOnBase: true}, false},
		"a refusal":             {&history{err: errors.New("answered 403")}, base, Start{}, true},
		"no base commit":        {&history{added: added}, "", Start{}, true},
	}
	for name, tc := range cases {
		start, err := ReadStart(context.Background(), tc.history, ".github/workflows/costroid.yml", tc.base)
		if start != tc.want || (err != nil) != tc.err {
			t.Errorf("%s: %+v, %v", name, start, err)
		}
		if tc.base == "" && len(tc.history.asked) != 0 {
			t.Errorf("%s: asked %v", name, tc.history.asked)
		}
	}
}
