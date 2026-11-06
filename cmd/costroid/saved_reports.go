package main

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"

	"github.com/Costroid/costroid/internal/reconcile"
)

// savedReportsHoldNoKey fills the key a fetcher asks for. Nothing is sent.
const savedReportsHoldNoKey = "saved-reports"

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
	name, known := reconcile.SavedReportFiles[request.URL.Path]
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
