package server

import (
	"cmp"
	"fmt"
	"maps"
	"net/http"
	"regexp"
	"slices"
	"time"

	"github.com/Costroid/costroid/internal/catalog"
	"github.com/Costroid/costroid/internal/decimal"
	"github.com/Costroid/costroid/internal/fanout"
	"github.com/Costroid/costroid/internal/focus"
	"github.com/Costroid/costroid/internal/focusfile"
	"github.com/Costroid/costroid/internal/oidc"
	"github.com/Costroid/costroid/internal/store"
	"github.com/Costroid/costroid/internal/vcs"
)

const (
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
	// estimateHorizonYears is how far ahead of this server's clock an
	// estimate's charge period may start. An estimate states a period that has
	// not happened: an estimator states the month ahead, and a plan states at
	// most a year ahead.
	estimateHorizonYears = 1
)

// The tag convention's value patterns, version 0.2.
var (
	repoTag    = regexp.MustCompile(`^[a-z0-9.-]+(/[a-z0-9._~%-]+){1,8}$`)
	prTag      = regexp.MustCompile(`^[1-9][0-9]{0,19}$`)
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
	region      string
	credential  string
	account     string
	context     int64
	// contextUnstated means the row's SkuPriceId states no context tier, so
	// context is the highest any input could reach.
	contextUnstated bool
	class           focus.TokenClass
	tokens          int64
	start, end      time.Time

	session      string
	agent        string
	agentVersion string
	basis        focus.CostBasis
	enforcement  string
	overshoot    *decimal.Decimal
	source       string
	notes        []string
	// assumedTTL is costroid/assumed_ttl, empty on a stated lifetime.
	assumedTTL string
	// named holds the tags a caller names, by tag.
	named map[string]string
	// visibility is costroid/repo_visibility as the sender stated it, empty
	// when it stated none.
	visibility string

	// Join keys as the sender stated them. Empty means not stated.
	repo, pr, sha, run string
	// weakJoin is a sender's statement that its join is only windowed.
	weakJoin bool
	email    string
	// requestID is the provider's request id, empty when the sender gave none.
	requestID string
	// estimate is an estimate row's own facts, nil on an agent's row. The
	// ledger stores it as stated and never prices it.
	estimate *focusfile.Estimate
}

// joinResult is what the join decided for one row.
type joinResult struct {
	repo, pr, sha, run string
	// tokenSHA is the commit the token verifies. sha is the head, which is
	// the same commit only when headBasis is store.HeadFromToken.
	tokenSHA, headBasis string
	grade, identity     string
	attempt             string
	actor, event        string
	// credentialBasis is store.CredentialMinted when an attestation names the
	// row's credential as minted for this run.
	credentialBasis string
}

func joinKeyRefusal(key string) *refusal {
	return refuse(http.StatusForbidden, codeJoinKey, "The row's "+key+" disagrees with the CI token.")
}

// join applies the token to a charge. Keys come from the token. A stated key
// that disagrees rejects the batch, and a stated pull request the token cannot
// confirm is discarded, which leaves the row windowed. A row with no head to
// name stays windowed too, because the tag contract's exact row names one, and
// so does a row whose credential no attestation names as minted for this run,
// because a stated credential may be a key every run shares.
func (s *Server) join(c *charge, who caller, proven map[string]bool) (joinResult, error) {
	if who.identity == nil {
		result, err := s.joinUnverified(c, who)
		result.credentialBasis = basisOf(c, proven)
		return result, err
	}
	id := who.identity
	for _, pair := range []struct{ key, stated, token string }{
		{"repository", c.repo, id.Repository},
		{"run", c.run, id.RunID},
	} {
		if pair.stated != "" && pair.stated != pair.token {
			return joinResult{}, joinKeyRefusal(pair.key)
		}
	}
	if c.pr != "" && id.PullRequest != "" && c.pr != id.PullRequest {
		return joinResult{}, joinKeyRefusal("pull request")
	}
	head, basis, err := headOf(c, id)
	if err != nil {
		return joinResult{}, err
	}
	result := joinResult{
		repo: id.Repository, pr: id.PullRequest, sha: head, run: id.RunID,
		tokenSHA: id.CommitSHA, headBasis: basis,
		grade: gradeWindowed, identity: identityToken,
		attempt: id.RunAttempt, actor: id.Actor, event: id.Event,
		credentialBasis: basisOf(c, proven),
	}
	if id.PullRequest != "" && head != "" && result.credentialBasis == store.CredentialMinted && !c.weakJoin {
		result.grade = gradeExact
	}
	return result, nil
}

// basisOf says whether the row's credential was attested as minted for the
// run, or only stated. A session id stated as the credential is never minted.
func basisOf(c *charge, proven map[string]bool) string {
	switch {
	case c.credential == "":
		return ""
	case c.namesCredential() && proven[c.credential]:
		return store.CredentialMinted
	}
	return store.CredentialStated
}

// namesCredential reports whether a row states what a provider's report can be
// filtered by. A session id is a join for the row and never that: Anthropic's
// usage session_id is a Managed Agents id, not a Claude Code session.
func (c *charge) namesCredential() bool {
	return c.credential != "" && c.credential != c.session
}

// headOf decides a verified row's head. A token that proves the head is the
// authority over it. Any other token says nothing about the head, so a stated
// head that differs from the token's commit is not a disagreement.
func headOf(c *charge, id *oidc.Identity) (string, string, error) {
	if !id.CommitIsHead {
		return statedHead(c.sha)
	}
	if c.sha != "" && c.sha != id.CommitSHA {
		return "", "", joinKeyRefusal("commit")
	}
	return id.CommitSHA, store.HeadFromToken, nil
}

// statedHead keeps a sender's head as a claim. Every such row carries
// store.HeadStated, and that is the seam for a later SCM check: it reads those
// rows and reports a head the pull request never had as its own issue. The
// join cannot tell, because no token names a pull request's head.
func statedHead(sha string) (string, string, error) {
	if sha == "" {
		return "", "", nil
	}
	if !vcs.IsCommit(sha) {
		return "", "", joinKeyRefusal("commit")
	}
	return sha, store.HeadStated, nil
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
	if vcs.IsCommit(c.sha) {
		result.sha, result.headBasis = c.sha, store.HeadStated
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
	priced := focus.Charge{
		RowID:               c.rowID,
		Provider:            c.provider,
		ServiceProviderName: provider,
		Model:               c.model,
		Class:               c.class,
		Tokens:              c.tokens,
		Start:               c.start,
		End:                 c.end,
		Account:             c.account,
		Credential:          c.credential,
		Region:              c.region,
		ServiceTier:         c.serviceTier,
		CatalogVersion:      s.Catalog.Version(),
		Basis:               c.basis,
		Tags:                tagsOf(c, joined),
		Notes:               slices.Clone(c.notes),
		OvershootBound:      overshootOf(c, joined),
	}
	if c.class.Informational() {
		priced.Notes = append(priced.Notes, focus.NoteInformational)
	} else if err := s.applyPrice(&priced, c); err != nil {
		return store.Record{}, err
	}
	row := focus.NewRow(priced)
	if err := row.Check(); err != nil {
		return store.Record{}, fmt.Errorf("server: priced row: %w", err)
	}
	return store.Record{
		Row: row, RunAttempt: joined.attempt, Actor: joined.actor, Event: joined.event,
		TokenCommitSHA: joined.tokenSHA, HeadBasis: joined.headBasis, CredentialBasis: joined.credentialBasis,
		RequestID: c.requestID,
	}, nil
}

func (s *Server) applyPrice(priced *focus.Charge, c *charge) error {
	start, _ := focus.ChargePeriod(c.start, c.end)
	price, err := s.Catalog.Lookup(catalog.Query{
		Provider:           c.provider,
		Model:              c.model,
		Class:              c.class,
		ServiceTier:        c.serviceTier,
		Region:             cmp.Or(c.region, catalog.RegionGlobal),
		ContextInputTokens: c.context,
		At:                 start,
	})
	if err != nil {
		return refuse(http.StatusUnprocessableEntity, codeUnknownPrice, "The catalog has no price or fallback for this row.")
	}
	priced.Price = &focus.Price{SkuPriceID: price.SkuPriceID, PricingUnit: price.PricingUnit, ListUnitPrice: price.ListUnitPrice}
	if price.Fallback {
		priced.Notes = append(priced.Notes, focus.NoteFallback)
	}
	if price.ResidencyMultiplier != nil {
		priced.Notes = append(priced.Notes, focus.ResidencyNote(*price.ResidencyMultiplier, c.region))
	}
	if c.contextUnstated && price.ContextTierMin != nil {
		priced.Notes = append(priced.Notes, fmt.Sprintf(noteNoContextTier, *price.ContextTierMin))
	}
	return nil
}

// noteNoContextTier says a row whose SkuPriceId states no context tier took
// the highest one its model has.
const noteNoContextTier = "its SkuPriceId states no context tier, so the prices above %d input tokens, the highest tier, apply, which may read high"

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
		store.TagRepo:           joined.repo,
		store.TagPR:             joined.pr,
		store.TagCommitSHA:      joined.sha,
		store.TagWorkflowRunID:  joined.run,
		focus.TagSessionID:      c.session,
		focus.TagAgentVersion:   c.agentVersion,
		focus.TagAssumedTTL:     c.assumedTTL,
		focus.TagRepoVisibility: c.visibility,
	} {
		if value != "" {
			tags[key] = value
		}
	}
	maps.Copy(tags, c.named)
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

// checkStart refuses a charge that starts after this server's clock. A row
// that measures, bills or prices a run reports what has happened, so it may
// start no later than futureSkew ahead. An estimate states a period that has
// not happened, so it may start up to estimateHorizonYears ahead.
func checkStart(c *charge, now time.Time) error {
	if c.estimate == nil {
		if c.start.After(now.Add(futureSkew)) {
			return refuse(http.StatusUnprocessableEntity, codeInvalidRow, "A row's charge period starts in the future.")
		}
		return nil
	}
	if c.start.After(now.AddDate(estimateHorizonYears, 0, 0).Add(futureSkew)) {
		return refuse(http.StatusUnprocessableEntity, codeInvalidRow, "An estimate's charge period starts more than a year ahead, and an estimate may start at most a year ahead.")
	}
	return nil
}

// write checks every charge's join, prices it and stores the batch. One bad
// row fails the whole request, so a retry is whole too.
func (s *Server) write(r *http.Request, who caller, charges []charge, drops map[string]int64) (store.Receipt, error) {
	now := s.Now()
	proven, err := s.provenCredentials(r.Context(), who, charges)
	if err != nil {
		return store.Receipt{}, err
	}
	records := make([]store.Record, len(charges))
	err = fanout.Each(len(charges), func(_, i int) error {
		c := &charges[i]
		if err := checkStart(c, now); err != nil {
			return err
		}
		joined, err := s.join(c, who, proven)
		if err != nil {
			return err
		}
		if c.estimate != nil {
			records[i] = estimateRecord(c, joined)
			return nil
		}
		records[i], err = s.price(c, joined)
		return err
	})
	if err != nil {
		return store.Receipt{}, err
	}
	return s.Ledger.Ingest(r.Context(), who.tenant, store.Batch{Records: records, Drops: drops, Day: now})
}
