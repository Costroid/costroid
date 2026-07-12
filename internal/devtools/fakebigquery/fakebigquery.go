// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Costroid Authors

// Package fakebigquery is a development/test-only HTTP fake for the
// gcp-focus-bq connector's OAuth token exchange and BigQuery v2 calls. It is
// backed by <dir>/<YYYY-MM>.json files whose rows use the real v2
// {"rows":[{"f":[{"v":...}]}]} envelope in the connector's explicit column
// order. Replacing a month file immediately changes tables.get metadata and the
// aggregate change token; both are derived from fixture rows, never duplicated
// hand-authored metadata.
//
// The handler implements POST /token, tables.get, jobs.query, and
// jobs.getQueryResults. It rejects missing location/useInt64Timestamp, SELECT
// star, and any per-period SELECT set that differs from the pinned connector
// list. PageSize is enforced by the fake regardless of maxResults, and
// DelayedMonth returns jobComplete=false until its first poll. It is never a
// product code path.
package fakebigquery

import (
	"crypto"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/Costroid/costroid/internal/ingest/gcpfocusbq"
)

// Handler serves one fixture directory.
type Handler struct {
	dir string

	PageSize     int
	DelayedMonth string
	FailMonth    string
	LogWriter    io.Writer

	// SchemaAdditions and MissingColumn are test hooks for the Preview drift
	// probe. Normal metadata remains wholly derived from fixture rows + the
	// connector's pinned schema.
	SchemaAdditions []string
	MissingColumn   string

	mu      sync.Mutex
	public  map[string]*rsa.PublicKey
	tokens  map[string]bool
	issuers []string
	calls   []string
}

// New returns a fake with enforced two-row pages and June delayed until poll.
func New(dir string) *Handler {
	return &Handler{dir: dir, PageSize: 2, DelayedMonth: "2026-06", public: map[string]*rsa.PublicKey{}, tokens: map[string]bool{}}
}

// AllowServiceAccount registers a runtime-generated test key.
func (h *Handler) AllowServiceAccount(email string, key *rsa.PublicKey) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.public[email] = key
}

func (h *Handler) Calls() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]string(nil), h.calls...)
}

func (h *Handler) Issuers() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]string(nil), h.issuers...)
}

func (h *Handler) log(label string) {
	h.mu.Lock()
	h.calls = append(h.calls, label)
	h.mu.Unlock()
	if h.LogWriter != nil {
		_, _ = fmt.Fprintln(h.LogWriter, "fakebigquery:", label)
	}
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch {
	case r.Method == http.MethodPost && r.URL.Path == "/token":
		h.token(w, r)
	case r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/datasets/") && strings.Contains(r.URL.Path, "/tables/"):
		if !h.authorized(w, r) {
			return
		}
		h.log("tables.get")
		h.table(w)
	case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/queries"):
		if !h.authorized(w, r) {
			return
		}
		h.query(w, r)
	case r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/queries/"):
		if !h.authorized(w, r) {
			return
		}
		h.queryResults(w, r)
	default:
		http.Error(w, "fakebigquery: endpoint not implemented", http.StatusNotFound)
	}
}

func (h *Handler) token(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid form", http.StatusBadRequest)
		return
	}
	if r.Form.Get("grant_type") != "urn:ietf:params:oauth:grant-type:jwt-bearer" || len(r.Form["assertion"]) != 1 {
		http.Error(w, "grant_type and exactly one assertion are required", http.StatusBadRequest)
		return
	}
	assertion := r.Form.Get("assertion")
	parts := strings.Split(assertion, ".")
	if len(parts) != 3 {
		http.Error(w, "assertion is not a compact JWT", http.StatusBadRequest)
		return
	}
	var header struct {
		Alg string `json:"alg"`
	}
	var claims struct {
		Issuer   string `json:"iss"`
		Scope    string `json:"scope"`
		Audience string `json:"aud"`
		IssuedAt int64  `json:"iat"`
		Expires  int64  `json:"exp"`
	}
	if decodePart(parts[0], &header) != nil || decodePart(parts[1], &claims) != nil || header.Alg != "RS256" {
		http.Error(w, "JWT header/claims invalid", http.StatusBadRequest)
		return
	}
	if claims.Scope != gcpfocusbq.BigQueryScope {
		http.Error(w, "JWT scope must equal "+gcpfocusbq.BigQueryScope, http.StatusBadRequest)
		return
	}
	if claims.Audience != "http://"+r.Host+r.URL.Path {
		http.Error(w, "JWT audience must equal the token URL", http.StatusBadRequest)
		return
	}
	if claims.Expires <= claims.IssuedAt || claims.Expires-claims.IssuedAt > 3600 {
		http.Error(w, "JWT lifetime must be positive and at most one hour", http.StatusBadRequest)
		return
	}
	h.mu.Lock()
	key := h.public[claims.Issuer]
	h.mu.Unlock()
	if key == nil {
		http.Error(w, "unknown service-account issuer", http.StatusUnauthorized)
		return
	}
	sig, err := base64.RawURLEncoding.DecodeString(parts[2])
	digest := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	if err != nil || rsa.VerifyPKCS1v15(key, crypto.SHA256, digest[:], sig) != nil {
		http.Error(w, "JWT signature invalid", http.StatusUnauthorized)
		return
	}
	token := "fake-token:" + claims.Issuer
	h.mu.Lock()
	h.tokens[token] = true
	h.issuers = append(h.issuers, claims.Issuer)
	h.mu.Unlock()
	h.log("token iss=" + claims.Issuer)
	writeJSON(w, http.StatusOK, map[string]any{"access_token": token, "expires_in": 3600, "token_type": "Bearer"})
}

func decodePart(raw string, out any) error {
	body, err := base64.RawURLEncoding.DecodeString(raw)
	if err != nil {
		return err
	}
	return json.Unmarshal(body, out)
}

func (h *Handler) authorized(w http.ResponseWriter, r *http.Request) bool {
	token := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	h.mu.Lock()
	ok := h.tokens[token]
	h.mu.Unlock()
	if !ok || token == "" {
		http.Error(w, "invalid bearer token", http.StatusUnauthorized)
		return false
	}
	return true
}

type fixtureResponse struct {
	Schema fixtureSchema `json:"schema"`
	Rows   []fixtureRow  `json:"rows"`
}

type fixtureSchema struct {
	Fields []fixtureSchemaField `json:"fields"`
}

type fixtureSchemaField struct {
	Name   string               `json:"name"`
	Type   string               `json:"type"`
	Mode   string               `json:"mode"`
	Fields []fixtureSchemaField `json:"fields,omitempty"`
}

type fixtureRow struct {
	F []fixtureCell `json:"f"`
}

type fixtureCell struct {
	V json.RawMessage `json:"v"`
}

func (h *Handler) months() (map[string][]fixtureRow, error) {
	paths, err := filepath.Glob(filepath.Join(h.dir, "????-??.json"))
	if err != nil {
		return nil, err
	}
	sort.Strings(paths)
	out := make(map[string][]fixtureRow, len(paths))
	for _, p := range paths {
		body, err := os.ReadFile(p)
		if err != nil {
			return nil, err
		}
		var response fixtureResponse
		if err := json.Unmarshal(body, &response); err != nil {
			return nil, fmt.Errorf("fixture %s is not a BigQuery rows envelope: %w", filepath.Base(p), err)
		}
		if !schemaNamesEqualPinned(response.Schema.Fields) {
			return nil, fmt.Errorf("fixture %s schema does not set-equal the connector's pinned columns", filepath.Base(p))
		}
		month := strings.TrimSuffix(filepath.Base(p), ".json")
		for i, row := range response.Rows {
			if len(row.F) != len(gcpfocusbq.PinnedFields) {
				return nil, fmt.Errorf("fixture %s row %d has %d cells, want %d", filepath.Base(p), i+1, len(row.F), len(gcpfocusbq.PinnedFields))
			}
		}
		out[month] = response.Rows
	}
	return out, nil
}

func (h *Handler) schema() (fixtureSchema, error) {
	paths, err := filepath.Glob(filepath.Join(h.dir, "????-??.json"))
	if err != nil || len(paths) == 0 {
		return fixtureSchema{}, errors.New("fixture directory has no YYYY-MM.json row envelope")
	}
	sort.Strings(paths)
	body, err := os.ReadFile(paths[0])
	if err != nil {
		return fixtureSchema{}, err
	}
	var response fixtureResponse
	if err := json.Unmarshal(body, &response); err != nil {
		return fixtureSchema{}, err
	}
	if !schemaNamesEqualPinned(response.Schema.Fields) {
		return fixtureSchema{}, errors.New("fixture schema does not set-equal the connector's pinned columns")
	}
	return response.Schema, nil
}

func schemaNamesEqualPinned(fields []fixtureSchemaField) bool {
	got := make([]string, len(fields))
	for i, field := range fields {
		got[i] = field.Name
	}
	want := gcpfocusbq.PinnedColumnNames()
	slices.Sort(got)
	slices.Sort(want)
	return slices.Equal(got, want)
}

func (h *Handler) table(w http.ResponseWriter) {
	months, err := h.months()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	schema, err := h.schema()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	var rows int64
	hash := sha256.New()
	for _, month := range sortedKeys(months) {
		for _, row := range months[month] {
			rows++
			body, _ := json.Marshal(row)
			_, _ = hash.Write(body)
		}
	}
	sum := hash.Sum(nil)
	// A timestamp-shaped, content-derived change token. It changes whenever
	// fixture rows change, like fakeblob's digest-derived ETag, while remaining
	// a valid epoch-millisecond string for the real tables.get shape.
	modifiedMillis := int64(1_700_000_000_000 + binary.BigEndian.Uint64(sum[:8])%100_000_000_000)
	fields := make([]fixtureSchemaField, 0, len(schema.Fields)+len(h.SchemaAdditions))
	for _, field := range schema.Fields {
		if field.Name == h.MissingColumn {
			continue
		}
		fields = append(fields, field)
	}
	for _, name := range h.SchemaAdditions {
		fields = append(fields, fixtureSchemaField{Name: name, Type: "STRING", Mode: "NULLABLE"})
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"lastModifiedTime": strconv.FormatInt(modifiedMillis, 10),
		"numRows":          strconv.FormatInt(rows, 10),
		"schema":           fixtureSchema{Fields: fields},
	})
}

type requestShape struct {
	Query         string `json:"query"`
	Location      string `json:"location"`
	MaxResults    int    `json:"maxResults"`
	FormatOptions struct {
		UseInt64Timestamp bool `json:"useInt64Timestamp"`
	} `json:"formatOptions"`
}

func (h *Handler) query(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(io.LimitReader(r.Body, 8<<20))
	if err != nil {
		http.Error(w, "reading request", http.StatusBadRequest)
		return
	}
	var raw map[string]json.RawMessage
	var request requestShape
	if json.Unmarshal(body, &raw) != nil || json.Unmarshal(body, &request) != nil {
		http.Error(w, "malformed query request", http.StatusBadRequest)
		return
	}
	var legacy bool
	legacyRaw, legacyPresent := raw["useLegacySql"]
	if !legacyPresent || json.Unmarshal(legacyRaw, &legacy) != nil || legacy {
		http.Error(w, "useLegacySql must be present and false", http.StatusBadRequest)
		return
	}
	if request.Location == "" || !request.FormatOptions.UseInt64Timestamp {
		http.Error(w, "location and formatOptions.useInt64Timestamp=true are required", http.StatusBadRequest)
		return
	}
	if strings.Contains(strings.ToUpper(request.Query), "SELECT"+" *") {
		http.Error(w, "SELECT star is forbidden", http.StatusBadRequest)
		return
	}
	if strings.Contains(request.Query, "MAX(x_ExportTime)") && strings.Contains(request.Query, "GROUP BY") {
		h.log("jobs.query aggregate")
		h.aggregate(w, request.Location)
		return
	}
	if !exactSelectSet(request.Query) {
		http.Error(w, "per-period SELECT columns must set-equal the pinned schema", http.StatusBadRequest)
		return
	}
	month := monthFromQuery(request.Query)
	if month == "" {
		http.Error(w, "per-period query is missing an inline month timestamp", http.StatusBadRequest)
		return
	}
	h.log("jobs.query period=" + month)
	if h.FailMonth == month {
		http.Error(w, "fixture-injected month failure", http.StatusInternalServerError)
		return
	}
	if h.DelayedMonth == month {
		writeJSON(w, http.StatusOK, map[string]any{
			"jobComplete":  false,
			"jobReference": map[string]any{"projectId": "job-project", "jobId": "period-" + month, "location": request.Location},
		})
		return
	}
	h.page(w, month, 0, request.Location)
}

func (h *Handler) aggregate(w http.ResponseWriter, location string) {
	months, err := h.months()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	rows := make([]fixtureRow, 0, len(months))
	for _, month := range sortedKeys(months) {
		var maxRaw string
		for _, row := range months[month] {
			raw := scalar(row.F[fieldIndex("x_ExportTime")].V)
			if raw != "" && (maxRaw == "" || decimalIntegerLess(maxRaw, raw)) {
				maxRaw = raw
			}
		}
		rows = append(rows, fixtureRow{F: []fixtureCell{stringCell(month), nullableStringCell(maxRaw), stringCell(strconv.Itoa(len(months[month])))}})
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"jobComplete": true, "rows": rows,
		"jobReference": map[string]any{"projectId": "job-project", "jobId": "aggregate", "location": location},
	})
}

func decimalIntegerLess(a, b string) bool {
	ai, aerr := strconv.ParseInt(a, 10, 64)
	bi, berr := strconv.ParseInt(b, 10, 64)
	return aerr == nil && berr == nil && ai < bi
}

func (h *Handler) queryResults(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	if q.Get("location") == "" || q.Get("formatOptions.useInt64Timestamp") != "true" {
		http.Error(w, "poll requires location and formatOptions.useInt64Timestamp=true", http.StatusBadRequest)
		return
	}
	jobID := pathTail(r.URL.Path)
	if !strings.HasPrefix(jobID, "period-") {
		http.Error(w, "unknown query job", http.StatusNotFound)
		return
	}
	month := strings.TrimPrefix(jobID, "period-")
	offset := 0
	if token := q.Get("pageToken"); token != "" {
		offset, _ = strconv.Atoi(token)
	}
	h.log(fmt.Sprintf("jobs.getQueryResults period=%s offset=%d", month, offset))
	h.page(w, month, offset, q.Get("location"))
}

func (h *Handler) page(w http.ResponseWriter, month string, offset int, location string) {
	months, err := h.months()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	rows, ok := months[month]
	if !ok {
		http.Error(w, "fixture month not found", http.StatusNotFound)
		return
	}
	size := h.PageSize
	if size <= 0 {
		size = 2
	}
	if offset > len(rows) {
		offset = len(rows)
	}
	end := min(offset+size, len(rows))
	response := map[string]any{
		"jobComplete":  true,
		"rows":         rows[offset:end],
		"jobReference": map[string]any{"projectId": "job-project", "jobId": "period-" + month, "location": location},
	}
	if schema, err := h.schema(); err == nil {
		response["schema"] = schema
	}
	if end < len(rows) {
		response["pageToken"] = strconv.Itoa(end)
	}
	writeJSON(w, http.StatusOK, response)
}

func exactSelectSet(query string) bool {
	upper := strings.ToUpper(query)
	start, end := strings.Index(upper, "SELECT "), strings.Index(upper, " FROM ")
	if start < 0 || end <= start {
		return false
	}
	parts := strings.Split(query[start+len("SELECT "):end], ",")
	got := make([]string, 0, len(parts))
	for _, part := range parts {
		got = append(got, strings.Trim(strings.TrimSpace(part), "`"))
	}
	want := gcpfocusbq.PinnedColumnNames()
	slices.Sort(got)
	slices.Sort(want)
	return slices.Equal(got, want)
}

var inlineMonth = regexp.MustCompile(`TIMESTAMP\('(\d{4}-\d{2})-01T00:00:00Z'\)`)

func monthFromQuery(query string) string {
	match := inlineMonth.FindStringSubmatch(query)
	if len(match) != 2 {
		return ""
	}
	return match[1]
}

func fieldIndex(name string) int {
	for i, field := range gcpfocusbq.PinnedFields {
		if field.Name == name {
			return i
		}
	}
	panic("unknown pinned field " + name)
}

func scalar(raw json.RawMessage) string {
	var value string
	_ = json.Unmarshal(raw, &value)
	return value
}

func stringCell(value string) fixtureCell {
	raw, _ := json.Marshal(value)
	return fixtureCell{V: raw}
}

func nullableStringCell(value string) fixtureCell {
	if value == "" {
		return fixtureCell{V: json.RawMessage("null")}
	}
	return stringCell(value)
}

func sortedKeys[V any](values map[string]V) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func pathTail(value string) string {
	value = strings.TrimRight(value, "/")
	if i := strings.LastIndexByte(value, '/'); i >= 0 {
		return value[i+1:]
	}
	return value
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
