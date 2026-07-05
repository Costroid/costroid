// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Costroid Authors

package azurefocus

import (
	"testing"
	"time"
)

// TestParseManifestTimeLenient proves every wire form Microsoft's own
// published manifest samples use parses — timezone-less, Z-suffixed with
// seven fractional digits, and Z-suffixed without fraction — and that
// timezone-less values are read as UTC.
func TestParseManifestTimeLenient(t *testing.T) {
	tests := []struct {
		in   string
		want time.Time
	}{
		// The tutorial sample's timezone-less startDate form.
		{"2026-05-01T00:00:00", time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)},
		// submittedTime: Z-suffixed with seven fractional digits. Parsed
		// values truncate to microseconds (see TestParseManifestTimeMicros),
		// so the 700 ns tail is dropped: 901_396_700 → 901_396_000.
		{"2026-06-05T09:19:01.9013967Z", time.Date(2026, 6, 5, 9, 19, 1, 901_396_000, time.UTC)},
		// endDate appears both with and without the Z across samples.
		{"2026-06-30T00:00:00Z", time.Date(2026, 6, 30, 0, 0, 0, 0, time.UTC)},
		{"2026-06-30T00:00:00", time.Date(2026, 6, 30, 0, 0, 0, 0, time.UTC)},
		// Timezone-less with fractional seconds.
		{"2026-05-01T00:00:00.5", time.Date(2026, 5, 1, 0, 0, 0, 500_000_000, time.UTC)},
		// An explicit offset normalizes to UTC.
		{"2026-05-01T02:00:00+02:00", time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)},
	}
	for _, tt := range tests {
		got, err := parseManifestTime(tt.in)
		if err != nil {
			t.Errorf("parseManifestTime(%q): %v", tt.in, err)
			continue
		}
		if !got.Equal(tt.want) || got.Location() != time.UTC {
			t.Errorf("parseManifestTime(%q) = %v, want %v UTC", tt.in, got, tt.want)
		}
	}

	for _, bad := range []string{"", "03/01/2026", "2026-05-01", "yesterday"} {
		if _, err := parseManifestTime(bad); err == nil {
			t.Errorf("parseManifestTime(%q) succeeded, want an error", bad)
		}
	}
}

// TestParseManifestTimeMicros proves submittedTime is truncated to
// microseconds at parse time (slice-4 review fix-up): Azure emits seven
// fractional digits, but the attribution cache round-trips through a
// DuckDB TIMESTAMP (µs), so a fresh parse must already equal its cached
// copy or a sub-µs difference could flip a period's current-run selection
// between syncs.
func TestParseManifestTimeMicros(t *testing.T) {
	// A DuckDB TIMESTAMP holds microseconds; this mimics the cache
	// round-trip the attribution cache performs.
	roundTrip := func(t time.Time) time.Time { return t.Truncate(time.Microsecond) }

	got, err := parseManifestTime("2026-06-05T09:19:01.9013967Z")
	if err != nil {
		t.Fatalf("parseManifestTime: %v", err)
	}
	if got.Nanosecond()%1000 != 0 {
		t.Errorf("parseManifestTime kept sub-µs precision: %d ns", got.Nanosecond())
	}
	if !got.Equal(roundTrip(got)) {
		t.Errorf("parsed value %v is not stable across a µs cache round-trip (%v)", got, roundTrip(got))
	}

	// Two submittedTimes 100 ns apart within one microsecond parse equal —
	// so the cache round-trip cannot flip which is "later".
	a, err1 := parseManifestTime("2026-06-05T09:19:01.9013967Z") // .9013967 → .901396
	b, err2 := parseManifestTime("2026-06-05T09:19:01.9013968Z") // .9013968 → .901396
	if err1 != nil || err2 != nil {
		t.Fatalf("parseManifestTime: %v / %v", err1, err2)
	}
	if !a.Equal(b) {
		t.Errorf("100 ns-apart submittedTimes parsed unequal (%v vs %v); a cache round-trip could flip selection", a, b)
	}
}

// TestScrubURLs proves query strings — where a SAS token would live —
// never survive into error text.
func TestScrubURLs(t *testing.T) {
	tests := []struct{ in, want string }{
		{"https://acct.blob.core.windows.net/c/b?sig=SECRET&se=2026", "https://acct.blob.core.windows.net/c/b"},
		{"read http://127.0.0.1:1/x?comp=list failed", "read http://127.0.0.1:1/x failed"},
		{"no urls here", "no urls here"},
		{"two http://a/b?x=1 and https://c/d?y=2 urls", "two http://a/b and https://c/d urls"},
		// userinfo is credential-shaped (an account key mangled from a
		// connection string) and must not survive either.
		{"https://acct:ACCOUNTKEY@acct.blob.core.windows.net/?sv=1&sig=S", "https://acct.blob.core.windows.net/"},
	}
	for _, tt := range tests {
		if got := scrubURLs(tt.in); got != tt.want {
			t.Errorf("scrubURLs(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

// TestContentHashIsAChangeToken pins the documented change-token
// construction: order-insensitive over blobs, sensitive to every field
// and to the dataset version.
func TestContentHashIsAChangeToken(t *testing.T) {
	m := func(version string, blobs ...manifestBlob) *manifest {
		var v manifest
		v.Blobs = blobs
		v.ExportConfig.DataVersion = version
		return &v
	}
	a := manifestBlob{BlobName: "p/run/part_0.csv.gz", ByteCount: 10, DataRowCount: 2}
	b := manifestBlob{BlobName: "p/run/part_1.csv.gz", ByteCount: 20, DataRowCount: 3}

	base := contentHash(m("1.2-preview", a, b))
	if got := contentHash(m("1.2-preview", b, a)); got != base {
		t.Error("hash depends on blob order")
	}
	for name, changed := range map[string]*manifest{
		"blob renamed":    m("1.2-preview", manifestBlob{BlobName: "p/run2/part_0.csv.gz", ByteCount: 10, DataRowCount: 2}, b),
		"byteCount":       m("1.2-preview", manifestBlob{BlobName: a.BlobName, ByteCount: 11, DataRowCount: 2}, b),
		"dataRowCount":    m("1.2-preview", manifestBlob{BlobName: a.BlobName, ByteCount: 10, DataRowCount: 3}, b),
		"dataset version": m("1.0r2", a, b),
	} {
		if contentHash(changed) == base {
			t.Errorf("hash did not change when %s changed", name)
		}
	}
}
