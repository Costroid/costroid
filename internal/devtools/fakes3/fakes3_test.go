// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Costroid Authors

package fakes3

import (
	"crypto/md5" //nolint:gosec // asserting the S3 ETag shape, not security.
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func newFixture(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	for name, body := range map[string]string{
		"demo/exports/a.txt":  "alpha",
		"demo/exports/b.txt":  "beta",
		"demo/exports/c.txt":  "gamma",
		"demo/other/skip.txt": "skip",
	} {
		p := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func doGet(t *testing.T, url string, header map[string]string) (*http.Response, string) {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		t.Fatal(err)
	}
	for k, v := range header {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return resp, string(body)
}

func TestListObjectsV2PrefixAndPagination(t *testing.T) {
	srv := httptest.NewServer(New(newFixture(t)))
	defer srv.Close()

	// Page 1: two of the three matching keys, truncated.
	resp, body := doGet(t, srv.URL+"/demo?list-type=2&prefix=exports/&max-keys=2", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, body %s", resp.StatusCode, body)
	}
	for _, want := range []string{"<Key>exports/a.txt</Key>", "<Key>exports/b.txt</Key>", "<IsTruncated>true</IsTruncated>", "<NextContinuationToken>exports/b.txt</NextContinuationToken>"} {
		if !strings.Contains(body, want) {
			t.Errorf("page 1 %s missing %s", body, want)
		}
	}
	if strings.Contains(body, "c.txt") || strings.Contains(body, "skip.txt") {
		t.Errorf("page 1 leaked keys beyond the page or prefix: %s", body)
	}

	// Page 2 via the continuation token: the remaining key, not truncated.
	resp, body = doGet(t, srv.URL+"/demo?list-type=2&prefix=exports/&max-keys=2&continuation-token=exports/b.txt", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, body %s", resp.StatusCode, body)
	}
	for _, want := range []string{"<Key>exports/c.txt</Key>", "<IsTruncated>false</IsTruncated>"} {
		if !strings.Contains(body, want) {
			t.Errorf("page 2 %s missing %s", body, want)
		}
	}
	if strings.Contains(body, "a.txt") || strings.Contains(body, "b.txt") {
		t.Errorf("page 2 repeated earlier keys: %s", body)
	}

	// ETags are quoted MD5 hex of the object bytes.
	wantETag := fmt.Sprintf("&#34;%x&#34;", md5.Sum([]byte("gamma"))) //nolint:gosec // ETag shape
	if !strings.Contains(body, wantETag) {
		t.Errorf("listing %s lacks the content-derived ETag %s", body, wantETag)
	}
}

func TestGetObjectETagAndIfMatch(t *testing.T) {
	srv := httptest.NewServer(New(newFixture(t)))
	defer srv.Close()

	resp, body := doGet(t, srv.URL+"/demo/exports/a.txt", nil)
	if resp.StatusCode != http.StatusOK || body != "alpha" {
		t.Fatalf("GetObject = %d %q, want 200 alpha", resp.StatusCode, body)
	}
	wantETag := fmt.Sprintf("%q", fmt.Sprintf("%x", md5.Sum([]byte("alpha")))) //nolint:gosec // ETag shape
	if got := resp.Header.Get("ETag"); got != wantETag {
		t.Errorf("ETag = %s, want %s", got, wantETag)
	}

	// If-Match honors the content ETag and rejects a stale one.
	resp, _ = doGet(t, srv.URL+"/demo/exports/a.txt", map[string]string{"If-Match": wantETag})
	if resp.StatusCode != http.StatusOK {
		t.Errorf("If-Match with current ETag = %d, want 200", resp.StatusCode)
	}
	resp, body = doGet(t, srv.URL+"/demo/exports/a.txt", map[string]string{"If-Match": `"stale"`})
	if resp.StatusCode != http.StatusPreconditionFailed || !strings.Contains(body, "PreconditionFailed") {
		t.Errorf("If-Match with stale ETag = %d %s, want 412 PreconditionFailed", resp.StatusCode, body)
	}

	// Missing key and bucket produce S3-shaped errors.
	resp, body = doGet(t, srv.URL+"/demo/exports/nope.txt", nil)
	if resp.StatusCode != http.StatusNotFound || !strings.Contains(body, "NoSuchKey") {
		t.Errorf("missing key = %d %s, want 404 NoSuchKey", resp.StatusCode, body)
	}
	resp, body = doGet(t, srv.URL+"/nobucket/whatever?list-type=2", nil)
	if resp.StatusCode != http.StatusNotFound || !strings.Contains(body, "NoSuchBucket") {
		t.Errorf("missing bucket = %d %s, want 404 NoSuchBucket", resp.StatusCode, body)
	}
}

func TestForbid(t *testing.T) {
	h := New(newFixture(t))
	h.Forbid("demo/exports")
	srv := httptest.NewServer(h)
	defer srv.Close()

	resp, body := doGet(t, srv.URL+"/demo?list-type=2&prefix=exports/", nil)
	if resp.StatusCode != http.StatusForbidden || !strings.Contains(body, "AccessDenied") {
		t.Errorf("forbidden list = %d %s, want 403 AccessDenied", resp.StatusCode, body)
	}
	resp, body = doGet(t, srv.URL+"/demo/exports/a.txt", nil)
	if resp.StatusCode != http.StatusForbidden || !strings.Contains(body, "AccessDenied") {
		t.Errorf("forbidden get = %d %s, want 403 AccessDenied", resp.StatusCode, body)
	}
	// Keys outside the forbidden prefix stay readable.
	resp, _ = doGet(t, srv.URL+"/demo/other/skip.txt", nil)
	if resp.StatusCode != http.StatusOK {
		t.Errorf("unforbidden get = %d, want 200", resp.StatusCode)
	}
}
