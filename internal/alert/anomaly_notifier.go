// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Costroid Authors

package alert

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"time"

	"github.com/Costroid/costroid/internal/anomalyscan"
	"github.com/Costroid/costroid/internal/storage"
)

// anomalyStore is the NARROW slice of the store the anomaly alerter needs. It is
// deliberately local (not storage.Store) and is satisfied by *storage.DuckDB, so
// the big Store interface and its test fakes are untouched. The four methods are
// the part-A detector/dedup surface consumed verbatim: list currencies, load the
// per-service daily costs the shared detector scores, record-if-absent the
// dedup identities, and count recorded rows (a zero count is the first-enable
// signal).
type anomalyStore interface {
	BillingCurrencies(ctx context.Context, tenant string, start, end time.Time) ([]string, error)
	DailyCostsByService(ctx context.Context, tenant string, start, end time.Time, currency string, groupBy ...storage.CostGroupBy) (storage.DailyCosts, error)
	InsertNewAnomalyAlerts(ctx context.Context, alerts []storage.AnomalyAlert, at time.Time) ([]storage.AnomalyAlert, error)
	AnomalyAlertCount(ctx context.Context, tenant string) (int, error)
}

// AnomalyNotifier proactively alerts on newly-detected cost anomalies. It is
// single-tenant (focus.DefaultTenant, passed once at construction) because the
// whole anomaly surface is: the dashboard anomaly card is DefaultTenant-only, so
// the alerter must match it. Holding the tenant on the notifier keeps the
// scheduler from threading a run's source tenant - a non-default source tenant
// would never be seeded (and would storm) and would alert on data the dashboard
// never shows.
//
// # Cardinal Rule
//
// An anomaly alert carries aggregate cost metadata only: amounts (observed,
// median, deviation, threshold as exact decimal strings), a FOCUS service key,
// currency, the anomaly day, and direction. This is a deliberate, documented
// widening versus the sync-failure Message (which carries no figure); it never
// carries a usage metric, a prompt, or a response.
type AnomalyNotifier struct {
	channels []Channel
	store    anomalyStore
	logger   *slog.Logger
	tenant   string           // focus.DefaultTenant (passed at construction, testable)
	now      func() time.Time // injected so alerted_at is deterministic in tests
}

// NewAnomalyNotifier builds an AnomalyNotifier over channels and store, alerting
// for tenant and stamping alerted_at from now. A nil logger discards.
func NewAnomalyNotifier(channels []Channel, store anomalyStore, logger *slog.Logger, tenant string, now func() time.Time) *AnomalyNotifier {
	if logger == nil {
		logger = slog.New(slog.NewJSONHandler(io.Discard, nil))
	}
	return &AnomalyNotifier{channels: channels, store: store, logger: logger, tenant: tenant, now: now}
}

// scopedFlagCurrency pairs one detected anomaly flag with the currency whose
// series produced it (the detector runs per currency to respect the D26c
// single-currency guard).
type scopedFlagCurrency struct {
	flag     anomalyscan.ScopedFlag
	currency string
}

// scan is the shared detection pass used by both Seed and CheckAndNotify. It
// mirrors part A's detector exactly: a full-history (zero start) per-currency
// loop over the store's daily-by-service costs, scored by anomalyscan.Flags. The
// per-currency loop sidesteps the D26c mixed-currency guard, and the identity
// (built later) includes the currency so per-currency anomalies dedup
// independently.
func (a *AnomalyNotifier) scan(ctx context.Context) ([]scopedFlagCurrency, error) {
	now := a.now()
	currencies, err := a.store.BillingCurrencies(ctx, a.tenant, time.Time{}, now)
	if err != nil {
		return nil, fmt.Errorf("listing billing currencies: %w", err)
	}
	var out []scopedFlagCurrency
	for _, currency := range currencies {
		daily, err := a.store.DailyCostsByService(ctx, a.tenant, time.Time{}, now, currency, storage.GroupByService)
		if err != nil {
			return nil, fmt.Errorf("loading daily costs for currency %q: %w", currency, err)
		}
		for _, sf := range anomalyscan.Flags(daily) {
			out = append(out, scopedFlagCurrency{flag: sf, currency: currency})
		}
	}
	return out, nil
}

// identity is the persisted dedup key for one detected flag under one currency.
// It is the exact value passed to InsertNewAnomalyAlerts AND used as the lookup
// map key in CheckAndNotify; Date is sourced from the detector's flag (a stored
// calendar day), never from a clock, so a monotonic reading or *Location can
// never split or lose an alert.
func (a *AnomalyNotifier) identity(fc scopedFlagCurrency) storage.AnomalyAlert {
	return storage.AnomalyAlert{
		TenantID:  a.tenant,
		Scope:     fc.flag.Scope,
		SeriesKey: fc.flag.Key,
		Currency:  fc.currency,
		Date:      fc.flag.Flag.Date,
		Direction: fc.flag.Flag.Direction,
	}
}

// Seed records every currently-detectable anomaly WITHOUT alerting, used once on
// first enable so an upgraded store full of history never storms. It returns the
// error so the caller can refuse to enable alerting on a seed failure (leaving
// an un-seeded table that the next run would page in full). The newly-inserted
// result is intentionally discarded.
func (a *AnomalyNotifier) Seed(ctx context.Context) error {
	flags, err := a.scan(ctx)
	if err != nil {
		return err
	}
	identities := make([]storage.AnomalyAlert, len(flags))
	for i, fc := range flags {
		identities[i] = a.identity(fc)
	}
	if _, err := a.store.InsertNewAnomalyAlerts(ctx, identities, a.now()); err != nil {
		return err
	}
	a.logger.Info("seeded anomaly-alert dedup table", "tenant", a.tenant, "seeded", len(identities))
	return nil
}

// CheckAndNotify scans for anomalies, records the ones never seen before, and
// alerts exactly on those. It NEVER returns an error (mirroring
// Notifier.NotifySyncRun): a scan, insert, or send failure is swallowed and
// logged so it can never break the serial scheduler. Dedup is entirely the
// persisted anomaly_alerts table - an identity already recorded is not returned
// by InsertNewAnomalyAlerts, so it never re-fires.
func (a *AnomalyNotifier) CheckAndNotify(ctx context.Context) {
	flags, err := a.scan(ctx)
	if err != nil {
		a.logger.Error("scanning for anomaly alerts", "tenant", a.tenant, "error", err)
		return
	}
	if len(flags) == 0 {
		return
	}
	identities := make([]storage.AnomalyAlert, len(flags))
	byIdentity := make(map[storage.AnomalyAlert]scopedFlagCurrency, len(flags))
	for i, fc := range flags {
		id := a.identity(fc)
		identities[i] = id
		// The map key is the SAME value passed into the store; the store echoes
		// it verbatim, so the lookup below over the returned rows always hits.
		byIdentity[id] = fc
	}
	inserted, err := a.store.InsertNewAnomalyAlerts(ctx, identities, a.now())
	if err != nil {
		a.logger.Error("recording new anomaly alerts", "tenant", a.tenant, "error", err)
		return
	}
	for _, id := range inserted {
		fc, ok := byIdentity[id]
		if !ok {
			// Unreachable: the store returns input values verbatim. Skip rather
			// than fabricate an alert from a key we cannot resolve.
			continue
		}
		msg := buildAnomalyMessage(a.tenant, fc.currency, fc.flag)
		for _, channel := range a.channels {
			if err := channel.SendAnomaly(ctx, msg); err != nil {
				a.logger.Error("sending anomaly alert",
					"channel", channel.Name(), "tenant", a.tenant,
					"scope", fc.flag.Scope, "key", fc.flag.Key,
					"currency", fc.currency, "date", msg.Date, "error", err)
			}
		}
	}
}
