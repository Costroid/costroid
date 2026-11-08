package claudecode_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Costroid/costroid/internal/adapter"
	"github.com/Costroid/costroid/internal/adapter/claudecode"
	"github.com/Costroid/costroid/internal/decimal"
	"github.com/Costroid/costroid/internal/focus"
	"github.com/Costroid/costroid/internal/gateway"
	"github.com/Costroid/costroid/internal/run"
)

// oneRequest is a recorded Claude Code 2.1.316 run whose stream states
// apiKeySource none.
const (
	oneRequest   = "testdata/2.1.316/one-request"
	oneRequestID = "00000000-0000-4000-8000-000000000001"
)

// runThroughGateway runs argv under a gateway that counts every request it
// is sent, and returns that count and the run's error.
func runThroughGateway(t *testing.T, argv ...string) (int64, error) {
	t.Helper()
	t.Setenv("CLAUDE_CONFIG_DIR", t.TempDir())
	var asked atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		asked.Add(1)
		w.WriteHeader(http.StatusUnauthorized)
	}))
	t.Cleanup(server.Close)
	session := &gateway.Session{Client: gateway.NewClient(server.URL), Key: "sk-run-key-for-tests", Wait: 50 * time.Millisecond, Poll: 10 * time.Millisecond}
	_, err := run.Run(context.Background(), run.Options{Argv: argv, Budget: decimal.MustParse("0.01"), Gateway: session})
	return asked.Load(), err
}

func TestGatewayRefusesAClaudeCodeBaseURLOverride(t *testing.T) {
	settings := filepath.Join(t.TempDir(), "settings.json")
	if err := os.WriteFile(settings, []byte(`{"env":{"DISABLE_AUTO_COMPACT":"1","ANTHROPIC_BASE_URL":"https://elsewhere.test"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, refused := range []struct {
		flag string
		argv []string
	}{
		{"--settings with ANTHROPIC_BASE_URL in its env", []string{"claude", "-p", "--settings", settings, "work"}},
		{"--settings with ANTHROPIC_BASE_URL in its env", []string{"claude", "-p", `--settings={"env":{"ANTHROPIC_BASE_URL":""}}`, "work"}},
		{"--settings with ANTHROPIC_AUTH_TOKEN in its env", []string{"claude", "-p", "--settings", `{"env":{"ANTHROPIC_AUTH_TOKEN":"another-key"}}`, "work"}},
		{"--settings with CLAUDE_CODE_USE_BEDROCK in its env", []string{"claude", "-p", "--settings", `{"model":"haiku"}`, "--settings", ` {"env":{"CLAUDE_CODE_USE_BEDROCK":"1"}}`, "work"}},
		{"--settings with ANTHROPIC_VERTEX_BASE_URL in its env", []string{"claude", "-p", "--settings", `{"env":{"ANTHROPIC_VERTEX_BASE_URL":"https://elsewhere.test"}}`, "work"}},
		{"--cloud", []string{"claude", "-p", "--cloud", "work"}},
		{"--remote", []string{"claude", "-p", "--remote", "work"}},
		{"--environment", []string{"claude", "-p", "--environment", "ccpool_abc123", "work"}},
	} {
		asked, err := runThroughGateway(t, refused.argv...)
		want := "run: " + refused.flag + " sends claude-code's requests around the gateway, and a gateway cannot hold a run that routes around it"
		if err == nil || !strings.HasPrefix(err.Error(), want) {
			t.Errorf("%q: err %v, want %q", refused.argv, err, want)
		}
		if asked != 0 {
			t.Errorf("%q: the run asked the gateway %d times before it refused", refused.argv, asked)
		}
	}

	if asked, err := runThroughGateway(t, "claude", "-p", "--settings", filepath.Join(t.TempDir(), "missing.json"), "work"); asked != 0 || err == nil || !strings.Contains(err.Error(), "could not be read") {
		t.Errorf("a settings file the run cannot read: asked %d, err %v", asked, err)
	}
	if asked, err := runThroughGateway(t, "claude", "-p", "--settings", `{"env":`, "work"); asked != 0 || err == nil || strings.Contains(err.Error(), `{"env":`) {
		t.Errorf("settings the run cannot parse, which it must not print: asked %d, err %v", asked, err)
	}

	for _, argv := range [][]string{
		{"claude", "-p", "--settings", `{"env":{"DISABLE_AUTO_COMPACT":"1"}}`, "work"},
		{"claude", "-p", "--model", "haiku", "work"},
		{"claude", "-p", "--", "--cloud"},
	} {
		if flag, err := (claudecode.Agent{}).GatewayBypass(argv); flag != "" || err != nil {
			t.Errorf("%q routes nothing around the gateway, yet reads as %q, %v", argv, flag, err)
		}
	}
	if asked, _ := runThroughGateway(t, "claude", "-p", "--settings", `{"env":{"DISABLE_AUTO_COMPACT":"1"}}`, "work"); asked == 0 {
		t.Error("a command that routes nothing around the gateway never reached it")
	}
}

// replayOneRequest feeds the recorded run to a session as the run does, with
// its session file where Claude Code writes it, and tells the session it ran
// on a gateway's key when gatewayed is set.
func replayOneRequest(t *testing.T, gatewayed bool) adapter.Report {
	t.Helper()
	config := t.TempDir()
	project := filepath.Join(config, "projects", "-work")
	if err := os.MkdirAll(project, 0o755); err != nil {
		t.Fatal(err)
	}
	sessionFile, err := os.ReadFile(filepath.Join(oneRequest, "session.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(project, oneRequestID+".jsonl"), sessionFile, 0o600); err != nil {
		t.Fatal(err)
	}
	session := claudecode.Agent{ConfigDir: config}.NewSession(adapter.Plan{})
	if gatewayed {
		billed, ok := session.(adapter.GatewayBilled)
		if !ok {
			t.Fatal("a Claude Code session cannot be told it runs on a gateway's key")
		}
		billed.BilledThroughGateway()
	}
	stream, err := os.ReadFile(filepath.Join(oneRequest, "stream.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(strings.TrimSpace(string(stream)), "\n") {
		if err := session.Feed([]byte(line), time.Date(2026, 11, 1, 11, 15, 0, 0, time.UTC)); err != nil {
			t.Fatal(err)
		}
	}
	report, err := session.Finish()
	if err != nil {
		t.Fatal(err)
	}
	return report
}

func TestClaudeCodeGatewayRowsReadMeasured(t *testing.T) {
	if report := replayOneRequest(t, false); report.CostBasis != focus.ListEquivalent || len(report.Requests) == 0 {
		t.Fatalf("off the gateway, apiKeySource none reads %q over %d requests", report.CostBasis, len(report.Requests))
	}
	report := replayOneRequest(t, true)
	if report.CostBasis != focus.Measured || len(report.Requests) == 0 {
		t.Errorf("on the gateway's key, the rows read %q over %d requests", report.CostBasis, len(report.Requests))
	}
}
