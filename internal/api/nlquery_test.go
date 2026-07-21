// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Costroid Authors

package api

import (
	"strings"
	"testing"

	"github.com/Costroid/costroid/internal/focus"
	"github.com/Costroid/costroid/internal/nlquery"
)

func planString(value string) *string { return &value }

func TestValidatePlanRejections(t *testing.T) {
	long := strings.Repeat("é", focus.MaxFreeTextBytes/2+1)
	for _, tc := range []struct {
		name    string
		plan    nlquery.Plan
		metrics []string
		want    string
	}{
		{name: "unknown endpoint", plan: nlquery.Plan{Endpoint: "meta"}, want: "unknown plan endpoint"},
		{name: "unknown groupBy", plan: nlquery.Plan{Endpoint: "costs-daily", GroupBy: planString("account")}, want: "invalid groupBy value"},
		{name: "groupBy unsupported", plan: nlquery.Plan{Endpoint: "tokens", GroupBy: planString("service")}, want: "endpoint tokens does not accept groupBy"},
		{name: "currency unsupported", plan: nlquery.Plan{Endpoint: "tokens", Currency: planString("USD")}, want: "endpoint tokens does not accept currency"},
		{name: "provider unsupported", plan: nlquery.Plan{Endpoint: "usage", Provider: planString("AcmeCloud")}, want: "endpoint usage does not accept provider"},
		{name: "tag missing key", plan: nlquery.Plan{Endpoint: "costs-summary", GroupBy: planString("tag")}, want: "groupBy=tag requires the tagKey parameter"},
		{name: "key without tag", plan: nlquery.Plan{Endpoint: "costs-summary", TagKey: planString("team")}, want: "the tagKey parameter requires groupBy=tag"},
		{name: "empty tag key", plan: nlquery.Plan{Endpoint: "anomalies", GroupBy: planString("tag"), TagKey: planString("")}, want: tagKeyShapeMessage},
		{name: "long tag key", plan: nlquery.Plan{Endpoint: "anomalies", GroupBy: planString("tag"), TagKey: &long}, want: tagKeyShapeMessage},
		{name: "sigil tag key", plan: nlquery.Plan{Endpoint: "anomalies", GroupBy: planString("tag"), TagKey: planString("$team")}, want: "tag key \"$team\" begins with '$', which DuckDB interprets as a JSONPath expression, not a literal key; drop the '$'"},
		{name: "currency shape", plan: nlquery.Plan{Endpoint: "costs-daily", Currency: planString("usd")}, want: "currency must be a three-letter uppercase code (for example, USD)"},
		{name: "provider byte limit", plan: nlquery.Plan{Endpoint: "costs-daily", Provider: &long}, want: providerShapeMessage},
		{name: "start format", plan: nlquery.Plan{Endpoint: "usage", Start: planString("07/01/2026")}, want: "start must be YYYY-MM-DD"},
		{name: "end format", plan: nlquery.Plan{Endpoint: "usage", End: planString("2026-7-1")}, want: "end must be YYYY-MM-DD"},
		{name: "inverted dates", plan: nlquery.Plan{Endpoint: "tokens", Start: planString("2026-07-02"), End: planString("2026-07-01")}, want: "start date must not be after end date"},
		{name: "metric missing", plan: nlquery.Plan{Endpoint: "unit-economics"}, want: "unit-economics requires a metric"},
		{name: "metric elsewhere", plan: nlquery.Plan{Endpoint: "costs-summary", Metric: planString("requests")}, want: "endpoint costs-summary does not accept metric"},
		{name: "metric byte limit", plan: nlquery.Plan{Endpoint: "unit-economics", Metric: &long}, want: "metric must be at most 8192 bytes"},
		{name: "unknown metric", plan: nlquery.Plan{Endpoint: "unit-economics", Metric: planString("seats")}, metrics: []string{"requests"}, want: "unknown business metric"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := ValidatePlan(tc.plan, tc.metrics); err == nil || err.Error() != tc.want {
				t.Fatalf("ValidatePlan error = %v, want %q", err, tc.want)
			}
		})
	}
}

func TestValidatePlanUsesGeneratedGroupBySets(t *testing.T) {
	tests := []struct {
		endpoint string
		members  []string
	}{
		{endpoint: "costs-daily", members: []string{string(GetDailyCostsParamsGroupByAllocation), string(GetDailyCostsParamsGroupByProvider), string(GetDailyCostsParamsGroupByRegion), string(GetDailyCostsParamsGroupByService), string(GetDailyCostsParamsGroupBySubaccount), string(GetDailyCostsParamsGroupByTag)}},
		{endpoint: "costs-summary", members: []string{string(Allocation), string(Provider), string(Region), string(Service), string(Subaccount), string(Tag)}},
		{endpoint: "anomalies", members: []string{string(GetAnomaliesParamsGroupByAllocation), string(GetAnomaliesParamsGroupByProvider), string(GetAnomaliesParamsGroupByRegion), string(GetAnomaliesParamsGroupByService), string(GetAnomaliesParamsGroupBySubaccount), string(GetAnomaliesParamsGroupByTag)}},
	}
	for _, tc := range tests {
		t.Run(tc.endpoint, func(t *testing.T) {
			for _, member := range tc.members {
				plan := nlquery.Plan{Endpoint: tc.endpoint, GroupBy: planString(member)}
				if member == "tag" {
					plan.TagKey = planString("team")
				}
				if err := ValidatePlan(plan, nil); err != nil {
					t.Errorf("generated member %q rejected: %v", member, err)
				}
			}
			if err := ValidatePlan(nlquery.Plan{Endpoint: tc.endpoint, GroupBy: planString("outside-generated-set")}, nil); err == nil {
				t.Error("value outside generated set accepted")
			}
		})
	}
	if err := ValidatePlan(nlquery.Plan{Endpoint: "usage", GroupBy: planString("service")}, nil); err == nil {
		t.Error("groupBy accepted by endpoint that takes none")
	}
}
