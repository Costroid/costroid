package reconcile

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"slices"
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

// credentialFields are the fields a report record names its credential in:
// Anthropic's group, and each result of an OpenAI bucket.
var credentialFields = []string{"api_key_id", "workspace_id", "project_id"}

// Recorder sends each request through Client and keeps the response in
// Directory, so the report can be reconciled again with no key. A saved report
// is one response, so a second request for one file is refused: that report
// has more than one page or spans more than one provider window.
//
// When Credentials is set, each response is cut to the records of those
// credentials before it is kept or read, so no other key's usage is written.
type Recorder struct {
	Client      Doer
	Directory   string
	Credentials []string
	kept        map[string]bool
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
	if r.Credentials != nil {
		if body, err = keepOnly(body, r.Credentials); err != nil {
			return nil, fmt.Errorf("%s: %w", request.URL.Path, err)
		}
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

// keepOnly cuts a report to the records of the credentials. Every other field
// of the envelope stays. An Anthropic record is kept when its group names a
// credential; an OpenAI bucket keeps the results that name one. A kept field
// keeps the provider's own text, so no figure is re-encoded.
func keepOnly(body []byte, credentials []string) ([]byte, error) {
	var envelope map[string]json.RawMessage
	if err := json.Unmarshal(body, &envelope); err != nil {
		return nil, fmt.Errorf("cut to the credentials: %w", err)
	}
	var records []map[string]json.RawMessage
	if err := json.Unmarshal(envelope["data"], &records); err != nil || records == nil {
		return nil, errors.New("cut to the credentials: the report holds no data list")
	}
	kept := []map[string]json.RawMessage{}
	for _, record := range records {
		switch {
		case record["results"] != nil:
			if err := keepResults(record, credentials); err != nil {
				return nil, err
			}
			kept = append(kept, record)
		case record["group"] != nil:
			var group map[string]json.RawMessage
			if err := json.Unmarshal(record["group"], &group); err != nil {
				return nil, fmt.Errorf("cut to the credentials: group: %w", err)
			}
			if names(group, credentials) {
				kept = append(kept, record)
			}
		default:
			return nil, errors.New("cut to the credentials: a record has neither a group nor results")
		}
	}
	data, err := json.Marshal(kept)
	if err != nil {
		return nil, err
	}
	envelope["data"] = data
	cut, err := json.Marshal(envelope)
	if err != nil {
		return nil, err
	}
	return append(cut, '\n'), nil
}

func keepResults(bucket map[string]json.RawMessage, credentials []string) error {
	var results []map[string]json.RawMessage
	if err := json.Unmarshal(bucket["results"], &results); err != nil {
		return fmt.Errorf("cut to the credentials: results: %w", err)
	}
	kept := []map[string]json.RawMessage{}
	for _, result := range results {
		if names(result, credentials) {
			kept = append(kept, result)
		}
	}
	data, err := json.Marshal(kept)
	if err != nil {
		return err
	}
	bucket["results"] = data
	return nil
}

// names reports whether a record's credential fields name one of the
// credentials. A null or absent field names none.
func names(fields map[string]json.RawMessage, credentials []string) bool {
	for _, field := range credentialFields {
		var value *string
		if json.Unmarshal(fields[field], &value) == nil && value != nil && slices.Contains(credentials, *value) {
			return true
		}
	}
	return false
}
