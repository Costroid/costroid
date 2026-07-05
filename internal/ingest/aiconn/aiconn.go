// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Costroid Authors

// Package aiconn holds the small helpers the AI-vendor cost connectors
// (anthropic-cost, openai-cost) share verbatim: the calendar-month billing
// window, the month bounds, and the --base-url validation. They lived as
// byte-identical copies in each connector; extracting them here removes the
// drift risk and lets them (and, through them, the connectors' public
// behavior) be tested once, directly. The connectors keep their
// vendor-specific request, auth, and mapping logic.
package aiconn

import (
	"fmt"
	"net"
	"net/url"
	"time"
)

// Window returns the "YYYY-MM" months to ingest, oldest first. period (one
// month) wins; otherwise the window runs from since (or 11 months before the
// current month, giving 12 months) through the current month. now fixes "the
// current month", so callers and tests share one computation and no assertion
// depends on the wall clock.
func Window(since, period string, now time.Time) ([]string, error) {
	if period != "" {
		if _, err := time.Parse("2006-01", period); err != nil {
			return nil, fmt.Errorf("invalid --period %q, want YYYY-MM", period)
		}
		return []string{period}, nil
	}
	current := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC)
	start := current.AddDate(0, -11, 0)
	if since != "" {
		s, err := time.Parse("2006-01", since)
		if err != nil {
			return nil, fmt.Errorf("invalid --since %q, want YYYY-MM", since)
		}
		start = s.UTC()
		if start.After(current) {
			return nil, fmt.Errorf("--since %q is in the future (current month %s)", since, current.Format("2006-01"))
		}
	}
	var months []string
	for m := start; !m.After(current); m = m.AddDate(0, 1, 0) {
		months = append(months, m.Format("2006-01"))
	}
	return months, nil
}

// MonthBounds returns [firstOfMonth, firstOfNextMonth) in UTC for "YYYY-MM".
func MonthBounds(month string) (start, end time.Time, err error) {
	start, err = time.Parse("2006-01", month)
	if err != nil {
		return time.Time{}, time.Time{}, fmt.Errorf("invalid month %q, want YYYY-MM", month)
	}
	start = start.UTC()
	return start, start.AddDate(0, 1, 0), nil
}

// ValidateBaseURL refuses a malformed base URL and refuses plain http://
// unless the host is loopback (the offline fake). example is the vendor's
// canonical endpoint, named in the hint so each connector keeps its own
// actionable message.
func ValidateBaseURL(raw, example string) error {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return fmt.Errorf("invalid --base-url: expected an https:// API endpoint, e.g. %s", example)
	}
	if u.Scheme == "http" && !isLoopback(u.Hostname()) {
		return fmt.Errorf("--base-url %q uses http:// with a non-loopback host — use https:// for real endpoints "+
			"(http is allowed only for a loopback test server)", u.Scheme+"://"+u.Host)
	}
	return nil
}

func isLoopback(host string) bool {
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
