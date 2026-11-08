package export

import (
	"bytes"
	"fmt"
)

// Vantage takes a custom provider's costs as a CSV of FOCUS columns, at most
// 10,000 rows and 2 MB a file (https://docs.vantage.sh/connecting_custom_providers,
// retrieved 2026-11-08). The page does not say whether a megabyte is 10^6 or
// 2^20 bytes, so a file stays within 2,000,000.
const (
	vantageMaxRows  = 10000
	vantageMaxBytes = 2000000
)

// vantageColumns are the page's columns that a row fills, in the page's
// order, each taken from the row's column of the same name but BilledCost.
// ResourceId and ResourceType are left out, because a row has neither.
//
// BilledCost is the row's EffectiveCost, and EffectiveCost is left out. A row's
// own BilledCost is 0, because Costroid issues no invoice. The page states that
// when EffectiveCost is "provided and different from BilledCost, Vantage
// records the row at EffectiveCost and adds a separate Discount charge for the
// BilledCost − EffectiveCost difference", so the row's two columns as they are
// would add a Discount of minus the figure that no provider gave.
var vantageColumns = []string{
	"ChargeCategory",
	"ChargePeriodStart",
	"BilledCost",
	"ServiceName",
	"ChargePeriodEnd",
	"BillingCurrency",
	"ConsumedQuantity",
	"ConsumedUnit",
	"BillingAccountId",
	"SubAccountId",
	"RegionId",
	"RegionName",
	"ServiceCategory",
	"ServiceSubcategory",
	"Tags",
}

// vantageSource is the row's column a Vantage column takes.
func vantageSource(column string) string {
	if column == "BilledCost" {
		return "EffectiveCost"
	}
	return column
}

// writeVantage writes the rows as Vantage's CSV, a new file each time the next
// row would pass 10,000 rows or 2,000,000 bytes. Each file starts with the
// header.
func writeVantage(rows table) (Export, error) {
	header, err := encodeRecord(vantageColumns)
	if err != nil {
		return Export{}, err
	}
	var files []File
	current := File{Body: bytes.Clone(header)}
	for _, source := range rows.rows() {
		record := make([]string, len(vantageColumns))
		for i, column := range vantageColumns {
			record[i] = source.cell(vantageSource(column))
		}
		line, err := encodeRecord(record)
		if err != nil {
			return Export{}, err
		}
		if len(header)+len(line) > vantageMaxBytes {
			return Export{}, &RowsError{Format: Vantage, Rows: []string{source.name()}, Reason: fmt.Sprintf("it takes %d bytes with the header, past Vantage's %d a file", len(header)+len(line), vantageMaxBytes)}
		}
		if current.Rows == vantageMaxRows || len(current.Body)+len(line) > vantageMaxBytes {
			files = append(files, current)
			current = File{Body: bytes.Clone(header)}
		}
		current.Body = append(current.Body, line...)
		current.Rows++
	}
	return Export{Files: append(files, current)}, nil
}
