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
		{V1_2, false},          // implemented (1.2 → 1.4 entity mapping)
		{V1_3, false},          // implemented (1.3 → 1.4 drop-only identity)
		{V1_4, false},          // identity transform (synthesized 1.4 sources)
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

func TestTransform13To14(t *testing.T) {
	transform, err := TransformTo14(V1_3)
	if err != nil {
		t.Fatalf("TransformTo14(1.3): %v", err)
	}
	tests := []struct {
		name string
		in   RawRecord
		want map[string]string // expected keys; "" means must be absent
	}{
		{
			name: "carried 1.4 columns pass through; native successor columns kept",
			in: RawRecord{
				"BilledCost":          "2.4192",
				"ServiceProviderName": "AWS",
				"HostProviderName":    "AWS",
				"InvoiceIssuerName":   "Amazon Web Services, Inc.",
			},
			want: map[string]string{
				"BilledCost":          "2.4192",
				"ServiceProviderName": "AWS",
				"HostProviderName":    "AWS",
				"InvoiceIssuerName":   "Amazon Web Services, Inc.",
			},
		},
		{
			name: "deprecated + 1.3-only + x_ columns are dropped",
			in: RawRecord{
				"ServiceName":       "AWS Lambda",
				"ProviderName":      "AWS",     // removed in 1.4
				"PublisherName":     "AWS",     // removed in 1.4
				"AllocatedCost":     "1.00",    // 1.3 split/shared-cost, not carried
				"AllocatedTags":     `{"a":1}`, // 1.3 addition, not carried
				"ContractApplied":   "true",    // 1.3 addition, not carried
				"x_ProviderService": "AWSLambda",
			},
			want: map[string]string{
				"ServiceName":       "AWS Lambda",
				"ProviderName":      "",
				"PublisherName":     "",
				"AllocatedCost":     "",
				"AllocatedTags":     "",
				"ContractApplied":   "",
				"x_ProviderService": "",
			},
		},
		{
			name: "empty values are dropped (empty == null)",
			in:   RawRecord{"ServiceName": "AWS Lambda", "HostProviderName": ""},
			want: map[string]string{"ServiceName": "AWS Lambda", "HostProviderName": ""},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := transform(tt.in)
			if err != nil {
				t.Fatalf("transform13To14 error: %v", err)
			}
			for col, want := range tt.want {
				// Per the `want` comment, "" means the key must be ABSENT
				// (dropped), not merely present-and-empty. Assert absence via the
				// two-value read — got[col] alone cannot tell a dropped key from a
				// present-but-empty one, so the empty-value-drop subtest could
				// never fail before this.
				if want == "" {
					if v, ok := got[col]; ok {
						t.Errorf("column %s = %q, want it ABSENT (dropped)", col, v)
					}
					continue
				}
				if got[col] != want {
					t.Errorf("column %s = %q, want %q", col, got[col], want)
				}
			}
		})
	}
}

// TestTransform13To14DoesNotOverwriteNativeServiceProvider is the load-bearing
// pin: a 1.3 row whose native ServiceProviderName diverges from its deprecated
// PublisherName (a marketplace charge) MUST keep the native value. Routing the
// SAME input through transform12To14 would overwrite it (ServiceProviderName ←
// PublisherName), so the two transforms MUST differ on this input — which
// proves why 1.3 is pinned to its own transform and never to transform12To14.
func TestTransform13To14DoesNotOverwriteNativeServiceProvider(t *testing.T) {
	in := RawRecord{
		"BilledCost":          "0",
		"ServiceProviderName": "Datadog", // native 1.3 successor: the marketplace seller
		"HostProviderName":    "AWS",     // native 1.3 successor: the host CSP
		"ProviderName":        "AWS",     // deprecated (host)
		"PublisherName":       "AWS",     // deprecated — diverges from the native ServiceProviderName
		"InvoiceIssuerName":   "Amazon Web Services, Inc.",
	}

	got13, err := transform13To14(in)
	if err != nil {
		t.Fatalf("transform13To14: %v", err)
	}
	if got13["ServiceProviderName"] != "Datadog" {
		t.Errorf("13→14 ServiceProviderName = %q, want the native Datadog (not overwritten from PublisherName)", got13["ServiceProviderName"])
	}

	// The REGISTRY must route V1_3 to this transform, not to transform12To14
	// (which would corrupt the value): the pin is only worth anything if it is
	// wired in.
	regTransform, err := TransformTo14(V1_3)
	if err != nil {
		t.Fatalf("TransformTo14(1.3): %v", err)
	}
	gotReg, err := regTransform(in)
	if err != nil {
		t.Fatalf("registry V1_3 transform: %v", err)
	}
	if gotReg["ServiceProviderName"] != "Datadog" {
		t.Errorf("registry V1_3 transform ServiceProviderName = %q, want native Datadog "+
			"(is V1_3 mis-routed to transform12To14?)", gotReg["ServiceProviderName"])
	}
	if got13["HostProviderName"] != "AWS" {
		t.Errorf("13→14 HostProviderName = %q, want the native AWS", got13["HostProviderName"])
	}

	got12, err := transform12To14(in)
	if err != nil {
		t.Fatalf("transform12To14: %v", err)
	}
	// The wrong routing would set ServiceProviderName ← PublisherName ("AWS").
	if got12["ServiceProviderName"] != "AWS" {
		t.Fatalf("test setup: transform12To14 ServiceProviderName = %q, want AWS (from PublisherName)", got12["ServiceProviderName"])
	}
	if got13["ServiceProviderName"] == got12["ServiceProviderName"] {
		t.Errorf("the two transforms agree on ServiceProviderName (%q); the 1.3 pin would then not matter",
			got13["ServiceProviderName"])
	}
}

func TestTransform14To14(t *testing.T) {
	transform, err := TransformTo14(V1_4)
	if err != nil {
		t.Fatalf("TransformTo14(1.4): %v", err)
	}
	in := RawRecord{
		"BilledCost":          "1.2378912",
		"EffectiveCost":       "1.2378912",
		"ListCost":            "1.2378912",
		"ContractedCost":      "1.2378912",
		"BillingCurrency":     "USD",
		"ChargeCategory":      "Usage",
		"ServiceName":         "Claude API",
		"ServiceProviderName": "Anthropic",
		"InvoiceIssuerName":   "Anthropic",
		// Post-ANT-10-re-point enriched shape: SkuMeter is a friendly meter
		// name paired with a set SkuId (never the model id — that retired shape
		// was a latent 1.4 conformance bug, since SkuMeter must be null when
		// SkuId is null). Model identity lives in ChargeDescription.
		"SkuMeter":           "Input Tokens",
		"SkuId":              "anthropic/claude-opus-4-6/uncached_input_tokens/0-200k",
		"":                   "empty-key-dropped", // absent from columns14
		"x_SomethingCustom":  "dropped",           // not a 1.4 column
		"BillingAccountName": "",                  // empty stays absent
	}
	out, err := transform(in)
	if err != nil {
		t.Fatalf("identity transform: %v", err)
	}
	// The 1.4 columns pass through unchanged.
	for _, col := range []string{"BilledCost", "BillingCurrency", "ChargeCategory", "ServiceName", "ServiceProviderName", "InvoiceIssuerName", "SkuMeter", "SkuId"} {
		if out[col] != in[col] {
			t.Errorf("column %s = %q, want %q (identity)", col, out[col], in[col])
		}
	}
	// Non-1.4 and empty columns are dropped.
	for _, col := range []string{"x_SomethingCustom", "", "BillingAccountName"} {
		if _, ok := out[col]; ok {
			t.Errorf("column %q survived the identity transform, want dropped", col)
		}
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
		"Tags":                `{"user:env": "prod", "user:team": "platform", "user:opted-in": true, "user:owner": null}`,
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
	// Valueless (label-style) tags carry boolean true per the FOCUS spec;
	// null-valued tag keys are allowed too (KeyValueFormat).
	if rec.Tags["user:opted-in"] != true {
		t.Errorf("Tags = %v, want user:opted-in=true", rec.Tags)
	}
	if v, ok := rec.Tags["user:owner"]; !ok || v != nil {
		t.Errorf("Tags = %v, want user:owner present and null", rec.Tags)
	}

	bad := RawRecord{}
	for k, v := range raw {
		bad[k] = v
	}
	bad["BilledCost"] = "not-a-number"
	if _, err := ParseRecord(bad); err == nil {
		t.Error("ParseRecord accepted an unparseable BilledCost")
	}

	// KeyValueFormat forbids object and array tag values.
	bad = RawRecord{}
	for k, v := range raw {
		bad[k] = v
	}
	bad["Tags"] = `{"user:env": ["prod", "dev"]}`
	if _, err := ParseRecord(bad); err == nil {
		t.Error("ParseRecord accepted an array-valued tag")
	}
}
