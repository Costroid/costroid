// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Costroid Authors

package focus

import "fmt"

// Version identifies a released FOCUS specification version a source can
// declare for its data (decision D4).
type Version string

// Released FOCUS specification versions.
const (
	V1_0 Version = "1.0"
	V1_1 Version = "1.1"
	V1_2 Version = "1.2"
	V1_3 Version = "1.3"
	V1_4 Version = "1.4"
)

// Transform rewrites one RawRecord from a source FOCUS version into the
// internal FOCUS 1.4 column shape (decision D4). Transforms operate on
// raw string values — typed parsing happens afterwards in ParseRecord,
// once the record is 1.4-shaped and validated.
type Transform func(RawRecord) (RawRecord, error)

// transforms is the version-aware transform registry (decision D4): one
// slot per released FOCUS version, mapping data in that version to the
// internal 1.4 model. A nil slot is a recognized version whose transform
// is not yet implemented.
var transforms = map[Version]Transform{
	V1_0: nil, // registry slot — not yet implemented
	V1_1: nil, // registry slot — not yet implemented
	V1_2: transform12To14,
	V1_3: nil, // registry slot — not yet implemented
	V1_4: transform14To14,
}

// TransformTo14 returns the transform normalizing data of the given
// source FOCUS version into the internal 1.4 model.
func TransformTo14(v Version) (Transform, error) {
	t, ok := transforms[v]
	if !ok {
		return nil, fmt.Errorf("unknown FOCUS version %q", v)
	}
	if t == nil {
		return nil, fmt.Errorf("FOCUS %s → 1.4 transform is not implemented yet", v)
	}
	return t, nil
}

// columns14 are the FOCUS 1.4 CostAndUsage columns this slice carries
// (the CostRecord fields). Version transforms copy these through when the
// column name is unchanged between the source version and 1.4.
var columns14 = []string{
	"AvailabilityZone",
	"BilledCost",
	"BillingAccountId",
	"BillingAccountName",
	"BillingAccountType",
	"BillingCurrency",
	"BillingPeriodEnd",
	"BillingPeriodStart",
	"ChargeCategory",
	"ChargeClass",
	"ChargeDescription",
	"ChargeFrequency",
	"ChargePeriodEnd",
	"ChargePeriodStart",
	"ConsumedQuantity",
	"ConsumedUnit",
	"ContractedCost",
	"ContractedUnitPrice",
	"EffectiveCost",
	"HostProviderName",
	"InvoiceId",
	"InvoiceIssuerName",
	"ListCost",
	"ListUnitPrice",
	"PricingCategory",
	"PricingQuantity",
	"PricingUnit",
	"RegionId",
	"RegionName",
	"ResourceId",
	"ResourceName",
	"ResourceType",
	"ServiceCategory",
	"ServiceName",
	"ServiceProviderName",
	"ServiceSubcategory",
	"SkuId",
	"SkuMeter",
	"SkuPriceId",
	"SubAccountId",
	"SubAccountName",
	"SubAccountType",
	"Tags",
}

// transform14To14 is the identity transform for data already in the
// internal FOCUS 1.4 shape: it copies the 1.4 columns this slice carries
// through unchanged and drops anything else. It is what lets a connector
// SYNTHESIZE 1.4-shaped RawRecords directly — the AI-vendor connectors
// (anthropic-cost, openai-cost) are the repo's first non-FOCUS sources,
// with no upstream FOCUS export to transform — and still flow through the
// one shared read → transform → validate → replace pipeline. Declaring
// focus.V1_4 selects this transform; the connector owns the source→1.4
// mapping in its own godoc rule table.
func transform14To14(raw RawRecord) (RawRecord, error) {
	out := make(RawRecord, len(columns14))
	for _, col := range columns14 {
		if v, ok := raw[col]; ok && v != "" {
			out[col] = v
		}
	}
	return out, nil
}

// transform12To14 normalizes a FOCUS 1.2 record into the 1.4 shape.
//
// The only column-level difference between 1.2 and 1.4 that affects the
// columns this slice carries is participating-entity identification:
// FOCUS 1.3 deprecated ProviderName and PublisherName (definitional
// conflicts) and 1.4 removed them, migrating their use cases to
// ServiceProviderName, HostProviderName, InvoiceIssuerName, and the
// DataGenerator metadata. Following the spec's participating-entity
// guidance (FOCUS 1.4 §2.17 and the Participating Entity Identification
// Examples appendix):
//
//   - ServiceProviderName ← PublisherName ("the entity that produced the
//     resources or services") when present, else ProviderName. For a
//     native cloud charge both name the CSP; for a marketplace charge the
//     publisher is the marketplace seller, which 1.4 defines as the
//     service provider.
//   - HostProviderName ← ProviderName: the entity that made the
//     resources available for purchase in a 1.2 export is the CSP
//     operating the underlying infrastructure.
//   - InvoiceIssuerName already exists in 1.2 and passes through.
//
// Columns added after 1.2 (allocation, ContractApplied, ...) have no 1.2
// source and stay absent. Provider-proprietary x_ columns in the source
// (e.g. AWS x_Discounts) are not part of the internal model and are
// dropped.
func transform12To14(raw RawRecord) (RawRecord, error) {
	out := make(RawRecord, len(columns14))
	for _, col := range columns14 {
		if v, ok := raw[col]; ok && v != "" {
			out[col] = v
		}
	}

	provider, publisher := raw["ProviderName"], raw["PublisherName"]
	if publisher != "" {
		out["ServiceProviderName"] = publisher
	} else if provider != "" {
		out["ServiceProviderName"] = provider
	}
	if provider != "" {
		out["HostProviderName"] = provider
	}
	return out, nil
}
