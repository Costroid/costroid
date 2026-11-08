package export

import (
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"
	"unicode/utf8"
)

// Datadog's Custom Costs page (https://docs.datadoghq.com/cloud_cost_management/setup/custom/,
// retrieved 2026-11-08) states the file: the required columns "must appear as
// columns in your CSV in the order listed above", BillingCurrency "Must be
// USD", dates are "Formatted YYYY-MM-DD" and ChargePeriodEnd is the "Last day
// of a charge (inclusive)". "For a CSV file, add a column per tag", after the
// required columns. "NULL or blank ("") parameter values are not accepted", and
// names and values "have a maximum of 1,000 characters".
const (
	datadogCurrency  = "USD"
	datadogDate      = "2006-01-02"
	datadogMaxLength = 1000
)

// datadogColumns are the required columns in the page's order.
var datadogColumns = []string{"ProviderName", "ChargeDescription", "ChargePeriodStart", "ChargePeriodEnd", "BilledCost", "BillingCurrency"}

// writeDatadog writes the rows as Datadog's CSV. ProviderName is the row's
// ServiceProviderName, which FOCUS 1.3 renamed it to. BilledCost is the row's
// EffectiveCost: a row's own BilledCost is 0, because Costroid issues no
// invoice, and Datadog has no other cost column. A tag gets a column only when
// every row carries it.
func writeDatadog(rows table) (Export, error) {
	all := rows.rows()
	var foreign []string
	for _, source := range all {
		if currency := source.cell("BillingCurrency"); currency != datadogCurrency {
			foreign = append(foreign, source.name()+" in "+currency)
		}
	}
	if len(foreign) > 0 {
		return Export{}, &RowsError{Format: Datadog, Rows: foreign, Reason: "Datadog takes " + datadogCurrency + " only, and export never converts an amount"}
	}
	tags := make([]map[string]string, len(all))
	var order [][]string
	for i, source := range all {
		keys, values, err := orderedTags(source.cell("Tags"))
		if err != nil {
			return Export{}, fmt.Errorf("%s: %w", source.name(), err)
		}
		tags[i] = values
		order = append(order, keys)
	}
	kept, omitted := sharedTags(order, tags)
	header := append(slices.Clone(datadogColumns), kept...)
	body, err := encodeRecord(header)
	if err != nil {
		return Export{}, err
	}
	for i, source := range all {
		record, err := datadogRecord(source, tags[i], kept)
		if err != nil {
			return Export{}, err
		}
		for column, value := range record {
			if reason := datadogCell(header[column], value); reason != "" {
				return Export{}, &RowsError{Format: Datadog, Rows: []string{source.name()}, Reason: reason}
			}
		}
		line, err := encodeRecord(record)
		if err != nil {
			return Export{}, err
		}
		body = append(body, line...)
	}
	return Export{Files: []File{{Body: body, Rows: len(all)}}, OmittedTags: omitted}, nil
}

func datadogRecord(source row, tags map[string]string, kept []string) ([]string, error) {
	start, err := time.Parse(time.RFC3339, source.cell("ChargePeriodStart"))
	if err != nil {
		return nil, fmt.Errorf("%s: ChargePeriodStart is not an instant: %w", source.name(), err)
	}
	end, err := time.Parse(time.RFC3339, source.cell("ChargePeriodEnd"))
	if err != nil {
		return nil, fmt.Errorf("%s: ChargePeriodEnd is not an instant: %w", source.name(), err)
	}
	record := []string{
		source.cell("ServiceProviderName"),
		source.cell("ChargeDescription"),
		start.UTC().Format(datadogDate),
		lastDay(start, end).Format(datadogDate),
		source.cell("EffectiveCost"),
		source.cell("BillingCurrency"),
	}
	for _, key := range kept {
		record = append(record, tags[key])
	}
	return record, nil
}

// lastDay is the UTC day that holds a charge's last instant. A row's
// ChargePeriodEnd is exclusive, so a charge that ends at midnight ends on the
// day before, and never before the day it starts.
func lastDay(start, end time.Time) time.Time {
	last := end.UTC().Add(-time.Nanosecond)
	if last.Before(start) {
		return start.UTC()
	}
	return last
}

// datadogCell states why Datadog would refuse a value, and is empty when it
// would take it.
func datadogCell(column, value string) string {
	if value == "" {
		return column + " is blank, and Datadog takes no blank value"
	}
	if length := utf8.RuneCountInString(value); length > datadogMaxLength {
		return fmt.Sprintf("%s is %d characters, past Datadog's %d", column, length, datadogMaxLength)
	}
	return ""
}

// sharedTags returns the tag keys every row carries with a value, in the order
// the first row names them, and every other key, sorted.
func sharedTags(order [][]string, tags []map[string]string) (kept, omitted []string) {
	for _, key := range order[0] {
		everywhere := !slices.Contains(kept, key)
		for _, values := range tags {
			everywhere = everywhere && values[key] != ""
		}
		if everywhere {
			kept = append(kept, key)
		}
	}
	for _, keys := range order {
		for _, key := range keys {
			if !slices.Contains(kept, key) && !slices.Contains(omitted, key) {
				omitted = append(omitted, key)
			}
		}
	}
	slices.Sort(omitted)
	return kept, omitted
}

// orderedTags reads a Tags cell, an object of strings, keeping its order.
func orderedTags(cell string) ([]string, map[string]string, error) {
	values := map[string]string{}
	if cell == "" {
		return nil, values, nil
	}
	decoder := json.NewDecoder(strings.NewReader(cell))
	if open, err := decoder.Token(); err != nil || open != json.Delim('{') {
		return nil, nil, errors.New("the column Tags is not an object")
	}
	var keys []string
	for decoder.More() {
		token, err := decoder.Token()
		if err != nil {
			return nil, nil, fmt.Errorf("the column Tags: %w", err)
		}
		key, _ := token.(string)
		var value string
		if err := decoder.Decode(&value); err != nil {
			return nil, nil, fmt.Errorf("the column Tags: %w", err)
		}
		keys, values[key] = append(keys, key), value
	}
	return keys, values, nil
}
