package reconcile

import (
	"context"
	"fmt"
	"maps"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"time"

	"github.com/Costroid/costroid/internal/catalog"
	"github.com/Costroid/costroid/internal/decimal"
	"github.com/Costroid/costroid/internal/focus"
)

// The Usage and Cost Analytics API. The legacy cost report groups by workspace
// and description only, so it cannot answer per API key.
// https://platform.claude.com/docs/en/manage-claude/usage-cost-analytics-api
// retrieved 2026-11-01. The cost report's token types:
// https://platform.claude.com/docs/en/api/beta/organization/analytics/cost
// retrieved 2026-11-07.
const (
	anthropicUsage   = "https://api.anthropic.com/v1/organizations/analytics/usage"
	anthropicCost    = "https://api.anthropic.com/v1/organizations/analytics/cost"
	anthropicVersion = "2023-06-01"
	anthropicBeta    = "analytics-usage-cost-2026-08-24"

	anthropicPageSize = 1000
	anthropicMaxSpan  = 31
	anthropicKeyGrain = "api_key_id"
	anthropicScope    = "workspace_id"
	anthropicScopeID  = "wrkspc_"

	centsPerDollar = 2
	costTypeTokens = "tokens"
	speedFast      = "fast"
)

type anthropic struct {
	client Doer
	key    string
}

// NewAnthropic returns the Anthropic fetcher. The key is an Admin API key.
func NewAnthropic(client Doer, key string) Provider {
	return anthropic{client: client, key: key}
}

func (a anthropic) Name() string { return SourceAnthropic }

func (a anthropic) authorize(header http.Header) {
	header.Set("anthropic-version", anthropicVersion)
	header.Set("anthropic-beta", anthropicBeta)
	header.Set("x-api-key", a.key)
}

func (a anthropic) Fetch(ctx context.Context, days Range, credentials []string) (Report, error) {
	report := Report{Source: SourceAnthropic, Unsettled: map[string]bool{}, Refreshed: map[string]time.Time{}}
	for _, grain := range grains(credentials, anthropicScopeID, anthropicKeyGrain, anthropicScope) {
		for _, span := range days.windows(anthropicMaxSpan) {
			if err := a.usage(ctx, grain, span, &report); err != nil {
				return Report{}, err
			}
			if err := a.cost(ctx, grain, span, &report); err != nil {
				return Report{}, err
			}
		}
	}
	return report, nil
}

func anthropicQuery(span window, groupBy ...string) url.Values {
	return url.Values{
		"starting_at":       {span.start.Format(time.RFC3339)},
		"ending_at":         {span.end.Format(time.RFC3339)},
		"time_bucket_width": {"1d"},
		"group_by[]":        groupBy,
		"limit":             {strconv.Itoa(anthropicPageSize)},
		"order_by":          {"time"},
		"order":             {"asc"},
	}
}

type anthropicGroup struct {
	APIKeyID     *string `json:"api_key_id"`
	WorkspaceID  *string `json:"workspace_id"`
	Model        *string `json:"model"`
	ServiceTier  *string `json:"service_tier"`
	InferenceGeo *string `json:"inference_geo"`
	Speed        *string `json:"speed"`
	CostType     *string `json:"cost_type"`
	TokenType    *string `json:"token_type"`
}

// anthropicTokenTypes maps the cost report's token types to classes.
var anthropicTokenTypes = map[string]focus.TokenClass{
	"uncached_input_tokens":                    focus.InputUncached,
	"cache_read_input_tokens":                  focus.CacheRead,
	"cache_creation.ephemeral_5m_input_tokens": focus.CacheWrite5m,
	"cache_creation.ephemeral_1h_input_tokens": focus.CacheWrite1h,
	"output_tokens":                            focus.Output,
}

func (g anthropicGroup) credential(grain string) string {
	if grain == anthropicScope {
		return text(g.WorkspaceID)
	}
	return text(g.APIKeyID)
}

// tier joins two report dimensions into the catalog's one: fast mode is a tier
// on the sheet and a speed on the report. A row with no tier was served
// standard. Priority is read first, because its tokens carry no cost at any
// speed.
func (g anthropicGroup) tier() string {
	if text(g.ServiceTier) == tierPriority {
		return tierPriority
	}
	if text(g.Speed) == speedFast {
		return speedFast
	}
	if g.ServiceTier == nil {
		return catalog.TierStandard
	}
	return *g.ServiceTier
}

func (g anthropicGroup) region() string {
	if g.InferenceGeo == nil {
		return catalog.RegionGlobal
	}
	return *g.InferenceGeo
}

func text(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

type anthropicUsageRow struct {
	StartingAt time.Time      `json:"starting_at"`
	Group      anthropicGroup `json:"group"`
	Usage      struct {
		UncachedInputTokens int64 `json:"uncached_input_tokens"`
		CacheCreation       struct {
			Ephemeral1h int64 `json:"ephemeral_1h_input_tokens"`
			Ephemeral5m int64 `json:"ephemeral_5m_input_tokens"`
		} `json:"cache_creation"`
		CacheReadInputTokens int64 `json:"cache_read_input_tokens"`
		OutputTokens         int64 `json:"output_tokens"`
	} `json:"usage"`
}

func (a anthropic) usage(ctx context.Context, grain string, span window, report *Report) error {
	endpoint := fetch{client: a.client, endpoint: anthropicUsage, authorize: a.authorize}
	query := anthropicQuery(span, grain, "model", "service_tier", "inference_geo", "speed")
	return pages(ctx, endpoint, query, func(current page[anthropicUsageRow]) error {
		report.stamp(span, current.DataRefreshedAt)
		for _, row := range current.Data {
			usage, err := row.usage(grain)
			if err != nil {
				return err
			}
			report.addUsage(usage)
		}
		return nil
	})
}

// usage reads one bucket. A count below zero is no usage the report can
// state, and every figure compared from it would be wrong, so the report is
// refused.
func (r anthropicUsageRow) usage(grain string) (Usage, error) {
	day := r.StartingAt.UTC().Format(dayLayout)
	counts := map[focus.TokenClass]int64{
		focus.InputUncached: r.Usage.UncachedInputTokens,
		focus.CacheRead:     r.Usage.CacheReadInputTokens,
		focus.CacheWrite5m:  r.Usage.CacheCreation.Ephemeral5m,
		focus.CacheWrite1h:  r.Usage.CacheCreation.Ephemeral1h,
		focus.Output:        r.Usage.OutputTokens,
	}
	tokens := make(map[focus.TokenClass]decimal.Decimal, len(counts))
	for _, class := range slices.Sorted(maps.Keys(counts)) {
		if counts[class] < 0 {
			return Usage{}, fmt.Errorf("usage on %s for %s: %s reads %d tokens, below zero", day, text(r.Group.Model), class, counts[class])
		}
		tokens[class] = decimal.FromInt(counts[class])
	}
	return Usage{
		Credential: r.Group.credential(grain),
		Day:        day,
		Model:      text(r.Group.Model),
		Tier:       r.Group.tier(),
		Region:     r.Group.region(),
		Tokens:     tokens,
	}, nil
}

type anthropicCostRow struct {
	StartingAt time.Time      `json:"starting_at"`
	Group      anthropicGroup `json:"group"`
	Cost       struct {
		Currency   string  `json:"currency"`
		Amount     string  `json:"amount"`
		ListAmount *string `json:"list_amount"`
	} `json:"cost"`
}

// cost asks for the dimensions usage is read by, then splits each row by cost
// type and token type, which do not count toward the report's limit of five.
// Each token line then joins one usage row by model and class.
func (a anthropic) cost(ctx context.Context, grain string, span window, report *Report) error {
	endpoint := fetch{client: a.client, endpoint: anthropicCost, authorize: a.authorize}
	query := anthropicQuery(span, grain, "model", "service_tier", "inference_geo", "speed", "cost_type", "token_type")
	return pages(ctx, endpoint, query, func(current page[anthropicCostRow]) error {
		report.stamp(span, current.DataRefreshedAt)
		for _, row := range current.Data {
			cost, err := row.cost(grain)
			if err != nil {
				return err
			}
			report.addCost(cost)
		}
		return nil
	})
}

func (r anthropicCostRow) cost(grain string) (Cost, error) {
	if r.Cost.Currency != currency {
		return Cost{}, fmt.Errorf("currency %q: reports are read in USD", r.Cost.Currency)
	}
	amount, err := parseDollars(r.Cost.Amount)
	if err != nil {
		return Cost{}, fmt.Errorf("amount: %w", err)
	}
	cost := Cost{
		Credential: r.Group.credential(grain),
		Day:        r.StartingAt.UTC().Format(dayLayout),
		Amount:     amount,
		Tokens:     text(r.Group.CostType) == costTypeTokens,
	}
	if r.Group.TokenType != nil {
		cost.Label = *r.Group.TokenType
		cost.Class = anthropicTokenTypes[cost.Label]
		cost.Model, cost.Tier, cost.Region = text(r.Group.Model), r.Group.tier(), r.Group.region()
	}
	if r.Cost.ListAmount != nil {
		list, err := stated(*r.Cost.ListAmount, centsPerDollar)
		if err != nil {
			return Cost{}, fmt.Errorf("list_amount: %w", err)
		}
		cost.List = &list.Value
		cost.ListFigure = &list
	}
	return cost, nil
}

// parseDollars converts Anthropic's amounts, which are decimal strings in cents.
func parseDollars(cents string) (decimal.Decimal, error) {
	amount, err := parseAmount(cents)
	if err != nil {
		return decimal.Decimal{}, err
	}
	return amount.DivPow10(centsPerDollar), nil
}

// stamp keeps a page's freshness stamp for each day of its span, and marks
// the days past it unsettled. Usage after the stamp is missing or partial, and
// a null stamp means nothing is in yet. Where pages disagree, the earliest
// stamp is kept, because the day's figures are no fresher than their oldest
// page.
func (r *Report) stamp(span window, refreshed *time.Time) {
	for _, day := range span.days() {
		if refreshed == nil || endOfDay(day).After(*refreshed) {
			r.Unsettled[day] = true
		}
		if refreshed == nil {
			continue
		}
		if kept, found := r.Refreshed[day]; !found || refreshed.Before(kept) {
			r.Refreshed[day] = refreshed.UTC()
		}
	}
}

// addUsage drops a record no credential owns: a report row outside an API key
// or workspace, such as Console use, cannot join anything.
func (r *Report) addUsage(usage Usage) {
	if usage.Credential != "" {
		r.Usage = append(r.Usage, usage)
	}
}

func (r *Report) addCost(cost Cost) {
	if cost.Credential != "" {
		r.Costs = append(r.Costs, cost)
	}
}
