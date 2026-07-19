// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Costroid Authors

// Package anomalyscan turns stored daily costs into detected anomaly flags. It
// is the single detection path shared by the /api/v1/anomalies dashboard card
// and the anomaly alerter, so the two can never disagree about what is flagged.
// It layers over the pure internal/anomaly detector (which knows nothing of
// storage); range filtering, formatting, and ordering stay with each caller.
package anomalyscan

import (
	"github.com/shopspring/decimal"

	"github.com/Costroid/costroid/internal/anomaly"
	"github.com/Costroid/costroid/internal/storage"
)

// ScopedFlag is a detected anomaly plus which series produced it: the "total"
// daily-spend series (Key empty), or a per-service "key" series (Key = the
// FOCUS ServiceName / ServiceProviderName / allocation label used by daily).
type ScopedFlag struct {
	Flag  anomaly.Flag
	Scope string // "total" or "key"
	Key   string // "" for the total scope
}

// Flags scores the per-day TOTAL series (the exact decimal sum over each day's
// services) and each service key's own series, returning every detected flag
// with its scope and key. The total series exists on every day in daily; a key
// series has an observation only on days that key has data. Output order is
// total flags (date order) then per-key flags in first-seen key order; callers
// that need a stable presentation order sort their own formatted result.
func Flags(daily storage.DailyCosts) []ScopedFlag {
	total := make([]anomaly.Observation, 0, len(daily.Days))
	keySeries := map[string][]anomaly.Observation{}
	var keyOrder []string
	for _, day := range daily.Days {
		sum := decimal.Zero
		for _, svc := range day.Services {
			sum = sum.Add(svc.Cost)
			if _, seen := keySeries[svc.ServiceName]; !seen {
				keyOrder = append(keyOrder, svc.ServiceName)
			}
			keySeries[svc.ServiceName] = append(keySeries[svc.ServiceName],
				anomaly.Observation{Date: day.Date, Value: svc.Cost})
		}
		total = append(total, anomaly.Observation{Date: day.Date, Value: sum})
	}

	var out []ScopedFlag
	for _, f := range anomaly.Detect(total) {
		out = append(out, ScopedFlag{Flag: f, Scope: "total"})
	}
	for _, key := range keyOrder {
		for _, f := range anomaly.Detect(keySeries[key]) {
			out = append(out, ScopedFlag{Flag: f, Scope: "key", Key: key})
		}
	}
	return out
}
