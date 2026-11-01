// Package storetest runs Postgres in Docker for the packages that need it.
package storetest

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"flag"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/Costroid/costroid/internal/store"
)

// Image is the Postgres the suite runs on.
const Image = "postgres:17-alpine"

const (
	// OwnerRole owns the schema, as the migration role does in production.
	OwnerRole = "costroid_owner"
	// AppRole is the role the service runs as.
	AppRole = "costroid_app"

	startTimeout = 60 * time.Second
)

var (
	address  string
	password string
	sequence atomic.Int64
)

// Main runs a test binary around one Postgres container. In short mode it
// starts nothing, and every test that asks for a database skips. In full mode
// a failure to start Postgres fails the run: a silent skip would be a false
// green.
func Main(m *testing.M) int {
	flag.Parse()
	if testing.Short() {
		return m.Run()
	}
	stop, err := start()
	if err != nil {
		fmt.Fprintf(os.Stderr, "storetest: %v\n", err)
		return 1
	}
	defer stop()
	return m.Run()
}

func start() (func(), error) {
	secret := make([]byte, 16)
	if _, err := rand.Read(secret); err != nil {
		return nil, err
	}
	password = hex.EncodeToString(secret)
	out, err := output("docker", "run", "-d", "--rm", "--label", "costroid-test=1",
		"-e", "POSTGRES_PASSWORD="+password, "-p", "127.0.0.1::5432", Image,
		"-c", "fsync=off", "-c", "max_connections=200", "-c", "shared_buffers=256MB")
	if err != nil {
		return nil, fmt.Errorf("start postgres: %w", err)
	}
	id := strings.TrimSpace(out)
	stop := func() { _, _ = output("docker", "rm", "-f", id) }
	mapped, err := output("docker", "port", id, "5432/tcp")
	if err != nil {
		stop()
		return nil, fmt.Errorf("read the port: %w", err)
	}
	address = strings.TrimSpace(strings.Split(mapped, "\n")[0])
	if err := prepare(); err != nil {
		stop()
		return nil, err
	}
	return stop, nil
}

func output(name string, args ...string) (string, error) {
	var stdout, stderr bytes.Buffer
	cmd := exec.Command(name, args...)
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("%s: %w: %s", name, err, stderr.String())
	}
	return stdout.String(), nil
}

// SuperURL is the bootstrap superuser, for setup and for looking past RLS.
func SuperURL(database string) string {
	return roleURL("postgres", database)
}

func roleURL(role, database string) string {
	return (&url.URL{
		Scheme: "postgres", User: url.UserPassword(role, password), Host: address, Path: "/" + database,
		RawQuery: "sslmode=disable",
	}).String()
}

func prepare() error {
	ctx, cancel := context.WithTimeout(context.Background(), startTimeout)
	defer cancel()
	conn, err := waitForPostgres(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = conn.Close(context.Background()) }()
	for _, role := range []string{OwnerRole, AppRole} {
		statement := fmt.Sprintf(`CREATE ROLE %s LOGIN PASSWORD '%s' NOSUPERUSER NOBYPASSRLS NOCREATEROLE NOCREATEDB`, role, password)
		if _, err := conn.Exec(ctx, statement); err != nil {
			return fmt.Errorf("create %s: %w", role, err)
		}
	}
	return nil
}

// waitForPostgres waits out the image's init phase, which runs one server,
// stops it and starts the real one.
func waitForPostgres(ctx context.Context) (*pgx.Conn, error) {
	var last error
	stable := 0
	for {
		if conn, err := pgx.Connect(ctx, SuperURL("postgres")); err == nil {
			if stable++; stable == 3 {
				return conn, nil
			}
			_ = conn.Close(ctx)
		} else {
			last, stable = err, 0
		}
		select {
		case <-ctx.Done():
			return nil, fmt.Errorf("postgres did not start: %w", last)
		case <-time.After(200 * time.Millisecond):
		}
	}
}

// Database is one test's own migrated database.
type Database struct {
	Name     string
	OwnerURL string
	AppURL   string
	SuperURL string
}

// New creates and migrates a database, or skips the test in short mode.
func New(t testing.TB) Database {
	t.Helper()
	db := Empty(t)
	if err := store.Migrate(context.Background(), db.OwnerURL); err != nil {
		t.Fatal(err)
	}
	return db
}

// Empty creates a database with no schema, or skips the test in short mode.
func Empty(t testing.TB) Database {
	t.Helper()
	if testing.Short() {
		t.Skip("needs Postgres, which short mode does not start")
	}
	name := fmt.Sprintf("test_%d_%d", os.Getpid(), sequence.Add(1))
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, SuperURL("postgres"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close(ctx) }()
	if _, err := conn.Exec(ctx, fmt.Sprintf(`CREATE DATABASE %s OWNER %s`, name, OwnerRole)); err != nil {
		t.Fatal(err)
	}
	return Database{
		Name:     name,
		OwnerURL: roleURL(OwnerRole, name),
		AppURL:   roleURL(AppRole, name),
		SuperURL: SuperURL(name),
	}
}

// Tenant provisions a tenant with the given binding and ingest key digests.
func (d Database) Tenant(t testing.TB, name string, bindings []store.Binding, keys ...string) store.TenantID {
	t.Helper()
	id, err := store.NewTenantID()
	if err != nil {
		t.Fatal(err)
	}
	tenant := store.Tenant{ID: id, Name: name, Bindings: bindings, KeySHA256: keys}
	if err := store.Provision(context.Background(), d.OwnerURL, tenant); err != nil {
		t.Fatal(err)
	}
	return id
}
