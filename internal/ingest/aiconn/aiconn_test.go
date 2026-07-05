// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Costroid Authors

package aiconn_test

import (
	"strings"
	"testing"
	"time"

	"github.com/Costroid/costroid/internal/ingest/aiconn"
)

// TestWindow pins the calendar-month window at several fixed "now" values —
// mid-year, a month rollover, and a year boundary — so the computation is
// verified directly and no caller has to trust the wall clock (this is the
// real helper the e2e imports to derive its expected periods).
func TestWindow(t *testing.T) {
	tests := []struct {
		name   string
		since  string
		period string
		now    time.Time
		want   []string
	}{
		{
			name: "default 12-month window mid-year",
			now:  time.Date(2026, 7, 6, 12, 0, 0, 0, time.UTC),
			want: []string{
				"2025-08", "2025-09", "2025-10", "2025-11", "2025-12",
				"2026-01", "2026-02", "2026-03", "2026-04", "2026-05", "2026-06", "2026-07",
			},
		},
		{
			name:  "since through current month, 2026-07",
			since: "2026-05",
			now:   time.Date(2026, 7, 6, 0, 0, 0, 0, time.UTC),
			want:  []string{"2026-05", "2026-06", "2026-07"},
		},
		{
			name:  "same since, one month later includes the new month",
			since: "2026-05",
			now:   time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC),
			want:  []string{"2026-05", "2026-06", "2026-07", "2026-08"},
		},
		{
			name:  "first instant of a month is already that month",
			since: "2026-07",
			now:   time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC),
			want:  []string{"2026-07", "2026-08"},
		},
		{
			name:  "window spans a year boundary",
			since: "2026-11",
			now:   time.Date(2027, 1, 15, 0, 0, 0, 0, time.UTC),
			want:  []string{"2026-11", "2026-12", "2027-01"},
		},
		{
			name:   "period pins exactly one month regardless of now",
			period: "2026-03",
			now:    time.Date(2026, 12, 31, 23, 59, 59, 0, time.UTC),
			want:   []string{"2026-03"},
		},
		{
			name:  "since equal to current month is a single month",
			since: "2026-07",
			now:   time.Date(2026, 7, 20, 0, 0, 0, 0, time.UTC),
			want:  []string{"2026-07"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := aiconn.Window(tt.since, tt.period, tt.now)
			if err != nil {
				t.Fatalf("Window: %v", err)
			}
			if strings.Join(got, ",") != strings.Join(tt.want, ",") {
				t.Errorf("Window(%q,%q,%s) = %v, want %v", tt.since, tt.period, tt.now.Format("2006-01"), got, tt.want)
			}
		})
	}
}

func TestWindowErrors(t *testing.T) {
	now := time.Date(2026, 7, 6, 0, 0, 0, 0, time.UTC)
	tests := []struct {
		name         string
		since        string
		period       string
		wantContains string
	}{
		{name: "bad period", period: "2026-13-01", wantContains: "invalid --period"},
		{name: "bad since", since: "not-a-month", wantContains: "invalid --since"},
		{name: "future since", since: "2026-09", wantContains: "in the future"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := aiconn.Window(tt.since, tt.period, now)
			if err == nil || !strings.Contains(err.Error(), tt.wantContains) {
				t.Errorf("Window(%q,%q) err = %v, want one containing %q", tt.since, tt.period, err, tt.wantContains)
			}
		})
	}
}

func TestMonthBounds(t *testing.T) {
	start, end, err := aiconn.MonthBounds("2026-05")
	if err != nil {
		t.Fatalf("MonthBounds: %v", err)
	}
	if start.Format(time.RFC3339) != "2026-05-01T00:00:00Z" {
		t.Errorf("start = %s, want 2026-05-01T00:00:00Z", start.Format(time.RFC3339))
	}
	if end.Format(time.RFC3339) != "2026-06-01T00:00:00Z" {
		t.Errorf("end = %s, want 2026-06-01T00:00:00Z", end.Format(time.RFC3339))
	}
	if _, _, err := aiconn.MonthBounds("nope"); err == nil {
		t.Error("MonthBounds(nope) succeeded, want an error")
	}
}

func TestValidateBaseURL(t *testing.T) {
	const example = "https://api.example.com"
	// Rejected.
	for _, bad := range []string{"", "not-a-url", "ftp://x", "http://api.example.com"} {
		if err := aiconn.ValidateBaseURL(bad, example); err == nil {
			t.Errorf("ValidateBaseURL(%q) = nil, want an error", bad)
		}
	}
	// The malformed-URL hint names the example endpoint.
	err := aiconn.ValidateBaseURL("", example)
	if err == nil || !strings.Contains(err.Error(), example) {
		t.Errorf("empty base-url error = %v, want it to name %q", err, example)
	}
	// Accepted: https anywhere, and http only on loopback.
	for _, ok := range []string{
		"https://api.example.com",
		"http://localhost:8080",
		"http://127.0.0.1:9000",
		"http://[::1]:9000",
	} {
		if err := aiconn.ValidateBaseURL(ok, example); err != nil {
			t.Errorf("ValidateBaseURL(%q) = %v, want it allowed", ok, err)
		}
	}
}
