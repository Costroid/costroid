package server

import (
	"context"
	"net/http"
	"time"

	"github.com/Costroid/costroid/internal/decimal"
	"github.com/Costroid/costroid/internal/focus"
	"github.com/Costroid/costroid/internal/store"
)

// labels are the accuracy labels of the specification's section C.
var labels = map[string]string{
	string(focus.Measured):       "Measured",
	string(focus.ListEquivalent): "List-equivalent",
}

type viewLine struct {
	Currency         string `json:"currency"`
	BillingAccountID string `json:"billing_account_id"`
	ServiceCategory  string `json:"service_category"`
	Agent            string `json:"agent,omitempty"`
	Model            string `json:"model,omitempty"`
	Meter            string `json:"meter"`
	Unit             string `json:"unit"`
	Label            string `json:"label"`
	CostBasis        string `json:"cost_basis"`
	CostSource       string `json:"cost_source"`
	AttributionGrade string `json:"attribution_grade"`
	RunIdentity      string `json:"run_identity"`
	BudgetEligible   bool   `json:"budget_eligible"`
	Rows             int64  `json:"rows"`
	Quantity         string `json:"quantity"`
	ListCost         string `json:"list_cost"`
	EffectiveCost    string `json:"effective_cost"`
	FirstCharge      string `json:"first_charge"`
	LastCharge       string `json:"last_charge"`
}

// basisTotal adds up one currency and one cost basis. It covers every row of
// the pull request, verified or not, so it is a reading and never a budget's
// evidence.
type basisTotal struct {
	Currency  string `json:"currency"`
	CostBasis string `json:"cost_basis"`
	Label     string `json:"label"`
	ListCost  string `json:"list_cost"`
}

type currencySpend struct {
	Currency string `json:"currency"`
	ListCost string `json:"list_cost"`
}

// budgetView is the only part of the answer a budget rule may read. Evidence
// is false while no token-verified, attributed row exists, and such a pull
// request has no spend for a rule to pass.
type budgetView struct {
	Evidence bool            `json:"evidence"`
	Spend    []currencySpend `json:"spend"`
}

type pullRequestView struct {
	Repository  string       `json:"repository"`
	PullRequest string       `json:"pull_request"`
	Totals      []basisTotal `json:"totals"`
	Budget      budgetView   `json:"budget"`
	Lines       []viewLine   `json:"lines"`
}

func (s *Server) pullRequest(ctx context.Context, tenant store.TenantID, repo, pr string) (pullRequestView, error) {
	lines, err := s.Ledger.PullRequest(ctx, tenant, repo, pr)
	if err != nil {
		return pullRequestView{}, err
	}
	view := pullRequestView{Repository: repo, PullRequest: pr, Lines: make([]viewLine, 0, len(lines)), Totals: totalsOf(lines), Budget: budgetOf(lines)}
	for i := range lines {
		view.Lines = append(view.Lines, viewLineOf(&lines[i]))
	}
	return view, nil
}

// totalsOf sums list cost per currency and cost basis. Neither boundary is ever
// crossed: a measured figure and a list-equivalent one are different claims.
func totalsOf(lines []store.Line) []basisTotal {
	type key struct{ currency, basis string }
	sums := map[key]decimal.Decimal{}
	var order []key
	for i := range lines {
		group := key{lines[i].Currency, lines[i].CostBasis}
		if _, seen := sums[group]; !seen {
			order = append(order, group)
		}
		sums[group] = sums[group].Add(lines[i].ListCost)
	}
	totals := make([]basisTotal, 0, len(order))
	for _, group := range order {
		totals = append(totals, basisTotal{group.currency, group.basis, labels[group.basis], sums[group].String()})
	}
	return totals
}

// budgetOf sums list cost per currency over eligible lines only. Informational
// lines carry zero, so they never move the figure.
func budgetOf(lines []store.Line) budgetView {
	budget := budgetView{Spend: []currencySpend{}}
	totals := map[string]decimal.Decimal{}
	var order []string
	for i := range lines {
		line := &lines[i]
		if !line.BudgetEligible {
			continue
		}
		budget.Evidence = true
		if _, seen := totals[line.Currency]; !seen {
			order = append(order, line.Currency)
		}
		totals[line.Currency] = totals[line.Currency].Add(line.ListCost)
	}
	for _, code := range order {
		budget.Spend = append(budget.Spend, currencySpend{Currency: code, ListCost: totals[code].String()})
	}
	return budget
}

func viewLineOf(line *store.Line) viewLine {
	return viewLine{
		Currency:         line.Currency,
		BillingAccountID: line.BillingAccountID,
		ServiceCategory:  line.ServiceCategory,
		Agent:            line.Agent,
		Model:            line.Model,
		Meter:            line.SkuMeter,
		Unit:             line.ConsumedUnit,
		Label:            labels[line.CostBasis],
		CostBasis:        line.CostBasis,
		CostSource:       line.CostSource,
		AttributionGrade: line.AttributionGrade,
		RunIdentity:      line.RunIdentity,
		BudgetEligible:   line.BudgetEligible,
		Rows:             line.Rows,
		Quantity:         line.ConsumedQuantity.String(),
		ListCost:         line.ListCost.String(),
		EffectiveCost:    line.EffectiveCost.String(),
		FirstCharge:      line.FirstCharge.UTC().Format(time.RFC3339),
		LastCharge:       line.LastCharge.UTC().Format(time.RFC3339),
	}
}

func (s *Server) handlePullRequest(w http.ResponseWriter, r *http.Request) {
	who, err := s.authenticate(r)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	repo, pr := r.URL.Query().Get("repo"), r.PathValue("pr")
	if !repoTag.MatchString(repo) || !prTag.MatchString(pr) {
		s.fail(w, r, refuse(http.StatusBadRequest, codeBadQuery, "repo must be a lower-case host and path, and the pull request a number."))
		return
	}
	if who.identity != nil && who.identity.Repository != repo {
		s.fail(w, r, refuse(http.StatusForbidden, codeWrongRepo, "This CI token reads its own repository only."))
		return
	}
	view, err := s.pullRequest(r.Context(), who.tenant, repo, pr)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, view)
}
