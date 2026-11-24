package store

import (
	"fmt"
	"net"
	"syscall"

	"github.com/jackc/pgx/v5/pgconn"
)

// cannotConnectNow is the SQLSTATE of a server that is starting, stopping or
// recovering, and takes no connection until it is done.
const cannotConnectNow = "57P03"

// NotAnswering reports whether err is only Postgres not answering yet: its
// name does not resolve, it refuses the connection, or it says it is
// starting. A server that answers anything else, a password or a role it
// refuses among them, is a fault that waiting does not heal, and so is an
// error that holds none of the three.
func NotAnswering(err error) bool {
	starting := false
	for _, node := range chain(err) {
		switch node := node.(type) {
		case *pgconn.PgError:
			if node.Code != cannotConnectNow {
				return false
			}
			starting = true
		case *net.DNSError:
			starting = true
		case syscall.Errno:
			starting = starting || node == syscall.ECONNREFUSED
		}
	}
	return starting
}

// chain is err and every error it wraps, through both forms of Unwrap.
func chain(err error) []error {
	if err == nil {
		return nil
	}
	nodes := []error{err}
	switch wrapper := err.(type) {
	case interface{ Unwrap() error }:
		nodes = append(nodes, chain(wrapper.Unwrap())...)
	case interface{ Unwrap() []error }:
		for _, inner := range wrapper.Unwrap() {
			nodes = append(nodes, chain(inner)...)
		}
	}
	return nodes
}

// Address names the role, the host and the database a connection string
// connects to, and never its password.
func Address(url string) string {
	config, err := pgconn.ParseConfig(url)
	if err != nil {
		return "a connection string that does not parse"
	}
	return fmt.Sprintf("%s@%s:%d/%s", config.User, config.Host, config.Port, config.Database)
}
