// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Costroid Authors

package fakeanthropic_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Costroid/costroid/internal/devtools/fakeanthropic"
)

const fixture = "../../../testdata/anthropic-cost/fixture"

func get(t *testing.T, url, key string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		t.Fatal(err)
	}
	if key != "" {
		req.Header.Set("x-api-key", key)
		req.Header.Set("anthropic-version", "2023-06-01")
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

func TestFakeAnthropicAuthAndPagination(t *testing.T) {
	h := fakeanthropic.New(fixture)
	h.PageSize = 1 // 2026-05 has 2 buckets → forces a cursor
	srv := httptest.NewServer(h)
	defer srv.Close()
	endpoint := srv.URL + "/v1/organizations/cost_report?starting_at=2026-05-01T00:00:00Z&ending_at=2026-06-01T00:00:00Z"

	// Missing/wrong key → 401.
	if resp := get(t, endpoint, ""); resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("no key: HTTP %d, want 401", resp.StatusCode)
	}
	if resp := get(t, endpoint, "wrong"); resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("wrong key: HTTP %d, want 401", resp.StatusCode)
	}

	// Page 1: one bucket, more to come.
	var page struct {
		Data     []json.RawMessage `json:"data"`
		HasMore  bool              `json:"has_more"`
		NextPage string            `json:"next_page"`
	}
	resp := get(t, endpoint, fakeanthropic.AdminKey)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("page 1: HTTP %d", resp.StatusCode)
	}
	if err := json.NewDecoder(resp.Body).Decode(&page); err != nil {
		t.Fatal(err)
	}
	if len(page.Data) != 1 || !page.HasMore || page.NextPage == "" {
		t.Fatalf("page 1 = %d bucket(s), has_more=%t, next=%q; want 1/true/nonempty", len(page.Data), page.HasMore, page.NextPage)
	}

	// Page 2 via the cursor: the last bucket, no more.
	resp = get(t, endpoint+"&page="+page.NextPage, fakeanthropic.AdminKey)
	if err := json.NewDecoder(resp.Body).Decode(&page); err != nil {
		t.Fatal(err)
	}
	if len(page.Data) != 1 || page.HasMore {
		t.Fatalf("page 2 = %d bucket(s), has_more=%t; want 1/false", len(page.Data), page.HasMore)
	}

	// A month with no fixture is an empty data array.
	resp = get(t, srv.URL+"/v1/organizations/cost_report?starting_at=2026-09-01T00:00:00Z", fakeanthropic.AdminKey)
	if err := json.NewDecoder(resp.Body).Decode(&page); err != nil {
		t.Fatal(err)
	}
	if len(page.Data) != 0 || page.HasMore {
		t.Errorf("empty month = %d bucket(s), has_more=%t; want 0/false", len(page.Data), page.HasMore)
	}

	if len(h.Requests()) == 0 {
		t.Error("no requests recorded")
	}
}
