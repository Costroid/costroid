// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Costroid Authors

package aiwire

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// TestGetDecodesBodyAndDropsUnmodeledFields proves Body.Decode is the ONLY way
// bytes come back: a 200 body decodes into a narrow struct correctly, each
// per-element json.RawMessage is byte-identical to the source (the property the
// connectors' ContentHash relies on — including an exact money literal a float64
// could not hold), and envelope fields the struct does not model are dropped.
func TestGetDecodesBodyAndDropsUnmodeledFields(t *testing.T) {
	// The value has more precision than a float64 holds, so a float64 round-trip
	// would corrupt it; it must survive byte-for-byte inside the raw element.
	const body = `{"data":[{"amount":{"value":123.4567890123456789}},{"line_item":"x"}],"has_more":true,"secret":"SENSITIVE-DROP"}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, body)
	}))
	defer srv.Close()

	got, err := Get(context.Background(), srv.Client(), srv.URL, nil)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	// A narrow struct pulls out ONLY the data elements; has_more and secret are
	// not modeled, so json.Unmarshal drops them (there is no other accessor).
	var page struct {
		Data []json.RawMessage `json:"data"`
	}
	if err := got.Decode(&page); err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if len(page.Data) != 2 {
		t.Fatalf("decoded %d data elements, want 2", len(page.Data))
	}
	if string(page.Data[0]) != `{"amount":{"value":123.4567890123456789}}` {
		t.Errorf("data[0] = %s, want the byte-identical element (exact money literal preserved)", page.Data[0])
	}
}

// TestGetNonOKTrapsRawBody proves a non-200 body never enters the error: a 500
// whose body carries a sentinel yields an error that does NOT contain it, a
// *StatusError with the numeric status, and no decodable Body.
func TestGetNonOKTrapsRawBody(t *testing.T) {
	const sentinel = "SENSITIVE-BODY-abc"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = io.WriteString(w, "raw proxy error page: "+sentinel)
	}))
	defer srv.Close()

	body, err := Get(context.Background(), srv.Client(), srv.URL, nil)
	if err == nil {
		t.Fatal("Get returned nil error on a 500")
	}
	if strings.Contains(err.Error(), sentinel) {
		t.Errorf("error leaked the raw body: %q contains %q", err.Error(), sentinel)
	}
	var se *StatusError
	if !errors.As(err, &se) {
		t.Fatalf("error is not a *StatusError: %v", err)
	}
	if se.Status != http.StatusInternalServerError {
		t.Errorf("StatusError.Status = %d, want 500", se.Status)
	}
	// No Body escapes on the error path (raw is nil → Decode fails on empty input).
	if decErr := body.Decode(&struct{}{}); decErr == nil {
		t.Error("a non-200 must not return a decodable Body")
	}
}

// TestGetVendorCodeIsIdentifierNotMessage proves the vendor envelope extracts
// ONLY the identifier (code/type) and NEVER a `message` — across OpenAI's shape,
// Anthropic's nested-error shape, and the code-wins-over-type priority.
func TestGetVendorCodeIsIdentifierNotMessage(t *testing.T) {
	cases := []struct {
		name     string
		status   int
		body     string
		wantCode string
	}{
		{"openai type", http.StatusUnauthorized,
			`{"error":{"type":"invalid_api_key","message":"SENSITIVE-msg"}}`, "invalid_api_key"},
		{"anthropic nested type", http.StatusUnauthorized,
			`{"type":"error","error":{"type":"authentication_error","message":"SENSITIVE-msg"}}`, "authentication_error"},
		{"code wins over type", http.StatusNotFound,
			`{"error":{"type":"invalid_request_error","code":"model_not_found","message":"SENSITIVE-msg"}}`, "model_not_found"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tc.status)
				_, _ = io.WriteString(w, tc.body)
			}))
			defer srv.Close()

			_, err := Get(context.Background(), srv.Client(), srv.URL, nil)
			var se *StatusError
			if !errors.As(err, &se) {
				t.Fatalf("error is not a *StatusError: %v", err)
			}
			if se.VendorCode != tc.wantCode {
				t.Errorf("VendorCode = %q, want %q", se.VendorCode, tc.wantCode)
			}
			if se.Status != tc.status {
				t.Errorf("Status = %d, want %d", se.Status, tc.status)
			}
			if strings.Contains(err.Error(), "SENSITIVE-msg") {
				t.Errorf("error leaked the vendor message: %q", err.Error())
			}
		})
	}
}

// TestGetRetriesThenSucceeds proves the bounded 429 retry loop: two 429s (with a
// tiny Retry-After) then a 200 returns the body after exactly three requests. No
// real sleeping occurs (Retry-After is 0.01s).
func TestGetRetriesThenSucceeds(t *testing.T) {
	var count int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if atomic.AddInt32(&count, 1) <= 2 {
			w.Header().Set("Retry-After", "0.01")
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = io.WriteString(w, `{"error":{"type":"rate_limit_exceeded"}}`)
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, `{"ok":true}`)
	}))
	defer srv.Close()

	body, err := Get(context.Background(), srv.Client(), srv.URL, nil)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got := atomic.LoadInt32(&count); got != 3 {
		t.Errorf("served %d requests, want 3 (two 429s then one success)", got)
	}
	var v struct {
		OK bool `json:"ok"`
	}
	if err := body.Decode(&v); err != nil || !v.OK {
		t.Errorf("Decode = %+v, err %v; want ok=true", v, err)
	}
}

// TestGetRetryGivesUp proves the retry loop is BOUNDED: an always-429 server
// makes Get give up after maxRetries and return a *StatusError with Status 429,
// after exactly 1 + maxRetries requests, without hanging.
func TestGetRetryGivesUp(t *testing.T) {
	var count int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&count, 1)
		w.Header().Set("Retry-After", "0.01")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = io.WriteString(w, `{"error":{"type":"rate_limit_exceeded"}}`)
	}))
	defer srv.Close()

	_, err := Get(context.Background(), srv.Client(), srv.URL, nil)
	var se *StatusError
	if !errors.As(err, &se) {
		t.Fatalf("give-up error is not a *StatusError: %v", err)
	}
	if se.Status != http.StatusTooManyRequests {
		t.Errorf("give-up Status = %d, want 429", se.Status)
	}
	if got := atomic.LoadInt32(&count); got != int32(1+maxRetries) {
		t.Errorf("served %d requests, want %d (1 + %d bounded retries)", got, 1+maxRetries, maxRetries)
	}
}

// TestGetTransportErrorScrubsQuery proves a transport failure's error text
// scrubs the URL query string (a ?token=... never reaches a log or error) and is
// NOT a *StatusError.
func TestGetTransportErrorScrubsQuery(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	srvURL := srv.URL
	client := srv.Client()
	srv.Close() // the port now refuses connections → a transport error

	_, err := Get(context.Background(), client, srvURL+"/x?token=SECRETTOKEN", nil)
	if err == nil {
		t.Fatal("Get to a closed server returned nil error")
	}
	if strings.Contains(err.Error(), "SECRETTOKEN") || strings.Contains(err.Error(), "token=") {
		t.Errorf("transport error leaked the query string: %q", err.Error())
	}
	var se *StatusError
	if errors.As(err, &se) {
		t.Errorf("transport error must not be a *StatusError, got %+v", se)
	}
}

// TestRetryAfterDelay proves the Retry-After parser honors BOTH delta-seconds
// AND an RFC 1123 HTTP-date (a bare date previously fell through to the
// default), caps the wait, and treats a past date as an immediate retry.
func TestRetryAfterDelay(t *testing.T) {
	future := time.Now().UTC().Add(30 * time.Second).Format(http.TimeFormat)
	past := "Mon, 01 Jan 2001 00:00:00 GMT"
	tests := []struct {
		name        string
		header      string
		wantZero    bool
		wantAtMost  time.Duration
		wantAtLeast time.Duration
	}{
		{name: "absent → default", header: "", wantAtLeast: 2 * time.Second, wantAtMost: 2 * time.Second},
		{name: "delta seconds", header: "5", wantAtLeast: 5 * time.Second, wantAtMost: 5 * time.Second},
		{name: "fractional delta", header: "0.01", wantAtMost: 20 * time.Millisecond, wantAtLeast: time.Millisecond},
		{name: "delta capped at 60s", header: "600", wantAtLeast: 60 * time.Second, wantAtMost: 60 * time.Second},
		{name: "http-date in the future", header: future, wantAtLeast: 20 * time.Second, wantAtMost: 60 * time.Second},
		{name: "http-date in the past → zero", header: past, wantZero: true},
		{name: "garbage → default", header: "not-a-number", wantAtLeast: 2 * time.Second, wantAtMost: 2 * time.Second},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := retryAfterDelay(tt.header)
			if tt.wantZero {
				if got != 0 {
					t.Errorf("retryAfterDelay(%q) = %v, want 0", tt.header, got)
				}
				return
			}
			if got < tt.wantAtLeast || got > tt.wantAtMost {
				t.Errorf("retryAfterDelay(%q) = %v, want within [%v, %v]", tt.header, got, tt.wantAtLeast, tt.wantAtMost)
			}
		})
	}
}

// TestWaitRetryAfterHonorsContext proves a cancelled context aborts the wait
// rather than sleeping (no wall-clock dependency).
func TestWaitRetryAfterHonorsContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := waitRetryAfter(ctx, "600"); err == nil {
		t.Error("waitRetryAfter with a cancelled context returned nil, want ctx.Err()")
	}
}
