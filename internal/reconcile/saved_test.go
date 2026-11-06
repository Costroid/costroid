package reconcile

import (
	"context"
	"io"
	"maps"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/Costroid/costroid/internal/decimal"
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

func TestRecorderKeepsOnlyTheNamedCredentials(t *testing.T) {
	const openAIBody = `{"object":"page","data":[{"object":"bucket","start_time":1791936000,"end_time":1792022400,"results":[
{"api_key_id":"key_ExampleCi","project_id":null,"model":"gpt-5.6-sol","input_tokens":151000,"output_tokens":20000},
{"api_key_id":"key_ExampleProd","project_id":null,"model":"gpt-5.6-sol","input_tokens":9,"output_tokens":9}]}],
"has_more":false,"next_page":null}`
	directory := t.TempDir()
	recorder := &Recorder{Client: doerFunc(func(request *http.Request) (*http.Response, error) {
		if request.URL.Path == "/v1/organization/usage/completions" {
			return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(openAIBody))}, nil
		}
		return replay(t, map[string]string{"/analytics/usage": "anthropic/usage-other-key.json"}).Do(request)
	}), Directory: directory, Credentials: []string{anthropicID, openAIID}}
	for _, endpoint := range []string{anthropicUsage, openAIUsage} {
		request, err := http.NewRequest(http.MethodGet, endpoint, nil)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := recorder.Do(request); err != nil {
			t.Fatal(err)
		}
	}
	for name, want := range map[string][]string{
		"anthropic-usage.json": {anthropicID, `"data_refreshed_at"`},
		"openai-usage.json":    {openAIID, `"input_tokens":151000`, `"object":"page"`},
	} {
		data, err := os.ReadFile(filepath.Join(directory, name))
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(data), "ExampleProd") {
			t.Errorf("%s keeps another key's records:\n%s", name, data)
		}
		for _, text := range want {
			if !strings.Contains(string(data), text) {
				t.Errorf("%s lost %s:\n%s", name, text, data)
			}
		}
	}
}

func TestCutReportStillReconciles(t *testing.T) {
	directory := t.TempDir()
	recorder := &Recorder{Client: replay(t, map[string]string{
		"/analytics/usage": "anthropic/usage-other-key.json",
		"/analytics/cost":  "anthropic/cost-at-list.json",
	}), Directory: directory, Credentials: []string{anthropicID}}
	days, err := ParseRange(day, day)
	if err != nil {
		t.Fatal(err)
	}
	cut, err := NewAnthropic(recorder, sampleKey).Fetch(context.Background(), days, []string{anthropicID})
	if err != nil {
		t.Fatal(err)
	}
	whole, err := NewAnthropic(replay(t, map[string]string{
		"/analytics/usage": "anthropic/usage-other-key.json",
		"/analytics/cost":  "anthropic/cost-at-list.json",
	}), sampleKey).Fetch(context.Background(), days, []string{anthropicID})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := len(cut.Usage), len(whole.Usage)-1; got != want {
		t.Fatalf("the cut report holds %d usage records, want %d", got, want)
	}
	if cut.Usage[0].Credential != anthropicID || !maps.EqualFunc(cut.Usage[0].Tokens, whole.Usage[0].Tokens, func(a, b decimal.Decimal) bool { return a.Cmp(b) == 0 }) {
		t.Errorf("the kept record moved: %+v, was %+v", cut.Usage[0], whole.Usage[0])
	}
}
