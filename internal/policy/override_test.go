package policy

import (
	"context"
	"maps"
	"reflect"
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
		if got := ledgerOverrides(c.failing, c.approved, c.approvers).demotes(); got != c.demotes {
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
	if got := (Demotion{Limit: limit}).Record(demotionStart, ledgerOverrides(20, 20, 9)); !got.Demoted(demotionStart) {
		t.Fatalf("the same record read from a ledger did not demote: %+v", got)
	}
}

func TestOverrideDemotionRearms(t *testing.T) {
	limit := decimal.MustParse("5")
	demoting := ledgerOverrides(10, 3, 2)
	demoted := Demotion{Limit: limit}.Record(demotionStart, demoting)
	day := 24 * time.Hour

	t.Run("the term runs thirty days from the verdict that first crossed the threshold", func(t *testing.T) {
		if want := demotionStart.Add(30 * day); !demoted.Until().Equal(want) {
			t.Fatalf("until %s, want %s", demoted.Until(), want)
		}
		later := demoted.Rearm(demotionStart.Add(10*day), limit, true).Record(demotionStart.Add(10*day), ledgerOverrides(20, 20, 4))
		if !later.From.Equal(demotionStart) {
			t.Fatalf("a later verdict moved the term's start to %s", later.From)
		}
		last := demotionStart.Add(30*day - time.Second)
		if !demoted.Rearm(last, limit, true).Demoted(last) {
			t.Fatal("the rule was not demoted a second before its term ended")
		}
		if demoted.Demoted(demoted.Until()) {
			t.Fatal("the rule was still demoted when its term ended")
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
		if got := armed.Record(demotionStart, ledgerOverrides(10, 2, 2)); got.Demoted(demotionStart) {
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

// memoryRecord is a ledger's override record of one judged pull request. It
// answers the counts counted gives for the window asked, keeps what a verdict
// keeps, and notes every call.
type memoryRecord struct {
	counted  func(rule string, rearmed time.Time) (failing, approved, approvers int)
	failures []string
	asked    []time.Time
	kept     map[string]Demotion
	reads    int
}

func newMemoryRecord(counted func(rule string, rearmed time.Time) (int, int, int)) *memoryRecord {
	return &memoryRecord{counted: counted, kept: map[string]Demotion{}}
}

func (m *memoryRecord) AddFailure(_ context.Context, rule string, limit decimal.Decimal, _ time.Time) error {
	m.failures = append(m.failures, rule+" "+limit.String())
	return nil
}

func (m *memoryRecord) Overrides(_ context.Context, rule string, _ decimal.Decimal, rearmed, _ time.Time) (int, int, int, error) {
	m.asked = append(m.asked, rearmed)
	failing, approved, approvers := m.counted(rule, rearmed)
	return failing, approved, approvers, nil
}

func (m *memoryRecord) Demotion(_ context.Context, rule string) (Demotion, bool, error) {
	m.reads++
	kept, found := m.kept[rule]
	// What a ledger hands back is data, as a row is: its record unverified.
	kept.Crossed = Overrides{Failing: kept.Crossed.Failing, Approved: kept.Crossed.Approved, Approvers: kept.Crossed.Approvers}
	return kept, found, nil
}

func (m *memoryRecord) KeepDemotion(ctx context.Context, rule string, demotion Demotion) (Demotion, error) {
	m.kept[rule] = demotion
	kept, _, err := m.Demotion(ctx, rule)
	return kept, err
}

// overridden is a record of 10 failing pull requests, 3 of them approved by 2
// accounts, from the record's start, and of none once the rule is re-armed.
func overridden(string, time.Time) (int, int, int) {
	return 10, 3, 2
}

func emptySinceRearmed(_ string, rearmed time.Time) (int, int, int) {
	if rearmed.IsZero() {
		return overridden("", rearmed)
	}
	return 0, 0, 0
}

// ledgerVerdict is a ledger's verdict on a pull request whose rows cost USD
// 10, under settings, read with record.
func ledgerVerdict(t *testing.T, settings string, record OverrideLedger, at time.Time) Verdict {
	t.Helper()
	verdict, err := EvaluateLedger(context.Background(), LedgerRequest{
		Lines:  []LedgerLine{{Currency: "USD", ListCost: mustAmount(t, "10"), Rows: 1, Model: "claude-opus-5-5"}},
		Config: config(settings), At: at, Record: record,
	})
	if err != nil {
		t.Fatal(err)
	}
	return verdict
}

const overBudget = "policy:\n  pr_budget_usd: \"5\"\n  enforce: true\n"

// TestOverrideDemotionCanBeTurnedOff holds policy.auto_demote: false on the
// base branch to its word: a rule whose record demotes it fails as written,
// its failure is still recorded, and every rule is re-armed at once, so a
// demotion under way ends. Turned on again, the window starts empty at that
// verdict. A rule that did not fail reads nothing of its record. The key takes
// true or false and nothing else.
func TestOverrideDemotionCanBeTurnedOff(t *testing.T) {
	record := newMemoryRecord(emptySinceRearmed)
	day := 24 * time.Hour
	on := ledgerVerdict(t, overBudget, record, checkTime)
	if got := result(t, on, RulePullRequestBudget); got.Demoted == nil || on.Conclusion != Neutral {
		t.Fatalf("with demotion on, a record of 3 in 10 did not demote: %+v", on)
	}
	off := ledgerVerdict(t, overBudget+"  auto_demote: false\n", record, checkTime.Add(day))
	if got := result(t, off, RulePullRequestBudget); got.Outcome != Fail || got.Demoted != nil || off.Conclusion != Failure {
		t.Fatalf("with demotion off, the rule did not fail as written: %+v", off)
	}
	if kept := record.kept[RulePullRequestBudget]; !kept.Off || !kept.From.IsZero() || !kept.Rearmed.Equal(checkTime.Add(day)) {
		t.Fatalf("turning demotion off did not re-arm the rule at once: %+v", kept)
	}
	if len(record.failures) != 2 {
		t.Fatalf("with demotion off the failure was not recorded: %v", record.failures)
	}
	ledgerVerdict(t, overBudget+"  auto_demote: false\n", record, checkTime.Add(day+time.Hour))
	if kept := record.kept[RulePullRequestBudget]; !kept.Rearmed.Equal(checkTime.Add(day)) {
		t.Fatalf("a second verdict with demotion off moved the re-arm to %s", kept.Rearmed)
	}
	if off := (Demotion{Limit: decimal.MustParse("5"), Off: true}).Record(checkTime, ledgerOverrides(10, 3, 2)); !off.From.IsZero() {
		t.Fatalf("a record started a term with demotion off: %+v", off)
	}
	again := ledgerVerdict(t, overBudget+"  auto_demote: true\n", record, checkTime.Add(2*day))
	if got := result(t, again, RulePullRequestBudget); got.Outcome != Fail || got.Demoted != nil {
		t.Fatalf("turned on again, the rule was demoted on a record from before: %+v", got)
	}
	if asked := record.asked[len(record.asked)-1]; !asked.Equal(checkTime.Add(2 * day)) {
		t.Fatalf("turned on again, the window opened at %s, want the verdict's time", asked)
	}

	quiet := newMemoryRecord(overridden)
	within := ledgerVerdict(t, "policy:\n  pr_budget_usd: \"50\"\n  enforce: true\n", quiet, checkTime)
	if quiet.reads != 0 || len(quiet.asked) != 0 || len(quiet.failures) != 0 || result(t, within, RulePullRequestBudget).Outcome == Fail {
		t.Fatalf("a rule within its budget read its record: %d reads, %d windows, %v", quiet.reads, len(quiet.asked), quiet.failures)
	}

	for document, off := range map[string]bool{
		"policy:\n  auto_demote: false\n": true, "policy:\n  auto_demote: true\n": false, "policy:\n  enforce: true\n": false,
	} {
		settings, err := ParseSettings([]byte(document))
		if err != nil || settings.DemotionOff != off {
			t.Errorf("%q: off %t, %v", document, settings.DemotionOff, err)
		}
	}
	for _, document := range []string{"policy:\n  auto_demote: \"false\"\n", "policy:\n  auto_demote: 0\n", "policy:\n  auto_demote: [false]\n"} {
		if _, err := ParseSettings([]byte(document)); err == nil {
			t.Errorf("%q was read", document)
		}
	}
}

// TestADemotedRuleNeverConcludesSuccess demotes the one rule that fails on
// rows whose every other rule passes. The demoted rule warns with the reason
// it failed with, and the verdict concludes neutral, never success. Beside
// another rule that fails, it concludes failure, and that rule still fails.
func TestADemotedRuleNeverConcludesSuccess(t *testing.T) {
	lines := []LedgerLine{{Currency: "USD", ListCost: mustAmount(t, "1"), Rows: 1, Model: "claude-opus-5-5", Verified: true, Attributed: true}}
	verdict := func(settings string) Verdict {
		t.Helper()
		got, err := EvaluateLedger(context.Background(), LedgerRequest{
			Lines: lines, Evidence: GatewayMeasured, MeasuredUSD: mustAmount(t, "10"),
			Config: config(settings), At: checkTime, Record: newMemoryRecord(overridden),
		})
		if err != nil {
			t.Fatal(err)
		}
		return got
	}
	alone := verdict(overBudget + "  models: [\"claude-*\"]\n")
	demoted := result(t, alone, RulePullRequestBudget)
	if result(t, alone, RuleModelAllowlist).Outcome != Pass || demoted.Outcome != Warn || demoted.Reason != ReasonOverLimit {
		t.Fatalf("the rules beside the demoted one: %+v", alone.Results)
	}
	want := Demoted{Reason: DemotedByOverrideRate, Approved: 3, Failing: 10, Until: checkTime.Add(DemotionTerm).Format(time.RFC3339)}
	if demoted.Demoted == nil || *demoted.Demoted != want || demoted.HeldByWarnOnly {
		t.Fatalf("the demoted result: %+v", demoted)
	}
	if alone.Conclusion != Neutral {
		t.Fatalf("every other rule passed and one is demoted: the verdict concludes %s", alone.Conclusion)
	}
	beside := verdict(overBudget + "  models: [\"gpt-*\"]\n")
	if beside.Conclusion != Failure || result(t, beside, RuleModelAllowlist).Outcome != Fail || result(t, beside, RulePullRequestBudget).Demoted == nil {
		t.Fatalf("a demoted rule beside one that fails: %+v", beside)
	}
}

// TestOnlyTheSixCostRulesAreDemoted holds the step that decides a demotion to
// the six rules that hold a cost to a limit: each of them, failing under a
// record that demotes it, warns, and any other rule of Costroid's, a pack's
// own rule and a rule Costroid does not know keeps its failure. On a ledger's
// verdict a model off the list still fails beside a demoted budget.
func TestOnlyTheSixCostRulesAreDemoted(t *testing.T) {
	running := Demotion{Limit: decimal.MustParse("5")}.Record(checkTime, ledgerOverrides(10, 3, 2))
	six := []string{RuleRunBudget, RuleRunCostThreshold, RuleRunIncrease, RuleRunIncreasePercent, RuleRunP95, RulePullRequestBudget}
	for _, rule := range append(slices.Clone(reservedRules), "no_opus_on_docs", "team_rule") {
		got := demoted(Result{Rule: rule, Outcome: Fail, Reason: ReasonOverLimit}, running, checkTime)
		if lowered := got.Demoted != nil || got.Outcome != Fail; lowered != slices.Contains(six, rule) {
			t.Errorf("%s: lowered %t: %+v", rule, lowered, got)
		}
	}
	record := newMemoryRecord(overridden)
	verdict := ledgerVerdict(t, overBudget+"  models: [\"gpt-*\"]\n", record, checkTime)
	if models := result(t, verdict, RuleModelAllowlist); models.Outcome != Fail || models.Demoted != nil {
		t.Fatalf("the model list was demoted: %+v", models)
	}
	if !slices.Equal(record.failures, []string{RulePullRequestBudget + " 5"}) {
		t.Fatalf("the record holds %v, want the budget's failure alone", record.failures)
	}
	for limit, rule := range demotionLimits {
		if !slices.Contains(six, limit) || !Demotable(limit) || !slices.Contains(Keys(), rule) {
			t.Errorf("%s is held to %s", limit, rule)
		}
	}
	if len(demotionLimits) != len(six) {
		t.Errorf("%d rules have a limit a record counts under, want %d", len(demotionLimits), len(six))
	}
}

// TestAFailureTheWarnOnlyPeriodHeldIsNeitherRecordedNorDemoted holds a rule
// the warn-only period held to a warning apart from the record: nothing failed
// the check, so there was nothing to approve. Its result keeps its own words
// and carries no demotion, and a result that passed or could not be decided
// is never lowered either.
func TestAFailureTheWarnOnlyPeriodHeldIsNeitherRecordedNorDemoted(t *testing.T) {
	record := newMemoryRecord(overridden)
	verdict := ledgerVerdict(t, "policy:\n  pr_budget_usd: \"5\"\n", record, checkTime)
	got := result(t, verdict, RulePullRequestBudget)
	if got.Outcome != Warn || !got.HeldByWarnOnly || got.Demoted != nil || len(record.failures) != 0 {
		t.Fatalf("a held failure: %+v, recorded %v", got, record.failures)
	}
	running := Demotion{Limit: decimal.MustParse("5")}.Record(checkTime, ledgerOverrides(10, 3, 2))
	for _, kept := range []Result{
		{Rule: RulePullRequestBudget, Outcome: Warn, Reason: ReasonOverLimit, HeldByWarnOnly: true},
		{Rule: RulePullRequestBudget, Outcome: Pass, Reason: ReasonWithinLimit},
		{Rule: RulePullRequestBudget, Outcome: Unproven, Reason: ReasonUnmeasuredSpend},
	} {
		if lowered := demoted(kept, running, checkTime); lowered.Demoted != nil || lowered.Outcome != kept.Outcome {
			t.Errorf("%s %s became %+v", kept.Outcome, kept.Reason, lowered)
		}
	}
}

// TestWithoutALedgerNoRuleIsDemoted holds every path that has no ledger's own
// record to the rule as written. Request, which the report, costroid policy
// and the fork's follow-up evaluate, has no field a record can reach the rule
// through, and the verdict it gives fails. A ledger's verdict without a record
// fails too, and so does one whose record a ledger did not count: a record of
// the same counts built anywhere else never demotes.
func TestWithoutALedgerNoRuleIsDemoted(t *testing.T) {
	carriers := []reflect.Type{reflect.TypeFor[OverrideLedger](), reflect.TypeFor[Overrides](), reflect.TypeFor[Demotion]()}
	var walk func(kind reflect.Type, path string)
	seen := map[reflect.Type]bool{}
	walk = func(kind reflect.Type, path string) {
		if seen[kind] {
			return
		}
		seen[kind] = true
		if slices.Contains(carriers, kind) {
			t.Errorf("%s can carry an override record", path)
		}
		switch kind.Kind() {
		case reflect.Pointer, reflect.Slice, reflect.Array, reflect.Map:
			walk(kind.Elem(), path)
		case reflect.Struct:
			for index := range kind.NumField() {
				walk(kind.Field(index).Type, path+"."+kind.Field(index).Name)
			}
		}
	}
	walk(reflect.TypeFor[Request](), "Request")

	usage := Usage{Rows: 1, Currencies: []CurrencyUsage{{Currency: "USD", ListCost: mustAmount(t, "10"), Rows: 1, Models: map[string]int{"claude-opus-5-5": 1}}}}
	report := evaluate(t, Request{Usage: usage, Config: config("policy:\n  run_budget_usd: \"5\"\n  enforce: true\n")})
	if got := result(t, report, RuleRunBudget); got.Outcome != Fail || got.Demoted != nil || report.Conclusion != Failure {
		t.Fatalf("the report's verdict: %+v", report)
	}
	if served := ledgerVerdict(t, overBudget, nil, checkTime); served.Conclusion != Failure || result(t, served, RulePullRequestBudget).Demoted != nil {
		t.Fatalf("a ledger's verdict without a record: %+v", served)
	}
	claimed := Demotion{Limit: decimal.MustParse("5"), From: checkTime, Crossed: Overrides{Failing: 10, Approved: 10, Approvers: 9}}
	if got := demoted(Result{Rule: RulePullRequestBudget, Outcome: Fail, Reason: ReasonOverLimit}, claimed, checkTime); got.Demoted != nil {
		t.Fatalf("a demotion whose record no ledger counted lowered a rule: %+v", got)
	}
}
