package main

import (
	"io"
	"net/http"
	"sync"

	"github.com/Costroid/costroid/internal/cli"
)

// asking is client with one line said on stderr before its first request to
// each host, so that a command which then waits on that host has said what it
// waits on. It says nothing while it waits, and --quiet drops the line.
func asking(client *http.Client, stderr io.Writer) *http.Client {
	asked := *client
	next := client.Transport
	if next == nil {
		next = http.DefaultTransport
	}
	asked.Transport = &askingTransport{next: next, stderr: stderr, hosts: map[string]bool{}}
	return &asked
}

type askingTransport struct {
	next   http.RoundTripper
	stderr io.Writer
	mu     sync.Mutex
	hosts  map[string]bool
}

func (a *askingTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	a.mu.Lock()
	first := !a.hosts[request.URL.Host]
	a.hosts[request.URL.Host] = true
	a.mu.Unlock()
	if first {
		cli.Say(a.stderr, "costroid: asking %s\n", request.URL.Host)
	}
	return a.next.RoundTrip(request)
}
