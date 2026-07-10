// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Costroid Authors

// Package businessmetrics parses and validates user-supplied business-value
// counts (requests served, active users, customers) and their names. It owns no
// SQL: storage binds every name and quantity as data. Cardinal Rule (decision
// D7): this surface accepts business counts and identifiers only, never AI
// prompt or response content.
package businessmetrics

import (
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/shopspring/decimal"

	"github.com/Costroid/costroid/internal/focus"
	"github.com/Costroid/costroid/internal/storage"
)

var maxStoreIntegerAbs = decimal.New(1, 38-storage.MaxDecimalScale)

// Parse reads the strict long-format date,metric,quantity CSV completely. A
// header-only file is valid and returns an empty slice so its source label can
// be cleared atomically.
func Parse(r io.Reader) ([]storage.BusinessMetricRow, error) {
	csvr := csv.NewReader(r)
	header, err := csvr.Read()
	if err != nil {
		if errors.Is(err, io.EOF) {
			return nil, errors.New("business metrics CSV is empty; expected header date,metric,quantity")
		}
		return nil, fmt.Errorf("reading business metrics CSV header: %w", err)
	}
	if len(header) != 3 || header[0] != "date" || header[1] != "metric" || header[2] != "quantity" {
		return nil, fmt.Errorf("business metrics CSV header must be exactly date,metric,quantity; got %q", strings.Join(header, ","))
	}

	seen := map[string]int{}
	var rows []storage.BusinessMetricRow
	for recordNumber := 1; ; recordNumber++ {
		record, err := csvr.Read()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			// csv.Reader enforces the 3-field width itself (FieldsPerRecord is
			// pinned to the header's field count), so a wrong-width row surfaces
			// here as an ErrFieldCount, never as a short/long record below.
			return nil, fmt.Errorf("record %d: reading CSV fields: %w", recordNumber, err)
		}

		day, err := parseDay(record[0])
		if err != nil {
			return nil, fmt.Errorf("record %d date: %w", recordNumber, err)
		}
		metric := record[1]
		if metric == "" {
			return nil, fmt.Errorf("record %d metric: must be non-empty", recordNumber)
		}
		if strings.TrimSpace(metric) != metric {
			return nil, fmt.Errorf("record %d metric %q: leading or trailing whitespace is not allowed", recordNumber, metric)
		}

		quantity, err := focus.ParseDecimal(record[2])
		if err != nil {
			return nil, fmt.Errorf("record %d quantity: %w", recordNumber, err)
		}
		// Mirrors internal/ingest.storeScaleErrors deliberately: that guard is
		// unexported and CostRecord-shaped, while this parser validates its own
		// storage row before any transaction begins.
		if !quantity.Equal(quantity.Truncate(storage.MaxDecimalScale)) {
			return nil, fmt.Errorf("record %d quantity: value %s has more than %d fractional digits; the embedded store holds DECIMAL(38,%d) and never rounds silently", recordNumber, quantity, storage.MaxDecimalScale, storage.MaxDecimalScale)
		}
		if quantity.Abs().Cmp(maxStoreIntegerAbs) >= 0 {
			return nil, fmt.Errorf("record %d quantity: value %s has more than %d integer digits; the embedded store holds DECIMAL(38,%d) and never truncates silently", recordNumber, quantity, 38-storage.MaxDecimalScale, storage.MaxDecimalScale)
		}
		if !quantity.IsPositive() {
			return nil, fmt.Errorf("record %d quantity: must be greater than zero; omit a row to express no quantity for a day", recordNumber)
		}

		key := day.Format(time.DateOnly) + "\x00" + metric
		if first, ok := seen[key]; ok {
			return nil, fmt.Errorf("record %d duplicates date %s and metric %q from record %d; each (date, metric) pair may appear only once per file", recordNumber, day.Format(time.DateOnly), metric, first)
		}
		seen[key] = recordNumber
		rows = append(rows, storage.BusinessMetricRow{MetricDay: day, MetricName: metric, Quantity: quantity})
	}
	return rows, nil
}

func parseDay(value string) (time.Time, error) {
	if len(value) != len("YYYY-MM-DD") {
		return time.Time{}, fmt.Errorf("%q must be exactly YYYY-MM-DD", value)
	}
	day, err := time.Parse(time.DateOnly, value)
	if err != nil || day.Format(time.DateOnly) != value {
		return time.Time{}, fmt.Errorf("%q must be exactly YYYY-MM-DD", value)
	}
	return day.UTC(), nil
}
