package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http/httptest"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/Costroid/costroid/internal/decimal"
	"github.com/Costroid/costroid/internal/focus"
	"github.com/Costroid/costroid/internal/mcpserver"
	"github.com/Costroid/costroid/internal/server"
	"github.com/Costroid/costroid/internal/status"
	"github.com/Costroid/costroid/internal/store"
)

// lockedBuffer is stderr for a command that runs on another goroutine.
type lockedBuffer struct {
	mu     sync.Mutex
	buffer bytes.Buffer
}

func (b *lockedBuffer) Write(data []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buffer.Write(data)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buffer.String()
}

// serveMCP runs costroid mcp with args in this process and connects the SDK's
// own client to its stdin and stdout. The command must exit 0 when the client
// closes.
func serveMCP(t *testing.T, args ...string) *mcp.ClientSession {
	t.Helper()
	serverIn, clientOut := io.Pipe()
	clientIn, serverOut := io.Pipe()
	stderr := &lockedBuffer{}
	exited := make(chan int, 1)
	go func() {
		code := dispatch(context.Background(), append([]string{"mcp"}, args...), serverIn, serverOut, stderr)
		serverOut.Close()
		exited <- code
	}()
	session, err := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "0"}, nil).
		Connect(context.Background(), &mcp.IOTransport{Reader: clientIn, Writer: clientOut}, nil)
	if err != nil {
		t.Fatalf("%v: %s", err, stderr)
	}
	t.Cleanup(func() {
		session.Close()
		if code := <-exited; code != 0 {
			t.Errorf("costroid mcp exited %d: %s", code, stderr)
		}
	})
	return session
}

// callText calls a tool and returns its answer as the server wrote it.
func callText(t *testing.T, session *mcp.ClientSession, name string, arguments any) string {
	t.Helper()
	result, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: arguments})
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	var parts []string
	for _, content := range result.Content {
		if typed, ok := content.(*mcp.TextContent); ok {
			parts = append(parts, typed.Text)
		}
	}
	answer := strings.Join(parts, "\n")
	if result.IsError {
		t.Fatalf("%s failed: %s", name, answer)
	}
	return answer
}

func TestStdioServerListsFourReadOnlyTools(t *testing.T) {
	binary := filepath.Join(t.TempDir(), "costroid")
	if output, err := exec.Command("go", "build", "-o", binary, ".").CombinedOutput(); err != nil {
		t.Fatalf("go build: %v: %s", err, output)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	session, err := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "0"}, nil).
		Connect(ctx, &mcp.CommandTransport{Command: exec.Command(binary, "mcp")}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	if name := session.InitializeResult().ServerInfo.Name; name != "costroid" {
		t.Errorf("the binary names itself %q", name)
	}
	listed, err := session.ListTools(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, tool := range listed.Tools {
		names = append(names, tool.Name)
		if tool.Annotations == nil || !tool.Annotations.ReadOnlyHint {
			t.Errorf("%s does not state that it only reads", tool.Name)
		}
	}
	slices.Sort(names)
	if want := []string{"ledger_pull", "policy_check", "pull_result", "run_status"}; !slices.Equal(names, want) {
		t.Errorf("the binary lists %v, want %v", names, want)
	}
}

func TestMCPCommandReadsTheStatusFileTheVariableNames(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "costroid-status.json")
	t.Setenv(status.FileVariable, missing)
	answer := callText(t, serveMCP(t), mcpserver.RunStatus, map[string]any{})
	if want := `{"status":null,"missing":"no status file at ` + missing + `: no costroid run has started an agent in this job"}`; answer != want {
		t.Errorf("run_status answered %s, want %s", answer, want)
	}
}

func TestPullResultReadsCheckRunNotComment(t *testing.T) {
	stub := reportEnvironment(t, pullRequestEvent())
	stub.Head = headCommit
	stub.Files[configFile] = "comment:\n  show_cost: false\n"
	stub.SetPrivate(false)
	if code, _, stderr := execute(t, "report", "--rows", realRows); code != 0 {
		t.Fatalf("exit %d: %s", code, stderr)
	}
	if len(stub.Checks) != 1 || len(stub.Comments) != 1 {
		t.Fatalf("%d checks, %d comments", len(stub.Checks), len(stub.Comments))
	}
	copied := commentBlock(t, stub.Comments[0].Body)
	if !strings.Contains(copied, `"amounts": "withheld"`) {
		t.Fatalf("the comment's copy does not withhold amounts:\n%s", copied)
	}
	before := len(stub.Requests)
	var answer struct {
		CheckRun int64           `json:"check_run"`
		Result   json.RawMessage `json:"result"`
	}
	if err := json.Unmarshal([]byte(callText(t, serveMCP(t), mcpserver.PullResult, map[string]any{"pull_request": 7})), &answer); err != nil {
		t.Fatal(err)
	}
	check := stub.Checks[0]
	if answer.CheckRun != check.ID || string(answer.Result) != compactJSON(t, checkBlock(t, check.Text)) {
		t.Errorf("pull_result answered check run %d\n%s\nthe check run carries %d\n%s", answer.CheckRun, answer.Result, check.ID, checkBlock(t, check.Text))
	}
	for _, request := range stub.Requests[before:] {
		if !strings.HasPrefix(request, "GET ") || strings.Contains(request, "/comments") {
			t.Errorf("pull_result asked %s", request)
		}
	}
}

func TestPolicyCheckMatchesPolicyCommand(t *testing.T) {
	const at = "2026-11-06T00:00:00Z"
	for name, test := range map[string]struct {
		config, added, conclusion string
	}{
		"over the budget, warn-only":     {overBudget, "", "neutral"},
		"over the budget, enforced":      {overBudget, "2026-10-01T00:00:00Z", "failure"},
		"under the budget, unproven":     {"policy:\n  run_budget_usd: \"5\"\n  max_run_cost_usd: \"1\"\n", "2026-10-01T00:00:00Z", "neutral"},
		"no policy":                      {"comment:\n  show_cost: true\n", "", "neutral"},
		"a model off the allowlist":      {"policy:\n  models: [\"gpt-5.5\"]\n", "2026-10-01T00:00:00Z", "failure"},
		"a rows file that does not read": {overBudget, "", ""},
	} {
		t.Run(name, func(t *testing.T) {
			config := writeBaseConfig(t, test.config)
			rows := realRows
			if test.conclusion == "" {
				rows = filepath.Join(t.TempDir(), "absent.jsonl")
			}
			args := []string{"policy", "--rows", rows, "--config", config, "--at", at, "--json"}
			arguments := map[string]any{"rows": rows, "config": config, "at": at}
			if test.added != "" {
				args = append(args, "--workflow-added", test.added)
				arguments["workflow_added"] = test.added
			}
			code, stdout, stderr := execute(t, args...)
			result, err := serveMCP(t).CallTool(context.Background(), &mcp.CallToolParams{Name: mcpserver.PolicyCheck, Arguments: arguments})
			if err != nil {
				t.Fatal(err)
			}
			answer := result.Content[0].(*mcp.TextContent).Text
			if test.conclusion == "" {
				if code != exitFailure || !result.IsError || "costroid: "+answer+"\n" != stderr {
					t.Errorf("policy exited %d with %q, policy_check answered %v %q", code, stderr, result.IsError, answer)
				}
				return
			}
			if result.IsError {
				t.Fatalf("policy_check failed: %s", answer)
			}
			if compactJSON(t, answer) != compactJSON(t, stdout) {
				t.Errorf("policy_check answered\n%s\ncostroid policy printed\n%s", answer, stdout)
			}
			var verdict struct {
				Conclusion string `json:"conclusion"`
			}
			if err := json.Unmarshal([]byte(answer), &verdict); err != nil || verdict.Conclusion != test.conclusion {
				t.Errorf("policy_check concludes %q, want %s: %v", verdict.Conclusion, test.conclusion, err)
			}
		})
	}
}

// ledgerKey is the ingest key the test's ledger holds.
const ledgerKey = "costroid_ik_mcp-test-key"

// viewLedger is costroid serve's store holding one pull request's line, so
// the view comes from the service's own code. The methods it leaves out are
// never called on a read.
type viewLedger struct {
	server.Ledger
	line store.Line
}

func (viewLedger) TenantForKey(_ context.Context, digest string) (store.TenantID, error) {
	if digest != store.KeyDigest(ledgerKey) {
		return store.TenantID{}, store.ErrNotFound
	}
	return store.TenantID{}, nil
}

func (l viewLedger) PullRequest(_ context.Context, _ store.TenantID, repo, pr string) ([]store.Line, error) {
	if repo != "github.com/example/widgets" || pr != "7" {
		return nil, nil
	}
	return []store.Line{l.line}, nil
}

func serveLedger(t *testing.T) string {
	t.Helper()
	charged := time.Date(2026, 11, 1, 11, 15, 12, 0, time.UTC)
	line := store.Line{
		Currency: "USD", BillingAccountID: "unknown", ServiceCategory: "AI and Machine Learning",
		Agent: "claude-code", Model: "claude-haiku-5-5", SkuMeter: "input_uncached", ConsumedUnit: "Tokens",
		CostBasis: string(focus.Measured), CostSource: "agent", AttributionGrade: "unattributed", RunIdentity: "unverified",
		Rows: 4, ConsumedQuantity: decimal.MustParse("5632"), ListCost: decimal.MustParse(realTotal), EffectiveCost: decimal.MustParse(realTotal),
		FirstCharge: charged, LastCharge: charged.Add(3 * time.Second),
	}
	ledger := httptest.NewServer(server.New(server.Config{Ledger: viewLedger{line: line}}).Handler())
	t.Cleanup(ledger.Close)
	return ledger.URL
}

// moneyKeys name every field of any answer that holds an amount. The ones
// that end in _usd name their currency; the others sit beside a currency.
var moneyKeys = []string{"list_cost", "effective_cost", "amount", "cost", "remainder", "overshoot", "gateway_usd", "limit_usd", "cost_usd", "baseline_usd"}

// checkAmounts holds every amount under value to a decimal string, or null,
// beside its currency, and counts them.
func checkAmounts(t *testing.T, tool string, value any) int {
	t.Helper()
	found := 0
	switch typed := value.(type) {
	case map[string]any:
		for key, field := range typed {
			found += checkAmounts(t, tool, field)
			if !slices.Contains(moneyKeys, key) || field == nil {
				continue
			}
			found++
			amount, isString := field.(string)
			if _, err := decimal.Parse(amount); !isString || err != nil {
				t.Errorf("%s answers %s as %T %v, not a decimal string", tool, key, field, field)
			}
			if _, named := typed["currency"].(string); !named && !strings.HasSuffix(key, "_usd") {
				t.Errorf("%s answers %s with no currency beside it", tool, key)
			}
		}
	case []any:
		for _, item := range typed {
			found += checkAmounts(t, tool, item)
		}
	}
	return found
}

func TestAmountsAreDecimalStrings(t *testing.T) {
	stub := reportEnvironment(t, pullRequestEvent())
	stub.Head = headCommit
	if code, _, stderr := execute(t, "report", "--rows", realRows, "--budget-usd", "0.001"); code != 0 {
		t.Fatalf("exit %d: %s", code, stderr)
	}
	statusPath := filepath.Join(t.TempDir(), "costroid-status.json")
	measured := focus.Measured
	spend := []status.Spend{{Currency: "USD", CostBasis: &measured, ListCost: decimal.MustParse(realTotal)}}
	at := time.Date(2026, 11, 8, 9, 30, 0, 0, time.UTC)
	if err := status.Write(statusPath, status.Status{
		State: status.Running, Agent: "claude-code", Updates: status.EachRequest, StartedAt: at, UpdatedAt: at,
		Spend: spend, Cap: status.NewCap(decimal.MustParse("0.5"), spend), EnforcementGrade: "wrapper-watchdog",
	}); err != nil {
		t.Fatal(err)
	}
	t.Setenv(status.FileVariable, statusPath)
	t.Setenv(ledgerTokenVariable, ledgerKey)
	session := serveMCP(t, "--ledger", serveLedger(t))
	config := writeBaseConfig(t, "policy:\n  run_budget_usd: \"0.001\"\n  max_run_cost_usd: \"1\"\n")
	for tool, arguments := range map[string]map[string]any{
		mcpserver.RunStatus:   {},
		mcpserver.PullResult:  {"pull_request": 7},
		mcpserver.PolicyCheck: {"rows": realRows, "config": config},
		mcpserver.LedgerPull:  {"pull_request": 7},
	} {
		decoder := json.NewDecoder(strings.NewReader(callText(t, session, tool, arguments)))
		decoder.UseNumber()
		var answer any
		if err := decoder.Decode(&answer); err != nil {
			t.Fatalf("%s: %v", tool, err)
		}
		if found := checkAmounts(t, tool, answer); found == 0 {
			t.Errorf("%s answered no amount, so the test proves nothing about it", tool)
		}
	}
}
