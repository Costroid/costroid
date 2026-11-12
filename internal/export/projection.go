package export

import (
	"fmt"
	"slices"
	"time"

	"github.com/Costroid/costroid/internal/focus"
)

// A row's FOCUS 1.4 columns are 1.1's and 1.2's but in three places. FOCUS
// 1.4 has ServiceProviderName and HostProviderName, and 1.1 and 1.2 have
// neither. They have ProviderName and PublisherName, which 1.4 does not. So a
// 1.1 or 1.2 file reads both ProviderName and PublisherName from the row's
// ServiceProviderName, the entity that provides and, for a model's tokens,
// produces the service. HostProviderName has no column there, so it leaves
// as the custom column x_HostProviderName, after every FOCUS column as 1.1's
// column ordering asks. The columns are the specification's at the tags v1.1
// and v1.2 (https://github.com/FinOps-Open-Cost-and-Usage-Spec/FOCUS_Spec/tree/v1.1/specification/columns,
// retrieved 2026-11-12).
var focus11And12Columns = []string{
	"BilledCost", "BillingAccountId", "BillingAccountName", "BillingCurrency", "BillingPeriodEnd",
	"BillingPeriodStart", "ChargeCategory", "ChargeClass", "ChargeDescription", "ChargeFrequency",
	"ChargePeriodEnd", "ChargePeriodStart", "ConsumedQuantity", "ConsumedUnit", "ContractedCost",
	"ContractedUnitPrice", "EffectiveCost", "InvoiceIssuerName", "ListCost", "ListUnitPrice",
	"PricingCategory", "PricingQuantity", "PricingUnit", "ProviderName", "PublisherName",
	"RegionId", "RegionName", "ServiceCategory", "ServiceName", "ServiceSubcategory",
	"SkuId", "SkuMeter", "SkuPriceId", "SubAccountId", "SubAccountName",
	"Tags", "x_CostroidCatalogVersion", "x_CostroidOvershootBound", "x_CostroidRowId", "x_CostroidServiceTier",
	"x_HostProviderName",
}

// focus11And12Source is the row's column a 1.1 or 1.2 column is read from, where
// the two names differ.
var focus11And12Source = map[string]string{
	"ProviderName":       "ServiceProviderName",
	"PublisherName":      "ServiceProviderName",
	"x_HostProviderName": "HostProviderName",
}

// project is the row's cells for columns, each from the row's column of the
// same name or the one sources names. A column sources maps to "" is one the
// row has nothing for, and is null.
func project(source row, columns []string, sources map[string]string) []string {
	record := make([]string, len(columns))
	for i, column := range columns {
		from, renamed := sources[column]
		switch {
		case !renamed:
			record[i] = source.cell(column)
		case from != "":
			record[i] = source.cell(from)
		}
	}
	return record
}

// csvFile encodes a header and records as one CSV file.
func csvFile(header []string, records [][]string) ([]byte, error) {
	body, err := encodeRecord(header)
	if err != nil {
		return nil, err
	}
	for _, record := range records {
		line, err := encodeRecord(record)
		if err != nil {
			return nil, err
		}
		body = append(body, line...)
	}
	return body, nil
}

// instant reads one of a row's date and time columns, which the row contract
// writes as a UTC instant to the second. A row that holds any other form is
// refused by the format, so no instant is moved to fit.
func instant(format Format, source row, column string) (time.Time, error) {
	value := source.cell(column)
	parsed, err := time.Parse(focus.InstantLayout, value)
	if err != nil {
		return time.Time{}, &RowsError{Format: format, Rows: []string{source.name()}, Reason: fmt.Sprintf("%s is not a UTC instant to the second, as YYYY-MM-DDTHH:MM:SSZ", column)}
	}
	return parsed, nil
}

// groupRows keeps the rows' order within each group, and orders the groups
// by key.
func groupRows(rows []row, key func(row) (string, error)) ([]string, map[string][]row, error) {
	groups := map[string][]row{}
	for _, source := range rows {
		name, err := key(source)
		if err != nil {
			return nil, nil, err
		}
		groups[name] = append(groups[name], source)
	}
	keys := make([]string, 0, len(groups))
	for name := range groups {
		keys = append(keys, name)
	}
	slices.Sort(keys)
	return keys, groups, nil
}
