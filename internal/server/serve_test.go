package server

import (
	"bytes"
	"context"
	"encoding/base64"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

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
			if code := Main(context.Background(), nil, &stderr); code != exitFailure {
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
	if code := Main(context.Background(), []string{"--bind", "acme"}, &stderr); code != exitFailure {
		t.Fatalf("exit %d", code)
	}
}

func freeAddress(t *testing.T) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = listener.Close() }()
	return listener.Addr().String()
}

func serveOnce(t *testing.T, addr string) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	exit := make(chan int, 1)
	var stderr bytes.Buffer
	go func() { exit <- Main(ctx, []string{"--addr", addr, "--bind", "github.com:acme"}, &stderr) }()
	deadline := time.Now().Add(20 * time.Second)
	for {
		response, err := http.Get("http://" + addr + "/healthz")
		if err == nil {
			_ = response.Body.Close()
			if response.StatusCode != http.StatusNoContent {
				t.Fatalf("health status %d", response.StatusCode)
			}
			break
		}
		select {
		case code := <-exit:
			t.Fatalf("exited %d: %s", code, stderr.String())
		default:
		}
		if time.Now().After(deadline) {
			t.Fatalf("not healthy: %v", err)
		}
		time.Sleep(50 * time.Millisecond)
	}
	cancel()
	if code := <-exit; code != 0 {
		t.Fatalf("exit %d", code)
	}
}

func TestPostgresServeMigratesBindsAndRestartsCleanly(t *testing.T) {
	db := storetest.Empty(t)
	t.Setenv(envDatabase, db.AppURL)
	t.Setenv(envMigration, db.OwnerURL)
	t.Setenv(envAudience, audience)
	t.Setenv(envPseudonym, base64.StdEncoding.EncodeToString(make([]byte, minPseudonymKeyBytes)))
	addr := freeAddress(t)
	serveOnce(t, addr)
	serveOnce(t, addr)
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
