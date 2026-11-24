package server

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgproto3"

	"github.com/Costroid/costroid/internal/cli"
	"github.com/Costroid/costroid/internal/store/storetest"
)

// waitLine is the message of the line serve logs for each try of a database
// that does not answer yet.
const waitLine = "the database does not answer yet"

// frontPassword is the password in every connection string these tests hand
// serve. No line serve prints may hold it.
const frontPassword = "front-password-3f9c2a71"

var (
	starting      = &pgproto3.ErrorResponse{Severity: "FATAL", Code: "57P03", Message: "the database system is starting up"}
	wrongPassword = &pgproto3.ErrorResponse{Severity: "FATAL", Code: "28P01", Message: `password authentication failed for user "costroid_owner"`}
	missingRole   = &pgproto3.ErrorResponse{Severity: "FATAL", Code: "28000", Message: `role "costroid_owner" does not exist`}
)

// postgresFront stands where serve's database is. It answers each connection
// with the error answer gives for that connection, counted from 0, as a
// server that is starting or that refuses a login does. Where answer gives
// none, it hands the connection to the Postgres at behind.
type postgresFront struct {
	listener net.Listener
	behind   string
	answer   func(connection int) *pgproto3.ErrorResponse
	mu       sync.Mutex
	taken    int
}

// listenFront answers on listener, or on a local port of its own when
// listener is nil.
func listenFront(t *testing.T, listener net.Listener, behind string, answer func(int) *pgproto3.ErrorResponse) *postgresFront {
	t.Helper()
	if listener == nil {
		var err error
		if listener, err = net.Listen("tcp", "127.0.0.1:0"); err != nil {
			t.Fatal(err)
		}
	}
	front := &postgresFront{listener: listener, behind: behind, answer: answer}
	t.Cleanup(func() { _ = listener.Close() })
	go front.accept()
	return front
}

func (f *postgresFront) address() string {
	return f.listener.Addr().String()
}

func (f *postgresFront) accept() {
	for {
		conn, err := f.listener.Accept()
		if err != nil {
			return
		}
		f.mu.Lock()
		connection := f.taken
		f.taken++
		f.mu.Unlock()
		go f.serve(conn, f.answer(connection))
	}
}

func (f *postgresFront) connections() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.taken
}

func (f *postgresFront) serve(conn net.Conn, refusal *pgproto3.ErrorResponse) {
	defer conn.Close()
	if refusal == nil {
		behind, err := net.Dial("tcp", f.behind)
		if err != nil {
			return
		}
		defer behind.Close()
		go func() { _, _ = io.Copy(behind, conn) }()
		_, _ = io.Copy(conn, behind)
		return
	}
	backend := pgproto3.NewBackend(conn, conn)
	for {
		message, err := backend.ReceiveStartupMessage()
		if err != nil {
			return
		}
		switch message.(type) {
		case *pgproto3.SSLRequest, *pgproto3.GSSEncRequest:
			if _, err := conn.Write([]byte("N")); err != nil {
				return
			}
			continue
		}
		break
	}
	backend.Send(refusal)
	_ = backend.Flush()
}

// always answers every connection with one refusal.
func always(refusal *pgproto3.ErrorResponse) func(int) *pgproto3.ErrorResponse {
	return func(int) *pgproto3.ErrorResponse { return refusal }
}

// heldPort is a local port bound and not listened on: the kernel refuses
// every connection to it, and no other process can take it until the test
// listens there or ends.
type heldPort struct {
	descriptor int
	addr       string
}

func holdPort(t *testing.T) *heldPort {
	t.Helper()
	descriptor, err := syscall.Socket(syscall.AF_INET, syscall.SOCK_STREAM, 0)
	if err != nil {
		t.Fatal(err)
	}
	held := &heldPort{descriptor: descriptor}
	t.Cleanup(func() {
		if held.descriptor >= 0 {
			_ = syscall.Close(held.descriptor)
		}
	})
	if err := syscall.Bind(descriptor, &syscall.SockaddrInet4{Addr: [4]byte{127, 0, 0, 1}}); err != nil {
		t.Fatal(err)
	}
	bound, err := syscall.Getsockname(descriptor)
	if err != nil {
		t.Fatal(err)
	}
	held.addr = net.JoinHostPort("127.0.0.1", strconv.Itoa(bound.(*syscall.SockaddrInet4).Port))
	return held
}

// listen starts listening on the held port, and hands the socket to the
// listener it returns.
func (h *heldPort) listen(t *testing.T) net.Listener {
	t.Helper()
	if err := syscall.Listen(h.descriptor, syscall.SOMAXCONN); err != nil {
		t.Fatal(err)
	}
	file := os.NewFile(uintptr(h.descriptor), h.addr)
	h.descriptor = -1
	defer file.Close()
	listener, err := net.FileListener(file)
	if err != nil {
		t.Fatal(err)
	}
	return listener
}

// paced has serve try every interval, for bound.
func paced(t *testing.T, interval, bound time.Duration) {
	t.Helper()
	previous := databasePace
	databasePace.interval, databasePace.bound = interval, bound
	t.Cleanup(func() { databasePace = previous })
}

// frontURL is role's connection string to database at addr, with the
// password no line may print.
func frontURL(role, addr, database string) string {
	return (&url.URL{Scheme: "postgres", User: url.UserPassword(role, frontPassword), Host: addr, Path: "/" + database, RawQuery: "sslmode=disable"}).String()
}

// atAddress moves a connection string to another host and port.
func atAddress(t *testing.T, connection, addr string) string {
	t.Helper()
	parsed, err := url.Parse(connection)
	if err != nil {
		t.Fatal(err)
	}
	parsed.Host = addr
	return parsed.String()
}

// serveEnvironment sets what serve reads, with the two connection strings.
func serveEnvironment(t *testing.T, owner, app string) {
	t.Helper()
	t.Setenv(envDatabase, app)
	t.Setenv(envMigration, owner)
	t.Setenv(envAudience, audience)
	t.Setenv(envPseudonym, base64.StdEncoding.EncodeToString(make([]byte, minPseudonymKeyBytes)))
	t.Setenv(envWebhook, "")
}

// startOnce runs serve until it exits, and returns its code, its stderr and
// how long it ran.
func startOnce(t *testing.T) (int, string, time.Duration) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	var stderr bytes.Buffer
	began := time.Now()
	code := Main(ctx, []string{"--addr", anyPort}, new(bytes.Buffer), &stderr)
	return code, stderr.String(), time.Since(began)
}

// waitEntry is the line serve logs for one try of a database that did not
// answer.
type waitEntry struct {
	Level    string `json:"level"`
	Msg      string `json:"msg"`
	Tried    string `json:"tried"`
	Answered string `json:"answered"`
	Try      int    `json:"try"`
	AgainIn  string `json:"again_in"`
	Bound    string `json:"bound"`
}

// waitLines are the lines serve logged for each try of a database that did
// not answer, with what each tried and what answered.
func waitLines(log string) []waitEntry {
	var lines []waitEntry
	for line := range strings.SplitSeq(log, "\n") {
		var entry waitEntry
		if json.Unmarshal([]byte(line), &entry) == nil && entry.Msg == waitLine {
			lines = append(lines, entry)
		}
	}
	return lines
}

// TestServeWaitsForADatabaseThatIsStarting starts serve before anything
// listens where its database is, then puts a Postgres there that says twice
// that it is starting before it takes a connection. serve logs a line for
// each try, naming the role, host and database it tried and what answered,
// never the password, then migrates, opens the ledger and answers its health
// probe.
func TestServeWaitsForADatabaseThatIsStarting(t *testing.T) {
	db := storetest.Empty(t)
	paced(t, 50*time.Millisecond, 20*time.Second)
	behind, err := url.Parse(db.OwnerURL)
	if err != nil {
		t.Fatal(err)
	}
	port := holdPort(t)
	addr := port.addr
	serveEnvironment(t, atAddress(t, db.OwnerURL, addr), atAddress(t, db.AppURL, addr))
	ctx, cancel := context.WithCancel(context.Background())
	log := &lockedBuffer{}
	done := make(chan int, 1)
	go func() { done <- Main(ctx, []string{"--addr", anyPort}, new(bytes.Buffer), log) }()
	t.Cleanup(cancel)
	for !strings.Contains(log.String(), "connection refused") {
		select {
		case code := <-done:
			t.Fatalf("serve exited %d before it tried a database that refuses:\n%s", code, log.String())
		case <-time.After(10 * time.Millisecond):
		}
	}
	listenFront(t, port.listen(t), behind.Host, func(connection int) *pgproto3.ErrorResponse {
		if connection < 2 {
			return starting
		}
		return nil
	})
	listening := listeningAddress(t, log, nil)
	response, err := http.Get("http://" + listening + "/healthz")
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	cancel()
	if code := <-done; code != 0 || response.StatusCode != http.StatusNoContent {
		t.Fatalf("serve answered its health probe %d and exited %d:\n%s", response.StatusCode, code, log.String())
	}
	tried := "costroid_owner@" + addr + "/" + db.Name
	var refused, startingUp int
	for _, line := range waitLines(log.String()) {
		if line.Tried != tried || line.Level != "WARN" {
			t.Errorf("a try names %s at %s, want %s at WARN", line.Tried, line.Level, tried)
		}
		switch {
		case strings.Contains(line.Answered, "connection refused"):
			refused++
		case strings.Contains(line.Answered, "the database system is starting up"):
			startingUp++
		default:
			t.Errorf("a try answered %q", line.Answered)
		}
	}
	password, _ := behind.User.Password()
	if refused == 0 || startingUp != 2 || strings.Contains(log.String(), password) {
		t.Errorf("%d tries refused and %d found the database starting, want some and 2, and the log holds the password: %t\n%s",
			refused, startingUp, strings.Contains(log.String(), password), log.String())
	}
	conn, err := pgx.Connect(context.Background(), db.SuperURL)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close(context.Background()) }()
	var migrated int
	if err := conn.QueryRow(context.Background(), `SELECT count(*) FROM schema_migrations`).Scan(&migrated); err != nil || migrated == 0 {
		t.Errorf("serve applied %d migrations after the wait: %v", migrated, err)
	}
}

// TestServeNeverWaitsOnAFaultThatWillNotHeal starts serve against a database
// that refuses its password, one that has no such role, and one whose
// migration fails. Each ends the start at its first try with the line it
// ended on before serve waited for anything, and serve logs no try.
func TestServeNeverWaitsOnAFaultThatWillNotHeal(t *testing.T) {
	paced(t, 5*time.Second, 20*time.Second)
	for name, refusal := range map[string]*pgproto3.ErrorResponse{"a wrong password": wrongPassword, "a missing role": missingRole} {
		t.Run(name, func(t *testing.T) {
			front := listenFront(t, nil, "", always(refusal))
			addr := front.address()
			serveEnvironment(t, frontURL("costroid_owner", addr, "costroid"), frontURL("costroid_app", addr, "costroid"))
			code, stderr, took := startOnce(t)
			want := "costroid: store: connect to migrate: failed to connect to `user=costroid_owner database=costroid`: " + addr + " (127.0.0.1): server error: " +
				refusal.Severity + ": " + refusal.Message + " (SQLSTATE " + refusal.Code + ")\nSee " + cli.Troubleshooting + ".\n"
			if code != cli.ExitCouldNot || stderr != want || front.connections() != 1 || took >= databasePace.interval {
				t.Errorf("exit %d after %d connections in %s, stderr:\n%s\nwant:\n%s", code, front.connections(), took, stderr, want)
			}
		})
	}
	t.Run("a migration that fails", func(t *testing.T) {
		db := storetest.Empty(t)
		conn, err := pgx.Connect(context.Background(), db.SuperURL)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = conn.Close(context.Background()) }()
		if _, err := conn.Exec(context.Background(), `CREATE FUNCTION costroid_tenant() RETURNS uuid LANGUAGE sql AS 'SELECT NULL::uuid'`); err != nil {
			t.Fatal(err)
		}
		serveEnvironment(t, db.OwnerURL, db.AppURL)
		code, stderr, took := startOnce(t)
		want := "costroid: store: apply 0001_ledger.sql: ERROR: function \"costroid_tenant\" already exists with same argument types (SQLSTATE 42723)\nSee " + cli.Troubleshooting + ".\n"
		if code != cli.ExitCouldNot || stderr != want || took >= databasePace.interval {
			t.Errorf("exit %d in %s, stderr:\n%s\nwant:\n%s", code, took, stderr, want)
		}
	})
}

// TestServeStopsWaitingAtItsBound starts serve against a database that only
// ever says it is starting, and against an address that only ever refuses.
// serve tries again at each interval, logs each try, and at its bound exits
// 125 on the last try's error, as it did before it waited for anything.
func TestServeStopsWaitingAtItsBound(t *testing.T) {
	interval, bound := 20*time.Millisecond, 400*time.Millisecond
	paced(t, interval, bound)
	for name, tc := range map[string]struct {
		front    bool
		answered string
	}{
		"a database that is starting": {front: true, answered: starting.Message + " (SQLSTATE 57P03)"},
		"an address that refuses":     {answered: "connect: connection refused"},
	} {
		t.Run(name, func(t *testing.T) {
			var front *postgresFront
			var addr string
			if tc.front {
				front = listenFront(t, nil, "", always(starting))
				addr = front.address()
			} else {
				addr = holdPort(t).addr
			}
			serveEnvironment(t, frontURL("costroid_owner", addr, "costroid"), frontURL("costroid_app", addr, "costroid"))
			code, stderr, took := startOnce(t)
			lines := waitLines(stderr)
			last := stderr[strings.LastIndex(strings.TrimSuffix(stderr, "\nSee "+cli.Troubleshooting+".\n"), "\n")+1:]
			if code != cli.ExitCouldNot || !strings.HasPrefix(last, "costroid: store: connect to migrate: ") || !strings.Contains(last, tc.answered) ||
				!strings.HasSuffix(stderr, "\nSee "+cli.Troubleshooting+".\n") {
				t.Errorf("exit %d, and the last line is not the last try's error, %q:\n%s", code, tc.answered, stderr)
			}
			if took < bound-interval || took > bound+time.Second || len(lines) < int(bound/interval)/4 || len(lines) > int(bound/interval) {
				t.Errorf("serve stopped after %s and %d tries, with a bound of %s", took, len(lines), bound)
			}
			if front != nil && front.connections() != len(lines)+1 {
				t.Errorf("serve made %d connections and logged %d tries", front.connections(), len(lines))
			}
			for i, line := range lines {
				if line.Try != i+1 || line.Bound != bound.String() || line.AgainIn != interval.String() || !strings.Contains(line.Answered, tc.answered) {
					t.Errorf("try %d logged %+v", i+1, line)
				}
			}
			if strings.Contains(stderr, frontPassword) {
				t.Errorf("stderr holds the password:\n%s", stderr)
			}
		})
	}
}

// TestServeStatesItsWaitWhereItIsRun holds docs/serve.md to the interval and
// the bound serve waits for its database with.
func TestServeStatesItsWaitWhereItIsRun(t *testing.T) {
	page, err := os.ReadFile(filepath.Join("..", "..", "docs", "serve.md"))
	if err != nil {
		t.Fatal(err)
	}
	want := fmt.Sprintf("It tries every %d seconds for up to %d seconds", databasePace.interval/time.Second, databasePace.bound/time.Second)
	if !strings.Contains(string(page), want) {
		t.Errorf("docs/serve.md does not say %q", want)
	}
}
