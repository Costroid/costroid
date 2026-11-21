package policy

import (
	"maps"
	"slices"
	"testing"
	"time"

	"github.com/Costroid/costroid/internal/decimal"
)

var demotionStart = time.Date(2026, 11, 2, 9, 0, 0, 0, time.UTC)

type overrideCase struct {
	failing, approved, approvers int
	demotes                      bool
}

func checkDemotes(t *testing.T, cases []overrideCase) {
	t.Helper()
	for _, c := range cases {
		if got := LedgerOverrides(c.failing, c.approved, c.approvers).demotes(); got != c.demotes {
			t.Errorf("%d approved of %d failing, %d approvers: demotes %t, want %t", c.approved, c.failing, c.approvers, got, c.demotes)
		}
	}
}

func TestOverrideDemotion(t *testing.T) {
	checkDemotes(t, []overrideCase{
		{failing: 10, approved: 2, approvers: 2, demotes: false},
		{failing: 10, approved: 3, approvers: 2, demotes: true},
		{failing: 15, approved: 3, approvers: 2, demotes: false},
		{failing: 16, approved: 4, approvers: 3, demotes: true},
		{failing: 20, approved: 4, approvers: 3, demotes: false},
		{failing: 20, approved: 5, approvers: 3, demotes: true},
		{failing: 20, approved: 20, approvers: 9, demotes: true},
		{failing: 20, approved: 0, approvers: 0, demotes: false},
	})
}

func TestOverrideDemotionNeedsItsSample(t *testing.T) {
	checkDemotes(t, []overrideCase{
		{failing: 9, approved: 9, approvers: 9, demotes: false},
		{failing: 1, approved: 1, approvers: 2, demotes: false},
		{failing: 10, approved: 3, approvers: 2, demotes: true},
	})
}

func TestOverrideDemotionNeedsTwoApprovers(t *testing.T) {
	checkDemotes(t, []overrideCase{
		{failing: 20, approved: 20, approvers: 1, demotes: false},
		{failing: 20, approved: 20, approvers: 0, demotes: false},
		{failing: 20, approved: 20, approvers: 2, demotes: true},
	})
}

func TestAClaimNeverDemotes(t *testing.T) {
	claim := Overrides{Failing: 20, Approved: 20, Approvers: 9}
	if claim.demotes() {
		t.Fatal("a record built outside a ledger demotes")
	}
	limit := decimal.MustParse("5")
	if got := (Demotion{Limit: limit}).Record(demotionStart, claim); got.Demoted(demotionStart) {
		t.Fatalf("a claim started a term: %+v", got)
	}
	if got := (Demotion{Limit: limit}).Record(demotionStart, LedgerOverrides(20, 20, 9)); !got.Demoted(demotionStart) {
		t.Fatalf("the same record read from a ledger did not demote: %+v", got)
	}
}

func TestOverrideDemotionRearms(t *testing.T) {
	limit := decimal.MustParse("5")
	demoting := LedgerOverrides(10, 3, 2)
	demoted := Demotion{Limit: limit}.Record(demotionStart, demoting)
	day := 24 * time.Hour

	t.Run("the term runs thirty days from the verdict that first crossed the threshold", func(t *testing.T) {
		if want := demotionStart.Add(30 * day); !demoted.Until().Equal(want) {
			t.Fatalf("until %s, want %s", demoted.Until(), want)
		}
		later := demoted.Rearm(demotionStart.Add(10*day), limit, true).Record(demotionStart.Add(10*day), LedgerOverrides(20, 20, 4))
		if !later.From.Equal(demotionStart) {
			t.Fatalf("a later verdict moved the term's start to %s", later.From)
		}
		last := demotionStart.Add(30*day - time.Second)
		if !demoted.Rearm(last, limit, true).Demoted(last) {
			t.Fatal("the rule was not demoted a second before its term ended")
		}
	})
	t.Run("at the term's end the window starts empty", func(t *testing.T) {
		at := demotionStart.Add(31 * day)
		got := demoted.Rearm(at, limit, true)
		if got.Demoted(at) || !got.Rearmed.Equal(demoted.Until()) {
			t.Fatalf("after the term: %+v", got)
		}
	})
	t.Run("a changed limit re-arms at once", func(t *testing.T) {
		at := demotionStart.Add(day)
		got := demoted.Rearm(at, decimal.MustParse("6"), true)
		if got.Demoted(at) || !got.Rearmed.Equal(at) || got.Limit.String() != "6" {
			t.Fatalf("after a changed limit: %+v", got)
		}
	})
	t.Run("the same limit written another way does not re-arm", func(t *testing.T) {
		at := demotionStart.Add(day)
		if got := demoted.Rearm(at, decimal.MustParse("5.00"), true); !got.Demoted(at) || !got.Rearmed.IsZero() {
			t.Fatalf("5.00 re-armed a rule demoted under 5: %+v", got)
		}
	})
	t.Run("turning demotion off re-arms at once", func(t *testing.T) {
		at := demotionStart.Add(day)
		got := demoted.Rearm(at, limit, false)
		if got.Demoted(at) || !got.Rearmed.Equal(at) {
			t.Fatalf("after turning it off: %+v", got)
		}
	})
	t.Run("an armed rule stays armed until its record demotes it", func(t *testing.T) {
		armed := Demotion{Limit: limit}
		if got := armed.Rearm(demotionStart, limit, true); !got.Rearmed.IsZero() || !got.From.IsZero() || got.Limit.Cmp(limit) != 0 {
			t.Fatalf("an armed rule moved: %+v", got)
		}
		if got := armed.Record(demotionStart, LedgerOverrides(10, 2, 2)); got.Demoted(demotionStart) {
			t.Fatalf("2 of 10 started a term: %+v", got)
		}
	})
}

func TestEveryRuleIsDemotableOrNever(t *testing.T) {
	listed := slices.Sorted(maps.Keys(demotable))
	rules := slices.Sorted(slices.Values(reservedRules))
	if !slices.Equal(listed, rules) {
		t.Fatalf("the demotion list names %v, and Costroid's rules are %v", listed, rules)
	}
	for _, rule := range []string{RuleModelAllowlist, RuleCustomerPack, "no_opus_on_docs"} {
		if Demotable(rule) {
			t.Errorf("%s can be demoted", rule)
		}
	}
	for _, rule := range runCostRules {
		if !Demotable(rule) {
			t.Errorf("%s holds a run's cost to a limit and cannot be demoted", rule)
		}
	}
	if !Demotable(RulePullRequestBudget) {
		t.Errorf("%s holds a pull request's cost to a limit and cannot be demoted", RulePullRequestBudget)
	}
}
