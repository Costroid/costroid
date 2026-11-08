// Package egress is the one way out of costroid serve. Every outbound HTTP
// client the service builds comes from a Gate, which holds the deployment's
// allowlist of exact host names and refuses everything else, before a socket
// opens. The list is empty by default, and an empty list refuses every call.
//
// The Gate checks twice: once per request, which a redirect passes through
// again, and once per dial. It holds against a mistake in this program, not
// against code running inside it. A firewall or a network policy holds against
// both, which docs/ledger.md says.
package egress

import (
	"context"
	"fmt"
	"io"
	"maps"
	"net"
	"net/http"
	"regexp"
	"slices"
	"strings"
	"time"
)

const (
	// Flag is what an operator passes to add a host to the list.
	Flag = "--egress-allow"

	maxHostBytes = 253
	// unnamed stands for a destination that is not a host name, so that a
	// report and a refusal never repeat one. It can never be on the list.
	unnamed = "(invalid)"

	dialTimeout      = 10 * time.Second
	handshakeTimeout = 10 * time.Second
	idleTimeout      = 90 * time.Second
	maxIdleConns     = 8
)

// The outcomes of one attempted call. A redirect's hops count one each.
const (
	// Completed means the host answered, whatever the answer was.
	Completed = "completed"
	// Failed means the list allowed the call and the transport gave no answer.
	Failed = "failed"
	// Refused means the list stopped the call and no socket opened.
	Refused = "refused"
)

var hostName = regexp.MustCompile(`^[a-z0-9]([a-z0-9_-]*[a-z0-9])?(\.[a-z0-9]([a-z0-9_-]*[a-z0-9])?)*$`)

// RefusedError is a call the list stopped.
type RefusedError struct {
	Host string
}

func (e *RefusedError) Error() string {
	return fmt.Sprintf("egress: %s is not on the allowlist: add it with %s", e.Host, Flag)
}

// Recorder counts one attempted call. It must not block on anything slower
// than a lock, because a call waits for it.
type Recorder interface {
	Record(host, outcome string)
}

// Gate is one deployment's allowlist.
type Gate struct {
	allowed   map[string]bool
	recorder  Recorder
	dialer    net.Dialer
	transport http.RoundTripper
}

// ParseList reads the flag's values and the variable's comma-separated value
// into one list. The two add up, and an entry that is not an exact host name
// is an error, so a wildcard never starts a service that would ignore it.
func ParseList(values []string, joined string) ([]string, error) {
	entries := slices.Clone(values)
	for _, entry := range strings.Split(joined, ",") {
		if trimmed := strings.TrimSpace(entry); trimmed != "" {
			entries = append(entries, trimmed)
		}
	}
	hosts := make([]string, 0, len(entries))
	for _, entry := range entries {
		host := named(entry)
		if host == unnamed {
			return nil, fmt.Errorf("egress allowlist entry %q is not a host name: list exact hosts, with no scheme, port, path or wildcard", entry)
		}
		hosts = append(hosts, host)
	}
	return hosts, nil
}

// named normalises case and one trailing dot, and answers unnamed for anything
// that is neither a host name nor an IP address.
func named(host string) string {
	host = strings.TrimSuffix(strings.ToLower(host), ".")
	if len(host) > maxHostBytes || (!hostName.MatchString(host) && net.ParseIP(host) == nil) {
		return unnamed
	}
	return host
}

// New returns the Gate for a list ParseList read.
func New(hosts []string, recorder Recorder) *Gate {
	g := &Gate{allowed: make(map[string]bool, len(hosts)), recorder: recorder, dialer: net.Dialer{Timeout: dialTimeout}}
	for _, host := range hosts {
		g.allowed[named(host)] = true
	}
	delete(g.allowed, unnamed)
	g.transport = &http.Transport{
		DialContext:         g.DialContext,
		ForceAttemptHTTP2:   true,
		TLSHandshakeTimeout: handshakeTimeout,
		IdleConnTimeout:     idleTimeout,
		MaxIdleConns:        maxIdleConns,
	}
	return g
}

// Client returns an HTTP client bound by the list. It reads no proxy from the
// environment, because a proxy would make the dial check see the proxy's name.
func (g *Gate) Client(timeout time.Duration) *http.Client {
	return &http.Client{Transport: g, Timeout: timeout}
}

// Hosts returns the list in order.
func (g *Gate) Hosts() []string {
	return slices.Sorted(maps.Keys(g.allowed))
}

// Allows reports whether the list holds a host. It counts nothing.
func (g *Gate) Allows(host string) bool {
	return g.allowed[named(host)]
}

func (g *Gate) check(raw string) (string, error) {
	host := named(raw)
	if !g.allowed[host] {
		g.recorder.Record(host, Refused)
		return "", &RefusedError{Host: host}
	}
	return host, nil
}

// RoundTrip checks a request's host, then sends it. A client calls this for
// each hop of a redirect.
func (g *Gate) RoundTrip(request *http.Request) (*http.Response, error) {
	host, err := g.check(request.URL.Hostname())
	if err != nil {
		closeBody(request.Body)
		return nil, err
	}
	response, err := g.transport.RoundTrip(request)
	if err != nil {
		g.recorder.Record(host, Failed)
		return nil, err
	}
	g.recorder.Record(host, Completed)
	return response, nil
}

func closeBody(body io.Closer) {
	if body != nil {
		_ = body.Close()
	}
}

// DialContext checks an address's host, then dials it.
func (g *Gate) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return nil, fmt.Errorf("egress: dial: %w", err)
	}
	if _, err := g.check(host); err != nil {
		return nil, err
	}
	return g.dialer.DialContext(ctx, network, address)
}
