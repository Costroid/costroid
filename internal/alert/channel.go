// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Costroid Authors

package alert

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/Costroid/costroid/internal/credentials"
)

const (
	// defaultSendTimeout hard-bounds a single alert POST. It is deliberately
	// shorter than the connectors' 60s ingest timeout: an alert is best-effort
	// and must not delay the serial scheduler loop for long.
	defaultSendTimeout = 10 * time.Second
	// defaultRetryBackoff spaces the single bounded retry.
	defaultRetryBackoff = 500 * time.Millisecond
	// maxRetries is the bounded retry count: at most one retry on a transport
	// error or a 5xx response.
	maxRetries = 1
	// responseBodyLimit bounds the (discarded) response body read.
	responseBodyLimit = 1 << 20
)

// Channel is one delivery target. Send posts msg and returns an error that
// NEVER includes the channel's secret (an endpoint token, a Slack incoming
// webhook URL, or a bearer token). Name identifies the channel in isolation
// logging.
type Channel interface {
	Send(ctx context.Context, msg Message) error
	Name() string
}

// httpPoster carries the shared outbound-HTTP discipline for both channels: a
// per-attempt context timeout, a bounded retry, and a response body bounded by
// io.LimitReader. Transport errors are redacted so no secret-bearing URL leaks
// into a log line.
type httpPoster struct {
	client  *http.Client
	timeout time.Duration
	backoff time.Duration
	retries int
}

func newHTTPPoster() httpPoster {
	return httpPoster{
		client:  &http.Client{Timeout: defaultSendTimeout},
		timeout: defaultSendTimeout,
		backoff: defaultRetryBackoff,
		retries: maxRetries,
	}
}

// post sends body to endpoint, retrying at most p.retries times on a transport
// error or a 5xx response. authHeader, when non-empty, is the full Authorization
// header value; the caller reveals the secret exactly once, so post itself never
// touches a credentials.Secret. It honors ctx across the retry backoff.
func (p httpPoster) post(ctx context.Context, endpoint, contentType string, body []byte, authHeader string) error {
	var lastErr error
	for attempt := 0; attempt <= p.retries; attempt++ {
		if attempt > 0 {
			timer := time.NewTimer(p.backoff)
			select {
			case <-ctx.Done():
				timer.Stop()
				return ctx.Err()
			case <-timer.C:
			}
		}
		retry, err := p.attempt(ctx, endpoint, contentType, body, authHeader)
		if err == nil {
			return nil
		}
		lastErr = err
		if !retry {
			return err
		}
	}
	return lastErr
}

func (p httpPoster) attempt(ctx context.Context, endpoint, contentType string, body []byte, authHeader string) (retry bool, err error) {
	reqCtx, cancel := context.WithTimeout(ctx, p.timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		// Never echo the endpoint: it can be, or can carry, a secret.
		return false, errors.New("building alert request failed")
	}
	req.Header.Set("Content-Type", contentType)
	if authHeader != "" {
		req.Header.Set("Authorization", authHeader)
	}
	resp, err := p.client.Do(req)
	if err != nil {
		return true, redactTransportError(err)
	}
	defer func() { _ = resp.Body.Close() }()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, responseBodyLimit))
	switch {
	case resp.StatusCode >= 200 && resp.StatusCode < 300:
		return false, nil
	case resp.StatusCode >= 500:
		return true, fmt.Errorf("alert endpoint returned HTTP %d", resp.StatusCode)
	default:
		return false, fmt.Errorf("alert endpoint returned HTTP %d", resp.StatusCode)
	}
}

// redactTransportError strips the URL from a *url.Error so a secret-bearing
// endpoint (a Slack incoming-webhook URL, or a webhook whose path or query
// carries a token) never reaches a log line. The underlying transport cause (a
// dial or deadline error, which carries at most a host and port, never the
// secret path) is preserved for diagnosis.
func redactTransportError(err error) error {
	var uerr *url.Error
	if errors.As(err, &uerr) {
		return fmt.Errorf("alert %s request failed: %v", uerr.Op, uerr.Err)
	}
	return err
}

// webhookChannel POSTs the Message as JSON to an operator-configured endpoint,
// with an optional bearer token.
type webhookChannel struct {
	name     string
	endpoint *url.URL
	// auth, when non-nil, is added as `Authorization: Bearer <secret>`. It is
	// Reveal()'d once per Send, only at the header write.
	auth   *credentials.Secret
	poster httpPoster
}

// NewWebhookChannel validates endpoint and returns a webhook Channel. auth is
// optional; nil disables the Authorization header.
func NewWebhookChannel(name, endpoint string, auth *credentials.Secret) (Channel, error) {
	u, err := validateWebhookEndpoint(endpoint)
	if err != nil {
		return nil, err
	}
	return &webhookChannel{name: name, endpoint: u, auth: auth, poster: newHTTPPoster()}, nil
}

func (c *webhookChannel) Name() string { return c.name }

func (c *webhookChannel) Send(ctx context.Context, msg Message) error {
	body, err := json.Marshal(msg)
	if err != nil {
		return fmt.Errorf("marshalling alert payload: %w", err)
	}
	authHeader := ""
	if c.auth != nil {
		authHeader = "Bearer " + c.auth.Reveal()
	}
	return c.poster.post(ctx, c.endpoint.String(), "application/json", body, authHeader)
}

// slackChannel POSTs {"text": ...} to a Slack incoming-webhook URL. The URL is
// itself the secret and is Reveal()'d once per Send, only at the POST.
type slackChannel struct {
	name   string
	url    credentials.Secret
	poster httpPoster
}

// NewSlackChannel returns a Slack Channel. The incoming-webhook URL is a
// credential (it authenticates the post), so it is never validated or logged at
// construction; it is revealed only inside Send.
func NewSlackChannel(name string, webhookURL credentials.Secret) Channel {
	return &slackChannel{name: name, url: webhookURL, poster: newHTTPPoster()}
}

func (c *slackChannel) Name() string { return c.name }

func (c *slackChannel) Send(ctx context.Context, msg Message) error {
	payload := struct {
		Text string `json:"text"`
	}{Text: slackText(msg)}
	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshalling Slack alert: %w", err)
	}
	return c.poster.post(ctx, c.url.Reveal(), "application/json", body, "")
}

// slackText is a concise human summary carrying the source and outcome. It adds
// no cost amount, credential, or AI content of its own, and contains no em dash
// (rule 9). The connector error text, when present, is appended verbatim (the
// same operational string the operator already sees on the status endpoint).
func slackText(msg Message) string {
	var label string
	switch msg.Kind {
	case KindFailing:
		label = "FAILING"
	case KindReminder:
		label = "STILL FAILING"
	case KindRecovered:
		label = "RECOVERED"
	default:
		label = strings.ToUpper(string(msg.Kind))
	}
	text := fmt.Sprintf("Costroid sync %s: %s (%s); outcome %s", label, msg.Source, msg.Connector, msg.Outcome)
	if msg.Error != "" {
		text += "; " + msg.Error
	}
	return text
}

// validateWebhookEndpoint is the alert-specific endpoint guard. Unlike the
// connectors' validateEndpoint it does NOT mutate the path or reject a query
// string: a webhook POST target keeps its path and query verbatim. It requires
// an http or https scheme, rejects userinfo and a fragment, and requires https
// unless the host is loopback (the loopback-http carve-out keeps httptest
// servers usable). It deliberately does NOT block private or internal hosts:
// this is an operator-chosen outbound target (possibly an internal webhook), not
// a connector pulling untrusted data.
func validateWebhookEndpoint(raw string) (*url.URL, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil || u.Fragment != "" {
		return nil, errors.New("alert webhook endpoint must be an http:// or https:// URL with no userinfo and no fragment")
	}
	if u.Scheme == "http" && !isLoopback(u.Hostname()) {
		return nil, errors.New("alert webhook endpoint must use https unless the host is loopback")
	}
	return u, nil
}

// ValidateWebhookEndpoint reports whether raw is an acceptable webhook endpoint.
// The config parser (cmd/costroid) uses it to validate the alerts block
// structurally, without constructing a channel or touching the network.
func ValidateWebhookEndpoint(raw string) error {
	_, err := validateWebhookEndpoint(raw)
	return err
}

func isLoopback(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
