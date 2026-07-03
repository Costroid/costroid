// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Costroid Authors

package focus

import (
	"slices"
	"testing"
)

// validRaw returns a FOCUS-1.4-shaped record satisfying every rule.
func validRaw() RawRecord {
	return RawRecord{
		"BilledCost":          "2.4192",
		"EffectiveCost":       "2.4192",
		"ListCost":            "2.4192",
		"ContractedCost":      "2.4192",
		"BillingCurrency":     "USD",
		"BillingPeriodStart":  "2026-05-01T00:00:00.000Z",
		"BillingPeriodEnd":    "2026-06-01T00:00:00.000Z",
		"ChargeCategory":      "Usage",
		"ChargePeriodStart":   "2026-05-01T00:00:00.000Z",
		"ChargePeriodEnd":     "2026-05-02T00:00:00.000Z",
		"BillingAccountId":    "999999999999",
		"ServiceName":         "Amazon Elastic Compute Cloud",
		"ServiceCategory":     "Compute",
		"ServiceProviderName": "AWS",
		"InvoiceIssuerName":   "Amazon Web Services, Inc.",
	}
}

func TestValidate(t *testing.T) {
	tests := []struct {
		name        string
		mutate      func(RawRecord)
		wantRuleIDs []string
	}{
		{
			name:   "conformant record",
			mutate: func(RawRecord) {},
		},
		{
			name:        "charge category outside the allowed value set",
			mutate:      func(r RawRecord) { r["ChargeCategory"] = "Consumption" },
			wantRuleIDs: []string{"CAU-ChargeCategory-C-003-M"},
		},
		{
			name:        "null charge category",
			mutate:      func(r RawRecord) { r["ChargeCategory"] = "" },
			wantRuleIDs: []string{"CAU-ChargeCategory-C-002-M"},
		},
		{
			name:   "correction charge class conforms",
			mutate: func(r RawRecord) { r["ChargeClass"] = "Correction" },
		},
		{
			name:        "charge class outside the allowed value set",
			mutate:      func(r RawRecord) { r["ChargeClass"] = "Refund" },
			wantRuleIDs: []string{"CAU-ChargeClass-C-005-C"},
		},
		{
			name:        "charge period end not after start",
			mutate:      func(r RawRecord) { r["ChargePeriodEnd"] = "2026-05-01T00:00:00.000Z" },
			wantRuleIDs: []string{"CAU-ChargePeriodEnd-C-004-M"},
		},
		{
			name:        "invalid currency code with costs present",
			mutate:      func(r RawRecord) { r["BillingCurrency"] = "dollars" },
			wantRuleIDs: []string{"CAU-BillingCurrency-C-006-M"},
		},
		{
			name:        "null currency",
			mutate:      func(r RawRecord) { r["BillingCurrency"] = "" },
			wantRuleIDs: []string{"CAU-BillingCurrency-C-004-M"},
		},
		{
			name:        "unparseable billed cost",
			mutate:      func(r RawRecord) { r["BilledCost"] = "12,34" },
			wantRuleIDs: []string{"CAU-BilledCost-C-001-M"},
		},
		{
			name:        "unparseable optional quantity",
			mutate:      func(r RawRecord) { r["ConsumedQuantity"] = "many" },
			wantRuleIDs: []string{"CAU-ConsumedQuantity-C-001-M"},
		},
		{
			name:        "unparseable charge period start",
			mutate:      func(r RawRecord) { r["ChargePeriodStart"] = "yesterday" },
			wantRuleIDs: []string{"CAU-ChargePeriodStart-C-001-M"},
		},
		{
			name:        "null effective cost",
			mutate:      func(r RawRecord) { delete(r, "EffectiveCost") },
			wantRuleIDs: []string{"CAU-EffectiveCost-C-003-M"},
		},
		{
			name:        "null service provider",
			mutate:      func(r RawRecord) { r["ServiceProviderName"] = "" },
			wantRuleIDs: []string{"CAU-ServiceProviderName-C-003-M"},
		},
		{
			name: "tags with boolean, number, and null values conform",
			mutate: func(r RawRecord) {
				r["Tags"] = `{"user:env": "prod", "user:opted-in": true, "user:weight": 3, "user:owner": null}`
			},
		},
		{
			name:        "tag with an object value violates KeyValueFormat",
			mutate:      func(r RawRecord) { r["Tags"] = `{"user:env": {"nested": "no"}}` },
			wantRuleIDs: []string{"CAU-Tags-C-001-M"},
		},
		{
			name:        "tags that are not a JSON object violate KeyValueFormat",
			mutate:      func(r RawRecord) { r["Tags"] = `not-json` },
			wantRuleIDs: []string{"CAU-Tags-C-001-M"},
		},
		{
			name:   "multiple violations are all reported",
			mutate: func(r RawRecord) { r["ChargeCategory"] = ""; r["ServiceName"] = "" },
			wantRuleIDs: []string{
				"CAU-ChargeCategory-C-002-M",
				"CAU-ServiceName-C-003-M",
			},
		},
	}

	rules := DefaultRules()
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			raw := validRaw()
			tt.mutate(raw)
			violations := Validate(raw, rules)

			var gotIDs []string
			for _, v := range violations {
				gotIDs = append(gotIDs, v.RuleID)
			}
			slices.Sort(gotIDs)
			want := slices.Clone(tt.wantRuleIDs)
			slices.Sort(want)
			if !slices.Equal(gotIDs, want) {
				t.Errorf("violations = %v, want rules %v", violations, want)
			}
		})
	}
}
