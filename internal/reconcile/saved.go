package reconcile

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
)

// savedReportMode keeps a saved report readable by its owner alone, because an
// organization's report names every key in it.
const savedReportMode = 0o600

// SavedReportFiles names the file that keeps each provider path's response.
// `costroid reconcile --reports` reads a directory of these files.
var SavedReportFiles = map[string]string{
	"/v1/organizations/analytics/usage":  "anthropic-usage.json",
	"/v1/organizations/analytics/cost":   "anthropic-cost.json",
	"/v1/organization/usage/completions": "openai-usage.json",
	"/v1/organization/costs":             "openai-costs.json",
}

// Recorder sends each request through Client and keeps the response in
// Directory, so the report can be reconciled again with no key. A saved report
// is one response, so a second request for one file is refused: that report
// has more than one page or spans more than one provider window.
type Recorder struct {
	Client    Doer
	Directory string
	kept      map[string]bool
}

// Do forwards one request and keeps its body. A refused request keeps nothing,
// and its status reaches the fetcher as it came.
func (r *Recorder) Do(request *http.Request) (*http.Response, error) {
	name, known := SavedReportFiles[request.URL.Path]
	if !known {
		return nil, fmt.Errorf("no saved report keeps %s", request.URL.Path)
	}
	if r.kept[name] {
		return nil, fmt.Errorf("%s keeps one response, and %s answered more than one", name, request.URL.Path)
	}
	response, err := r.Client.Do(request)
	if err != nil {
		return nil, err
	}
	if response.StatusCode != http.StatusOK {
		return response, nil
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, maxResponse))
	if err = errors.Join(err, response.Body.Close()); err != nil {
		return nil, fmt.Errorf("%s: read: %w", request.URL.Path, err)
	}
	if err := os.WriteFile(filepath.Join(r.Directory, name), body, savedReportMode); err != nil {
		return nil, fmt.Errorf("keep %s: %w", name, err)
	}
	if r.kept == nil {
		r.kept = map[string]bool{}
	}
	r.kept[name] = true
	response.Body = io.NopCloser(bytes.NewReader(body))
	return response, nil
}
