// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Costroid Authors

package modelwire

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestCompleteRequestAndReply(t *testing.T) {
	calls := 0
	transport := roundTripFunc(func(req *http.Request) (*http.Response, error) {
		calls++
		if req.Method != http.MethodPost || req.URL.String() != "https://model.invalid/translate" {
			t.Fatalf("request = %s %s", req.Method, req.URL)
		}
		if got := req.Header.Get("Authorization"); got != "Bearer secret-value" {
			t.Fatalf("Authorization = %q", got)
		}
		body, err := io.ReadAll(req.Body)
		if err != nil {
			t.Fatal(err)
		}
		want := `{"model":"local-model","messages":[{"role":"user","content":"{\"question\":\"cost?\"}"}],"response_format":{"type":"json_object"}}`
		if string(body) != want {
			t.Fatalf("body = %s, want %s", body, want)
		}
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(`{"choices":[{"message":{"role":"assistant","content":"{\"endpoint\":\"costs-summary\"}"}}]}`))}, nil
	})
	client := New("https://model.invalid/translate", "local-model", "secret-value", &http.Client{Transport: transport})
	reply, err := client.Complete(context.Background(), []byte(`{"question":"cost?"}`))
	if err != nil {
		t.Fatal(err)
	}
	if calls != 1 || string(reply) != `{"endpoint":"costs-summary"}` {
		t.Fatalf("calls = %d, reply = %q", calls, reply)
	}
}

func TestCompleteRejectsOversizedReply(t *testing.T) {
	transport := roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(strings.Repeat("x", responseBodyLimit+1)))}, nil
	})
	client := New("https://model.invalid/translate", "local-model", "", &http.Client{Transport: transport})
	if _, err := client.Complete(context.Background(), []byte(`{}`)); err == nil || err.Error() != "model reply exceeded the 1 MiB limit" {
		t.Fatalf("Complete error = %v", err)
	}
}

// A model endpoint that answers with a redirect must not cause the prompt to be
// replayed to the redirect target: the operator chose one endpoint, and only
// that endpoint may see the question. The redirect is surfaced as a failure
// rather than followed.
func TestRedirectIsNotFollowedAndPromptIsNotReplayed(t *testing.T) {
	var visited []string
	transport := roundTripFunc(func(req *http.Request) (*http.Response, error) {
		visited = append(visited, req.URL.String())
		if req.URL.Host == "model.invalid" {
			return &http.Response{
				StatusCode: http.StatusTemporaryRedirect,
				Header:     http.Header{"Location": []string{"https://attacker.invalid/collect"}},
				Body:       io.NopCloser(strings.NewReader("")),
			}, nil
		}
		t.Fatalf("prompt was replayed to an unconfigured host: %s", req.URL)
		return nil, nil
	})
	client := New("https://model.invalid/translate", "m", "secret-value", &http.Client{Transport: transport})

	if _, err := client.Complete(context.Background(), []byte("question-sentinel")); err == nil {
		t.Fatal("redirect response was accepted; want an error")
	}
	if len(visited) != 1 || visited[0] != "https://model.invalid/translate" {
		t.Fatalf("visited = %v, want only the configured endpoint", visited)
	}
}

// The no-redirect guarantee must not be bought by mutating a client the caller
// owns and may reuse elsewhere.
func TestNewDoesNotMutateTheCallersClient(t *testing.T) {
	caller := &http.Client{}
	New("https://model.invalid/translate", "m", "c", caller)
	if caller.CheckRedirect != nil {
		t.Fatal("New mutated the caller's client")
	}
}

// Instruction-tuned models wrap replies in a markdown fence unless asked for a
// bare object, and the strict parser rejects a fenced reply rather than peeling
// it. Asking for the shape up front is what keeps the parser strict, so the
// request must carry the ask.
func TestRequestAsksForABareJSONObject(t *testing.T) {
	var sent map[string]any
	transport := roundTripFunc(func(req *http.Request) (*http.Response, error) {
		body, err := io.ReadAll(req.Body)
		if err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(body, &sent); err != nil {
			t.Fatalf("request body is not JSON: %v", err)
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Body:       io.NopCloser(strings.NewReader(`{"choices":[{"message":{"role":"assistant","content":"{}"}}]}`)),
		}, nil
	})
	client := New("https://model.invalid/translate", "m", "", &http.Client{Transport: transport})
	if _, err := client.Complete(context.Background(), []byte(`{"question":"cost?"}`)); err != nil {
		t.Fatal(err)
	}
	format, ok := sent["response_format"].(map[string]any)
	if !ok {
		t.Fatalf("request carried no response_format: %v", sent)
	}
	if format["type"] != "json_object" {
		t.Fatalf("response_format type = %v, want json_object", format["type"])
	}
}
