package reconcile

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/Costroid/costroid/internal/catalog"
	"github.com/Costroid/costroid/internal/decimal"
	"github.com/Costroid/costroid/internal/focus"
)

// The organization usage and costs endpoints. Both take an Admin API key.
// https://developers.openai.com/api/reference/resources/admin/subresources/organization/subresources/usage
// retrieved 2026-11-01.
const (
	openAIUsage = "https://api.openai.com/v1/organization/usage/completions"
	openAICosts = "https://api.openai.com/v1/organization/costs"

	openAIBuckets  = 31
	openAIKeyGrain = "api_key_id"
	openAIScope    = "project_id"
	openAIScopeID  = "proj_"
	openAIBatch    = "batch"
	openAILineItem = "line_item"

	// openAILag is SPEC.md C's outer bound on a provider's lateness. The
	// reports stamp no freshness, so a day younger than this is pending.
	openAILag = 24 * time.Hour
)

// standardTiers are the report's words for the default tier.
var standardTiers = []string{"", "default", "auto", catalog.TierStandard}

// openAILineKinds maps the part of a line item after the model to a class.
// The costs page shows one line item, "gpt-6-astra, input_tokens"
// (https://developers.openai.com/api/reference/resources/admin/subresources/organization/subresources/usage/methods/costs
// retrieved 2026-11-07); the other
// kinds are the usage report's own field names, and are not yet seen on a
// line item. input_tokens is read as uncached input, the class billed at the
// input price. A kind not named here proves no price, and its amount stays in
// the day's total.
var openAILineKinds = map[string]focus.TokenClass{
	"input_tokens":                 focus.InputUncached,
	"input_cached_tokens":          focus.CacheRead,
	"input_cache_write_30m_tokens": focus.CacheWrite30m,
	"input_cache_write_12h_tokens": focus.CacheWrite12h,
	"output_tokens":                focus.Output,
}

type openAI struct {
	client Doer
	key    string
}

// NewOpenAI returns the OpenAI fetcher. The key is an Admin API key: no read
// scope exists for these endpoints.
func NewOpenAI(client Doer, key string) Provider {
	return openAI{client: client, key: key}
}

func (o openAI) Name() string { return SourceOpenAI }

func (o openAI) authorize(header http.Header) {
	header.Set("Authorization", "Bearer "+o.key)
}

func (o openAI) Fetch(ctx context.Context, days Range, credentials []string) (Report, error) {
	report := Report{Source: SourceOpenAI, Unsettled: map[string]bool{}, Lag: openAILag}
	span := window{days.From, days.To.AddDate(0, 0, 1)}
	for _, grain := range grains(credentials, openAIScopeID, openAIKeyGrain, openAIScope) {
		if err := o.usage(ctx, grain, span, &report); err != nil {
			return Report{}, err
		}
		if err := o.costs(ctx, grain, span, &report); err != nil {
			return Report{}, err
		}
	}
	return report, nil
}

func openAIQuery(span window, groupBy ...string) url.Values {
	return url.Values{
		"start_time":   {strconv.FormatInt(span.start.Unix(), 10)},
		"end_time":     {strconv.FormatInt(span.end.Unix(), 10)},
		"bucket_width": {"1d"},
		"group_by":     groupBy,
		"limit":        {strconv.Itoa(openAIBuckets)},
	}
}

type openAIBucket[T any] struct {
	StartTime int64 `json:"start_time"`
	Results   []T   `json:"results"`
}

func (b openAIBucket[T]) day() string {
	return time.Unix(b.StartTime, 0).UTC().Format(dayLayout)
}

type openAIUsageResult struct {
	APIKeyID            *string `json:"api_key_id"`
	ProjectID           *string `json:"project_id"`
	Model               *string `json:"model"`
	Batch               *bool   `json:"batch"`
	ServiceTier         *string `json:"service_tier"`
	InputTokens         int64   `json:"input_tokens"`
	InputCachedTokens   int64   `json:"input_cached_tokens"`
	InputUncachedTokens *int64  `json:"input_uncached_tokens"`
	CacheWriteTokens    int64   `json:"input_cache_write_tokens"`
	CacheWrite30mTokens *int64  `json:"input_cache_write_30m_tokens"`
	CacheWrite12hTokens int64   `json:"input_cache_write_12h_tokens"`
	OutputTokens        int64   `json:"output_tokens"`
}

func (o openAI) usage(ctx context.Context, grain string, span window, report *Report) error {
	endpoint := fetch{client: o.client, endpoint: openAIUsage, authorize: o.authorize}
	query := openAIQuery(span, grain, "model", openAIBatch, "service_tier")
	return pages(ctx, endpoint, query, func(current page[openAIBucket[openAIUsageResult]]) error {
		for _, bucket := range current.Data {
			for _, result := range bucket.Results {
				usage, err := result.usage(grain, bucket.day())
				if err != nil {
					return err
				}
				report.addUsage(usage)
			}
		}
		return nil
	})
}

// usage maps the report's overlapping fields to disjoint classes.
// input_tokens holds cached and cache-write tokens. The two finer fields can
// be absent on older responses, so each falls back to the difference. A
// class below zero means the fields do not overlap as read, and every figure
// compared from them would be wrong, so the report is refused.
func (r openAIUsageResult) usage(grain, day string) (Usage, error) {
	uncached := r.InputTokens - r.InputCachedTokens - r.CacheWriteTokens
	if r.InputUncachedTokens != nil {
		uncached = *r.InputUncachedTokens
	}
	write30m := r.CacheWriteTokens - r.CacheWrite12hTokens
	if r.CacheWrite30mTokens != nil {
		write30m = *r.CacheWrite30mTokens
	}
	counts := map[focus.TokenClass]int64{
		focus.InputUncached: uncached,
		focus.CacheRead:     r.InputCachedTokens,
		focus.CacheWrite30m: write30m,
		focus.CacheWrite12h: r.CacheWrite12hTokens,
		focus.Output:        r.OutputTokens,
	}
	tokens := make(map[focus.TokenClass]decimal.Decimal, len(counts))
	for _, class := range slices.Sorted(maps.Keys(counts)) {
		if counts[class] < 0 {
			return Usage{}, fmt.Errorf("usage on %s for %s: %s reads %d tokens, below zero", day, text(r.Model), class, counts[class])
		}
		tokens[class] = decimal.FromInt(counts[class])
	}
	return Usage{
		Credential: openAICredential(grain, r.APIKeyID, r.ProjectID),
		Day:        day,
		Model:      text(r.Model),
		Tier:       r.tier(),
		Region:     catalog.RegionGlobal,
		Tokens:     tokens,
	}, nil
}

func (r openAIUsageResult) tier() string {
	if r.Batch != nil && *r.Batch {
		return openAIBatch
	}
	tier := strings.ToLower(text(r.ServiceTier))
	for _, standard := range standardTiers {
		if tier == standard {
			return catalog.TierStandard
		}
	}
	return tier
}

func openAICredential(grain string, key, project *string) string {
	if grain == openAIScope {
		return text(project)
	}
	return text(key)
}

// openAICostResult holds the amount as json.Number because the report sends a
// JSON number, and a float would move its digits.
type openAICostResult struct {
	APIKeyID  *string `json:"api_key_id"`
	ProjectID *string `json:"project_id"`
	LineItem  *string `json:"line_item"`
	Amount    struct {
		Value    json.Number `json:"value"`
		Currency string      `json:"currency"`
	} `json:"amount"`
}

func (o openAI) costs(ctx context.Context, grain string, span window, report *Report) error {
	endpoint := fetch{client: o.client, endpoint: openAICosts, authorize: o.authorize}
	return pages(ctx, endpoint, openAIQuery(span, openAILineItem, grain), func(current page[openAIBucket[openAICostResult]]) error {
		for _, bucket := range current.Data {
			for _, result := range bucket.Results {
				cost, err := result.cost(grain, bucket.day())
				if err != nil {
					return err
				}
				report.addCost(cost)
			}
		}
		return nil
	})
}

// cost reads every line as a token line. The report's line_item is a free
// string, so a tool's charge stays in the amount and reads as residual. The
// report states one amount, so the price proof reads it as the list figure,
// and the line item names its model and class.
func (r openAICostResult) cost(grain, day string) (Cost, error) {
	if !strings.EqualFold(r.Amount.Currency, currency) {
		return Cost{}, fmt.Errorf("currency %q: reports are read in USD", r.Amount.Currency)
	}
	figure, err := stated(r.Amount.Value.String(), 0)
	if err != nil {
		return Cost{}, fmt.Errorf("amount: %w", err)
	}
	cost := Cost{
		Credential: openAICredential(grain, r.APIKeyID, r.ProjectID),
		Day:        day,
		Amount:     figure.Value,
		Tokens:     true,
		ListFigure: &figure,
	}
	if r.LineItem != nil {
		cost.Label = *r.LineItem
		if index := strings.LastIndex(cost.Label, ", "); index >= 0 {
			cost.Model = cost.Label[:index]
			cost.Class = openAILineKinds[cost.Label[index+len(", "):]]
		}
	}
	return cost, nil
}
