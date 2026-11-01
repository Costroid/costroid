package main

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
)

// savedReportsHoldNoKey fills the key a fetcher asks for. Nothing is sent.
const savedReportsHoldNoKey = "saved-reports"

// savedReportFiles names the file that answers each provider path.
var savedReportFiles = map[string]string{
	"/v1/organizations/analytics/usage":  "anthropic-usage.json",
	"/v1/organizations/analytics/cost":   "anthropic-cost.json",
	"/v1/organization/usage/completions": "openai-usage.json",
	"/v1/organization/costs":             "openai-costs.json",
}

// savedReports answers the fetchers from a directory, so the path through
// them is the path a live report takes. Each file is one complete response.
type savedReports struct {
	directory string
	read      map[string]bool
}

// Do answers one request from its file. A second request for one file means the
// range spans more than one provider window, and repeating the file would count
// its records twice.
func (s *savedReports) Do(request *http.Request) (*http.Response, error) {
	name, known := savedReportFiles[request.URL.Path]
	if !known {
		return nil, fmt.Errorf("no saved report answers %s", request.URL.Path)
	}
	if s.read[name] {
		return nil, fmt.Errorf("%s answers one request: narrow --from and --to to one month", name)
	}
	data, err := os.ReadFile(filepath.Join(s.directory, name))
	if err != nil {
		return nil, errors.Join(fmt.Errorf("the saved report %s", name), err)
	}
	if s.read == nil {
		s.read = map[string]bool{}
	}
	s.read[name] = true
	return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(bytes.NewReader(data))}, nil
}
