// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Costroid Authors

package api

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"testing/fstest"
)

func TestHandler(t *testing.T) {
	static := fstest.MapFS{
		"index.html": &fstest.MapFile{Data: []byte("<!doctype html><title>Costroid</title>")},
	}
	handler := NewHandler("1.2.3-test", static)

	tests := []struct {
		name       string
		method     string
		path       string
		wantStatus int
		check      func(t *testing.T, body []byte)
	}{
		{
			name:       "healthz is alive",
			method:     http.MethodGet,
			path:       "/healthz",
			wantStatus: http.StatusOK,
			check: func(t *testing.T, body []byte) {
				if got := string(body); got != "ok" {
					t.Errorf("body = %q, want %q", got, "ok")
				}
			},
		},
		{
			name:       "meta reports identity and versions",
			method:     http.MethodGet,
			path:       "/api/v1/meta",
			wantStatus: http.StatusOK,
			check: func(t *testing.T, body []byte) {
				var meta Meta
				if err := json.Unmarshal(body, &meta); err != nil {
					t.Fatalf("unmarshaling body %q: %v", body, err)
				}
				want := Meta{Name: "costroid", Version: "1.2.3-test", FocusVersion: "1.4"}
				if meta != want {
					t.Errorf("meta = %+v, want %+v", meta, want)
				}
			},
		},
		{
			name:       "root serves the dashboard",
			method:     http.MethodGet,
			path:       "/",
			wantStatus: http.StatusOK,
			check: func(t *testing.T, body []byte) {
				if got := string(body); got != "<!doctype html><title>Costroid</title>" {
					t.Errorf("body = %q, want the static index.html", got)
				}
			},
		},
		{
			// Non-GET requests don't match the generated "GET /api/v1/meta"
			// route; they fall through to the catch-all static handler.
			name:       "non-GET meta is not served",
			method:     http.MethodPost,
			path:       "/api/v1/meta",
			wantStatus: http.StatusNotFound,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(tt.method, tt.path, nil)
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)

			res := rec.Result()
			defer func() { _ = res.Body.Close() }()
			body, err := io.ReadAll(res.Body)
			if err != nil {
				t.Fatalf("reading body: %v", err)
			}
			if res.StatusCode != tt.wantStatus {
				t.Fatalf("status = %d, want %d (body: %q)", res.StatusCode, tt.wantStatus, body)
			}
			if tt.check != nil {
				tt.check(t, body)
			}
		})
	}
}
