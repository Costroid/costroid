package history_test

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Costroid/costroid/internal/comment"
	"github.com/Costroid/costroid/internal/decimal"
	"github.com/Costroid/costroid/internal/history"
	"github.com/Costroid/costroid/internal/policy"
	"github.com/Costroid/costroid/internal/scm"
	"github.com/Costroid/costroid/internal/scm/scmtest"
)

const (
	// recordedCommit is the synthetic commit of the recorded check run.
	recordedCommit = "0000000000000000000000000000000000000001"
	secondCommit   = "2b7d9c0e41f35a68b0c2d4e6f8091a2b3c4d5e6f"
	thirdCommit    = "3c8e0d1f52a46b79c1d3e5f7091b2c3d4e5f6a7b"
	pullNumber     = 7
	// plantedDigest and otherDigest stand for digests of rows. The figures
	// beside them are synthetic, so no rows have these digests.
	plantedDigest = "5f1d3c0b8a9e7d6c5b4a39281706f5e4d3c2b1a09f8e7d6c5b4a392817060504"
	otherDigest   = "a0b1c2d3e4f5061728394a5b6c7d8e9f00112233445566778899aabbccddeeff"
)

func adapter(t *testing.T, stub *scmtest.Stub) *scm.GitHub {
	t.Helper()
	host, err := scm.NewGitHub(scm.GitHubOptions{API: stub.Server.URL, Repository: "example/widgets", Tokens: scm.StaticToken(stub.Token), Client: http.DefaultClient})
	if err != nil {
		t.Fatal(err)
	}
	return host
}

func recorded(t *testing.T) []byte {
	t.Helper()
	answer, err := os.ReadFile(filepath.Join("testdata", "check-runs", "check-runs.json"))
	if err != nil {
		t.Fatal(err)
	}
	return answer
}

func figure(currency, basis, cost string) comment.Figure {
	return comment.Figure{Currency: currency, Basis: basis, Attribution: "unattributed", Enforcement: "none", Cost: decimal.MustParse(cost), Rows: 1}
}

// checkText is the check run text this release posts for the figures. The
// figures are synthetic: no run produced them.
func checkText(t *testing.T, figures ...comment.Figure) string {
	t.Helper()
	output, err := comment.Check(comment.Input{State: comment.Reported, Ledger: comment.Ledger{Rows: len(figures), Figures: figures}})
	if err != nil {
		t.Fatal(err)
	}
	return output.Text
}

// withSchema rewrites the object's schema, as another release wrote it.
func withSchema(text, schema string) string {
	return strings.Replace(text, `"schema": "costroid.result/0.9"`, `"schema": "`+schema+`"`, 1)
}

// withDigest writes a rows digest into the object.
func withDigest(text, digest string) string {
	return strings.Replace(text, `"rows_sha256": null`, `"rows_sha256": "`+digest+`"`, 1)
}

// ownReport is this run's report, which wrote no rows.
func ownReport(t *testing.T) history.Report {
	t.Helper()
	result, err := comment.Result(comment.Input{State: comment.NoRows, Reason: comment.EmptyRowsFile})
	if err != nil {
		t.Fatal(err)
	}
	own, err := history.Parse([]byte(result))
	if err != nil {
		t.Fatal(err)
	}
	return own
}

func pullWith(stub *scmtest.Stub, commits ...string) {
	stub.Pulls = append(stub.Pulls, scmtest.Pull{Number: pullNumber, State: "open", HeadSHA: commits[len(commits)-1], Commits: commits})
}

func posted(stub *scmtest.Stub, commit, text string) {
	stub.Checks = append(stub.Checks, scmtest.Check{ID: int64(500 + len(stub.Checks)), Name: comment.CheckName, HeadSHA: commit, Conclusion: "neutral", Text: text, App: "github-actions"})
}

func read(t *testing.T, stub *scmtest.Stub, head string) comment.Lifetime {
	t.Helper()
	return readWith(t, stub, head, ownReport(t)).Lifetime
}

func readWith(t *testing.T, stub *scmtest.Stub, head string, own history.Report) history.Totals {
	t.Helper()
	totals, err := history.Read(context.Background(), adapter(t, stub), pullNumber, head, own)
	if err != nil {
		t.Fatal(err)
	}
	return totals
}

// sentences renders the comment with the lifetime and returns its lifetime
// lines.
func sentences(t *testing.T, lifetime comment.Lifetime) []string {
	t.Helper()
	body, err := comment.CommentWithLifetime(comment.Input{State: comment.NoRows, Amounts: comment.AmountsShown}, &lifetime)
	if err != nil {
		t.Fatal(err)
	}
	var lines []string
	for _, line := range strings.Split(body, "\n") {
		if strings.HasPrefix(line, "Across this pull request:") || strings.Contains(line, "left out") || strings.HasPrefix(line, "GitHub lists") {
			lines = append(lines, line)
		}
	}
	return lines
}

type want struct {
	currency, basis, cost string
	reports               int
	undigested            bool
}

func assertFigures(t *testing.T, got []comment.LifetimeFigure, wanted ...want) {
	t.Helper()
	if len(got) != len(wanted) {
		t.Fatalf("%d figures, want %d: %+v", len(got), len(wanted), got)
	}
	for index, figure := range got {
		w := wanted[index]
		if figure.Currency != w.currency || figure.Basis != w.basis || figure.Cost.String() != w.cost || figure.Reports != w.reports || figure.Undigested != w.undigested {
			t.Errorf("figure %d = %s %s %s over %d reports (undigested %t), want %+v", index, figure.Currency, figure.Basis, figure.Cost, figure.Reports, figure.Undigested, w)
		}
	}
}

func TestLifetimeTotalFromCheckRuns(t *testing.T) {
	stub := scmtest.New(t)
	pullWith(stub, recordedCommit, secondCommit, thirdCommit)
	stub.CheckLists[recordedCommit] = recorded(t)
	posted(stub, secondCommit, checkText(t, figure("EUR", "measured", "0.25"), figure("USD", "list_equivalent", "1.5")))
	posted(stub, thirdCommit, checkText(t, figure("EUR", "list_equivalent", "0.4"), figure("USD", "measured", "0.0190116")))
	lifetime := read(t, stub, thirdCommit)
	// USD measured is the recorded run's 0.1309884 and the third commit's
	// 0.0190116, which add up to 0.15 exactly.
	assertFigures(t, lifetime.Figures,
		want{"EUR", "list_equivalent", "0.4", 1, true},
		want{"EUR", "measured", "0.25", 1, true},
		want{"USD", "list_equivalent", "1.5", 1, true},
		want{"USD", "measured", "0.15", 2, true},
	)
	if lifetime.Capped || lifetime.Unread != 0 {
		t.Errorf("capped %t, unread %d", lifetime.Capped, lifetime.Unread)
	}
	lines := sentences(t, lifetime)
	wanted := []string{
		"Across this pull request: EUR 0.4 at list (List-equivalent, not billed), from 1 report.",
		"Across this pull request: EUR 0.25 at list (Measured), from 1 report.",
		"Across this pull request: USD 1.5 at list (List-equivalent, not billed), from 1 report.",
		"Across this pull request: USD 0.15 at list (Measured), from 2 reports, which may include a repeated report.",
	}
	if strings.Join(lines, "\n") != strings.Join(wanted, "\n") {
		t.Errorf("lines\n%s\nwant\n%s", strings.Join(lines, "\n"), strings.Join(wanted, "\n"))
	}
}

func TestLifetimeDropsRepeats(t *testing.T) {
	t.Run("a repeated digest counts once", func(t *testing.T) {
		stub := scmtest.New(t)
		pullWith(stub, secondCommit)
		text := withDigest(checkText(t, figure("USD", "measured", "0.1")), plantedDigest)
		posted(stub, secondCommit, text)
		posted(stub, secondCommit, text)
		lifetime := read(t, stub, secondCommit)
		assertFigures(t, lifetime.Figures, want{"USD", "measured", "0.1", 1, false})
		if lines := sentences(t, lifetime); len(lines) != 1 || lines[0] != "Across this pull request: USD 0.1 at list (Measured), from 1 report." {
			t.Errorf("lines %q", lines)
		}
	})
	t.Run("an object without a digest counts once per check run", func(t *testing.T) {
		stub := scmtest.New(t)
		pullWith(stub, secondCommit)
		text := withSchema(checkText(t, figure("USD", "measured", "0.1")), "costroid.result/0.3")
		posted(stub, secondCommit, text)
		posted(stub, secondCommit, text)
		lifetime := read(t, stub, secondCommit)
		assertFigures(t, lifetime.Figures, want{"USD", "measured", "0.2", 2, true})
		if lines := sentences(t, lifetime); len(lines) != 1 || !strings.HasSuffix(lines[0], "from 2 reports, which may include a repeated report.") {
			t.Errorf("lines %q", lines)
		}
	})
}

// digestedReport is this run's own report: synthetic figures over rows whose
// digest is digest.
func digestedReport(t *testing.T, digest string, figures ...comment.Figure) history.Report {
	t.Helper()
	result, err := comment.Result(comment.Input{State: comment.Reported, Ledger: comment.Ledger{Rows: len(figures), Figures: figures}, RowsSHA256: digest})
	if err != nil {
		t.Fatal(err)
	}
	own, err := history.Parse([]byte(result))
	if err != nil {
		t.Fatal(err)
	}
	return own
}

// TestLifetimeDropsRepeatsByDigest holds the repeat rule to result/v0.6 and
// later: a digest such an object states drops every later report over the
// same rows,
// this run's own included, and an older object's digest is never read, so it
// still counts once per check run and the line says so.
func TestLifetimeDropsRepeatsByDigest(t *testing.T) {
	t.Run("a v0.6 repeat on another commit counts once", func(t *testing.T) {
		stub := scmtest.New(t)
		pullWith(stub, secondCommit, thirdCommit)
		text := withDigest(checkText(t, figure("USD", "measured", "0.1")), plantedDigest)
		posted(stub, secondCommit, text)
		posted(stub, thirdCommit, text)
		totals := readWith(t, stub, thirdCommit, digestedReport(t, otherDigest, figure("USD", "measured", "0.05")))
		assertFigures(t, totals.Lifetime.Figures, want{"USD", "measured", "0.15", 2, false})
		if lines := sentences(t, totals.Lifetime); len(lines) != 1 || lines[0] != "Across this pull request: USD 0.15 at list (Measured), from 2 reports." {
			t.Errorf("lines %q", lines)
		}
		if totals.Earlier.USD.String() != "0.1" || totals.Earlier.MayRepeat || totals.Earlier.OtherCurrency || totals.Earlier.Trust != policy.Unverified {
			t.Errorf("earlier %+v, want USD 0.1, no repeat, unverified", totals.Earlier)
		}
	})
	t.Run("a v0.6 report and a v0.7 report over the same rows count once", func(t *testing.T) {
		stub := scmtest.New(t)
		pullWith(stub, secondCommit, thirdCommit)
		text := withDigest(checkText(t, figure("USD", "measured", "0.1")), plantedDigest)
		posted(stub, secondCommit, withSchema(text, "costroid.result/0.6"))
		posted(stub, thirdCommit, text)
		totals := readWith(t, stub, thirdCommit, digestedReport(t, otherDigest, figure("USD", "measured", "0.05")))
		assertFigures(t, totals.Lifetime.Figures, want{"USD", "measured", "0.15", 2, false})
	})
	t.Run("this run reported again counts once and is not earlier", func(t *testing.T) {
		stub := scmtest.New(t)
		pullWith(stub, secondCommit)
		posted(stub, secondCommit, withDigest(checkText(t, figure("USD", "measured", "0.1")), plantedDigest))
		totals := readWith(t, stub, secondCommit, digestedReport(t, plantedDigest, figure("USD", "measured", "0.1")))
		assertFigures(t, totals.Lifetime.Figures, want{"USD", "measured", "0.1", 1, false})
		if totals.Earlier.USD.Sign() != 0 || totals.Earlier.MayRepeat {
			t.Errorf("earlier %+v counts this run's own rows", totals.Earlier)
		}
	})
	t.Run("an older object's digest is not read", func(t *testing.T) {
		stub := scmtest.New(t)
		pullWith(stub, secondCommit)
		older := withSchema(withDigest(checkText(t, figure("USD", "measured", "0.1")), plantedDigest), "costroid.result/0.5")
		posted(stub, secondCommit, older)
		posted(stub, secondCommit, older)
		totals := readWith(t, stub, secondCommit, digestedReport(t, otherDigest, figure("EUR", "measured", "0.3")))
		assertFigures(t, totals.Lifetime.Figures, want{"EUR", "measured", "0.3", 1, false}, want{"USD", "measured", "0.2", 2, true})
		if lines := sentences(t, totals.Lifetime); len(lines) != 2 || lines[1] != "Across this pull request: USD 0.2 at list (Measured), from 2 reports, which may include a repeated report." {
			t.Errorf("lines %q", lines)
		}
		if totals.Earlier.USD.String() != "0.2" || !totals.Earlier.MayRepeat {
			t.Errorf("earlier %+v, want USD 0.2 that may repeat", totals.Earlier)
		}
	})
	t.Run("a run without a digest cannot tell itself apart", func(t *testing.T) {
		stub := scmtest.New(t)
		pullWith(stub, secondCommit)
		posted(stub, secondCommit, withDigest(checkText(t, figure("USD", "measured", "0.1"), figure("EUR", "billed", "2")), plantedDigest))
		totals := readWith(t, stub, secondCommit, digestedReport(t, "", figure("USD", "measured", "0.1")))
		if totals.Earlier.USD.String() != "0.1" || !totals.Earlier.MayRepeat || !totals.Earlier.OtherCurrency {
			t.Errorf("earlier %+v, want USD 0.1 that may repeat, beside another currency", totals.Earlier)
		}
	})
}

// TestLifetimeLeavesOutARecordedRunsReport posts a report on a recorded run's
// rows beside one on this pull request's own. The recorded figures were spent
// on no commit here, so the total neither counts them nor calls them unread.
func TestLifetimeLeavesOutARecordedRunsReport(t *testing.T) {
	stub := scmtest.New(t)
	pullWith(stub, secondCommit, thirdCommit)
	recordedText := strings.Replace(checkText(t, figure("USD", "measured", "0.0391529")), `"recorded": null`, `"recorded": {"run_id": 42368537950, "date": "2026-11-07"}`, 1)
	if !strings.Contains(recordedText, `"run_id": 42368537950`) {
		t.Fatal("the object states no recorded run")
	}
	posted(stub, secondCommit, recordedText)
	posted(stub, secondCommit, checkText(t, figure("USD", "measured", "0.1")))
	totals := readWith(t, stub, thirdCommit, digestedReport(t, otherDigest, figure("USD", "measured", "0.05")))
	assertFigures(t, totals.Lifetime.Figures, want{"USD", "measured", "0.15", 2, true})
	if totals.Lifetime.Unread != 0 || totals.Earlier.USD.String() != "0.1" {
		t.Errorf("unread %d, earlier %+v: the recorded report was counted", totals.Lifetime.Unread, totals.Earlier)
	}
}

// TestLifetimeNeverAddsTheAgentsOwnFigure reads a result/v0.9 object that
// states an agent's own figure and adds up its USD figure alone, OpenCode's
// own price in USD included.
func TestLifetimeNeverAddsTheAgentsOwnFigure(t *testing.T) {
	text := checkText(t, figure("USD", "measured", "0.1"))
	for _, own := range []string{
		`{"unit": "AIC", "quantity": "1500", "source": "gh-aw"}`,
		`{"unit": "USD", "quantity": "1500", "source": "opencode"}`,
		`{"unit": "nano-AIU", "quantity": "1500", "source": "copilot"}`,
	} {
		stated := strings.Replace(text, `"agent_stated": null`, `"agent_stated": `+own, 1)
		if stated == text {
			t.Fatal("the object states no agent_stated")
		}
		report, err := history.Parse([]byte(strings.TrimSuffix(strings.TrimPrefix(stated, "```json\n"), "\n```")))
		if err != nil {
			t.Fatal(err)
		}
		figures, err := history.Total([]history.Report{report})
		if err != nil {
			t.Fatal(err)
		}
		assertFigures(t, figures, want{"USD", "measured", "0.1", 1, true})
	}
}

func TestLifetimeNeverSumsCurrencies(t *testing.T) {
	var reports []history.Report
	for _, text := range []string{
		checkText(t, figure("USD", "measured", "1.5"), figure("EUR", "measured", "2.25")),
		checkText(t, figure("EUR", "measured", "0.75")),
	} {
		object := strings.TrimSuffix(strings.TrimPrefix(text, "```json\n"), "\n```")
		report, err := history.Parse([]byte(object))
		if err != nil {
			t.Fatal(err)
		}
		reports = append(reports, report)
	}
	figures, err := history.Total(reports)
	if err != nil {
		t.Fatal(err)
	}
	assertFigures(t, figures, want{"EUR", "measured", "3", 2, true}, want{"USD", "measured", "1.5", 1, true})
	lines := strings.Join(sentences(t, comment.Lifetime{Figures: figures}), "\n")
	for _, sum := range []string{"4.5", "3.75", "2.25"} {
		if strings.Contains(lines, sum) {
			t.Errorf("the lines hold %s, a sum across currencies or a figure counted alone:\n%s", sum, lines)
		}
	}
}

func TestLifetimeLeavesOutWhatItCannotAddUp(t *testing.T) {
	stub := scmtest.New(t)
	pullWith(stub, secondCommit)
	text := checkText(t, figure("USD", "measured", "0.1"))
	posted(stub, secondCommit, text)
	for _, unread := range []string{
		withSchema(text, "costroid.result/0.2"),
		withSchema(text, "costroid.result/0.10"),
		strings.Replace(text, `"amounts": "shown"`, `"amounts": "withheld"`, 1),
		strings.Replace(text, `"figures_omitted": 0`, `"figures_omitted": 1`, 1),
		strings.Replace(text, `"currency": "USD"`, `"currency": "usd"`, 1),
		strings.Replace(text, `"cost_basis": "measured"`, `"cost_basis": "guessed"`, 1),
		strings.Replace(text, `"list_cost": "0.1"`, `"list_cost": "-0.1"`, 1),
		withDigest(text, "not-a-digest"),
		strings.TrimPrefix(text, "```json\n"),
		"```json\n" + strings.Repeat(" ", 1<<20) + "{}\n```",
	} {
		posted(stub, secondCommit, unread)
	}
	stub.Checks = append(stub.Checks, scmtest.Check{ID: 900, Name: comment.CheckName, HeadSHA: secondCommit, Text: text, App: "someone-else"})
	lifetime := read(t, stub, secondCommit)
	assertFigures(t, lifetime.Figures, want{"USD", "measured", "0.1", 1, true})
	if lifetime.Unread != 10 {
		t.Errorf("%d check runs left out, want 10: another App's check run is not read at all", lifetime.Unread)
	}
	lines := sentences(t, lifetime)
	if want := "10 check runs are left out of these totals: this release cannot read or add up their result objects."; len(lines) != 2 || lines[1] != want {
		t.Errorf("lines %q", lines)
	}
}

func TestLifetimeReadsTheTokensOwnApp(t *testing.T) {
	stub := scmtest.New(t)
	stub.App = "widgets-ci"
	pullWith(stub, secondCommit)
	host := adapter(t, stub)
	if _, err := host.PostCheck(context.Background(), scm.CheckRun{Name: comment.CheckName, HeadSHA: secondCommit, Conclusion: "neutral", Text: checkText(t, figure("USD", "measured", "0.3")), Completed: time.Unix(0, 0)}); err != nil {
		t.Fatal(err)
	}
	posted(stub, secondCommit, checkText(t, figure("USD", "measured", "0.2")))
	stub.Checks = append(stub.Checks, scmtest.Check{ID: 901, Name: comment.CheckName, HeadSHA: secondCommit, Text: checkText(t, figure("USD", "measured", "5")), App: "someone-else"})
	totals, err := history.Read(context.Background(), host, pullNumber, secondCommit, ownReport(t))
	if err != nil {
		t.Fatal(err)
	}
	assertFigures(t, totals.Lifetime.Figures, want{"USD", "measured", "0.5", 2, true})
}

func TestLifetimeSaysWhenGitHubListsNoMoreCommits(t *testing.T) {
	stub := scmtest.New(t)
	commits := make([]string, 260)
	for index := range commits {
		commits[index] = fmt.Sprintf("%040x", index+1)
	}
	pullWith(stub, commits...)
	posted(stub, commits[259], checkText(t, figure("USD", "measured", "0.1")))
	lifetime := read(t, stub, commits[259])
	if !lifetime.Capped {
		t.Error("a list of 250 commits is not marked capped")
	}
	assertFigures(t, lifetime.Figures, want{"USD", "measured", "0.1", 1, true})
	lines := sentences(t, lifetime)
	if len(lines) != 2 || lines[1] != "GitHub lists at most 250 commits of a pull request, so these totals can leave out later commits." {
		t.Errorf("lines %q", lines)
	}
	if reads := len(requestsWith(stub, "/check-runs?")); reads != 251 {
		t.Errorf("%d commits read, want the 250 listed and the head", reads)
	}
}

func TestLifetimeFailsWholeWhenACommitCannotBeRead(t *testing.T) {
	stub := scmtest.New(t)
	pullWith(stub, secondCommit, thirdCommit)
	posted(stub, secondCommit, checkText(t, figure("USD", "measured", "0.1")))
	stub.Fail["GET /commits/"+thirdCommit+"/check-runs"] = http.StatusBadGateway
	if lifetime, err := history.Read(context.Background(), adapter(t, stub), pullNumber, thirdCommit, ownReport(t)); err == nil {
		t.Errorf("a partial history gave %+v", lifetime)
	}
}

func requestsWith(stub *scmtest.Stub, part string) []string {
	var found []string
	for _, line := range stub.Requests {
		if strings.Contains(line, part) {
			found = append(found, line)
		}
	}
	return found
}
