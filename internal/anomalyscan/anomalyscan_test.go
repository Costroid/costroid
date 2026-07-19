// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Costroid Authors

package anomalyscan

import (
	"testing"
	"time"

	"github.com/shopspring/decimal"

	"github.com/Costroid/costroid/internal/anomaly"
	"github.com/Costroid/costroid/internal/storage"
)

func dc(s string) decimal.Decimal { return decimal.RequireFromString(s) }

// wantFlag is the expected shape of one ScopedFlag, decimals compared as
// strings (never float) so exactness is proven end to end.
type wantFlag struct {
	scope, key, date, direction     string
	observed, median, mad           string
	scaledMAD, threshold, deviation string
}

// TestFlagsScopesAndMinObservations feeds deterministic storage.DailyCosts (no
// clock) and pins that Flags scores the exact-decimal TOTAL series and each
// per-key series, tags each flag with the right scope/key, and skips a series
// with fewer than anomaly.MinObservations baseline days.
func TestFlagsScopesAndMinObservations(t *testing.T) {
	base := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	day := func(i int) time.Time { return base.AddDate(0, 0, i) }

	// "mixed": one flat "compute" series that spikes on the scored day, plus a
	// one-day "sparse" key present only on that day. The total on the spike day
	// is the exact sum (100 + 50 = 150) over a ten-day flat "10" baseline, so
	// both the total series and the compute key flag; "sparse" has a single
	// observation, far below MinObservations, so it is never scored.
	mixedDays := make([]storage.DayCosts, 11)
	for i := 0; i < 10; i++ {
		mixedDays[i] = storage.DayCosts{
			Date:     day(i),
			Services: []storage.ServiceCost{{ServiceName: "compute", Cost: dc("10")}},
		}
	}
	mixedDays[10] = storage.DayCosts{
		Date: day(10),
		Services: []storage.ServiceCost{
			{ServiceName: "compute", Cost: dc("100")},
			{ServiceName: "sparse", Cost: dc("50")},
		},
	}

	// "short": exactly MinObservations days with a spike on the last one. A day
	// is scored only when at least MinObservations days precede it, so the first
	// scorable index is MinObservations; with only that many days present, no day
	// (total or key) is ever scored despite the obvious spike.
	shortDays := make([]storage.DayCosts, anomaly.MinObservations)
	for i := range shortDays {
		v := "10"
		if i == len(shortDays)-1 {
			v = "500"
		}
		shortDays[i] = storage.DayCosts{
			Date:     day(i),
			Services: []storage.ServiceCost{{ServiceName: "compute", Cost: dc(v)}},
		}
	}

	tests := []struct {
		name  string
		daily storage.DailyCosts
		want  []wantFlag
	}{
		{
			name:  "total and key flag on the spike day, sparse key skipped",
			daily: storage.DailyCosts{Currency: "USD", Days: mixedDays},
			want: []wantFlag{
				{
					scope: "total", key: "", date: "2026-06-11", direction: "increase",
					observed: "150", median: "10", mad: "0",
					scaledMAD: "0", threshold: "0", deviation: "140",
				},
				{
					scope: "key", key: "compute", date: "2026-06-11", direction: "increase",
					observed: "100", median: "10", mad: "0",
					scaledMAD: "0", threshold: "0", deviation: "90",
				},
			},
		},
		{
			name:  "series below MinObservations yields no flag",
			daily: storage.DailyCosts{Currency: "USD", Days: shortDays},
			want:  nil,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := Flags(tc.daily)
			if len(got) != len(tc.want) {
				t.Fatalf("Flags returned %d flags, want %d: %+v", len(got), len(tc.want), got)
			}
			for i, w := range tc.want {
				g := got[i]
				if g.Scope != w.scope || g.Key != w.key {
					t.Errorf("flag %d scope/key = (%q,%q), want (%q,%q)", i, g.Scope, g.Key, w.scope, w.key)
				}
				if d := g.Flag.Date.UTC().Format(time.DateOnly); d != w.date {
					t.Errorf("flag %d date = %s, want %s", i, d, w.date)
				}
				if g.Flag.Direction != w.direction {
					t.Errorf("flag %d direction = %q, want %q", i, g.Flag.Direction, w.direction)
				}
				if s := g.Flag.Observed.String(); s != w.observed {
					t.Errorf("flag %d observed = %s, want %s", i, s, w.observed)
				}
				if s := g.Flag.Median.String(); s != w.median {
					t.Errorf("flag %d median = %s, want %s", i, s, w.median)
				}
				if s := g.Flag.MAD.String(); s != w.mad {
					t.Errorf("flag %d mad = %s, want %s", i, s, w.mad)
				}
				if s := g.Flag.ScaledMAD.String(); s != w.scaledMAD {
					t.Errorf("flag %d scaledMAD = %s, want %s", i, s, w.scaledMAD)
				}
				if s := g.Flag.Threshold.String(); s != w.threshold {
					t.Errorf("flag %d threshold = %s, want %s", i, s, w.threshold)
				}
				if s := g.Flag.Deviation.String(); s != w.deviation {
					t.Errorf("flag %d deviation = %s, want %s", i, s, w.deviation)
				}
			}
			// The sparse one-day key must never surface a flag.
			for _, g := range got {
				if g.Key == "sparse" {
					t.Errorf("sparse key (below MinObservations) was flagged: %+v", g)
				}
			}
		})
	}
}
