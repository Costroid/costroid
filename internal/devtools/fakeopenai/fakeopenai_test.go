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

// shape is the connector's documented request parameters the fake now
// requires (bare repeated group_by=, bucket_width, limit); the fake 400s a
// request that omits or mis-shapes them.
const shape = "&bucket_width=1d&limit=180&group_by=project_id&group_by=line_item"

func TestFakeOpenAIAuthAndPagination(t *testing.T) {
	h := fakeopenai.New(fixture)
	h.PageSize = 1 // 2026-05 has 2 buckets → forces a cursor
	srv := httptest.NewServer(h)
	defer srv.Close()
	// start_time = 2026-05-01T00:00:00Z in Unix seconds.
	endpoint := srv.URL + "/v1/organization/costs?start_time=1777593600&end_time=1780272000" + shape

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
	resp = get(t, srv.URL+"/v1/organization/costs?start_time=1788307200"+shape, fakeopenai.AdminKey)
	if err := json.NewDecoder(resp.Body).Decode(&page); err != nil {
		t.Fatal(err)
	}
	if len(page.Data) != 0 || page.HasMore {
		t.Errorf("empty month = %d bucket(s), has_more=%t; want 0/false", len(page.Data), page.HasMore)
	}
}

// TestFakeOpenAIRejectsRequestShape proves the fake asserts the request SHAPE
// per parameter: a bracketed group_by[]= (instead of the bare group_by=
// OpenAI documents), a wrong group_by value set, a wrong bucket_width, and a
// wrong limit each get a 400, while the correct shape gets a 200.
func TestFakeOpenAIRejectsRequestShape(t *testing.T) {
	srv := httptest.NewServer(fakeopenai.New(fixture))
	defer srv.Close()
	base := srv.URL + "/v1/organization/costs?start_time=1777593600&end_time=1780272000"

	bad := map[string]string{
		"bracketed group_by": "&bucket_width=1d&limit=180&group_by[]=project_id&group_by[]=line_item",
		"wrong group_by set": "&bucket_width=1d&limit=180&group_by=project_id",
		"wrong bucket_width": "&bucket_width=1w&limit=180&group_by=project_id&group_by=line_item",
		"wrong limit":        "&bucket_width=1d&limit=31&group_by=project_id&group_by=line_item",
		"missing shape":      "",
	}
	for name, suffix := range bad {
		if resp := get(t, base+suffix, fakeopenai.AdminKey); resp.StatusCode != http.StatusBadRequest {
			t.Errorf("%s: HTTP %d, want 400", name, resp.StatusCode)
		}
	}
	if resp := get(t, base+shape, fakeopenai.AdminKey); resp.StatusCode != http.StatusOK {
		t.Errorf("well-shaped request: HTTP %d, want 200", resp.StatusCode)
	}
}

// usageShape is the connector's documented per-endpoint usage request shape (bare
// group_by=model, bucket_width, limit=31) for the six MODEL endpoints.
const usageShape = "&bucket_width=1d&limit=31&group_by=model"

// TestFakeOpenAIUsageShape proves the fake asserts each usage endpoint's request
// SHAPE per parameter: the six model endpoints require the bare group_by=model
// (bracketed group_by[]=, a wrong dim, wrong bucket_width, and the costs
// endpoint's limit=180 each 400); code_interpreter_sessions must send NO group_by
// (any group_by 400s); a well-shaped request gets 200; and an unknown path 404s.
func TestFakeOpenAIUsageShape(t *testing.T) {
	srv := httptest.NewServer(fakeopenai.New(fixture))
	defer srv.Close()
	const times = "start_time=1777593600&end_time=1780272000"

	modelBase := srv.URL + "/v1/organization/usage/completions?" + times
	badModel := map[string]string{
		"bracketed group_by": "&bucket_width=1d&limit=31&group_by[]=model",
		"wrong group_by dim": "&bucket_width=1d&limit=31&group_by=service_tier",
		"wrong bucket_width": "&bucket_width=1w&limit=31&group_by=model",
		"costs limit 180":    "&bucket_width=1d&limit=180&group_by=model",
		"missing shape":      "",
	}
	for name, suffix := range badModel {
		if resp := get(t, modelBase+suffix, fakeopenai.AdminKey); resp.StatusCode != http.StatusBadRequest {
			t.Errorf("completions %s: HTTP %d, want 400", name, resp.StatusCode)
		}
	}
	if resp := get(t, modelBase+usageShape, fakeopenai.AdminKey); resp.StatusCode != http.StatusOK {
		t.Errorf("well-shaped completions: HTTP %d, want 200", resp.StatusCode)
	}

	// code_interpreter_sessions: NO group_by allowed.
	sessBase := srv.URL + "/v1/organization/usage/code_interpreter_sessions?" + times + "&bucket_width=1d&limit=31"
	if resp := get(t, sessBase+"&group_by=model", fakeopenai.AdminKey); resp.StatusCode != http.StatusBadRequest {
		t.Errorf("code_interpreter_sessions with group_by: HTTP %d, want 400", resp.StatusCode)
	}
	if resp := get(t, sessBase, fakeopenai.AdminKey); resp.StatusCode != http.StatusOK {
		t.Errorf("well-shaped code_interpreter_sessions: HTTP %d, want 200", resp.StatusCode)
	}

	// An unknown (non-usage, non-costs) path still 404s.
	if resp := get(t, srv.URL+"/v1/organization/projects", fakeopenai.AdminKey); resp.StatusCode != http.StatusNotFound {
		t.Errorf("unknown path: HTTP %d, want 404", resp.StatusCode)
	}
}
