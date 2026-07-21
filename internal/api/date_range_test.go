// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Costroid Authors

package api

import (
	"bufio"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/Costroid/costroid/internal/storage"
)

func TestContractDateRanges(t *testing.T) {
	contract, err := os.Open("../../contracts/openapi.yaml")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = contract.Close() }()

	var currentPath string
	var paths []string
	scanner := bufio.NewScanner(contract)
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "  /api/v1/") && strings.HasSuffix(line, ":") {
			currentPath = strings.TrimSuffix(strings.TrimSpace(line), ":")
		}
		if strings.TrimSpace(line) == "- name: start" {
			paths = append(paths, currentPath)
		}
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
	if len(paths) != 7 {
		t.Fatalf("contract endpoints with start = %v, want 7", paths)
	}

	store := &fakeStore{businessInfos: []storage.BusinessMetricInfo{{Name: "requests"}}}
	handler := NewHandler("test", testStatic(), store, "")
	for _, endpoint := range paths {
		endpoint := endpoint
		t.Run(strings.TrimPrefix(endpoint, "/api/v1/"), func(t *testing.T) {
			separator := "?"
			if endpoint == "/api/v1/unit-economics/daily" {
				endpoint += "?metric=requests"
				separator = "&"
			}
			for _, tc := range []struct {
				name  string
				query string
				want  int
			}{
				{name: "inverted", query: "start=2026-07-02&end=2026-07-01", want: http.StatusBadRequest},
				{name: "equal", query: "start=2026-07-01&end=2026-07-01", want: http.StatusOK},
				{name: "start only", query: "start=2026-07-01", want: http.StatusOK},
				{name: "end only", query: "end=2026-07-01", want: http.StatusOK},
			} {
				t.Run(tc.name, func(t *testing.T) {
					recorder := httptest.NewRecorder()
					handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, endpoint+separator+tc.query, nil))
					if recorder.Code != tc.want {
						t.Fatalf("GET %s: status = %d, want %d; body = %q", endpoint+separator+tc.query, recorder.Code, tc.want, recorder.Body.String())
					}
				})
			}
		})
	}
}
