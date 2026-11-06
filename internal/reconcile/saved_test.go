package reconcile

import (
	"context"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func TestRecorderKeepsEachResponseUnderItsSavedName(t *testing.T) {
	directory := t.TempDir()
	recorder := &Recorder{Client: replay(t, map[string]string{
		"/analytics/usage": "anthropic/usage.json",
		"/analytics/cost":  "anthropic/cost-at-list.json",
	}), Directory: directory}
	days, err := ParseRange(day, day)
	if err != nil {
		t.Fatal(err)
	}
	report, err := NewAnthropic(recorder, sampleKey).Fetch(context.Background(), days, []string{anthropicID})
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Usage) == 0 || len(report.Costs) == 0 {
		t.Fatalf("the fetcher read nothing through the recorder: %+v", report)
	}
	for kept, sample := range map[string]string{
		"anthropic-usage.json": "anthropic/usage.json",
		"anthropic-cost.json":  "anthropic/cost-at-list.json",
	} {
		got, err := os.ReadFile(filepath.Join(directory, kept))
		if err != nil {
			t.Fatal(err)
		}
		want, err := os.ReadFile(filepath.Join(samples, sample))
		if err != nil {
			t.Fatal(err)
		}
		if string(got) != string(want) {
			t.Errorf("%s does not hold the response byte for byte", kept)
		}
		if strings.Contains(string(got), sampleKey) {
			t.Errorf("%s holds the key", kept)
		}
		info, err := os.Stat(filepath.Join(directory, kept))
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != savedReportMode {
			t.Errorf("%s has mode %v, want %v", kept, info.Mode().Perm(), os.FileMode(savedReportMode))
		}
	}
}

func TestRecorderRefusesASecondResponseForOneFile(t *testing.T) {
	recorder := &Recorder{Client: replay(t, map[string]string{"/analytics/usage": "anthropic/usage.json"}), Directory: t.TempDir()}
	request, err := http.NewRequest(http.MethodGet, anthropicUsage, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := recorder.Do(request); err != nil {
		t.Fatal(err)
	}
	if _, err := recorder.Do(request); err == nil {
		t.Fatal("a second page was kept over the first")
	}
}

func TestRecorderKeepsNothingFromARefusal(t *testing.T) {
	directory := t.TempDir()
	recorder := &Recorder{Client: doerFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusUnauthorized, Body: io.NopCloser(strings.NewReader("{}"))}, nil
	}), Directory: directory}
	request, err := http.NewRequest(http.MethodGet, anthropicCost, nil)
	if err != nil {
		t.Fatal(err)
	}
	response, err := recorder.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusUnauthorized {
		t.Errorf("status %d reached the fetcher, want %d", response.StatusCode, http.StatusUnauthorized)
	}
	entries, err := os.ReadDir(directory)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Errorf("a refusal was kept: %v", entries)
	}
}

func TestReconcileDocListsEverySavedReport(t *testing.T) {
	data, err := os.ReadFile("../../docs/reconcile.md")
	if err != nil {
		t.Fatal(err)
	}
	for path, name := range SavedReportFiles {
		row := regexp.MustCompile("\\| `" + regexp.QuoteMeta(name) + "` +\\| `GET " + regexp.QuoteMeta(path) + "` +\\|")
		if !row.Match(data) {
			t.Errorf("docs/reconcile.md does not list %s as the response of %s", name, path)
		}
	}
}
