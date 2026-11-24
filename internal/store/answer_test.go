package store_test

import (
	"errors"
	"fmt"
	"net"
	"os"
	"syscall"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/Costroid/costroid/internal/store"
)

// TestNotAnsweringIsANameARefusalOrAServerThatIsStarting reads errors shaped
// as pgx wraps them. A name that does not resolve, a refused connection and a
// server that is starting are Postgres not answering yet, alone or beside the
// TLS refusal that sslmode=prefer meets first. A server that refuses the login
// is a fault even beside one that is starting, and so is anything else.
func TestNotAnsweringIsANameARefusalOrAServerThatIsStarting(t *testing.T) {
	refused := &net.OpError{Op: "dial", Net: "tcp", Err: os.NewSyscallError("connect", syscall.ECONNREFUSED)}
	startingUp := &pgconn.PgError{Severity: "FATAL", Code: "57P03", Message: "the database system is starting up"}
	noTLS := errors.New("server refused TLS connection")
	for name, tc := range map[string]struct {
		err  error
		want bool
	}{
		"no such host":                              {fmt.Errorf("hostname resolving error: %w", errors.Join(&net.DNSError{Err: "no such host", Name: "postgres", IsNotFound: true})), true},
		"a resolver's own failure":                  {fmt.Errorf("hostname resolving error: %w", errors.Join(&net.DNSError{Err: "server misbehaving", Name: "postgres"})), true},
		"a refused connection":                      {fmt.Errorf("store: connect to migrate: %w", errors.Join(refused)), true},
		"a server starting":                         {fmt.Errorf("store: read the application role: %w", errors.Join(noTLS, startingUp)), true},
		"a wrong password beside a server starting": {errors.Join(startingUp, &pgconn.PgError{Code: "28P01"}), false},
		"a missing role":                            {errors.Join(noTLS, &pgconn.PgError{Code: "28000"}), false},
		"a migration that fails":                    {fmt.Errorf("store: apply 0001_ledger.sql: %w", &pgconn.PgError{Code: "42723"}), false},
		"a TLS refusal alone":                       {errors.Join(noTLS), false},
		"a reset connection":                        {&net.OpError{Op: "read", Net: "tcp", Err: os.NewSyscallError("read", syscall.ECONNRESET)}, false},
		"an unsafe role":                            {store.ErrUnsafeRole, false},
		"no error":                                  {nil, false},
	} {
		if got := store.NotAnswering(tc.err); got != tc.want {
			t.Errorf("%s: NotAnswering(%v) = %t", name, tc.err, got)
		}
	}
}

// TestAddressNeverNamesThePassword names the role, host, port and database of
// a connection string, and leaves its password out.
func TestAddressNeverNamesThePassword(t *testing.T) {
	if got := store.Address("postgres://costroid_owner:s3cret-password@postgres:5433/costroid"); got != "costroid_owner@postgres:5433/costroid" {
		t.Errorf("Address = %q", got)
	}
}
