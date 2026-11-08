package reconcile

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/Costroid/costroid/internal/focus"
)

const serviceAccount = "svac_01ExampleCi"

// federatedReport answers the usage and cost endpoints as the Usage and Cost
// Analytics API documents a day on a Workload Identity Federation token:
// api_key_id is null, and the actor is the service account the token acts
// as. Beside it are a second service account, a user and usage no identity
// stands behind, which no credential of the ledger names. It keeps each
// request's group_by values.
func federatedReport(t *testing.T, asked *[][]string) Provider {
	t.Helper()
	actor := func(id string) map[string]any {
		return map[string]any{"type": "service_account_actor", "service_account_id": id}
	}
	user := map[string]any{"type": "user_actor", "user_id": "user_01Example", "deleted": false, "email_address": nil, "name": nil}
	group := func(who any) map[string]any {
		return map[string]any{"api_key_id": nil, "actor": who, "model": sonnet, "service_tier": "standard", "inference_geo": "global", "speed": "standard"}
	}
	start := day + "T00:00:00Z"
	usage := func(who any, input int64) map[string]any {
		return map[string]any{"starting_at": start, "group": group(who), "usage": map[string]any{
			"uncached_input_tokens":   input,
			"cache_creation":          map[string]int64{"ephemeral_1h_input_tokens": 0, "ephemeral_5m_input_tokens": 20000},
			"cache_read_input_tokens": 300000,
			"output_tokens":           50000,
		}}
	}
	cost := func(who any, cents string) map[string]any {
		return map[string]any{"starting_at": start, "group": map[string]any{"api_key_id": nil, "actor": who, "cost_type": "tokens"},
			"cost": map[string]any{"currency": "USD", "amount": cents, "list_amount": cents}}
	}
	pages := map[string][]map[string]any{
		"/analytics/usage": {usage(actor(serviceAccount), 100000), usage(actor("svac_02ExampleOther"), 7), usage(user, 9), usage(nil, 11)},
		"/analytics/cost":  {cost(actor(serviceAccount), "81.000000"), cost(actor("svac_02ExampleOther"), "1.000000"), cost(user, "2.000000"), cost(nil, "3.000000")},
	}
	return NewAnthropic(doerFunc(func(request *http.Request) (*http.Response, error) {
		*asked = append(*asked, request.URL.Query()["group_by[]"])
		for route, data := range pages {
			if strings.HasSuffix(request.URL.Path, route) {
				page, err := json.Marshal(map[string]any{"data": data, "has_more": false, "next_page": nil, "data_refreshed_at": "2026-10-15T08:00:00Z"})
				if err != nil {
					t.Fatal(err)
				}
				return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(string(page)))}, nil
			}
		}
		t.Fatalf("no page for %s", request.URL.Path)
		return nil, nil
	}), sampleKey)
}

func TestNullApiKeyIdIsReadAsTheServiceAccount(t *testing.T) {
	var asked [][]string
	days, err := ParseRange(day, day)
	if err != nil {
		t.Fatal(err)
	}
	report, err := federatedReport(t, &asked).Fetch(context.Background(), days, []string{serviceAccount})
	if err != nil {
		t.Fatal(err)
	}
	for _, groupBy := range asked {
		if !slices.Contains(groupBy, anthropicActorGrain) || slices.Contains(groupBy, anthropicKeyGrain) {
			t.Errorf("a request grouped by %v, want actor and not api_key_id", groupBy)
		}
	}
	if len(report.Usage) != 2 || len(report.Costs) != 2 {
		t.Fatalf("read %d usage and %d cost records, want the two service accounts' of each: %+v", len(report.Usage), len(report.Costs), report)
	}
	byCredential := map[string]Usage{}
	for _, usage := range report.Usage {
		byCredential[usage.Credential] = usage
	}
	if got := byCredential[serviceAccount].Tokens[focus.InputUncached].String(); got != "100000" {
		t.Errorf("%s reads %s input tokens, want 100000", serviceAccount, got)
	}
	if _, found := byCredential[""]; found {
		t.Error("a user's usage was read as a credential")
	}
}

func TestServiceAccountReportMatchesByActor(t *testing.T) {
	rows := anthropicRows()
	for i := range rows {
		rows[i].credential = ""
	}
	var asked [][]string
	result := pass(t, readRows(t, rows, serviceAccount), federatedReport(t, &asked), settledNow)
	got := only(t, result)
	if got.Credential != serviceAccount || got.State != Matched {
		t.Fatalf("credential %s in state %s, want %s matched: %+v", got.Credential, got.State, serviceAccount, got.Tokens)
	}
	wantDollars(t, got, "0.81", "0.81", "0", "0", Within)
	if len(result.Unmatched) != 1 || result.Unmatched[0].Credential != "svac_02ExampleOther" {
		t.Errorf("unmatched %+v, want the other service account alone", result.Unmatched)
	}
}
