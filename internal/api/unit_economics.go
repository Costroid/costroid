// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Costroid Authors

package api

import (
	"github.com/shopspring/decimal"

	"github.com/Costroid/costroid/internal/storage"
)

// unitCostString performs the only division in unit economics. DivRound at the
// explicit store scale is round-half-away-from-zero; String trims trailing
// zeros. The zero guard is mandatory because DivRound panics on division by 0.
func unitCostString(cost, quantity decimal.Decimal) *string {
	if quantity.IsZero() {
		return nil
	}
	value := cost.DivRound(quantity, storage.MaxDecimalScale).String()
	return &value
}

func storedDayCost(day storage.DayCosts) decimal.Decimal {
	total := decimal.Zero
	for _, service := range day.Services {
		total = total.Add(service.Cost)
	}
	return total
}

// mergeUnitEconomics unions two day-ascending streams. A bin is covered only
// when both streams have that day and quantity is positive; cost sign is
// deliberately irrelevant, so negative and exact-zero cost bins remain real.
func mergeUnitEconomics(metric string, costs storage.DailyCosts, quantities []storage.DayQuantity, currencies []string) UnitEconomics {
	resp := UnitEconomics{
		Metric:     metric,
		Currency:   costs.Currency,
		Currencies: append([]string{}, currencies...),
		Days:       make([]UnitEconomicsDay, 0, len(costs.Days)+len(quantities)),
		Period: UnitEconomicsPeriod{
			CoveredDays: 0,
			Cost:        "0",
			Quantity:    "0",
		},
	}
	periodCost, periodQuantity := decimal.Zero, decimal.Zero
	for ci, qi := 0, 0; ci < len(costs.Days) || qi < len(quantities); {
		costPresent := ci < len(costs.Days)
		quantityPresent := qi < len(quantities)
		useCost := costPresent && (!quantityPresent || costs.Days[ci].Date.Before(quantities[qi].Date))
		useQuantity := quantityPresent && (!costPresent || quantities[qi].Date.Before(costs.Days[ci].Date))

		entry := UnitEconomicsDay{}
		var cost, quantity decimal.Decimal
		switch {
		case useCost:
			entry.Date.Time = costs.Days[ci].Date
			cost = storedDayCost(costs.Days[ci])
			value := cost.String()
			entry.Cost = &value
			ci++
			quantityPresent = false
		case useQuantity:
			entry.Date.Time = quantities[qi].Date
			quantity = quantities[qi].Quantity
			value := quantity.String()
			entry.Quantity = &value
			qi++
			costPresent = false
		default:
			entry.Date.Time = costs.Days[ci].Date
			cost = storedDayCost(costs.Days[ci])
			quantity = quantities[qi].Quantity
			costValue, quantityValue := cost.String(), quantity.String()
			entry.Cost, entry.Quantity = &costValue, &quantityValue
			ci++
			qi++
		}

		if costPresent && quantityPresent && quantity.IsPositive() {
			entry.UnitCost = unitCostString(cost, quantity)
			resp.Period.CoveredDays++
			periodCost = periodCost.Add(cost)
			periodQuantity = periodQuantity.Add(quantity)
		}
		resp.Days = append(resp.Days, entry)
	}
	resp.Period.Cost = periodCost.String()
	resp.Period.Quantity = periodQuantity.String()
	if resp.Period.CoveredDays > 0 {
		resp.Period.UnitCost = unitCostString(periodCost, periodQuantity)
	}
	return resp
}
