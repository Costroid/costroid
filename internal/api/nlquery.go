// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Costroid Authors

package api

import (
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/Costroid/costroid/internal/focus"
	"github.com/Costroid/costroid/internal/nlquery"
)

// ValidatePlan rejects any model plan that the named API endpoint does not
// accept. It never defaults, coerces, narrows, or otherwise repairs a plan.
func ValidatePlan(plan nlquery.Plan, metricNames []string) error {
	supportsGrouping := false
	switch plan.Endpoint {
	case "costs-daily":
		supportsGrouping = true
		if plan.GroupBy != nil && !GetDailyCostsParamsGroupBy(*plan.GroupBy).Valid() {
			return errors.New("invalid groupBy value")
		}
	case "costs-summary":
		supportsGrouping = true
		if plan.GroupBy != nil && !GetCostsSummaryParamsGroupBy(*plan.GroupBy).Valid() {
			return errors.New("invalid groupBy value")
		}
	case "anomalies":
		supportsGrouping = true
		if plan.GroupBy != nil && !GetAnomaliesParamsGroupBy(*plan.GroupBy).Valid() {
			return errors.New("invalid groupBy value")
		}
	case "tokens", "usage", "unit-economics":
		if plan.GroupBy != nil {
			return fmt.Errorf("endpoint %s does not accept groupBy", plan.Endpoint)
		}
	default:
		return errors.New("unknown plan endpoint")
	}
	if plan.Endpoint == "tokens" || plan.Endpoint == "usage" {
		if plan.Currency != nil {
			return fmt.Errorf("endpoint %s does not accept currency", plan.Endpoint)
		}
		if plan.Provider != nil {
			return fmt.Errorf("endpoint %s does not accept provider", plan.Endpoint)
		}
	}

	if err := validateTagGrouping(supportsGrouping && plan.GroupBy != nil && *plan.GroupBy == "tag", plan.TagKey); err != nil {
		return err
	}
	if plan.Currency != nil && !billingCurrencyPattern.MatchString(*plan.Currency) {
		return errors.New("currency must be a three-letter uppercase code (for example, USD)")
	}
	if plan.Provider != nil && (*plan.Provider == "" || len(*plan.Provider) > focus.MaxFreeTextBytes) {
		// MaxFreeTextBytes is an ingest safety guard; reuse here is deliberate
		// as the outbound free-text bound, and values are rejected, not truncated.
		return errors.New(providerShapeMessage)
	}
	if err := validatePlanDates(plan.Start, plan.End); err != nil {
		return err
	}

	if plan.Endpoint == "unit-economics" {
		if plan.Metric == nil || *plan.Metric == "" {
			return errors.New("unit-economics requires a metric")
		}
		if len(*plan.Metric) > focus.MaxFreeTextBytes {
			// MaxFreeTextBytes is an ingest safety guard; reuse here is deliberate
			// as the outbound free-text bound, and values are rejected, not truncated.
			return fmt.Errorf("metric must be at most %d bytes", focus.MaxFreeTextBytes)
		}
		if !slices.Contains(metricNames, *plan.Metric) {
			return errors.New("unknown business metric")
		}
	} else if plan.Metric != nil {
		return fmt.Errorf("endpoint %s does not accept metric", plan.Endpoint)
	}
	return nil
}

func validatePlanDates(start, end *string) error {
	parse := func(name string, value *string) (time.Time, error) {
		if value == nil {
			return time.Time{}, nil
		}
		parsed, err := time.Parse("2006-01-02", *value)
		if err != nil || parsed.Format("2006-01-02") != *value {
			return time.Time{}, fmt.Errorf("%s must be YYYY-MM-DD", name)
		}
		return parsed, nil
	}
	startTime, err := parse("start", start)
	if err != nil {
		return err
	}
	endTime, err := parse("end", end)
	if err != nil {
		return err
	}
	if start != nil && end != nil && startTime.After(endTime) {
		return errors.New("start date must not be after end date")
	}
	return nil
}
