// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Costroid Authors

package fakeblob

import (
	"encoding/xml"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func serve(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for name, body := range files {
		p := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	h := New(dir)
	h.PageSize = 2 // force paging in the listing test
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return srv.URL
}

func get(t *testing.T, url string, headers map[string]string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		t.Fatal(err)
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = resp.Body.Close() })
	return resp
}

func TestReadinessAndListPaging(t *testing.T) {
	url := serve(t, map[string]string{
		"acct/c/p/a.txt": "aaa",
		"acct/c/p/b.txt": "bbbb",
		"acct/c/p/c.txt": "c",
		"acct/c/q/d.txt": "outside the prefix",
	})

	if resp := get(t, url+"/", nil); resp.StatusCode != http.StatusOK {
		t.Fatalf("readiness = %d, want 200", resp.StatusCode)
	}

	type props struct {
		Etag          string `xml:"Etag"`
		LastModified  string `xml:"Last-Modified"`
		ContentLength int64  `xml:"Content-Length"`
	}
	type result struct {
		Blobs []struct {
			Name       string `xml:"Name"`
			Properties props  `xml:"Properties"`
		} `xml:"Blobs>Blob"`
		NextMarker string `xml:"NextMarker"`
	}
	list := func(marker string) result {
		u := url + "/acct/c?restype=container&comp=list&prefix=p/"
		if marker != "" {
			u += "&marker=" + marker
		}
		resp := get(t, u, nil)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("list = %d, want 200", resp.StatusCode)
		}
		var r result
		body, _ := io.ReadAll(resp.Body)
		if err := xml.Unmarshal(body, &r); err != nil {
			t.Fatalf("decoding listing: %v\n%s", err, body)
		}
		return r
	}

	// PageSize 2: three matching blobs arrive over two pages, in order,
	// with per-blob ETag / Last-Modified / Content-Length.
	page1 := list("")
	if len(page1.Blobs) != 2 || page1.NextMarker != "p/b.txt" {
		t.Fatalf("page 1 = %+v, want 2 blobs and marker p/b.txt", page1)
	}
	page2 := list(page1.NextMarker)
	if len(page2.Blobs) != 1 || page2.NextMarker != "" {
		t.Fatalf("page 2 = %+v, want the final blob", page2)
	}
	all := append(page1.Blobs, page2.Blobs...)
	wantSizes := map[string]int64{"p/a.txt": 3, "p/b.txt": 4, "p/c.txt": 1}
	for _, b := range all {
		if b.Properties.Etag == "" || b.Properties.LastModified == "" {
			t.Errorf("blob %s missing Etag/Last-Modified: %+v", b.Name, b.Properties)
		}
		if b.Properties.ContentLength != wantSizes[b.Name] {
			t.Errorf("blob %s Content-Length = %d, want %d", b.Name, b.Properties.ContentLength, wantSizes[b.Name])
		}
	}
}

func TestGetBlobIfMatchAndErrors(t *testing.T) {
	url := serve(t, map[string]string{"acct/c/k/blob.bin": "0123456789"})

	ok := get(t, url+"/acct/c/k/blob.bin", nil)
	if ok.StatusCode != http.StatusOK {
		t.Fatalf("get = %d, want 200", ok.StatusCode)
	}
	tag := ok.Header.Get("ETag")
	if !strings.HasPrefix(tag, `"0x`) {
		t.Fatalf("ETag = %q, want an Azure-shaped quoted token", tag)
	}

	if resp := get(t, url+"/acct/c/k/blob.bin", map[string]string{"If-Match": tag}); resp.StatusCode != http.StatusOK {
		t.Errorf("matching If-Match = %d, want 200", resp.StatusCode)
	}
	stale := get(t, url+"/acct/c/k/blob.bin", map[string]string{"If-Match": `"0xDEADBEEF"`})
	if stale.StatusCode != http.StatusPreconditionFailed || stale.Header.Get("x-ms-error-code") != "ConditionNotMet" {
		t.Errorf("stale If-Match = %d %q, want 412 ConditionNotMet", stale.StatusCode, stale.Header.Get("x-ms-error-code"))
	}

	// Ranged read (the SDK's RetryReader resumes with these).
	ranged := get(t, url+"/acct/c/k/blob.bin", map[string]string{"x-ms-range": "bytes=4-6"})
	body, _ := io.ReadAll(ranged.Body)
	if ranged.StatusCode != http.StatusPartialContent || string(body) != "456" {
		t.Errorf("ranged read = %d %q, want 206 \"456\"", ranged.StatusCode, body)
	}

	if resp := get(t, url+"/acct/c/k/missing.bin", nil); resp.StatusCode != http.StatusNotFound || resp.Header.Get("x-ms-error-code") != "BlobNotFound" {
		t.Errorf("missing blob = %d %q, want 404 BlobNotFound", resp.StatusCode, resp.Header.Get("x-ms-error-code"))
	}
	if resp := get(t, url+"/acct/no-such-container?restype=container&comp=list", nil); resp.StatusCode != http.StatusNotFound || resp.Header.Get("x-ms-error-code") != "ContainerNotFound" {
		t.Errorf("missing container = %d %q, want 404 ContainerNotFound", resp.StatusCode, resp.Header.Get("x-ms-error-code"))
	}
}

func TestForbid(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "acct/c/secret"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "acct/c/secret/x.bin"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	h := New(dir)
	h.Forbid("acct/c/secret")
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)

	resp := get(t, srv.URL+"/acct/c/secret/x.bin", nil)
	if resp.StatusCode != http.StatusForbidden || resp.Header.Get("x-ms-error-code") != "AuthorizationPermissionMismatch" {
		t.Errorf("forbidden blob = %d %q, want 403 AuthorizationPermissionMismatch",
			resp.StatusCode, resp.Header.Get("x-ms-error-code"))
	}
}
