package egress

import (
	"errors"
	"net"
	"net/url"
	"slices"
	"strings"
	"testing"
)

// FuzzParseList reads the operator's allowlist from the flag and the
// variable. Every entry kept is an exact host name or an IP address in its
// one written form, and a Gate built from the list allows those and nothing
// else.
func FuzzParseList(f *testing.F) {
	f.Add("api.anthropic.com", "api.openai.com, Example.COM.")
	f.Add("*.example.com", "")
	f.Add("https://example.com:443/x", "")
	f.Add("::1", "10.0.0.1,fe80::1%eth0")
	f.Add("", ",,  ,")
	f.Fuzz(func(t *testing.T, value, joined string) {
		hosts, err := ParseList([]string{value}, joined)
		if err != nil {
			return
		}
		for _, host := range hosts {
			if host == unnamed || host != strings.ToLower(host) || strings.HasSuffix(host, ".") || len(host) > maxHostBytes ||
				strings.ContainsAny(host, "*/@ \t,") || (!hostName.MatchString(host) && net.ParseIP(host) == nil) {
				t.Fatalf("the list kept %q", host)
			}
		}
		gate := New(hosts, &tally{})
		if listed := gate.Hosts(); len(listed) > len(hosts) || slices.ContainsFunc(listed, func(host string) bool { return !slices.Contains(hosts, host) }) {
			t.Fatalf("the gate holds %q from %q", listed, hosts)
		}
	})
}

// FuzzGateCheck asks a Gate of one host about any address. A request and a
// dial to the same address get the same answer, the answer is yes only for
// the listed host in another case or with one trailing dot, and a refusal
// never repeats what was asked when it is not a host name.
func FuzzGateCheck(f *testing.F) {
	f.Add("api.anthropic.com", "https://api.anthropic.com/v1/messages")
	f.Add("api.anthropic.com", "https://API.anthropic.com.:443/")
	f.Add("api.anthropic.com", "https://api.anthropic.com.evil.example/")
	f.Add("api.anthropic.com", "https://api.anthropic.com@evil.example/")
	f.Add("127.0.0.1", "http://[::ffff:127.0.0.1]:80/")
	f.Add("10.0.0.1", "http://10.0.0.1%2f/")
	f.Fuzz(func(t *testing.T, allowed, raw string) {
		hosts, err := ParseList([]string{allowed}, "")
		if err != nil {
			return
		}
		parsed, err := url.Parse(raw)
		if err != nil {
			return
		}
		gate := New(hosts, &tally{})
		host, requestErr := gate.check(parsed.Hostname())
		dialed, _, dialErr := net.SplitHostPort(net.JoinHostPort(parsed.Hostname(), "443"))
		if dialErr == nil {
			_, dialErr = gate.check(dialed)
		}
		if (requestErr == nil) != (dialErr == nil) {
			t.Fatalf("%q: the request reads %v and the dial %v", raw, requestErr, dialErr)
		}
		if requestErr == nil && (host != hosts[0] || host != strings.TrimSuffix(strings.ToLower(parsed.Hostname()), ".")) {
			t.Fatalf("%q was let through as %q under the list %q", raw, host, hosts)
		}
		var refused *RefusedError
		if errors.As(requestErr, &refused) && refused.Host != unnamed && !hostName.MatchString(refused.Host) && net.ParseIP(refused.Host) == nil {
			t.Fatalf("the refusal repeats %q", refused.Host)
		}
	})
}
