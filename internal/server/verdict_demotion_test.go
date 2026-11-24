package server

import (
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Costroid/costroid/internal/decimal"
	"github.com/Costroid/costroid/internal/policy"
	"github.com/Costroid/costroid/internal/scm"
	"github.com/Costroid/costroid/internal/scm/scmtest"
	"github.com/Costroid/costroid/internal/store"
	"github.com/Costroid/costroid/internal/store/storetest"
)

// widgetsRepo is example/widgets as a ledger names it.
const widgetsRepo = "github.com/" + approvalRepo

// budgetOfFive holds a pull request's runs to USD 5, enforced.
const budgetOfFive = "policy:\n  pr_budget_usd: \"5\"\n  enforce: true\n"

// memoryOverrides is the override and demotion records without Postgres,
// counted as the store's query counts them.
type memoryOverrides struct {
	mu        sync.Mutex
	failures  map[store.TenantID][]store.FailingVerdict
	approvals map[store.TenantID][]store.PullRequestApproval
	demotions map[store.TenantID][]store.RuleDemotion
}

func newMemoryOverrides() *memoryOverrides {
	return &memoryOverrides{
		failures:  map[store.TenantID][]store.FailingVerdict{},
		approvals: map[store.TenantID][]store.PullRequestApproval{},
		demotions: map[store.TenantID][]store.RuleDemotion{},
	}
}

func (m *memoryOverrides) AddFailure(_ context.Context, tenant store.TenantID, verdict store.FailingVerdict) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.failures[tenant] = append(m.failures[tenant], verdict)
	return nil
}

func (m *memoryOverrides) AddApproval(_ context.Context, tenant store.TenantID, approval store.PullRequestApproval) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.approvals[tenant] = append(m.approvals[tenant], approval)
	return nil
}

func (m *memoryOverrides) Overrides(_ context.Context, tenant store.TenantID, query store.OverrideQuery) (store.OverrideCount, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	opens := query.At.Add(-store.OverrideWindowDays * 24 * time.Hour)
	if query.Rearmed.After(opens) {
		opens = query.Rearmed
	}
	latest, heads := map[string]time.Time{}, map[string][]string{}
	for _, failed := range m.failures[tenant] {
		if failed.Repository != query.Repository || failed.BaseBranch != query.BaseBranch || failed.Rule != query.Rule || failed.Limit.Cmp(query.Limit) != 0 ||
			failed.PullRequest == query.PullRequest || failed.At.Before(opens) || failed.At.After(query.At) {
			continue
		}
		if failed.At.After(latest[failed.PullRequest]) {
			latest[failed.PullRequest] = failed.At
		}
		heads[failed.PullRequest] = append(heads[failed.PullRequest], failed.HeadSHA)
	}
	pulls := make([]string, 0, len(latest))
	for pull := range latest {
		pulls = append(pulls, pull)
	}
	slices.SortFunc(pulls, func(a, b string) int {
		return cmp.Or(latest[b].Compare(latest[a]), strings.Compare(b, a))
	})
	pulls = pulls[:min(len(pulls), store.OverrideWindowPullRequests)]
	approved, approvers := map[string]bool{}, map[string]bool{}
	for _, approval := range m.approvals[tenant] {
		if approval.Repository == query.Repository && slices.Contains(pulls, approval.PullRequest) && !approval.At.Before(opens) &&
			!approval.At.After(query.At) && slices.Contains(heads[approval.PullRequest], approval.HeadSHA) {
			approved[approval.PullRequest], approvers[strings.ToLower(approval.Approver)] = true, true
		}
	}
	return store.OverrideCount{Failing: len(pulls), Approved: len(approved), Approvers: len(approvers)}, nil
}

func (m *memoryOverrides) Demotion(_ context.Context, tenant store.TenantID, repository, branch, rule string) (store.RuleDemotion, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.latest(tenant, repository, branch, rule)
}

func (m *memoryOverrides) latest(tenant store.TenantID, repository, branch, rule string) (store.RuleDemotion, bool, error) {
	var kept []store.RuleDemotion
	for _, demotion := range m.demotions[tenant] {
		if demotion.Repository == repository && demotion.BaseBranch == branch && demotion.Rule == rule {
			kept = append(kept, demotion)
		}
	}
	if len(kept) == 0 {
		return store.RuleDemotion{}, false, nil
	}
	return slices.MaxFunc(kept, func(a, b store.RuleDemotion) int {
		return cmp.Or(a.Rearmed.Compare(b.Rearmed), cmp.Compare(btoi(!a.From.IsZero()), btoi(!b.From.IsZero())))
	}), true, nil
}

func btoi(value bool) int {
	if value {
		return 1
	}
	return 0
}

func (m *memoryOverrides) KeepDemotion(_ context.Context, tenant store.TenantID, demotion store.RuleDemotion) (store.RuleDemotion, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !slices.ContainsFunc(m.demotions[tenant], func(kept store.RuleDemotion) bool {
		return kept.Repository == demotion.Repository && kept.BaseBranch == demotion.BaseBranch && kept.Rule == demotion.Rule && kept.Rearmed.Equal(demotion.Rearmed) &&
			kept.From.IsZero() == demotion.From.IsZero()
	}) {
		m.demotions[tenant] = append(m.demotions[tenant], demotion)
	}
	kept, _, err := m.latest(tenant, demotion.Repository, demotion.BaseBranch, demotion.Rule)
	return kept, err
}

// failuresOf are the failing verdicts the record holds for a pull request.
func (m *memoryOverrides) failuresOf(tenant store.TenantID, pull string) []store.FailingVerdict {
	m.mu.Lock()
	defer m.mu.Unlock()
	return slices.DeleteFunc(slices.Clone(m.failures[tenant]), func(failed store.FailingVerdict) bool { return failed.PullRequest != pull })
}

func (m *memoryOverrides) approvalsOf(tenant store.TenantID) []store.PullRequestApproval {
	m.mu.Lock()
	defer m.mu.Unlock()
	return slices.Clone(m.approvals[tenant])
}

// demotionLedger is a ledger whose every pull request's rows cost USD 10,
// unchecked, beside an override record.
type demotionLedger struct {
	Ledger
	overrideStore
}

func (demotionLedger) PullRequest(context.Context, store.TenantID, string, string) ([]store.Line, error) {
	return []store.Line{{Currency: "USD", ListCost: decimal.MustParse("10"), Rows: 1, Model: "claude-opus-5-5", BudgetEligible: true}}, nil
}

// demotionWorld is serve reaching example/widgets as the App's installation
// 58, whose costroid.yml the stub holds at every commit, over a ledger whose
// owner example and ingest key are one tenant's. Its clock is now.
type demotionWorld struct {
	*world
	stub   *scmtest.Stub
	hook   *hook
	keeper overrideStore
	tenant store.TenantID
	now    time.Time
}

func newDemotionWorld(t *testing.T, ledger Ledger, keeper overrideStore, tenant store.TenantID) *demotionWorld {
	t.Helper()
	stub := approvalStub(t)
	stub.Files["costroid.yml"] = budgetOfFive
	stub.BaseBranch = "main"
	installations := newMemoryInstallations()
	if err := installations.AddRepositories(context.Background(), installationID, "example", []store.Repository{widgets}); err != nil {
		t.Fatal(err)
	}
	w := &demotionWorld{world: newWorld(t, demotionLedger{Ledger: ledger, overrideStore: keeper}), stub: stub, keeper: keeper, tenant: tenant, now: testNow}
	config := w.server.Config
	config.Ledger = demotionLedger{Ledger: ledger, overrideStore: keeper}
	config.App = &GitHubApp{Secret: []byte(webhookSecretText), Installations: installations, Hosts: newAppFront(t, stub).hosts(t)}
	config.Logger = slog.New(slog.DiscardHandler)
	config.Now = func() time.Time { return w.now }
	w.server = New(config)
	w.http = httptest.NewServer(w.server.Handler())
	t.Cleanup(w.http.Close)
	w.hook = &hook{server: w.server, handler: w.server.Handler(), installations: installations, log: &lockedBuffer{}}
	return w
}

// memoryDemotionWorld is a demotionWorld on the memory ledger and record.
func memoryDemotionWorld(t *testing.T) *demotionWorld {
	t.Helper()
	ledger := newMemoryLedger()
	tenant := tenantID(t, tenantA)
	ledger.owners["github.com:example"] = tenant
	ledger.keys[store.KeyDigest(acmeKey)] = tenant
	return newDemotionWorld(t, ledger, newMemoryOverrides(), tenant)
}

// postgresDemotionWorld is a demotionWorld on Postgres.
func postgresDemotionWorld(t *testing.T) *demotionWorld {
	t.Helper()
	db := storetest.New(t)
	tenant := db.Tenant(t, "example", []store.Binding{{Host: "github.com", Owner: "example"}}, store.KeyDigest(acmeKey))
	ledger, err := store.Open(context.Background(), db.AppURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(ledger.Close)
	return newDemotionWorld(t, ledger, storeOverrides{OverrideRecord: ledger.OverrideRecord(), demotions: ledger.DemotionRecord()}, tenant)
}

// onBothLedgers runs a test on the memory record and on Postgres.
func onBothLedgers(t *testing.T, test func(*testing.T, *demotionWorld)) {
	t.Run("in memory", func(t *testing.T) { test(t, memoryDemotionWorld(t)) })
	t.Run("on Postgres", func(t *testing.T) { test(t, postgresDemotionWorld(t)) })
}

// ask asks for a pull request's verdict at w.now, stating config, through the
// installation unless fields say otherwise.
func (w *demotionWorld) ask(t *testing.T, pull int, config string, fields map[string]any) policy.Verdict {
	t.Helper()
	return w.answer(t, pull, config, fields).Policy
}

// answer is the service's whole answer to ask.
func (w *demotionWorld) answer(t *testing.T, pull int, config string, fields map[string]any) verdictAnswer {
	t.Helper()
	if fields == nil {
		fields = map[string]any{"installation": installationID}
	}
	got := w.send(http.MethodPost, fmt.Sprintf("/v1/pulls/%d/verdict?repo=%s", pull, widgetsRepo), acmeKey, jsonType, verdictBody(t, config, fields))
	var answer verdictAnswer
	if got.status != http.StatusOK || json.Unmarshal(got.body, &answer) != nil {
		t.Fatalf("answered %d: %s", got.status, got.body)
	}
	return answer
}

// seed records failing verdicts of pr_budget under limit on pull requests
// from first on, at, each on one head, and an approval of each of the first
// approved by an account of accounts in turn.
func (w *demotionWorld) seed(t *testing.T, first, failing, approved int, limit string, at time.Time, accounts ...string) {
	t.Helper()
	ctx := context.Background()
	for index := range failing {
		pull := first + index
		head := fmt.Sprintf("%040x", pull)
		verdict := store.FailingVerdict{Repository: widgetsRepo, BaseBranch: w.stub.BaseBranch, PullRequest: fmt.Sprint(pull), HeadSHA: head, Rule: policy.RulePullRequestBudget, Limit: decimal.MustParse(limit), At: at}
		if err := w.keeper.AddFailure(ctx, w.tenant, verdict); err != nil {
			t.Fatal(err)
		}
		if index >= approved {
			continue
		}
		approval := store.PullRequestApproval{Repository: widgetsRepo, PullRequest: fmt.Sprint(pull), HeadSHA: head, Approver: accounts[index%len(accounts)], At: at.Add(time.Minute)}
		if err := w.keeper.AddApproval(ctx, w.tenant, approval); err != nil {
			t.Fatal(err)
		}
	}
}

// budgetResult is the verdict's pr_budget result.
func budgetResult(t *testing.T, verdict policy.Verdict) policy.Result {
	t.Helper()
	return ruleOf(t, verdict, policy.RulePullRequestBudget)
}

func requireDemoted(t *testing.T, verdict policy.Verdict, approved, failing int, until time.Time) {
	t.Helper()
	got := budgetResult(t, verdict)
	want := policy.Demoted{Reason: policy.DemotedByOverrideRate, Approved: approved, Failing: failing, Until: until.UTC().Format(time.RFC3339)}
	if got.Outcome != policy.Warn || got.Reason != policy.ReasonOverLimit || got.Demoted == nil || *got.Demoted != want || verdict.Conclusion != policy.Neutral {
		t.Fatalf("want demoted by %+v, got %+v concluding %s", want, got, verdict.Conclusion)
	}
}

func requireFails(t *testing.T, verdict policy.Verdict, why string) {
	t.Helper()
	if got := budgetResult(t, verdict); got.Outcome != policy.Fail || got.Demoted != nil || verdict.Conclusion != policy.Failure {
		t.Fatalf("%s: %+v concluding %s", why, got, verdict.Conclusion)
	}
}

// press presses the approve button on pull request 7's head as sender, and
// applies the delivery.
func (w *demotionWorld) press(t *testing.T, delivery, sender, head string) {
	t.Helper()
	w.hook.send(t, scm.EventCheckRun, delivery, pressBody(sender, head, scm.ApproveAction), http.StatusAccepted)
	w.hook.settle(t)
}

// TestOverrideDemotionNeverCountsThePullRequestItself records, through serve,
// pull request 7 failing its budget and a reviewer approving it with the
// button, beside 10 other pull requests the rule failed, 2 of them approved by
// 2 accounts. Counted with its own failure and approval, 3 of 11 would demote
// the rule. Its own verdict leaves itself out, 2 of 10, and fails. The verdict
// on pull request 8, which counts 7, is demoted by 3 of 11.
func TestOverrideDemotionNeverCountsThePullRequestItself(t *testing.T) {
	onBothLedgers(t, func(t *testing.T, w *demotionWorld) {
		w.seed(t, 101, 10, 2, "5", testNow.Add(-48*time.Hour), "lead-one", "lead-two")
		w.now = testNow.Add(-24 * time.Hour)
		requireFails(t, w.ask(t, 7, budgetOfFive, nil), "the first verdict on 7")
		w.press(t, "press-7", approverLogin, acmeSHA)
		w.now = testNow
		requireFails(t, w.ask(t, 7, budgetOfFive, nil), "7 counted its own failure and approval")
		requireDemoted(t, w.ask(t, 8, budgetOfFive, nil), 3, 11, testNow.Add(policy.DemotionTerm))
	})
}

// TestADemotionEndsAndRearms demotes pull request 7's budget on a record of 3
// of 10, then holds the demotion to its term. A later verdict whose record
// still demotes leaves the term where it started. It does not flap: 10 more
// failing pull requests that take the window to 3 of 20 leave it standing,
// with the until and the counts of the verdict that started it. At until the
// rule is re-armed and fails, its window empty. A changed limit re-arms it at
// once, and so does a change back.
func TestADemotionEndsAndRearms(t *testing.T) {
	day := 24 * time.Hour
	until := testNow.Add(policy.DemotionTerm)
	t.Run("at the end of its term", func(t *testing.T) {
		onBothLedgers(t, func(t *testing.T, w *demotionWorld) {
			w.seed(t, 101, 10, 3, "5", testNow.Add(-48*time.Hour), "lead-one", "lead-two")
			requireDemoted(t, w.ask(t, 7, budgetOfFive, nil), 3, 10, until)
			w.now = testNow.Add(12 * time.Hour)
			requireDemoted(t, w.ask(t, 7, budgetOfFive, nil), 3, 10, until)
			w.seed(t, 201, 10, 0, "5", testNow.Add(day))
			w.now = testNow.Add(29 * day)
			requireDemoted(t, w.ask(t, 7, budgetOfFive, nil), 3, 10, until)
			w.now = until.Add(-time.Second)
			requireDemoted(t, w.ask(t, 7, budgetOfFive, nil), 3, 10, until)
			w.now = until
			requireFails(t, w.ask(t, 7, budgetOfFive, nil), "at its until")
			w.seed(t, 301, 2, 2, "5", until.Add(-time.Minute), "lead-one", "lead-two")
			w.now = until.Add(day)
			requireFails(t, w.ask(t, 7, budgetOfFive, nil), "approvals before the re-arm counted after it")
		})
	})
	t.Run("when its limit changes", func(t *testing.T) {
		onBothLedgers(t, func(t *testing.T, w *demotionWorld) {
			w.seed(t, 101, 10, 3, "5", testNow.Add(-48*time.Hour), "lead-one", "lead-two")
			requireDemoted(t, w.ask(t, 7, budgetOfFive, nil), 3, 10, until)
			six := strings.Replace(budgetOfFive, `"5"`, `"6"`, 1)
			w.stub.Files["costroid.yml"] = six
			w.seed(t, 101, 10, 3, "6", testNow.Add(time.Hour), "lead-one", "lead-two")
			w.now = testNow.Add(day)
			requireFails(t, w.ask(t, 7, six, nil), "a changed limit")
			w.stub.Files["costroid.yml"] = budgetOfFive
			w.now = testNow.Add(2 * day)
			requireFails(t, w.ask(t, 7, budgetOfFive, nil), "the limit changed back")
			w.stub.Files["costroid.yml"] = budgetOfFive + "  auto_demote: false\n"
			w.now = testNow.Add(3 * day)
			requireFails(t, w.ask(t, 7, budgetOfFive+"  auto_demote: false\n", nil), "demotion turned off")
		})
	})
}

// TestAVerdictOnAStatedConfigIsNotRecorded asks for pull request 7's verdict
// beside a record that demotes its budget. Only where serve read costroid.yml
// at config_ref itself, through the App's installation, found the bytes the
// caller stated, and read the pull request's head from the host, is the
// failure recorded, on that head, and the rule demoted. Every other verdict is
// answered as written and leaves the record as it was.
func TestAVerdictOnAStatedConfigIsNotRecorded(t *testing.T) {
	for name, tc := range map[string]struct {
		fields map[string]any
		setup  func(*scmtest.Stub)
		why    overrideReach
	}{
		"no installation":                    {fields: map[string]any{}, why: reachConfigUnread},
		"an installation it does not hold":   {fields: map[string]any{"installation": installationID + 1}, why: reachConfigUnread},
		"another costroid.yml than the base": {setup: func(s *scmtest.Stub) { s.Files["costroid.yml"] = budgetOfFive + "  models: [\"claude-*\"]\n" }, why: reachConfigUnread},
		"no costroid.yml at the base":        {setup: func(s *scmtest.Stub) { delete(s.Files, "costroid.yml") }, why: reachConfigUnread},
		"a base the host did not answer":     {setup: func(s *scmtest.Stub) { s.Fail["GET /contents/"] = http.StatusBadGateway }, why: reachConfigUnread},
		"a head the host did not answer":     {setup: func(s *scmtest.Stub) { s.Fail["GET /repos/example/widgets/pulls/"] = http.StatusBadGateway }, why: reachPullUnread},
		"a head that is no commit":           {setup: func(s *scmtest.Stub) { s.Head = "main" }, why: reachPullUnread},
	} {
		t.Run(name, func(t *testing.T) {
			w := memoryDemotionWorld(t)
			w.seed(t, 101, 10, 3, "5", testNow.Add(-48*time.Hour), "lead-one", "lead-two")
			if tc.setup != nil {
				tc.setup(w.stub)
			}
			answer := w.answer(t, 7, budgetOfFive, tc.fields)
			requireFails(t, answer.Policy, name)
			if answer.OverrideRecord != tc.why {
				t.Fatalf("the answer says %q, want %q", answer.OverrideRecord, tc.why)
			}
			if recorded := w.keeper.(*memoryOverrides).failuresOf(w.tenant, "7"); len(recorded) != 0 {
				t.Fatalf("a verdict serve did not read for itself was recorded: %+v", recorded)
			}
		})
	}
	w := memoryDemotionWorld(t)
	w.seed(t, 101, 10, 3, "5", testNow.Add(-48*time.Hour), "lead-one", "lead-two")
	reached := w.answer(t, 7, budgetOfFive, nil)
	requireDemoted(t, reached.Policy, 3, 10, testNow.Add(policy.DemotionTerm))
	if reached.OverrideRecord != reachRecorded {
		t.Fatalf("the verdict serve read for itself says %q", reached.OverrideRecord)
	}
	want := store.FailingVerdict{Repository: widgetsRepo, BaseBranch: "main", PullRequest: "7", HeadSHA: acmeSHA, Rule: policy.RulePullRequestBudget, Limit: decimal.MustParse("5"), At: testNow}
	recorded := w.keeper.(*memoryOverrides).failuresOf(w.tenant, "7")
	if len(recorded) != 1 || recorded[0].HeadSHA != want.HeadSHA || recorded[0].Limit.Cmp(want.Limit) != 0 ||
		recorded[0].Repository != want.Repository || recorded[0].BaseBranch != want.BaseBranch || recorded[0].Rule != want.Rule || !recorded[0].At.Equal(want.At) {
		t.Fatalf("the verdict serve read for itself recorded %+v, want %+v", recorded, want)
	}
}

// TestAnApprovalIsRecordedOnlyWhereTheHostConfirmedIt presses the approve
// button on pull request 7. A press the host confirmed, the head current and
// the approver's write role answered, is recorded once, on the head the host
// named, with the repository as a ledger names it. A refused press and a
// press the host did not answer record nothing, and a verdict request cannot
// state an approval: its body refuses any field it does not read.
func TestAnApprovalIsRecordedOnlyWhereTheHostConfirmedIt(t *testing.T) {
	for name, tc := range map[string]struct {
		sender, head string
		setup        func(*scmtest.Stub)
	}{
		"the author":                     {sender: authorLogin},
		"a reader":                       {setup: func(s *scmtest.Stub) { s.Permission = "read" }},
		"an App's account":               {sender: "helper[bot]"},
		"a moved head":                   {head: movedHead},
		"a role the host did not answer": {setup: func(s *scmtest.Stub) { s.Fail["GET /collaborators/"] = http.StatusBadGateway }},
		"a head the host did not answer": {setup: func(s *scmtest.Stub) { s.Fail["GET /repos/example/widgets/pulls/"] = http.StatusBadGateway }},
	} {
		t.Run(name, func(t *testing.T) {
			w := memoryDemotionWorld(t)
			if tc.setup != nil {
				tc.setup(w.stub)
			}
			w.press(t, "press", cmp.Or(tc.sender, approverLogin), cmp.Or(tc.head, acmeSHA))
			if recorded := w.keeper.(*memoryOverrides).approvalsOf(w.tenant); len(recorded) != 0 {
				t.Fatalf("a press the host did not confirm was recorded: %+v", recorded)
			}
		})
	}
	w := memoryDemotionWorld(t)
	w.now = testNow.Add(time.Hour)
	w.press(t, "press", approverLogin, acmeSHA)
	want := store.PullRequestApproval{Repository: widgetsRepo, PullRequest: "7", HeadSHA: acmeSHA, Approver: approverLogin, At: testNow.Add(time.Hour)}
	if recorded := w.keeper.(*memoryOverrides).approvalsOf(w.tenant); len(recorded) != 1 || recorded[0] != want {
		t.Fatalf("the press the host confirmed recorded %+v, want %+v", recorded, want)
	}
	if posted := posted(w.stub); len(posted) != 1 {
		t.Fatalf("the press posted %d checks", len(posted))
	}
	stated := w.send(http.MethodPost, "/v1/pulls/7/verdict?repo="+widgetsRepo, acmeKey, jsonType,
		verdictBody(t, budgetOfFive, map[string]any{"installation": installationID, "approvals": []map[string]string{{"approver": "lead-one", "head_sha": acmeSHA}}}))
	if stated.status != http.StatusBadRequest || stated.code(t) != codeMalformed || len(w.keeper.(*memoryOverrides).approvalsOf(w.tenant)) != 1 {
		t.Fatalf("a verdict request that states an approval answered %d %s", stated.status, stated.body)
	}
}

// notBaseCommits are commits a caller can state as config_ref that are not
// the head of the pull request's base branch: the pull request's own head,
// any other commit, and a base branch the host answers at another commit.
func notBaseCommits() map[string]struct {
	ref   string
	setup func(*scmtest.Stub)
} {
	return map[string]struct {
		ref   string
		setup func(*scmtest.Stub)
	}{
		"the pull request's own head": {ref: acmeSHA},
		"another commit":              {ref: strings.Repeat("2", 40)},
		"a base branch that moved":    {ref: approvalBase, setup: func(s *scmtest.Stub) { s.Branches["main"] = movedHead }},
	}
}

// TestAVerdictAtACommitThatIsNotTheBaseIsNotRecorded asks for pull request
// 7's verdict with a config_ref that is not the head of its base branch, as
// GitHub names the branch, with the very bytes the service reads there. The
// verdict is answered, its failure is not recorded, and the answer says
// not_base. A base branch the host does not answer records nothing either,
// and says pull_unread.
func TestAVerdictAtACommitThatIsNotTheBaseIsNotRecorded(t *testing.T) {
	for name, tc := range notBaseCommits() {
		t.Run(name, func(t *testing.T) {
			w := memoryDemotionWorld(t)
			if tc.setup != nil {
				tc.setup(w.stub)
			}
			answer := w.answer(t, 7, budgetOfFive, map[string]any{"installation": installationID, "config_ref": tc.ref})
			if budgetResult(t, answer.Policy).Outcome != policy.Fail || answer.OverrideRecord != reachNotBase {
				t.Fatalf("answered %+v, saying %q", budgetResult(t, answer.Policy), answer.OverrideRecord)
			}
			if recorded := w.keeper.(*memoryOverrides).failuresOf(w.tenant, "7"); len(recorded) != 0 {
				t.Fatalf("a verdict at a commit that is not the base was recorded: %+v", recorded)
			}
		})
	}
	w := memoryDemotionWorld(t)
	w.stub.Fail["GET /repos/example/widgets/git/ref/heads/"] = http.StatusBadGateway
	if answer := w.answer(t, 7, budgetOfFive, nil); answer.OverrideRecord != reachPullUnread || len(w.keeper.(*memoryOverrides).failuresOf(w.tenant, "7")) != 0 {
		t.Fatalf("with the base branch unread the answer says %q", answer.OverrideRecord)
	}
}

// TestAVerdictAtACommitThatIsNotTheBaseDemotesNothing demotes pull request
// 7's budget at its base, then asks again at a commit that is not the base,
// on memory and on Postgres. The rule fails as written there, though its term
// runs, and a verdict at the base is still demoted.
func TestAVerdictAtACommitThatIsNotTheBaseDemotesNothing(t *testing.T) {
	for name, tc := range notBaseCommits() {
		t.Run(name, func(t *testing.T) {
			onBothLedgers(t, func(t *testing.T, w *demotionWorld) {
				w.seed(t, 101, 10, 3, "5", testNow.Add(-48*time.Hour), "lead-one", "lead-two")
				requireDemoted(t, w.ask(t, 7, budgetOfFive, nil), 3, 10, testNow.Add(policy.DemotionTerm))
				w.now = testNow.Add(time.Hour)
				if tc.setup != nil {
					tc.setup(w.stub)
				}
				answer := w.answer(t, 7, budgetOfFive, map[string]any{"installation": installationID, "config_ref": tc.ref})
				requireFails(t, answer.Policy, "a verdict at a commit that is not the base")
				if answer.OverrideRecord != reachNotBase {
					t.Fatalf("the answer says %q", answer.OverrideRecord)
				}
			})
		})
	}
}

// attestedSpend is a ledger's gateway figures: every eligible row of each pull
// request is on a key whose count a protected run attested, and the count is
// usd.
type attestedSpend struct {
	usd string
}

func (attestedSpend) AttestSpend(context.Context, store.TenantID, store.AttestedSpend) error {
	return nil
}

func (a attestedSpend) GatewaySpend(context.Context, store.TenantID, string, string) (int64, decimal.Decimal, error) {
	return 1, decimal.MustParse(a.usd), nil
}

// attestedLedger is a demotion ledger whose pull requests' rows a CI token
// proved, graded exact, costing USD 10.
type attestedLedger struct {
	demotionLedger
}

func (attestedLedger) PullRequest(context.Context, store.TenantID, string, string) ([]store.Line, error) {
	return []store.Line{{
		Currency: "USD", ListCost: decimal.MustParse("10"), Rows: 1, Model: "claude-opus-5-5", BudgetEligible: true,
		RunIdentity: identityToken, AttributionGrade: gradeExact,
	}}, nil
}

// TestAtTheServiceOnlyThePullRequestBudgetIsDemoted sets every rule of the
// six that holds a USD amount to 5, over rows a CI token proved and a gateway
// count a protected run attested at USD 12. The service holds the pull
// request's budget to that count: it fails, its failure is recorded, and a
// record of 3 in 10 demotes it. Every run rule concludes unproven with
// no_run_rows, as each does at the service whatever the evidence, so none
// fails, none is recorded and none is demoted.
func TestAtTheServiceOnlyThePullRequestBudgetIsDemoted(t *testing.T) {
	w := memoryDemotionWorld(t)
	config := w.server.Config
	config.Ledger = attestedLedger{demotionLedger{Ledger: config.Ledger.(demotionLedger).Ledger, overrideStore: w.keeper}}
	config.Spend = attestedSpend{usd: "12"}
	w.server = New(config)
	w.http = httptest.NewServer(w.server.Handler())
	t.Cleanup(w.http.Close)
	settings := "policy:\n  run_budget_usd: \"5\"\n  max_run_cost_usd: \"5\"\n  pr_budget_usd: \"5\"\n  enforce: true\n"
	w.stub.Files["costroid.yml"] = settings
	w.seed(t, 101, 10, 3, "5", testNow.Add(-48*time.Hour), "lead-one", "lead-two")
	answer := w.answer(t, 7, settings, nil)
	if answer.Policy.Evidence != policy.GatewayMeasured || answer.Budget.GatewayUSD != "12" {
		t.Fatalf("the gateway's attested count does not stand behind the spend: %+v, %+v", answer.Policy.Evidence, answer.Budget)
	}
	requireDemoted(t, answer.Policy, 3, 10, testNow.Add(policy.DemotionTerm))
	if got := budgetResult(t, answer.Policy); got.CostUSD != "12" {
		t.Fatalf("the budget was not held to the gateway's count: %+v", got)
	}
	for _, rule := range []string{policy.RuleRunBudget, policy.RuleRunCostThreshold} {
		if got := ruleOf(t, answer.Policy, rule); got.Outcome != policy.Unproven || got.Reason != policy.ReasonNoRunRows || got.Demoted != nil {
			t.Errorf("%s at the service: %+v", rule, got)
		}
	}
	recorded := w.keeper.(*memoryOverrides).failuresOf(w.tenant, "7")
	if len(recorded) != 1 || recorded[0].Rule != policy.RulePullRequestBudget {
		t.Fatalf("the record holds %+v, want the budget's failure alone", recorded)
	}
}

// releaseHead is the head of the base branch release.
var releaseHead = strings.Repeat("3", 40)

// intoRelease makes pull requests merge into release, whose costroid.yml at
// its head is config, and returns the fields of a verdict request at that
// head.
func (w *demotionWorld) intoRelease(config string) map[string]any {
	w.stub.BaseBranch = "release"
	w.stub.Branches["release"] = releaseHead
	w.stub.FilesAt[releaseHead] = map[string]string{"costroid.yml": config}
	return map[string]any{"installation": installationID, "config_ref": releaseHead}
}

// intoMain makes pull requests merge into main again.
func (w *demotionWorld) intoMain() {
	w.stub.BaseBranch = "main"
}

// TestAFailureOnAnotherBaseBranchNeitherCountsNorRearms keeps the record of a
// rule at one base branch. Ten unapproved pull requests that failed into
// release do not dilute main's 3 of 10, so pull request 7 into main is
// demoted. A failure into release under release's own limit is recorded at
// release, and neither counts at main nor re-arms main's rule: pull request 7
// stays demoted with the until it had.
func TestAFailureOnAnotherBaseBranchNeitherCountsNorRearms(t *testing.T) {
	until := testNow.Add(policy.DemotionTerm)
	onBothLedgers(t, func(t *testing.T, w *demotionWorld) {
		w.seed(t, 101, 10, 3, "5", testNow.Add(-48*time.Hour), "lead-one", "lead-two")
		releaseConfig := strings.Replace(budgetOfFive, `"5"`, `"6"`, 1)
		fields := w.intoRelease(releaseConfig)
		w.seed(t, 201, 10, 0, "5", testNow.Add(-24*time.Hour))
		w.intoMain()
		requireDemoted(t, w.ask(t, 7, budgetOfFive, nil), 3, 10, until)
		w.now = testNow.Add(time.Hour)
		w.intoRelease(releaseConfig)
		released := w.answer(t, 8, releaseConfig, fields)
		requireFails(t, released.Policy, "a pull request into release")
		if released.OverrideRecord != reachRecorded {
			t.Fatalf("the verdict into release says %q", released.OverrideRecord)
		}
		w.intoMain()
		w.now = testNow.Add(2 * time.Hour)
		requireDemoted(t, w.ask(t, 7, budgetOfFive, nil), 3, 10, until)
	})
}

// TestDemotionOffOnAnotherBaseBranchLeavesThisOneStanding demotes pull
// request 7's budget at main, then answers a pull request into release, whose
// costroid.yml turns demotion off. That re-arms the rule at release alone:
// pull request 7 into main stays demoted with the until it had.
func TestDemotionOffOnAnotherBaseBranchLeavesThisOneStanding(t *testing.T) {
	until := testNow.Add(policy.DemotionTerm)
	onBothLedgers(t, func(t *testing.T, w *demotionWorld) {
		w.seed(t, 101, 10, 3, "5", testNow.Add(-48*time.Hour), "lead-one", "lead-two")
		requireDemoted(t, w.ask(t, 7, budgetOfFive, nil), 3, 10, until)
		w.now = testNow.Add(time.Hour)
		off := budgetOfFive + "  auto_demote: false\n"
		requireFails(t, w.answer(t, 8, off, w.intoRelease(off)).Policy, "a pull request into release with demotion off")
		w.intoMain()
		w.now = testNow.Add(2 * time.Hour)
		requireDemoted(t, w.ask(t, 7, budgetOfFive, nil), 3, 10, until)
	})
}
