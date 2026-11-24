package server

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Costroid/costroid/internal/policy"
	"github.com/Costroid/costroid/internal/scm/scmtest"
	"github.com/Costroid/costroid/internal/store"
)

// The pack the served repository names, which fails every run, and the
// costroid.yml that names it.
const (
	servedPackPath   = ".costroid/policy.rego"
	servedPackSource = "package costroid.pack\n\nresults contains {\"rule\": \"team_rule\", \"outcome\": \"fail\", \"reason\": \"on_every_run\"}\n"
	servedPackConfig = "policy_pack: " + servedPackPath + "\npolicy:\n  enforce: true\n"
)

// packWorld is a memory world whose service reaches example/widgets as the
// App's installation 58, signing its token through the KMS key, with the
// stub's files at every commit. Pull request 7's head is acmeSHA, and its
// base branch main is at approvalBase, the config_ref a verdict states unless
// it says otherwise. The memory ledger keeps no override record.
func packWorld(t *testing.T, files map[string]string) (*world, *scmtest.Stub) {
	t.Helper()
	stub := scmtest.New(t)
	stub.Head, stub.BaseBranch = acmeSHA, "main"
	stub.Branches["main"] = approvalBase
	for path, body := range files {
		stub.Files[path] = body
	}
	installations := newMemoryInstallations()
	if err := installations.AddRepositories(context.Background(), installationID, "example", []store.Repository{widgets}); err != nil {
		t.Fatal(err)
	}
	w, _ := memoryWorld(t)
	config := w.server.Config
	config.App = &GitHubApp{Secret: []byte(webhookSecretText), Installations: installations, Hosts: newAppFront(t, stub).hosts(t)}
	config.Logger = slog.New(slog.DiscardHandler)
	w.server = New(config)
	w.http = httptest.NewServer(w.server.Handler())
	t.Cleanup(w.http.Close)
	return w, stub
}

// askVerdict asks for pull request 7's verdict on example/widgets with the
// ingest key.
func (w *world) askVerdict(t *testing.T, config string, fields map[string]any) answer {
	t.Helper()
	return w.send(http.MethodPost, "/v1/pulls/7/verdict?repo=github.com/"+approvalRepo, acmeKey, jsonType, verdictBody(t, config, fields))
}

func verdictOfAnswer(t *testing.T, got answer) policy.Verdict {
	t.Helper()
	return readAnswer(t, got).Policy
}

// TestServeRunsAPackOnlyWhereItReadTheFileItself asks for a verdict under a
// costroid.yml that names a pack. With the App's installation named, serve
// reads costroid.yml and the pack at config_ref itself and runs the pack.
// Without the installation, with one that does not reach the repository, or
// with a costroid.yml that is not the one at config_ref, it reads no pack and
// states customer_pack pack_unread.
func TestServeRunsAPackOnlyWhereItReadTheFileItself(t *testing.T) {
	w, _ := packWorld(t, map[string]string{"costroid.yml": servedPackConfig, servedPackPath: servedPackSource})
	ran := verdictOfAnswer(t, w.askVerdict(t, servedPackConfig, map[string]any{"installation": installationID}))
	if ran.Conclusion != policy.Failure || ran.Pack == nil || len(ran.Pack.Results) != 1 || ran.Pack.Results[0].Rule != "team_rule" {
		t.Errorf("serve did not run the pack it read: %+v, %+v", ran.Conclusion, ran.Pack)
	}
	for name, tc := range map[string]struct {
		config string
		fields map[string]any
	}{
		"no installation":                    {servedPackConfig, nil},
		"an installation it does not hold":   {servedPackConfig, map[string]any{"installation": installationID + 1}},
		"another costroid.yml than the base": {servedPackConfig + "  run_budget_usd: \"5\"\n", map[string]any{"installation": installationID}},
	} {
		verdict := verdictOfAnswer(t, w.askVerdict(t, tc.config, tc.fields))
		last := verdict.Results[len(verdict.Results)-1]
		if last.Rule != policy.RuleCustomerPack || last.Reason != policy.ReasonPackUnread || verdict.Pack == nil || len(verdict.Pack.Results) != 0 {
			t.Errorf("%s: %+v beside %+v", name, verdict.Results, verdict.Pack)
		}
	}
}

// TestServeCapsThePacksItRunsAtOnce holds serve to maxPackChildren children
// at once. With one slot free a verdict runs its pack. With every slot held, a
// verdict that would run a pack waits packWait, then answers 503 PACK_BUSY
// with a Retry-After, and runs nothing: a verdict without its pack could read
// looser than the repository's policy. A verdict that runs no pack is never
// held. docs/serve.md states the number.
func TestServeCapsThePacksItRunsAtOnce(t *testing.T) {
	previous := packWait
	packWait = 200 * time.Millisecond
	t.Cleanup(func() { packWait = previous })
	w, _ := packWorld(t, map[string]string{"costroid.yml": servedPackConfig, servedPackPath: servedPackSource})
	for range maxPackChildren - 1 {
		packChildren <- struct{}{}
	}
	t.Cleanup(func() {
		for len(packChildren) > 0 {
			<-packChildren
		}
	})
	if verdict := verdictOfAnswer(t, w.askVerdict(t, servedPackConfig, map[string]any{"installation": installationID})); verdict.Conclusion != policy.Failure || len(verdict.Pack.Results) != 1 {
		t.Fatalf("with one slot of %d free the pack did not run: %+v", maxPackChildren, verdict.Pack)
	}
	packChildren <- struct{}{}
	request, err := http.NewRequest(http.MethodPost, w.http.URL+"/v1/pulls/7/verdict?repo=github.com/"+approvalRepo, strings.NewReader(string(verdictBody(t, servedPackConfig, map[string]any{"installation": installationID}))))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer "+acmeKey)
	request.Header.Set("Content-Type", jsonType)
	start := time.Now()
	response, err := w.http.Client().Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	busy := answer{status: response.StatusCode}
	if busy.body, err = io.ReadAll(response.Body); err != nil {
		t.Fatal(err)
	}
	if waited := time.Since(start); busy.status != http.StatusServiceUnavailable || busy.code(t) != codePackBusy || waited < packWait || response.Header.Get("Retry-After") == "" {
		t.Errorf("with %d packs running: %d %s after %s, Retry-After %q", maxPackChildren, busy.status, busy.body, waited, response.Header.Get("Retry-After"))
	}
	if len(packChildren) != maxPackChildren {
		t.Errorf("%d slots are held after a refused verdict, want %d", len(packChildren), maxPackChildren)
	}
	for name, config := range map[string]string{"no pack": "policy:\n  enforce: true\n", "a pack serve did not read": servedPackConfig} {
		if verdict := verdictOfAnswer(t, w.askVerdict(t, config, nil)); verdict.Conclusion == "" {
			t.Errorf("%s: no verdict", name)
		}
	}
	page, err := os.ReadFile(filepath.Join("..", "..", "docs", "serve.md"))
	if err != nil {
		t.Fatal(err)
	}
	if want := fmt.Sprintf("at most %d policy packs at once", maxPackChildren); !strings.Contains(string(page), want) {
		t.Errorf("docs/serve.md does not say %q", want)
	}
}

// TestAPackIsReadOnlyAtTheBase asks for pull request 7's verdict under a
// costroid.yml that names a pack which fails every run, with the very bytes
// the service reads at every commit, on a ledger that keeps an override
// record and on one that keeps none. At the head of the pull request's base
// branch, as GitHub states it, the pack runs. At any other commit, or where
// GitHub does not answer the base branch, the service reads no pack: the
// verdict states customer_pack pack_unread and concludes neutral, and
// override_record says why. A caller that named another commit could name
// one whose pack is looser or absent.
func TestAPackIsReadOnlyAtTheBase(t *testing.T) {
	files := map[string]string{"costroid.yml": servedPackConfig, servedPackPath: servedPackSource}
	worlds := map[string]struct {
		open   func(*testing.T) (*world, *scmtest.Stub)
		atBase overrideReach
	}{
		"a ledger with an override record": {open: func(t *testing.T) (*world, *scmtest.Stub) {
			w := memoryDemotionWorld(t)
			for path, body := range files {
				w.stub.Files[path] = body
			}
			return w.world, w.stub
		}, atBase: reachRecorded},
		"a ledger without one": {open: func(t *testing.T) (*world, *scmtest.Stub) { return packWorld(t, files) }, atBase: reachNoRecord},
	}
	for name, ledger := range worlds {
		t.Run(name, func(t *testing.T) {
			w, _ := ledger.open(t)
			ran := readAnswer(t, w.askVerdict(t, servedPackConfig, map[string]any{"installation": installationID}))
			if ran.Policy.Conclusion != policy.Failure || ran.Policy.Pack == nil || len(ran.Policy.Pack.Results) != 1 || ran.OverrideRecord != ledger.atBase {
				t.Errorf("at the base's head: %s, %+v, saying %q", ran.Policy.Conclusion, ran.Policy.Pack, ran.OverrideRecord)
			}
			for commit, tc := range notBaseCommits() {
				w, stub := ledger.open(t)
				if tc.setup != nil {
					tc.setup(stub)
				}
				requirePackUnread(t, commit, readAnswer(t, w.askVerdict(t, servedPackConfig, map[string]any{"installation": installationID, "config_ref": tc.ref})), reachNotBase)
			}
			w, stub := ledger.open(t)
			stub.Fail["GET /repos/example/widgets/git/ref/heads/"] = http.StatusBadGateway
			requirePackUnread(t, "a base branch GitHub does not answer", readAnswer(t, w.askVerdict(t, servedPackConfig, map[string]any{"installation": installationID})), reachPullUnread)
		})
	}
}

func readAnswer(t *testing.T, got answer) verdictAnswer {
	t.Helper()
	var read verdictAnswer
	if got.status != http.StatusOK || json.Unmarshal(got.body, &read) != nil {
		t.Fatalf("answered %d: %s", got.status, got.body)
	}
	return read
}

// requirePackUnread holds a verdict to no pack read, and its answer to the
// reason.
func requirePackUnread(t *testing.T, name string, got verdictAnswer, reason overrideReach) {
	t.Helper()
	var last policy.Result
	if results := got.Policy.Results; len(results) > 0 {
		last = results[len(results)-1]
	}
	if last.Rule != policy.RuleCustomerPack || last.Reason != policy.ReasonPackUnread || got.Policy.Pack == nil || len(got.Policy.Pack.Results) != 0 ||
		got.Policy.Conclusion == policy.Failure || got.OverrideRecord != reason {
		t.Errorf("%s: %s, %+v beside %+v, saying %q, want %q", name, got.Policy.Conclusion, got.Policy.Results, got.Policy.Pack, got.OverrideRecord, reason)
	}
}
