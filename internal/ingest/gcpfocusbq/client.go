// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Costroid Authors

package gcpfocusbq

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"path"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Production endpoints and the one accepted OAuth scope.
const (
	DefaultBaseURL  = "https://bigquery.googleapis.com/bigquery/v2/"
	DefaultTokenURL = "https://oauth2.googleapis.com/token"
	BigQueryScope   = "https://www.googleapis.com/auth/bigquery"
)

type serviceAccount struct {
	Type        string `json:"type"`
	ClientEmail string `json:"client_email"`
	PrivateKey  string `json:"private_key"`
	TokenURI    string `json:"token_uri"`
	privateKey  *rsa.PrivateKey
}

// Client is the small authenticated BigQuery v2 REST surface the connector
// needs. It is safe for sequential discovery/record reads within one CLI run.
type Client struct {
	httpClient *http.Client
	baseURL    *url.URL
	tokenURL   *url.URL
	account    serviceAccount

	mu          sync.Mutex
	accessToken string
	probe       ProbeResult
}

// ProbeResult is the latest tables.get schema/partition probe.
type ProbeResult struct {
	TimePartitioning bool
	AdditiveColumns  []string
}

// NewClient validates both endpoints before parsing the credential and returns
// a client backed by service-account JSON. No error includes credential bytes.
func NewClient(httpClient *http.Client, baseURL, tokenURL string, credentialJSON []byte) (*Client, error) {
	if httpClient == nil {
		return nil, errors.New("BigQuery HTTP client must not be nil")
	}
	base, err := validateEndpoint(baseURL, true)
	if err != nil {
		return nil, fmt.Errorf("invalid --base-url: %w", err)
	}
	token, err := validateEndpoint(tokenURL, false)
	if err != nil {
		return nil, fmt.Errorf("invalid --token-url: %w", err)
	}
	account, err := parseServiceAccount(credentialJSON)
	if err != nil {
		return nil, err
	}
	return &Client{httpClient: httpClient, baseURL: base, tokenURL: token, account: account}, nil
}

func validateEndpoint(raw string, directory bool) (*url.URL, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil || u.Fragment != "" {
		return nil, errors.New("expected an https:// endpoint (http:// is allowed only for a loopback test server)")
	}
	if u.Scheme == "http" && !isLoopback(u.Hostname()) {
		return nil, errors.New("plain HTTP with a non-loopback host is refused before any credential is sent")
	}
	if directory {
		u.Path = strings.TrimRight(u.Path, "/") + "/"
	} else if u.RawQuery != "" {
		return nil, errors.New("token endpoint must not carry query parameters")
	}
	return u, nil
}

func isLoopback(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func parseServiceAccount(raw []byte) (serviceAccount, error) {
	var sa serviceAccount
	if err := json.Unmarshal(raw, &sa); err != nil {
		return serviceAccount{}, errors.New("parsing Google credential JSON: malformed JSON (credential contents are not echoed)")
	}
	if sa.Type != "service_account" {
		return serviceAccount{}, fmt.Errorf("google credential JSON type must be %q; authorized_user and other ADC types are not supported", "service_account")
	}
	if sa.ClientEmail == "" || sa.PrivateKey == "" {
		return serviceAccount{}, errors.New("google service_account JSON must contain client_email and private_key")
	}
	block, _ := pem.Decode([]byte(sa.PrivateKey))
	if block == nil {
		return serviceAccount{}, errors.New("parsing Google service-account private_key: expected a PEM-encoded PKCS#8 RSA key (key contents are not echoed)")
	}
	key, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return serviceAccount{}, errors.New("parsing Google service-account private_key: expected a valid PKCS#8 RSA key (key contents are not echoed)")
	}
	rsaKey, ok := key.(*rsa.PrivateKey)
	if !ok {
		return serviceAccount{}, errors.New("parsing Google service-account private_key: PKCS#8 key is not RSA")
	}
	sa.privateKey = rsaKey
	sa.PrivateKey = ""
	return sa, nil
}

func (c *Client) ProbeResult() ProbeResult {
	c.mu.Lock()
	defer c.mu.Unlock()
	return ProbeResult{TimePartitioning: c.probe.TimePartitioning, AdditiveColumns: append([]string(nil), c.probe.AdditiveColumns...)}
}

func (c *Client) token(ctx context.Context) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.accessToken != "" {
		return c.accessToken, nil
	}
	now := time.Now().UTC()
	header, _ := json.Marshal(map[string]string{"alg": "RS256", "typ": "JWT"})
	claims, _ := json.Marshal(map[string]any{
		"iss":   c.account.ClientEmail,
		"scope": BigQueryScope,
		"aud":   c.tokenURL.String(),
		"iat":   now.Unix(),
		"exp":   now.Add(time.Hour).Unix(),
	})
	unsigned := base64.RawURLEncoding.EncodeToString(header) + "." + base64.RawURLEncoding.EncodeToString(claims)
	digest := sha256.Sum256([]byte(unsigned))
	sig, err := rsa.SignPKCS1v15(rand.Reader, c.account.privateKey, crypto.SHA256, digest[:])
	if err != nil {
		return "", errors.New("signing Google service-account JWT failed")
	}
	assertion := unsigned + "." + base64.RawURLEncoding.EncodeToString(sig)
	form := url.Values{
		"grant_type": {"urn:ietf:params:oauth:grant-type:jwt-bearer"},
		"assertion":  {assertion},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.tokenURL.String(), strings.NewReader(form.Encode()))
	if err != nil {
		return "", errors.New("building Google token request failed")
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("exchanging Google service-account JWT: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20))
		return "", fmt.Errorf("google token endpoint returned HTTP %d (credential contents are not echoed)", resp.StatusCode)
	}
	var body struct {
		AccessToken string `json:"access_token"`
		TokenType   string `json:"token_type"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&body); err != nil || body.AccessToken == "" {
		return "", errors.New("google token endpoint returned a malformed response without access_token")
	}
	if body.TokenType != "" && !strings.EqualFold(body.TokenType, "Bearer") {
		return "", fmt.Errorf("google token endpoint returned unsupported token_type %q", body.TokenType)
	}
	c.accessToken = body.AccessToken
	return c.accessToken, nil
}

func (c *Client) doJSON(ctx context.Context, method, rawURL string, requestBody, responseBody any) error {
	var body io.Reader
	if requestBody != nil {
		encoded, err := json.Marshal(requestBody)
		if err != nil {
			return fmt.Errorf("encoding BigQuery request: %w", err)
		}
		body = strings.NewReader(string(encoded))
	}
	req, err := http.NewRequestWithContext(ctx, method, rawURL, body)
	if err != nil {
		return fmt.Errorf("building BigQuery request: %w", err)
	}
	token, err := c.token(ctx)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	if requestBody != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("calling BigQuery: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20))
		return fmt.Errorf("BigQuery returned HTTP %d for %s", resp.StatusCode, req.URL.Path)
	}
	if responseBody == nil {
		return nil
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 64<<20)).Decode(responseBody); err != nil {
		return fmt.Errorf("decoding BigQuery response: %w", err)
	}
	return nil
}

func (c *Client) endpoint(parts ...string) string {
	u := *c.baseURL
	joined := u.Path
	for _, p := range parts {
		joined = path.Join(joined, url.PathEscape(p))
	}
	u.Path = joined
	return u.String()
}

type tableResponse struct {
	LastModifiedTime string `json:"lastModifiedTime"`
	NumRows          string `json:"numRows"`
	Schema           struct {
		Fields []struct {
			Name string `json:"name"`
		} `json:"fields"`
	} `json:"schema"`
	TimePartitioning json.RawMessage `json:"timePartitioning"`
}

func (c *Client) probeTable(ctx context.Context, coords Coordinates) (time.Time, error) {
	var table tableResponse
	if err := c.doJSON(ctx, http.MethodGet,
		c.endpoint("projects", coords.DatasetProject, "datasets", coords.Dataset, "tables", coords.Table), nil, &table); err != nil {
		return time.Time{}, fmt.Errorf("probing BigQuery table metadata: %w", err)
	}
	seen := make(map[string]bool, len(table.Schema.Fields))
	for _, f := range table.Schema.Fields {
		seen[f.Name] = true
	}
	var missing []string
	for _, f := range PinnedFields {
		if !seen[f.Name] {
			missing = append(missing, f.Name)
		}
		delete(seen, f.Name)
	}
	if len(missing) > 0 {
		return time.Time{}, fmt.Errorf("BigQuery FOCUS table schema is missing selected column(s): %s; Google Preview schema may have changed", strings.Join(missing, ", "))
	}
	var additions []string
	for name := range seen {
		additions = append(additions, name)
	}
	slicesSort(additions)
	c.mu.Lock()
	c.probe = ProbeResult{TimePartitioning: len(table.TimePartitioning) > 0 && string(table.TimePartitioning) != "null", AdditiveColumns: additions}
	c.mu.Unlock()
	millis, err := strconv.ParseInt(table.LastModifiedTime, 10, 64)
	if err != nil {
		return time.Time{}, errors.New("BigQuery tables.get returned invalid lastModifiedTime")
	}
	return time.UnixMilli(millis).UTC(), nil
}

func slicesSort(values []string) {
	for i := 1; i < len(values); i++ {
		for j := i; j > 0 && values[j] < values[j-1]; j-- {
			values[j], values[j-1] = values[j-1], values[j]
		}
	}
}

type queryRequest struct {
	Query         string        `json:"query"`
	UseLegacySQL  bool          `json:"useLegacySql"`
	Location      string        `json:"location"`
	MaxResults    int           `json:"maxResults"`
	FormatOptions formatOptions `json:"formatOptions"`
}

type formatOptions struct {
	UseInt64Timestamp bool `json:"useInt64Timestamp"`
}

type bqCell struct {
	V json.RawMessage `json:"v"`
}

type bqRow struct {
	F []bqCell `json:"f"`
}

type queryResponse struct {
	JobComplete  bool    `json:"jobComplete"`
	PageToken    string  `json:"pageToken"`
	Rows         []bqRow `json:"rows"`
	JobReference struct {
		ProjectID string `json:"projectId"`
		JobID     string `json:"jobId"`
		Location  string `json:"location"`
	} `json:"jobReference"`
}

func (c *Client) startQuery(ctx context.Context, coords Coordinates, query string) (queryResponse, error) {
	var resp queryResponse
	err := c.doJSON(ctx, http.MethodPost, c.endpoint("projects", coords.JobProject, "queries"), queryRequest{
		Query: query, UseLegacySQL: false, Location: coords.Location, MaxResults: 10000,
		FormatOptions: formatOptions{UseInt64Timestamp: true},
	}, &resp)
	if err != nil {
		return queryResponse{}, err
	}
	if !resp.JobComplete {
		if resp.JobReference.JobID == "" {
			return queryResponse{}, errors.New("BigQuery returned jobComplete=false without a jobReference")
		}
		return c.pollQueryResults(ctx, coords, resp.JobReference.JobID, "")
	}
	return resp, nil
}

func (c *Client) getQueryResults(ctx context.Context, coords Coordinates, jobID, pageToken string) (queryResponse, error) {
	raw := c.endpoint("projects", coords.JobProject, "queries", jobID)
	u, _ := url.Parse(raw)
	q := u.Query()
	q.Set("location", coords.Location)
	q.Set("formatOptions.useInt64Timestamp", "true")
	q.Set("maxResults", "10000")
	if pageToken != "" {
		q.Set("pageToken", pageToken)
	}
	u.RawQuery = q.Encode()
	var resp queryResponse
	if err := c.doJSON(ctx, http.MethodGet, u.String(), nil, &resp); err != nil {
		return queryResponse{}, err
	}
	return resp, nil
}

// pollQueryResults follows jobComplete=false until BigQuery completes the job
// or the caller's context ends. BigQuery's endpoint may long-poll; no local
// wall-clock sleep or fixed attempt count can incorrectly abandon a valid job.
func (c *Client) pollQueryResults(ctx context.Context, coords Coordinates, jobID, pageToken string) (queryResponse, error) {
	for {
		if err := ctx.Err(); err != nil {
			return queryResponse{}, err
		}
		resp, err := c.getQueryResults(ctx, coords, jobID, pageToken)
		if err != nil {
			return queryResponse{}, err
		}
		if resp.JobComplete {
			return resp, nil
		}
	}
}
