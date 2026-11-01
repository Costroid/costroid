package store

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/Costroid/costroid/internal/decimal"
)

// Line is one group of a pull request's rows. Lines never add across
// currencies or across units.
type Line struct {
	Currency string
	// BillingAccountID is a sender's statement, so a line never adds across it.
	BillingAccountID string
	ServiceCategory  string
	Agent            string
	Model            string
	SkuMeter         string
	ConsumedUnit     string
	CostBasis        string
	CostSource       string
	AttributionGrade string
	RunIdentity      string
	BudgetEligible   bool
	Rows             int64
	ConsumedQuantity decimal.Decimal
	ListCost         decimal.Decimal
	EffectiveCost    decimal.Decimal
	FirstCharge      time.Time
	LastCharge       time.Time
}

const pullRequestLines = `
SELECT billing_currency, billing_account_id, service_category, coalesce(agent, ''), coalesce(model, ''), sku_meter,
	consumed_unit, cost_basis, cost_source, attribution_grade, run_identity, budget_eligible,
	count(*), sum(consumed_quantity)::text, sum(list_cost)::text, sum(effective_cost)::text,
	min(charge_period_start), max(charge_period_end)
FROM focus_rows
WHERE repo = $1 AND pr = $2
GROUP BY 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12
ORDER BY 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12`

// PullRequest derives one pull request's lines from the rows.
func (s *Store) PullRequest(ctx context.Context, tenant TenantID, repo, pr string) ([]Line, error) {
	var lines []Line
	err := s.inTenant(ctx, tenant, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, pullRequestLines, repo, pr)
		if err != nil {
			return fmt.Errorf("store: read pull request: %w", err)
		}
		lines, err = pgx.CollectRows(rows, scanLine)
		if err != nil {
			return fmt.Errorf("store: read pull request: %w", err)
		}
		return nil
	})
	return lines, err
}

func scanLine(row pgx.CollectableRow) (Line, error) {
	var line Line
	var quantity, list, effective string
	err := row.Scan(&line.Currency, &line.BillingAccountID, &line.ServiceCategory, &line.Agent, &line.Model, &line.SkuMeter,
		&line.ConsumedUnit, &line.CostBasis, &line.CostSource, &line.AttributionGrade, &line.RunIdentity,
		&line.BudgetEligible, &line.Rows, &quantity, &list, &effective, &line.FirstCharge, &line.LastCharge)
	if err != nil {
		return Line{}, err
	}
	for _, field := range []struct {
		text   string
		target *decimal.Decimal
	}{{quantity, &line.ConsumedQuantity}, {list, &line.ListCost}, {effective, &line.EffectiveCost}} {
		if *field.target, err = decimal.Parse(field.text); err != nil {
			return Line{}, err
		}
	}
	return line, nil
}
