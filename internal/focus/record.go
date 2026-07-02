// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Costroid Authors

// Package focus implements Costroid's FOCUS engine: the internal cost
// schema mirroring FOCUS 1.4 (decisions D3, D4), version-aware transforms
// from older FOCUS versions, and scoped conformance validation.
package focus

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/shopspring/decimal"
)

// SpecVersion is the FOCUS specification version the internal model
// mirrors (decision D4).
const SpecVersion = Version("1.4")

// TenantColumn is the FOCUS custom-column name carrying the tenant
// identifier (decision D15). Per the FOCUS spec, provider/consumer
// custom columns use the "x_" prefix.
const TenantColumn = "x_TenantId"

// DefaultTenant is the tenant identifier the single-tenant OSS core
// applies at ingest (decision D15).
const DefaultTenant = "default"

// ChargeCategories is the allowed value set for ChargeCategory per FOCUS
// 1.4 (requirements-model rule CAU-ChargeCategory-C-003-M).
var ChargeCategories = []string{"Usage", "Purchase", "Tax", "Credit", "Adjustment"}

// ChargeClassCorrection is the only non-null ChargeClass value per FOCUS
// 1.4: a charge correcting a charge in a previously CLOSED billing
// period. Correction rows are additive — they arrive in a later open
// billing period while keeping the ORIGINAL incurred timeframe in
// ChargePeriodStart/End, so ChargePeriod-based aggregation retroactively
// adjusts the corrected days (decision D26).
const ChargeClassCorrection = "Correction"

// CostRecord is one row of the internal Cost and Usage dataset. It
// mirrors the FOCUS 1.4 column library (decision D3): every field
// corresponds to a FOCUS 1.4 CostAndUsage column, plus the x_TenantId
// custom column (decision D15). This slice carries the columns AWS FOCUS
// exports populate; remaining 1.4 columns (allocation, commitment
// discounts, capacity reservations, pricing currency, ...) are added in
// later slices — never as a parallel proprietary schema.
//
// Conventions:
//   - Monetary and quantity columns are exact decimals — never floats —
//     so no precision is lost between export and query.
//   - Timestamps are UTC.
//   - Nullable string columns represent null as the empty string.
//   - Nullable numeric columns use decimal.NullDecimal.
type CostRecord struct {
	// XTenantID is the x_TenantId custom column (decision D15). It is
	// applied at ingest, not read from the source.
	XTenantID string

	// Billing account.
	BillingAccountID   string
	BillingAccountName string
	BillingAccountType string
	SubAccountID       string
	SubAccountName     string
	SubAccountType     string

	// Participating entities. FOCUS 1.4 removed ProviderName and
	// PublisherName (deprecated in 1.3); these successor columns
	// identify the entities instead.
	ServiceProviderName string
	HostProviderName    string
	InvoiceIssuerName   string

	// Billing period and currency.
	BillingCurrency    string
	BillingPeriodStart time.Time
	BillingPeriodEnd   time.Time
	InvoiceID          string

	// Charge.
	ChargeCategory    string
	ChargeClass       string
	ChargeDescription string
	ChargeFrequency   string
	ChargePeriodStart time.Time
	ChargePeriodEnd   time.Time

	// Monetary columns (must-not-be-null per FOCUS 1.4).
	BilledCost     decimal.Decimal
	EffectiveCost  decimal.Decimal
	ListCost       decimal.Decimal
	ContractedCost decimal.Decimal

	// Pricing.
	PricingCategory     string
	PricingQuantity     decimal.NullDecimal
	PricingUnit         string
	ListUnitPrice       decimal.NullDecimal
	ContractedUnitPrice decimal.NullDecimal

	// Usage.
	ConsumedQuantity decimal.NullDecimal
	ConsumedUnit     string

	// Service.
	ServiceName        string
	ServiceCategory    string
	ServiceSubcategory string

	// SKU.
	SkuID      string
	SkuPriceID string
	SkuMeter   string

	// Resource.
	ResourceID   string
	ResourceName string
	ResourceType string

	// Location.
	RegionID         string
	RegionName       string
	AvailabilityZone string

	// Tags is the FOCUS Tags column as a key→value map. Per the spec's
	// KeyValueFormat attribute, values are JSON scalars — string, number,
	// boolean, or null — never objects or arrays (valueless label-style
	// tags carry boolean true). Values are string, bool, json.Number, or
	// nil; numbers stay json.Number so they round-trip exactly.
	Tags map[string]any
}

// RawRecord is one source row as raw column-name→value strings, before
// typed parsing. Connectors yield RawRecords in their declared FOCUS
// version's column shape; version transforms rewrite them into the FOCUS
// 1.4 shape. A missing key and an empty value both mean null.
type RawRecord map[string]string

// ParseRecord converts a FOCUS-1.4-shaped RawRecord into a typed
// CostRecord. It assumes the record has passed Validate; a validated
// record always parses, but errors are still reported (never dropped)
// for defense in depth.
func ParseRecord(raw RawRecord) (*CostRecord, error) {
	rec := &CostRecord{
		BillingAccountID:    raw["BillingAccountId"],
		BillingAccountName:  raw["BillingAccountName"],
		BillingAccountType:  raw["BillingAccountType"],
		SubAccountID:        raw["SubAccountId"],
		SubAccountName:      raw["SubAccountName"],
		SubAccountType:      raw["SubAccountType"],
		ServiceProviderName: raw["ServiceProviderName"],
		HostProviderName:    raw["HostProviderName"],
		InvoiceIssuerName:   raw["InvoiceIssuerName"],
		BillingCurrency:     raw["BillingCurrency"],
		InvoiceID:           raw["InvoiceId"],
		ChargeCategory:      raw["ChargeCategory"],
		ChargeClass:         raw["ChargeClass"],
		ChargeDescription:   raw["ChargeDescription"],
		ChargeFrequency:     raw["ChargeFrequency"],
		PricingCategory:     raw["PricingCategory"],
		PricingUnit:         raw["PricingUnit"],
		ConsumedUnit:        raw["ConsumedUnit"],
		ServiceName:         raw["ServiceName"],
		ServiceCategory:     raw["ServiceCategory"],
		ServiceSubcategory:  raw["ServiceSubcategory"],
		SkuID:               raw["SkuId"],
		SkuPriceID:          raw["SkuPriceId"],
		SkuMeter:            raw["SkuMeter"],
		ResourceID:          raw["ResourceId"],
		ResourceName:        raw["ResourceName"],
		ResourceType:        raw["ResourceType"],
		RegionID:            raw["RegionId"],
		RegionName:          raw["RegionName"],
		AvailabilityZone:    raw["AvailabilityZone"],
	}

	var err error
	parse := func(field string, dst *decimal.Decimal) {
		if err != nil {
			return
		}
		var d decimal.Decimal
		if d, err = ParseDecimal(raw[field]); err != nil {
			err = fmt.Errorf("column %s: %w", field, err)
			return
		}
		*dst = d
	}
	parseNull := func(field string, dst *decimal.NullDecimal) {
		if err != nil || raw[field] == "" {
			return
		}
		var d decimal.Decimal
		if d, err = ParseDecimal(raw[field]); err != nil {
			err = fmt.Errorf("column %s: %w", field, err)
			return
		}
		*dst = decimal.NullDecimal{Decimal: d, Valid: true}
	}
	parseTime := func(field string, dst *time.Time) {
		if err != nil {
			return
		}
		var t time.Time
		if t, err = ParseTime(raw[field]); err != nil {
			err = fmt.Errorf("column %s: %w", field, err)
			return
		}
		*dst = t
	}

	parse("BilledCost", &rec.BilledCost)
	parse("EffectiveCost", &rec.EffectiveCost)
	parse("ListCost", &rec.ListCost)
	parse("ContractedCost", &rec.ContractedCost)
	parseNull("PricingQuantity", &rec.PricingQuantity)
	parseNull("ListUnitPrice", &rec.ListUnitPrice)
	parseNull("ContractedUnitPrice", &rec.ContractedUnitPrice)
	parseNull("ConsumedQuantity", &rec.ConsumedQuantity)
	parseTime("BillingPeriodStart", &rec.BillingPeriodStart)
	parseTime("BillingPeriodEnd", &rec.BillingPeriodEnd)
	parseTime("ChargePeriodStart", &rec.ChargePeriodStart)
	parseTime("ChargePeriodEnd", &rec.ChargePeriodEnd)
	if err != nil {
		return nil, err
	}

	if tags := raw["Tags"]; tags != "" {
		parsed, err := ParseTags(tags)
		if err != nil {
			return nil, fmt.Errorf("column Tags: %w", err)
		}
		rec.Tags = parsed
	}
	return rec, nil
}

// ParseTags parses a serialized FOCUS Tags value into a key→value map,
// enforcing the KeyValueFormat attribute: a single JSON object whose keys
// are unique (ATT-KeyValueFormat-A-002-M) and whose values are string,
// number, boolean, or null — never objects or arrays. Numbers are kept as
// json.Number so they round-trip exactly. It walks the tokens itself
// because encoding/json's map decoding silently keeps the last duplicate
// key and ignores trailing data after the object.
func ParseTags(s string) (map[string]any, error) {
	dec := json.NewDecoder(strings.NewReader(s))
	dec.UseNumber()

	open, err := dec.Token()
	if err != nil {
		return nil, fmt.Errorf("parsing key-value JSON: %w", err)
	}
	if open != json.Delim('{') {
		if open == nil {
			return nil, errors.New("key-value JSON is null; KeyValueFormat requires an object")
		}
		return nil, fmt.Errorf("key-value JSON is not an object (starts with %v)", open)
	}

	tags := map[string]any{}
	for dec.More() {
		keyTok, err := dec.Token()
		if err != nil {
			return nil, fmt.Errorf("parsing key-value JSON: %w", err)
		}
		key, ok := keyTok.(string)
		if !ok {
			return nil, fmt.Errorf("parsing key-value JSON: object key %v is not a string", keyTok)
		}
		if _, dup := tags[key]; dup {
			return nil, fmt.Errorf("duplicate tag key %q; KeyValueFormat requires unique keys", key)
		}
		valTok, err := dec.Token()
		if err != nil {
			return nil, fmt.Errorf("parsing key-value JSON: %w", err)
		}
		switch v := valTok.(type) {
		case json.Delim:
			kind := "object"
			if v == json.Delim('[') {
				kind = "array"
			}
			return nil, fmt.Errorf("tag %q has a JSON %s value; KeyValueFormat allows string, number, boolean, or null", key, kind)
		default:
			tags[key] = valTok
		}
	}
	if _, err := dec.Token(); err != nil { // the closing '}'
		return nil, fmt.Errorf("parsing key-value JSON: %w", err)
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return nil, errors.New("trailing data after the key-value object; KeyValueFormat is a single JSON object")
	}
	return tags, nil
}

// ParseDecimal parses a FOCUS numeric value without precision loss.
func ParseDecimal(s string) (decimal.Decimal, error) {
	d, err := decimal.NewFromString(strings.TrimSpace(s))
	if err != nil {
		return decimal.Decimal{}, fmt.Errorf("%q is not a valid decimal", s)
	}
	return d, nil
}

// ParseTime parses a FOCUS date/time value (ISO 8601 / RFC 3339 with
// optional fractional seconds, e.g. "2026-06-01T00:00:00.000Z") and
// normalizes it to UTC.
func ParseTime(s string) (time.Time, error) {
	t, err := time.Parse(time.RFC3339, strings.TrimSpace(s))
	if err != nil {
		return time.Time{}, fmt.Errorf("%q is not a valid ISO 8601 date/time", s)
	}
	return t.UTC(), nil
}
