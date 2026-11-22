package server

import (
	"bytes"
	"cmp"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Costroid/costroid/internal/comment"
	"github.com/Costroid/costroid/internal/policy"
	"github.com/Costroid/costroid/internal/scm"
	"github.com/Costroid/costroid/internal/scm/scmtest"
	"github.com/Costroid/costroid/internal/store"
	"github.com/Costroid/costroid/internal/store/storetest"
)

const (
	webhookSecretText = "a webhook secret used by these tests only"
	installationID    = 58
	approvalRepo      = "example/widgets"
	approvalBase      = "1111111111111111111111111111111111111111"
	movedHead         = "4444444444444444444444444444444444444444"
	approverLogin     = "reviewer"
	authorLogin       = "author"
	// failedCheck is the id of the failed costroid check the report posted.
	failedCheck = 500
	// settleMark ends a settle: the queue is first in, first out.
	settleMark = "settle"
)

func signed(secret, body string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(body))
	return "sha256=" + hex.EncodeToString(mac.Sum(nil))
}

// pressBody is a check_run requested_action event: sender pressed button on
// the check on head of pull request 7, whose base branch is main.
func pressBody(sender, head, button string) string {
	return fmt.Sprintf(`{"action":"requested_action","installation":{"id":%d},"repository":{"full_name":%q},"sender":{"login":%q},`+
		`"check_run":{"head_sha":%q,"pull_requests":[{"number":7,"base":{"ref":"main"}}]},"requested_action":{"identifier":%q}}`,
		installationID, approvalRepo, sender, head, button)
}

// repositoriesJSON lists repositories as an installation event does.
func repositoriesJSON(repositories ...store.Repository) string {
	listed := make([]map[string]any, len(repositories))
	for i, repository := range repositories {
		listed[i] = map[string]any{"id": repository.ID, "full_name": repository.FullName, "private": true}
	}
	encoded, err := json.Marshal(listed)
	if err != nil {
		panic(err)
	}
	return string(encoded)
}

// installationBody is an installation event on installation 58 of example.
func installationBody(action string, repositories ...store.Repository) string {
	return fmt.Sprintf(`{"action":%q,"installation":{"id":%d,"account":{"login":"example","id":9}},"repositories":%s,"sender":{"login":"octocat"}}`,
		action, installationID, repositoriesJSON(repositories...))
}

// repositoriesBody is an installation_repositories event on installation 58.
func repositoriesBody(action string, added, removed []store.Repository) string {
	return fmt.Sprintf(`{"action":%q,"installation":{"id":%d,"account":{"login":"example","id":9}},"repository_selection":"selected",`+
		`"repositories_added":%s,"repositories_removed":%s,"sender":{"login":"octocat"}}`,
		action, installationID, repositoriesJSON(added...), repositoriesJSON(removed...))
}

var (
	widgets = store.Repository{ID: 101, FullName: approvalRepo}
	gadgets = store.Repository{ID: 102, FullName: "example/gadgets"}
	gizmos  = store.Repository{ID: 103, FullName: "example/gizmos"}
)

// lockedBuffer is a log the worker writes while a test reads it.
type lockedBuffer struct {
	mu     sync.Mutex
	buffer bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buffer.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buffer.String()
}

// memoryInstallations is the installations table without Postgres.
type memoryInstallations struct {
	mu   sync.Mutex
	held map[int64]store.Installation
}

func newMemoryInstallations() *memoryInstallations {
	return &memoryInstallations{held: map[int64]store.Installation{}}
}

func (m *memoryInstallations) AddRepositories(_ context.Context, id int64, account string, repositories []store.Repository) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	held := m.held[id]
	held.ID, held.Account = id, account
	for _, repository := range repositories {
		held.Repositories = slices.DeleteFunc(held.Repositories, func(r store.Repository) bool { return r.ID == repository.ID })
		held.Repositories = append(held.Repositories, repository)
	}
	slices.SortFunc(held.Repositories, func(a, b store.Repository) int { return int(a.ID - b.ID) })
	m.held[id] = held
	return nil
}

func (m *memoryInstallations) RemoveRepositories(_ context.Context, id int64, ids []int64) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	held, found := m.held[id]
	if found {
		held.Repositories = slices.DeleteFunc(held.Repositories, func(r store.Repository) bool { return slices.Contains(ids, r.ID) })
		m.held[id] = held
	}
	return nil
}

func (m *memoryInstallations) SuspendInstallation(_ context.Context, id int64, account string, suspended bool) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	held := m.held[id]
	held.ID, held.Account, held.Suspended = id, account, suspended
	m.held[id] = held
	return nil
}

func (m *memoryInstallations) DeleteInstallation(_ context.Context, id int64) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.held, id)
	return nil
}

func (m *memoryInstallations) Installation(_ context.Context, id int64) (store.Installation, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	held, found := m.held[id]
	if !found {
		return store.Installation{}, store.ErrNotFound
	}
	held.Repositories = slices.Clone(held.Repositories)
	return held, nil
}

// stubHosts reaches every repository through one stub GitHub.
type stubHosts struct {
	stub *scmtest.Stub
}

func (h stubHosts) Repository(_ int64, repository string) (ApprovalHost, error) {
	host, err := scm.NewGitHub(scm.GitHubOptions{
		API: h.stub.Server.URL, Repository: repository, Tokens: scm.StaticToken(h.stub.Token), Client: h.stub.Server.Client(),
	})
	if err != nil {
		return nil, err
	}
	return host, nil
}

// failedText is the text of a report's check whose verdict failed on
// acmeSHA: its fenced result object, as the report's golden holds it.
func failedText(t testing.TB) string {
	t.Helper()
	_, text, found := strings.Cut(string(readFile(t, "../comment/testdata/golden/fail-shown/check.md")), "# Text\n\n")
	if !found {
		t.Fatal("the golden holds no text")
	}
	return strings.TrimSuffix(text, "\n")
}

// approvalStub is example/widgets with pull request 7 by author, whose head
// is acmeSHA and holds the report's failed check. The base branch main is at
// approvalBase, and reviewer can write.
func approvalStub(t *testing.T) *scmtest.Stub {
	t.Helper()
	stub := scmtest.New(t)
	stub.Head, stub.Author, stub.Permission = acmeSHA, authorLogin, "write"
	stub.Branches["main"] = approvalBase
	stub.Checks = []scmtest.Check{{
		ID: failedCheck, Name: comment.CheckName, HeadSHA: acmeSHA, Conclusion: policy.Failure,
		Text: failedText(t), App: "github-actions",
	}}
	return stub
}

// hook is the service with the webhook route, its installations and its log.
type hook struct {
	server        *Server
	handler       http.Handler
	installations Installations
	log           *lockedBuffer
}

func newHook(t testing.TB, installations Installations, hosts RepositoryHosts) *hook {
	t.Helper()
	log := &lockedBuffer{}
	server := New(Config{
		Ledger: newMemoryLedger(), Now: func() time.Time { return testNow },
		Logger: slog.New(slog.NewJSONHandler(log, nil)),
		App:    &GitHubApp{Secret: []byte(webhookSecretText), Installations: installations, Hosts: hosts},
	})
	return &hook{server: server, handler: server.Handler(), installations: installations, log: log}
}

func (h *hook) deliver(event, delivery, body, signature string) int {
	request := httptest.NewRequest(http.MethodPost, webhookPath, strings.NewReader(body))
	request.Header.Set("X-GitHub-Event", event)
	request.Header.Set("X-GitHub-Delivery", delivery)
	if signature != "" {
		request.Header.Set("X-Hub-Signature-256", signature)
	}
	recorder := httptest.NewRecorder()
	h.handler.ServeHTTP(recorder, request)
	return recorder.Code
}

// send delivers a body signed with the service's secret and wants status.
func (h *hook) send(t testing.TB, event, delivery, body string, status int) {
	t.Helper()
	if got := h.deliver(event, delivery, body, signed(webhookSecretText, body)); got != status {
		t.Fatalf("%s %s answered %d, want %d", event, delivery, got, status)
	}
}

// settle applies every queued delivery and returns how many there were.
func (h *hook) settle(t testing.TB) int {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	applied := 0
	done := make(chan struct{})
	go func() {
		defer close(done)
		h.server.queue.Run(ctx, func(ctx context.Context, event scm.Event) {
			if event.Name == settleMark {
				cancel()
				return
			}
			applied++
			h.server.apply(ctx, event)
		})
	}()
	for !h.server.queue.Offer(scm.Event{Name: settleMark}) {
		time.Sleep(time.Millisecond)
	}
	<-done
	return applied
}

func (h *hook) installation(t testing.TB) (store.Installation, bool) {
	t.Helper()
	held, err := h.installations.Installation(context.Background(), installationID)
	if errors.Is(err, store.ErrNotFound) {
		return store.Installation{}, false
	}
	if err != nil {
		t.Fatal(err)
	}
	return held, true
}

// installed is a hook whose installation 58 is held, reaching stub as serve
// does with the App's id and KMS key set.
func installed(t testing.TB, stub *scmtest.Stub) *hook {
	t.Helper()
	h, _ := installedAsTheApp(t, stub)
	return h
}

// installedAsTheApp is installed, and the GitHub that mints the App's tokens.
func installedAsTheApp(t testing.TB, stub *scmtest.Stub) (*hook, *appFront) {
	t.Helper()
	front := newAppFront(t, stub)
	h := newHook(t, newMemoryInstallations(), front.hosts(t))
	h.send(t, scm.EventInstallation, "install", installationBody(scm.InstallationCreated, widgets), http.StatusAccepted)
	h.settle(t)
	return h, front
}

func TestWebhookRouteRefusesGet(t *testing.T) {
	h := newHook(t, newMemoryInstallations(), nil)
	for _, method := range []string{http.MethodGet, http.MethodHead, http.MethodPut, http.MethodPatch, http.MethodDelete} {
		recorder := httptest.NewRecorder()
		h.handler.ServeHTTP(recorder, httptest.NewRequest(method, webhookPath, nil))
		if recorder.Code != http.StatusMethodNotAllowed {
			t.Errorf("%s answered %d", method, recorder.Code)
		}
	}
	h.send(t, "ping", "ping-1", `{"zen":"Keep it logically awesome."}`, http.StatusAccepted)

	unserved := New(Config{Ledger: newMemoryLedger(), Logger: slog.New(slog.DiscardHandler)})
	for _, method := range []string{http.MethodGet, http.MethodPost} {
		body := `{"zen":"Keep it logically awesome."}`
		request := httptest.NewRequest(method, webhookPath, strings.NewReader(body))
		request.Header.Set("X-GitHub-Event", "ping")
		request.Header.Set("X-GitHub-Delivery", "ping-2")
		request.Header.Set("X-Hub-Signature-256", signed(webhookSecretText, body))
		recorder := httptest.NewRecorder()
		unserved.Handler().ServeHTTP(recorder, request)
		if recorder.Code != http.StatusNotFound {
			t.Errorf("with no secret, %s answered %d", method, recorder.Code)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if unserved.ProcessWebhooks(ctx); ctx.Err() != nil {
		t.Error("with no secret, the worker waited for deliveries")
	}
}

func TestServeSaysOnceWhetherTheWebhookRouteIsServed(t *testing.T) {
	var logged bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&logged, nil))
	if app := githubApp(nil, nil, nil, logger); app != nil {
		t.Fatalf("no secret configured %+v", app)
	}
	if lines := strings.Count(logged.String(), "\n"); lines != 1 || !strings.Contains(logged.String(), `"msg":"webhook route not served","path":"/webhooks/github","reason":"COSTROID_GITHUB_WEBHOOK_SECRET is not set"`) {
		t.Fatalf("%d lines: %s", lines, logged.String())
	}
	logged.Reset()
	if app := githubApp([]byte(webhookSecretText), nil, nil, logger); app == nil || string(app.Secret) != webhookSecretText {
		t.Fatal("a secret served no route")
	}
	if strings.Contains(logged.String(), webhookSecretText) || !strings.Contains(logged.String(), `"msg":"webhook route served"`) {
		t.Fatalf("%s", logged.String())
	}
	t.Setenv(envWebhook, "short")
	if _, err := webhookSecret(); err == nil || strings.Contains(err.Error(), "short") {
		t.Errorf("a short secret: %v", err)
	}
	t.Setenv(envWebhook, "")
	if secret, err := webhookSecret(); secret != nil || err != nil {
		t.Errorf("an unset secret: %q, %v", secret, err)
	}
}

func TestWebhookRefusesUnsigned(t *testing.T) {
	h := newHook(t, newMemoryInstallations(), nil)
	body := installationBody(scm.InstallationCreated, widgets)
	other := hmac.New(sha256.New, []byte("another secret of the same length as the test's"))
	other.Write([]byte(body))
	for name, signature := range map[string]string{
		"absent":      "",
		"tampered":    signed(webhookSecretText, body+" "),
		"another key": "sha256=" + hex.EncodeToString(other.Sum(nil)),
		"sha1 prefix": strings.Replace(signed(webhookSecretText, body), "sha256=", "sha1=", 1),
		"bare hex":    strings.TrimPrefix(signed(webhookSecretText, body), "sha256="),
	} {
		if status := h.deliver(scm.EventInstallation, "unsigned-"+strings.ReplaceAll(name, " ", "-"), body, signature); status != http.StatusUnauthorized {
			t.Errorf("%s: answered %d", name, status)
		}
	}
	if status := h.deliver(scm.EventInstallation, "junk", "{", "sha256=00"); status != http.StatusUnauthorized {
		t.Errorf("an unsigned body that is not JSON answered %d, so it was parsed", status)
	}
	if applied := h.settle(t); applied != 0 {
		t.Errorf("%d unsigned deliveries were queued", applied)
	}
	if _, held := h.installation(t); held {
		t.Error("an unsigned delivery kept an installation")
	}
}

func TestWebhookBoundsTheBody(t *testing.T) {
	h := newHook(t, newMemoryInstallations(), nil)
	padded := strings.Replace(installationBody(scm.InstallationCreated, widgets), `"sender"`, `"padding":"`+strings.Repeat("x", 25<<20)+`","sender"`, 1)
	if status := h.deliver(scm.EventInstallation, "large", padded, signed(webhookSecretText, padded)); status != http.StatusBadRequest {
		t.Errorf("a body past 25 MiB answered %d", status)
	}
	if applied := h.settle(t); applied != 0 {
		t.Errorf("%d deliveries were queued", applied)
	}
}

func TestWebhookRefusesReplay(t *testing.T) {
	h := newHook(t, newMemoryInstallations(), nil)
	created := installationBody(scm.InstallationCreated, widgets)
	h.send(t, scm.EventInstallation, "created-1", created, http.StatusAccepted)
	h.send(t, scm.EventInstallation, "deleted-1", installationBody(scm.InstallationDeleted), http.StatusAccepted)
	if applied := h.settle(t); applied != 2 {
		t.Fatalf("%d deliveries applied", applied)
	}
	h.send(t, scm.EventInstallation, "created-1", created, http.StatusOK)
	if applied := h.settle(t); applied != 0 {
		t.Errorf("the replay was queued %d times", applied)
	}
	if held, found := h.installation(t); found {
		t.Errorf("the replay kept %+v again", held)
	}
}

func TestInstallationEventsKeepTheTable(t *testing.T) {
	db := storetest.New(t)
	ledger, err := store.Open(context.Background(), db.AppURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(ledger.Close)
	h := newHook(t, ledger, nil)
	ctx, cancel := context.WithCancel(context.Background())
	processed := make(chan struct{})
	go func() {
		defer close(processed)
		h.server.ProcessWebhooks(ctx)
	}()
	t.Cleanup(func() {
		cancel()
		<-processed
	})
	steps := []struct {
		event, body string
		want        *store.Installation
	}{
		{scm.EventInstallation, installationBody(scm.InstallationCreated, widgets, gadgets),
			&store.Installation{ID: installationID, Account: "example", Repositories: []store.Repository{widgets, gadgets}}},
		{scm.EventInstallationRepositories, repositoriesBody(scm.RepositoriesAdded, []store.Repository{gizmos}, nil),
			&store.Installation{ID: installationID, Account: "example", Repositories: []store.Repository{widgets, gadgets, gizmos}}},
		{scm.EventInstallationRepositories, repositoriesBody(scm.RepositoriesRemoved, nil, []store.Repository{gadgets}),
			&store.Installation{ID: installationID, Account: "example", Repositories: []store.Repository{widgets, gizmos}}},
		{scm.EventInstallation, installationBody(scm.InstallationSuspended),
			&store.Installation{ID: installationID, Account: "example", Suspended: true, Repositories: []store.Repository{widgets, gizmos}}},
		{scm.EventInstallation, installationBody(scm.InstallationUnsuspended),
			&store.Installation{ID: installationID, Account: "example", Repositories: []store.Repository{widgets, gizmos}}},
		{scm.EventInstallation, installationBody(scm.InstallationDeleted), nil},
	}
	for index, step := range steps {
		h.send(t, step.event, fmt.Sprintf("step-%d", index), step.body, http.StatusAccepted)
		deadline := time.Now().Add(10 * time.Second)
		for {
			held, found := h.installation(t)
			if step.want == nil && !found || step.want != nil && found && equalInstallations(held, *step.want) {
				break
			}
			if time.Now().After(deadline) {
				t.Fatalf("step %d: the table holds %+v (held %t), want %+v\n%s", index, held, found, step.want, h.log.String())
			}
			time.Sleep(10 * time.Millisecond)
		}
	}
	h.send(t, scm.EventInstallation, "permissions", installationBody("new_permissions_accepted"), http.StatusAccepted)
	if strings.Contains(h.log.String(), `"level":"ERROR"`) {
		t.Errorf("the log holds an error:\n%s", h.log.String())
	}
}

func equalInstallations(a, b store.Installation) bool {
	return a.ID == b.ID && a.Account == b.Account && a.Suspended == b.Suspended && slices.Equal(a.Repositories, b.Repositories)
}

// posted lists the check runs posted past the report's.
func posted(stub *scmtest.Stub) []scmtest.Check {
	return slices.DeleteFunc(slices.Clone(stub.Checks), func(check scmtest.Check) bool { return check.ID == failedCheck })
}

func TestApproveButtonReconcludes(t *testing.T) {
	stub := approvalStub(t)
	h, front := installedAsTheApp(t, stub)
	h.send(t, scm.EventCheckRun, "press-1", pressBody(approverLogin, acmeSHA, scm.ApproveAction), http.StatusAccepted)
	h.settle(t)
	checks := posted(stub)
	if len(checks) != 1 {
		t.Fatalf("%d checks posted\n%s", len(checks), h.log.String())
	}
	check := checks[0]
	block, err := comment.ParseResult(check.Text)
	if err != nil {
		t.Fatal(err)
	}
	if check.Conclusion != policy.Neutral || *block.Conclusion != policy.Neutral || block.Policy.Conclusion != policy.Failure {
		t.Errorf("check %s, object %s, policy %s", check.Conclusion, *block.Conclusion, block.Policy.Conclusion)
	}
	if block.CheckRunID == nil || *block.CheckRunID != check.ID {
		t.Errorf("the object names check run %v, not %d", block.CheckRunID, check.ID)
	}
	if !strings.Contains(check.Summary, "`reviewer` approved commit `9f2c6e1` at 2026-11-02 09:00 UTC") || !strings.Contains(check.Summary, fmt.Sprintf("Check run %d carries this report in full.", failedCheck)) {
		t.Errorf("summary:\n%s", check.Summary)
	}
	for _, request := range stub.Writes() {
		if !strings.HasPrefix(request, "POST /repos/example/widgets/check-runs") && !strings.HasPrefix(request, "PATCH /repos/example/widgets/check-runs/") {
			t.Errorf("the press wrote %s", request)
		}
	}
	h.send(t, scm.EventCheckRun, "press-2", pressBody(approverLogin, acmeSHA, scm.ApproveAction), http.StatusAccepted)
	h.settle(t)
	if len(posted(stub)) != 1 || !strings.Contains(h.log.String(), "the newest costroid check already concludes so") {
		t.Errorf("a second press posted again:\n%s", h.log.String())
	}
	signs := 0
	for _, call := range front.kms.Calls() {
		if call.Action == "Sign" {
			signs++
		}
	}
	if front.minted.Load() != 1 || signs != 1 {
		t.Errorf("%d installation tokens minted, %d KMS signatures: the token is not held for its hour", front.minted.Load(), signs)
	}
}

// TestTheApproveButtonNamesTheAmountWhereTheCommentDoes presses the button
// on a failed check, on repositories whose comment shows amounts and on ones
// whose comment withholds them. The check the service posts has the title and
// the summary costroid approve gives the same approval under the same
// setting: the amount and the budget where the comment shows amounts, and
// "Amounts are withheld here." where it does not. Where the service could not
// read the setting, the check withholds and the log says which read failed. A
// press that posts nothing reads neither the setting nor the visibility.
func TestTheApproveButtonNamesTheAmountWhereTheCommentDoes(t *testing.T) {
	const showCostOff, notASwitch = "comment:\n  show_cost: false\n", "comment:\n  show_cost: sometimes\n"
	yes, no := true, false
	visibility := func(stub *scmtest.Stub) int {
		return len(slices.DeleteFunc(slices.Clone(stub.Requests), func(request string) bool { return request != "GET /repos/"+approvalRepo }))
	}
	for name, tc := range map[string]struct {
		config  string
		private *bool
		amounts comment.Amounts
		// logs are what the log says of a setting it could not settle.
		logs []string
	}{
		"a private repository":                                   {private: &yes, amounts: comment.AmountsShown},
		"a private repository that sets show_cost off":           {config: showCostOff, private: &yes, amounts: comment.AmountsShown},
		"a public repository that leaves show_cost on":           {private: &no, amounts: comment.AmountsShown},
		"a visibility GitHub does not answer, with show_cost on": {amounts: comment.AmountsShown},
		"a public repository that sets show_cost off":            {config: showCostOff, private: &no, amounts: comment.AmountsOff},
		"a visibility GitHub does not answer, with show_cost off": {
			config: showCostOff, amounts: comment.AmountsUnsettled, logs: []string{`"visibility":"`},
		},
		"a public repository whose show_cost is no switch": {
			config: notASwitch, private: &no, amounts: comment.AmountsUnsettled, logs: []string{`"costroid_yml":"`},
		},
		"a visibility GitHub does not answer, with a show_cost that is no switch": {
			config: notASwitch, amounts: comment.AmountsUnsettled, logs: []string{`"costroid_yml":"`, `"visibility":"`},
		},
	} {
		t.Run(name, func(t *testing.T) {
			stub := approvalStub(t)
			if tc.config != "" {
				stub.Files["costroid.yml"] = tc.config
			}
			stub.Private = tc.private
			h := installed(t, stub)
			h.send(t, scm.EventCheckRun, "press-1", pressBody(approverLogin, acmeSHA, scm.ApproveAction), http.StatusAccepted)
			h.settle(t)
			checks := posted(stub)
			if len(checks) != 1 {
				t.Fatalf("%d checks posted\n%s", len(checks), h.log.String())
			}
			block, err := comment.ParseResult(failedText(t))
			if err != nil {
				t.Fatal(err)
			}
			approved := &comment.Approval{Approver: approverLogin, HeadSHA: acmeSHA, At: testNow}
			named := block
			named.CheckRunID = &checks[0].ID
			want, err := comment.Reconclude(named, approved, tc.amounts, failedCheck)
			if err != nil {
				t.Fatal(err)
			}
			if checks[0].Title != want.Title || checks[0].Summary != want.Summary {
				t.Errorf("the button titles the check\n%s\nand costroid approve, under the same setting\n%s\nsummary:\n%s", checks[0].Title, want.Title, checks[0].Summary)
			}
			amount := "this run cost USD 0.001619725 at list (List-equivalent, not billed), over the USD 0.001 run budget."
			shown := tc.amounts == comment.AmountsShown
			if strings.Contains(checks[0].Title, amount) != shown || strings.HasSuffix(checks[0].Title, " Amounts are withheld here.") == shown {
				t.Errorf("amounts shown is %t, and the title reads\n%s", shown, checks[0].Title)
			}
			if money := regexp.MustCompile(`[A-Z]{3} [0-9]`).FindString(checks[0].Title + checks[0].Summary); !shown && money != "" {
				t.Errorf("a check that withholds amounts names %q:\n%s", money, checks[0].Summary)
			}
			const withheld = `"msg":"check run withholds amounts; comment.show_cost could not be settled"`
			if said := strings.Contains(h.log.String(), withheld); said != (len(tc.logs) > 0) {
				t.Errorf("the log says the setting could not be settled: %t\n%s", said, h.log.String())
			}
			for _, why := range tc.logs {
				if !strings.Contains(lineOf(h.log.String(), withheld), why) {
					t.Errorf("the log does not say why with %s:\n%s", why, h.log.String())
				}
			}
			asked := visibility(stub)
			h.send(t, scm.EventCheckRun, "press-2", pressBody(approverLogin, acmeSHA, scm.ApproveAction), http.StatusAccepted)
			h.settle(t)
			if len(posted(stub)) != 1 || visibility(stub) != asked {
				t.Errorf("a press that posts nothing asked for the visibility: %d requests, were %d", visibility(stub), asked)
			}
		})
	}
}

// TestTheServiceLogsASettingsWarningAsJSON presses the button on a
// repository whose costroid.yml holds a reserved key, a key Costroid does not
// read, a YAML 1.1 word for show_cost and a receipts value that is no switch.
// The reader of the setting gives a warning for each. The service writes each
// as the line field of a JSON line of its log, in the file's order and with
// the repository, every line of the log is one JSON object, and nothing
// reaches the process's standard error, where a command prints the same
// warnings as plain lines. A press that posts nothing reads no setting and
// logs none.
func TestTheServiceLogsASettingsWarningAsJSON(t *testing.T) {
	const file = "catalog: 0.1.0\ncolour: blue\ncomment:\n  show_cost: no\n  receipts: maybe\n"
	_, want, err := comment.ParseConfig([]byte(file))
	if err != nil || len(want) != 4 {
		t.Fatalf("the reader gives %d warnings for the file, want 4: %v %q", len(want), err, want)
	}
	stub := approvalStub(t)
	stub.Files["costroid.yml"] = file
	stub.SetPrivate(false)
	h := installed(t, stub)
	plain, err := os.Create(filepath.Join(t.TempDir(), "stderr"))
	if err != nil {
		t.Fatal(err)
	}
	stderr := os.Stderr
	os.Stderr = plain
	t.Cleanup(func() { os.Stderr = stderr })
	logged := func() []string {
		var lines []string
		for line := range strings.SplitSeq(strings.TrimSuffix(h.log.String(), "\n"), "\n") {
			var entry map[string]any
			if err := json.Unmarshal([]byte(line), &entry); err != nil {
				t.Fatalf("a line of the service's log is not a JSON object: %v\n%s", err, line)
			}
			if entry["msg"] != "costroid.yml read with a warning" {
				continue
			}
			if entry["level"] != "WARN" || entry["repository"] != approvalRepo {
				t.Errorf("a settings warning is logged at level %v for the repository %v:\n%s", entry["level"], entry["repository"], line)
			}
			text, _ := entry["line"].(string)
			lines = append(lines, text)
		}
		return lines
	}
	h.send(t, scm.EventCheckRun, "press-1", pressBody(approverLogin, acmeSHA, scm.ApproveAction), http.StatusAccepted)
	h.settle(t)
	if checks := posted(stub); len(checks) != 1 || !strings.HasSuffix(checks[0].Title, " Amounts are withheld here.") {
		t.Fatalf("the press posted %+v\n%s", checks, h.log.String())
	}
	if got := logged(); !slices.Equal(got, want) {
		t.Errorf("the log holds the warnings %q, want %q", got, want)
	}
	h.send(t, scm.EventCheckRun, "press-2", pressBody(approverLogin, acmeSHA, scm.ApproveAction), http.StatusAccepted)
	h.settle(t)
	if got := logged(); len(posted(stub)) != 1 || len(got) != len(want) {
		t.Errorf("a press that posts nothing logged a settings warning: %q", got)
	}
	os.Stderr = stderr
	if err := plain.Close(); err != nil {
		t.Fatal(err)
	}
	if printed := readFile(t, plain.Name()); len(printed) != 0 {
		t.Errorf("the service wrote outside its log, on standard error:\n%s", printed)
	}
}

// lineOf is the line of a log that holds text, empty when none does.
func lineOf(log, text string) string {
	for line := range strings.SplitSeq(log, "\n") {
		if strings.Contains(line, text) {
			return line
		}
	}
	return ""
}

func TestApproveButtonRefusesAndChangesNothing(t *testing.T) {
	cases := map[string]struct {
		sender, head, button string
		setup                func(*scmtest.Stub, *hook)
		says                 string
	}{
		"the author":              {sender: authorLogin, says: scm.ErrAuthorApproval.Error()},
		"a reader":                {setup: func(s *scmtest.Stub, _ *hook) { s.Permission = "read" }, says: scm.ErrNoWriteRole.Error()},
		"an App's account":        {sender: "helper[bot]", says: scm.ErrBotApproval.Error()},
		"a moved head":            {head: movedHead, says: scm.ErrStaleHead.Error()},
		"another button":          {button: "explain", says: scm.ErrNotAnApproval.Error()},
		"an invalid setting":      {setup: func(s *scmtest.Stub, _ *hook) { s.Files["costroid.yml"] = "approval:\n  allow_bots: true\n" }, says: errSettings.Error()},
		"a suspended App":         {setup: func(_ *scmtest.Stub, h *hook) { suspend(h) }, says: errSuspended.Error()},
		"an unknown installation": {setup: func(_ *scmtest.Stub, h *hook) { forget(h) }, says: errNotInstalled.Error()},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			stub := approvalStub(t)
			h := installed(t, stub)
			if tc.setup != nil {
				tc.setup(stub, h)
			}
			sender, head, button := cmp.Or(tc.sender, approverLogin), cmp.Or(tc.head, acmeSHA), cmp.Or(tc.button, scm.ApproveAction)
			h.send(t, scm.EventCheckRun, "press", pressBody(sender, head, button), http.StatusAccepted)
			h.settle(t)
			if checks := posted(stub); len(checks) != 0 {
				t.Fatalf("a refused press posted %+v", checks)
			}
			if !strings.Contains(h.log.String(), `"msg":"webhook delivery changed nothing"`) || !strings.Contains(h.log.String(), tc.says) {
				t.Errorf("the log does not say %q:\n%s", tc.says, h.log.String())
			}
			h.send(t, scm.EventCheckRun, "press", pressBody(sender, head, button), http.StatusOK)
		})
	}
	t.Run("no App key", func(t *testing.T) {
		h := newHook(t, newMemoryInstallations(), nil)
		h.send(t, scm.EventInstallation, "install", installationBody(scm.InstallationCreated, widgets), http.StatusAccepted)
		h.send(t, scm.EventCheckRun, "press", pressBody(approverLogin, acmeSHA, scm.ApproveAction), http.StatusAccepted)
		h.settle(t)
		if !strings.Contains(h.log.String(), errNoAppKey.Error()) {
			t.Errorf("%s", h.log.String())
		}
	})
}

// TestAFailedReadReleasesTheDelivery: a press the host could not answer is
// processed again on GitHub's redelivery.
func TestAFailedReadReleasesTheDelivery(t *testing.T) {
	stub := approvalStub(t)
	h := installed(t, stub)
	stub.Fail["GET /collaborators/"] = http.StatusBadGateway
	body := pressBody(approverLogin, acmeSHA, scm.ApproveAction)
	h.send(t, scm.EventCheckRun, "press", body, http.StatusAccepted)
	h.settle(t)
	if len(posted(stub)) != 0 || !strings.Contains(h.log.String(), "a redelivery is processed") {
		t.Fatalf("%s", h.log.String())
	}
	delete(stub.Fail, "GET /collaborators/")
	h.send(t, scm.EventCheckRun, "press", body, http.StatusAccepted)
	h.settle(t)
	if checks := posted(stub); len(checks) != 1 || checks[0].Conclusion != policy.Neutral {
		t.Errorf("the redelivery posted %+v", checks)
	}
}

func suspend(h *hook) {
	if err := h.installations.SuspendInstallation(context.Background(), installationID, "example", true); err != nil {
		panic(err)
	}
}

func forget(h *hook) {
	if err := h.installations.DeleteInstallation(context.Background(), installationID); err != nil {
		panic(err)
	}
}

// approveOutcome is what costroid approve concludes for one review with the
// same inputs as a press: it reads the approval settings at the head of the
// base branch, and holds the review to every rule through scm.ApprovalOf, as
// approvalOf in cmd/costroid/approve.go does. The review is an approval of
// the commit the press names, submitted an hour before.
func approveOutcome(t *testing.T, stub *scmtest.Stub, sender, head string) (string, error) {
	t.Helper()
	ctx := context.Background()
	host, err := stubHosts{stub}.Repository(installationID, approvalRepo)
	if err != nil {
		t.Fatal(err)
	}
	pull, err := host.PullRequest(ctx, 7)
	if err != nil {
		t.Fatal(err)
	}
	var rules scm.ApprovalRules
	config := policy.ReadConfig(ctx, host, policy.Commits{Base: approvalBase, PullRequest: true})
	settings, err := policy.ParseApproval(config.Data)
	if config.Unread != nil || err != nil {
		return policy.Failure, errors.Join(config.Unread, err)
	}
	rules = scm.ApprovalRules{AllowAuthor: settings.AllowAuthor, Lasts: time.Duration(settings.ExpiresDays) * 24 * time.Hour}
	review := scm.Review{ID: 31, Reviewer: sender, State: scm.ReviewApproved, CommitID: head, SubmittedAt: testNow.Add(-time.Hour)}
	approval, err := scm.ApprovalOf(ctx, host, rules, pull, []scm.Review{review}, testNow)
	if err != nil {
		return policy.Failure, err
	}
	block, err := comment.ParseResult(failedText(t))
	if err != nil {
		t.Fatal(err)
	}
	return block.ApprovedConclusion(&comment.Approval{Approver: approval.Approver, HeadSHA: approval.HeadSHA, At: approval.At}), nil
}

// TestApprovalContract holds the approve button and costroid approve to one
// outcome for the same inputs: the same account, pull request, head, role and
// settings either approve under both, and the check concludes the same, or
// are refused under both, and the check stays as it was.
func TestApprovalContract(t *testing.T) {
	cases := []struct {
		name         string
		sender, head string
		permission   string
		config       string
		approves     bool
	}{
		{name: "a writer", sender: approverLogin, approves: true},
		{name: "an administrator", sender: approverLogin, permission: "admin", approves: true},
		{name: "a triager", sender: approverLogin, permission: "triage"},
		{name: "a reader", sender: approverLogin, permission: "read"},
		{name: "the author by default", sender: authorLogin},
		{name: "the author in another case", sender: "AUTHOR"},
		{name: "the author when allowed", sender: authorLogin, config: "approval:\n  allow_author: true\n", approves: true},
		{name: "an App's account", sender: "helper[bot]"},
		{name: "a commit the head has left", sender: approverLogin, head: movedHead},
		{name: "a setting Costroid does not read", sender: approverLogin, config: "approval:\n  allow_bots: true\n"},
		{name: "settings that only shorten the expiry", sender: approverLogin, config: "approval:\n  expires_days: 1\n", approves: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			stub := approvalStub(t)
			stub.Permission = cmp.Or(tc.permission, "write")
			if tc.config != "" {
				stub.Files["costroid.yml"] = tc.config
			}
			head := cmp.Or(tc.head, acmeSHA)
			want, refusal := approveOutcome(t, stub, tc.sender, head)
			if (refusal == nil) != tc.approves {
				t.Fatalf("costroid approve: %s, %v", want, refusal)
			}
			h := installed(t, stub)
			h.send(t, scm.EventCheckRun, "press", pressBody(tc.sender, head, scm.ApproveAction), http.StatusAccepted)
			h.settle(t)
			checks := posted(stub)
			if !tc.approves {
				if len(checks) != 0 {
					t.Fatalf("costroid approve refused (%v), and the button posted %+v", refusal, checks)
				}
				return
			}
			if len(checks) != 1 || checks[0].Conclusion != want {
				t.Fatalf("costroid approve concludes %s, and the button posted %+v\n%s", want, checks, h.log.String())
			}
		})
	}
}

// TestTheAccountsInTheContractAreTheOnesTheButtonSees keeps the contract's
// inputs honest: each sender is one the event parser passes, so a refusal
// above comes from the rules, not from the parser.
func TestTheAccountsInTheContractAreTheOnesTheButtonSees(t *testing.T) {
	for _, sender := range []string{approverLogin, authorLogin, "AUTHOR", "helper[bot]"} {
		if _, err := scm.ParseEvent(scm.EventCheckRun, []byte(pressBody(sender, acmeSHA, scm.ApproveAction))); err != nil {
			t.Errorf("%s: %v", sender, err)
		}
	}
}

// TestWebhookRefusesReplayAfterRestart runs two instances of serve on one
// database: the second refuses a delivery the first took, as a restarted
// instance would.
func TestWebhookRefusesReplayAfterRestart(t *testing.T) {
	db := storetest.New(t)
	t.Setenv(envDatabase, db.AppURL)
	t.Setenv(envMigration, db.OwnerURL)
	t.Setenv(envAudience, audience)
	t.Setenv(envPseudonym, base64.StdEncoding.EncodeToString(make([]byte, minPseudonymKeyBytes)))
	t.Setenv(envWebhook, webhookSecretText)
	body := installationBody(scm.InstallationCreated, widgets)
	deliver := func(addr, delivery string) int {
		request, err := http.NewRequest(http.MethodPost, "http://"+addr+webhookPath, strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		request.Header.Set("X-GitHub-Event", scm.EventInstallation)
		request.Header.Set("X-GitHub-Delivery", delivery)
		request.Header.Set("X-Hub-Signature-256", signed(webhookSecretText, body))
		response, err := http.DefaultClient.Do(request)
		if err != nil {
			t.Fatal(err)
		}
		_ = response.Body.Close()
		return response.StatusCode
	}
	first, _, _ := startServe(t, anyPort)
	second, log, _ := startServe(t, anyPort)
	if status := deliver(first, "taken-by-the-first"); status != http.StatusAccepted {
		t.Fatalf("the first instance answered %d", status)
	}
	if status := deliver(second, "taken-by-the-first"); status != http.StatusOK {
		t.Errorf("the second instance answered %d to the first's delivery", status)
	}
	if status := deliver(second, "new-to-both"); status != http.StatusAccepted {
		t.Errorf("the second instance answered %d to a new delivery", status)
	}
	if strings.Contains(log.String(), webhookSecretText) {
		t.Error("serve logged the webhook secret")
	}
}
