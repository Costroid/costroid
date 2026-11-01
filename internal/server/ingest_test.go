package server

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Costroid/costroid/internal/decimal"
	"github.com/Costroid/costroid/internal/focus"
	"github.com/Costroid/costroid/internal/oidc"
	"github.com/Costroid/costroid/internal/otlp"
	"github.com/Costroid/costroid/internal/store"
)

const (
	tenantA = "aaaaaaaa-0000-4000-8000-000000000001"
	tenantB = "bbbbbbbb-0000-4000-8000-000000000002"
)

func memoryWorld(t *testing.T) (*world, *memoryLedger) {
	t.Helper()
	ledger := newMemoryLedger()
	a, b := tenantID(t, tenantA), tenantID(t, tenantB)
	ledger.owners["github.com:acme"] = a
	ledger.owners["github.com:beta"] = b
	ledger.keys[store.KeyDigest(acmeKey)] = a
	ledger.keys[store.KeyDigest(betaKey)] = b
	return newWorld(t, ledger), ledger
}

func (m *memoryLedger) stored(t *testing.T, tenant string) []store.Record {
	t.Helper()
	var records []store.Record
	for _, record := range m.rows[tenantID(t, tenant)] {
		records = append(records, record)
	}
	return records
}

func (m *memoryLedger) only(t *testing.T, tenant string) store.Record {
	t.Helper()
	records := m.stored(t, tenant)
	if len(records) != 1 {
		t.Fatalf("%d rows stored", len(records))
	}
	return records[0]
}

func total(records []store.Record) decimal.Decimal {
	var sum decimal.Decimal
	for _, record := range records {
		sum = sum.Add(record.Row.ListCost)
	}
	return sum
}

func TestIdentityComesFromTheTokenNeverThePayload(t *testing.T) {
	w, ledger := memoryWorld(t)
	w.rows(w.token(), lines(t, row("req-1", focus.Output, 100))).want(t, http.StatusOK)
	record := ledger.only(t, tenantA)
	got := record.Row.Tags
	for key, want := range map[string]string{
		store.TagRepo: acmeRepo, store.TagPR: acmePull, store.TagWorkflowRunID: acmeRun,
		focus.TagRunIdentity: "token_verified", focus.TagAttributionGrade: "exact",
	} {
		if got[key] != want {
			t.Errorf("%s = %q, want %q", key, got[key], want)
		}
	}
	if record.Actor != "octo-dev" || record.Event != "pull_request" || record.RunAttempt != "1" || record.TokenCommitSHA != acmeSHA {
		t.Errorf("%+v", record)
	}
}

func TestRowsWhoseJoinKeysDisagreeWithTheTokenAreRejected(t *testing.T) {
	cases := map[string]struct{ tag, value string }{
		"repository":   {store.TagRepo, "github.com/acme/other"},
		"owner":        {store.TagRepo, "github.com/beta/widgets"},
		"run":          {store.TagWorkflowRunID, "1"},
		"pull request": {store.TagPR, "999"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			w, ledger := memoryWorld(t)
			honest := row("req-1", focus.Output, 100)
			forged := row("req-2", focus.Output, 100)
			tags(forged)[tc.tag] = tc.value
			got := w.rows(w.token(), lines(t, honest, forged)).want(t, http.StatusForbidden)
			if got.code(t) != codeJoinKey {
				t.Fatalf("code %s", got.code(t))
			}
			if rows := ledger.stored(t, tenantA); len(rows) != 0 {
				t.Fatalf("%d rows stored from a rejected batch", len(rows))
			}
		})
	}
}

// The fixtures' pull_request token states acmeSHA, a merge commit. Its pull
// request's head, acmeHead, is a different commit.
func TestAHeadThatDiffersFromTheMergeCommitIsAcceptedAsAClaim(t *testing.T) {
	w, ledger := memoryWorld(t)
	w.rows(w.token(), lines(t, row("req-1", focus.Output, 100))).want(t, http.StatusOK)
	record := ledger.only(t, tenantA)
	if got := record.Row.Tags[store.TagCommitSHA]; got != acmeHead {
		t.Errorf("commit_sha %q, want the stated head", got)
	}
	if record.TokenCommitSHA != acmeSHA || record.HeadBasis != store.HeadStated {
		t.Errorf("token commit %q, basis %q", record.TokenCommitSHA, record.HeadBasis)
	}
	if got := record.Row.Tags[focus.TagAttributionGrade]; got != "exact" {
		t.Errorf("grade %s", got)
	}
}

func TestAPullRequestRunStatingNoHeadNeverWritesTheMergeCommitAsOne(t *testing.T) {
	w, ledger := memoryWorld(t)
	r := row("req-1", focus.Output, 100)
	delete(tags(r), store.TagCommitSHA)
	w.rows(w.token(), lines(t, r)).want(t, http.StatusOK)
	record := ledger.only(t, tenantA)
	if got, present := record.Row.Tags[store.TagCommitSHA]; present {
		t.Errorf("commit_sha %q on a row that stated no head", got)
	}
	if record.TokenCommitSHA != acmeSHA || record.HeadBasis != "" {
		t.Errorf("token commit %q, basis %q", record.TokenCommitSHA, record.HeadBasis)
	}
	if got := record.Row.Tags[focus.TagAttributionGrade]; got != "windowed" {
		t.Errorf("grade %s, and an exact row must name a head", got)
	}
}

func TestAHeadThatIsNotACommitIsRefused(t *testing.T) {
	w, ledger := memoryWorld(t)
	r := row("req-1", focus.Output, 100)
	tags(r)[store.TagCommitSHA] = "not-a-commit"
	got := w.rows(w.token(), lines(t, r)).want(t, http.StatusForbidden)
	if got.code(t) != codeJoinKey {
		t.Fatalf("code %s", got.code(t))
	}
	if len(ledger.stored(t, tenantA)) != 0 {
		t.Fatal("rows stored")
	}
}

func pushToken(w *world) string {
	claims := w.claims()
	claims["event_name"], claims["ref"] = "push", "refs/heads/main"
	return w.issuer.Token(claims)
}

func TestAPushTokenIsTheAuthorityOverTheHead(t *testing.T) {
	w, ledger := memoryWorld(t)
	got := w.rows(pushToken(w), lines(t, row("req-1", focus.Output, 100))).want(t, http.StatusForbidden)
	if got.code(t) != codeJoinKey {
		t.Fatalf("code %s", got.code(t))
	}
	if len(ledger.stored(t, tenantA)) != 0 {
		t.Fatal("rows stored from a rejected batch")
	}
}

func TestAPushTokenWritesItsCommitAsTheHead(t *testing.T) {
	for name, stated := range map[string]string{"stated": acmeSHA, "not stated": ""} {
		t.Run(name, func(t *testing.T) {
			w, ledger := memoryWorld(t)
			r := row("req-1", focus.Output, 100)
			tags(r)[store.TagCommitSHA] = stated
			if stated == "" {
				delete(tags(r), store.TagCommitSHA)
			}
			w.rows(pushToken(w), lines(t, r)).want(t, http.StatusOK)
			record := ledger.only(t, tenantA)
			if record.Row.Tags[store.TagCommitSHA] != acmeSHA || record.TokenCommitSHA != acmeSHA || record.HeadBasis != store.HeadFromToken {
				t.Errorf("%+v", record)
			}
		})
	}
}

func TestAHeadStatedUnderAnIngestKeyIsAClaimWithNoTokenCommit(t *testing.T) {
	w, ledger := memoryWorld(t)
	w.rows(acmeKey, lines(t, row("req-1", focus.Output, 100))).want(t, http.StatusOK)
	record := ledger.only(t, tenantA)
	if record.Row.Tags[store.TagCommitSHA] != acmeHead || record.HeadBasis != store.HeadStated || record.TokenCommitSHA != "" {
		t.Errorf("%+v", record)
	}
}

func TestMatchingJoinKeysPassInAnyCase(t *testing.T) {
	w, ledger := memoryWorld(t)
	r := row("req-1", focus.Output, 100)
	tags(r)[store.TagRepo] = "github.com/Acme/Widgets"
	tags(r)[store.TagPR] = acmePull
	tags(r)[store.TagCommitSHA] = acmeSHA
	tags(r)[store.TagWorkflowRunID] = acmeRun
	w.rows(w.token(), lines(t, r)).want(t, http.StatusOK)
	if got := ledger.only(t, tenantA).Row.Tags[store.TagRepo]; got != acmeRepo {
		t.Fatalf("repo %q", got)
	}
}

func TestTokenValidationAtTheEdge(t *testing.T) {
	cases := map[string]func(map[string]any){
		"wrong audience": func(c map[string]any) { c["aud"] = "https://elsewhere.test" },
		"expired":        func(c map[string]any) { c["exp"] = testNow.Add(-time.Hour).Unix() },
		"unknown issuer": func(c map[string]any) { c["iss"] = "https://evil.test" },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			w, ledger := memoryWorld(t)
			claims := w.claims()
			mutate(claims)
			got := w.rows(w.issuer.Token(claims), lines(t, row("req-1", focus.Output, 100))).want(t, http.StatusUnauthorized)
			if got.code(t) != codeAuth {
				t.Fatalf("code %s", got.code(t))
			}
			if len(ledger.rows) != 0 {
				t.Fatal("a refused token stored rows")
			}
		})
	}
}

func TestEveryBadTokenGetsOneAnswer(t *testing.T) {
	w, _ := memoryWorld(t)
	body := lines(t, row("req-1", focus.Output, 100))
	expired := w.claims()
	expired["exp"] = testNow.Add(-time.Hour).Unix()
	wrongAudience := w.claims()
	wrongAudience["aud"] = "x"
	first := w.rows("", body).want(t, http.StatusUnauthorized)
	for _, bearer := range []string{"garbage", "costroid_ik_unknown", w.issuer.Token(expired), w.issuer.Token(wrongAudience)} {
		got := w.rows(bearer, body).want(t, http.StatusUnauthorized)
		if !bytes.Equal(got.body, first.body) {
			t.Fatalf("%s differs from %s", got.body, first.body)
		}
	}
}

func TestATokenForAnotherRepositoryCannotWriteThisOne(t *testing.T) {
	w, ledger := memoryWorld(t)
	r := row("req-1", focus.Output, 100)
	tags(r)[store.TagRepo] = acmeRepo
	w.rows(w.tokenFor("beta", "widgets"), lines(t, r)).want(t, http.StatusForbidden)
	if len(ledger.rows) != 0 {
		t.Fatal("rows stored")
	}
}

func TestAnOwnerNoTenantHoldsIsRefused(t *testing.T) {
	w, _ := memoryWorld(t)
	got := w.rows(w.tokenFor("stranger", "widgets"), lines(t, row("req-1", focus.Output, 100))).want(t, http.StatusForbidden)
	if got.code(t) != codeNoTenant {
		t.Fatalf("code %s", got.code(t))
	}
}

// forge rewrites every column a sender could use to state a price.
func forge(r map[string]any) map[string]any {
	forged := clone(r)
	for _, column := range []string{"ListCost", "ContractedCost", "EffectiveCost", "ListUnitPrice", "ContractedUnitPrice", "BilledCost"} {
		forged[column] = "0.000000000001"
	}
	forged["ChargeDescription"] = "free of charge"
	forged["PricingQuantity"] = "0"
	forged["PricingUnit"] = "Nothing"
	forged["PricingCategory"] = "Committed"
	forged["BillingCurrency"] = "TRY"
	forged["x_CostroidCatalogVersion"] = "9.9.9"
	forged["cost_usd"] = "0"
	forged["SkuPriceId"] = "anthropic:claude-haiku-5-5:global:batch:all:output"
	tags(forged)["costroid/cost_source"] = "provider_report"
	return forged
}

func TestAForgedCostChangesNothing(t *testing.T) {
	honest, forged := memoryWorldPair(t)
	if !reflect.DeepEqual(honest, forged) {
		t.Fatalf("a forged row stored differently:\n%+v\n%+v", honest, forged)
	}
	if honest.Row.ListCost.String() != "0.002" {
		t.Fatalf("list cost %s, want 100 tokens at 20 USD per million", honest.Row.ListCost)
	}
}

func memoryWorldPair(t *testing.T) (store.Record, store.Record) {
	t.Helper()
	send := func(r map[string]any) store.Record {
		w, ledger := memoryWorld(t)
		w.rows(w.token(), lines(t, r)).want(t, http.StatusOK)
		return ledger.only(t, tenantA)
	}
	honest := row("req-1", focus.Output, 100)
	return send(honest), send(forge(honest))
}

func TestAForgedCostIsCounted(t *testing.T) {
	w, _ := memoryWorld(t)
	var got ingestAnswer
	body := w.rows(w.token(), lines(t, forge(row("req-1", focus.Output, 100)))).want(t, http.StatusOK).body
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatal(err)
	}
	if got.Dropped["client_cost"] != 7 {
		t.Fatalf("%+v", got.Dropped)
	}
}

func TestTheServerPricesTheRecordedRunToItsKnownTotal(t *testing.T) {
	t.Run("from the rows costroid run wrote", func(t *testing.T) {
		w, ledger := memoryWorld(t)
		w.rows(w.token(), readFile(t, goldenRows)).want(t, http.StatusOK)
		if got := total(ledger.stored(t, tenantA)); got.Cmp(decimal.MustParse(goldenTotal)) != 0 {
			t.Fatalf("total %s", got)
		}
	})
	t.Run("from the agent's OTLP events", func(t *testing.T) {
		w, ledger := memoryWorld(t)
		body := goldenLogs(t, map[string]string{"service.name": "claude-code"})
		w.send(http.MethodPost, "/otlp/v1/logs", w.token(), "application/json", body).want(t, http.StatusOK)
		if got := total(ledger.stored(t, tenantA)); got.Cmp(decimal.MustParse(goldenTotal)) != 0 {
			t.Fatalf("total %s", got)
		}
	})
}

func TestBothPathsWriteTheSameRowsForOneRun(t *testing.T) {
	w, ledger := memoryWorld(t)
	w.rows(w.token(), readFile(t, goldenRows)).want(t, http.StatusOK)
	before := len(ledger.stored(t, tenantA))
	body := goldenLogs(t, map[string]string{"service.name": "claude-code"})
	w.send(http.MethodPost, "/otlp/v1/logs", w.token(), "application/json", body).want(t, http.StatusOK)
	if after := len(ledger.stored(t, tenantA)); after != before {
		t.Fatalf("OTLP added %d rows to a run already recorded", after-before)
	}
}

func TestAStatedPriceIsNotRead(t *testing.T) {
	w, ledger := memoryWorld(t)
	r := row("req-1", focus.Output, 1000000)
	r["ListCost"] = "0.1"
	w.rows(w.token(), lines(t, r)).want(t, http.StatusOK)
	if got := ledger.only(t, tenantA).Row.ListCost.String(); got != "20" {
		t.Fatalf("list cost %s", got)
	}
}

func TestAServiceTierAndContextTierAreUsageFactsFromThePriceID(t *testing.T) {
	w, ledger := memoryWorld(t)
	r := row("req-1", focus.InputUncached, 1000000)
	r["SkuId"] = "anthropic/claude-haiku-5-5/input_uncached"
	r["SkuPriceId"] = "anthropic:claude-haiku-5-5:global:batch:over-100000:input_uncached"
	tags(r)[focus.TagModel] = "claude-haiku-5-5"
	w.rows(w.token(), lines(t, r)).want(t, http.StatusOK)
	record := ledger.only(t, tenantA)
	if record.Row.SkuPriceID != "anthropic:claude-haiku-5-5:global:batch:over-100000:input_uncached" || record.Row.ListCost.String() != "0.25" {
		t.Fatalf("%s %s", record.Row.SkuPriceID, record.Row.ListCost)
	}
}

func TestAnUnknownModelFallsToANonZeroPrice(t *testing.T) {
	w, ledger := memoryWorld(t)
	r := row("req-1", focus.Output, 1000000)
	r["SkuId"] = "anthropic/claude-unreleased/output"
	r["SkuPriceId"] = "anthropic:claude-unreleased:global:standard:all:output"
	tags(r)[focus.TagModel] = "claude-unreleased"
	w.rows(w.token(), lines(t, r)).want(t, http.StatusOK)
	record := ledger.only(t, tenantA)
	if record.Row.ListCost.Sign() <= 0 || !strings.Contains(record.Row.ChargeDescription, "fallback") {
		t.Fatalf("%+v", record.Row)
	}
}

func TestAProviderWithNoSheetIsRefusedNeverStoredAtZero(t *testing.T) {
	w, ledger := memoryWorld(t)
	r := row("req-1", focus.Output, 100)
	r["SkuId"] = "example-ai/claude-opus-5-5/output"
	got := w.rows(w.token(), lines(t, r)).want(t, http.StatusUnprocessableEntity)
	if got.code(t) != codeUnknownPrice || len(ledger.rows) != 0 {
		t.Fatalf("%s", got.body)
	}
}

func TestInformationalClassesCarryNoPrice(t *testing.T) {
	w, ledger := memoryWorld(t)
	w.rows(w.token(), lines(t, row("req-1", focus.ReasoningOutput, 100))).want(t, http.StatusOK)
	got := ledger.only(t, tenantA).Row
	if got.ListCost.Sign() != 0 || got.ConsumedUnit != "Informational Tokens" {
		t.Fatalf("%+v", got)
	}
}

func TestListEquivalentStaysOutOfEffectiveCost(t *testing.T) {
	w, ledger := memoryWorld(t)
	r := row("req-1", focus.Output, 1000000)
	tags(r)[focus.TagCostBasis] = "list_equivalent"
	w.rows(w.token(), lines(t, r)).want(t, http.StatusOK)
	got := ledger.only(t, tenantA).Row
	if got.EffectiveCost.Sign() != 0 || got.ListCost.Sign() <= 0 {
		t.Fatalf("%s %s", got.EffectiveCost, got.ListCost)
	}
}

func TestARowWithNoVerifiableIdentityIsUnattributed(t *testing.T) {
	w, ledger := memoryWorld(t)
	r := row("req-1", focus.Output, 100)
	claim := tags(r)
	claim[focus.TagAttributionGrade] = "exact"
	claim[focus.TagRunIdentity] = "token_verified"
	claim[focus.TagEnforcementGrade] = "gateway-precheck"
	claim[store.TagRepo] = acmeRepo
	claim[store.TagPR] = acmePull
	r["x_CostroidOvershootBound"] = "1"
	w.rows(acmeKey, lines(t, r)).want(t, http.StatusOK)
	got := ledger.only(t, tenantA).Row
	for key, want := range map[string]string{
		focus.TagAttributionGrade: "unattributed", focus.TagRunIdentity: "unverified", focus.TagEnforcementGrade: "none",
	} {
		if got.Tags[key] != want {
			t.Errorf("%s = %q", key, got.Tags[key])
		}
	}
	if got.OvershootBound != nil {
		t.Errorf("an unverified sender's bound survived: %s", got.OvershootBound)
	}
}

func TestATokenThatCannotProveAPullRequestLeavesTheRowWindowed(t *testing.T) {
	w, ledger := memoryWorld(t)
	claims := w.claims()
	claims["ref"] = "refs/heads/main"
	claims["event_name"] = "push"
	r := row("req-1", focus.Output, 100)
	tags(r)[store.TagPR] = "777"
	tags(r)[store.TagCommitSHA] = acmeSHA
	w.rows(w.issuer.Token(claims), lines(t, r)).want(t, http.StatusOK)
	got := ledger.only(t, tenantA).Row.Tags
	if got[focus.TagAttributionGrade] != "windowed" || got[focus.TagRunIdentity] != "token_verified" {
		t.Fatalf("%v", got)
	}
	if _, present := got[store.TagPR]; present {
		t.Fatalf("an unprovable pull request was kept: %v", got)
	}
}

func TestExactNeedsASessionToFilterTheProviderReportBy(t *testing.T) {
	w, ledger := memoryWorld(t)
	r := row("req-1", focus.Output, 100)
	delete(tags(r), focus.TagSessionID)
	w.rows(w.token(), lines(t, r)).want(t, http.StatusOK)
	if got := ledger.only(t, tenantA).Row.Tags[focus.TagAttributionGrade]; got != "windowed" {
		t.Fatalf("grade %s", got)
	}
}

// rowVersions holds one row of each published version. Version 0.1 has no
// region or tier column, and 0.3 adds two classes and the assumed lifetime.
func rowVersions() map[string]map[string]any {
	v01 := row("req-1", focus.Output, 100)
	v02 := row("req-2", focus.Output, 100)
	v02["RegionId"], v02["RegionName"], v02["x_CostroidServiceTier"] = nil, nil, "standard"
	stated := openAIRow("req-3", focus.CacheWrite12h, 1000)
	assumed := openAIRow("req-4", focus.CacheWrite30m, 1000)
	tags(assumed)[focus.TagAssumedTTL] = "30m"
	return map[string]map[string]any{"0.1": v01, "0.2": v02, "0.3 stated": stated, "0.3 assumed": assumed}
}

func openAIRow(id string, class focus.TokenClass, tokens int) map[string]any {
	r := row(id, class, tokens)
	r["SkuId"] = "openai/gpt-5.6-sol/" + string(class)
	r["SkuPriceId"] = "openai:gpt-5.6-sol:global:standard:to-272000:" + string(class)
	r["RegionId"], r["RegionName"], r["x_CostroidServiceTier"] = nil, nil, "standard"
	tags(r)[focus.TagAgent] = "codex"
	tags(r)[focus.TagModel] = "gpt-5.6-sol"
	return r
}

func TestEveryPublishedRowVersionIsAccepted(t *testing.T) {
	for version, r := range rowVersions() {
		t.Run(version, func(t *testing.T) {
			w, ledger := memoryWorld(t)
			w.rows(w.token(), lines(t, r)).want(t, http.StatusOK)
			got := ledger.only(t, tenantA).Row
			if string(got.SkuMeter) != r["SkuMeter"] || got.ConsumedQuantity.String() != r["ConsumedQuantity"] {
				t.Fatalf("%s %s", got.SkuMeter, got.ConsumedQuantity)
			}
			if !strings.HasSuffix(got.SkuPriceID, ":"+string(got.SkuMeter)) {
				t.Fatalf("priced under %s", got.SkuPriceID)
			}
		})
	}
}

func TestACacheWriteKeepsItsClassPriceAndAssumedLifetime(t *testing.T) {
	w, ledger := memoryWorld(t)
	versions := rowVersions()
	w.rows(w.token(), lines(t, versions["0.3 assumed"], versions["0.3 stated"])).want(t, http.StatusOK)
	// From the sheet's gpt-5.6-sol rows per million: 1000 × 5.00 and 1000 × 10.00.
	want := map[focus.TokenClass]struct{ list, ttl string }{
		focus.CacheWrite30m: {"0.005", "30m"},
		focus.CacheWrite12h: {"0.01", ""},
	}
	for _, record := range ledger.stored(t, tenantA) {
		got, expected := record.Row, want[record.Row.SkuMeter]
		if got.ListCost.String() != expected.list || got.Tags[focus.TagAssumedTTL] != expected.ttl {
			t.Errorf("%s: list %s, tags %v", got.SkuMeter, got.ListCost, got.Tags)
		}
	}
}

func TestAnAssumedLifetimeThatMissesTheRowsClassIsRefused(t *testing.T) {
	cases := map[string]struct {
		class focus.TokenClass
		ttl   string
	}{
		"a class with no lifetime": {focus.CacheRead, "5m"},
		"another class's lifetime": {focus.CacheWrite30m, "5m"},
		"a lifetime no class has":  {focus.CacheWrite30m, "24h"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			w, ledger := memoryWorld(t)
			r := openAIRow("req-1", tc.class, 1000)
			tags(r)[focus.TagAssumedTTL] = tc.ttl
			got := w.rows(w.token(), lines(t, r)).want(t, http.StatusUnprocessableEntity)
			if got.code(t) != codeInvalidRow {
				t.Fatalf("code %s", got.code(t))
			}
			if rows := ledger.stored(t, tenantA); len(rows) != 0 {
				t.Fatalf("%d rows stored", len(rows))
			}
		})
	}
}

func TestTelemetryTagsAnAssumedLifetimeOnItsWriteClassOnly(t *testing.T) {
	cases := map[string]struct {
		split string
		ttl   string
	}{
		"a reported split": {"1500", ""},
		"no split":         {"0", "1h"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			w, ledger := memoryWorld(t)
			body := claudeLogs(t, map[string]string{"service.name": "claude-code"}, map[string]string{
				"event.name": "api_request", "model": "claude-haiku-5-5", "request_id": "req_1",
				"input_tokens": "2", "output_tokens": "84",
				"cache_creation_tokens": "1500", "cache_creation_1h_tokens": tc.split,
			})
			w.send(http.MethodPost, "/otlp/v1/logs", w.token(), "application/json", body).want(t, http.StatusOK)
			for _, record := range ledger.stored(t, tenantA) {
				want := ""
				if record.Row.SkuMeter == focus.CacheWrite1h {
					want = tc.ttl
				}
				if got := record.Row.Tags[focus.TagAssumedTTL]; got != want {
					t.Errorf("%s: assumed %q, want %q", record.Row.SkuMeter, got, want)
				}
			}
		})
	}
}

func TestASendersWindowedClaimIsNeverRaised(t *testing.T) {
	w, ledger := memoryWorld(t)
	r := row("req-1", focus.Output, 100)
	tags(r)[focus.TagAttributionGrade] = "windowed"
	w.rows(w.token(), lines(t, r)).want(t, http.StatusOK)
	if got := ledger.only(t, tenantA).Row.Tags[focus.TagAttributionGrade]; got != "windowed" {
		t.Fatalf("grade %s", got)
	}
}

func TestAGatewayGradeNeedsItsBound(t *testing.T) {
	w, ledger := memoryWorld(t)
	r := row("req-1", focus.Output, 100)
	tags(r)[focus.TagEnforcementGrade] = "gateway-precheck"
	w.rows(w.token(), lines(t, r)).want(t, http.StatusOK)
	if got := ledger.only(t, tenantA).Row.Tags[focus.TagEnforcementGrade]; got != "none" {
		t.Fatalf("grade %s", got)
	}
	r = row("req-2", focus.Output, 100)
	tags(r)[focus.TagEnforcementGrade] = "gateway-precheck"
	r["x_CostroidOvershootBound"] = "4.5"
	w.rows(w.token(), lines(t, r)).want(t, http.StatusOK)
	stored := ledger.rows[tenantID(t, tenantA)][focus.RowIdentity("req-2", focus.Output)]
	if stored.Row.OvershootBound == nil || stored.Row.OvershootBound.String() != "4.5" {
		t.Fatalf("%+v", stored.Row)
	}
}

func TestContentInARowIsDroppedAndCounted(t *testing.T) {
	w, ledger := memoryWorld(t)
	r := row("req-1", focus.Output, 100)
	for _, column := range []string{"prompt", "gen_ai.input.messages", "tool_output", "raw_body", "x_anything"} {
		r[column] = sentinel
	}
	tags(r)["gen_ai.output.messages"] = sentinel
	tags(r)["team"] = sentinel
	r["ChargeDescription"] = sentinel
	var got ingestAnswer
	body := w.rows(w.token(), lines(t, r)).want(t, http.StatusOK).body
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatal(err)
	}
	for _, label := range []string{"prompt", "gen_ai.input.messages", "tool_output", "gen_ai.output.messages"} {
		if got.Dropped[label] != 1 {
			t.Errorf("%s dropped %d times", label, got.Dropped[label])
		}
	}
	if strings.Contains(fmt.Sprintf("%+v", ledger.stored(t, tenantA)), sentinel) {
		t.Fatal("content reached the ledger")
	}
}

const sentinel = "SENTINEL-CONTENT-7f3a"

func telemetryWithContent(t *testing.T, extra map[string]string) []byte {
	t.Helper()
	record := map[string]string{
		"event.name": "api_request", "model": "claude-haiku-5-5", "request_id": "req_1", "session.id": "session-1",
		"input_tokens": "10", "output_tokens": "2", "cost_usd": "0.000000000001",
		"prompt": sentinel, "tool_parameters": sentinel, "gen_ai.input.messages": sentinel,
		"gen_ai.output.messages": sentinel, "gen_ai.system_instructions": sentinel, "tool_output": sentinel,
	}
	for key, value := range extra {
		record[key] = value
	}
	return claudeLogs(t, map[string]string{"service.name": "claude-code"}, record)
}

func TestTelemetryContentIsDroppedAndCounted(t *testing.T) {
	w, ledger := memoryWorld(t)
	w.send(http.MethodPost, "/otlp/v1/logs", w.token(), "application/json", telemetryWithContent(t, nil)).want(t, http.StatusOK)
	drops := ledger.drops[tenantID(t, tenantA)]
	for _, label := range []string{"prompt", "tool_parameters", "gen_ai.input.messages", "gen_ai.output.messages", "gen_ai.system_instructions", "tool_output", "client_cost"} {
		if drops[label] != 1 {
			t.Errorf("%s dropped %d times", label, drops[label])
		}
	}
	if len(ledger.stored(t, tenantA)) == 0 {
		t.Fatal("no rows")
	}
	if strings.Contains(fmt.Sprintf("%+v", ledger.stored(t, tenantA)), sentinel) {
		t.Fatal("content reached the ledger")
	}
}

func TestAForgedTelemetryCostChangesNothing(t *testing.T) {
	w, ledger := memoryWorld(t)
	body := telemetryWithContent(t, map[string]string{"cost_usd": "1000000", "gen_ai.usage.cost": "1000000"})
	w.send(http.MethodPost, "/otlp/v1/logs", w.token(), "application/json", body).want(t, http.StatusOK)
	want := decimal.MustParse("0.000002")
	if got := total(ledger.stored(t, tenantA)); got.Cmp(want) != 0 {
		t.Fatalf("total %s, want 10 input at 0.10 and 2 output at 0.50 USD per million", got)
	}
}

func TestUserEmailBecomesTheLoginUnderAToken(t *testing.T) {
	w, ledger := memoryWorld(t)
	body := telemetryWithContent(t, map[string]string{"user.email": "dev@example.com"})
	w.send(http.MethodPost, "/otlp/v1/logs", w.token(), "application/json", body).want(t, http.StatusOK)
	for _, record := range ledger.stored(t, tenantA) {
		if record.Actor != "octo-dev" {
			t.Fatalf("actor %q", record.Actor)
		}
	}
	if strings.Contains(fmt.Sprintf("%+v", ledger.stored(t, tenantA)), "dev@example.com") {
		t.Fatal("the address reached the ledger")
	}
	if ledger.drops[tenantID(t, tenantA)]["user.email"] != 1 {
		t.Fatalf("%+v", ledger.drops)
	}
}

func TestUserEmailBecomesAPerTenantPseudonymWithoutAToken(t *testing.T) {
	w, ledger := memoryWorld(t)
	body := telemetryWithContent(t, map[string]string{"user.email": "Dev@Example.com"})
	again := telemetryWithContent(t, map[string]string{"user.email": "dev@example.com ", "request_id": "req_2"})
	w.send(http.MethodPost, "/otlp/v1/logs", acmeKey, "application/json", body).want(t, http.StatusOK)
	w.send(http.MethodPost, "/otlp/v1/logs", acmeKey, "application/json", again).want(t, http.StatusOK)
	w.send(http.MethodPost, "/otlp/v1/logs", betaKey, "application/json", body).want(t, http.StatusOK)
	seen := map[string]bool{}
	for _, record := range ledger.stored(t, tenantA) {
		seen[record.Actor] = true
	}
	if len(seen) != 1 {
		t.Fatalf("one address, %d pseudonyms: %v", len(seen), seen)
	}
	for actor := range seen {
		if !strings.HasPrefix(actor, "hmac:") || len(actor) != len("hmac:")+64 {
			t.Fatalf("actor %q", actor)
		}
		for _, record := range ledger.stored(t, tenantB) {
			if record.Actor == actor {
				t.Fatal("two tenants share a pseudonym")
			}
		}
	}
	for _, tenant := range []string{tenantA, tenantB} {
		if text := strings.ToLower(fmt.Sprintf("%+v", ledger.stored(t, tenant))); strings.Contains(text, "example.com") {
			t.Fatal("the address reached the ledger")
		}
	}
}

func TestAnEmailTagOnARowBecomesAPseudonym(t *testing.T) {
	w, ledger := memoryWorld(t)
	r := row("req-1", focus.Output, 100)
	tags(r)["user.email"] = "dev@example.com"
	w.rows(acmeKey, lines(t, r)).want(t, http.StatusOK)
	got := ledger.only(t, tenantA)
	if !strings.HasPrefix(got.Actor, "hmac:") || strings.Contains(fmt.Sprintf("%+v", got), "example.com") {
		t.Fatalf("%+v", got)
	}
}

func TestTelemetryJoinKeysAreCheckedAgainstTheToken(t *testing.T) {
	cases := map[string]map[string]string{
		"repository":   {"vcs.repository.url.full": "https://github.com/acme/other"},
		"pull request": {"vcs.change.id": "999"},
		"run":          {"cicd.pipeline.run.id": "1"},
	}
	for name, resource := range cases {
		t.Run(name, func(t *testing.T) {
			w, ledger := memoryWorld(t)
			resource["service.name"] = "claude-code"
			body := goldenLogs(t, resource)
			w.send(http.MethodPost, "/otlp/v1/logs", w.token(), "application/json", body).want(t, http.StatusForbidden)
			if len(ledger.stored(t, tenantA)) != 0 {
				t.Fatal("rows stored")
			}
		})
	}
}

func TestTelemetryJoinKeysThatAgreePass(t *testing.T) {
	w, ledger := memoryWorld(t)
	body := goldenLogs(t, map[string]string{
		"service.name":            "claude-code",
		"vcs.repository.url.full": "https://github.com/Acme/Widgets.git",
		"vcs.change.id":           acmePull,
		"vcs.ref.head.revision":   acmeSHA,
		"cicd.pipeline.run.id":    acmeRun,
	})
	w.send(http.MethodPost, "/otlp/v1/logs", w.token(), "application/json", body).want(t, http.StatusOK)
	for _, record := range ledger.stored(t, tenantA) {
		if record.Row.Tags[focus.TagAttributionGrade] != "exact" || record.Row.Tags[focus.TagCostSource] != "otlp" {
			t.Fatalf("%v", record.Row.Tags)
		}
	}
}

func TestTelemetryCarriesTheStatedHeadBesideTheTokensCommit(t *testing.T) {
	w, ledger := memoryWorld(t)
	body := goldenLogs(t, map[string]string{"service.name": "claude-code", "vcs.ref.head.revision": strings.ToUpper(acmeHead)})
	w.send(http.MethodPost, "/otlp/v1/logs", w.token(), "application/json", body).want(t, http.StatusOK)
	for _, record := range ledger.stored(t, tenantA) {
		if record.Row.Tags[store.TagCommitSHA] != acmeHead || record.TokenCommitSHA != acmeSHA || record.HeadBasis != store.HeadStated {
			t.Fatalf("%+v", record)
		}
	}
}

func TestOTLPAnswersProtobufForProtobuf(t *testing.T) {
	w, _ := memoryWorld(t)
	got := w.send(http.MethodPost, "/otlp/v1/traces", w.token(), protobufType, nil).want(t, http.StatusOK)
	if len(got.body) != 0 {
		t.Fatalf("body %x", got.body)
	}
}

func TestPartialSuccessEncodesTheRejectedCount(t *testing.T) {
	if got := fmt.Sprintf("%x", partialSuccess(3)); got != "0a020803" {
		t.Fatalf("got %s", got)
	}
}

func TestARejectedTelemetryRecordIsReportedNotRetried(t *testing.T) {
	w, ledger := memoryWorld(t)
	body := claudeLogs(t, map[string]string{"service.name": "claude-code"}, map[string]string{
		"model": "claude-haiku-5-5", "input_tokens": "ten", "output_tokens": "2",
	})
	got := w.send(http.MethodPost, "/otlp/v1/logs", w.token(), "application/json", body).want(t, http.StatusOK)
	if !strings.Contains(string(got.body), "partialSuccess") {
		t.Fatalf("%s", got.body)
	}
	if len(ledger.stored(t, tenantA)) != 0 {
		t.Fatal("rows stored")
	}
}

func TestAnInvalidModelNameIsRejectedNotStored(t *testing.T) {
	w, ledger := memoryWorld(t)
	body := claudeLogs(t, map[string]string{"service.name": "claude-code"}, map[string]string{
		"model": "not a model name: " + sentinel, "input_tokens": "1", "output_tokens": "2",
	})
	got := w.send(http.MethodPost, "/otlp/v1/logs", w.token(), "application/json", body).want(t, http.StatusOK)
	if !strings.Contains(string(got.body), "partialSuccess") || len(ledger.stored(t, tenantA)) != 0 {
		t.Fatalf("%s", got.body)
	}
}

func TestMetricsAreAcceptedAndStoreNoRows(t *testing.T) {
	w, ledger := memoryWorld(t)
	w.send(http.MethodPost, "/otlp/v1/metrics", w.token(), "application/json", []byte(`{}`)).want(t, http.StatusOK)
	if ledger.drops[tenantID(t, tenantA)]["metrics_export"] != 1 || len(ledger.stored(t, tenantA)) != 0 {
		t.Fatalf("%+v", ledger.drops)
	}
}

func TestEveryWritePathNeedsACredential(t *testing.T) {
	w, _ := memoryWorld(t)
	for _, path := range []string{"/v1/rows", "/otlp/v1/traces", "/otlp/v1/logs", "/otlp/v1/metrics"} {
		w.send(http.MethodPost, path, "", "application/json", []byte(`{}`)).want(t, http.StatusUnauthorized)
	}
	w.send(http.MethodGet, "/v1/pulls/1?repo="+acmeRepo, "", "", nil).want(t, http.StatusUnauthorized)
}

func TestAGzipBodyInflates(t *testing.T) {
	w, ledger := memoryWorld(t)
	var buffer bytes.Buffer
	zipper := gzip.NewWriter(&buffer)
	if _, err := zipper.Write(lines(t, row("req-1", focus.Output, 100))); err != nil {
		t.Fatal(err)
	}
	if err := zipper.Close(); err != nil {
		t.Fatal(err)
	}
	w.send(http.MethodPost, "/v1/rows", w.token(), "application/x-ndjson", buffer.Bytes(), "Content-Encoding", "gzip").want(t, http.StatusOK)
	if len(ledger.stored(t, tenantA)) != 1 {
		t.Fatal("no row")
	}
}

func TestAGzipBombIsCappedAfterInflation(t *testing.T) {
	w, _ := memoryWorld(t)
	var buffer bytes.Buffer
	zipper, err := gzip.NewWriterLevel(&buffer, gzip.BestCompression)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := zipper.Write(make([]byte, maxBodyBytes+1)); err != nil {
		t.Fatal(err)
	}
	if err := zipper.Close(); err != nil {
		t.Fatal(err)
	}
	if buffer.Len() >= maxBodyBytes {
		t.Fatal("the bomb did not compress")
	}
	got := w.send(http.MethodPost, "/v1/rows", w.token(), "application/x-ndjson", buffer.Bytes(), "Content-Encoding", "gzip")
	got.want(t, http.StatusRequestEntityTooLarge)
}

func TestAGzipBombInflatesNoFurtherThanTheCap(t *testing.T) {
	var buffer bytes.Buffer
	zipper := gzip.NewWriter(&buffer)
	if _, err := zipper.Write(make([]byte, 4*maxBodyBytes)); err != nil {
		t.Fatal(err)
	}
	if err := zipper.Close(); err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "/v1/rows", &buffer)
	request.Header.Set("Content-Encoding", "gzip")
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	_, err := readBody(httptest.NewRecorder(), request)
	runtime.ReadMemStats(&after)
	var typed *refusal
	if !errors.As(err, &typed) || typed.code != codeBodyTooLarge {
		t.Fatalf("got %v", err)
	}
	// io.ReadAll allocates about twice what it reads, so a read stopped at the
	// cap stays under three caps, and the whole bomb would pass eight.
	if allocated := after.TotalAlloc - before.TotalAlloc; allocated > 3*maxBodyBytes {
		t.Fatalf("allocated %d bytes reading a body capped at %d", allocated, maxBodyBytes)
	}
}

func TestABodyOverTheCapIsRefused(t *testing.T) {
	w, _ := memoryWorld(t)
	w.rows(w.token(), make([]byte, maxBodyBytes+1)).want(t, http.StatusRequestEntityTooLarge)
}

func TestMalformedRowsAreRefusedWithTheirLine(t *testing.T) {
	good := func() map[string]any { return row("req-1", focus.Output, 100) }
	cases := map[string]func() []byte{
		"not json": func() []byte { return []byte("{") },
		"bad id": func() []byte {
			r := good()
			r["x_CostroidRowId"] = "short"
			return lines(t, r)
		},
		"sku without a model": func() []byte {
			r := good()
			r["SkuId"] = "anthropic"
			return lines(t, r)
		},
		"meter disagrees with sku": func() []byte {
			r := good()
			r["SkuMeter"] = "input_uncached"
			return lines(t, r)
		},
		"class outside the vocabulary": func() []byte {
			r := good()
			r["SkuId"], r["SkuMeter"] = "anthropic/claude-opus-5-5/free_tokens", "free_tokens"
			return lines(t, r)
		},
		"model tag disagrees": func() []byte {
			r := good()
			tags(r)[focus.TagModel] = "claude-haiku-5-5"
			return lines(t, r)
		},
		"fractional tokens": func() []byte {
			r := good()
			r["ConsumedQuantity"] = "1.5"
			return lines(t, r)
		},
		"negative tokens": func() []byte {
			r := good()
			r["ConsumedQuantity"] = "-1"
			return lines(t, r)
		},
		"tokens past the bound": func() []byte {
			r := good()
			r["ConsumedQuantity"] = "9000000000000000"
			return lines(t, r)
		},
		"period backwards": func() []byte {
			r := good()
			r["ChargePeriodEnd"] = "2026-11-01T09:00:00Z"
			return lines(t, r)
		},
		"undefined costroid tag": func() []byte {
			r := good()
			tags(r)["costroid/price"] = "0"
			return lines(t, r)
		},
		"basis the path cannot price": func() []byte {
			r := good()
			tags(r)[focus.TagCostBasis] = "billed"
			return lines(t, r)
		},
		"row over the size limit": func() []byte {
			r := good()
			r["prompt"] = strings.Repeat("x", maxRowBytes)
			return lines(t, r)
		},
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			w, ledger := memoryWorld(t)
			got := w.rows(w.token(), body())
			if got.status != http.StatusUnprocessableEntity || got.code(t) != codeInvalidRow {
				t.Fatalf("status %d: %s", got.status, got.body)
			}
			if !strings.Contains(string(got.body), "Line") {
				t.Fatalf("%s", got.body)
			}
			if len(ledger.rows) != 0 {
				t.Fatal("rows stored")
			}
		})
	}
}

func TestAFutureChargeIsRefused(t *testing.T) {
	w, ledger := memoryWorld(t)
	r := row("req-1", focus.Output, 100)
	r["ChargePeriodStart"], r["ChargePeriodEnd"] = "2027-01-01T00:00:00Z", "2027-01-01T00:00:01Z"
	got := w.rows(w.token(), lines(t, r)).want(t, http.StatusUnprocessableEntity)
	if got.code(t) != codeInvalidRow || len(ledger.rows) != 0 {
		t.Fatalf("%s", got.body)
	}
}

func TestARefusalEchoesNothingTheSenderSupplied(t *testing.T) {
	w, _ := memoryWorld(t)
	r := row("req-1", focus.Output, 100)
	r["SkuId"] = sentinel
	got := w.rows(w.token(), lines(t, r))
	if strings.Contains(string(got.body), sentinel) {
		t.Fatalf("%s", got.body)
	}
}

func TestViewAuthorization(t *testing.T) {
	w, _ := memoryWorld(t)
	path := "/v1/pulls/" + acmePull + "?repo="
	w.send(http.MethodGet, path+acmeRepo, w.token(), "", nil).want(t, http.StatusOK)
	got := w.send(http.MethodGet, path+"github.com/acme/other", w.token(), "", nil).want(t, http.StatusForbidden)
	if got.code(t) != codeWrongRepo {
		t.Fatalf("code %s", got.code(t))
	}
	w.send(http.MethodGet, path+"github.com/acme/other", acmeKey, "", nil).want(t, http.StatusOK)
	w.send(http.MethodGet, path+"not a repo", acmeKey, "", nil).want(t, http.StatusBadRequest)
	w.send(http.MethodGet, "/v1/pulls/abc?repo="+acmeRepo, acmeKey, "", nil).want(t, http.StatusBadRequest)
}

func line(currency, list string, eligible bool) store.Line {
	return store.Line{Currency: currency, ListCost: decimal.MustParse(list), BudgetEligible: eligible}
}

func TestABudgetReadsEligibleLinesOnlyAndNeverAddsCurrencies(t *testing.T) {
	got := budgetOf([]store.Line{
		line("EUR", "1", true), line("USD", "2", true), line("USD", "4", false), line("USD", "8", true),
	})
	want := budgetView{Evidence: true, Spend: []currencySpend{{"EUR", "1"}, {"USD", "10"}}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v", got)
	}
}

func TestUnattributedLinesAreNoEvidenceForABudget(t *testing.T) {
	got := budgetOf([]store.Line{line("USD", "4", false)})
	if got.Evidence || len(got.Spend) != 0 {
		t.Fatalf("got %+v", got)
	}
}

func TestRepoOfNormalizesAURL(t *testing.T) {
	for input, want := range map[string]string{
		"https://github.com/Acme/Widgets":             acmeRepo,
		"https://github.com/acme/widgets.git":         acmeRepo,
		"https://github.com/acme/widgets/":            acmeRepo,
		"https://user:secret@github.com/acme/widgets": acmeRepo,
		"github.com/acme/widgets":                     acmeRepo,
		"":                                            "",
	} {
		if got := repoOf(input); got != want {
			t.Errorf("%q: got %q", input, got)
		}
	}
}

func TestRefusalReasonNamesTheCauseOnly(t *testing.T) {
	wrapped := fmt.Errorf("%w: kid %q", oidc.ErrKeyUnavailable, sentinel)
	if got := refusalReason(wrapped); strings.Contains(got, sentinel) {
		t.Fatalf("got %s", got)
	}
	if got := refusalReason(errors.New("x")); got != "oidc: unknown" {
		t.Fatalf("got %s", got)
	}
}

func FuzzParseRows(f *testing.F) {
	for _, seed := range []string{
		`{"x_CostroidRowId":"` + strings.Repeat("a", 64) + `","SkuId":"anthropic/m/output","SkuMeter":"output","ConsumedQuantity":"1","ChargePeriodStart":"2026-11-01T10:00:00Z","ChargePeriodEnd":"2026-11-01T10:00:01Z","Tags":{"costroid/model":"m","costroid/agent":"a","costroid/cost_basis":"measured"}}`,
		"{}\n[]\nnull\n",
		`{"Tags":{"user.email":"a@b.c"}}`,
	} {
		f.Add([]byte(seed))
	}
	f.Fuzz(func(t *testing.T, body []byte) {
		drops := map[string]int64{}
		charges, err := parseRows(body, drops)
		if err != nil {
			var typed *refusal
			if !errors.As(err, &typed) {
				t.Fatalf("an untyped refusal: %v", err)
			}
			return
		}
		for i := range charges {
			if charges[i].tokens < 0 || !classes[charges[i].class] || !rowIDPattern.MatchString(charges[i].rowID) {
				t.Fatalf("an invalid charge passed: %+v", charges[i])
			}
		}
		for label := range drops {
			if len(label) > 64 {
				t.Fatalf("an unbounded label %q", label)
			}
		}
	})
}

func manyRows(n int) []map[string]any {
	rows := make([]map[string]any, n)
	for i := range rows {
		rows[i] = row(fmt.Sprintf("req-%d", i), focus.Output, 100+i)
	}
	return rows
}

func TestALargeBodyKeepsItsOrderAndCountsItsDropsOnce(t *testing.T) {
	body := make([]map[string]any, 0)
	for _, r := range manyRows(5000) {
		r["ForeignColumn"] = "x"
		body = append(body, r)
	}
	drops := map[string]int64{}
	charges, err := parseRows(lines(t, body...), drops)
	if err != nil {
		t.Fatal(err)
	}
	for i := range charges {
		if charges[i].rowID != body[i]["x_CostroidRowId"] {
			t.Fatalf("row %d out of order", i)
		}
	}
	if got := drops[otlp.DropClientCost]; got != 5000*6 {
		t.Fatalf("client_cost %d", got)
	}
	if got := drops[otlp.DropLabel("ForeignColumn")]; got != 5000 {
		t.Fatalf("ForeignColumn %d", got)
	}
}

func TestALargeBodyIsRefusedAtItsEarliestBadLine(t *testing.T) {
	rows := manyRows(5000)
	rows[1234]["SkuMeter"] = "nothing"
	rows[4000]["SkuMeter"] = "nothing"
	_, err := parseRows(lines(t, rows...), map[string]int64{})
	var refused *refusal
	if !errors.As(err, &refused) || !strings.Contains(refused.message, "Line 1235:") {
		t.Fatalf("err = %v", err)
	}
}

func TestABadLineAboveAnOversizedOneIsTheOneReported(t *testing.T) {
	rows := manyRows(2)
	rows[0]["SkuMeter"] = "nothing"
	body := append(lines(t, rows...), bytes.Repeat([]byte("x"), maxRowBytes+1)...)
	_, err := parseRows(body, map[string]int64{})
	var refused *refusal
	if !errors.As(err, &refused) || !strings.Contains(refused.message, "Line 1:") {
		t.Fatalf("err = %v", err)
	}
}

func TestALargeBatchStoresAllOrNothingOnOneBadRow(t *testing.T) {
	w, ledger := memoryWorld(t)
	rows := manyRows(3000)
	tags(rows[2500])[store.TagRepo] = "github.com/acme/other"
	if got := w.rows(w.token(), lines(t, rows...)).want(t, http.StatusForbidden); got.code(t) != codeJoinKey {
		t.Fatalf("code %s", got.code(t))
	}
	if len(ledger.rows) != 0 && len(ledger.stored(t, tenantA)) != 0 {
		t.Fatal("rows stored from a refused batch")
	}
}

func TestAStatedRegionIsPricedAsTheCLIPricesIt(t *testing.T) {
	w, ledger := memoryWorld(t)
	r := row("req-us", focus.InputUncached, 1000000)
	r["SkuId"] = "anthropic/claude-haiku-5-5/input_uncached"
	r["SkuPriceId"] = "anthropic:claude-haiku-5-5:global:standard:to-100000:input_uncached"
	r["RegionId"] = "us"
	r["RegionName"] = "us"
	tags(r)[focus.TagModel] = "claude-haiku-5-5"
	w.rows(w.token(), lines(t, r)).want(t, http.StatusOK)
	got := ledger.only(t, tenantA).Row
	// The sheet's 0.10 per million tokens, times its multiplier of 1.1 for us.
	if got.ListCost.Cmp(decimal.MustParse("0.11")) != 0 {
		t.Fatalf("list cost %s", got.ListCost)
	}
	if got.RegionID == nil || *got.RegionID != "us" || !strings.Contains(got.ChargeDescription, "for data residency in us") {
		t.Fatalf("%v %q", got.RegionID, got.ChargeDescription)
	}
}

func TestARowWithNoRegionPricesAtGlobalAndStatesNone(t *testing.T) {
	w, ledger := memoryWorld(t)
	w.rows(w.token(), lines(t, row("req-1", focus.Output, 100))).want(t, http.StatusOK)
	if got := ledger.only(t, tenantA).Row; got.RegionID != nil || got.RegionName != nil {
		t.Fatalf("%v %v", got.RegionID, got.RegionName)
	}
}

func TestATierColumnBeatsTheTierInThePriceID(t *testing.T) {
	w, ledger := memoryWorld(t)
	r := row("req-1", focus.InputUncached, 1000000)
	r["SkuId"] = "anthropic/claude-haiku-5-5/input_uncached"
	r["SkuPriceId"] = "anthropic:claude-haiku-5-5:global:standard:to-100000:input_uncached"
	r["x_CostroidServiceTier"] = "batch"
	tags(r)[focus.TagModel] = "claude-haiku-5-5"
	w.rows(w.token(), lines(t, r)).want(t, http.StatusOK)
	got := ledger.only(t, tenantA).Row
	if got.ServiceTier == nil || *got.ServiceTier != "batch" || !strings.Contains(got.SkuPriceID, ":batch:") {
		t.Fatalf("%v %s", got.ServiceTier, got.SkuPriceID)
	}
}

func TestACredentialIsKeptSoAStoredRowCanReconcile(t *testing.T) {
	w, ledger := memoryWorld(t)
	r := row("req-1", focus.Output, 100)
	r["SubAccountId"] = "apikey_01ExampleCi"
	got := w.rows(w.token(), lines(t, r)).want(t, http.StatusOK)
	var receipt ingestAnswer
	if err := json.Unmarshal(got.body, &receipt); err != nil {
		t.Fatal(err)
	}
	if receipt.Dropped[otlp.DropLabel("SubAccountId")] != 0 {
		t.Fatalf("counted as a drop: %s", got.body)
	}
	if id := ledger.only(t, tenantA).Row.SubAccountID; id == nil || *id != "apikey_01ExampleCi" {
		t.Fatalf("%v", id)
	}
}

func TestABillingAccountIsKeptAsStated(t *testing.T) {
	for _, stated := range []string{"acct-1", "unknown", strings.Repeat("é", 512)} {
		w, ledger := memoryWorld(t)
		r := row("req-1", focus.Output, 100)
		r["BillingAccountId"] = stated
		got := w.rows(w.token(), lines(t, r)).want(t, http.StatusOK)
		var receipt ingestAnswer
		if err := json.Unmarshal(got.body, &receipt); err != nil {
			t.Fatal(err)
		}
		if receipt.Dropped[otlp.DropLabel("BillingAccountId")] != 0 {
			t.Fatalf("counted as a drop: %s", got.body)
		}
		if account := ledger.only(t, tenantA).Row.BillingAccountID; account != stated {
			t.Fatalf("reads %q, want %q", account, stated)
		}
	}
}

func TestARowThatStatesNoBillingAccountReadsUnknown(t *testing.T) {
	for name, edit := range map[string]func(map[string]any){
		"absent": func(r map[string]any) { delete(r, "BillingAccountId") },
		"null":   func(r map[string]any) { r["BillingAccountId"] = nil },
	} {
		t.Run(name, func(t *testing.T) {
			w, ledger := memoryWorld(t)
			r := row("req-1", focus.Output, 100)
			edit(r)
			w.rows(w.token(), lines(t, r)).want(t, http.StatusOK)
			if account := ledger.only(t, tenantA).Row.BillingAccountID; account != "unknown" {
				t.Fatalf("reads %q", account)
			}
		})
	}
}

func TestABillingAccountNeverChoosesTheTenant(t *testing.T) {
	w, ledger := memoryWorld(t)
	r := row("req-1", focus.Output, 100)
	r["BillingAccountId"] = tenantB
	w.rows(w.token(), lines(t, r)).want(t, http.StatusOK)
	if account := ledger.only(t, tenantA).Row.BillingAccountID; account != tenantB {
		t.Fatalf("reads %q", account)
	}
	if len(ledger.rows[tenantID(t, tenantB)]) != 0 {
		t.Fatal("the stated account moved the row to its tenant")
	}
}

func TestRoutingColumnsThatAreNotValidRefuseTheRow(t *testing.T) {
	for column, value := range map[string]any{
		"RegionId":              "us; drop",
		"x_CostroidServiceTier": "Batch Tier",
		"SubAccountId":          strings.Repeat("a", 513),
		"BillingAccountId":      strings.Repeat("a", 513),
	} {
		w, ledger := memoryWorld(t)
		r := row("req-1", focus.Output, 100)
		r[column] = value
		got := w.rows(w.token(), lines(t, r)).want(t, http.StatusUnprocessableEntity)
		if got.code(t) != codeInvalidRow || len(ledger.rows) != 0 {
			t.Errorf("%s: %s", column, got.body)
		}
	}
}

func TestTheViewTotalsPerCurrencyAndBasisAndNeverAcrossThem(t *testing.T) {
	lines := []store.Line{
		{Currency: "EUR", CostBasis: "measured", ListCost: decimal.MustParse("1.5")},
		{Currency: "USD", CostBasis: "list_equivalent", ListCost: decimal.MustParse("0.25")},
		{Currency: "USD", CostBasis: "list_equivalent", ListCost: decimal.MustParse("0.25")},
		{Currency: "USD", CostBasis: "measured", ListCost: decimal.MustParse("2")},
	}
	got := totalsOf(lines)
	want := []basisTotal{
		{"EUR", "measured", "Measured", "1.5"},
		{"USD", "list_equivalent", "List-equivalent", "0.5"},
		{"USD", "measured", "Measured", "2"},
	}
	if !slices.Equal(got, want) {
		t.Fatalf("%+v", got)
	}
}
