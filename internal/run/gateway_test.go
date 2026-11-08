package run

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/Costroid/costroid/internal/adapter"
	"github.com/Costroid/costroid/internal/adapter/claudecode"
	"github.com/Costroid/costroid/internal/adapter/codex"
	"github.com/Costroid/costroid/internal/cicontext"
	"github.com/Costroid/costroid/internal/decimal"
	"github.com/Costroid/costroid/internal/focus"
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

// gatewayRunWith runs the recorded Claude Code run through the gateway, with
// args between -p and the prompt.
func gatewayRunWith(t *testing.T, session *gateway.Session, args ...string) (outcome, string) {
	t.Helper()
	log := installClaudeRun(t, oneRequest, oneRequestID)
	return runOnGateway(t, session, args...), log
}

// runOnGateway runs the Claude Code run already installed through the
// gateway, with args between -p and the prompt. The job holds a provider key.
func runOnGateway(t *testing.T, session *gateway.Session, args ...string) outcome {
	t.Helper()
	t.Setenv("ANTHROPIC_API_KEY", providerKey)
	var rows, stdout, stderr bytes.Buffer
	result, err := Run(context.Background(), Options{
		Argv:       slices.Concat([]string{agentNamed(t, "claude"), "-p"}, args, []string{"work"}),
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
	return outcome{result: result, stdout: stdout.String(), stderr: stderr.String(), err: err, rows: parseRows(t, rows.Bytes())}
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

// plainAgent is an agent with no way to reach a gateway.
type plainAgent struct{}

func (plainAgent) Name() string { return "plain" }

func (plainAgent) Plan(argv []string, _ decimal.Decimal) (adapter.Plan, error) {
	return adapter.Plan{Argv: argv}, nil
}

func (plainAgent) NewSession(adapter.Plan) adapter.Session { return nil }

func TestOpenGatewayRefusesAnAgentWithoutGatewayEnv(t *testing.T) {
	var asked atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		asked.Add(1)
		w.Write([]byte(keyInfoHolding))
	}))
	t.Cleanup(server.Close)
	session := &gateway.Session{Client: gateway.NewClient(server.URL), Key: gatewayKey, Wait: time.Second, Poll: 10 * time.Millisecond}
	options := Options{Gateway: session, Budget: decimal.MustParse("0.01"), CI: cicontext.Context{Run: gatewayRun}}.withDefaults()
	t.Setenv("TMPDIR", t.TempDir())

	held, err := options.openGateway(context.Background(), plainAgent{})
	if err == nil || held != nil || !strings.Contains(err.Error(), "plain cannot be pointed at a gateway") {
		t.Fatalf("an agent without gateway variables: held %v, err %v", held, err)
	}
	if asked.Load() != 0 {
		t.Error("the run asked the gateway for a key it could not hand the agent")
	}
	for _, agent := range []adapter.Agent{claudecode.Agent{}, codex.Agent{}} {
		if held, err := options.openGateway(context.Background(), agent); err != nil || held == nil {
			t.Errorf("%s through a gateway: %v", agent.Name(), err)
		}
	}
}

// codexGatewayScript stands in for codex exec. It writes its arguments and its
// environment beside log, one per line, and reports one turn on the stream.
const codexGatewayScript = `#!/bin/sh
if [ "$1" = "--version" ]; then echo "codex-cli 0.164.0-alpha.11.1"; exit 0; fi
printf '%%s\n' "$@" > %[1]q.argv
env > %[1]q.env
echo '{"type":"thread.started","thread_id":"019a0000-0000-7000-8000-00000000c0de"}'
echo '{"type":"turn.started"}'
echo '{"type":"turn.completed","usage":{"input_tokens":10000,"cached_input_tokens":0,"output_tokens":10000}}'
`

// codexGatewayRun runs Codex through a holding gateway key. The job holds an
// OpenAI key and no CODEX_API_KEY, as the recipe's job does. It returns the
// outcome, the agent's arguments and its environment.
func codexGatewayRun(t *testing.T, extra ...string) (outcome, []string, []string, *gateway.Session) {
	t.Helper()
	directory := t.TempDir()
	log := filepath.Join(directory, "codex")
	agent := filepath.Join(directory, "bin", "codex")
	if err := os.MkdirAll(filepath.Dir(agent), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(agent, []byte(fmt.Sprintf(codexGatewayScript, log)), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CODEX_HOME", t.TempDir())
	t.Setenv("OPENAI_API_KEY", providerKey)
	t.Setenv("CODEX_API_KEY", "")
	session := fakeGateway(t, keyInfoHolding, `{"status":"healthy","db":"connected"}`)
	argv := append([]string{agent, "exec", "-m", "gpt-5-nano"}, extra...)
	var rows, stdout, stderr bytes.Buffer
	result, err := Run(context.Background(), Options{
		Argv: append(argv, "work"), Budget: decimal.MustParse("0.01"), CI: cicontext.Context{Run: gatewayRun},
		Rows: &rows, Stdin: strings.NewReader(""), Stdout: &stdout, Stderr: &stderr, Catalog: pinned(t), Gateway: session,
	})
	got := outcome{result: result, stdout: stdout.String(), stderr: stderr.String(), err: err, rows: parseRows(t, rows.Bytes())}
	if err != nil || len(got.rows) == 0 {
		t.Fatalf("run: %v, %d rows, %s", err, len(got.rows), got.stderr)
	}
	return got, logged(t, log+".argv"), strings.Split(string(mustRead(t, log+".env")), "\n"), session
}

// lastConfig is the value Codex takes for a --config key: the last one given.
func lastConfig(argv []string, key string) string {
	var value string
	for i := 0; i+1 < len(argv); i++ {
		if argv[i] != "-c" && argv[i] != "--config" {
			continue
		}
		if name, setting, ok := strings.Cut(argv[i+1], "="); ok && name == key {
			value = setting
		}
	}
	return value
}

func TestCodexGatewaySetsTheOpenAIBaseOnTheCommandLine(t *testing.T) {
	got, argv, env, session := codexGatewayRun(t)
	if want := `"` + session.Client.URL + `/v1"`; lastConfig(argv, "openai_base_url") != want {
		t.Errorf("Codex reads openai_base_url as %s, want %s: %q", lastConfig(argv, "openai_base_url"), want, argv)
	}
	if !slices.Contains(env, "CODEX_API_KEY="+gatewayKey) || slices.ContainsFunc(env, func(entry string) bool { return strings.HasPrefix(entry, "OPENAI_API_KEY=") }) {
		t.Errorf("the agent's credentials: %q", slices.DeleteFunc(env, func(entry string) bool { return !strings.Contains(entry, "API_KEY") }))
	}
	for _, row := range got.rows {
		if tagOf(row, "costroid/enforcement_grade") != "gateway-precheck" || row["SubAccountId"] != gateway.KeyID(gatewayKey) {
			t.Errorf("grade %q, SubAccountId %v: %v", tagOf(row, "costroid/enforcement_grade"), row["SubAccountId"], row["ChargeDescription"])
		}
		// One request in flight and one not yet counted, each of 20000 tokens
		// at gpt-5-nano's highest price, output at USD 0.40 per million.
		if row["x_CostroidOvershootBound"] != "0.016" {
			t.Errorf("bound %v", row["x_CostroidOvershootBound"])
		}
		if tagOf(row, focus.TagCostBasis) != string(focus.Measured) {
			t.Errorf("a run on the gateway's key reads %q with no CODEX_API_KEY in costroid's environment", tagOf(row, focus.TagCostBasis))
		}
	}
	if strings.Contains(got.stdout+got.stderr, gatewayKey) {
		t.Error("the run printed the key")
	}
}

func TestCodexGatewayPinsTheProvider(t *testing.T) {
	_, argv, _, _ := codexGatewayRun(t)
	if got := lastConfig(argv, "model_provider"); got != `"openai"` {
		t.Errorf("Codex reads model_provider as %s, so its requests can go around the gateway: %q", got, argv)
	}
}

func TestGatewayArgumentsComeBeforeTheEndOfOptions(t *testing.T) {
	_, argv, _, _ := codexGatewayRun(t, "--")
	end := slices.Index(argv, "--")
	if end < 0 || argv[len(argv)-1] != "work" || lastConfig(argv[:end], "model_provider") != `"openai"` {
		t.Errorf("the gateway's arguments after the end of options would read as the prompt: %q", argv)
	}
}

// settingsFiles are the settings files the run has written for Claude Code
// and not yet removed, in TMPDIR.
func settingsFiles(t *testing.T) []string {
	found, err := filepath.Glob(filepath.Join(os.Getenv("TMPDIR"), "costroid-claude-settings-*.json"))
	if err != nil {
		t.Error(err)
	}
	return found
}

// whileTheAgentRuns holds the fake agent at its start, until it has read
// the settings files the run left in TMPDIR, and returns what each held.
func whileTheAgentRuns(t *testing.T, log string) <-chan map[string][]byte {
	t.Helper()
	if err := os.WriteFile(log+".hold", nil, 0o600); err != nil {
		t.Fatal(err)
	}
	seen := make(chan map[string][]byte, 1)
	go func() {
		defer os.Remove(log + ".hold")
		for wait := 0; wait < 200; wait++ {
			if _, err := os.Stat(log + ".starts"); err == nil {
				break
			}
			time.Sleep(10 * time.Millisecond)
		}
		files := map[string][]byte{}
		for _, file := range settingsFiles(t) {
			files[file], _ = os.ReadFile(file)
		}
		seen <- files
	}()
	return seen
}

// A repository's .claude/settings.json can name another base URL in its env,
// and Claude Code applies it over the environment the run gives it. A
// --settings file outranks it, and Claude Code reads only the last
// --settings, so the run's comes last and carries the command's own.
func TestGatewayRunSuppliesItsOwnSettings(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())
	session := fakeGateway(t, keyInfoHolding, `{"status":"healthy","db":"connected"}`)
	log := installClaudeRun(t, oneRequest, oneRequestID)
	seen := whileTheAgentRuns(t, log)
	own := `{"permissions":{"deny":["Read(./.env)"]},"env":{"DISABLE_AUTO_COMPACT":"1"}}`
	got := runOnGateway(t, session, "--settings", `{"model":"claude-opus-5-5"}`, "--settings", own)
	if got.err != nil || len(got.rows) == 0 {
		t.Fatalf("run: %v, %d rows, %s", got.err, len(got.rows), got.stderr)
	}
	files := <-seen
	if len(files) != 1 {
		t.Fatalf("while the agent ran, TMPDIR held %d settings files, want 1", len(files))
	}
	var path string
	var data []byte
	for file, content := range files {
		path, data = file, content
	}

	var settings struct {
		Model       *string           `json:"model"`
		Permissions json.RawMessage   `json:"permissions"`
		Env         map[string]string `json:"env"`
	}
	if err := json.Unmarshal(data, &settings); err != nil {
		t.Fatalf("the run's settings are not JSON: %v", err)
	}
	want := map[string]string{
		"ANTHROPIC_BASE_URL": session.Client.URL, "ANTHROPIC_AUTH_TOKEN": gatewayKey, "ANTHROPIC_API_KEY": "",
		"CLAUDE_CODE_USE_BEDROCK": "", "CLAUDE_CODE_USE_MANTLE": "", "CLAUDE_CODE_USE_VERTEX": "",
		"CLAUDE_CODE_USE_FOUNDRY": "", "CLAUDE_CODE_USE_ANTHROPIC_AWS": "", "ANTHROPIC_BEDROCK_BASE_URL": "",
		"ANTHROPIC_VERTEX_BASE_URL": "", "ANTHROPIC_FOUNDRY_BASE_URL": "", "ANTHROPIC_AWS_BASE_URL": "",
		"DISABLE_AUTO_COMPACT": "1",
	}
	for variable, value := range want {
		if got, ok := settings.Env[variable]; !ok || got != value {
			t.Errorf("the run's settings set %s to %q (set %v), want %q", variable, got, ok, value)
		}
	}
	if string(settings.Permissions) != `{"deny":["Read(./.env)"]}` || settings.Model != nil {
		t.Errorf("the run's settings do not carry the command's last --settings as written: %s", data)
	}

	argv := strings.Split(strings.TrimSpace(string(mustRead(t, log))), "\n")
	last := -1
	for i, arg := range argv {
		if arg == "--settings" {
			last = i
		}
	}
	if last < 0 || last+1 >= len(argv) || argv[last+1] != path {
		t.Errorf("the last --settings the agent reads is not the run's %s: %q", path, argv)
	}
	if strings.Contains(got.stdout+got.stderr, gatewayKey) {
		t.Error("the run printed the key")
	}
}

func TestGatewaySettingsFileIsPrivateAndRemoved(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())
	var stderr bytes.Buffer
	options := Options{
		Argv: []string{"claude", "-p", "work"}, Gateway: fakeGateway(t, keyInfoHolding, `{"status":"healthy","db":"connected"}`),
		Budget: decimal.MustParse("0.01"), CI: cicontext.Context{Run: gatewayRun}, Stderr: &stderr,
	}.withDefaults()
	held, err := options.openGateway(context.Background(), claudecode.Agent{})
	if err != nil || len(held.settings) != 2 || held.settings[0] != "--settings" {
		t.Fatalf("held %+v, err %v", held, err)
	}
	path := held.settings[1]
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	owner := info.Sys().(*syscall.Stat_t).Uid
	if !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 || owner != uint32(os.Getuid()) || filepath.Dir(path) != os.Getenv("TMPDIR") {
		t.Errorf("the settings file %s reads %v, owner %d, want a regular file of the run's user that only it can read", path, info.Mode(), owner)
	}
	if !strings.Contains(string(mustRead(t, path)), gatewayKey) {
		t.Error("the settings file does not hold the run's key")
	}
	another, err := options.openGateway(context.Background(), claudecode.Agent{})
	if err != nil || another.settings[1] == path {
		t.Errorf("a second run reuses the settings file %s: %v", path, err)
	}
	another.dropSettings()
	held.watch(context.Background())()
	if _, err := os.Stat(path); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("the settings file outlives the run: %v", err)
	}

	installClaudeRun(t, oneRequest, oneRequestID)
	if got := runOnGateway(t, options.Gateway); got.err != nil || len(settingsFiles(t)) != 0 {
		t.Errorf("after a run that ended: err %v, files %q", got.err, settingsFiles(t))
	}
	missing := options
	missing.Argv = []string{filepath.Join(t.TempDir(), "claude"), "-p", "work"}
	missing.Rows, missing.Stdout, missing.Catalog = &bytes.Buffer{}, &bytes.Buffer{}, pinned(t)
	if _, err := Run(context.Background(), missing); err == nil || !strings.Contains(err.Error(), "run: start") || len(settingsFiles(t)) != 0 {
		t.Errorf("after a run whose agent never started: err %v, files %q", err, settingsFiles(t))
	}

	stuck, err := options.openGateway(context.Background(), claudecode.Agent{})
	if err != nil {
		t.Fatal(err)
	}
	directory := os.Getenv("TMPDIR")
	if err := os.Chmod(directory, 0o500); err != nil {
		t.Fatal(err)
	}
	stuck.dropSettings()
	if err := os.Chmod(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	if line := stderr.String(); !strings.Contains(line, "costroid: warning: the settings file the run wrote for the agent holds the run's key and could not be removed") ||
		!strings.Contains(line, stuck.settings[1]) || strings.Contains(line, gatewayKey) {
		t.Errorf("a settings file the run could not remove reads: %q", line)
	}
}
