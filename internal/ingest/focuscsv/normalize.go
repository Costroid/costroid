// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Costroid Authors

package focuscsv

import (
	"regexp"
	"strings"
	"time"

	"github.com/Costroid/costroid/internal/focus"
)

// dateTimeColumns are the four FOCUS Date/Time columns --lenient may rewrite to
// canonical RFC 3339 before the shared strict parser and validation see them.
// Nothing else is ever touched.
var dateTimeColumns = []string{
	"BillingPeriodStart",
	"BillingPeriodEnd",
	"ChargePeriodStart",
	"ChargePeriodEnd",
}

// dateSpaceTime matches a "YYYY-MM-DD HH:MM" prefix — a single space where
// RFC 3339 uses 'T' between the date and time (BigQuery CSV extracts, warehouse
// dumps). Anchored so it only ever rewrites the date/time separator.
var dateSpaceTime = regexp.MustCompile(`^\d{4}-\d{2}-\d{2} \d{2}:\d{2}`)

// lenientLayouts are the timestamp layouts --lenient tolerates beyond strict
// RFC 3339. Every layout carries an explicit zone element (Z07:00 / Z0700), so a
// genuinely zone-less value matches NONE of them and is returned unchanged for
// the strict parser to reject. That is the money-safety boundary by construction:
// an Alibaba-class local-time value (no zone) is never silently assumed UTC.
var lenientLayouts = []string{
	time.RFC3339,               // 2006-01-02T15:04:05Z07:00 (also the space→T / UTC→Z target)
	time.RFC3339Nano,           // + fractional seconds
	"2006-01-02T15:04Z07:00",   // no seconds, Z or offset-with-colon (OCI, Azure 1.0)
	"2006-01-02T15:04:05Z0700", // offset WITHOUT colon (JVM/warehouse dumps)
	"2006-01-02T15:04Z0700",    // no seconds, offset without colon
}

// normalizeTimestamp canonicalizes a real-world but unambiguously-UTC timestamp
// FORMAT variant to RFC 3339 (UTC), or returns the input UNCHANGED when it does
// not match a recognized ZONE-BEARING shape (so the strict focus.ParseTime then
// produces the existing actionable rejection). It is pure and idempotent: a UTC
// (Z) RFC 3339 input returns itself, while any other explicit offset converts to
// UTC (e.g. 2024-01-01T05:00:00+05:00 → 2024-01-01T00:00:00Z), never itself.
//
// Money safety is by construction: every accepted shape carries an explicit zone.
// Zone-less values (e.g. "2024-01-01 00:00:00", "2024-01-01T00:00:00") match no
// layout and come back byte-verbatim. Only the LITERAL " UTC" suffix (BigQuery)
// is honored — a Go "MST" layout would fabricate a zero offset for a non-UTC
// abbreviation (PST → +0000), silently producing a wrong instant, so named zones
// are never parsed generically.
//
// It mirrors azurefocus's AZF-5 connector-side pattern (normalize before the
// shared parser so the frozen files never see a lenient string) but, unlike that
// helper, accepts ONLY zone-bearing layouts.
func normalizeTimestamp(s string) string {
	work := strings.TrimSpace(s)
	// BigQuery's named-UTC suffix → 'Z'. Literal " UTC" only (see the doc above).
	if strings.HasSuffix(work, " UTC") {
		work = strings.TrimSuffix(work, " UTC") + "Z"
	}
	// A single space where RFC 3339 uses 'T' between date and time. Replace only
	// that first (separator) space; a trailing " PST"-style token, if any, is left
	// intact so it fails the zone-bearing layouts below.
	if dateSpaceTime.MatchString(work) {
		work = strings.Replace(work, " ", "T", 1)
	}
	for _, layout := range lenientLayouts {
		if t, err := time.Parse(layout, work); err == nil {
			return t.UTC().Format(time.RFC3339Nano)
		}
	}
	return s // no zone-bearing shape matched → original bytes, verbatim
}

// normalizeRecordTimestamps rewrites the four Date/Time columns of a --lenient
// record to canonical RFC 3339 in place, so the pipeline's frozen Validate /
// ParseRecord see only canonical strings. Empty or absent cells are skipped so
// lenient mode never materializes a column strict mode leaves absent (a 1.4
// DatasetConfiguration subset may legitimately omit BillingPeriodEnd, etc.).
func normalizeRecordTimestamps(rec focus.RawRecord) {
	for _, col := range dateTimeColumns {
		v, ok := rec[col]
		if !ok || v == "" {
			continue
		}
		rec[col] = normalizeTimestamp(v)
	}
}
