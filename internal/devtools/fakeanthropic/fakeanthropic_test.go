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

// shape is the connector's documented request parameters the fake now
// requires (bracketed group_by[]=, bucket_width, limit); the fake 400s a
// request that omits or mis-shapes them.
const shape = "&bucket_width=1d&limit=31&group_by[]=description&group_by[]=workspace_id"

func TestFakeAnthropicAuthAndPagination(t *testing.T) {
	h := fakeanthropic.New(fixture)
	h.PageSize = 1 // 2026-05 has 2 buckets → forces a cursor
	srv := httptest.NewServer(h)
	defer srv.Close()
	endpoint := srv.URL + "/v1/organizations/cost_report?starting_at=2026-05-01T00:00:00Z&ending_at=2026-06-01T00:00:00Z" + shape

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
	resp = get(t, srv.URL+"/v1/organizations/cost_report?starting_at=2026-09-01T00:00:00Z"+shape, fakeanthropic.AdminKey)
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

// TestFakeAnthropicRejectsRequestShape proves the fake asserts the request
// SHAPE per parameter: a bare group_by= (instead of the bracketed group_by[]=
// Anthropic documents), a wrong group_by[] value set, a wrong bucket_width,
// and a wrong limit each get a 400, while the correct shape gets a 200.
func TestFakeAnthropicRejectsRequestShape(t *testing.T) {
	srv := httptest.NewServer(fakeanthropic.New(fixture))
	defer srv.Close()
	base := srv.URL + "/v1/organizations/cost_report?starting_at=2026-05-01T00:00:00Z&ending_at=2026-06-01T00:00:00Z"

	bad := map[string]string{
		"bare group_by":      "&bucket_width=1d&limit=31&group_by=description&group_by=workspace_id",
		"wrong group_by set": "&bucket_width=1d&limit=31&group_by[]=description",
		"wrong bucket_width": "&bucket_width=1w&limit=31&group_by[]=description&group_by[]=workspace_id",
		"wrong limit":        "&bucket_width=1d&limit=7&group_by[]=description&group_by[]=workspace_id",
		"missing shape":      "",
	}
	for name, suffix := range bad {
		if resp := get(t, base+suffix, fakeanthropic.AdminKey); resp.StatusCode != http.StatusBadRequest {
			t.Errorf("%s: HTTP %d, want 400", name, resp.StatusCode)
		}
	}
	if resp := get(t, base+shape, fakeanthropic.AdminKey); resp.StatusCode != http.StatusOK {
		t.Errorf("well-shaped request: HTTP %d, want 200", resp.StatusCode)
	}
}

// usageShape is the usage endpoint's documented request parameters (bracketed
// group_by[]= over the five join dims, bucket_width, limit).
const usageShape = "&bucket_width=1d&limit=31&group_by[]=model&group_by[]=workspace_id&group_by[]=context_window&group_by[]=inference_geo&group_by[]=service_tier"

// TestFakeAnthropicUsageEndpoint proves the usage_report/messages endpoint
// enforces auth, its OWN per-parameter shape check (the five join dims,
// bucket_width, limit; rejecting a bare group_by= and a wrong dim set),
// paginates via has_more/next_page, and can be forced to fail one month.
func TestFakeAnthropicUsageEndpoint(t *testing.T) {
	h := fakeanthropic.New(fixture)
	h.PageSize = 1 // 2026-05 usage has 2 buckets → forces a cursor
	srv := httptest.NewServer(h)
	defer srv.Close()
	usagePath := "/v1/organizations/usage_report/messages"
	base := srv.URL + usagePath + "?starting_at=2026-05-01T00:00:00Z&ending_at=2026-06-01T00:00:00Z"

	// Auth applies to the usage path too.
	if resp := get(t, base+usageShape, ""); resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("no key: HTTP %d, want 401", resp.StatusCode)
	}

	// Per-parameter shape rejections.
	bad := map[string]string{
		"bare group_by":      "&bucket_width=1d&limit=31&group_by=model&group_by=workspace_id&group_by=context_window&group_by=inference_geo&group_by=service_tier",
		"wrong group_by set": "&bucket_width=1d&limit=31&group_by[]=model&group_by[]=workspace_id",
		"wrong bucket_width": "&bucket_width=1w&limit=31&group_by[]=model&group_by[]=workspace_id&group_by[]=context_window&group_by[]=inference_geo&group_by[]=service_tier",
		"wrong limit":        "&bucket_width=1d&limit=7&group_by[]=model&group_by[]=workspace_id&group_by[]=context_window&group_by[]=inference_geo&group_by[]=service_tier",
		"missing shape":      "",
	}
	for name, suffix := range bad {
		if resp := get(t, base+suffix, fakeanthropic.AdminKey); resp.StatusCode != http.StatusBadRequest {
			t.Errorf("%s: HTTP %d, want 400", name, resp.StatusCode)
		}
	}

	// Page 1: one bucket, more to come.
	var page struct {
		Data     []json.RawMessage `json:"data"`
		HasMore  bool              `json:"has_more"`
		NextPage string            `json:"next_page"`
	}
	resp := get(t, base+usageShape, fakeanthropic.AdminKey)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("usage page 1: HTTP %d", resp.StatusCode)
	}
	if err := json.NewDecoder(resp.Body).Decode(&page); err != nil {
		t.Fatal(err)
	}
	if len(page.Data) != 1 || !page.HasMore || page.NextPage == "" {
		t.Fatalf("usage page 1 = %d bucket(s), has_more=%t; want 1/true", len(page.Data), page.HasMore)
	}

	// Forced usage failure for one month → 500.
	h.UsageFailMonth = "2026-05"
	if resp := get(t, base+usageShape, fakeanthropic.AdminKey); resp.StatusCode != http.StatusInternalServerError {
		t.Errorf("forced usage failure: HTTP %d, want 500", resp.StatusCode)
	}
}
