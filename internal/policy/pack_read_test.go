package policy

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/Costroid/costroid/internal/decimal"
	"github.com/Costroid/costroid/internal/scm"
)

// The pack the tests name, and two bodies of it: one that warns and one that
// fails every run.
const (
	packPath  = ".costroid/policy.rego"
	packWarns = "package costroid.pack\n\nresults contains {\"rule\": \"team_rule\", \"outcome\": \"warn\", \"reason\": \"answered\"}\n"
	packFails = "package costroid.pack\n\nresults contains {\"rule\": \"team_rule\", \"outcome\": \"fail\", \"reason\": \"answered\"}\n"
)

// packConfig is a costroid.yml that names the pack and enforces a budget the
// run keeps to.
const packConfig = "policy_pack: " + packPath + "\npolicy:\n  run_budget_usd: \"5\"\n  enforce: true\n"

// tree is a host that holds files by commit, then path, and records each read
// as path@commit.
type tree struct {
	held map[string]map[string]string
	read []string
}

func (t *tree) ReadFile(_ context.Context, path, ref string) ([]byte, error) {
	t.read = append(t.read, path+"@"+ref)
	data, found := t.held[ref][path]
	if !found {
		return nil, scm.ErrNotFound
	}
	return []byte(data), nil
}

// packUsage is one run of USD 3 the agent's job wrote.
func packUsage() Usage {
	return Usage{Rows: 1, Currencies: []CurrencyUsage{{Currency: "USD", ListCost: decimal.MustParse("3"), Rows: 1, Models: map[string]int{"claude-sonnet-5-5": 1}}}}
}

// TestCustomerPackIsReadAtTheConfigsCommit reads the pack at the commit
// ReadConfig read costroid.yml at, and at no other: the base on a pull
// request, the commit itself on a push. A configuration ReadConfig did not
// read, one whose commit was changed after the read, and one on a pull
// request that is not the base's get no read at all, and their verdict states
// customer_pack pack_unread with the digest of zero bytes. A pack read for one
// costroid.yml never runs under another.
func TestCustomerPackIsReadAtTheConfigsCommit(t *testing.T) {
	host := func() *tree {
		return &tree{held: map[string]map[string]string{
			base: {ConfigPath: packConfig, packPath: packWarns},
			head: {ConfigPath: packConfig, packPath: packFails},
		}}
	}
	ctx := context.Background()
	for name, tc := range map[string]struct {
		commits Commits
		at      string
	}{
		"a pull request reads its base": {Commits{Base: base, Head: head, PullRequest: true}, base},
		"a push reads its own commit":   {Commits{Head: head}, head},
	} {
		files := host()
		config := ReadConfig(ctx, files, tc.commits)
		pack := ReadPack(ctx, files, config)
		if want := []string{ConfigPath + "@" + tc.at, packPath + "@" + tc.at}; !slices.Equal(files.read, want) {
			t.Errorf("%s: read %q, want %q", name, files.read, want)
		}
		verdict := evaluate(t, Request{Usage: packUsage(), Config: config, Pack: pack})
		body := packWarns
		if tc.at == head {
			body = packFails
		}
		if verdict.Pack == nil || verdict.Pack.Path != packPath || verdict.Pack.SHA256 != digest([]byte(body)) || len(verdict.Pack.Results) != 1 {
			t.Errorf("%s: the verdict holds the pack %+v", name, verdict.Pack)
		}
	}

	files := host()
	read := ReadConfig(ctx, files, Commits{Base: base, Head: head, PullRequest: true})
	moved := read
	moved.Ref = head
	notBase := read
	notBase.FromBase = false
	for name, config := range map[string]ConfigFile{
		"a configuration ReadConfig did not read": {Data: []byte(packConfig), Found: true, Ref: head, FromBase: true},
		"a commit changed after the read":         moved,
		"a pull request's file not from the base": notBase,
	} {
		files.read = nil
		pack := ReadPack(ctx, files, config)
		if len(files.read) > 0 {
			t.Errorf("%s: read %q", name, files.read)
		}
		assertPackUnread(t, name, evaluate(t, Request{Usage: packUsage(), Config: config, Pack: pack}))
	}

	other := ReadConfig(ctx, &tree{held: map[string]map[string]string{base: {ConfigPath: packConfig + "  max_run_cost_usd: 9\n"}}}, Commits{Base: base, Head: head, PullRequest: true})
	assertPackUnread(t, "a pack read for another costroid.yml", evaluate(t, Request{Usage: packUsage(), Config: other, Pack: ReadPack(ctx, host(), read)}))
	assertPackUnread(t, "no pack read", evaluate(t, Request{Usage: packUsage(), Config: read}))

	files = host()
	plain := ReadConfig(ctx, files, Commits{Base: base, Head: head, PullRequest: true})
	plain.Data = []byte("policy:\n  run_budget_usd: \"5\"\n")
	if pack := ReadPack(ctx, files, plain); len(files.read) != 1 || evaluate(t, Request{Usage: packUsage(), Config: plain, Pack: pack}).Pack != nil {
		t.Errorf("a costroid.yml that names no pack read %q", files.read)
	}
}

// assertPackUnread holds a verdict to a pack that did not run: Costroid's
// customer_pack result is pack_unread, the pack holds no result, and its
// digest is of zero bytes.
func assertPackUnread(t *testing.T, name string, verdict Verdict) {
	t.Helper()
	last := verdict.Results[len(verdict.Results)-1]
	if last.Rule != RuleCustomerPack || last.Reason != ReasonPackUnread || last.Outcome != Unproven {
		t.Errorf("%s: the last result is %+v", name, last)
	}
	if verdict.Pack == nil || verdict.Pack.SHA256 != digest(nil) || len(verdict.Pack.Results) != 0 {
		t.Errorf("%s: the verdict holds the pack %+v", name, verdict.Pack)
	}
}

// TestAPackIsUnreadWhenAbsentOrPastItsSize reads a pack that is not at the
// commit, one past 64 KiB, and one a host did not answer for: each is
// pack_unread, and none runs.
func TestAPackIsUnreadWhenAbsentOrPastItsSize(t *testing.T) {
	ctx := context.Background()
	large := "package costroid.pack\n" + strings.Repeat("#", maxPackBytes)
	for name, held := range map[string]map[string]string{
		"absent":      {ConfigPath: packConfig},
		"over 64 KiB": {ConfigPath: packConfig, packPath: large},
	} {
		files := &tree{held: map[string]map[string]string{base: held}}
		config := ReadConfig(ctx, files, Commits{Base: base, Head: head, PullRequest: true})
		verdict := evaluate(t, Request{Usage: packUsage(), Config: config, Pack: ReadPack(ctx, files, config)})
		last := verdict.Results[len(verdict.Results)-1]
		if last.Rule != RuleCustomerPack || last.Reason != ReasonPackUnread || verdict.Pack == nil || len(verdict.Pack.Results) != 0 {
			t.Errorf("%s: %+v, %+v", name, last, verdict.Pack)
		}
		if want := digest([]byte(held[packPath])); held[packPath] == "" && verdict.Pack.SHA256 != digest(nil) || held[packPath] != "" && verdict.Pack.SHA256 != want {
			t.Errorf("%s: the digest is %s", name, verdict.Pack.SHA256)
		}
	}
	config := ReadConfig(ctx, &tree{held: map[string]map[string]string{base: {ConfigPath: packConfig}}}, Commits{Base: base, Head: head, PullRequest: true})
	assertPackUnread(t, "a host that did not answer", evaluate(t, Request{Usage: packUsage(), Config: config, Pack: ReadPack(ctx, failingFiles{}, config)}))
}

// TestAPackFromAFileReadsAsOneAtACommit reads the same pack from a file, as
// costroid policy does, and from the host: the verdicts are the same, and a
// missing file is pack_unread as an absent one is.
func TestAPackFromAFileReadsAsOneAtACommit(t *testing.T) {
	ctx := context.Background()
	file := filepath.Join(t.TempDir(), "policy.rego")
	if err := os.WriteFile(file, []byte(packWarns), 0o600); err != nil {
		t.Fatal(err)
	}
	offline := config(packConfig)
	fromFile := evaluate(t, Request{Usage: packUsage(), Config: offline, Pack: PackFromFile(file, offline)})
	files := &tree{held: map[string]map[string]string{head: {ConfigPath: packConfig, packPath: packWarns}}}
	online := ReadConfig(ctx, files, Commits{Head: head})
	fromHost := evaluate(t, Request{Usage: packUsage(), Config: online, Pack: ReadPack(ctx, files, online)})
	if fromFile.Conclusion != fromHost.Conclusion || !slices.Equal(fromFile.Pack.Results, fromHost.Pack.Results) || fromFile.Pack.SHA256 != fromHost.Pack.SHA256 {
		t.Errorf("offline %+v, online %+v", fromFile.Pack, fromHost.Pack)
	}
	assertPackUnread(t, "a missing file", evaluate(t, Request{Usage: packUsage(), Config: offline, Pack: PackFromFile(filepath.Join(t.TempDir(), "none.rego"), offline)}))
}

// TestAChangeToThePackAtTheHeadIsNamedAndNeverRuns compares the base's pack
// with the pull request's head. A changed file and a changed key are named,
// an unchanged pack and a head the host did not answer for are not, and the
// verdict is the base pack's in every case.
func TestAChangeToThePackAtTheHeadIsNamedAndNeverRuns(t *testing.T) {
	ctx := context.Background()
	for name, tc := range map[string]struct {
		head map[string]string
		want PackChange
	}{
		"the same pack":         {map[string]string{ConfigPath: packConfig, packPath: packWarns}, PackUnchanged},
		"another body":          {map[string]string{ConfigPath: packConfig, packPath: packFails}, PackFileChanged},
		"the file removed":      {map[string]string{ConfigPath: packConfig}, PackFileChanged},
		"another path":          {map[string]string{ConfigPath: strings.Replace(packConfig, packPath, ".costroid/other.rego", 1), ".costroid/other.rego": packFails}, PackKeyChanged},
		"the key removed":       {map[string]string{ConfigPath: "policy:\n  run_budget_usd: \"5\"\n", packPath: packWarns}, PackKeyChanged},
		"costroid.yml removed":  {map[string]string{packPath: packFails}, PackKeyChanged},
		"a head the host lacks": {nil, PackUnchanged},
	} {
		files := &tree{held: map[string]map[string]string{base: {ConfigPath: packConfig, packPath: packWarns}}}
		if tc.head != nil {
			files.held[head] = tc.head
		}
		read := ReadConfig(ctx, files, Commits{Base: base, Head: head, PullRequest: true})
		pack := ReadPack(ctx, files, read)
		verdict := evaluate(t, Request{Usage: packUsage(), Config: read, Pack: pack})
		got := pack.ChangedAtHead(ctx, files, read, head)
		if tc.head == nil {
			got = pack.ChangedAtHead(ctx, failingFiles{}, read, head)
		}
		if got != tc.want {
			t.Errorf("%s: %d, want %d", name, got, tc.want)
		}
		if verdict.Pack.SHA256 != digest([]byte(packWarns)) || verdict.Pack.Results[0].Outcome != PackWarn || verdict.Conclusion != Neutral {
			t.Errorf("%s: the head's pack decided: %+v, %+v", name, verdict.Pack, verdict.Conclusion)
		}
	}
	files := &tree{held: map[string]map[string]string{head: {ConfigPath: packConfig, packPath: packWarns}}}
	push := ReadConfig(ctx, files, Commits{Head: head})
	if got := ReadPack(ctx, files, push).ChangedAtHead(ctx, files, push, head); got != PackUnchanged {
		t.Errorf("a push compares itself with itself: %d", got)
	}
}
