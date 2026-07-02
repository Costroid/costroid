// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Costroid Authors

package focus

import (
	"testing"
)

func TestTransformTo14Registry(t *testing.T) {
	tests := []struct {
		version Version
		wantErr bool
	}{
		{V1_0, true},           // registry slot, not implemented
		{V1_1, true},           // registry slot, not implemented
		{V1_2, false},          // implemented in this slice
		{V1_3, true},           // registry slot, not implemented
		{V1_4, true},           // identity slot, not needed yet
		{Version("2.0"), true}, // unknown version
	}
	for _, tt := range tests {
		t.Run(string(tt.version), func(t *testing.T) {
			transform, err := TransformTo14(tt.version)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("TransformTo14(%q) = nil error, want error", tt.version)
				}
				return
			}
			if err != nil {
				t.Fatalf("TransformTo14(%q) error: %v", tt.version, err)
			}
			if transform == nil {
				t.Fatalf("TransformTo14(%q) returned nil transform", tt.version)
			}
		})
	}
}

func TestTransform12To14(t *testing.T) {
	tests := []struct {
		name string
		in   RawRecord
		want map[string]string // expected keys; "" means must be absent
	}{
		{
			name: "native charge: provider and publisher both name the CSP",
			in: RawRecord{
				"BilledCost":        "2.4192",
				"ProviderName":      "AWS",
				"PublisherName":     "AWS",
				"InvoiceIssuerName": "Amazon Web Services, Inc.",
			},
			want: map[string]string{
				"BilledCost":          "2.4192",
				"ServiceProviderName": "AWS",
				"HostProviderName":    "AWS",
				"InvoiceIssuerName":   "Amazon Web Services, Inc.",
				"ProviderName":        "", // removed in 1.4
				"PublisherName":       "", // removed in 1.4
			},
		},
		{
			name: "marketplace charge: the publisher (seller) is the 1.4 service provider",
			in: RawRecord{
				"ProviderName":      "AWS",
				"PublisherName":     "Datadog",
				"InvoiceIssuerName": "Amazon Web Services, Inc.",
			},
			want: map[string]string{
				"ServiceProviderName": "Datadog",
				"HostProviderName":    "AWS",
				"InvoiceIssuerName":   "Amazon Web Services, Inc.",
			},
		},
		{
			name: "null publisher falls back to the provider",
			in:   RawRecord{"ProviderName": "AWS"},
			want: map[string]string{
				"ServiceProviderName": "AWS",
				"HostProviderName":    "AWS",
			},
		},
		{
			name: "proprietary x_ columns and unknown columns are dropped",
			in: RawRecord{
				"ServiceName":   "AWS Lambda",
				"x_Operation":   "Invoke",
				"x_ServiceCode": "AWSLambda",
				"x_Discounts":   "",
				"NotAColumn":    "surprise",
			},
			want: map[string]string{
				"ServiceName":   "AWS Lambda",
				"x_Operation":   "",
				"x_ServiceCode": "",
				"NotAColumn":    "",
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := transform12To14(tt.in)
			if err != nil {
				t.Fatalf("transform12To14 error: %v", err)
			}
			for col, want := range tt.want {
				if got[col] != want {
					t.Errorf("column %s = %q, want %q", col, got[col], want)
				}
			}
		})
	}
}

func TestParseRecord(t *testing.T) {
	raw := RawRecord{
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
		"HostProviderName":    "AWS",
		"InvoiceIssuerName":   "Amazon Web Services, Inc.",
		"ConsumedQuantity":    "24",
		"ConsumedUnit":        "Hrs",
		"Tags":                `{"user:env": "prod", "user:team": "platform"}`,
	}
	rec, err := ParseRecord(raw)
	if err != nil {
		t.Fatalf("ParseRecord error: %v", err)
	}
	if got := rec.BilledCost.String(); got != "2.4192" {
		t.Errorf("BilledCost = %s, want 2.4192", got)
	}
	if !rec.ConsumedQuantity.Valid || rec.ConsumedQuantity.Decimal.String() != "24" {
		t.Errorf("ConsumedQuantity = %+v, want valid 24", rec.ConsumedQuantity)
	}
	if rec.PricingQuantity.Valid {
		t.Errorf("PricingQuantity = %+v, want null", rec.PricingQuantity)
	}
	if got := rec.ChargePeriodStart.Format("2006-01-02T15:04:05Z"); got != "2026-05-01T00:00:00Z" {
		t.Errorf("ChargePeriodStart = %s", got)
	}
	if rec.Tags["user:team"] != "platform" {
		t.Errorf("Tags = %v, want user:team=platform", rec.Tags)
	}

	bad := RawRecord{}
	for k, v := range raw {
		bad[k] = v
	}
	bad["BilledCost"] = "not-a-number"
	if _, err := ParseRecord(bad); err == nil {
		t.Error("ParseRecord accepted an unparseable BilledCost")
	}
}
