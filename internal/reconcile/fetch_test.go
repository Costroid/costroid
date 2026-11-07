package reconcile

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Costroid/costroid/internal/focus"
)

const sampleKey = "sk-sample-sentinel-0123456789"

// replayClient answers each request from the first saved sample whose route
// its path ends with, and keeps what was asked.
type replayClient struct {
	t      *testing.T
	routes map[string]string
	mu     sync.Mutex
	asked  []*http.Request
}

func replay(t *testing.T, routes map[string]string) *replayClient {
	t.Helper()
	return &replayClient{t: t, routes: routes}
}

func (c *replayClient) Do(request *http.Request) (*http.Response, error) {
	c.mu.Lock()
	c.asked = append(c.asked, request)
	c.mu.Unlock()
	for route, file := range c.routes {
		if strings.HasSuffix(request.URL.Path, route) {
			return c.respond(file)
		}
	}
	return nil, fmt.Errorf("no sample for %s", request.URL.Path)
}

func (c *replayClient) respond(file string) (*http.Response, error) {
	data, err := os.ReadFile(filepath.Join(samples, file))
	if err != nil {
		c.t.Fatal(err)
	}
	return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(string(data)))}, nil
}

type doerFunc func(*http.Request) (*http.Response, error)

func (f doerFunc) Do(request *http.Request) (*http.Response, error) { return f(request) }

func readSample[T any](t *testing.T, file string) T {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(samples, file))
	if err != nil {
		t.Fatal(err)
	}
	var value T
	if err := json.Unmarshal(data, &value); err != nil {
		t.Fatal(err)
	}
	return value
}

func TestAnthropicPublishedUsageSampleMapsToCanonicalClasses(t *testing.T) {
	sample := readSample[page[anthropicUsageRow]](t, "anthropic/published-usage.json")
	usage := sample.Data[0].usage(anthropicKeyGrain)
	want := map[focus.TokenClass]string{
		focus.InputUncached: "1284500",
		focus.CacheRead:     "3200000",
		focus.CacheWrite5m:  "215300",
		focus.CacheWrite1h:  "0",
		focus.Output:        "891000",
	}
	for class, tokens := range want {
		if got := usage.Tokens[class].String(); got != tokens {
			t.Errorf("%s = %s, want %s", class, got, tokens)
		}
	}
	if usage.Day != "2026-09-03" || usage.Model != "claude-sonnet-5" {
		t.Errorf("usage %+v", usage)
	}
	if sample.DataRefreshedAt == nil || !sample.HasMore || sample.NextPage == nil {
		t.Errorf("envelope %+v", sample)
	}
}

// The documentation's own example: "41280.000000" is 412.80 USD.
func TestAnthropicPublishedCostSampleIsCentsNotDollars(t *testing.T) {
	sample := readSample[anthropicCostRow](t, "anthropic/published-cost-row.json")
	cost, err := sample.cost(anthropicKeyGrain)
	if err != nil {
		t.Fatal(err)
	}
	if cost.Amount.String() != "412.8" || cost.List.String() != "516" || !cost.Tokens {
		t.Errorf("cost %+v, list %s", cost, cost.List)
	}
}

func TestOpenAIPublishedUsageSampleMapsToDisjointClasses(t *testing.T) {
	sample := readSample[page[openAIBucket[openAIUsageResult]]](t, "openai/published-usage.json")
	usage, err := sample.Data[0].Results[0].usage(openAIKeyGrain, sample.Data[0].day())
	if err != nil {
		t.Fatal(err)
	}
	want := map[focus.TokenClass]string{
		focus.InputUncached: "500",
		focus.CacheRead:     "400",
		focus.CacheWrite30m: "75",
		focus.CacheWrite12h: "25",
		focus.Output:        "500",
	}
	sum := usage.Tokens[focus.InputUncached].Add(usage.Tokens[focus.CacheRead]).Add(usage.Tokens[focus.CacheWrite30m]).Add(usage.Tokens[focus.CacheWrite12h])
	if sum.String() != "1000" {
		t.Errorf("input classes sum to %s, want input_tokens 1000", sum)
	}
	for class, tokens := range want {
		if got := usage.Tokens[class].String(); got != tokens {
			t.Errorf("%s = %s, want %s", class, got, tokens)
		}
	}
	if usage.Day != "2024-11-01" {
		t.Errorf("day %s", usage.Day)
	}
}

func TestOpenAIPublishedCostsSampleKeepsTheLiteralDigits(t *testing.T) {
	sample := readSample[page[openAIBucket[openAICostResult]]](t, "openai/published-costs.json")
	cost, err := sample.Data[0].Results[0].cost(openAIKeyGrain, "2024-11-01")
	if err != nil {
		t.Fatal(err)
	}
	if cost.Amount.String() != "0.06" || cost.List != nil {
		t.Errorf("cost %+v", cost)
	}
}

func TestAnthropicRequestsCarryTheBetaHeaderAndAnExclusiveEnd(t *testing.T) {
	client := replay(t, map[string]string{"/analytics/usage": "anthropic/empty-refreshed.json", "/analytics/cost": "anthropic/empty-refreshed.json"})
	days, _ := ParseRange("2026-10-01", "2026-10-14")
	if _, err := NewAnthropic(client, sampleKey).Fetch(context.Background(), days, []string{anthropicID}); err != nil {
		t.Fatal(err)
	}
	if len(client.asked) != 2 {
		t.Fatalf("%d requests, want usage and cost", len(client.asked))
	}
	usage, cost := client.asked[0], client.asked[1]
	if got := usage.URL.String(); !strings.HasPrefix(got, "https://api.anthropic.com/v1/organizations/analytics/usage?") {
		t.Errorf("url %s", got)
	}
	if got := usage.Header.Get("anthropic-beta"); got != "analytics-usage-cost-2026-08-24" {
		t.Errorf("beta header %q", got)
	}
	if usage.Header.Get("anthropic-version") == "" || usage.Header.Get("x-api-key") != sampleKey {
		t.Errorf("headers lack the version or the key")
	}
	query := usage.URL.Query()
	if query.Get("starting_at") != "2026-10-01T00:00:00Z" || query.Get("ending_at") != "2026-10-15T00:00:00Z" {
		t.Errorf("span %s to %s", query.Get("starting_at"), query.Get("ending_at"))
	}
	if strings.Join(query["group_by[]"], ",") != "api_key_id,model,service_tier,inference_geo,speed" {
		t.Errorf("group_by %v", query["group_by[]"])
	}
	if strings.Join(cost.URL.Query()["group_by[]"], ",") != "api_key_id,cost_type" {
		t.Errorf("cost group_by %v", cost.URL.Query()["group_by[]"])
	}
}

func TestAnthropicRangeSplitsAtTheThirtyOneDaySpan(t *testing.T) {
	client := replay(t, map[string]string{"/analytics/usage": "anthropic/empty-refreshed.json", "/analytics/cost": "anthropic/empty-refreshed.json"})
	days, _ := ParseRange("2026-08-01", "2026-10-01")
	if _, err := NewAnthropic(client, sampleKey).Fetch(context.Background(), days, []string{anthropicID}); err != nil {
		t.Fatal(err)
	}
	var spans []string
	for _, request := range client.asked {
		if !strings.HasSuffix(request.URL.Path, "/usage") {
			continue
		}
		start, _ := time.Parse(time.RFC3339, request.URL.Query().Get("starting_at"))
		end, _ := time.Parse(time.RFC3339, request.URL.Query().Get("ending_at"))
		if end.Sub(start) > 31*24*time.Hour {
			t.Errorf("span %s to %s is past 31 days", start, end)
		}
		spans = append(spans, start.Format(dayLayout)+".."+end.Format(dayLayout))
	}
	want := "2026-08-01..2026-09-01,2026-09-01..2026-10-02"
	if strings.Join(spans, ",") != want {
		t.Errorf("spans %v, want %s", spans, want)
	}
}

func TestWorkspaceAndProjectCredentialsAskForTheirOwnGrain(t *testing.T) {
	client := replay(t, map[string]string{"/analytics/usage": "anthropic/empty-refreshed.json", "/analytics/cost": "anthropic/empty-refreshed.json"})
	days, _ := ParseRange(day, day)
	if _, err := NewAnthropic(client, sampleKey).Fetch(context.Background(), days, []string{"wrkspc_01Example"}); err != nil {
		t.Fatal(err)
	}
	if got := client.asked[0].URL.Query()["group_by[]"][0]; got != "workspace_id" {
		t.Errorf("group_by leads with %s", got)
	}
	openai := replay(t, map[string]string{"/usage/completions": "openai/empty.json", "/organization/costs": "openai/empty.json"})
	if _, err := NewOpenAI(openai, sampleKey).Fetch(context.Background(), days, []string{"proj_Example"}); err != nil {
		t.Fatal(err)
	}
	if got := openai.asked[0].URL.Query()["group_by"][0]; got != "project_id" {
		t.Errorf("group_by leads with %s", got)
	}
}

func TestOpenAIRequestsUseUnixSecondsAndABearerKey(t *testing.T) {
	client := replay(t, map[string]string{"/usage/completions": "openai/empty.json", "/organization/costs": "openai/empty.json"})
	days, _ := ParseRange(day, day)
	if _, err := NewOpenAI(client, sampleKey).Fetch(context.Background(), days, []string{openAIID}); err != nil {
		t.Fatal(err)
	}
	usage := client.asked[0]
	if got := usage.URL.Scheme + "://" + usage.URL.Host + usage.URL.Path; got != "https://api.openai.com/v1/organization/usage/completions" {
		t.Errorf("url %s", got)
	}
	if got := usage.Header.Get("Authorization"); got != "Bearer "+sampleKey {
		t.Error("the bearer header is wrong")
	}
	query := usage.URL.Query()
	if query.Get("start_time") != "1791936000" || query.Get("end_time") != "1792022400" {
		t.Errorf("span %s to %s", query.Get("start_time"), query.Get("end_time"))
	}
	if strings.Join(query["group_by"], ",") != "api_key_id,model,batch,service_tier" {
		t.Errorf("group_by %v", query["group_by"])
	}
	if got := client.asked[1].URL.Path; got != "/v1/organization/costs" {
		t.Errorf("costs path %s", got)
	}
}

func TestPagesAreWalkedToTheLast(t *testing.T) {
	var cursors []string
	client := doerFunc(func(request *http.Request) (*http.Response, error) {
		cursor := request.URL.Query().Get("page")
		file := "anthropic/empty-refreshed.json"
		if strings.HasSuffix(request.URL.Path, "/usage") {
			cursors = append(cursors, cursor)
			file = "anthropic/usage-page-1.json"
			if cursor != "" {
				file = "anthropic/usage-page-2.json"
			}
		}
		data, err := os.ReadFile(filepath.Join(samples, file))
		if err != nil {
			t.Fatal(err)
		}
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(string(data)))}, nil
	})
	days, _ := ParseRange(day, day)
	report, err := NewAnthropic(client, sampleKey).Fetch(context.Background(), days, []string{anthropicID})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(cursors, ",") != ",page_example_2" {
		t.Errorf("cursors %q", cursors)
	}
	if got := sumTokens(report.Usage)[focus.InputUncached].String(); got != "100000" {
		t.Errorf("input across pages = %s, want 100000", got)
	}
}

func TestHasMoreWithNoCursorIsAnError(t *testing.T) {
	client := doerFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(`{"data":[],"has_more":true,"next_page":null}`))}, nil
	})
	days, _ := ParseRange(day, day)
	if _, err := NewOpenAI(client, sampleKey).Fetch(context.Background(), days, []string{openAIID}); err == nil {
		t.Error("a report that could not finish was accepted")
	}
}

func TestARefusalReportsItsStatusAndNeitherTheBodyNorTheKey(t *testing.T) {
	client := doerFunc(func(*http.Request) (*http.Response, error) {
		body := fmt.Sprintf(`{"error":{"message":"bad key %s"}}`, sampleKey)
		return &http.Response{StatusCode: http.StatusUnauthorized, Body: io.NopCloser(strings.NewReader(body))}, nil
	})
	days, _ := ParseRange(day, day)
	for _, provider := range []Provider{NewAnthropic(client, sampleKey), NewOpenAI(client, sampleKey)} {
		_, err := provider.Fetch(context.Background(), days, []string{anthropicID})
		if err == nil {
			t.Fatal("a refusal was accepted")
		}
		if strings.Contains(err.Error(), sampleKey) || strings.Contains(err.Error(), "bad key") {
			t.Errorf("error carries the body: %v", err)
		}
		if !strings.Contains(err.Error(), "status 401") {
			t.Errorf("error %v lacks the status", err)
		}
	}
}

func TestATransportFailureNeverCarriesTheKey(t *testing.T) {
	client := doerFunc(func(*http.Request) (*http.Response, error) {
		return nil, errors.New("connection refused")
	})
	days, _ := ParseRange(day, day)
	_, err := NewOpenAI(client, sampleKey).Fetch(context.Background(), days, []string{openAIID})
	if err == nil || strings.Contains(err.Error(), sampleKey) {
		t.Errorf("err = %v", err)
	}
}

func TestANonDollarAmountIsRefusedNotSummed(t *testing.T) {
	var row anthropicCostRow
	row.Cost.Currency = "EUR"
	row.Cost.Amount = "1.000000"
	if _, err := row.cost(anthropicKeyGrain); err == nil {
		t.Error("EUR was accepted")
	}
}

// The test server plays a provider that redirects. Go keeps x-api-key across
// hosts, so a followed redirect would hand the key to the target.
func TestTheProductionClientFollowsNoRedirect(t *testing.T) {
	var reached bool
	target := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { reached = true }))
	defer target.Close()
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusFound)
	}))
	defer origin.Close()
	response, err := NewClient().Get(origin.URL)
	if err != nil {
		t.Fatal(err)
	}
	if err := response.Body.Close(); err != nil {
		t.Fatal(err)
	}
	if reached || response.StatusCode != http.StatusFound {
		t.Errorf("reached %t, status %d", reached, response.StatusCode)
	}
}
