// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Costroid Authors

// Package aiwire is the single HTTP GET chokepoint the AI-vendor cost
// connectors (anthropic-cost, openai-cost) share for every request to a
// vendor's cost/usage API. It exists to make one Cardinal-Rule (decision D7)
// guarantee STRUCTURAL rather than reviewer-enforced: a raw HTTP response body
// can never become an error string, a log line, or a return value.
//
// The raw bytes of a 200 response live in the UNEXPORTED field of a Body and
// leave the package ONLY through Body.Decode(&typedStruct): a caller models the
// fields it needs and json.Unmarshal drops everything else, so the whole body
// is never handed back. There is deliberately no Bytes/String/Raw/Hash
// accessor. On a non-200 the body is dropped entirely — Get returns a typed
// *StatusError carrying ONLY the HTTP status and a TYPED-decoded vendor error
// identifier (code/type), never the raw body, never a vendor `message` (which
// can echo request content), never the credential. Transport errors are
// query-scrubbed so a request URL never reaches a log or error verbatim.
//
// This package has NO dependency on either connector: the connectors set their
// own auth headers and translate *StatusError back into their bespoke,
// body-free error prose.
package aiwire

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"
)

const (
	// maxBodyBytes caps how much of a response body is read into memory; the
	// excess never enters memory, an error, or a log.
	maxBodyBytes = 1 << 20
	// maxRetries bounds the Retry-After honoring on 429 responses.
	maxRetries = 5
)

// Body wraps the raw bytes of a successful (200) response. The bytes are
// UNEXPORTED and leave the package ONLY through Decode; there is intentionally
// no accessor that returns or renders them (Cardinal Rule, decision D7).
type Body struct {
	raw []byte
}

// Decode unmarshals the response body into v, a pointer to a caller-owned,
// narrowly-modeled struct. It is the ONLY way any response bytes leave this
// package; fields v does not model are dropped by json.Unmarshal.
func (b Body) Decode(v any) error {
	return json.Unmarshal(b.raw, v)
}

// StatusError is the typed error Get returns on any non-200 response. It
// carries ONLY the HTTP status and, when the body decoded as a vendor error
// envelope, the vendor's error IDENTIFIER (code/type) — never the raw body and
// never a vendor `message`.
type StatusError struct {
	Status     int
	VendorCode string
}

// Error implements error with body-free text: the numeric status, plus the
// typed vendor code in parentheses when one decoded.
func (e *StatusError) Error() string {
	if e.VendorCode != "" {
		return fmt.Sprintf("HTTP %d (%s)", e.Status, e.VendorCode)
	}
	return fmt.Sprintf("HTTP %d", e.Status)
}

// Get issues an HTTP GET to url with the given header (the caller sets any auth
// headers), bounded 429 retries honoring Retry-After, and the Cardinal-Rule
// body handling described on the package. It never logs or echoes the header or
// the request URL's query. GET only; this package has no POST surface.
func Get(ctx context.Context, client *http.Client, url string, header http.Header) (Body, error) {
	if client == nil {
		client = http.DefaultClient
	}
	for attempt := 0; ; attempt++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if err != nil {
			return Body{}, fmt.Errorf("aiwire: building request: %w", err)
		}
		for k, vs := range header {
			for _, v := range vs {
				req.Header.Add(k, v)
			}
		}

		resp, err := client.Do(req)
		if err != nil {
			// A transport error may embed the request URL — scrub the query so
			// it never reaches a log or error verbatim.
			return Body{}, errors.New(scrubTransportErr(err))
		}
		body, readErr := io.ReadAll(io.LimitReader(resp.Body, maxBodyBytes))
		_ = resp.Body.Close()

		switch {
		case resp.StatusCode == http.StatusOK:
			if readErr != nil {
				// Fail closed: never hand back a truncated Body on success.
				return Body{}, fmt.Errorf("aiwire: reading response body: %w", readErr)
			}
			return Body{raw: body}, nil
		case resp.StatusCode == http.StatusTooManyRequests && attempt < maxRetries:
			if err := waitRetryAfter(ctx, resp.Header.Get("Retry-After")); err != nil {
				return Body{}, err
			}
			continue
		default:
			// Non-200 (including the final 429 after exhausting retries): drop
			// the body entirely, keep only the status and the typed vendor code.
			return Body{}, &StatusError{Status: resp.StatusCode, VendorCode: vendorCode(body)}
		}
	}
}

// vendorError is the lenient envelope for BOTH vendors' error shapes — OpenAI's
// {"error":{"type":..,"code":..}} and Anthropic's
// {"type":"error","error":{"type":..}}. It models ONLY identifier fields: it
// has NO field bound to a JSON key named message/content/text/prompt, so a
// vendor message (which can echo request content) is structurally un-decodable
// here, not merely ignored.
type vendorError struct {
	Type  string `json:"type"`
	Error struct {
		Type string `json:"type"`
		Code string `json:"code"`
	} `json:"error"`
}

// vendorCode decodes body into the identifier-only envelope and returns the
// first non-empty of the nested code, the nested type, and the top-level type.
// A body that does not decode yields "" (fail-safe: no identifier, no leak).
func vendorCode(body []byte) string {
	var ve vendorError
	if err := json.Unmarshal(body, &ve); err != nil {
		return ""
	}
	switch {
	case ve.Error.Code != "":
		return ve.Error.Code
	case ve.Error.Type != "":
		return ve.Error.Type
	default:
		return ve.Type
	}
}

// waitRetryAfter honors a Retry-After header — either delta-seconds or an
// RFC 1123 HTTP-date (both forms the spec permits) — bounded to a sane
// maximum, or a short default when the header is absent or unparseable. A
// date already in the past yields a zero wait (retry immediately).
func waitRetryAfter(ctx context.Context, header string) error {
	wait := retryAfterDelay(header)
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(wait):
		return nil
	}
}

// retryAfterDelay parses a Retry-After header into a bounded wait duration.
func retryAfterDelay(header string) time.Duration {
	const maxWait = 60 * time.Second
	if header == "" {
		return 2 * time.Second
	}
	if secs, err := time.ParseDuration(header + "s"); err == nil && secs > 0 {
		return min(secs, maxWait)
	}
	if t, err := http.ParseTime(header); err == nil {
		return min(max(time.Until(t), 0), maxWait)
	}
	return 2 * time.Second
}

// scrubTransportErr removes any URL query string from a transport error so a
// request URL never reaches a log or error verbatim.
func scrubTransportErr(err error) string {
	var urlErr *url.Error
	if errors.As(err, &urlErr) {
		if u, perr := url.Parse(urlErr.URL); perr == nil {
			u.RawQuery = ""
			return urlErr.Op + " " + u.String() + ": " + urlErr.Err.Error()
		}
	}
	return err.Error()
}
