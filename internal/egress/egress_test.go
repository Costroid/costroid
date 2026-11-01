package egress

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

const (
	testTimeout = 5 * time.Second
	loopback    = "127.0.0.1"
	localName   = "localhost"
)

// tally is a Recorder for tests.
type tally struct {
	mu   sync.Mutex
	seen []string
}

func (r *tally) Record(host, outcome string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.seen = append(r.seen, host+" "+outcome)
}

func (r *tally) lines() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.seen)
}

// connectionLog is a listener that records every connection it accepts.
type connectionLog struct {
	listener net.Listener
	accepted atomic.Int64
	arrived  chan struct{}
}

func listenAndLog(t *testing.T) *connectionLog {
	t.Helper()
	listener, err := net.Listen("tcp", loopback+":0")
	if err != nil {
		t.Fatal(err)
	}
	log := &connectionLog{listener: listener, arrived: make(chan struct{}, 16)}
	t.Cleanup(func() { _ = listener.Close() })
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			log.accepted.Add(1)
			log.arrived <- struct{}{}
			_ = conn.Close()
		}
	}()
	return log
}

func (l *connectionLog) port() string {
	return strconv.Itoa(l.listener.Addr().(*net.TCPAddr).Port)
}

// connectionsBefore dials the listener itself and counts what arrived ahead of
// that dial. Connections are accepted in order, so a stray call made earlier
// cannot arrive late and go unseen.
func (l *connectionLog) connectionsBefore(t *testing.T) int64 {
	t.Helper()
	conn, err := net.Dial("tcp", l.listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	select {
	case <-l.arrived:
	case <-time.After(testTimeout):
		t.Fatal("the listener saw no connection at all")
	}
	for len(l.arrived) > 0 {
		<-l.arrived
	}
	return l.accepted.Load() - 1
}

func get(t *testing.T, client *http.Client, url string) (*http.Response, error) {
	t.Helper()
	request, err := http.NewRequestWithContext(context.Background(), http.MethodGet, url, nil)
	if err != nil {
		t.Fatal(err)
	}
	return client.Do(request)
}

func wantRefused(t *testing.T, err error, host string) {
	t.Helper()
	var refused *RefusedError
	if !errors.As(err, &refused) {
		t.Fatalf("err = %v, want a RefusedError", err)
	}
	if refused.Host != host {
		t.Fatalf("names %q, want %q", refused.Host, host)
	}
}

func TestACallToAHostOffTheListNeverOpensASocket(t *testing.T) {
	for name, list := range map[string][]string{"empty list": nil, "another host": {"gitlab.com"}} {
		t.Run(name, func(t *testing.T) {
			listener := listenAndLog(t)
			record := &tally{}
			client := New(list, record).Client(testTimeout)
			_, err := get(t, client, "http://"+loopback+":"+listener.port()+"/")
			wantRefused(t, err, loopback)
			if opened := listener.connectionsBefore(t); opened != 0 {
				t.Fatalf("%d connections reached the listener", opened)
			}
			if got := record.lines(); !slices.Equal(got, []string{loopback + " " + Refused}) {
				t.Fatalf("recorded %v", got)
			}
		})
	}
}

// TestTheDialCheckHoldsWithoutTheRequestCheck reaches the dial through a bare
// transport, so nothing else stands between the call and the socket.
func TestTheDialCheckHoldsWithoutTheRequestCheck(t *testing.T) {
	listener := listenAndLog(t)
	bare := &http.Client{Transport: &http.Transport{DialContext: New(nil, &tally{}).DialContext}, Timeout: testTimeout}
	_, err := get(t, bare, "http://"+loopback+":"+listener.port()+"/")
	wantRefused(t, err, loopback)
	if opened := listener.connectionsBefore(t); opened != 0 {
		t.Fatalf("%d connections reached the listener", opened)
	}
}

type recordingTransport struct {
	called atomic.Bool
}

func (r *recordingTransport) RoundTrip(*http.Request) (*http.Response, error) {
	r.called.Store(true)
	return nil, errors.New("the transport was reached")
}

type trackedBody struct {
	io.Reader
	closed bool
}

func (b *trackedBody) Close() error {
	b.closed = true
	return nil
}

func TestTheRequestCheckHoldsWithoutTheDialCheck(t *testing.T) {
	gate := New(nil, &tally{})
	transport := &recordingTransport{}
	gate.transport = transport
	body := &trackedBody{Reader: strings.NewReader("x")}
	request, err := http.NewRequestWithContext(context.Background(), http.MethodPost, "http://gitlab.com/", body)
	if err != nil {
		t.Fatal(err)
	}
	_, err = gate.RoundTrip(request)
	wantRefused(t, err, "gitlab.com")
	if transport.called.Load() {
		t.Fatal("the transport was reached")
	}
	if !body.closed {
		t.Fatal("a refused request left its body open")
	}
}

func TestARedirectFromAListedHostToAnUnlistedOneIsRefused(t *testing.T) {
	target := listenAndLog(t)
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "http://"+localName+":"+target.port()+"/next", http.StatusFound)
	}))
	defer source.Close()
	record := &tally{}
	_, err := get(t, New([]string{loopback}, record).Client(testTimeout), source.URL)
	wantRefused(t, err, localName)
	if opened := target.connectionsBefore(t); opened != 0 {
		t.Fatalf("the redirect opened %d connections", opened)
	}
	want := []string{loopback + " " + Completed, localName + " " + Refused}
	if got := record.lines(); !slices.Equal(got, want) {
		t.Fatalf("recorded %v, want %v", got, want)
	}
}

func TestACallToAListedHostGoesThrough(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTeapot)
	}))
	defer server.Close()
	record := &tally{}
	response, err := get(t, New([]string{loopback}, record).Client(testTimeout), server.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusTeapot {
		t.Fatalf("status %d", response.StatusCode)
	}
	if got := record.lines(); !slices.Equal(got, []string{loopback + " " + Completed}) {
		t.Fatalf("an answer of any status is completed: %v", got)
	}
}

func TestACallThatGetsNoAnswerCountsAsFailed(t *testing.T) {
	listener, err := net.Listen("tcp", loopback+":0")
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	record := &tally{}
	_, err = get(t, New([]string{loopback}, record).Client(testTimeout), "http://"+address+"/")
	if err == nil {
		t.Fatal("a closed port answered")
	}
	var refused *RefusedError
	if errors.As(err, &refused) {
		t.Fatalf("a listed host was refused: %v", err)
	}
	if got := record.lines(); !slices.Equal(got, []string{loopback + " " + Failed}) {
		t.Fatalf("recorded %v", got)
	}
}

func TestHostsCompareAfterCaseAndATrailingDot(t *testing.T) {
	gate := New([]string{"GitLab.Example."}, &tally{})
	for _, host := range []string{"gitlab.example", "GITLAB.EXAMPLE", "gitlab.example."} {
		if !gate.Allows(host) {
			t.Errorf("%q is the listed host", host)
		}
	}
	for _, host := range []string{"evil.gitlab.example", "gitlab.example.evil", "gitlab.example..", "example", ""} {
		if gate.Allows(host) {
			t.Errorf("%q is not the listed host", host)
		}
	}
}

func TestAListEntryMustBeAnExactHost(t *testing.T) {
	for _, entry := range []string{
		"*", "*.github.com", "git*.com", "https://gitlab.com", "gitlab.com:443", "gitlab.com/keys", "a..b", ".", "..",
		"user@gitlab.com", strings.Repeat("a", maxHostBytes+1),
	} {
		if hosts, err := ParseList([]string{entry}, ""); err == nil {
			t.Errorf("flag %q was accepted: %v", entry, hosts)
		}
		if hosts, err := ParseList(nil, entry); err == nil {
			t.Errorf("variable %q was accepted: %v", entry, hosts)
		}
	}
}

func TestTheFlagAndTheVariableAddUp(t *testing.T) {
	hosts, err := ParseList([]string{"Gitlab.example"}, " token.actions.githubusercontent.com ,, ::1,127.0.0.1,")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"gitlab.example", "token.actions.githubusercontent.com", "::1", loopback}
	if !slices.Equal(hosts, want) {
		t.Fatalf("%v", hosts)
	}
}

func TestAnEmptyVariableIsAnEmptyList(t *testing.T) {
	hosts, err := ParseList(nil, "")
	if err != nil {
		t.Fatal(err)
	}
	if got := New(hosts, &tally{}).Hosts(); len(got) != 0 {
		t.Fatalf("%v", got)
	}
}

func TestADestinationThatIsNoHostNameIsNeverRepeated(t *testing.T) {
	record := &tally{}
	gate := New(nil, record)
	_, err := gate.check("secret value/path?query=1")
	wantRefused(t, err, unnamed)
	if got := record.lines(); !slices.Equal(got, []string{unnamed + " " + Refused}) {
		t.Fatalf("recorded %v", got)
	}
	if gate.Allows(unnamed) {
		t.Fatal("the stand-in is on the list")
	}
}

func TestARefusalNamesTheHostAndTheFlag(t *testing.T) {
	message := (&RefusedError{Host: "gitlab.com"}).Error()
	for _, want := range []string{"gitlab.com", Flag} {
		if !strings.Contains(message, want) {
			t.Errorf("%q lacks %q", message, want)
		}
	}
}

func TestAClientReadsNoProxyFromTheEnvironment(t *testing.T) {
	transport, ok := New(nil, &tally{}).transport.(*http.Transport)
	if !ok || transport.Proxy != nil {
		t.Fatalf("%#v", transport)
	}
}

// memorySink stands in for the store.
type memorySink struct {
	mu     sync.Mutex
	failed bool
	writes [][]Count
}

func (s *memorySink) AddEgress(_ context.Context, counts []Count) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.failed {
		return errors.New("the database is away")
	}
	s.writes = append(s.writes, counts)
	return nil
}

func fixedDay() time.Time {
	return time.Date(2026, 11, 2, 23, 59, 0, 0, time.FixedZone("east", 3*60*60))
}

func quiet() *slog.Logger {
	return slog.New(slog.DiscardHandler)
}

func TestACounterThatCannotBeWrittenNeverFailsItsCall(t *testing.T) {
	sink := &memorySink{failed: true}
	counters := NewCounters(sink, fixedDay, quiet())
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	defer server.Close()
	client := New([]string{loopback}, counters).Client(testTimeout)
	response, err := get(t, client, server.URL)
	if err != nil {
		t.Fatalf("the call failed with the database away: %v", err)
	}
	_ = response.Body.Close()
	if err := counters.Flush(context.Background()); err == nil {
		t.Fatal("the failed write was not reported to the flusher")
	}
	sink.failed = false
	if err := counters.Flush(context.Background()); err != nil {
		t.Fatal(err)
	}
	want := []Count{{"2026-11-02", loopback, Completed, 1}}
	if len(sink.writes) != 1 || !slices.Equal(sink.writes[0], want) {
		t.Fatalf("the count did not survive the failed write: %v", sink.writes)
	}
}

func TestCountersGroupByUTCDayHostAndOutcome(t *testing.T) {
	sink := &memorySink{}
	counters := NewCounters(sink, fixedDay, quiet())
	counters.Record("b.example", Refused)
	counters.Record("a.example", Completed)
	counters.Record("b.example", Refused)
	counters.Record("b.example", Completed)
	if err := counters.Flush(context.Background()); err != nil {
		t.Fatal(err)
	}
	want := []Count{
		{"2026-11-02", "a.example", Completed, 1},
		{"2026-11-02", "b.example", Completed, 1},
		{"2026-11-02", "b.example", Refused, 2},
	}
	if len(sink.writes) != 1 || !slices.Equal(sink.writes[0], want) {
		t.Fatalf("%v", sink.writes)
	}
	if err := counters.Flush(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(sink.writes) != 1 {
		t.Fatalf("a flush with nothing pending wrote: %v", sink.writes)
	}
}

func TestACountHasNoFieldForAPathOrAnythingElse(t *testing.T) {
	var fields []string
	for field := range reflect.TypeFor[Count]().Fields() {
		fields = append(fields, field.Name)
	}
	if !slices.Equal(fields, []string{"Day", "Host", "Outcome", "Count"}) {
		t.Fatalf("%v", fields)
	}
}

func TestACallsPathNeverReachesItsCount(t *testing.T) {
	sink := &memorySink{}
	counters := NewCounters(sink, fixedDay, quiet())
	client := New(nil, counters).Client(testTimeout)
	_, err := get(t, client, "https://user:pass@gitlab.example/secret/path?token=value#part")
	wantRefused(t, err, "gitlab.example")
	if err := counters.Flush(context.Background()); err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(sink.writes)
	if err != nil {
		t.Fatal(err)
	}
	for _, leak := range []string{"secret", "path", "token", "value", "user", "pass", "part"} {
		if strings.Contains(string(encoded), leak) {
			t.Errorf("%q reached the counters: %s", leak, encoded)
		}
	}
}

func TestRunFlushesOnceMoreWhenItsContextEnds(t *testing.T) {
	sink := &memorySink{}
	counters := NewCounters(sink, fixedDay, quiet())
	counters.Record("a.example", Refused)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	counters.Run(ctx, time.Hour)
	if len(sink.writes) != 1 {
		t.Fatalf("%d writes", len(sink.writes))
	}
}

func TestRunLogsAFailedWriteAndKeepsGoing(t *testing.T) {
	sink := &memorySink{failed: true}
	var log bytes.Buffer
	counters := NewCounters(sink, fixedDay, slog.New(slog.NewTextHandler(&log, nil)))
	counters.Record("a.example", Refused)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	counters.Run(ctx, time.Hour)
	if !strings.Contains(log.String(), "egress counters not written") {
		t.Fatalf("log %q", log.String())
	}
}
