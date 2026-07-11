// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Costroid Authors

// Package demodata seeds the credential-free synthetic dataset served by
// costroid demo. It uses the product's real FOCUS import, AI-usage storage, and
// business-metrics import paths and performs no network or credential access.
package demodata

import (
	"context"
	"encoding/csv"
	"fmt"
	"math/rand"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/shopspring/decimal"

	"github.com/Costroid/costroid/internal/businessmetrics"
	"github.com/Costroid/costroid/internal/focus"
	"github.com/Costroid/costroid/internal/ingest"
	"github.com/Costroid/costroid/internal/ingest/focuscsv"
	"github.com/Costroid/costroid/internal/storage"
)

// DefaultSeed is fixed so repeated demos have the same shape while asOf keeps
// their six-month window fresh.
const DefaultSeed int64 = 24024

// ExactAmount is deliberately wider than 15 fractional digits. It is stored as
// a BilledCost on the first day and must survive the importer, DuckDB, and API
// byte-identically.
const ExactAmount = "3141.592653589793238"

const businessMetricName = "requests served"

var focus10Header = []string{
	"AvailabilityZone", "BilledCost", "BillingAccountId", "BillingAccountName", "BillingCurrency",
	"BillingPeriodEnd", "BillingPeriodStart", "ChargeCategory", "ChargeClass", "ChargeDescription",
	"ChargeFrequency", "ChargePeriodEnd", "ChargePeriodStart", "CommitmentDiscountCategory",
	"CommitmentDiscountId", "CommitmentDiscountName", "CommitmentDiscountStatus", "CommitmentDiscountType",
	"ConsumedQuantity", "ConsumedUnit", "ContractedCost", "ContractedUnitPrice", "EffectiveCost",
	"InvoiceIssuerName", "ListCost", "ListUnitPrice", "PricingCategory", "PricingQuantity", "PricingUnit",
	"ProviderName", "PublisherName", "RegionId", "RegionName", "ResourceId", "ResourceName", "ResourceType",
	"ServiceCategory", "ServiceName", "SkuId", "SkuPriceId", "SubAccountId", "SubAccountName", "Tags",
}

type serviceSpec struct {
	provider string
	service  string
	weightBP int64
	region   string
	resource string
	tagged   bool
}

var services = []serviceSpec{
	{provider: "Amazon Web Services", service: "Amazon EC2", weightBP: 6000, region: "us-east-1", resource: "arn:aws:ec2:us-east-1:000000000000:instance/i-demo", tagged: true},
	{provider: "Microsoft", service: "Virtual Machines", weightBP: 2000, region: "eastus", resource: "/subscriptions/demo/resourceGroups/platform/providers/Microsoft.Compute/virtualMachines/demo", tagged: true},
	{provider: "Google", service: "Compute Engine", weightBP: 500, region: "us-central1", resource: "//compute.googleapis.com/projects/costroid-demo/zones/us-central1-a/instances/demo"},
	{provider: "Google", service: "Cloud Storage", weightBP: 500, region: "europe-west1", resource: "//storage.googleapis.com/projects/_/buckets/costroid-demo"},
	{provider: "Google", service: "BigQuery", weightBP: 500, region: "us-central1", resource: "//bigquery.googleapis.com/projects/costroid-demo/datasets/analytics"},
	{provider: "OpenAI", service: "GPT-5", weightBP: 300, region: "global", resource: "openai://models/gpt-5"},
	{provider: "Anthropic", service: "Claude", weightBP: 200, region: "global", resource: "anthropic://models/claude"},
}

// Window returns the inclusive rolling demo window in UTC.
func Window(asOf time.Time) (time.Time, time.Time) {
	end := utcDay(asOf)
	return end.AddDate(0, -6, 1), end
}

// SpikeDate returns the one date intentionally made anomalous.
func SpikeDate(asOf time.Time) time.Time {
	start, _ := Window(asOf)
	return start.AddDate(0, 4, 0)
}

// BusinessMetricName is the unit-economics dimension seeded by Seed.
func BusinessMetricName() string { return businessMetricName }

// Seed deterministically replaces the synthetic demo dataset for (asOf, seed).
// It uses a local pseudo-random generator only; no global randomness, wall
// clock, connector, credential, or network source is consulted.
func Seed(ctx context.Context, store *storage.DuckDB, asOf time.Time, seed int64) error {
	rng := rand.New(rand.NewSource(seed))
	start, end := Window(asOf)

	tmpDir, err := os.MkdirTemp("", "costroid-demodata-")
	if err != nil {
		return fmt.Errorf("creating synthetic CSV directory: %w", err)
	}
	defer func() { _ = os.RemoveAll(tmpDir) }()

	costPath := tmpDir + "/focus-1.0.csv"
	if err := writeCostCSV(costPath, start, end, SpikeDate(asOf), rng); err != nil {
		return err
	}
	periods, warnings, err := focuscsv.Discover(costPath, focus.V1_0, "demo-focus", false)
	if err != nil {
		return fmt.Errorf("discovering synthetic FOCUS CSV: %w", err)
	}
	if len(warnings) != 0 {
		return fmt.Errorf("synthetic FOCUS CSV produced unexpected warnings: %s", strings.Join(warnings, "; "))
	}
	for _, period := range periods {
		if _, err := ingest.Run(ctx, period.Conn, store, focus.DefaultTenant); err != nil {
			return fmt.Errorf("ingesting synthetic period %s: %w", period.Month, err)
		}
	}

	if err := seedUsage(ctx, store, start, end, seed); err != nil {
		return err
	}
	if err := seedBusinessMetrics(ctx, store, start, end); err != nil {
		return err
	}
	return nil
}

func writeCostCSV(path string, start, end, spike time.Time, rng *rand.Rand) error {
	f, err := os.Create(path)
	if err != nil {
		return fmt.Errorf("creating synthetic FOCUS CSV: %w", err)
	}
	w := csv.NewWriter(f)
	if err := w.Write(focus10Header); err != nil {
		_ = f.Close()
		return fmt.Errorf("writing synthetic FOCUS header: %w", err)
	}

	dayIndex := 0
	for day := start; !day.After(end); day = day.AddDate(0, 0, 1) {
		for serviceIndex, spec := range services {
			amount := dailyAmount(dayIndex, serviceIndex, spec.weightBP, rng)
			if dayIndex == 0 && serviceIndex == 0 {
				amount = ExactAmount
			}
			if day.Equal(spike) && spec.service == "BigQuery" {
				value, err := decimal.NewFromString(amount)
				if err != nil {
					return fmt.Errorf("parsing synthetic spike amount: %w", err)
				}
				amount = value.Mul(decimal.NewFromInt(8)).String()
			}
			row := focusRow(day, spec, amount)
			values := make([]string, len(focus10Header))
			for i, column := range focus10Header {
				values[i] = row[column]
			}
			if err := w.Write(values); err != nil {
				_ = f.Close()
				return fmt.Errorf("writing synthetic FOCUS row: %w", err)
			}
		}
		dayIndex++
	}
	w.Flush()
	if err := w.Error(); err != nil {
		_ = f.Close()
		return fmt.Errorf("flushing synthetic FOCUS CSV: %w", err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("closing synthetic FOCUS CSV: %w", err)
	}
	return nil
}

func dailyAmount(dayIndex, serviceIndex int, weightBP int64, rng *rand.Rand) string {
	const baseCents int64 = 500_000
	seasonal := [...]int64{10_300, 10_200, 10_100, 10_000, 10_100, 9_800, 9_700}
	growthBP := int64(10_000 + dayIndex*200/30)
	noiseBP := int64(9_950 + rng.Intn(101))
	cents := baseCents * weightBP / 10_000
	cents = cents * seasonal[dayIndex%len(seasonal)] / 10_000
	cents = cents * growthBP / 10_000
	cents = cents * noiseBP / 10_000
	// Keep each service's noise stream distinct even if a future weight repeats.
	cents += int64(serviceIndex % 2)
	return centsString(cents)
}

func centsString(cents int64) string {
	return strconv.FormatInt(cents/100, 10) + "." + fmt.Sprintf("%02d", cents%100)
}

func focusRow(day time.Time, spec serviceSpec, amount string) map[string]string {
	billingStart := time.Date(day.Year(), day.Month(), 1, 0, 0, 0, 0, time.UTC)
	billingEnd := billingStart.AddDate(0, 1, 0)
	tags := ""
	if spec.tagged {
		tags = `{"environment":"production"}`
	}
	providerID := strings.ToLower(strings.ReplaceAll(spec.provider, " ", "-"))
	return map[string]string{
		"BilledCost":         amount,
		"BillingAccountId":   "demo-" + providerID,
		"BillingAccountName": "Synthetic demo account",
		"BillingCurrency":    "USD",
		"BillingPeriodEnd":   billingEnd.Format(time.RFC3339),
		"BillingPeriodStart": billingStart.Format(time.RFC3339),
		"ChargeCategory":     "Usage",
		"ChargeDescription":  "Synthetic daily usage",
		"ChargePeriodEnd":    day.AddDate(0, 0, 1).Format(time.RFC3339),
		"ChargePeriodStart":  day.Format(time.RFC3339),
		"ContractedCost":     amount,
		"EffectiveCost":      amount,
		"InvoiceIssuerName":  spec.provider,
		"ListCost":           amount,
		"ProviderName":       spec.provider,
		"PublisherName":      spec.provider,
		"RegionId":           spec.region,
		"RegionName":         spec.region,
		"ResourceId":         spec.resource,
		"ResourceName":       "synthetic-demo-resource",
		"ResourceType":       "Synthetic resource",
		"ServiceCategory":    "Compute",
		"ServiceName":        spec.service,
		"SkuId":              "demo-" + strings.ToLower(strings.ReplaceAll(spec.service, " ", "-")),
		"SubAccountId":       "demo-production",
		"SubAccountName":     "Production",
		"Tags":               tags,
	}
}

func seedUsage(ctx context.Context, store *storage.DuckDB, start, end time.Time, seed int64) error {
	metrics := make([]storage.Metric, 0, int(end.Sub(start).Hours()/24+1)*4)
	for i, day := 0, start; !day.After(end); i, day = i+1, day.AddDate(0, 0, 1) {
		growth := int64(1000 + i*2)
		metrics = append(metrics,
			storage.Metric{ChargePeriodStart: day, ServiceName: "GPT-5", ServiceTier: "standard", MetricName: "input_tokens", Unit: "Tokens", Quantity: decimal.NewFromInt(growth * 900)},
			storage.Metric{ChargePeriodStart: day, ServiceName: "GPT-5", ServiceTier: "standard", MetricName: "num_model_requests", Unit: "Requests", Quantity: decimal.NewFromInt(growth)},
			storage.Metric{ChargePeriodStart: day, ServiceName: "Claude", ServiceTier: "standard", MetricName: "uncached_input_tokens", Unit: "Tokens", Quantity: decimal.NewFromInt(growth * 600)},
			storage.Metric{ChargePeriodStart: day, ServiceName: "Claude", ServiceTier: "standard", MetricName: "web_search_requests", Unit: "Requests", Quantity: decimal.NewFromInt(growth / 20)},
		)
	}
	batch := storage.UsageBatch{Connector: "demo-ai-usage", SourceIdentity: fmt.Sprintf("synthetic/%s/%s/%d", start.Format(time.DateOnly), end.Format(time.DateOnly), seed), TenantID: focus.DefaultTenant}
	if err := store.ReplaceUsageBatch(ctx, batch, metrics); err != nil {
		return fmt.Errorf("storing synthetic AI usage: %w", err)
	}
	return nil
}

func seedBusinessMetrics(ctx context.Context, store *storage.DuckDB, start, end time.Time) error {
	var input strings.Builder
	input.WriteString("date,metric,quantity\n")
	for i, day := 0, start; !day.After(end); i, day = i+1, day.AddDate(0, 0, 1) {
		quantity := 90_000 + i*320 + [...]int{3000, 2200, 1400, 800, 1800, -1800, -2600}[i%7]
		fmt.Fprintf(&input, "%s,%s,%d\n", day.Format(time.DateOnly), businessMetricName, quantity)
	}
	rows, err := businessmetrics.Parse(strings.NewReader(input.String()))
	if err != nil {
		return fmt.Errorf("parsing synthetic business metrics: %w", err)
	}
	if err := store.ReplaceBusinessMetricsBatch(ctx, focus.DefaultTenant, "demo-business", rows); err != nil {
		return fmt.Errorf("storing synthetic business metrics: %w", err)
	}
	return nil
}

func utcDay(t time.Time) time.Time {
	t = t.UTC()
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC)
}
