//go:build e2e

package e2e

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Costroid/costroid/internal/agenttest"
	"github.com/Costroid/costroid/internal/catalog"
	"github.com/Costroid/costroid/internal/decimal"
	"github.com/Costroid/costroid/internal/gateway"
	"github.com/Costroid/costroid/internal/oidc"
	"github.com/Costroid/costroid/internal/oidc/oidctest"
	"github.com/Costroid/costroid/internal/server"
	"github.com/Costroid/costroid/internal/store"
	"github.com/Costroid/costroid/internal/store/storetest"
)

// TestMain runs Postgres for the ledger the gateway journey attests to, with a
// stub first on PATH for every agent, so no test can start the machine's own.
func TestMain(m *testing.M) {
	os.Exit(agenttest.Main(func() int { return storetest.Main(m) }))
}

const (
	gatewayBudget        = "0.01"
	gatewayRequestTokens = "20000"
	// mockUsage is what the mock upstream reports for each request: 10000 input
	// and 10000 output tokens, USD 0.006 at claude-haiku-5-5's list prices and
	// USD 0.0045 at gpt-5-nano's.
	mockUsage = `{"input_tokens":10000,"output_tokens":10000}`
	// gatewayBound is one request in flight and one not yet counted, each of
	// 20000 tokens at the sheet's highest claude-haiku-5-5 price, output at
	// USD 0.50 per million tokens.
	gatewayBound = "0.02"
	// codexBound is the same at gpt-5-nano's highest price, output at USD 0.40
	// per million tokens.
	codexBound  = "0.016"
	gatewayWait = 4 * time.Minute
	author      = "octocat"
	// repositoryCap is the repository's daily cap, carried as its team's budget.
	repositoryCap = "20"
	team          = "costroid:github.com/acme/widgets"
)

// mockUpstream answers the Messages API and the Responses API with fixed usage
// and counts the requests that reached it.
type mockUpstream struct {
	url      string
	requests atomic.Int64
}

func startMock(t *testing.T) *mockUpstream {
	t.Helper()
	address := dockerBridge(t)
	listener, err := net.Listen("tcp", address+":0")
	if err != nil {
		t.Fatal(err)
	}
	mock := &mockUpstream{url: "http://host.docker.internal:" + strconv.Itoa(listener.Addr().(*net.TCPAddr).Port)}
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			Stream bool `json:"stream"`
		}
		_ = json.NewDecoder(r.Body).Decode(&request)
		switch r.URL.Path {
		case "/v1/messages":
			count := mock.requests.Add(1)
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprintf(w, `{"id":"msg_mock_%d","type":"message","role":"assistant","model":"claude-haiku-5-5","content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn","stop_sequence":null,"usage":%s}`, count, mockUsage)
		case "/v1/responses":
			respond(w, mock.requests.Add(1), request.Stream)
		default:
			http.NotFound(w, r)
		}
	})}
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(func() { _ = server.Close() })
	return mock
}

// respond answers one Responses API request, as a stream of events when the
// request asks for one, as Codex does.
func respond(w http.ResponseWriter, count int64, stream bool) {
	response := fmt.Sprintf(`{"id":"resp_mock_%d","object":"response","created_at":1762560000,"status":"completed","model":"gpt-5-nano","output":[{"type":"message","id":"msg_mock_%d","status":"completed","role":"assistant","content":[{"type":"output_text","text":"ok","annotations":[]}]}],"parallel_tool_calls":true,"tool_choice":"auto","tools":[],"usage":{"input_tokens":10000,"input_tokens_details":{"cached_tokens":0},"output_tokens":10000,"output_tokens_details":{"reasoning_tokens":0},"total_tokens":20000}}`, count, count)
	if !stream {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, response)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	created := fmt.Sprintf(`{"id":"resp_mock_%d","object":"response","created_at":1762560000,"status":"in_progress","model":"gpt-5-nano","output":[]}`, count)
	fmt.Fprintf(w, "event: response.created\ndata: {\"type\":\"response.created\",\"sequence_number\":0,\"response\":%s}\n\n", created)
	fmt.Fprintf(w, "event: response.completed\ndata: {\"type\":\"response.completed\",\"sequence_number\":1,\"response\":%s}\n\n", response)
}

// dockerBridge is the host's address on Docker's default bridge, which
// host.docker.internal names inside a container.
func dockerBridge(t *testing.T) string {
	t.Helper()
	out, err := exec.Command("docker", "network", "inspect", "bridge", "--format", "{{(index .IPAM.Config 0).Gateway}}").Output()
	if err != nil {
		t.Fatalf("read the bridge's address: %v", err)
	}
	return strings.TrimSpace(string(out))
}

type gatewayStack struct {
	t       *testing.T
	project string
	url     string
	admin   string
	// minter is a key limited to the routes mint and close call. The jobs
	// hold it as their admin key; the test reads the gateway with admin.
	minter string
	env    []string
}

// minterRoutes are the routes costroid gateway mint and close call, and the
// only ones the minting key may.
var minterRoutes = []string{"/key/generate", "/key/list", "/key/delete", "/team/info", "/team/new", "/team/update", "/user/info", "/user/new", "/user/update"}

const minterUser = "costroid-minter"

func composeGateway(t *testing.T, mock *mockUpstream) *gatewayStack {
	t.Helper()
	port := freePort(t)
	s := &gatewayStack{
		t:       t,
		project: fmt.Sprintf("costroid-gateway-e2e-%d", os.Getpid()),
		url:     "http://127.0.0.1:" + strconv.Itoa(port),
		admin:   "sk-e2e-admin-" + strconv.Itoa(os.Getpid()),
		minter:  "sk-e2e-minter-" + strconv.Itoa(os.Getpid()),
	}
	s.env = append(os.Environ(), "LITELLM_MASTER_KEY="+s.admin, "MOCK_UPSTREAM="+mock.url, "LITELLM_PORT="+strconv.Itoa(port))
	t.Cleanup(s.down)
	if out, err := s.docker("up", "--detach", "--wait"); err != nil {
		t.Fatalf("compose up: %v\n%s", err, out)
	}
	s.awaitStore("connected")
	s.adminCall(http.MethodPost, "/user/new", map[string]any{"user_id": minterUser, "user_role": "proxy_admin"}, http.StatusOK)
	s.adminCall(http.MethodPost, "/key/generate", map[string]any{"key": s.minter, "user_id": minterUser, "allowed_routes": minterRoutes}, http.StatusOK)
	return s
}

// adminCall calls the gateway with its master key and wants the status given.
func (s *gatewayStack) adminCall(method, route string, body any, want int) {
	s.t.Helper()
	if got, answer := s.call(s.admin, method, route, body); got != want {
		s.t.Fatalf("%s %s answered %d, want %d: %s", method, route, got, want, answer)
	}
}

// call calls the gateway with the bearer given and returns the status and body.
func (s *gatewayStack) call(bearer, method, route string, body any) (int, string) {
	s.t.Helper()
	var payload io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			s.t.Fatal(err)
		}
		payload = bytes.NewReader(data)
	}
	request, _ := http.NewRequest(method, s.url+route, payload)
	request.Header.Set("Authorization", "Bearer "+bearer)
	request.Header.Set("Content-Type", "application/json")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		s.t.Fatal(err)
	}
	defer response.Body.Close()
	data, _ := io.ReadAll(response.Body)
	return response.StatusCode, string(data)
}

func (s *gatewayStack) docker(args ...string) ([]byte, error) {
	command := exec.Command("docker", append([]string{"compose", "--project-name", s.project, "--file", "gateway-compose.yaml"}, args...)...)
	command.Env = s.env
	return command.CombinedOutput()
}

func (s *gatewayStack) down() {
	if s.t.Failed() {
		if out, err := s.docker("logs", "--no-color", "--tail", "60", "litellm"); err == nil {
			s.t.Logf("gateway logs:\n%s", out)
		}
	}
	if out, err := s.docker("down", "--volumes", "--remove-orphans"); err != nil {
		s.t.Errorf("compose down: %v\n%s", err, out)
	}
}

// awaitStore waits until the gateway's readiness probe names the store state.
func (s *gatewayStack) awaitStore(want string) {
	s.t.Helper()
	deadline := time.Now().Add(gatewayWait)
	var last string
	for time.Now().Before(deadline) {
		response, err := http.Get(s.url + "/health/readiness")
		if err == nil {
			body, _ := io.ReadAll(response.Body)
			_ = response.Body.Close()
			last = string(body)
			if strings.Contains(last, `"db":"`+want+`"`) {
				return
			}
		}
		time.Sleep(time.Second)
	}
	s.t.Fatalf("the gateway's store never read %q: %s", want, last)
}

// keyStatus asks the gateway, as its admin, what became of a key.
func (s *gatewayStack) keyStatus(id string) string {
	s.t.Helper()
	request, _ := http.NewRequest(http.MethodGet, s.url+"/key/info?key="+id, nil)
	request.Header.Set("Authorization", "Bearer "+s.admin)
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		s.t.Fatal(err)
	}
	defer response.Body.Close()
	var answer struct {
		Info struct {
			Status string `json:"status"`
		} `json:"info"`
	}
	_ = json.NewDecoder(response.Body).Decode(&answer)
	return answer.Info.Status
}

// teamBudget reads the repository's team as the gateway holds it.
func (s *gatewayStack) teamBudget() (string, string) {
	s.t.Helper()
	request, _ := http.NewRequest(http.MethodGet, s.url+"/team/info?team_id="+team, nil)
	request.Header.Set("Authorization", "Bearer "+s.admin)
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		s.t.Fatal(err)
	}
	defer response.Body.Close()
	var answer struct {
		Info struct {
			MaxBudget      json.Number `json:"max_budget"`
			BudgetDuration string      `json:"budget_duration"`
		} `json:"team_info"`
	}
	decoder := json.NewDecoder(response.Body)
	decoder.UseNumber()
	_ = decoder.Decode(&answer)
	return answer.Info.MaxBudget.String(), answer.Info.BudgetDuration
}

// authenticates reports whether the gateway still takes a key.
func (s *gatewayStack) authenticates(key string) bool {
	s.t.Helper()
	request, _ := http.NewRequest(http.MethodGet, s.url+"/key/info", nil)
	request.Header.Set("Authorization", "Bearer "+key)
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		s.t.Fatal(err)
	}
	_ = response.Body.Close()
	return response.StatusCode == http.StatusOK
}

// gatewayJob is the three jobs of the recipe on one runner: the agent's, the
// one that mints and the one that closes. Each gets only its own variables.
type gatewayJob struct {
	*workspace
	stack   *gatewayStack
	ci      []string
	sealing string
	public  string
	request string
	report  string
	// command is the agent's arguments, and provider the provider keys the
	// job holds, which must never reach the agent.
	command  []string
	provider []string
	// checkout is the directory the agent runs in, the pull request's
	// checkout, when the job has one.
	checkout string
}

// fakeAgent is a stand-in for an agent: the name costroid run knows it by,
// its source, its arguments, and the provider keys a job of its kind holds.
type fakeAgent struct {
	name, source string
	command      []string
	provider     []string
}

var (
	claudeCode = fakeAgent{name: "claude", source: "./testdata/gatewayagent", command: []string{"-p", "Fix the failing test."},
		provider: []string{"ANTHROPIC_API_KEY=sk-ant-must-not-reach-the-agent"}}
	// The Codex job holds an OpenAI key and no CODEX_API_KEY, as the recipe's
	// does. A command that names its own provider is refused before the
	// gateway is asked, so this one names none.
	codexExec = fakeAgent{name: "codex", source: "./testdata/gatewaycodex", command: []string{"exec", "-m", "gpt-5-nano", "Fix the failing test."},
		provider: []string{"OPENAI_API_KEY=sk-must-not-reach-the-agent"}}
)

func newGatewayJob(t *testing.T, stack *gatewayStack, agent fakeAgent) *gatewayJob {
	t.Helper()
	w := &workspace{t: t, directory: t.TempDir()}
	w.binary = filepath.Join(w.directory, "costroid")
	w.agent = filepath.Join(w.directory, agent.name)
	w.rows = filepath.Join(w.directory, "rows.jsonl")
	for target, source := range map[string]string{w.binary: "../cmd/costroid", w.agent: agent.source} {
		build := exec.Command("go", "build", "-o", target, source)
		build.Env = append(os.Environ(), "CGO_ENABLED=0")
		if out, err := build.CombinedOutput(); err != nil {
			t.Fatalf("go build %s: %v\n%s", source, err, out)
		}
	}
	event := filepath.Join(w.directory, "event.json")
	w.write(event, fmt.Sprintf(`{"pull_request":{"number":%s,"head":{"sha":%q},"base":{"sha":%q},"user":{"login":%q}}}`, pull, head, base, author), 0o644)
	job := &gatewayJob{workspace: w, stack: stack, request: filepath.Join(w.directory, "gateway"), report: filepath.Join(w.directory, "agent.json"),
		command: agent.command, provider: agent.provider}
	job.ci = append(os.Environ(),
		"GITHUB_ACTIONS=true", "GITHUB_SERVER_URL=https://github.com", "GITHUB_REPOSITORY=acme/widgets",
		"GITHUB_RUN_ID="+runID, "GITHUB_JOB=agent", "GITHUB_SHA="+merge, "GITHUB_EVENT_NAME=pull_request",
		"GITHUB_EVENT_PATH="+event, "CLAUDE_CONFIG_DIR="+filepath.Join(w.directory, "claude-config"),
		"CODEX_HOME="+filepath.Join(w.directory, "codex-home"),
	)
	sealing := filepath.Join(w.directory, "sealing-key")
	code, stdout, stderr := w.costroid(job.ci, "gateway", "keygen", "--out", sealing)
	if code != 0 {
		t.Fatalf("gateway keygen exit %d: %s", code, stderr)
	}
	data, err := os.ReadFile(sealing)
	if err != nil {
		t.Fatal(err)
	}
	job.sealing, job.public = strings.TrimSpace(string(data)), strings.TrimSpace(stdout)
	if strings.Contains(stdout+stderr, job.sealing) {
		t.Fatal("gateway keygen printed the sealing key's private half")
	}
	return job
}

func (j *gatewayJob) attempt(n int) []string {
	return append(j.ci, "GITHUB_RUN_ATTEMPT="+strconv.Itoa(n))
}

// open is the agent's job drawing its key and the minting job registering it,
// with the minting job's extra variables. It returns the key's id.
func (j *gatewayJob) open(attempt int, minting ...string) string {
	j.t.Helper()
	agentEnv := append(j.attempt(attempt), "COSTROID_GATEWAY_PUBLIC_KEY="+j.public)
	code, stdout, stderr := j.costroid(agentEnv, "gateway", "request", "--out", j.request)
	if code != 0 {
		j.t.Fatalf("gateway request exit %d: %s", code, stderr)
	}
	key := j.key()
	if strings.Contains(stdout+stderr, key) {
		j.t.Fatal("gateway request printed the key")
	}
	mintEnv := append(j.attempt(attempt), "COSTROID_GATEWAY_ADMIN_KEY="+j.stack.minter, "COSTROID_GATEWAY_SEALING_KEY="+j.sealing)
	mintEnv = append(mintEnv, minting...)
	code, stdout, stderr = j.costroid(mintEnv, "gateway", "mint", "--url", j.stack.url, "--budget-usd", gatewayBudget,
		"--lifetime-minutes", "10", "--max-request-tokens", gatewayRequestTokens,
		"--repository-daily-cap-usd", repositoryCap, "--author-daily-cap-usd", "5",
		"--request", filepath.Join(j.request, "costroid-gateway-request.json"))
	if code != 0 {
		j.t.Fatalf("gateway mint exit %d: %s", code, stderr)
	}
	for _, secret := range []string{key, j.stack.minter, j.sealing} {
		if strings.Contains(stdout+stderr, secret) {
			j.t.Fatal("gateway mint printed a key")
		}
	}
	id := keyID(key)
	if !strings.Contains(stdout, id) {
		j.t.Errorf("gateway mint did not name the key id %s: %s", id, stdout)
	}
	return id
}

func (j *gatewayJob) key() string {
	j.t.Helper()
	data, err := os.ReadFile(filepath.Join(j.request, "key"))
	if err != nil {
		j.t.Fatal(err)
	}
	return string(data)
}

// run is costroid run on the agent's job. The job also holds a provider key,
// which must never reach the agent.
func (j *gatewayJob) run(attempt int, extra ...string) *exec.Cmd {
	env := append(append(j.attempt(attempt), j.provider...), "GATEWAY_AGENT_REPORT="+j.report)
	args := []string{"run", "--budget-usd", gatewayBudget, "--out", j.rows,
		"--gateway", j.stack.url, "--gateway-key-file", filepath.Join(j.request, "key"), "--", j.agent}
	command := exec.Command(j.binary, append(args, j.command...)...)
	command.Env = append(env, extra...)
	command.Dir = j.checkout
	return command
}

// decoy is a base URL a pull request names in the repository's Claude Code
// settings. It counts the requests sent to it, which the gateway never sees.
type decoy struct {
	server   *httptest.Server
	requests atomic.Int64
}

// checkoutRoutingAround is a pull request's checkout whose
// .claude/settings.json names the decoy as Claude Code's base URL, with a
// token of its own and a provider key.
func checkoutRoutingAround(t *testing.T) (string, *decoy) {
	t.Helper()
	d := &decoy{}
	d.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		d.requests.Add(1)
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	t.Cleanup(d.server.Close)
	checkout := t.TempDir()
	settings := fmt.Sprintf(`{"env":{"ANTHROPIC_BASE_URL":%q,"ANTHROPIC_AUTH_TOKEN":"sk-from-the-pull-request","ANTHROPIC_API_KEY":"sk-ant-from-the-pull-request"}}`, d.server.URL)
	if err := os.MkdirAll(filepath.Join(checkout, ".claude"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(checkout, ".claude", "settings.json"), []byte(settings), 0o644); err != nil {
		t.Fatal(err)
	}
	return checkout, d
}

// close is the closing job, with its extra variables. It returns what close
// wrote to stderr.
func (j *gatewayJob) close(attempt int, closing ...string) string {
	j.t.Helper()
	env := append(j.attempt(attempt), "COSTROID_GATEWAY_ADMIN_KEY="+j.stack.minter)
	code, stdout, stderr := j.costroid(append(env, closing...), "gateway", "close", "--url", j.stack.url)
	if code != 0 || stdout != "deleted\n" {
		j.t.Fatalf("gateway close exit %d: %s%s", code, stdout, stderr)
	}
	if strings.Contains(stdout+stderr, j.stack.minter) {
		j.t.Fatal("gateway close printed the admin key")
	}
	return stderr
}

type agentOutcome struct {
	Accepted      int    `json:"accepted"`
	RefusedStatus int    `json:"refused_status"`
	Refusal       string `json:"refusal"`
	AfterPause    int    `json:"after_pause_status"`
	Settings      string `json:"settings"`
	SettingsMode  string `json:"settings_mode"`
}

func (j *gatewayJob) outcome() agentOutcome {
	j.t.Helper()
	data, err := os.ReadFile(j.report)
	if err != nil {
		j.t.Fatal(err)
	}
	var result agentOutcome
	if err := json.Unmarshal(data, &result); err != nil {
		j.t.Fatal(err)
	}
	return result
}

type gatewayRow struct {
	SubAccountID   string            `json:"SubAccountId"`
	ListCost       string            `json:"ListCost"`
	Description    string            `json:"ChargeDescription"`
	OvershootBound *string           `json:"x_CostroidOvershootBound"`
	Tags           map[string]string `json:"Tags"`
}

func (j *gatewayJob) readRows() []gatewayRow {
	j.t.Helper()
	data, err := os.ReadFile(j.rows)
	if err != nil {
		j.t.Fatal(err)
	}
	var rows []gatewayRow
	for _, line := range bytes.Split(bytes.TrimSpace(data), []byte("\n")) {
		var row gatewayRow
		if err := json.Unmarshal(line, &row); err != nil {
			j.t.Fatal(err)
		}
		rows = append(rows, row)
	}
	if len(rows) == 0 {
		j.t.Fatal("the run wrote no rows")
	}
	return rows
}

func keyID(key string) string {
	sum := sha256.Sum256([]byte(key))
	return hex.EncodeToString(sum[:])
}

// TestAGatewayKeyHoldsTheRunToItsBudgetAndIsGoneAfterTheJob runs each agent
// that takes a gateway through one LiteLLM and one mock upstream: Claude Code
// on the Messages API, Codex on the Responses API.
func TestAGatewayKeyHoldsTheRunToItsBudgetAndIsGoneAfterTheJob(t *testing.T) {
	mock := startMock(t)
	stack := composeGateway(t, mock)
	t.Run("claude-code", func(t *testing.T) { holdClaudeCode(t, stack.on(t), mock) })
	t.Run("codex", func(t *testing.T) { holdCodex(t, stack.on(t), mock) })
}

// on is the stack as a subtest calls it.
func (s *gatewayStack) on(t *testing.T) *gatewayStack {
	sub := *s
	sub.t = t
	return &sub
}

// holdClaudeCode runs Claude Code in a checkout whose .claude/settings.json
// names another base URL. The run's own settings outrank it, so the gateway
// still sees every request.
func holdClaudeCode(t *testing.T, stack *gatewayStack, mock *mockUpstream) {
	job := newGatewayJob(t, stack, claudeCode)
	checkout, elsewhere := checkoutRoutingAround(t)
	job.checkout = checkout
	ledger := startLedger(t)

	id := job.open(1, ledger.jobEnv(1)...)
	key := job.key()
	if budget, window := stack.teamBudget(); decimal.MustParse(budget).Cmp(decimal.MustParse(repositoryCap)) != 0 || window != "1d" {
		t.Errorf("the repository's team holds USD %v per %q, want %s per 1d", budget, window, repositoryCap)
	}
	total := runToTheRefusal(t, job, mock, 1, id, gatewayBound)
	holdTheSettingsFile(t, job)
	if sent := elsewhere.requests.Load(); sent != 0 {
		t.Errorf("the base URL in the checkout's .claude/settings.json received %d requests", sent)
	}

	holdTheBudgetToTheClosingJobsCount(t, ledger, job, total)
	if status := stack.keyStatus(id); status != "deleted" {
		t.Errorf("after close the gateway reads the key as %q", status)
	}
	if stack.authenticates(key) {
		t.Error("the key still authenticates after close")
	}

	recordAStoreFailureAsNone(t, stack, job, mock)
	if sent := elsewhere.requests.Load(); sent != 0 {
		t.Errorf("the base URL in the checkout's .claude/settings.json received %d requests", sent)
	}
	holdTheMintersKeyToItsRoutes(t, stack)
}

// holdTheSettingsFile holds the settings file the run gave the agent: only
// the run's user could read it, and it is gone now the run has ended.
func holdTheSettingsFile(t *testing.T, job *gatewayJob) {
	t.Helper()
	result := job.outcome()
	if result.Settings == "" || result.SettingsMode != "600" {
		t.Errorf("the agent read settings %q of mode %q, want the run's file of mode 600", result.Settings, result.SettingsMode)
	}
	if _, err := os.Stat(result.Settings); !os.IsNotExist(err) {
		t.Errorf("the settings file %s outlives the run: %v", result.Settings, err)
	}
}

// holdCodex runs Codex on the attempt after Claude Code's two. Its rows read
// measured though costroid's own environment holds no CODEX_API_KEY, because
// the agent runs on the gateway's key.
func holdCodex(t *testing.T, stack *gatewayStack, mock *mockUpstream) {
	const attempt = 3
	job := newGatewayJob(t, stack, codexExec)
	id := job.open(attempt)
	key := job.key()
	total := runToTheRefusal(t, job, mock, attempt, id, codexBound)
	for _, row := range job.readRows() {
		if row.Tags["costroid/agent"] != "codex" || row.Tags["costroid/cost_basis"] != "measured" {
			t.Errorf("agent %q, basis %q: %s", row.Tags["costroid/agent"], row.Tags["costroid/cost_basis"], row.Description)
		}
	}
	t.Logf("the Codex run's rows come to USD %s", total)
	job.close(attempt)
	if status := stack.keyStatus(id); status != "deleted" {
		t.Errorf("after close the gateway reads the key as %q", status)
	}
	if stack.authenticates(key) {
		t.Error("the key still authenticates after close")
	}
}

// runToTheRefusal runs the agent on the key until the gateway refuses it, and
// holds the rows to the key: its id, gateway-precheck, the bound given, an
// overshoot within it, and no count of the gateway's set beside them. It
// returns the rows' total.
func runToTheRefusal(t *testing.T, job *gatewayJob, mock *mockUpstream, attempt int, id, bound string) decimal.Decimal {
	t.Helper()
	before := mock.requests.Load()
	out, err := job.run(attempt).CombinedOutput()
	if err != nil {
		t.Fatalf("costroid run: %v\n%s", err, out)
	}
	result := job.outcome()
	if result.Accepted < 1 || result.RefusedStatus == 0 {
		t.Fatalf("the gateway never refused the key: %+v\n%s", result, out)
	}
	if got := mock.requests.Load() - before; got != int64(result.Accepted) {
		t.Errorf("the upstream saw %d requests, and the agent had %d accepted: a refused request reached it", got, result.Accepted)
	}
	t.Logf("accepted %d, then refused with %d: %.200s", result.Accepted, result.RefusedStatus, result.Refusal)

	total := decimal.Decimal{}
	for _, row := range job.readRows() {
		if row.SubAccountID != id {
			t.Errorf("SubAccountId reads %q, want the key id %s", row.SubAccountID, id)
		}
		if row.Tags["costroid/enforcement_grade"] != "gateway-precheck" {
			t.Errorf("the grade reads %q: %s", row.Tags["costroid/enforcement_grade"], row.Description)
		}
		if row.OvershootBound == nil || *row.OvershootBound != bound {
			t.Errorf("the overshoot bound reads %v, want %s", row.OvershootBound, bound)
		}
		total = total.Add(decimal.MustParse(row.ListCost))
	}
	over := total.Sub(decimal.MustParse(gatewayBudget))
	if over.Cmp(decimal.MustParse(bound)) > 0 {
		t.Errorf("the run cost USD %s, past the budget by more than the bound", total)
	}
	// The journey's gateway prices each model at the catalog's prices, so the
	// figure costroid run read once the gateway's store settled is the rows'
	// total, and the run sets no figure of the gateway's beside them.
	for line := range strings.Lines(string(out)) {
		if strings.Contains(line, "the gateway counted USD") || strings.Contains(line, "the gateway's count of the key's spend") {
			t.Errorf("the rows come to USD %s, and costroid run printed: %s", total, line)
		}
	}
	if strings.Contains(string(out), job.key()) {
		t.Error("costroid run printed the key")
	}
	return total
}

// holdTheMintersKeyToItsRoutes records what a key limited to mint's and
// close's routes holds on LiteLLM v1.108.1. The journey minted and closed
// both keys with it. It is refused on the other routes tried, the model's
// route among them.
// It can still mint a key without the limit for its own user, a proxy admin,
// and that key reaches every admin route: the limit is no boundary against
// whoever holds the key.
func holdTheMintersKeyToItsRoutes(t *testing.T, stack *gatewayStack) {
	t.Helper()
	for route, method := range map[string]string{"/team/list": http.MethodGet, "/model/new": http.MethodPost, "/config/update": http.MethodPost, "/v1/messages": http.MethodPost} {
		if status, body := stack.call(stack.minter, method, route, map[string]any{}); status != http.StatusForbidden || !strings.Contains(body, "not allowed to call this route") {
			t.Errorf("the minting key on %s %s answered %d: %.200s", method, route, status, body)
		}
	}
	unlimited := "sk-e2e-unlimited-" + strconv.Itoa(os.Getpid())
	stack.call(stack.minter, http.MethodPost, "/key/generate", map[string]any{"key": unlimited, "user_id": minterUser})
	if status, body := stack.call(unlimited, http.MethodGet, "/team/list", nil); status != http.StatusOK {
		t.Errorf("a key the minting key made for its own user answered %d on /team/list, and docs/gateway.md says it reaches every admin route: %.200s", status, body)
	}
}

// recordAStoreFailureAsNone stops the gateway's store while the agent runs. The
// gateway keeps serving without it, so nothing holds the budget, and the rows
// must say none.
func recordAStoreFailureAsNone(t *testing.T, stack *gatewayStack, job *gatewayJob, mock *mockUpstream) {
	t.Helper()
	opened := time.Now()
	id := job.open(2)
	before := mock.requests.Load()
	resume := filepath.Join(job.directory, "resume")
	command := job.run(2, "GATEWAY_AGENT_PAUSE="+resume)
	var output bytes.Buffer
	command.Stdout, command.Stderr = &output, &output
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(gatewayWait)
	for mock.requests.Load() == before && time.Now().Before(deadline) {
		time.Sleep(200 * time.Millisecond)
	}
	if out, err := stack.docker("stop", "litellm-db"); err != nil {
		t.Fatalf("stop the store: %v\n%s", err, out)
	}
	stack.awaitStoreDown()
	resumed := time.Since(opened).Round(time.Second)
	if err := os.WriteFile(resume, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := command.Wait(); err != nil {
		t.Fatalf("costroid run: %v\n%s", err, output.String())
	}
	if status := job.outcome().AfterPause; status != http.StatusOK {
		t.Errorf("with its store down the gateway answered %d, %s after mint, and the journey needs a gateway that fails open; LiteLLM serves a key without its store only while the key is in its cache, for user_api_key_cache_ttl in testdata/litellm.yaml", status, resumed)
	}
	t.Logf("the agent resumed with the store down %s after mint", resumed)
	for _, row := range job.readRows() {
		if row.SubAccountID != id {
			t.Errorf("SubAccountId reads %q, want %s", row.SubAccountID, id)
		}
		if row.Tags["costroid/enforcement_grade"] != "none" || !strings.Contains(row.Description, "store") {
			t.Errorf("a store failure reads %q: %s", row.Tags["costroid/enforcement_grade"], row.Description)
		}
		if row.OvershootBound != nil {
			t.Errorf("a none row states a bound of %s", *row.OvershootBound)
		}
	}
	if out, err := stack.docker("start", "litellm-db"); err != nil {
		t.Fatalf("start the store: %v\n%s", err, out)
	}
	stack.awaitStore("connected")
	job.close(2)
	if status := stack.keyStatus(id); status != "deleted" {
		t.Errorf("after close the gateway reads the second key as %q", status)
	}
}

// awaitStoreDown waits until the gateway answers that its store is not
// connected, whatever word it uses.
func (s *gatewayStack) awaitStoreDown() {
	s.t.Helper()
	deadline := time.Now().Add(gatewayWait)
	for time.Now().Before(deadline) {
		response, err := http.Get(s.url + "/health/readiness")
		if err == nil {
			body, _ := io.ReadAll(response.Body)
			_ = response.Body.Close()
			if !strings.Contains(string(body), `"db":"connected"`) {
				return
			}
		}
		time.Sleep(time.Second)
	}
	s.t.Fatal("the gateway kept reading its store as connected")
}

const (
	ledgerAudience     = "https://costroid.example"
	ledgerRequestToken = "e2e-id-token-request"
	gatewayEnvironment = "costroid-gateway"
	// gatewayPolicy holds the pull request to a budget far above the run and
	// to the one model the mock serves.
	gatewayPolicy = "policy:\n  pr_budget_usd: \"5\"\n  models: [\"claude-*\"]\n"
)

// gatewayLedger is costroid serve's handler on Postgres, in this process and
// over TLS, with a CI issuer the test signs for and GitHub's ID token endpoint
// for the jobs. The costroid binary trusts its certificate through
// SSL_CERT_FILE, and asks the endpoint for its job's token as it asks GitHub.
type gatewayLedger struct {
	t        *testing.T
	issuer   *oidctest.Issuer
	server   *httptest.Server
	tokens   *httptest.Server
	certFile string
}

func startLedger(t *testing.T) *gatewayLedger {
	t.Helper()
	db := storetest.New(t)
	db.Tenant(t, "acme", []store.Binding{{Host: "github.com", Owner: "acme"}})
	ledger, err := store.Open(context.Background(), db.AppURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(ledger.Close)
	pinned, err := catalog.Load()
	if err != nil {
		t.Fatal(err)
	}
	l := &gatewayLedger{t: t, issuer: oidctest.New(t)}
	verifier := oidc.NewVerifier(ledgerAudience, l.issuer.Server.Client(), nil,
		oidc.Issuer{Kind: oidc.GitHubActions, URL: l.issuer.URL(), JWKSURL: l.issuer.URL() + "/keys", Host: "github.com"})
	service := server.New(server.Config{
		Ledger: ledger, Verifier: verifier, Catalog: pinned, PseudonymKey: bytes.Repeat([]byte("k"), 32),
		GatewayEnvironment: gatewayEnvironment, Spend: ledger, Logger: slog.New(slog.DiscardHandler),
	})
	l.server = httptest.NewTLSServer(service.Handler())
	t.Cleanup(l.server.Close)
	l.certFile = filepath.Join(t.TempDir(), "ledger.pem")
	certificate := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: l.server.Certificate().Raw})
	if err := os.WriteFile(l.certFile, certificate, 0o644); err != nil {
		t.Fatal(err)
	}
	l.tokens = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		query := r.URL.Query()
		if r.Header.Get("Authorization") != "Bearer "+ledgerRequestToken || query.Get("audience") != ledgerAudience {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		attempt, _ := strconv.Atoi(query.Get("attempt"))
		_ = json.NewEncoder(w).Encode(map[string]string{"value": l.token(attempt, gatewayEnvironment)})
	}))
	t.Cleanup(l.tokens.Close)
	return l
}

// token is the CI token of one job of the run's attempt. An environment names
// the job that holds the gateway's admin key.
func (l *gatewayLedger) token(attempt int, environment string) string {
	claims := map[string]any{
		"iss": l.issuer.URL(), "aud": ledgerAudience, "exp": time.Now().Add(5 * time.Minute).Unix(),
		"repository": "acme/widgets", "repository_owner": "acme", "run_id": runID, "run_attempt": strconv.Itoa(attempt),
		"sha": merge, "actor": author, "event_name": "pull_request", "ref": "refs/pull/" + pull + "/merge",
	}
	if environment != "" {
		claims["environment"] = environment
	}
	return l.issuer.Token(claims)
}

// jobEnv names the ledger to a job of the gateway's environment, as the
// recipe's variables and id-token: write do.
func (l *gatewayLedger) jobEnv(attempt int) []string {
	return []string{
		"COSTROID_INGEST_URL=" + l.server.URL + "/v1/rows", "COSTROID_INGEST_AUDIENCE=" + ledgerAudience,
		"ACTIONS_ID_TOKEN_REQUEST_URL=" + l.tokens.URL + "/token?api-version=2.0&attempt=" + strconv.Itoa(attempt),
		"ACTIONS_ID_TOKEN_REQUEST_TOKEN=" + ledgerRequestToken, "SSL_CERT_FILE=" + l.certFile,
	}
}

// call sends a body under the agent's job's token of the attempt, which names
// no environment.
func (l *gatewayLedger) call(path string, attempt int, body []byte) []byte {
	l.t.Helper()
	request, err := http.NewRequest(http.MethodPost, l.server.URL+path, bytes.NewReader(body))
	if err != nil {
		l.t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer "+l.token(attempt, ""))
	request.Header.Set("Content-Type", "application/json")
	response, err := l.server.Client().Do(request)
	if err != nil {
		l.t.Fatal(err)
	}
	defer response.Body.Close()
	data, _ := io.ReadAll(response.Body)
	if response.StatusCode != http.StatusOK {
		l.t.Fatalf("POST %s: %d %s", path, response.StatusCode, data)
	}
	return data
}

type ledgerVerdict struct {
	Budget struct {
		Basis      string `json:"basis"`
		Evidence   bool   `json:"evidence"`
		GatewayUSD string `json:"gateway_usd"`
	} `json:"budget"`
	Policy struct {
		Conclusion string `json:"conclusion"`
		Trust      string `json:"trust"`
		Evidence   string `json:"evidence"`
		Results    []struct {
			Rule    string `json:"rule"`
			Outcome string `json:"outcome"`
			Reason  string `json:"reason"`
			CostUSD string `json:"cost_usd"`
		} `json:"results"`
	} `json:"policy"`
}

// verdict asks the ledger for the pull request's verdict under the gateway
// policy, with the warn-only period over.
func (l *gatewayLedger) verdict() (ledgerVerdict, string) {
	l.t.Helper()
	body, err := json.Marshal(map[string]any{
		"config":         base64.StdEncoding.EncodeToString([]byte(gatewayPolicy)),
		"config_ref":     base,
		"workflow_added": time.Now().Add(-8 * 24 * time.Hour).UTC().Format(time.RFC3339),
	})
	if err != nil {
		l.t.Fatal(err)
	}
	data := l.call("/v1/pulls/"+pull+"/verdict?repo="+repository, 1, body)
	var answer ledgerVerdict
	if err := json.Unmarshal(data, &answer); err != nil {
		l.t.Fatal(err)
	}
	return answer, string(data)
}

// outcomes reads each rule's outcome and reason.
func (v ledgerVerdict) outcomes() map[string]string {
	found := map[string]string{}
	for _, result := range v.Policy.Results {
		found[result.Rule] = result.Outcome + " " + result.Reason
	}
	return found
}

// holdTheBudgetToTheClosingJobsCount ingests the run's rows under the agent's
// job's token. On their token counts alone the budget cannot pass. Once the
// closing job has attested the gateway's count, it passes on that count, which
// is the rows' total at the catalog's prices, the gateway's prices here.
func holdTheBudgetToTheClosingJobsCount(t *testing.T, ledger *gatewayLedger, job *gatewayJob, total decimal.Decimal) {
	t.Helper()
	rows, err := os.ReadFile(job.rows)
	if err != nil {
		t.Fatal(err)
	}
	ledger.call("/v1/rows", 1, rows)
	before, data := ledger.verdict()
	want := map[string]string{"pr_budget": "unproven unmeasured_spend", "model_allowlist": "unproven unmeasured_spend"}
	if before.Policy.Conclusion != "neutral" || before.Policy.Trust != "token_verified" || before.Policy.Evidence != "agent_counts" ||
		before.Budget.Basis != "agent_counts" || before.Budget.Evidence || !equalOutcomes(before.outcomes(), want) {
		t.Fatalf("the verdict on the agent's own counts: %s", data)
	}

	stderr := job.close(1, ledger.jobEnv(1)...)
	attested, found := strings.CutPrefix(strings.TrimSpace(stderr), "attested the gateway's count of USD ")
	attested, ended := strings.CutSuffix(attested, " to the ledger")
	if !found || !ended {
		t.Fatalf("close attested nothing: %s", stderr)
	}
	if figure, err := decimal.Parse(attested); err != nil || !gateway.SameSpend(figure, total) {
		t.Fatalf("close attested USD %s, and the rows come to USD %s", attested, total)
	}

	after, data := ledger.verdict()
	want = map[string]string{"pr_budget": "pass within_limit", "model_allowlist": "pass models_on_allowlist"}
	if after.Policy.Conclusion != "success" || after.Policy.Evidence != "gateway_measured" || after.Budget.Basis != "gateway_measured" ||
		!after.Budget.Evidence || after.Budget.GatewayUSD != attested || !equalOutcomes(after.outcomes(), want) {
		t.Fatalf("the verdict on the gateway's count of USD %s: %s", attested, data)
	}
	t.Logf("close attested USD %s: %s", attested, data)
}

func equalOutcomes(got, want map[string]string) bool {
	if len(got) != len(want) {
		return false
	}
	for rule, outcome := range want {
		if got[rule] != outcome {
			return false
		}
	}
	return true
}
