// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Costroid Authors

package fakeopenai_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Costroid/costroid/internal/devtools/fakeopenai"
)

const fixture = "../../../testdata/openai-cost/fixture"

func get(t *testing.T, url, bearer string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		t.Fatal(err)
	}
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

func TestFakeOpenAIAuthAndPagination(t *testing.T) {
	h := fakeopenai.New(fixture)
	h.PageSize = 1 // 2026-05 has 2 buckets → forces a cursor
	srv := httptest.NewServer(h)
	defer srv.Close()
	// start_time = 2026-05-01T00:00:00Z in Unix seconds.
	endpoint := srv.URL + "/v1/organization/costs?start_time=1777593600&end_time=1780272000"

	if resp := get(t, endpoint, ""); resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("no key: HTTP %d, want 401", resp.StatusCode)
	}
	if resp := get(t, endpoint, "wrong"); resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("wrong key: HTTP %d, want 401", resp.StatusCode)
	}

	var page struct {
		Object   string            `json:"object"`
		Data     []json.RawMessage `json:"data"`
		HasMore  bool              `json:"has_more"`
		NextPage *string           `json:"next_page"`
	}
	resp := get(t, endpoint, fakeopenai.AdminKey)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("page 1: HTTP %d", resp.StatusCode)
	}
	if err := json.NewDecoder(resp.Body).Decode(&page); err != nil {
		t.Fatal(err)
	}
	if page.Object != "page" || len(page.Data) != 1 || !page.HasMore || page.NextPage == nil {
		t.Fatalf("page 1 = object %q, %d bucket(s), has_more=%t, next=%v; want page/1/true/nonnil",
			page.Object, len(page.Data), page.HasMore, page.NextPage)
	}

	resp = get(t, endpoint+"&page="+*page.NextPage, fakeopenai.AdminKey)
	if err := json.NewDecoder(resp.Body).Decode(&page); err != nil {
		t.Fatal(err)
	}
	if len(page.Data) != 1 || page.HasMore || page.NextPage != nil {
		t.Fatalf("page 2 = %d bucket(s), has_more=%t, next=%v; want 1/false/nil", len(page.Data), page.HasMore, page.NextPage)
	}

	// A month with no fixture is empty (start_time = 2026-09-01).
	resp = get(t, srv.URL+"/v1/organization/costs?start_time=1788307200", fakeopenai.AdminKey)
	if err := json.NewDecoder(resp.Body).Decode(&page); err != nil {
		t.Fatal(err)
	}
	if len(page.Data) != 0 || page.HasMore {
		t.Errorf("empty month = %d bucket(s), has_more=%t; want 0/false", len(page.Data), page.HasMore)
	}
}
