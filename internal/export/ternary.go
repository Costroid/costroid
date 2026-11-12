package export

import "slices"

// LiteLLM's Ternary page (https://docs.litellm.ai/docs/observability/ternary.md,
// retrieved 2026-11-12) states that a gateway "transforms recent spend into
// FOCUS 1.2 CSV and uploads one file per UTC day", and that "Each day maps to
// one file, so re-sending a day replaces it rather than double-counting".
// Ternary's own page (https://docs.ternary.app/docs/focus, retrieved the same
// day) states that "Column names must be valid BigQuery identifiers", that a
// date in a CSV takes the form YYYY-MM-DDTHH:MM:SSZ, and that a tag column may
// be "A single column containing a JSON object". The file is the 1.2 columns
// a row fills, which are 1.1's and InvoiceId. FOCUS 1.2 recommends InvoiceId
// and asks it be null on a charge no invoice holds, which is every row.
const ternaryDay = "2006-01-02"

var (
	ternaryColumns = slices.Insert(slices.Clone(focus11And12Columns), slices.Index(focus11And12Columns, "InvoiceIssuerName"), "InvoiceId")
	ternarySource  = map[string]string{"ProviderName": "ServiceProviderName", "PublisherName": "ServiceProviderName", "x_HostProviderName": "HostProviderName", "InvoiceId": ""}
)

// writeTernary writes one FOCUS 1.2 file for each UTC day a row's charge
// starts on, named for the day, in the order of the days.
func writeTernary(rows table) (Export, error) {
	days, groups, err := groupRows(rows.rows(), ternaryDayOf)
	if err != nil {
		return Export{}, err
	}
	files := make([]File, len(days))
	for i, day := range days {
		records := make([][]string, len(groups[day]))
		for j, source := range groups[day] {
			records[j] = project(source, ternaryColumns, ternarySource)
		}
		body, err := csvFile(ternaryColumns, records)
		if err != nil {
			return Export{}, err
		}
		files[i] = File{Name: day + ".csv", Body: body, Rows: len(records)}
	}
	return Export{Files: files}, nil
}

// ternaryDayOf is the UTC day of a row's ChargePeriodStart. It refuses a row
// whose dates are not UTC instants to the second.
func ternaryDayOf(source row) (string, error) {
	for _, column := range []string{"BillingPeriodStart", "BillingPeriodEnd", "ChargePeriodEnd"} {
		if _, err := instant(Ternary, source, column); err != nil {
			return "", err
		}
	}
	start, err := instant(Ternary, source, "ChargePeriodStart")
	if err != nil {
		return "", err
	}
	return start.Format(ternaryDay), nil
}
