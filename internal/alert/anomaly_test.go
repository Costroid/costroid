// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Costroid Authors

package alert

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/shopspring/decimal"

	"github.com/Costroid/costroid/internal/anomaly"
	"github.com/Costroid/costroid/internal/anomalyscan"
	"github.com/Costroid/costroid/internal/storage"
)

// anomalyWhitelistKeys is the exact, fixed field set of a fully-populated
// (key-scope) AnomalyMessage. A regression that adds a field fails the
// exact-count assertion below.
var anomalyWhitelistKeys = []string{
	"kind", "tenant", "scope", "key", "currency", "date", "direction",
	"observed", "median", "deviation", "threshold",
}

// anomalyKey mirrors the persisted anomaly_alerts primary key (with the day
// normalized to a calendar date, exactly like the store's CAST(... AS DATE)).
type anomalyKey struct {
	tenant, scope, seriesKey, currency, day, direction string
}

// fakeAnomalyStore is a deterministic, clock-free anomalyStore. Its
// InsertNewAnomalyAlerts faithfully reproduces the real store's insert-if-absent
// semantics: it dedups on the primary-key identity (day normalized to a calendar
// date), echoes each newly-inserted input value VERBATIM, and preserves input
// order - the contract CheckAndNotify's map lookup depends on.
type fakeAnomalyStore struct {
	currencies []string
	daily      map[string]storage.DailyCosts
	seen       map[anomalyKey]bool
	count      map[string]int

	failCurrencies bool
	failDaily      bool
	failInsert     bool
	failCount      bool
}

func newFakeAnomalyStore(currencies []string, daily map[string]storage.DailyCosts) *fakeAnomalyStore {
	return &fakeAnomalyStore{
		currencies: currencies,
		daily:      daily,
		seen:       map[anomalyKey]bool{},
		count:      map[string]int{},
	}
}

func (f *fakeAnomalyStore) BillingCurrencies(_ context.Context, _ string, _, _ time.Time) ([]string, error) {
	if f.failCurrencies {
		return nil, errors.New("currencies unavailable")
	}
	return append([]string(nil), f.currencies...), nil
}

func (f *fakeAnomalyStore) DailyCostsByService(_ context.Context, _ string, _, _ time.Time, currency string, _ ...storage.CostGroupBy) (storage.DailyCosts, error) {
	if f.failDaily {
		return storage.DailyCosts{}, errors.New("daily costs unavailable")
	}
	return f.daily[currency], nil
}

func (f *fakeAnomalyStore) InsertNewAnomalyAlerts(_ context.Context, alerts []storage.AnomalyAlert, _ time.Time) ([]storage.AnomalyAlert, error) {
	if f.failInsert {
		return nil, errors.New("insert failed")
	}
	inserted := []storage.AnomalyAlert{}
	for _, a := range alerts {
		key := anomalyKey{
			tenant: a.TenantID, scope: a.Scope, seriesKey: a.SeriesKey,
			currency: a.Currency, day: a.Date.UTC().Format(time.DateOnly), direction: a.Direction,
		}
		if f.seen[key] {
			continue
		}
		f.seen[key] = true
		f.count[a.TenantID]++
		inserted = append(inserted, a) // echo the input value verbatim
	}
	return inserted, nil
}

func (f *fakeAnomalyStore) AnomalyAlertCount(_ context.Context, tenant string) (int, error) {
	if f.failCount {
		return 0, errors.New("count failed")
	}
	return f.count[tenant], nil
}

// flatSpikeDip builds a single-service ("compute") daily series for one currency
// with a 10-day flat nonzero baseline (100), then a spike (1000) and a dip (10).
// With a flat nonzero baseline the detector's scaled-MAD threshold is zero and
// the relative floor (0.1 * |median|) gates, so the spike flags an increase and
// the dip a decrease - on BOTH the total series and the "compute" key series
// (they are identical when there is one service). That yields four flags per
// currency: {total,increase}, {key,increase}, {total,decrease}, {key,decrease}.
func flatSpikeDip(currency string) storage.DailyCosts {
	base := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	values := make([]decimal.Decimal, 0, 12)
	for i := 0; i < 10; i++ {
		values = append(values, decimal.NewFromInt(100))
	}
	values = append(values, decimal.NewFromInt(1000)) // spike -> increase
	values = append(values, decimal.NewFromInt(10))   // dip   -> decrease
	days := make([]storage.DayCosts, len(values))
	for i, v := range values {
		days[i] = storage.DayCosts{
			Date:     base.AddDate(0, 0, i),
			Services: []storage.ServiceCost{{ServiceName: "compute", Cost: v}},
		}
	}
	return storage.DailyCosts{Currency: currency, Days: days}
}

func fixedNow() func() time.Time {
	at := time.Date(2026, 7, 19, 12, 0, 0, 0, time.UTC)
	return func() time.Time { return at }
}

func newTestAnomalyNotifier(t *testing.T, channels []Channel, store anomalyStore, logger *slog.Logger) *AnomalyNotifier {
	t.Helper()
	return NewAnomalyNotifier(channels, store, logger, "default", fixedNow())
}

// TestAnomalyNotifierAlertsOnceThenDedups pins that a newly-detected anomaly
// fires exactly one alert and a second scan over the same history fires zero:
// the persisted dedup table suppresses the re-page.
func TestAnomalyNotifierAlertsOnceThenDedups(t *testing.T) {
	store := newFakeAnomalyStore([]string{"USD"}, map[string]storage.DailyCosts{"USD": flatSpikeDip("USD")})
	ch := &recordingChannel{name: "w"}
	notifier := newTestAnomalyNotifier(t, []Channel{ch}, store, nil)

	notifier.CheckAndNotify(context.Background())
	first := len(ch.anomalies())
	if first == 0 {
		t.Fatal("first CheckAndNotify fired no anomaly alerts; expected at least one newly-detected anomaly")
	}

	notifier.CheckAndNotify(context.Background())
	if got := len(ch.anomalies()); got != first {
		t.Fatalf("second CheckAndNotify over unchanged history fired %d new alerts, want 0 (dedup)", got-first)
	}
}

// TestAnomalyNotifierSeedSilencesFirstEnable pins that Seed records history
// WITHOUT alerting, so the first CheckAndNotify after a first enable never storms.
func TestAnomalyNotifierSeedSilencesFirstEnable(t *testing.T) {
	store := newFakeAnomalyStore([]string{"USD"}, map[string]storage.DailyCosts{"USD": flatSpikeDip("USD")})
	ch := &recordingChannel{name: "w"}
	notifier := newTestAnomalyNotifier(t, []Channel{ch}, store, nil)

	if err := notifier.Seed(context.Background()); err != nil {
		t.Fatalf("Seed: %v", err)
	}
	if len(ch.anomalies()) != 0 {
		t.Fatalf("Seed fired %d alerts; seeding must record without alerting", len(ch.anomalies()))
	}
	if got, _ := store.AnomalyAlertCount(context.Background(), "default"); got == 0 {
		t.Fatal("Seed recorded nothing; the first enable would not be silenced")
	}

	notifier.CheckAndNotify(context.Background())
	if got := len(ch.anomalies()); got != 0 {
		t.Fatalf("first CheckAndNotify after Seed fired %d alerts, want 0 (no retroactive storm)", got)
	}
}

// TestAnomalyNotifierBothScopesDirectionsCurrencies pins that total AND
// per-service scopes, increase AND decrease directions, and two currencies all
// alert, and that the two currencies dedup independently.
func TestAnomalyNotifierBothScopesDirectionsCurrencies(t *testing.T) {
	store := newFakeAnomalyStore([]string{"EUR", "USD"}, map[string]storage.DailyCosts{
		"USD": flatSpikeDip("USD"),
		"EUR": flatSpikeDip("EUR"),
	})
	ch := &recordingChannel{name: "w"}
	notifier := newTestAnomalyNotifier(t, []Channel{ch}, store, nil)

	notifier.CheckAndNotify(context.Background())
	msgs := ch.anomalies()
	// Four flags per currency (total/key x increase/decrease), two currencies.
	if len(msgs) != 8 {
		t.Fatalf("first scan fired %d alerts, want 8 (4 per currency x 2 currencies)", len(msgs))
	}

	scopes, directions, currencies := map[string]bool{}, map[string]bool{}, map[string]bool{}
	for _, m := range msgs {
		scopes[m.Scope] = true
		directions[m.Direction] = true
		currencies[m.Currency] = true
		if m.Kind != "anomaly" {
			t.Errorf("kind = %q, want \"anomaly\"", m.Kind)
		}
	}
	if !scopes["total"] || !scopes["key"] {
		t.Errorf("scopes = %v, want both total and key", scopes)
	}
	if !directions["increase"] || !directions["decrease"] {
		t.Errorf("directions = %v, want both increase and decrease", directions)
	}
	if !currencies["USD"] || !currencies["EUR"] {
		t.Errorf("currencies = %v, want both USD and EUR", currencies)
	}

	// A second scan dedups every identity independently: zero re-fires.
	notifier.CheckAndNotify(context.Background())
	if got := len(ch.anomalies()); got != 8 {
		t.Fatalf("second scan added %d alerts, want 0 (per-currency dedup)", got-8)
	}
}

// TestAnomalyMessageWhitelistExactMoneyCardinalClean marshals an AnomalyMessage
// and pins: exactly the whitelist keys, amounts are exact decimal strings (a
// 19-significant-digit value survives, proving no float64), and no credential or
// AI-content marker leaks.
func TestAnomalyMessageWhitelistExactMoneyCardinalClean(t *testing.T) {
	exact := "123.4567890123456789" // 19 significant digits: unrepresentable as float64
	sf := anomalyscan.ScopedFlag{
		Scope: "key",
		Key:   "compute",
		Flag: anomaly.Flag{
			Date:      time.Date(2026, 6, 12, 0, 0, 0, 0, time.UTC),
			Direction: "increase",
			Observed:  decimal.RequireFromString(exact),
			Median:    decimal.RequireFromString("100.5"),
			Deviation: decimal.RequireFromString("22.9567890123456789"),
			Threshold: decimal.RequireFromString("0"),
		},
	}
	msg := buildAnomalyMessage("default", "USD", sf)

	if msg.Observed != exact {
		t.Fatalf("observed = %q, want the exact decimal %q (a float64 round-trip would lose precision)", msg.Observed, exact)
	}
	if msg.Date != "2026-06-12T00:00:00Z" {
		t.Fatalf("date = %q, want RFC3339 UTC", msg.Date)
	}

	body, err := json.Marshal(msg)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), exact) {
		t.Fatalf("marshalled body dropped the exact amount: %s", body)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(body, &fields); err != nil {
		t.Fatal(err)
	}
	if len(fields) != len(anomalyWhitelistKeys) {
		t.Fatalf("payload has %d fields %v, want exactly the %d whitelisted keys", len(fields), mapKeys(fields), len(anomalyWhitelistKeys))
	}
	allowed := make(map[string]bool, len(anomalyWhitelistKeys))
	for _, k := range anomalyWhitelistKeys {
		allowed[k] = true
		if _, ok := fields[k]; !ok {
			t.Errorf("payload missing whitelisted field %q", k)
		}
	}
	for k := range fields {
		if !allowed[k] {
			t.Errorf("payload carries non-whitelisted field %q", k)
		}
	}
	// No credential marker and no AI prompt/response content marker.
	for _, forbidden := range []string{"Bearer", "prompt", "response", "completion", "content", "choices"} {
		if strings.Contains(string(body), forbidden) {
			t.Errorf("payload contains forbidden token %q: %s", forbidden, body)
		}
	}
}

// TestAnomalyNotifierSendErrorIsolatedAndLogged pins that a failing channel does
// not panic or propagate: the error is logged and every other channel still runs.
func TestAnomalyNotifierSendErrorIsolatedAndLogged(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, nil))
	store := newFakeAnomalyStore([]string{"USD"}, map[string]storage.DailyCosts{"USD": flatSpikeDip("USD")})
	bad := &recordingChannel{name: "bad", err: errors.New("channel down")}
	good := &recordingChannel{name: "good"}
	notifier := newTestAnomalyNotifier(t, []Channel{bad, good}, store, logger)

	// Returns normally (no panic, no error value to propagate).
	notifier.CheckAndNotify(context.Background())

	if len(bad.anomalies()) == 0 {
		t.Fatal("first channel was not attempted")
	}
	if len(good.anomalies()) != len(bad.anomalies()) {
		t.Fatalf("second channel received %d after the first errored, want %d", len(good.anomalies()), len(bad.anomalies()))
	}
	if !bytes.Contains(buf.Bytes(), []byte("bad")) {
		t.Errorf("channel error was not logged: %s", buf.String())
	}
}

// TestAnomalySlackTextNoEmDash pins the anomaly Slack summary carries the subject
// and figures and contains no em dash. The em-dash needle is written as the Go
// Unicode escape sequence backslash-u-2014, NOT a literal em-dash byte, so this
// source line does not itself trip the added-line em-dash gate while still
// checking the same rune at runtime.
func TestAnomalySlackTextNoEmDash(t *testing.T) {
	total := anomalySlackText(AnomalyMessage{
		Scope: "total", Currency: "USD", Date: "2026-06-12T00:00:00Z",
		Direction: "increase", Observed: "1000", Median: "100",
	})
	for _, want := range []string{"total", "increase", "USD", "1000", "100"} {
		if !strings.Contains(total, want) {
			t.Errorf("anomaly slack text %q missing %q", total, want)
		}
	}
	keyed := anomalySlackText(AnomalyMessage{
		Scope: "key", Key: "compute", Currency: "EUR", Date: "2026-06-13T00:00:00Z",
		Direction: "decrease", Observed: "10", Median: "100",
	})
	if !strings.Contains(keyed, "compute") || !strings.Contains(keyed, "decrease") {
		t.Errorf("keyed anomaly slack text = %q", keyed)
	}
	const emDash = "\u2014"
	for _, text := range []string{total, keyed} {
		if strings.Contains(text, emDash) {
			t.Errorf("anomaly slack text contains an em dash: %q", text)
		}
	}
}

// TestAnomalyNotifierScanErrorsNeverPropagate pins that a store failure at any
// stage is swallowed and logged, never panicking or emitting an alert.
func TestAnomalyNotifierScanErrorsNeverPropagate(t *testing.T) {
	for _, tc := range []struct {
		name     string
		sabotage func(*fakeAnomalyStore)
	}{
		{"currencies", func(s *fakeAnomalyStore) { s.failCurrencies = true }},
		{"daily", func(s *fakeAnomalyStore) { s.failDaily = true }},
		{"insert", func(s *fakeAnomalyStore) { s.failInsert = true }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var buf bytes.Buffer
			logger := slog.New(slog.NewJSONHandler(&buf, nil))
			store := newFakeAnomalyStore([]string{"USD"}, map[string]storage.DailyCosts{"USD": flatSpikeDip("USD")})
			tc.sabotage(store)
			ch := &recordingChannel{name: "w"}
			notifier := newTestAnomalyNotifier(t, []Channel{ch}, store, logger)

			notifier.CheckAndNotify(context.Background()) // must not panic
			if len(ch.anomalies()) != 0 {
				t.Fatalf("a store failure still emitted %d alerts", len(ch.anomalies()))
			}
			if buf.Len() == 0 {
				t.Error("store failure was not logged")
			}
		})
	}
}
