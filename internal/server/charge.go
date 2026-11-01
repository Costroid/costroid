package server

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/Costroid/costroid/internal/catalog"
	"github.com/Costroid/costroid/internal/decimal"
	"github.com/Costroid/costroid/internal/focus"
	"github.com/Costroid/costroid/internal/store"
)

const (
	currency          = "USD"
	unknownAccount    = "unknown"
	unitTokens        = "Tokens"
	unitInformational = "Informational Tokens"
	tokensPerMillion  = 6
	instantLayout     = "2006-01-02T15:04:05Z"

	gradeExact      = "exact"
	gradeWindowed   = "windowed"
	gradeNone       = "unattributed"
	identityToken   = "token_verified"
	identityNone    = "unverified"
	sourceStream    = "agent_stream"
	sourceTelemetry = "otlp"

	sourceProviderReport = "provider_report"

	enforcementNone    = "none"
	enforcementGateway = "gateway-precheck"

	// futureSkew is how far ahead of this server's clock a charge may start.
	futureSkew = 5 * time.Minute
)

// The tag convention's value patterns, version 0.1.
var (
	repoTag    = regexp.MustCompile(`^[a-z0-9.-]+(/[a-z0-9._~%-]+){1,8}$`)
	prTag      = regexp.MustCompile(`^[1-9][0-9]{0,19}$`)
	shaTag     = regexp.MustCompile(`^[0-9a-f]{40}([0-9a-f]{24})?$`)
	runTag     = regexp.MustCompile(`^[A-Za-z0-9._:-]{1,128}$`)
	sessionTag = regexp.MustCompile(`^[A-Za-z0-9._:-]{1,256}$`)
	agentTag   = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)
	versionTag = regexp.MustCompile(`^[A-Za-z0-9._+-]{1,64}$`)
	modelTag   = regexp.MustCompile(`^[A-Za-z0-9._:/@-]{1,128}$`)
)

var enforcementGrades = map[string]bool{
	enforcementGateway: true, "agent-flag": true, "wrapper-watchdog": true, "provider": true, enforcementNone: true,
}

// classes lists the token classes a row may carry.
var classes = map[focus.TokenClass]bool{
	focus.InputUncached: true, focus.CacheRead: true, focus.CacheWrite5m: true, focus.CacheWrite1h: true,
	focus.CacheWrite30m: true, focus.CacheWrite12h: true, focus.Output: true, focus.ReasoningOutput: true,
	focus.CachedInputReported: true,
}

// charge is one row's raw facts, as either ingest path read them. It holds no
// cost and no price: those exist only on the row this package builds.
type charge struct {
	rowID       string
	provider    string
	model       string
	serviceTier string
	context     int64
	class       focus.TokenClass
	tokens      int64
	start, end  time.Time

	session      string
	agent        string
	agentVersion string
	basis        focus.CostBasis
	enforcement  string
	overshoot    *decimal.Decimal
	source       string
	notes        []string

	// Join keys as the sender stated them. Empty means not stated.
	repo, pr, sha, run string
	// weakJoin is a sender's statement that its join is only windowed.
	weakJoin bool
	email    string
}

// joinResult is what the join decided for one row.
type joinResult struct {
	repo, pr, sha, run string
	grade, identity    string
	attempt            string
	actor, event       string
}

func joinKeyRefusal(key string) *refusal {
	return refuse(http.StatusForbidden, codeJoinKey, "The row's "+key+" disagrees with the CI token.")
}

// join applies the token to a charge. Keys come from the token. A stated key
// that disagrees rejects the batch, and a stated pull request the token cannot
// confirm is discarded, which leaves the row windowed.
func (s *Server) join(c *charge, who caller) (joinResult, error) {
	if who.identity == nil {
		return s.joinUnverified(c, who)
	}
	id := who.identity
	for _, pair := range []struct{ key, stated, token string }{
		{"repository", c.repo, id.Repository},
		{"commit", c.sha, id.CommitSHA},
		{"run", c.run, id.RunID},
	} {
		if pair.stated != "" && pair.stated != pair.token {
			return joinResult{}, joinKeyRefusal(pair.key)
		}
	}
	if c.pr != "" && id.PullRequest != "" && c.pr != id.PullRequest {
		return joinResult{}, joinKeyRefusal("pull request")
	}
	result := joinResult{
		repo: id.Repository, pr: id.PullRequest, sha: id.CommitSHA, run: id.RunID,
		grade: gradeWindowed, identity: identityToken,
		attempt: id.RunAttempt, actor: id.Actor, event: id.Event,
	}
	if id.PullRequest != "" && c.session != "" && !c.weakJoin {
		result.grade = gradeExact
	}
	return result, nil
}

// joinUnverified keeps a sender's stated keys only as claims. The row is
// unattributed, so nothing downstream can treat them as a join.
func (s *Server) joinUnverified(c *charge, who caller) (joinResult, error) {
	result := joinResult{grade: gradeNone, identity: identityNone}
	if repoTag.MatchString(c.repo) {
		result.repo = c.repo
	}
	if prTag.MatchString(c.pr) {
		result.pr = c.pr
	}
	if shaTag.MatchString(c.sha) {
		result.sha = c.sha
	}
	if runTag.MatchString(c.run) {
		result.run = c.run
	}
	if c.email != "" {
		actor, err := s.pseudonym(who.tenant, c.email)
		if err != nil {
			return joinResult{}, err
		}
		result.actor = actor
	}
	return result, nil
}

// price builds the row. Every cost column comes from the token count and the
// pinned catalog.
func (s *Server) price(c *charge, joined joinResult) (store.Record, error) {
	provider, err := s.Catalog.ServiceProviderName(c.provider)
	if err != nil {
		return store.Record{}, refuse(http.StatusUnprocessableEntity, codeUnknownPrice, "The catalog has no sheet for this row's provider.")
	}
	start, end := chargePeriod(c.start, c.end)
	periodStart, periodEnd := billingPeriod(start)
	version := s.Catalog.Version()
	sku := fmt.Sprintf("%s/%s/%s", c.provider, c.model, c.class)
	row := focus.Row{
		BillingAccountID:    unknownAccount,
		BillingCurrency:     currency,
		BillingPeriodEnd:    periodEnd.Format(instantLayout),
		BillingPeriodStart:  periodStart.Format(instantLayout),
		ChargeCategory:      "Usage",
		ChargeFrequency:     "Usage-Based",
		ChargePeriodEnd:     end.Format(instantLayout),
		ChargePeriodStart:   start.Format(instantLayout),
		ConsumedQuantity:    decimal.FromInt(c.tokens),
		HostProviderName:    &provider,
		InvoiceIssuerName:   provider,
		PricingCategory:     "Standard",
		ServiceCategory:     "AI and Machine Learning",
		ServiceName:         provider + " API",
		ServiceProviderName: provider,
		ServiceSubcategory:  "Generative AI",
		SkuID:               sku,
		SkuMeter:            c.class,
		Tags:                tagsOf(c, joined),
		CatalogVersion:      &version,
		OvershootBound:      overshootOf(c, joined),
		RowID:               c.rowID,
	}
	notes := append([]string{}, c.notes...)
	if c.class.Informational() {
		row.ConsumedUnit = unitInformational
		row.PricingUnit = unitInformational
		row.PricingQuantity = row.ConsumedQuantity
		row.SkuPriceID = sku + "/informational"
		notes = append(notes, "informational")
	} else {
		fallback, err := s.applyPrice(&row, c, start)
		if err != nil {
			return store.Record{}, err
		}
		if fallback {
			notes = append(notes, "priced at the catalog fallback tier: the sheet does not name this model, tier and class")
		}
	}
	row.ChargeDescription = strings.Join(append([]string{fmt.Sprintf("%s %s tokens", c.model, c.class)}, notes...), "; ")
	if err := row.Check(); err != nil {
		return store.Record{}, fmt.Errorf("server: priced row: %w", err)
	}
	return store.Record{Row: row, RunAttempt: joined.attempt, Actor: joined.actor, Event: joined.event}, nil
}

func (s *Server) applyPrice(row *focus.Row, c *charge, at time.Time) (bool, error) {
	price, err := s.Catalog.Lookup(catalog.Query{
		Provider:           c.provider,
		Model:              c.model,
		Class:              c.class,
		ServiceTier:        c.serviceTier,
		Region:             catalog.RegionGlobal,
		ContextInputTokens: c.context,
		At:                 at,
	})
	if err != nil {
		return false, refuse(http.StatusUnprocessableEntity, codeUnknownPrice, "The catalog has no price or fallback for this row.")
	}
	quantity := decimal.FromInt(c.tokens).DivPow10(tokensPerMillion)
	cost := price.ListUnitPrice.Mul(quantity)
	row.ConsumedUnit = unitTokens
	row.PricingUnit = price.PricingUnit
	row.PricingQuantity = quantity
	row.SkuPriceID = price.SkuPriceID
	row.ListUnitPrice = price.ListUnitPrice
	row.ListCost = cost
	row.ContractedUnitPrice = price.ListUnitPrice
	row.ContractedCost = cost
	if c.basis == focus.Measured {
		row.EffectiveCost = cost
	}
	return price.Fallback, nil
}

func tagsOf(c *charge, joined joinResult) map[string]string {
	tags := map[string]string{
		focus.TagAgent:            c.agent,
		focus.TagModel:            c.model,
		focus.TagCostSource:       c.source,
		focus.TagCostBasis:        string(c.basis),
		focus.TagAttributionGrade: joined.grade,
		focus.TagEnforcementGrade: enforcementOf(c, joined),
		focus.TagRunIdentity:      joined.identity,
	}
	for key, value := range map[string]string{
		store.TagRepo:          joined.repo,
		store.TagPR:            joined.pr,
		store.TagCommitSHA:     joined.sha,
		store.TagWorkflowRunID: joined.run,
		focus.TagSessionID:     c.session,
		focus.TagAgentVersion:  c.agentVersion,
	} {
		if value != "" {
			tags[key] = value
		}
	}
	return tags
}

// enforcementOf never shows a grade stronger than the ledger can stand behind:
// an unverified sender's claim reads none, and a gateway grade needs its bound.
func enforcementOf(c *charge, joined joinResult) string {
	if joined.identity != identityToken || !enforcementGrades[c.enforcement] {
		return enforcementNone
	}
	if c.enforcement == enforcementGateway && c.overshoot == nil {
		return enforcementNone
	}
	return c.enforcement
}

func overshootOf(c *charge, joined joinResult) *decimal.Decimal {
	if enforcementOf(c, joined) == enforcementGateway {
		return c.overshoot
	}
	return nil
}

// rowIdentity is the row contract's id: SHA-256 of the provider's request id
// and the token class. Both paths arrive at the same id for the same request.
func rowIdentity(requestID string, class focus.TokenClass) string {
	sum := sha256.Sum256([]byte(requestID + ":" + string(class)))
	return hex.EncodeToString(sum[:])
}

// chargePeriod widens a span to whole UTC seconds, end exclusive.
func chargePeriod(start, end time.Time) (time.Time, time.Time) {
	first := start.UTC().Truncate(time.Second)
	last := end.UTC().Truncate(time.Second)
	if last.Before(end.UTC()) || !last.After(first) {
		last = last.Add(time.Second)
	}
	return first, last
}

func billingPeriod(at time.Time) (time.Time, time.Time) {
	first := time.Date(at.Year(), at.Month(), 1, 0, 0, 0, 0, time.UTC)
	return first, first.AddDate(0, 1, 0)
}

// write checks every charge's join, prices it and stores the batch. One bad
// row fails the whole request, so a retry is whole too.
func (s *Server) write(r *http.Request, who caller, charges []charge, drops map[string]int64) (store.Receipt, error) {
	now := s.Now()
	records := make([]store.Record, 0, len(charges))
	for i := range charges {
		c := &charges[i]
		if c.start.After(now.Add(futureSkew)) {
			return store.Receipt{}, refuse(http.StatusUnprocessableEntity, codeInvalidRow, "A row's charge period starts in the future.")
		}
		joined, err := s.join(c, who)
		if err != nil {
			return store.Receipt{}, err
		}
		record, err := s.price(c, joined)
		if err != nil {
			return store.Receipt{}, err
		}
		records = append(records, record)
	}
	return s.Ledger.Ingest(r.Context(), who.tenant, store.Batch{Records: records, Drops: drops, Day: now})
}
