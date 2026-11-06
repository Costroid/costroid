//go:build e2e

package e2e

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Costroid/costroid/internal/decimal"
)

const (
	gatewayBudget        = "0.01"
	gatewayRequestTokens = "20000"
	// mockUsage is what the mock upstream reports for each request: 10000 input
	// and 10000 output tokens, USD 0.006 at claude-haiku-5-5's list prices.
	mockUsage = `{"input_tokens":10000,"output_tokens":10000}`
	// gatewayBound is one request in flight and one not yet counted, each of
	// 20000 tokens at the sheet's highest claude-haiku-5-5 price, output at
	// USD 0.50 per million tokens.
	gatewayBound = "0.02"
	gatewayWait  = 4 * time.Minute
	author       = "octocat"
	// repositoryCap is the repository's daily cap, carried as its team's budget.
	repositoryCap = "20"
	team          = "costroid:github.com/acme/widgets"
)

// mockUpstream answers the Messages API with fixed usage and counts the
// requests that reached it.
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
		_, _ = io.Copy(io.Discard, r.Body)
		if r.URL.Path != "/v1/messages" {
			http.NotFound(w, r)
			return
		}
		count := mock.requests.Add(1)
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"id":"msg_mock_%d","type":"message","role":"assistant","model":"claude-haiku-5-5","content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn","stop_sequence":null,"usage":%s}`, count, mockUsage)
	})}
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(func() { _ = server.Close() })
	return mock
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
	env     []string
}

func composeGateway(t *testing.T, mock *mockUpstream) *gatewayStack {
	t.Helper()
	port := freePort(t)
	s := &gatewayStack{
		t:       t,
		project: fmt.Sprintf("costroid-gateway-e2e-%d", os.Getpid()),
		url:     "http://127.0.0.1:" + strconv.Itoa(port),
		admin:   "sk-e2e-admin-" + strconv.Itoa(os.Getpid()),
	}
	s.env = append(os.Environ(), "LITELLM_MASTER_KEY="+s.admin, "MOCK_UPSTREAM="+mock.url, "LITELLM_PORT="+strconv.Itoa(port))
	t.Cleanup(s.down)
	if out, err := s.docker("up", "--detach", "--wait"); err != nil {
		t.Fatalf("compose up: %v\n%s", err, out)
	}
	s.awaitStore("connected")
	return s
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
	status, _ := s.keyInfo(id)
	return status
}

// keyInfo reads a key's status and its spend as the gateway answered it.
func (s *gatewayStack) keyInfo(id string) (string, string) {
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
			Status string      `json:"status"`
			Spend  json.Number `json:"spend"`
		} `json:"info"`
	}
	decoder := json.NewDecoder(response.Body)
	decoder.UseNumber()
	_ = decoder.Decode(&answer)
	return answer.Info.Status, answer.Info.Spend.String()
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
	public  string
	request string
	report  string
}

func newGatewayJob(t *testing.T, stack *gatewayStack) *gatewayJob {
	t.Helper()
	w := &workspace{t: t, directory: t.TempDir()}
	w.binary = filepath.Join(w.directory, "costroid")
	w.agent = filepath.Join(w.directory, "claude")
	w.rows = filepath.Join(w.directory, "rows.jsonl")
	for target, source := range map[string]string{w.binary: "../cmd/costroid", w.agent: "./testdata/gatewayagent"} {
		build := exec.Command("go", "build", "-o", target, source)
		build.Env = append(os.Environ(), "CGO_ENABLED=0")
		if out, err := build.CombinedOutput(); err != nil {
			t.Fatalf("go build %s: %v\n%s", source, err, out)
		}
	}
	event := filepath.Join(w.directory, "event.json")
	w.write(event, fmt.Sprintf(`{"pull_request":{"number":%s,"head":{"sha":%q},"base":{"sha":%q},"user":{"login":%q}}}`, pull, head, base, author), 0o644)
	job := &gatewayJob{workspace: w, stack: stack, request: filepath.Join(w.directory, "gateway"), report: filepath.Join(w.directory, "agent.json")}
	job.ci = append(os.Environ(),
		"GITHUB_ACTIONS=true", "GITHUB_SERVER_URL=https://github.com", "GITHUB_REPOSITORY=acme/widgets",
		"GITHUB_RUN_ID="+runID, "GITHUB_JOB=agent", "GITHUB_SHA="+merge, "GITHUB_EVENT_NAME=pull_request",
		"GITHUB_EVENT_PATH="+event, "CLAUDE_CONFIG_DIR="+filepath.Join(w.directory, "claude-config"),
	)
	code, stdout, stderr := w.costroid(append(job.ci, "COSTROID_GATEWAY_ADMIN_KEY="+stack.admin), "gateway", "public-key")
	if code != 0 {
		t.Fatalf("gateway public-key exit %d: %s", code, stderr)
	}
	job.public = strings.TrimSpace(stdout)
	return job
}

func (j *gatewayJob) attempt(n int) []string {
	return append(j.ci, "GITHUB_RUN_ATTEMPT="+strconv.Itoa(n))
}

// open is the agent's job drawing its key and the minting job registering it.
// It returns the key's id.
func (j *gatewayJob) open(attempt int) string {
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
	mintEnv := append(j.attempt(attempt), "COSTROID_GATEWAY_ADMIN_KEY="+j.stack.admin)
	code, stdout, stderr = j.costroid(mintEnv, "gateway", "mint", "--url", j.stack.url, "--budget-usd", gatewayBudget,
		"--lifetime-minutes", "10", "--max-request-tokens", gatewayRequestTokens,
		"--repository-daily-cap-usd", repositoryCap, "--author-daily-cap-usd", "5",
		"--request", filepath.Join(j.request, "costroid-gateway-request.json"))
	if code != 0 {
		j.t.Fatalf("gateway mint exit %d: %s", code, stderr)
	}
	if strings.Contains(stdout+stderr, key) {
		j.t.Fatal("gateway mint printed the key")
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
	env := append(j.attempt(attempt), "ANTHROPIC_API_KEY=sk-ant-must-not-reach-the-agent", "GATEWAY_AGENT_REPORT="+j.report)
	command := exec.Command(j.binary, "run", "--budget-usd", gatewayBudget, "--out", j.rows,
		"--gateway", j.stack.url, "--gateway-key-file", filepath.Join(j.request, "key"), "--", j.agent, "-p", "Fix the failing test.")
	command.Env = append(env, extra...)
	return command
}

func (j *gatewayJob) close(attempt int) {
	j.t.Helper()
	code, stdout, stderr := j.costroid(append(j.attempt(attempt), "COSTROID_GATEWAY_ADMIN_KEY="+j.stack.admin), "gateway", "close", "--url", j.stack.url)
	if code != 0 {
		j.t.Fatalf("gateway close exit %d: %s%s", code, stdout, stderr)
	}
}

type agentOutcome struct {
	Accepted      int    `json:"accepted"`
	RefusedStatus int    `json:"refused_status"`
	Refusal       string `json:"refusal"`
	AfterPause    int    `json:"after_pause_status"`
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

func TestAGatewayKeyHoldsTheRunToItsBudgetAndIsGoneAfterTheJob(t *testing.T) {
	mock := startMock(t)
	stack := composeGateway(t, mock)
	job := newGatewayJob(t, stack)

	id := job.open(1)
	key := job.key()
	if budget, window := stack.teamBudget(); decimal.MustParse(budget).Cmp(decimal.MustParse(repositoryCap)) != 0 || window != "1d" {
		t.Errorf("the repository's team holds USD %v per %q, want %s per 1d", budget, window, repositoryCap)
	}
	out, err := job.run(1).CombinedOutput()
	if err != nil {
		t.Fatalf("costroid run: %v\n%s", err, out)
	}
	result := job.outcome()
	if result.Accepted < 1 || result.RefusedStatus == 0 {
		t.Fatalf("the gateway never refused the key: %+v", result)
	}
	if got := mock.requests.Load(); got != int64(result.Accepted) {
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
		if row.OvershootBound == nil || *row.OvershootBound != gatewayBound {
			t.Errorf("the overshoot bound reads %v, want %s", row.OvershootBound, gatewayBound)
		}
		total = total.Add(decimal.MustParse(row.ListCost))
	}
	over := total.Sub(decimal.MustParse(gatewayBudget))
	if over.Cmp(decimal.MustParse(gatewayBound)) > 0 {
		t.Errorf("the run cost USD %s, past the budget by more than the bound", total)
	}
	// The gateway's figure is set beside the rows' whenever the two differ, and
	// never replaces them.
	_, spend := stack.keyInfo(id)
	warning := "the gateway counted USD " + spend + " on the run's key, and the rows, priced at the catalog, come to USD " + total.String()
	if equal := decimal.MustParse(spend).Cmp(total) == 0; equal == strings.Contains(string(out), warning) {
		t.Errorf("the gateway counted USD %s and the rows USD %s, and the run's warnings read:\n%s", spend, total, out)
	}
	t.Logf("the gateway counted USD %s, the rows USD %s", spend, total)
	if strings.Contains(string(out), key) {
		t.Error("costroid run printed the key")
	}

	job.close(1)
	if status := stack.keyStatus(id); status != "deleted" {
		t.Errorf("after close the gateway reads the key as %q", status)
	}
	if stack.authenticates(key) {
		t.Error("the key still authenticates after close")
	}

	recordAStoreFailureAsNone(t, stack, job, mock)
}

// recordAStoreFailureAsNone stops the gateway's store while the agent runs. The
// gateway keeps serving without it, so nothing holds the budget, and the rows
// must say none.
func recordAStoreFailureAsNone(t *testing.T, stack *gatewayStack, job *gatewayJob, mock *mockUpstream) {
	t.Helper()
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
	if err := os.WriteFile(resume, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := command.Wait(); err != nil {
		t.Fatalf("costroid run: %v\n%s", err, output.String())
	}
	if status := job.outcome().AfterPause; status != http.StatusOK {
		t.Errorf("with its store down the gateway answered %d, and the journey needs a gateway that fails open", status)
	}
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
