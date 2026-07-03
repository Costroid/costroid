// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Costroid Authors

// Package fakeblob is a development/test-only fake of the two read-only
// Azure Blob Storage APIs the azure-focus connector uses — List Blobs
// (flat) and Get Blob — served over an http.Handler backed by a local
// directory tree. It exists so the connector and the CLI can be verified
// fully offline; it is NOT product surface and must never ship in a
// release code path.
//
// The handler's directory layout is <dir>/<account>/<container>/<key...>
// and the URL is path-style: the account URL passed to the Azure SDK is
// http://<host>/<account>, so the SDK's container and blob paths land on
// /<account>/<container>/<key...> (azblob accepts any service URL).
// Requests are served anonymously — clients use
// azblob.NewClientWithNoCredential behind the connector's explicit
// test-only escape (COSTROID_AZURE_INSECURE_NO_AUTH=1, http:// only).
//
// ETags are stable per-write change tokens shaped like Azure's
// ("0x<hex>"), derived from the object bytes: real Azure ETags are
// opaque change tokens rather than content digests, and a content
// digest is the simplest token that is stable across restarts of the
// fake while changing exactly when a fixture file is rewritten — which
// is what the restatement and idempotency proofs need. Get Blob honors
// If-Match with 412 ConditionNotMet on mismatch (x-ms-error-code set,
// as the SDK's bloberror matching requires), and serves ranged reads so
// the SDK's RetryReader works against it.
//
// GET / returns a plain 200 so scripts can readiness-check the endpoint.
// Everything is stdlib-only by design.
package fakeblob

import (
	"crypto/sha256"
	"encoding/xml"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// defaultPageSize mirrors the List Blobs service default.
const defaultPageSize = 5000

// Handler serves a directory tree as a fake Azure Blob Storage endpoint.
type Handler struct {
	dir       string
	forbidden []string

	// PageSize caps every listing page (0 means the service default of
	// 5000) — a test hook to force multi-page listings from small
	// fixture trees. Set it before serving requests.
	PageSize int

	mu          sync.Mutex
	getBlobKeys []string
}

// New returns a Handler serving <dir>/<account>/<container>/<key...>.
func New(dir string) *Handler {
	return &Handler{dir: dir}
}

// GetBlobKeys returns the "<account>/<container>/<key>" of every Get
// Blob request served so far, in order — test instrumentation for the
// incremental-sync proofs (an unchanged re-sync must cost ZERO Get Blob
// calls).
func (h *Handler) GetBlobKeys() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]string(nil), h.getBlobKeys...)
}

// Forbid makes every request whose "<account>/<container>/<key-or-list-
// prefix>" starts with prefix fail with 403
// AuthorizationPermissionMismatch — a test hook for exercising
// missing-role error paths.
func (h *Handler) Forbid(prefix string) {
	h.forbidden = append(h.forbidden, strings.Trim(prefix, "/"))
}

// ServeHTTP implements http.Handler.
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	account, container, key := splitPath(r.URL.Path)
	if account == "" {
		// Not a storage API: a plain readiness endpoint for scripts.
		w.WriteHeader(http.StatusOK)
		_, _ = fmt.Fprintln(w, "fakeblob ok")
		return
	}
	if strings.Contains(key, "..") {
		writeError(w, http.StatusBadRequest, "InvalidUri", "key must not contain dot segments")
		return
	}

	if r.URL.Query().Get("comp") == "list" {
		h.listBlobs(w, r, account, container)
		return
	}
	if container == "" || key == "" {
		writeError(w, http.StatusBadRequest, "InvalidUri", "only List Blobs and Get Blob are implemented")
		return
	}
	h.getBlob(w, r, account, container, key)
}

// splitPath splits /<account>/<container>/<key...>.
func splitPath(p string) (account, container, key string) {
	p = strings.TrimPrefix(path.Clean("/"+p), "/")
	if p == "" || p == "." {
		return "", "", ""
	}
	account, rest, _ := strings.Cut(p, "/")
	container, key, _ = strings.Cut(rest, "/")
	return account, container, key
}

func (h *Handler) isForbidden(account, container, keyOrPrefix string) bool {
	full := account + "/" + container + "/" + keyOrPrefix
	for _, p := range h.forbidden {
		if strings.HasPrefix(full, p) {
			return true
		}
	}
	return false
}

// enumerationResults mirrors the List Blobs response document (the
// subset the azblob generated client reads).
type enumerationResults struct {
	XMLName         xml.Name   `xml:"EnumerationResults"`
	ServiceEndpoint string     `xml:"ServiceEndpoint,attr"`
	ContainerName   string     `xml:"ContainerName,attr"`
	Prefix          string     `xml:"Prefix"`
	MaxResults      int        `xml:"MaxResults"`
	Blobs           []blobItem `xml:"Blobs>Blob"`
	NextMarker      string     `xml:"NextMarker,omitempty"`
}

type blobItem struct {
	Name       string         `xml:"Name"`
	Deleted    bool           `xml:"Deleted"`
	Properties blobProperties `xml:"Properties"`
}

type blobProperties struct {
	LastModified  string `xml:"Last-Modified"`
	Etag          string `xml:"Etag"`
	ContentLength int64  `xml:"Content-Length"`
	BlobType      string `xml:"BlobType"`
}

func (h *Handler) listBlobs(w http.ResponseWriter, r *http.Request, account, container string) {
	q := r.URL.Query()
	prefix := q.Get("prefix")
	if h.isForbidden(account, container, prefix) {
		writeError(w, http.StatusForbidden, "AuthorizationPermissionMismatch",
			"This request is not authorized to perform this operation using this permission.")
		return
	}
	root := filepath.Join(h.dir, account, container)
	if info, err := os.Stat(root); err != nil || !info.IsDir() {
		writeError(w, http.StatusNotFound, "ContainerNotFound", "The specified container does not exist.")
		return
	}

	var keys []string
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		rel, err := filepath.Rel(root, p)
		if err != nil {
			return err
		}
		key := filepath.ToSlash(rel)
		if strings.HasPrefix(key, prefix) {
			keys = append(keys, key)
		}
		return nil
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "InternalError", err.Error())
		return
	}
	sort.Strings(keys)

	// Continuation: the marker is the last key of the previous page;
	// listing resumes strictly after it.
	if marker := q.Get("marker"); marker != "" {
		i := sort.SearchStrings(keys, marker)
		if i < len(keys) && keys[i] == marker {
			i++
		}
		keys = keys[i:]
	}
	pageSize := defaultPageSize
	if h.PageSize > 0 {
		pageSize = h.PageSize
	}
	if mr := q.Get("maxresults"); mr != "" {
		if n, err := strconv.Atoi(mr); err == nil && n > 0 && n < pageSize {
			pageSize = n
		}
	}
	result := enumerationResults{
		ServiceEndpoint: "http://" + r.Host + "/" + account,
		ContainerName:   container,
		Prefix:          prefix,
		MaxResults:      pageSize,
	}
	if len(keys) > pageSize {
		keys = keys[:pageSize]
		result.NextMarker = keys[len(keys)-1]
	}
	for _, key := range keys {
		p := filepath.Join(root, filepath.FromSlash(key))
		body, err := os.ReadFile(p)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "InternalError", err.Error())
			return
		}
		result.Blobs = append(result.Blobs, blobItem{
			Name: key,
			Properties: blobProperties{
				LastModified:  modTime(p),
				Etag:          etag(body),
				ContentLength: int64(len(body)),
				BlobType:      "BlockBlob",
			},
		})
	}
	writeXML(w, http.StatusOK, result)
}

func (h *Handler) getBlob(w http.ResponseWriter, r *http.Request, account, container, key string) {
	h.mu.Lock()
	h.getBlobKeys = append(h.getBlobKeys, account+"/"+container+"/"+key)
	h.mu.Unlock()
	if h.isForbidden(account, container, key) {
		writeError(w, http.StatusForbidden, "AuthorizationPermissionMismatch",
			"This request is not authorized to perform this operation using this permission.")
		return
	}
	root := filepath.Join(h.dir, account, container)
	if info, err := os.Stat(root); err != nil || !info.IsDir() {
		writeError(w, http.StatusNotFound, "ContainerNotFound", "The specified container does not exist.")
		return
	}
	p := filepath.Join(root, filepath.FromSlash(key))
	body, err := os.ReadFile(p)
	if err != nil {
		writeError(w, http.StatusNotFound, "BlobNotFound", "The specified blob does not exist.")
		return
	}
	tag := etag(body)
	if match := r.Header.Get("If-Match"); match != "" && trimQuotes(match) != trimQuotes(tag) {
		writeError(w, http.StatusPreconditionFailed, "ConditionNotMet",
			"The condition specified using HTTP conditional header(s) is not met.")
		return
	}

	status := http.StatusOK
	total := int64(len(body))
	// Ranged reads ("bytes=<start>-[<end>]", also sent as x-ms-range):
	// the SDK's RetryReader resumes interrupted downloads with them.
	if rng := firstNonEmpty(r.Header.Get("x-ms-range"), r.Header.Get("Range")); rng != "" {
		start, end, ok := parseRange(rng, total)
		if !ok {
			writeError(w, http.StatusRequestedRangeNotSatisfiable, "InvalidRange",
				"The range specified is invalid for the current size of the resource.")
			return
		}
		w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, end, total))
		body = body[start : end+1]
		status = http.StatusPartialContent
	}

	w.Header().Set("ETag", tag)
	w.Header().Set("Last-Modified", lastModifiedHeader(p))
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Length", strconv.Itoa(len(body)))
	w.Header().Set("Accept-Ranges", "bytes")
	w.Header().Set("x-ms-blob-type", "BlockBlob")
	w.WriteHeader(status)
	if r.Method != http.MethodHead {
		_, _ = w.Write(body)
	}
}

// parseRange parses "bytes=<start>-[<end>]" against a resource of the
// given size, returning the inclusive byte bounds.
func parseRange(rng string, size int64) (start, end int64, ok bool) {
	spec, found := strings.CutPrefix(rng, "bytes=")
	if !found {
		return 0, 0, false
	}
	from, to, found := strings.Cut(spec, "-")
	if !found {
		return 0, 0, false
	}
	start, err := strconv.ParseInt(from, 10, 64)
	if err != nil || start < 0 || start >= size {
		return 0, 0, false
	}
	end = size - 1
	if to != "" {
		end, err = strconv.ParseInt(to, 10, 64)
		if err != nil || end < start {
			return 0, 0, false
		}
		end = min(end, size-1)
	}
	return start, end, true
}

func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

func trimQuotes(s string) string { return strings.Trim(s, `"`) }

// storageError mirrors the storage error document; the load-bearing
// error code travels in the x-ms-error-code header, which is what the
// SDK's bloberror matching reads.
type storageError struct {
	XMLName xml.Name `xml:"Error"`
	Code    string   `xml:"Code"`
	Message string   `xml:"Message"`
}

func writeError(w http.ResponseWriter, status int, code, message string) {
	w.Header().Set("x-ms-error-code", code)
	writeXML(w, status, storageError{Code: code, Message: message})
}

func writeXML(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/xml")
	w.WriteHeader(status)
	_, _ = w.Write([]byte(xml.Header))
	_ = xml.NewEncoder(w).Encode(v)
}

// etag is the fake's stable per-write change token: Azure-shaped
// ("0x<hex>", quoted in headers and If-Match handling), derived from the
// object bytes (see the package documentation).
func etag(body []byte) string {
	sum := sha256.Sum256(body)
	return fmt.Sprintf(`"0x%X"`, sum[:8])
}

// modTime formats a blob's Last-Modified for the listing document
// (RFC 1123, as the real service emits — second precision).
func modTime(p string) string {
	info, err := os.Stat(p)
	if err != nil {
		return time.Unix(0, 0).UTC().Format(http.TimeFormat)
	}
	return info.ModTime().UTC().Format(http.TimeFormat)
}

func lastModifiedHeader(p string) string { return modTime(p) }
