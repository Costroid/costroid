package scm_test

import (
	"bufio"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Costroid/costroid/internal/scm"
)

const ackLimit = 2 * time.Second

var secret = []byte("webhook-secret")

func sign(body string) string {
	mac := hmac.New(sha256.New, secret)
	mac.Write([]byte(body))
	return "sha256=" + hex.EncodeToString(mac.Sum(nil))
}

func pullRequestBody(updated time.Time) string {
	return fmt.Sprintf(`{"action":"synchronize","installation":{"id":58},"repository":{"full_name":"example/widgets"},"sender":{"login":"octocat"},"pull_request":{"number":7,"updated_at":%q,"head":{"sha":%q}}}`,
		updated.Format(time.RFC3339), sampleSHA)
}

func installationBody(action string) string {
	return fmt.Sprintf(`{"action":%q,"installation":{"id":58,"account":{"login":"example"}},"repositories":[{"id":101,"full_name":"example/widgets"}],"sender":{"login":"octocat"}}`, action)
}

func repositoriesBody(action string) string {
	return fmt.Sprintf(`{"action":%q,"installation":{"id":58,"account":{"login":"example"}},"repositories_added":[{"id":102,"full_name":"example/gadgets"}],`+
		`"repositories_removed":[{"id":101,"full_name":"example/widgets"}],"sender":{"login":"octocat"}}`, action)
}

func approveBody() string {
	return fmt.Sprintf(`{"action":"requested_action","installation":{"id":58},"repository":{"full_name":"example/widgets"},"sender":{"login":"octocat"},"check_run":{"head_sha":%q,"pull_requests":[{"number":7}]},"requested_action":{"identifier":"approve"}}`, sampleSHA)
}

type receiver struct {
	webhook *scm.Webhook
	clock   *clock
	queue   *scm.Queue
}

func newReceiver(size int) *receiver {
	clock := newClock()
	queue := scm.NewQueue(size)
	return &receiver{
		clock: clock,
		queue: queue,
		webhook: &scm.Webhook{
			Secret:     secret,
			Deliveries: scm.NewMemoryDeliveries(),
			Queue:      queue,
			Now:        clock.now,
		},
	}
}

func (r *receiver) deliver(event, delivery, body, signature string) int {
	request := httptest.NewRequest(http.MethodPost, "/webhook", strings.NewReader(body))
	request.Header.Set("X-GitHub-Event", event)
	request.Header.Set("X-GitHub-Delivery", delivery)
	request.Header.Set("X-Hub-Signature-256", signature)
	recorder := httptest.NewRecorder()
	r.webhook.ServeHTTP(recorder, request)
	return recorder.Code
}

// drain runs the queue up to a sentinel. The queue is first in, first out, so
// everything queued before it has passed.
func (r *receiver) drain(t *testing.T) []scm.Event {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	var events []scm.Event
	seen := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		r.queue.Run(ctx, func(_ context.Context, event scm.Event) {
			if event.Name == "sentinel" {
				close(seen)
				return
			}
			events = append(events, event)
		})
	}()
	for !r.queue.Offer(scm.Event{Name: "sentinel"}) {
		time.Sleep(time.Millisecond)
	}
	<-seen
	cancel()
	<-done
	return events
}

func TestAVerifiedDeliveryIsQueuedAndAcknowledged(t *testing.T) {
	r := newReceiver(4)
	body := pullRequestBody(r.clock.now())
	if status := r.deliver(scm.EventPullRequest, "d-1", body, sign(body)); status != http.StatusAccepted {
		t.Fatalf("status = %d", status)
	}
	events := r.drain(t)
	if len(events) != 1 || events[0].Delivery != "d-1" || events[0].PullRequest != 7 || events[0].Installation != 58 || events[0].HeadSHA != sampleSHA {
		t.Errorf("events = %+v", events)
	}
}

func TestASignatureThatDoesNotVerifyIsRefusedBeforeParsing(t *testing.T) {
	r := newReceiver(4)
	body := pullRequestBody(r.clock.now())
	wrongKey := hmac.New(sha256.New, []byte("other"))
	wrongKey.Write([]byte(body))
	cases := map[string]string{
		"absent":      "",
		"tampered":    sign(body + " "),
		"wrong key":   "sha256=" + hex.EncodeToString(wrongKey.Sum(nil)),
		"sha1 prefix": strings.Replace(sign(body), "sha256=", "sha1=", 1),
		"not hex":     "sha256=zz",
		"bare hex":    strings.TrimPrefix(sign(body), "sha256="),
	}
	for name, signature := range cases {
		if status := r.deliver(scm.EventPullRequest, "d-"+strings.ReplaceAll(name, " ", "-"), body, signature); status != http.StatusUnauthorized {
			t.Errorf("%s: status = %d", name, status)
		}
	}
	if status := r.deliver(scm.EventPullRequest, "d-junk", "{", "sha256=00"); status != http.StatusUnauthorized {
		t.Errorf("junk answered %d, so it reached the parser", status)
	}
	if events := r.drain(t); len(events) != 0 {
		t.Errorf("events = %+v", events)
	}
}

func TestAnEmptySecretVerifiesNothing(t *testing.T) {
	mac := hmac.New(sha256.New, nil)
	mac.Write([]byte("body"))
	if scm.Verify(nil, []byte("body"), "sha256="+hex.EncodeToString(mac.Sum(nil))) {
		t.Error("an empty secret verified a body")
	}
}

func TestARepeatedDeliveryIsAcknowledgedAndNotQueuedAgain(t *testing.T) {
	r := newReceiver(4)
	body := pullRequestBody(r.clock.now())
	for range 3 {
		if status := r.deliver(scm.EventPullRequest, "same", body, sign(body)); status != http.StatusAccepted && status != http.StatusOK {
			t.Fatalf("status = %d", status)
		}
	}
	if events := r.drain(t); len(events) != 1 {
		t.Errorf("%d events", len(events))
	}
}

func TestAnEventOutsideTheReplayWindowIsRefused(t *testing.T) {
	r := newReceiver(4)
	for name, offset := range map[string]time.Duration{"old": -scm.ReplayWindow - time.Second, "future": scm.ReplayWindow + time.Second} {
		body := pullRequestBody(r.clock.now().Add(offset))
		if status := r.deliver(scm.EventPullRequest, "d-"+name, body, sign(body)); status != http.StatusForbidden {
			t.Errorf("%s: status = %d", name, status)
		}
	}
	edge := pullRequestBody(r.clock.now().Add(-scm.ReplayWindow))
	if status := r.deliver(scm.EventPullRequest, "d-edge", edge, sign(edge)); status != http.StatusAccepted {
		t.Errorf("the window's edge: status = %d", status)
	}
}

func TestAReplayedApproveIsRefusedByItsClaimForTheRetention(t *testing.T) {
	r := newReceiver(4)
	body := approveBody()
	if status := r.deliver(scm.EventCheckRun, "approve-1", body, sign(body)); status != http.StatusAccepted {
		t.Fatalf("status = %d", status)
	}
	r.clock.advance(scm.DeliveryRetention)
	if status := r.deliver(scm.EventCheckRun, "approve-1", body, sign(body)); status != http.StatusOK {
		t.Errorf("replay at the retention: status = %d", status)
	}
	if events := r.drain(t); len(events) != 1 {
		t.Errorf("%d events", len(events))
	}
}

func TestMemoryDeliveriesForgetAnIDPastTheRetention(t *testing.T) {
	store := scm.NewMemoryDeliveries()
	at := newClock().now()
	if first, err := store.Claim(context.Background(), "a", at); err != nil || !first {
		t.Fatal(first, err)
	}
	if first, err := store.Claim(context.Background(), "a", at.Add(scm.DeliveryRetention+time.Second)); err != nil || !first {
		t.Errorf("the id stayed claimed: %v, %v", first, err)
	}
}

func TestEventsTheAppDoesNotProcessAreAcknowledgedAndDropped(t *testing.T) {
	r := newReceiver(4)
	created := strings.Replace(approveBody(), "requested_action\"", "created\"", 1)
	for _, delivery := range []struct{ event, body string }{{"ping", `{"zen":"ok"}`}, {"star", "not json"}, {scm.EventCheckRun, created}} {
		if status := r.deliver(delivery.event, "d-"+strings.ReplaceAll(delivery.event, "_", "-"), delivery.body, sign(delivery.body)); status != http.StatusAccepted {
			t.Errorf("%s: status = %d", delivery.event, status)
		}
	}
	if events := r.drain(t); len(events) != 0 {
		t.Errorf("events = %+v", events)
	}
}

func TestAMalformedDeliveryIsRefused(t *testing.T) {
	r := newReceiver(4)
	body := pullRequestBody(r.clock.now())
	if status := r.deliver(scm.EventPullRequest, "", body, sign(body)); status != http.StatusBadRequest {
		t.Errorf("no delivery id: %d", status)
	}
	if status := r.deliver(scm.EventPullRequest, "bad id\n", body, sign(body)); status != http.StatusBadRequest {
		t.Errorf("bad delivery id: %d", status)
	}
	if status := r.deliver(scm.EventPullRequest, "d-1", "{", sign("{")); status != http.StatusBadRequest {
		t.Errorf("bad JSON: %d", status)
	}
	recorder := httptest.NewRecorder()
	r.webhook.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/webhook", nil))
	if recorder.Code != http.StatusMethodNotAllowed {
		t.Errorf("GET: %d", recorder.Code)
	}
}

func TestAFullQueueRefusesAndARedeliveryIsProcessedLater(t *testing.T) {
	r := newReceiver(1)
	first := pullRequestBody(r.clock.now())
	if status := r.deliver(scm.EventPullRequest, "fills", first, sign(first)); status != http.StatusAccepted {
		t.Fatalf("status = %d", status)
	}
	if status := r.deliver(scm.EventPullRequest, "waits", first, sign(first)); status != http.StatusServiceUnavailable {
		t.Fatalf("status = %d", status)
	}
	if events := r.drain(t); len(events) != 1 {
		t.Fatalf("%d events", len(events))
	}
	if status := r.deliver(scm.EventPullRequest, "waits", first, sign(first)); status != http.StatusAccepted {
		t.Errorf("the redelivery answered %d: the refused delivery stayed claimed", status)
	}
}

func TestTheAcknowledgementDoesNotWaitForProcessing(t *testing.T) {
	r := newReceiver(8)
	blocked := make(chan struct{})
	defer close(blocked)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go r.queue.Run(ctx, func(context.Context, scm.Event) { <-blocked })
	server := httptest.NewServer(r.webhook)
	defer server.Close()
	for index := range 4 {
		body := pullRequestBody(r.clock.now())
		request, err := http.NewRequest(http.MethodPost, server.URL, strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		request.Header.Set("X-GitHub-Event", scm.EventPullRequest)
		request.Header.Set("X-GitHub-Delivery", fmt.Sprintf("d-%d", index))
		request.Header.Set("X-Hub-Signature-256", sign(body))
		started := time.Now()
		response, err := server.Client().Do(request)
		if err != nil {
			t.Fatal(err)
		}
		response.Body.Close()
		if elapsed := time.Since(started); elapsed >= ackLimit || response.StatusCode != http.StatusAccepted {
			t.Errorf("delivery %d: %d after %s", index, response.StatusCode, elapsed)
		}
	}
}

func TestAStalledBodyIsAnsweredInsideTwoSeconds(t *testing.T) {
	r := newReceiver(1)
	server := httptest.NewServer(r.webhook)
	defer server.Close()
	connection, err := net.Dial("tcp", server.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	started := time.Now()
	if _, err := fmt.Fprintf(connection, "POST / HTTP/1.1\r\nHost: x\r\nContent-Length: 1000\r\n\r\npartial"); err != nil {
		t.Fatal(err)
	}
	if err := connection.SetReadDeadline(started.Add(5 * time.Second)); err != nil {
		t.Fatal(err)
	}
	line, err := bufio.NewReader(connection).ReadString('\n')
	if err != nil {
		t.Fatal(err)
	}
	if elapsed := time.Since(started); elapsed >= ackLimit || !strings.Contains(line, "400") {
		t.Errorf("%q after %s", line, elapsed)
	}
}

type stalledStore struct{}

func (stalledStore) Claim(ctx context.Context, _ string, _ time.Time) (bool, error) {
	<-ctx.Done()
	return false, ctx.Err()
}

func (stalledStore) Release(context.Context, string) error {
	return errors.New("unreachable")
}

func TestAStalledDeliveryStoreIsAnsweredInsideTwoSeconds(t *testing.T) {
	r := newReceiver(1)
	r.webhook.Deliveries = stalledStore{}
	body := pullRequestBody(r.clock.now())
	started := time.Now()
	status := r.deliver(scm.EventPullRequest, "d-1", body, sign(body))
	if elapsed := time.Since(started); elapsed >= ackLimit || status != http.StatusServiceUnavailable {
		t.Errorf("%d after %s", status, elapsed)
	}
}

func TestParseEventRefusesFieldsThatFailTheirFormat(t *testing.T) {
	good := pullRequestBody(newClock().now())
	cases := map[string]string{
		"sender":       strings.Replace(good, `"octocat"`, `"<script>"`, 1),
		"repository":   strings.Replace(good, `example/widgets`, `example/wid gets`, 1),
		"head":         strings.Replace(good, sampleSHA, "HEAD", 1),
		"installation": strings.Replace(good, `"id":58`, `"id":0`, 1),
		"number":       strings.Replace(good, `"number":7`, `"number":0`, 1),
		"action":       strings.Replace(good, `"synchronize"`, `"Sync It"`, 1),
		"time":         strings.Replace(good, `"updated_at"`, `"other"`, 1),
	}
	for name, body := range cases {
		if _, err := scm.ParseEvent(scm.EventPullRequest, []byte(body)); err == nil {
			t.Errorf("%s: parsed", name)
		}
	}
}

func FuzzParseEvent(f *testing.F) {
	at := newClock().now()
	f.Add(scm.EventPullRequest, []byte(pullRequestBody(at)))
	f.Add(scm.EventCheckRun, []byte(approveBody()))
	f.Add("ping", []byte(`{}`))
	f.Add(scm.EventPullRequest, []byte(`{"pull_request":null}`))
	f.Add(scm.EventCheckRun, []byte(`{"check_run":{"pull_requests":[{"number":-1}]}}`))
	f.Add(scm.EventInstallation, []byte(installationBody("created")))
	f.Add(scm.EventInstallationRepositories, []byte(repositoriesBody("removed")))
	f.Fuzz(func(t *testing.T, name string, body []byte) {
		event, err := scm.ParseEvent(name, body)
		if err != nil {
			return
		}
		installation := name == scm.EventInstallation || name == scm.EventInstallationRepositories
		if event.Name != name || event.Installation <= 0 || event.PullRequest < 0 || (event.HeadSHA == "") != installation || (event.Account != "") != installation {
			t.Errorf("parsed an invalid event: %+v", event)
		}
		texts := []string{event.Action, event.Repository, event.Sender, event.HeadSHA, event.RequestedAction, event.BaseBranch, event.Account}
		for _, repository := range append(slices.Clone(event.Added), event.Removed...) {
			if repository.ID <= 0 {
				t.Errorf("parsed repository id %d", repository.ID)
			}
			texts = append(texts, repository.FullName)
		}
		for _, text := range texts {
			if strings.ContainsAny(text, "<>`@\n\r ") {
				t.Errorf("unsafe text %q in %+v", text, event)
			}
		}
	})
}

func FuzzWebhookNeverPanicsOrFailsOnASignedBody(f *testing.F) {
	at := newClock().now()
	f.Add(scm.EventPullRequest, "d-1", pullRequestBody(at))
	f.Add(scm.EventCheckRun, "d-2", approveBody())
	f.Add("ping", "", "{}")
	f.Fuzz(func(t *testing.T, name, delivery, body string) {
		r := newReceiver(2)
		status := r.deliver(name, strings.ReplaceAll(delivery, "\n", ""), body, sign(body))
		switch status {
		case http.StatusAccepted, http.StatusOK, http.StatusBadRequest, http.StatusForbidden:
		default:
			t.Errorf("status = %d", status)
		}
	})
}

func TestInstallationEventsAreQueuedWithTheirRepositories(t *testing.T) {
	widgets := scm.InstalledRepository{ID: 101, FullName: "example/widgets"}
	gadgets := scm.InstalledRepository{ID: 102, FullName: "example/gadgets"}
	for _, tc := range []struct {
		name, action   string
		body           string
		added, removed []scm.InstalledRepository
	}{
		{scm.EventInstallation, scm.InstallationCreated, installationBody(scm.InstallationCreated), []scm.InstalledRepository{widgets}, nil},
		{scm.EventInstallation, scm.InstallationDeleted, installationBody(scm.InstallationDeleted), []scm.InstalledRepository{widgets}, nil},
		{scm.EventInstallation, scm.InstallationSuspended, installationBody(scm.InstallationSuspended), []scm.InstalledRepository{widgets}, nil},
		{scm.EventInstallation, scm.InstallationUnsuspended, installationBody(scm.InstallationUnsuspended), []scm.InstalledRepository{widgets}, nil},
		{scm.EventInstallationRepositories, scm.RepositoriesAdded, repositoriesBody(scm.RepositoriesAdded), []scm.InstalledRepository{gadgets}, []scm.InstalledRepository{widgets}},
		{scm.EventInstallationRepositories, scm.RepositoriesRemoved, repositoriesBody(scm.RepositoriesRemoved), []scm.InstalledRepository{gadgets}, []scm.InstalledRepository{widgets}},
	} {
		event, err := scm.ParseEvent(tc.name, []byte(tc.body))
		if err != nil {
			t.Fatalf("%s %s: %v", tc.name, tc.action, err)
		}
		if event.Action != tc.action || event.Installation != 58 || event.Account != "example" || !slices.Equal(event.Added, tc.added) || !slices.Equal(event.Removed, tc.removed) {
			t.Errorf("%s %s: %+v", tc.name, tc.action, event)
		}
	}
	if _, err := scm.ParseEvent(scm.EventInstallation, []byte(installationBody("new_permissions_accepted"))); !errors.Is(err, scm.ErrIgnoredEvent) {
		t.Errorf("new_permissions_accepted: %v", err)
	}
	good := installationBody(scm.InstallationCreated)
	for name, body := range map[string]string{
		"account":         strings.Replace(good, `"login":"example"`, `"login":"<script>"`, 1),
		"no account":      strings.Replace(good, `"account":{"login":"example"}`, `"account":null`, 1),
		"an App account":  strings.Replace(good, `"login":"example"`, `"login":"example[bot]"`, 1),
		"repository name": strings.Replace(good, `example/widgets`, `example/wid gets`, 1),
		"repository id":   strings.Replace(good, `"id":101`, `"id":0`, 1),
		"installation":    strings.Replace(good, `"id":58`, `"id":-58`, 1),
		"action":          strings.Replace(good, `"created"`, `"Created It"`, 1),
	} {
		if _, err := scm.ParseEvent(scm.EventInstallation, []byte(body)); err == nil || errors.Is(err, scm.ErrIgnoredEvent) {
			t.Errorf("%s: %v", name, err)
		}
	}
}

func TestACheckRunStatesItsBaseBranchInItsFormOnly(t *testing.T) {
	with := strings.Replace(approveBody(), `{"number":7}`, `{"number":7,"base":{"ref":"release/2026-11"}}`, 1)
	event, err := scm.ParseEvent(scm.EventCheckRun, []byte(with))
	if err != nil || event.BaseBranch != "release/2026-11" {
		t.Fatalf("%+v, %v", event, err)
	}
	for _, ref := range []string{"../main", "-main", "main branch", ""} {
		body := strings.Replace(with, "release/2026-11", ref, 1)
		if event, err := scm.ParseEvent(scm.EventCheckRun, []byte(body)); err != nil || event.BaseBranch != "" {
			t.Errorf("%q: %+v, %v", ref, event, err)
		}
	}
}
