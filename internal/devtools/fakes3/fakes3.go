// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Costroid Authors

// Package fakes3 is a development/test-only fake of the two read-only S3
// APIs the aws-focus-s3 connector uses — ListObjectsV2 and GetObject —
// served path-style over an http.Handler backed by a local directory
// tree. It exists so the connector and the CLI can be verified fully
// offline; it is NOT product surface and must never ship in a release
// code path.
//
// The handler's directory layout is <dir>/<bucket>/<key...>: the first
// path segment of a request selects the bucket subdirectory. ETags are
// S3-shaped and content-derived — the quoted MD5 hex of the object bytes
// — in both ListObjectsV2 and GetObject, so overwriting a fixture file
// changes its ETag exactly as a real S3 overwrite delivery would (the
// restatement/idempotency proofs depend on this). GetObject honors
// If-Match with 412 PreconditionFailed on mismatch, as real S3 does.
//
// GET / returns a plain 200 so scripts can readiness-check the endpoint.
// Everything is stdlib-only by design.
package fakes3

import (
	"crypto/md5" //nolint:gosec // S3 ETags are MD5 by definition; not used for security.
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
	"time"
)

// Handler serves a directory tree as a fake S3 endpoint.
type Handler struct {
	dir       string
	forbidden []string
}

// New returns a Handler serving <dir>/<bucket>/<key...>.
func New(dir string) *Handler {
	return &Handler{dir: dir}
}

// Forbid makes every request whose "<bucket>/<key-or-list-prefix>" starts
// with prefix fail with 403 AccessDenied — a test hook for exercising
// missing-permission error paths.
func (h *Handler) Forbid(prefix string) {
	h.forbidden = append(h.forbidden, strings.Trim(prefix, "/"))
}

// ServeHTTP implements http.Handler.
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	bucket, key := splitPath(r.URL.Path)
	if bucket == "" {
		// Not an S3 API: a plain readiness endpoint for scripts.
		w.WriteHeader(http.StatusOK)
		_, _ = fmt.Fprintln(w, "fakes3 ok")
		return
	}
	if strings.Contains(key, "..") {
		writeError(w, http.StatusBadRequest, "InvalidRequest", "key must not contain dot segments")
		return
	}

	if r.URL.Query().Get("list-type") == "2" || (key == "" && r.URL.Query().Has("list-type")) {
		h.listObjectsV2(w, r, bucket)
		return
	}
	if key == "" {
		writeError(w, http.StatusBadRequest, "InvalidRequest", "only ListObjectsV2 and GetObject are implemented")
		return
	}
	h.getObject(w, r, bucket, key)
}

func splitPath(p string) (bucket, key string) {
	p = strings.TrimPrefix(path.Clean("/"+p), "/")
	if p == "" || p == "." {
		return "", ""
	}
	bucket, key, _ = strings.Cut(p, "/")
	return bucket, key
}

func (h *Handler) isForbidden(bucket, keyOrPrefix string) bool {
	full := bucket + "/" + keyOrPrefix
	for _, p := range h.forbidden {
		if strings.HasPrefix(full, p) {
			return true
		}
	}
	return false
}

// listBucketResult mirrors the S3 ListObjectsV2 response document.
type listBucketResult struct {
	XMLName               xml.Name  `xml:"http://s3.amazonaws.com/doc/2006-03-01/ ListBucketResult"`
	Name                  string    `xml:"Name"`
	Prefix                string    `xml:"Prefix"`
	KeyCount              int       `xml:"KeyCount"`
	MaxKeys               int       `xml:"MaxKeys"`
	IsTruncated           bool      `xml:"IsTruncated"`
	NextContinuationToken string    `xml:"NextContinuationToken,omitempty"`
	Contents              []content `xml:"Contents"`
}

type content struct {
	Key          string `xml:"Key"`
	LastModified string `xml:"LastModified"`
	ETag         string `xml:"ETag"`
	Size         int64  `xml:"Size"`
	StorageClass string `xml:"StorageClass"`
}

func (h *Handler) listObjectsV2(w http.ResponseWriter, r *http.Request, bucket string) {
	q := r.URL.Query()
	prefix := q.Get("prefix")
	if h.isForbidden(bucket, prefix) {
		writeError(w, http.StatusForbidden, "AccessDenied", "Access Denied")
		return
	}
	root := filepath.Join(h.dir, bucket)
	if info, err := os.Stat(root); err != nil || !info.IsDir() {
		writeError(w, http.StatusNotFound, "NoSuchBucket", "The specified bucket does not exist")
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

	// Continuation: the token is the last key of the previous page;
	// start-after has the same strictly-after semantics.
	after := q.Get("continuation-token")
	if after == "" {
		after = q.Get("start-after")
	}
	if after != "" {
		i := sort.SearchStrings(keys, after)
		if i < len(keys) && keys[i] == after {
			i++
		}
		keys = keys[i:]
	}
	maxKeys := 1000
	if mk := q.Get("max-keys"); mk != "" {
		if n, err := strconv.Atoi(mk); err == nil && n >= 0 {
			maxKeys = n
		}
	}
	result := listBucketResult{Name: bucket, Prefix: prefix, MaxKeys: maxKeys}
	if len(keys) > maxKeys {
		keys, result.IsTruncated = keys[:maxKeys], true
		result.NextContinuationToken = keys[len(keys)-1]
	}
	for _, key := range keys {
		body, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(key)))
		if err != nil {
			writeError(w, http.StatusInternalServerError, "InternalError", err.Error())
			return
		}
		result.Contents = append(result.Contents, content{
			Key:          key,
			LastModified: modTime(filepath.Join(root, filepath.FromSlash(key))),
			ETag:         etag(body),
			Size:         int64(len(body)),
			StorageClass: "STANDARD",
		})
	}
	result.KeyCount = len(result.Contents)
	writeXML(w, http.StatusOK, result)
}

func (h *Handler) getObject(w http.ResponseWriter, r *http.Request, bucket, key string) {
	if h.isForbidden(bucket, key) {
		writeError(w, http.StatusForbidden, "AccessDenied", "Access Denied")
		return
	}
	if info, err := os.Stat(filepath.Join(h.dir, bucket)); err != nil || !info.IsDir() {
		writeError(w, http.StatusNotFound, "NoSuchBucket", "The specified bucket does not exist")
		return
	}
	p := filepath.Join(h.dir, bucket, filepath.FromSlash(key))
	body, err := os.ReadFile(p)
	if err != nil {
		writeError(w, http.StatusNotFound, "NoSuchKey", "The specified key does not exist.")
		return
	}
	tag := etag(body)
	if match := r.Header.Get("If-Match"); match != "" && match != tag {
		writeError(w, http.StatusPreconditionFailed, "PreconditionFailed",
			"At least one of the pre-conditions you specified did not hold")
		return
	}
	w.Header().Set("ETag", tag)
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Length", strconv.Itoa(len(body)))
	w.Header().Set("Last-Modified", time.Now().UTC().Format(http.TimeFormat))
	w.WriteHeader(http.StatusOK)
	if r.Method != http.MethodHead {
		_, _ = w.Write(body)
	}
}

// errorResult mirrors the S3 error document.
type errorResult struct {
	XMLName xml.Name `xml:"Error"`
	Code    string   `xml:"Code"`
	Message string   `xml:"Message"`
}

func writeError(w http.ResponseWriter, status int, code, message string) {
	writeXML(w, status, errorResult{Code: code, Message: message})
}

func writeXML(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/xml")
	w.WriteHeader(status)
	_, _ = w.Write([]byte(xml.Header))
	_ = xml.NewEncoder(w).Encode(v)
}

// etag is the S3-shaped content ETag: quoted MD5 hex of the object bytes.
func etag(body []byte) string {
	return fmt.Sprintf("%q", fmt.Sprintf("%x", md5.Sum(body))) //nolint:gosec // see package doc
}

func modTime(p string) string {
	info, err := os.Stat(p)
	if err != nil {
		return time.Unix(0, 0).UTC().Format("2006-01-02T15:04:05.000Z")
	}
	return info.ModTime().UTC().Format("2006-01-02T15:04:05.000Z")
}
