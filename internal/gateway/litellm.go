package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/Costroid/costroid/internal/decimal"
)

const (
	maxAnswerBytes = 1 << 20
	dailyWindow    = "1d"
	storeConnected = "connected"
)

// ErrNotFound is a gateway's answer that the key, team or user is absent.
var ErrNotFound = errors.New("gateway: not found")

// Client speaks LiteLLM's proxy API. It never logs a key: errors name the
// route and the status, and quote no request.
type Client struct {
	URL  string
	HTTP *http.Client
}

// NewClient trims the URL and sets a timeout per call.
func NewClient(base string) *Client {
	return &Client{URL: strings.TrimRight(base, "/"), HTTP: &http.Client{Timeout: 30 * time.Second}}
}

// KeySpec is one run's key as the gateway registers it.
type KeySpec struct {
	Key         string
	Alias       string
	MaxBudget   decimal.Decimal
	Lifetime    time.Duration
	MaxParallel int
	Metadata    map[string]string
	Team        string
	User        string
}

// KeyInfo is what the gateway says about a key.
type KeyInfo struct {
	MaxBudget   *json.Number   `json:"max_budget"`
	Spend       json.Number    `json:"spend"`
	Expires     *time.Time     `json:"expires"`
	MaxParallel *int           `json:"max_parallel_requests"`
	Metadata    map[string]any `json:"metadata"`
	Team        *string        `json:"team_id"`
	User        *string        `json:"user_id"`
	Status      string         `json:"status"`
}

// Readiness is the gateway's public health answer.
type Readiness struct {
	HTTPStatus int
	Store      string
}

// Generate registers a key the caller drew.
func (c *Client) Generate(ctx context.Context, adminKey string, spec KeySpec) error {
	body := map[string]any{
		"key":                   spec.Key,
		"key_alias":             spec.Alias,
		"max_budget":            json.Number(spec.MaxBudget.String()),
		"duration":              fmt.Sprintf("%ds", int64(spec.Lifetime/time.Second)),
		"max_parallel_requests": spec.MaxParallel,
		"metadata":              spec.Metadata,
	}
	if spec.Team != "" {
		body["team_id"] = spec.Team
	}
	if spec.User != "" {
		body["user_id"] = spec.User
	}
	return c.call(ctx, http.MethodPost, "/key/generate", adminKey, body, nil)
}

// Info reads a key. Bearer is the key itself, or the admin key with id naming
// the key by its id.
func (c *Client) Info(ctx context.Context, bearer, id string) (KeyInfo, error) {
	route := "/key/info"
	if id != "" {
		route += "?key=" + url.QueryEscape(id)
	}
	var answer struct {
		Info KeyInfo `json:"info"`
	}
	err := c.call(ctx, http.MethodGet, route, bearer, nil, &answer)
	return answer.Info, err
}

// Delete deletes the keys with the alias. A key already gone is not an error.
func (c *Client) Delete(ctx context.Context, adminKey, alias string) (bool, error) {
	var answer struct {
		Deleted []string `json:"deleted_keys"`
	}
	err := c.call(ctx, http.MethodPost, "/key/delete", adminKey, map[string]any{"key_aliases": []string{alias}}, &answer)
	if errors.Is(err, ErrNotFound) {
		return false, nil
	}
	return len(answer.Deleted) > 0, err
}

// Readiness asks the gateway's public health route, which needs no key.
func (c *Client) Readiness(ctx context.Context) (Readiness, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, c.URL+"/health/readiness", nil)
	if err != nil {
		return Readiness{}, fmt.Errorf("gateway: %w", err)
	}
	response, err := c.HTTP.Do(request)
	if err != nil {
		return Readiness{}, fmt.Errorf("gateway: GET /health/readiness: %w", err)
	}
	defer response.Body.Close()
	var answer struct {
		DB string `json:"db"`
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, maxAnswerBytes)).Decode(&answer); err != nil {
		return Readiness{HTTPStatus: response.StatusCode}, nil
	}
	return Readiness{HTTPStatus: response.StatusCode, Store: answer.DB}, nil
}

// EnsureDailyBudget creates a team or a user with a daily budget, or sets the
// budget of one that exists. Kind is team or user.
func (c *Client) EnsureDailyBudget(ctx context.Context, adminKey, kind, id string, budget decimal.Decimal) error {
	field := kind + "_id"
	err := c.call(ctx, http.MethodGet, "/"+kind+"/info?"+field+"="+url.QueryEscape(id), adminKey, nil, nil)
	route := "/" + kind + "/update"
	switch {
	case errors.Is(err, ErrNotFound):
		route = "/" + kind + "/new"
	case err != nil:
		return err
	}
	body := map[string]any{field: id, "max_budget": json.Number(budget.String()), "budget_duration": dailyWindow}
	return c.call(ctx, http.MethodPost, route, adminKey, body, nil)
}

func (c *Client) call(ctx context.Context, method, route, bearer string, body, answer any) error {
	var payload io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("gateway: encode %s: %w", route, err)
		}
		payload = bytes.NewReader(data)
	}
	request, err := http.NewRequestWithContext(ctx, method, c.URL+route, payload)
	if err != nil {
		return fmt.Errorf("gateway: %w", err)
	}
	request.Header.Set("Authorization", "Bearer "+bearer)
	request.Header.Set("Content-Type", "application/json")
	response, err := c.HTTP.Do(request)
	if err != nil {
		return fmt.Errorf("gateway: %s %s: %w", method, routeName(route), err)
	}
	defer response.Body.Close()
	data, err := io.ReadAll(io.LimitReader(response.Body, maxAnswerBytes))
	if err != nil {
		return fmt.Errorf("gateway: read %s: %w", routeName(route), err)
	}
	if response.StatusCode == http.StatusNotFound || notFound(data) {
		return fmt.Errorf("%w: %s %s", ErrNotFound, method, routeName(route))
	}
	if response.StatusCode/100 != 2 {
		return fmt.Errorf("gateway: %s %s answered %d", method, routeName(route), response.StatusCode)
	}
	if answer == nil {
		return nil
	}
	if err := json.Unmarshal(data, answer); err != nil {
		return fmt.Errorf("gateway: parse %s: %w", routeName(route), err)
	}
	return nil
}

// notFound reads LiteLLM's error body, which can carry 404 in its code under
// another HTTP status.
func notFound(data []byte) bool {
	var answer struct {
		Error *struct {
			Code json.RawMessage `json:"code"`
		} `json:"error"`
	}
	if json.Unmarshal(data, &answer) != nil || answer.Error == nil {
		return false
	}
	return strings.Trim(string(answer.Error.Code), `"`) == "404"
}

// routeName drops the query, which can hold a key id.
func routeName(route string) string {
	name, _, _ := strings.Cut(route, "?")
	return name
}
