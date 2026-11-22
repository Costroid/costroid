package server

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/Costroid/costroid/internal/cli"
	"github.com/Costroid/costroid/internal/store/storetest"
)

func TestServeRefusesAMissingOrShortConfiguration(t *testing.T) {
	short := base64.StdEncoding.EncodeToString([]byte("short"))
	long := base64.StdEncoding.EncodeToString(make([]byte, minPseudonymKeyBytes))
	cases := []struct {
		name                    string
		database, audience, key string
		want                    string
	}{
		{"no database", "", "aud", long, envDatabase},
		{"no audience", "postgres://x", "", long, envAudience},
		{"no key", "postgres://x", "aud", "", envPseudonym},
		{"short key", "postgres://x", "aud", short, "under 32"},
		{"key not base64", "postgres://x", "aud", "!!", "not base64"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv(envDatabase, tc.database)
			t.Setenv(envAudience, tc.audience)
			t.Setenv(envPseudonym, tc.key)
			var stderr bytes.Buffer
			if code := Main(context.Background(), nil, new(bytes.Buffer), &stderr); code != cli.ExitCouldNot {
				t.Fatalf("exit %d", code)
			}
			if !strings.Contains(stderr.String(), tc.want) {
				t.Fatalf("stderr %q", stderr.String())
			}
		})
	}
}

func TestServeRefusesABindThatIsNotHostAndOwner(t *testing.T) {
	var stderr bytes.Buffer
	if code := Main(context.Background(), []string{"--bind", "acme"}, new(bytes.Buffer), &stderr); code != cli.ExitUsage {
		t.Fatalf("exit %d", code)
	}
}

// anyPort has serve bind a port the kernel picks. serve holds it from the bind
// until it exits and names it in its "listening" line, so no other process can
// take it between a test's choice and serve's bind.
const anyPort = "127.0.0.1:0"

// serveStartWait bounds how long serve may take to migrate and listen.
const serveStartWait = 20 * time.Second

// startServe runs serve on addr with args and waits until serve names the
// address it holds and a health probe there answers 204. It returns that
// address, the log, and stop, which ends serve and fails the test unless serve
// exits 0. Cleanup calls stop when the test has not. Only the address serve
// names is probed, never one it was handed and may not hold.
func startServe(t *testing.T, addr string, args ...string) (string, *lockedBuffer, func()) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	log := &lockedBuffer{}
	done := make(chan struct{})
	var code int
	go func() {
		defer close(done)
		code = Main(ctx, append([]string{"--addr", addr}, args...), new(bytes.Buffer), log)
	}()
	stop := sync.OnceFunc(func() {
		cancel()
		<-done
		if code != 0 {
			t.Errorf("serve exited %d: %s", code, log.String())
		}
	})
	t.Cleanup(stop)
	held := listeningAddress(t, log, done)
	response, err := http.Get("http://" + held + "/healthz")
	if err != nil {
		t.Fatalf("serve listens on %s and does not answer: %v", held, err)
	}
	_ = response.Body.Close()
	if response.StatusCode != http.StatusNoContent {
		t.Fatalf("serve's health on %s answered %d:\n%s", held, response.StatusCode, log.String())
	}
	return held, log, stop
}

// listeningAddress waits for serve's "listening" line and returns the address
// it names: the one serve bound, and holds until it exits.
func listeningAddress(t *testing.T, log *lockedBuffer, done <-chan struct{}) string {
	t.Helper()
	deadline := time.After(serveStartWait)
	for {
		for _, line := range strings.SplitAfter(log.String(), "\n") {
			var entry struct {
				Msg  string `json:"msg"`
				Addr string `json:"addr"`
			}
			if strings.HasSuffix(line, "\n") && json.Unmarshal([]byte(line), &entry) == nil && entry.Msg == "listening" {
				return entry.Addr
			}
		}
		select {
		case <-done:
			t.Fatalf("serve exited before it listened: %s", log.String())
		case <-deadline:
			t.Fatalf("serve did not listen within %s: %s", serveStartWait, log.String())
		case <-time.After(10 * time.Millisecond):
		}
	}
}

func TestPostgresServeMigratesBindsAndRestartsCleanly(t *testing.T) {
	db := storetest.Empty(t)
	t.Setenv(envDatabase, db.AppURL)
	t.Setenv(envMigration, db.OwnerURL)
	t.Setenv(envAudience, audience)
	t.Setenv(envPseudonym, base64.StdEncoding.EncodeToString(make([]byte, minPseudonymKeyBytes)))
	t.Setenv(envWebhook, "")
	// Serve twice on one database: the second start finds the schema migrated
	// and the bind in place.
	_, _, stop := startServe(t, anyPort, "--bind", "github.com:acme")
	stop()
	_, log, stop := startServe(t, anyPort, "--bind", "github.com:acme")
	stop()
	if count := strings.Count(log.String(), `"msg":"webhook route not served"`); count != 1 {
		t.Errorf("serve said %d times that the webhook route is not served:\n%s", count, log.String())
	}
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, db.SuperURL)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close(ctx) }()
	var tenants, bindings int
	err = conn.QueryRow(ctx, `SELECT (SELECT count(*) FROM tenants), (SELECT count(*) FROM tenant_bindings)`).Scan(&tenants, &bindings)
	if err != nil {
		t.Fatal(err)
	}
	if tenants != 1 || bindings != 1 {
		t.Fatalf("%d tenants, %d bindings after two starts", tenants, bindings)
	}
}

// TestServeNeverProbesAnAddressItDoesNotHold takes the address a test hands
// serve, between that choice and serve's bind, with a server that fails on any
// request, as a stub in a test of another package can. serve still binds a port
// of its own and is probed there, and the server that took the address is
// asked nothing.
func TestServeNeverProbesAnAddressItDoesNotHold(t *testing.T) {
	db := storetest.Empty(t)
	t.Setenv(envDatabase, db.AppURL)
	t.Setenv(envMigration, db.OwnerURL)
	t.Setenv(envAudience, audience)
	t.Setenv(envPseudonym, base64.StdEncoding.EncodeToString(make([]byte, minPseudonymKeyBytes)))
	addr := anyPort
	listener, err := net.Listen("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	taker := &httptest.Server{Listener: listener, Config: &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("the server that took the address was asked %s %s", r.Method, r.URL.Path)
		w.WriteHeader(http.StatusTeapot)
	})}}
	taker.Start()
	_, _, stop := startServe(t, addr)
	stop()
	taker.Close()
}
