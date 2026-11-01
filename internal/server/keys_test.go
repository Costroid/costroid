package server

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"regexp"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/Costroid/costroid/internal/focus"
	"github.com/Costroid/costroid/internal/store"
	"github.com/Costroid/costroid/internal/store/storetest"
)

var mintedKey = regexp.MustCompile(`^costroid_ik_[A-Za-z0-9_-]{43}\n$`)

func keysCommandLine(t *testing.T, args ...string) (int, string, string) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	code := KeysMain(context.Background(), args, &stdout, &stderr)
	return code, stdout.String(), stderr.String()
}

func createdKey(t *testing.T, w *ledgerWorld) (key, id string) {
	t.Helper()
	t.Setenv(envMigration, w.db.OwnerURL)
	code, stdout, stderr := keysCommandLine(t, "create", "--owner", "github.com:acme")
	if code != 0 {
		t.Fatalf("exit %d: %s", code, stderr)
	}
	if !mintedKey.MatchString(stdout) {
		t.Fatalf("stdout %q is not one key", stdout)
	}
	key = strings.TrimSpace(stdout)
	if strings.Contains(stderr, key) {
		t.Fatal("the key reached stderr")
	}
	if !strings.Contains(stderr, store.KeyDigest(key)) {
		t.Fatalf("stderr %q does not name the id", stderr)
	}
	return key, store.KeyDigest(key)
}

func dumpKeys(t *testing.T, db storetest.Database) string {
	t.Helper()
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, db.SuperURL)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close(ctx) }()
	rows, err := conn.Query(ctx, `SELECT coalesce(json_agg(k), '[]')::text FROM ingest_keys k`)
	if err != nil {
		t.Fatal(err)
	}
	dump, err := pgx.CollectOneRow(rows, pgx.RowTo[string])
	if err != nil {
		t.Fatal(err)
	}
	return dump
}

func TestACreatedKeyIsPrintedOnceAndStoredOnlyAsItsHash(t *testing.T) {
	w := postgresWorld(t)
	key, id := createdKey(t, w)
	dump := dumpKeys(t, w.db)
	if strings.Contains(dump, key) || strings.Contains(dump, strings.TrimPrefix(key, store.KeyPrefix)) {
		t.Fatal("the key is stored in the clear")
	}
	if !strings.Contains(dump, id) {
		t.Fatalf("no digest stored: %s", dump)
	}
}

func TestACreatedKeyWritesToItsOwnersTenantAsUnverifiedRows(t *testing.T) {
	w := postgresWorld(t)
	key, _ := createdKey(t, w)
	w.rows(key, lines(t, row("req-1", focus.Output, 100))).want(t, http.StatusOK)
	view := w.view(t, key, acmeRepo, acmePull)
	if view.Budget.Evidence {
		t.Fatalf("a key's row reached a budget: %+v", view.Budget)
	}
	beta := w.view(t, betaKey, acmeRepo, acmePull)
	if len(beta.Lines) != 0 {
		t.Fatalf("another tenant reads the row: %+v", beta)
	}
}

func TestARevokedKeyStopsAtTheNextRequest(t *testing.T) {
	w := postgresWorld(t)
	key, id := createdKey(t, w)
	body := lines(t, row("req-1", focus.Output, 100))
	w.rows(key, body).want(t, http.StatusOK)
	for range 2 {
		if code, _, stderr := keysCommandLine(t, "revoke", id); code != 0 {
			t.Fatalf("exit %d: %s", code, stderr)
		}
	}
	if got := w.rows(key, body).want(t, http.StatusUnauthorized); got.code(t) != codeAuth {
		t.Fatalf("code %s", got.code(t))
	}
}

func TestRevokingOneKeyLeavesTheOthers(t *testing.T) {
	w := postgresWorld(t)
	_, id := createdKey(t, w)
	if code, _, stderr := keysCommandLine(t, "revoke", id); code != 0 {
		t.Fatalf("exit %d: %s", code, stderr)
	}
	w.rows(acmeKey, lines(t, row("req-1", focus.Output, 100))).want(t, http.StatusOK)
}

func TestARevocationKeepsItsFirstStamp(t *testing.T) {
	w := postgresWorld(t)
	_, id := createdKey(t, w)
	if code, _, stderr := keysCommandLine(t, "revoke", id); code != 0 {
		t.Fatalf("exit %d: %s", code, stderr)
	}
	first := dumpKeys(t, w.db)
	if code, _, stderr := keysCommandLine(t, "revoke", id); code != 0 {
		t.Fatalf("exit %d: %s", code, stderr)
	}
	if second := dumpKeys(t, w.db); second != first {
		t.Fatalf("%s became %s", first, second)
	}
}

func TestKeysRefuseWhatTheyCannotAct(t *testing.T) {
	w := postgresWorld(t)
	t.Setenv(envMigration, w.db.OwnerURL)
	unknown := strings.Repeat("c", 64)
	for name, args := range map[string][]string{
		"no subcommand":  {},
		"unknown":        {"rotate"},
		"no owner":       {"create"},
		"malformed":      {"create", "--owner", "acme"},
		"unbound owner":  {"create", "--owner", "github.com:nobody"},
		"no id":          {"revoke"},
		"unknown id":     {"revoke", unknown},
		"a pasted key":   {"revoke", "costroid_ik_pasted-by-mistake"},
		"two ids":        {"revoke", unknown, unknown},
		"an upper id":    {"revoke", strings.Repeat("C", 64)},
		"flag-less item": {"create", "github.com:acme"},
	} {
		t.Run(name, func(t *testing.T) {
			code, stdout, stderr := keysCommandLine(t, args...)
			if code != exitFailure {
				t.Fatalf("exit %d", code)
			}
			if stdout != "" {
				t.Fatalf("stdout %q", stdout)
			}
			if strings.Contains(stderr, "pasted-by-mistake") {
				t.Fatalf("stderr repeats the argument: %s", stderr)
			}
		})
	}
	var keys []json.RawMessage
	if err := json.Unmarshal([]byte(dumpKeys(t, w.db)), &keys); err != nil {
		t.Fatal(err)
	}
	if len(keys) != 2 {
		t.Fatalf("%d keys after only refusals, want the two fixtures", len(keys))
	}
}

func TestKeysNeedTheOwnerConnection(t *testing.T) {
	t.Setenv(envMigration, "")
	if code, _, stderr := keysCommandLine(t, "create", "--owner", "github.com:acme"); code != exitFailure || !strings.Contains(stderr, envMigration) {
		t.Fatalf("exit %d: %s", code, stderr)
	}
}
