package export

import (
	"bytes"
	"fmt"
	"slices"
)

// Harness's custom cost provider page (https://developer.harness.io/cloud-cost-management/integrations/custom-cost-provider,
// retrieved 2026-11-12) states: "All uploads must follow the FOCUSv1
// specification. The following fields are mandatory." Its link for FOCUSv1 is
// FOCUS 1.1. It states "File size limit: 20 MB per upload", and of
// BillingPeriodStart, "Must match invoice month on upload". The page does not
// say whether a megabyte is 10^6 or 2^20 bytes, so a file stays within
// 20,000,000, and a file holds one billing period.
const harnessMaxBytes = 20000000

// harnessColumns are the page's fields in its order, each read from the row's
// column of the same name but ProviderName, which is ServiceProviderName.
// ResourceId is null: a row is no resource. BillingCurrency and ConsumedUnit
// follow them. The page lists neither, and FOCUS 1.1 denominates EffectiveCost
// in BillingCurrency and counts ConsumedQuantity in ConsumedUnit, so each
// figure keeps its currency and each count its unit.
var harnessColumns = []string{
	"BillingAccountId", "BillingAccountName", "BillingPeriodStart", "BillingPeriodEnd", "ChargeCategory",
	"ChargePeriodStart", "ChargePeriodEnd", "ConsumedQuantity", "EffectiveCost", "ProviderName",
	"RegionName", "ResourceId", "ServiceName", "SkuId", "SubAccountId",
	"SubAccountName", "Tags", "BillingCurrency", "ConsumedUnit",
}

// harnessSource is the row's column a Harness column is read from, where the
// two names differ, and "" for ResourceId, which a row has none of.
var harnessSource = map[string]string{"ProviderName": "ServiceProviderName", "ResourceId": ""}

// harnessRequired are the columns the page states "no nulls" of, and
// BillingCurrency, which FOCUS 1.1 requires of every row.
var harnessRequired = []string{
	"BillingAccountId", "BillingPeriodStart", "BillingPeriodEnd", "ChargeCategory", "ChargePeriodStart",
	"ChargePeriodEnd", "EffectiveCost", "ProviderName", "ServiceName", "BillingCurrency",
}

// harnessCategories are ChargeCategory's "Allowed values" on the page.
var harnessCategories = []string{"Usage", "Purchase", "Tax", "Credit", "Adjustment"}

// writeHarness writes one file for each billing period the rows name, in
// order, and a further file each time the next row would pass 20,000,000
// bytes. Each file starts with the header.
func writeHarness(rows table) (Export, error) {
	header, err := encodeRecord(harnessColumns)
	if err != nil {
		return Export{}, err
	}
	periods, groups, err := groupRows(rows.rows(), harnessPeriod)
	if err != nil {
		return Export{}, err
	}
	var files []File
	for _, period := range periods {
		note := "billing period " + period
		current := File{Body: bytes.Clone(header), Note: note}
		for _, source := range groups[period] {
			line, err := encodeRecord(project(source, harnessColumns, harnessSource))
			if err != nil {
				return Export{}, err
			}
			if len(header)+len(line) > harnessMaxBytes {
				return Export{}, &RowsError{Format: Harness, Rows: []string{source.name()}, Reason: fmt.Sprintf("it takes %d bytes with the header, past Harness's %d an upload", len(header)+len(line), harnessMaxBytes)}
			}
			if len(current.Body)+len(line) > harnessMaxBytes {
				files = append(files, current)
				current = File{Body: bytes.Clone(header), Note: note}
			}
			current.Body = append(current.Body, line...)
			current.Rows++
		}
		files = append(files, current)
	}
	return Export{Files: files}, nil
}

// harnessPeriod is a row's billing period, from its start to its end, and
// refuses a row Harness would not take.
func harnessPeriod(source row) (string, error) {
	record := project(source, harnessColumns, harnessSource)
	for _, column := range harnessRequired {
		if record[slices.Index(harnessColumns, column)] == "" {
			return "", &RowsError{Format: Harness, Rows: []string{source.name()}, Reason: column + " is blank, and Harness takes no null there"}
		}
	}
	if category := source.cell("ChargeCategory"); !slices.Contains(harnessCategories, category) {
		return "", &RowsError{Format: Harness, Rows: []string{source.name()}, Reason: "ChargeCategory " + category + " is none of Harness's allowed values"}
	}
	for _, column := range []string{"BillingPeriodStart", "BillingPeriodEnd", "ChargePeriodStart", "ChargePeriodEnd"} {
		if _, err := instant(Harness, source, column); err != nil {
			return "", err
		}
	}
	return source.cell("BillingPeriodStart") + " to " + source.cell("BillingPeriodEnd"), nil
}
