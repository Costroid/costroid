package mcpserver

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/Costroid/costroid/internal/decimal"
	"github.com/Costroid/costroid/internal/focus"
	"github.com/Costroid/costroid/internal/history"
	"github.com/Costroid/costroid/internal/policy"
	"github.com/Costroid/costroid/internal/status"
)

// view is an answer of GET /v1/pulls/{pr}, cut down to its totals.
const view = `{"repository":"github.com/example/app","pull_request":"42","totals":[{"currency":"USD","cost_basis":"list_equivalent","label":"List-equivalent","list_cost":"0.001619725"}]}`

// connect serves options to an in-memory client for the test's length.
func connect(t *testing.T, options Options) *mcp.ClientSession {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	serverSide, clientSide := mcp.NewInMemoryTransports()
	served := make(chan error, 1)
	go func() { served <- New(options).Run(ctx, serverSide) }()
	session, err := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "0"}, nil).Connect(ctx, clientSide, nil)
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		session.Close()
		cancel()
		<-served
	})
	return session
}

func call(t *testing.T, session *mcp.ClientSession, name string, arguments any) *mcp.CallToolResult {
	t.Helper()
	result, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: arguments})
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	return result
}

func text(result *mcp.CallToolResult) string {
	var parts []string
	for _, content := range result.Content {
		if typed, ok := content.(*mcp.TextContent); ok {
			parts = append(parts, typed.Text)
		}
	}
	return strings.Join(parts, "\n")
}

func structured(t *testing.T, result *mcp.CallToolResult, into any) {
	t.Helper()
	if result.IsError {
		t.Fatalf("the tool failed: %s", text(result))
	}
	data, err := json.Marshal(result.StructuredContent)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, into); err != nil {
		t.Fatalf("%v: %s", err, data)
	}
}

func environment(values map[string]string) func(string) string {
	return func(name string) string { return values[name] }
}

func idle() Options {
	return Options{
		Version: "test",
		Getenv:  environment(nil),
		Result: func(context.Context, int) (history.Posted, error) {
			return history.Posted{}, history.ErrNoCheck
		},
		Policy: func(context.Context, PolicyRequest) (policy.Verdict, error) {
			return policy.Verdict{}, errors.New("no rows")
		},
	}
}

func TestToolsListIsReadOnly(t *testing.T) {
	listed, err := connect(t, idle()).ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, tool := range listed.Tools {
		names = append(names, tool.Name)
		annotations := tool.Annotations
		switch {
		case annotations == nil:
			t.Errorf("%s states no annotations", tool.Name)
		case !annotations.ReadOnlyHint:
			t.Errorf("%s does not state that it only reads", tool.Name)
		case annotations.DestructiveHint == nil || *annotations.DestructiveHint:
			t.Errorf("%s does not state that it destroys nothing", tool.Name)
		case !annotations.IdempotentHint:
			t.Errorf("%s does not state that a repeated call changes nothing", tool.Name)
		}
		if tool.Description == "" {
			t.Errorf("%s has no description", tool.Name)
		}
	}
	slices.Sort(names)
	if want := []string{LedgerPull, PolicyCheck, PullResult, RunStatus}; !slices.Equal(names, want) {
		t.Errorf("the server lists %v, want %v", names, want)
	}
}

func TestServerNamesItselfCostroid(t *testing.T) {
	info := connect(t, idle()).InitializeResult().ServerInfo
	if info.Name != "costroid" || info.Version != "test" {
		t.Errorf("the server names itself %+v", info)
	}
}

func TestServerOffersToolsAlone(t *testing.T) {
	capabilities, err := json.Marshal(connect(t, idle()).InitializeResult().Capabilities)
	if err != nil {
		t.Fatal(err)
	}
	if string(capabilities) != `{"tools":{}}` {
		t.Errorf("the server offers %s", capabilities)
	}
}

func writtenStatus(t *testing.T) (string, status.Status) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "costroid-status.json")
	measured := focus.Measured
	requests := 3
	at := time.Date(2026, 11, 8, 9, 30, 0, 0, time.UTC)
	spend := []status.Spend{{Currency: "USD", CostBasis: &measured, ListCost: decimal.MustParse("0.0283136")}}
	written := status.Status{
		State: status.Running, Agent: "codex", Updates: status.EachRequest,
		StartedAt: at, UpdatedAt: at.Add(time.Minute), Spend: spend, Requests: &requests,
		Cap: status.NewCap(decimal.MustParse("0.5"), spend), EnforcementGrade: "wrapper-watchdog",
	}
	if err := status.Write(path, written); err != nil {
		t.Fatal(err)
	}
	return path, written
}

func TestRunStatusReadsTheStatusFile(t *testing.T) {
	path, _ := writtenStatus(t)
	options := idle()
	options.Getenv = environment(map[string]string{status.FileVariable: path})
	result := call(t, connect(t, options), RunStatus, map[string]any{})
	file, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if want := `{"status":` + strings.TrimSuffix(string(file), "\n") + `}`; result.IsError || text(result) != want {
		t.Errorf("run_status answered\n%s\nwant\n%s", text(result), want)
	}
}

func TestRunStatusSaysWhenNoStatusFileExists(t *testing.T) {
	directory := t.TempDir()
	missing := filepath.Join(directory, "costroid-status.json")
	for name, test := range map[string]struct {
		variables map[string]string
		says      string
	}{
		"a path with no file": {map[string]string{status.FileVariable: missing}, "no status file at " + missing + ": no costroid run has started an agent in this job"},
		"no path at all":      {nil, "no status file: COSTROID_STATUS_FILE and RUNNER_TEMP are unset, so no costroid run writes one"},
	} {
		t.Run(name, func(t *testing.T) {
			options := idle()
			options.Getenv = environment(test.variables)
			var answer struct {
				Status  *json.RawMessage `json:"status"`
				Missing string           `json:"missing"`
			}
			structured(t, call(t, connect(t, options), RunStatus, map[string]any{}), &answer)
			if answer.Status != nil || answer.Missing != test.says {
				t.Errorf("run_status answered status %v, missing %q", answer.Status, answer.Missing)
			}
		})
	}
	t.Run("a file it cannot read", func(t *testing.T) {
		broken := filepath.Join(directory, "broken.json")
		if err := os.WriteFile(broken, []byte("{\"schema\":"), 0o644); err != nil {
			t.Fatal(err)
		}
		options := idle()
		options.Getenv = environment(map[string]string{status.FileVariable: broken})
		result := call(t, connect(t, options), RunStatus, map[string]any{})
		if !result.IsError || !strings.Contains(text(result), "status: read "+broken) {
			t.Errorf("an unreadable file answered %v: %s", result.IsError, text(result))
		}
	})
}

func TestPullResultAnswersTheObjectAndItsCheckRun(t *testing.T) {
	const object = "{\n  \"schema\": \"costroid.result/0.6\"\n}"
	options := idle()
	asked := 0
	options.Result = func(_ context.Context, number int) (history.Posted, error) {
		asked = number
		return history.Posted{CheckRun: 1001, Object: object}, nil
	}
	var answer struct {
		CheckRun int64           `json:"check_run"`
		Result   json.RawMessage `json:"result"`
	}
	structured(t, call(t, connect(t, options), PullResult, map[string]any{"pull_request": 7}), &answer)
	if asked != 7 || answer.CheckRun != 1001 || string(answer.Result) != `{"schema":"costroid.result/0.6"}` {
		t.Errorf("asked %d, answered %d %s", asked, answer.CheckRun, answer.Result)
	}
	if result := call(t, connect(t, options), PullResult, map[string]any{"pull_request": 0}); !result.IsError {
		t.Errorf("pull request 0 answered %s", text(result))
	}
}

// ledgerStub answers the view to the bearer it was given, and keeps
// what each request asked.
func ledgerStub(t *testing.T, answer string) (*httptest.Server, *[]*http.Request) {
	t.Helper()
	var asked []*http.Request
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		asked = append(asked, r)
		if r.Header.Get("Authorization") != "Bearer costroid_ik_test" {
			w.WriteHeader(http.StatusUnauthorized)
			w.Write([]byte(`{"error":{"code":"AUTH_TOKEN_INVALID","message":"The bearer is absent or invalid."}}`))
			return
		}
		w.Write([]byte(answer))
	}))
	t.Cleanup(server.Close)
	return server, &asked
}

func TestLedgerPullReadsThePullRequestFromTheNamedLedger(t *testing.T) {
	server, asked := ledgerStub(t, view)
	ledger, err := NewLedger(server.URL+"/ledger", "costroid_ik_test", "github.com/example/app")
	if err != nil {
		t.Fatal(err)
	}
	options := idle()
	options.Ledger = ledger
	session := connect(t, options)
	if result := call(t, session, LedgerPull, map[string]any{"pull_request": 42}); result.IsError || text(result) != view {
		t.Errorf("ledger_pull answered\n%s\nthe ledger wrote\n%s", text(result), view)
	}
	call(t, session, LedgerPull, map[string]any{"pull_request": 9, "repository": "github.com/example/other"})
	var paths []string
	for _, request := range *asked {
		paths = append(paths, request.Method+" "+request.URL.RequestURI())
	}
	want := []string{"GET /ledger/v1/pulls/42?repo=github.com%2Fexample%2Fapp", "GET /ledger/v1/pulls/9?repo=github.com%2Fexample%2Fother"}
	if !slices.Equal(paths, want) {
		t.Errorf("the ledger was asked %v", paths)
	}
}

func TestLedgerPullStatesTheLedgersRefusalAndNoToken(t *testing.T) {
	server, _ := ledgerStub(t, view)
	ledger, err := NewLedger(server.URL, "costroid_ik_wrong", "github.com/example/app")
	if err != nil {
		t.Fatal(err)
	}
	options := idle()
	options.Ledger = ledger
	result := call(t, connect(t, options), LedgerPull, map[string]any{"pull_request": 42})
	said := text(result)
	if !result.IsError || !strings.Contains(said, "401 AUTH_TOKEN_INVALID") || strings.Contains(said, "costroid_ik_wrong") {
		t.Errorf("a refused read answered %v: %s", result.IsError, said)
	}
}

func TestLedgerPullWithoutALedgerSaysHowToNameOne(t *testing.T) {
	result := call(t, connect(t, idle()), LedgerPull, map[string]any{"pull_request": 42})
	if !result.IsError || !strings.Contains(text(result), "--ledger") {
		t.Errorf("no ledger answered %v: %s", result.IsError, text(result))
	}
}

func TestLedgerRefusesAURLThatWouldSendTheTokenInTheClear(t *testing.T) {
	for _, root := range []string{"http://ledger.example", "ftp://ledger.example", "https://user@ledger.example", "https://ledger.example?x=1", "ledger.example"} {
		if _, err := NewLedger(root, "costroid_ik_test", ""); err == nil {
			t.Errorf("%s was accepted", root)
		}
	}
	for _, root := range []string{"https://ledger.example", "https://ledger.example/costroid", "http://localhost:8080", "http://127.0.0.1:8080", "http://[::1]:8080"} {
		if _, err := NewLedger(root, "costroid_ik_test", ""); err != nil {
			t.Errorf("%s was refused: %v", root, err)
		}
	}
}

func TestLedgerPullRefusesAnAmountThatIsNotAString(t *testing.T) {
	server, _ := ledgerStub(t, strings.Replace(view, `"list_cost":"0.001619725"`, `"list_cost":0.001619725`, 1))
	ledger, err := NewLedger(server.URL, "costroid_ik_test", "github.com/example/app")
	if err != nil {
		t.Fatal(err)
	}
	options := idle()
	options.Ledger = ledger
	result := call(t, connect(t, options), LedgerPull, map[string]any{"pull_request": 42})
	if !result.IsError || !strings.Contains(text(result), "list_cost as something other than a decimal string") {
		t.Errorf("a numeric amount answered %v: %s", result.IsError, text(result))
	}
}
