package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Costroid/costroid/internal/policy"
	"github.com/Costroid/costroid/internal/store"
)

// A pull request's budget the gateway's USD 12 passes, and one it fails.
const (
	looseBudget = "policy:\n  pr_budget_usd: \"50\"\n  enforce: true\n"
	tightBudget = "policy:\n  pr_budget_usd: \"5\"\n  enforce: true\n"
)

// attestedWorld is a demotion world whose pull request 7 costs USD 10 on
// rows a CI token proved, graded exact, with USD 12 the gateway's attested
// count: a budget can pass there. base is costroid.yml at the head of the
// base branch, beside the pack that fails every run, and head is the file at
// the pull request's own head.
func attestedWorld(t *testing.T, base, head string) *demotionWorld {
	t.Helper()
	w := memoryDemotionWorld(t)
	served := w.server.Config
	served.Ledger = attestedLedger{demotionLedger{Ledger: served.Ledger.(demotionLedger).Ledger, overrideStore: w.keeper}}
	served.Spend = attestedSpend{usd: "12"}
	w.server = New(served)
	w.http = httptest.NewServer(w.server.Handler())
	t.Cleanup(w.http.Close)
	w.stub.FilesAt[approvalBase] = map[string]string{"costroid.yml": base, servedPackPath: servedPackSource}
	w.stub.FilesAt[acmeSHA] = map[string]string{"costroid.yml": head}
	return w
}

// atHead asks for pull request 7's verdict at its own head, through the
// installation.
func atHead() map[string]any {
	return map[string]any{"installation": installationID, "config_ref": acmeSHA}
}

// TestAVerdictOffTheBaseNeverConcludesSuccess asks for verdicts whose
// settings the service did not read itself at the head of the pull request's
// base branch: the head's file that names no pack where the base's names one
// that fails every run, the head's file with a looser budget than the base's,
// a file stated with no installation to read it through, and a file the
// service read where GitHub does not answer the pull request. Each budget
// passes as computed, and each verdict concludes neutral, with
// override_record saying why.
func TestAVerdictOffTheBaseNeverConcludesSuccess(t *testing.T) {
	withPack := "policy_pack: " + servedPackPath + "\n" + looseBudget
	for name, tc := range map[string]struct {
		base, stated string
		fields       map[string]any
		fail         string
		reason       overrideReach
	}{
		"the head's file, which names no pack":  {base: withPack, stated: looseBudget, fields: atHead(), reason: reachNotBase},
		"the head's file, with a looser budget": {base: tightBudget, stated: looseBudget, fields: atHead(), reason: reachNotBase},
		"a file stated with no installation":    {base: tightBudget, stated: looseBudget, fields: map[string]any{}, reason: reachConfigUnread},
		"a pull request GitHub does not answer": {base: looseBudget, stated: looseBudget, fail: "GET /repos/example/widgets/pulls/7", reason: reachPullUnread},
	} {
		t.Run(name, func(t *testing.T) {
			w := attestedWorld(t, tc.base, tc.stated)
			if tc.fail != "" {
				w.stub.Fail[tc.fail] = http.StatusBadGateway
			}
			answer := w.answer(t, 7, tc.stated, tc.fields)
			if answer.Policy.Conclusion != policy.Neutral || answer.OverrideRecord != tc.reason || budgetResult(t, answer.Policy).Outcome != policy.Pass {
				t.Errorf("%s with %+v, saying %q, want neutral and %q", answer.Policy.Conclusion, answer.Policy.Results, answer.OverrideRecord, tc.reason)
			}
		})
	}
}

// TestAVerdictOffTheBaseStillFails asks for verdicts whose settings the
// service did not read at the base, on a budget the gateway's count passes.
// The cap holds a conclusion down and never raises one, so a failure stays a
// failure.
func TestAVerdictOffTheBaseStillFails(t *testing.T) {
	for name, fields := range map[string]map[string]any{"at the head": atHead(), "with no installation": {}} {
		w := attestedWorld(t, looseBudget, tightBudget)
		answer := w.answer(t, 7, tightBudget, fields)
		if answer.Policy.Conclusion != policy.Failure || budgetResult(t, answer.Policy).Outcome != policy.Fail {
			t.Errorf("%s: %s with %+v, saying %q", name, answer.Policy.Conclusion, answer.Policy.Results, answer.OverrideRecord)
		}
	}
}

// unrecordedLedger is the attested pull request on a ledger that keeps no
// override record.
type unrecordedLedger struct {
	Ledger
}

func (l unrecordedLedger) PullRequest(ctx context.Context, tenant store.TenantID, repo, pr string) ([]store.Line, error) {
	return attestedLedger{}.PullRequest(ctx, tenant, repo, pr)
}

// TestAVerdictAtTheBaseConcludesAsComputed asks for verdicts on the file the
// service read itself at the head of the base branch: a budget the gateway's
// count passes concludes success, one it fails concludes failure, on a ledger
// that keeps an override record and on one that keeps none.
func TestAVerdictAtTheBaseConcludesAsComputed(t *testing.T) {
	for config, want := range map[string]string{looseBudget: policy.Success, tightBudget: policy.Failure} {
		w := attestedWorld(t, config, config)
		if answer := w.answer(t, 7, config, nil); answer.Policy.Conclusion != want || answer.OverrideRecord != reachRecorded {
			t.Errorf("with a record: %s, saying %q, want %s", answer.Policy.Conclusion, answer.OverrideRecord, want)
		}
		served := w.server.Config
		served.Ledger = unrecordedLedger{served.Ledger.(attestedLedger).Ledger}
		w.server = New(served)
		w.http = httptest.NewServer(w.server.Handler())
		t.Cleanup(w.http.Close)
		if answer := w.answer(t, 7, config, nil); answer.Policy.Conclusion != want || answer.OverrideRecord != reachNoRecord {
			t.Errorf("with no record: %s, saying %q, want %s", answer.Policy.Conclusion, answer.OverrideRecord, want)
		}
	}
}
