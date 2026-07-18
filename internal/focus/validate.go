// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Costroid Authors

package focus

import (
	"fmt"
	"regexp"
	"slices"
	"strings"
)

// MaxFreeTextBytes bounds every ingested FOCUS field value. It is a Cardinal-Rule
// (decision D7) SIZE tripwire: the longest legitimate value is a worst-case cloud
// resource ARN (~2 KB), so 8 KiB clears real metadata with wide margin, while a
// bulk prompt or response dump (many KB) is rejected at ingest rather than
// persisted. It is not a content classifier; content shorter than the bound is
// out of its scope.
const MaxFreeTextBytes = 8192

// Rule is one FOCUS conformance check applied to a 1.4-shaped RawRecord.
// IDs reference the FOCUS 1.4 requirements model (model-1.4.json, e.g.
// CAU-ChargeCategory-C-003-M) so further FOCUS-Validator-style rules slot
// in alongside these. A Costroid-specific persistence guard instead uses a
// COSTROID-* prefix (e.g. COSTROID-FieldLength-001), not a FOCUS CAU-* id,
// because it is not a FOCUS conformance requirement. Check returns nil when
// the record satisfies the rule.
type Rule struct {
	ID          string
	Description string
	Check       func(RawRecord) error
}

// Violation is one failed rule on one record.
type Violation struct {
	RuleID string
	Err    error
}

func (v Violation) Error() string { return fmt.Sprintf("[%s] %v", v.RuleID, v.Err) }

// DefaultRules returns the conformance rule set this slice implements —
// deliberately scoped to the constraints the ingest pipeline relies on
// (required columns non-null, ChargeCategory allowed values, charge
// period ordering, currency code, parseable decimals and timestamps),
// not the full FOCUS-Validator rule set. Later slices append further
// rules to this table.
func DefaultRules() []Rule {
	rules := []Rule{
		{
			ID:          "CAU-ChargeCategory-C-003-M",
			Description: "ChargeCategory MUST be one of the allowed values.",
			Check: func(r RawRecord) error {
				if v := r["ChargeCategory"]; v != "" && !slices.Contains(ChargeCategories, v) {
					return fmt.Errorf("ChargeCategory %q is not one of %s", v, strings.Join(ChargeCategories, ", "))
				}
				return nil
			},
		},
		{
			ID:          "CAU-ChargeClass-C-005-C",
			Description: `ChargeClass MUST be "Correction" when ChargeClass is not null.`,
			Check: func(r RawRecord) error {
				if v := r["ChargeClass"]; v != "" && v != ChargeClassCorrection {
					return fmt.Errorf("ChargeClass %q is not allowed; the only non-null value is %q", v, ChargeClassCorrection)
				}
				return nil
			},
		},
		{
			ID:          "CAU-ChargePeriodEnd-C-004-M",
			Description: "ChargePeriodEnd MUST be the exclusive end bound of the effective period of the charge (after ChargePeriodStart).",
			Check: func(r RawRecord) error {
				start, err1 := ParseTime(r["ChargePeriodStart"])
				end, err2 := ParseTime(r["ChargePeriodEnd"])
				if err1 != nil || err2 != nil {
					return nil // unparseable periods are reported by the type rules
				}
				if !end.After(start) {
					return fmt.Errorf("ChargePeriodEnd %s is not after ChargePeriodStart %s", r["ChargePeriodEnd"], r["ChargePeriodStart"])
				}
				return nil
			},
		},
		{
			ID:          "CAU-BillingCurrency-C-006-M",
			Description: "BillingCurrency MUST be expressed in national currency (e.g., USD, EUR) when costs are present.",
			Check: func(r RawRecord) error {
				hasCost := false
				for _, col := range []string{"BilledCost", "EffectiveCost", "ListCost", "ContractedCost"} {
					if r[col] != "" {
						hasCost = true
						break
					}
				}
				if v := r["BillingCurrency"]; hasCost && v != "" && !currencyCode.MatchString(v) {
					return fmt.Errorf("BillingCurrency %q is not a valid ISO 4217 currency code", v)
				}
				return nil
			},
		},
	}

	// Required columns: present and non-null (an absent column and an
	// empty value both violate the nullability requirement).
	for _, c := range []struct{ id, col string }{
		{"CAU-BilledCost-C-003-M", "BilledCost"},
		{"CAU-EffectiveCost-C-003-M", "EffectiveCost"},
		{"CAU-ListCost-C-003-M", "ListCost"},
		{"CAU-ContractedCost-C-003-M", "ContractedCost"},
		{"CAU-BillingCurrency-C-004-M", "BillingCurrency"},
		{"CAU-ChargeCategory-C-002-M", "ChargeCategory"},
		{"CAU-ChargePeriodStart-C-003-M", "ChargePeriodStart"},
		{"CAU-ChargePeriodEnd-C-003-M", "ChargePeriodEnd"},
		{"CAU-BillingPeriodStart-C-003-M", "BillingPeriodStart"},
		{"CAU-BillingPeriodEnd-C-003-M", "BillingPeriodEnd"},
		{"CAU-BillingAccountId-C-001-M", "BillingAccountId"},
		{"CAU-ServiceName-C-003-M", "ServiceName"},
		{"CAU-ServiceCategory-C-002-M", "ServiceCategory"},
		{"CAU-ServiceProviderName-C-003-M", "ServiceProviderName"},
		{"CAU-InvoiceIssuerName-C-003-M", "InvoiceIssuerName"},
	} {
		rules = append(rules, Rule{
			ID:          c.id,
			Description: c.col + " MUST NOT be null.",
			Check:       notNull(c.col),
		})
	}

	rules = append(rules, Rule{
		ID:          "CAU-Tags-C-001-M",
		Description: "Tags MUST conform to KeyValueFormat requirements.",
		Check: func(r RawRecord) error {
			if v := r["Tags"]; v != "" {
				if _, err := ParseTags(v); err != nil {
					return fmt.Errorf("column Tags: %w", err)
				}
			}
			return nil
		},
	})

	rules = append(rules, Rule{
		ID:          "COSTROID-FieldLength-001",
		Description: "No ingested field value may exceed the Cardinal-Rule size bound (a bulk content dump is rejected, never persisted).",
		Check: func(r RawRecord) error {
			keys := make([]string, 0, len(r))
			for k := range r {
				if k != "Tags" {
					keys = append(keys, k)
				}
			}
			slices.Sort(keys)
			for _, col := range keys {
				if n := len(r[col]); n > MaxFreeTextBytes {
					return fmt.Errorf("%s is %d bytes, over the %d-byte field size bound (rejected as a possible content leak, not persisted)", col, n, MaxFreeTextBytes)
				}
			}
			if v := r["Tags"]; v != "" {
				// Only length-check parseable Tags; a malformed cell is already
				// reported by CAU-Tags-C-001-M. Bound each key and each string value,
				// never the whole cell. Do NOT echo the key or value into the error
				// (a key can itself be large) - report only a byte count.
				if tags, err := ParseTags(v); err == nil {
					tkeys := make([]string, 0, len(tags))
					for k := range tags {
						tkeys = append(tkeys, k)
					}
					slices.Sort(tkeys)
					for _, key := range tkeys {
						if n := len(key); n > MaxFreeTextBytes {
							return fmt.Errorf("column Tags: a key is %d bytes, over the %d-byte field size bound", n, MaxFreeTextBytes)
						}
						if s, ok := tags[key].(string); ok && len(s) > MaxFreeTextBytes {
							return fmt.Errorf("column Tags: a value is %d bytes, over the %d-byte field size bound", len(s), MaxFreeTextBytes)
						}
					}
				}
			}
			return nil
		},
	})

	// Decimal columns: parseable as exact decimals when present.
	for _, c := range []struct{ id, col string }{
		{"CAU-BilledCost-C-001-M", "BilledCost"},
		{"CAU-EffectiveCost-C-001-M", "EffectiveCost"},
		{"CAU-ListCost-C-001-M", "ListCost"},
		{"CAU-ContractedCost-C-001-M", "ContractedCost"},
		{"CAU-PricingQuantity-C-001-M", "PricingQuantity"},
		{"CAU-ConsumedQuantity-C-001-M", "ConsumedQuantity"},
		{"CAU-ListUnitPrice-C-001-M", "ListUnitPrice"},
		{"CAU-ContractedUnitPrice-C-002-M", "ContractedUnitPrice"},
	} {
		rules = append(rules, Rule{
			ID:          c.id,
			Description: c.col + " MUST be of type Decimal.",
			Check:       decimalTyped(c.col),
		})
	}

	// Date/time columns: parseable as ISO 8601 date/times when present.
	for _, c := range []struct{ id, col string }{
		{"CAU-ChargePeriodStart-C-001-M", "ChargePeriodStart"},
		{"CAU-ChargePeriodEnd-C-001-M", "ChargePeriodEnd"},
		{"CAU-BillingPeriodStart-C-001-M", "BillingPeriodStart"},
		{"CAU-BillingPeriodEnd-C-001-M", "BillingPeriodEnd"},
	} {
		rules = append(rules, Rule{
			ID:          c.id,
			Description: c.col + " MUST be of type Date/Time.",
			Check:       dateTimeTyped(c.col),
		})
	}
	return rules
}

// Validate applies the rules to one 1.4-shaped record and returns every
// violation. Aborting the ingest on violations (no partial loads) is the
// pipeline's responsibility.
func Validate(raw RawRecord, rules []Rule) []Violation {
	var violations []Violation
	for _, rule := range rules {
		if err := rule.Check(raw); err != nil {
			violations = append(violations, Violation{RuleID: rule.ID, Err: err})
		}
	}
	return violations
}

var currencyCode = regexp.MustCompile(`^[A-Z]{3}$`)

func notNull(col string) func(RawRecord) error {
	return func(r RawRecord) error {
		if r[col] == "" {
			return fmt.Errorf("%s is null", col)
		}
		return nil
	}
}

func decimalTyped(col string) func(RawRecord) error {
	return func(r RawRecord) error {
		if v := r[col]; v != "" {
			if _, err := ParseDecimal(v); err != nil {
				return fmt.Errorf("%s: %w", col, err)
			}
		}
		return nil
	}
}

func dateTimeTyped(col string) func(RawRecord) error {
	return func(r RawRecord) error {
		if v := r[col]; v != "" {
			if _, err := ParseTime(v); err != nil {
				return fmt.Errorf("%s: %w", col, err)
			}
		}
		return nil
	}
}
