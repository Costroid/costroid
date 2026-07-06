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
	V1_0: transform10To14,
	V1_1: transform11To14,
	V1_2: transform12To14,
	V1_3: transform13To14,
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

// transform13To14 normalizes a FOCUS 1.3 record into the internal 1.4 shape.
//
// The 1.3 → 1.4 delta is DROP-ONLY for the columns this slice carries: no
// column was renamed between the two versions (they share every carried
// Column ID byte-for-byte). 1.4 only removed ProviderName and PublisherName
// (deprecated in 1.3) and added CommitmentProgramEligibilityDetails and
// InvoiceDetailId (both Conditional and nullable, so their absence in
// up-converted 1.3 data is conformant — no gap-fill). Everything 1.3 added
// over 1.2 that 1.4 keeps — the Allocated* split/shared-cost columns and
// ContractApplied — is outside Costroid's carried set (columns14) too.
//
// So the transform is behaviorally the identity transform: copy the carried
// 1.4 columns through and drop everything else. That naturally drops
// ProviderName/PublisherName and the Allocated*/ContractApplied 1.3
// additions, while letting the NATIVE 1.3 successor columns
// (ServiceProviderName, HostProviderName, InvoiceIssuerName) pass through
// unchanged.
//
// CRITICAL: 1.3 must NOT be routed through transform12To14. In 1.3,
// ProviderName and PublisherName are Mandatory and non-null on EVERY row, so
// transform12To14's entity mapping would OVERWRITE the native 1.3
// ServiceProviderName ← PublisherName and HostProviderName ← ProviderName on
// every row — corrupting marketplace rows where the deprecated and successor
// columns legitimately diverge (e.g. a native ServiceProviderName that is not
// the PublisherName). The identity path preserves the source's own successor
// values, which is what a 1.3 export already carries correctly.
//
// Column-ID lists were generated from the FOCUS spec column .md files at tags
// v1.3 and v1.4:
// https://github.com/FinOps-Open-Cost-and-Usage-Spec/FOCUS_Spec/tree/v1.3/specification/datasets/cost_and_usage/columns
// https://github.com/FinOps-Open-Cost-and-Usage-Spec/FOCUS_Spec/tree/v1.4/specification/datasets/cost_and_usage/columns
func transform13To14(raw RawRecord) (RawRecord, error) {
	return transform14To14(raw)
}

// transform10To14 normalizes a FOCUS 1.0 record into the internal 1.4 shape by
// delegating to transform12To14, and transform11To14 does the same for 1.1.
//
// WHY reuse is SAFE — and CORRECT — for 1.0 and 1.1:
//
// The 1.0 Column ID set is a strict SUBSET of 1.2's (1.0 ⊂ 1.1 ⊂ 1.2) with ZERO
// renames and ZERO removals: 1.1 adds seven columns to 1.0 and 1.2 adds more to
// 1.1, and nothing carried by 1.0/1.1 changed name or meaning by 1.2. Crucially
// for the entity mapping, ProviderName and PublisherName exist and are
// Mandatory-non-null in 1.0 and 1.1 exactly as in 1.2, while their 1.3+
// successors (ServiceProviderName, HostProviderName) do NOT exist yet. So
// transform12To14's mapping — ServiceProviderName ← PublisherName (else
// ProviderName), HostProviderName ← ProviderName — is purely ADD-ONLY on a
// 1.0/1.1 record: there is no native successor column for it to clobber.
//
// CRITICAL CONTRAST TO 1.3 (the inverse landmine): routing 1.3 through
// transform12To14 is a BUG because 1.3 rows carry native, Mandatory-non-null
// ServiceProviderName/HostProviderName that the 1.2 mapping would OVERWRITE
// (see transform13To14). Routing 1.0/1.1 through transform12To14 is the OPPOSITE
// — it is exactly what synthesizes those successor columns, which the source
// legitimately lacks. Same delegation target, opposite reason it is right.
//
// Column-ID lists were generated from the FOCUS spec column .md files at tags
// v1.0 and v1.1 (both under the flat pre-1.3 specification/columns/ path):
// https://github.com/FinOps-Open-Cost-and-Usage-Spec/FOCUS_Spec/tree/v1.0/specification/columns
// https://github.com/FinOps-Open-Cost-and-Usage-Spec/FOCUS_Spec/tree/v1.1/specification/columns
func transform10To14(raw RawRecord) (RawRecord, error) {
	return transform12To14(raw)
}

// transform11To14 normalizes a FOCUS 1.1 record into the internal 1.4 shape.
// 1.1 = 1.0 plus seven columns, still a strict subset of 1.2 with zero renames;
// the reuse is safe for the same reason as transform10To14 (which see).
func transform11To14(raw RawRecord) (RawRecord, error) {
	return transform12To14(raw)
}
