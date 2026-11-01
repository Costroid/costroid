package reconcile

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"
)

const (
	userAgent      = "costroid"
	requestTimeout = 60 * time.Second
	maxResponse    = 64 << 20
	maxPages       = 10000
)

// Doer is the HTTP client a fetcher sends through.
type Doer interface {
	Do(*http.Request) (*http.Response, error)
}

// NewClient returns the client a fetcher uses in production. It follows no
// redirect: Go's client keeps x-api-key across hosts, and an admin key goes to
// the provider's own host or nowhere.
func NewClient() *http.Client {
	return &http.Client{
		Timeout: requestTimeout,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}

// page is the envelope both providers share. Only Anthropic stamps
// data_refreshed_at, and a stamp of null means no data exists yet.
type page[T any] struct {
	Data            []T        `json:"data"`
	HasMore         bool       `json:"has_more"`
	NextPage        *string    `json:"next_page"`
	DataRefreshedAt *time.Time `json:"data_refreshed_at"`
}

// fetch is one GET, with its credentials set by authorize.
type fetch struct {
	client    Doer
	endpoint  string
	authorize func(http.Header)
}

// pages walks a report to its last page.
func pages[T any](ctx context.Context, f fetch, query url.Values, each func(page[T]) error) error {
	for range maxPages {
		var current page[T]
		if err := f.get(ctx, query, &current); err != nil {
			return err
		}
		if err := each(current); err != nil {
			return fmt.Errorf("%s: %w", f.path(), err)
		}
		if !current.HasMore {
			return nil
		}
		if current.NextPage == nil || *current.NextPage == "" {
			return fmt.Errorf("%s: has_more with no next_page", f.path())
		}
		query.Set("page", *current.NextPage)
	}
	return fmt.Errorf("%s: more than %d pages", f.path(), maxPages)
}

func (f fetch) path() string {
	parsed, err := url.Parse(f.endpoint)
	if err != nil {
		return f.endpoint
	}
	return parsed.Path
}

// get never reads a refused body into an error: only the status leaves.
func (f fetch) get(ctx context.Context, query url.Values, into any) (err error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, f.endpoint+"?"+query.Encode(), nil)
	if err != nil {
		return fmt.Errorf("%s: build request: %w", f.path(), err)
	}
	request.Header.Set("User-Agent", userAgent)
	f.authorize(request.Header)
	response, err := f.client.Do(request)
	if err != nil {
		return fmt.Errorf("%s: %w", f.path(), err)
	}
	defer func() {
		err = errors.Join(err, response.Body.Close())
	}()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("%s: status %d", f.path(), response.StatusCode)
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, maxResponse)).Decode(into); err != nil {
		return fmt.Errorf("%s: decode: %w", f.path(), err)
	}
	return nil
}
