// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Costroid Authors

package alert

import (
	"time"

	"github.com/Costroid/costroid/internal/anomalyscan"
)

// AnomalyMessage is the anomaly-alert payload: a fixed whitelist of cost
// metadata plus magnitude. Unlike the sync-failure Message it MAY carry cost
// amounts (they are aggregate cost metadata, never usage content). Marshalled
// verbatim as the webhook JSON body and summarized into the Slack text.
type AnomalyMessage struct {
	Kind      string `json:"kind"` // always "anomaly"
	Tenant    string `json:"tenant"`
	Scope     string `json:"scope"`         // "total" or "key"
	Key       string `json:"key,omitempty"` // "" for total scope
	Currency  string `json:"currency"`
	Date      string `json:"date"`      // the anomaly day, RFC3339 UTC
	Direction string `json:"direction"` // "increase" or "decrease"
	Observed  string `json:"observed"`  // decimal string
	Median    string `json:"median"`
	Deviation string `json:"deviation"`
	Threshold string `json:"threshold"`
}

// buildAnomalyMessage is the PURE anomaly payload builder: a detected
// anomalyscan.ScopedFlag under one currency becomes an AnomalyMessage with no
// side effect and no field beyond the whitelist. Every amount is rendered as an
// exact decimal string via Decimal.String (never float64, per the exact-money
// invariant), and the anomaly day is rendered RFC3339 in UTC.
func buildAnomalyMessage(tenant, currency string, sf anomalyscan.ScopedFlag) AnomalyMessage {
	return AnomalyMessage{
		Kind:      "anomaly",
		Tenant:    tenant,
		Scope:     sf.Scope,
		Key:       sf.Key,
		Currency:  currency,
		Date:      sf.Flag.Date.UTC().Format(time.RFC3339),
		Direction: sf.Flag.Direction,
		Observed:  sf.Flag.Observed.String(),
		Median:    sf.Flag.Median.String(),
		Deviation: sf.Flag.Deviation.String(),
		Threshold: sf.Flag.Threshold.String(),
	}
}
