package main

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/Costroid/costroid/internal/policy"
	"github.com/Costroid/costroid/internal/scm/scmtest"
)

// The pack a repository names in the tests, and three bodies of it: one that
// fails every run, one that returns nothing, and one that warns.
const (
	packPath    = ".costroid/policy.rego"
	packFails   = "package costroid.pack\n\nresults contains {\"rule\": \"base_rule\", \"outcome\": \"fail\", \"reason\": \"on_every_run\"}\n"
	packSilent  = "package costroid.pack\n"
	packWarns   = "package costroid.pack\n\nresults contains {\"rule\": \"moved_rule\", \"outcome\": \"warn\", \"reason\": \"on_every_run\"}\n"
	packConfig  = "policy_pack: " + packPath + "\npolicy:\n  enforce: true\n"
	packChanged = "A change to `" + packPath + "` in this pull request applies after the merge, because the verdict reads the base branch's file."
)

func digestOf(text string) string {
	sum := sha256.Sum256([]byte(text))
	return hex.EncodeToString(sum[:])
}

// packReads are the requests for the pack's file, by the commit each names.
func packReads(stub *scmtest.Stub) map[string]int {
	reads := map[string]int{}
	for _, request := range stub.Requests {
		if _, query, found := strings.Cut(request, "/contents/"+packPath+"?ref="); found {
			reads[query]++
		}
	}
	return reads
}

// assertPackRan holds a verdict to the pack body that ran: its digest, and
// what it returned.
func assertPackRan(t *testing.T, name string, verdict policy.Verdict, body string, results ...string) {
	t.Helper()
	if verdict.Pack == nil || verdict.Pack.Path != packPath || verdict.Pack.SHA256 != digestOf(body) {
		t.Fatalf("%s: the verdict holds the pack %+v", name, verdict.Pack)
	}
	var got []string
	for _, result := range verdict.Pack.Results {
		got = append(got, result.Rule+" "+string(result.Outcome))
	}
	if !slices.Equal(got, results) {
		t.Errorf("%s: the pack returned %q, want %q", name, got, results)
	}
}

// TestCustomerPackFromBase runs the pack the base branch holds on each path
// that posts or gives a verdict: the report, whose check fails on the base
// pack's failure, names the pack's rule in its title and says the pull
// request's change to the pack applies after the merge; the report with the
// base branch moved past the event's base, which reads the pack where it
// reads costroid.yml; a fork's follow-up, which holds the failure at neutral
// and reads nothing at the head; and costroid policy, which reads the same
// pack from a file and gives the same verdict.
func TestCustomerPackFromBase(t *testing.T) {
	t.Run("the report", func(t *testing.T) {
		stub := reportEnvironment(t, pullRequestEvent())
		stub.FilesAt[baseCommit] = map[string]string{configFile: packConfig, packPath: packFails}
		stub.FilesAt[headCommit] = map[string]string{configFile: packConfig, packPath: packSilent}
		code, _, stderr := execute(t, "report", "--rows", realRows)
		if code != 0 || len(stub.Checks) != 1 || len(stub.Comments) != 1 {
			t.Fatalf("exit %d: %s", code, stderr)
		}
		check := stub.Checks[0]
		verdict := checkVerdict(t, check.Text)
		assertPackRan(t, "the report", verdict, packFails, "base_rule fail")
		if check.Conclusion != policy.Failure || !strings.HasPrefix(check.Title, "Pack rule failed (base_rule): ") || verdict.ConfigRef != baseCommit {
			t.Errorf("posted %s titled %q, settings from %s", check.Conclusion, check.Title, verdict.ConfigRef)
		}
		if !strings.Contains(stub.Comments[0].Body, packChanged) || !strings.Contains(check.Summary, "- `base_rule` failed with `on_every_run`") {
			t.Errorf("the comment does not say the change waits for the merge, or the check does not list the pack's result:\n%s\n%s", stub.Comments[0].Body, check.Summary)
		}
	})
	t.Run("the base branch moved", func(t *testing.T) {
		stub := gitHubBaseMoved(t, pullRequestEventOn(baseBranch))
		stub.Branches[baseBranch] = baseHead
		stub.FilesAt[baseCommit] = map[string]string{configFile: packConfig, packPath: packFails}
		stub.FilesAt[baseHead] = map[string]string{configFile: packConfig, packPath: packWarns}
		code, _, stderr := execute(t, "report", "--rows", realRows)
		if code != 0 || len(stub.Checks) != 1 {
			t.Fatalf("exit %d: %s", code, stderr)
		}
		verdict := checkVerdict(t, stub.Checks[0].Text)
		assertPackRan(t, "the base branch moved", verdict, packWarns, "moved_rule warn")
		if reads := packReads(stub); reads[baseHead] != 1 || reads[baseCommit] != 0 || verdict.ConfigRef != baseHead {
			t.Errorf("read the pack at %v, costroid.yml at %s", reads, verdict.ConfigRef)
		}
	})
	t.Run("a fork's follow-up", func(t *testing.T) {
		stub := followUpEnvironment(t, "stranger/widgets")
		stub.FilesAt[defaultBranchCommit] = map[string]string{configFile: packConfig, packPath: packFails}
		stub.FilesAt[headCommit] = map[string]string{configFile: packConfig, packPath: packSilent}
		code, _, stderr := execute(t, "follow-up", "--rows", realRows)
		if code != 0 || len(stub.Checks) != 1 {
			t.Fatalf("exit %d: %s", code, stderr)
		}
		check := stub.Checks[0]
		verdict := checkVerdict(t, check.Text)
		assertPackRan(t, "a fork's follow-up", verdict, packFails, "base_rule fail")
		if check.Conclusion != policy.Neutral || !strings.HasPrefix(check.Title, "Pack rule failed, on a fork's own rows (base_rule): ") {
			t.Errorf("posted %s titled %q", check.Conclusion, check.Title)
		}
		for _, request := range stub.Requests {
			if strings.Contains(request, headCommit) && !bindsTheHead(request) {
				t.Errorf("the follow-up read the head: %s", request)
			}
		}
	})
	t.Run("costroid policy", func(t *testing.T) {
		directory := t.TempDir()
		config := filepath.Join(directory, "base.yml")
		pack := filepath.Join(directory, "base.rego")
		for path, body := range map[string]string{config: packConfig, pack: packFails} {
			if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
				t.Fatal(err)
			}
		}
		code, stdout, stderr := execute(t, "policy", "--rows", realRows, "--config", config, "--pack", pack)
		if code != exitRejected || !strings.Contains(stdout, "- `base_rule` failed with `on_every_run`\n") || !strings.HasPrefix(lastLine(stdout), "Pack rule failed (base_rule): ") {
			t.Errorf("exit %d: %s%s", code, stdout, stderr)
		}
		if code, _, stderr := execute(t, "policy", "--rows", realRows, "--config", filepath.Join(directory, "none.yml"), "--pack", pack); code != exitFailure || !strings.Contains(stderr, "--pack names a policy pack, and") {
			t.Errorf("a --pack beside a costroid.yml that names none: exit %d, %s", code, stderr)
		}
	})
}

// TestCustomerPackIsNeverReadFromTheHead changes the pack at the pull
// request's head: a head that adds a failing pack where the base names none,
// and a head that rewrites the base's pack to fail every run. Neither failure
// reaches the verdict: no pack runs where the base names none, and none is
// read at the head, and where the base names one, its own pack runs and the
// head's file is read once, to say that the change applies after the merge.
func TestCustomerPackIsNeverReadFromTheHead(t *testing.T) {
	t.Run("the head adds a pack", func(t *testing.T) {
		stub := reportEnvironment(t, pullRequestEvent())
		stub.FilesAt[baseCommit] = map[string]string{configFile: underBudget}
		stub.FilesAt[headCommit] = map[string]string{configFile: packConfig + underBudget, packPath: packFails}
		code, _, stderr := execute(t, "report", "--rows", realRows)
		if code != 0 || len(stub.Checks) != 1 {
			t.Fatalf("exit %d: %s", code, stderr)
		}
		verdict := checkVerdict(t, stub.Checks[0].Text)
		if verdict.Pack != nil || stub.Checks[0].Conclusion == policy.Failure || len(packReads(stub)) != 0 {
			t.Errorf("the head's pack reached the check: %s, %+v, read at %v", stub.Checks[0].Conclusion, verdict.Pack, packReads(stub))
		}
	})
	t.Run("the head rewrites the pack", func(t *testing.T) {
		stub := reportEnvironment(t, pullRequestEvent())
		stub.FilesAt[baseCommit] = map[string]string{configFile: packConfig, packPath: packSilent}
		stub.FilesAt[headCommit] = map[string]string{configFile: packConfig, packPath: packFails}
		code, _, stderr := execute(t, "report", "--rows", realRows)
		if code != 0 || len(stub.Checks) != 1 {
			t.Fatalf("exit %d: %s", code, stderr)
		}
		check := stub.Checks[0]
		verdict := checkVerdict(t, check.Text)
		assertPackRan(t, "the head rewrites the pack", verdict, packSilent)
		if check.Conclusion == policy.Failure || strings.Contains(check.Text, "base_rule") {
			t.Errorf("the head's pack decided: %s\n%s", check.Conclusion, check.Text)
		}
		if reads := packReads(stub); reads[baseCommit] != 1 || reads[headCommit] != 1 || len(reads) != 2 {
			t.Errorf("read the pack at %v", reads)
		}
		if !strings.Contains(check.Summary, packChanged) {
			t.Errorf("the check does not say the change applies after the merge:\n%s", check.Summary)
		}
	})
}
