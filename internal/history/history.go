// Package history reads the result objects earlier reports posted on a pull
// request's commits, and adds their figures up with this run's.
package history

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"sync"

	"github.com/Costroid/costroid/internal/comment"
	"github.com/Costroid/costroid/internal/policy"
	"github.com/Costroid/costroid/internal/scm"
)

const (
	// maxText bounds the check run text read. A result object is a few
	// kilobytes.
	maxText = 1 << 20
	// parallel bounds the commits read at once.
	parallel = 5
	fence    = "```json\n"
	fenceEnd = "\n```"
)

// schemas are the result objects this release adds up: those that carry
// every figure with its amount in the check run.
var schemas = append([]string{"costroid.result/0.3", "costroid.result/0.4", "costroid.result/0.5"}, digestSchemas...)

// digestSchemas are the result objects that state the digest of their rows.
var digestSchemas = []string{"costroid.result/0.6", "costroid.result/0.7"}

// usd is the one currency the policy's pull request budget is held in.
const usd = "USD"

var digestPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)

// Report is one result object: an earlier run's, read from its check run, or
// this run's own.
type Report struct {
	// CheckRun is the check run that carried the object, 0 for this run's own.
	CheckRun int64
	Block    comment.Block
	// RowsSHA256 is the digest of the rows the object reports, empty when it
	// states none. Only a result/v0.6 or later object states one.
	RowsSHA256 string
}

// Totals is a pull request's reports added up twice: as the comment states
// them, and as the policy's pull request budget reads them.
type Totals struct {
	// Lifetime is every report, this run's included, per currency and basis.
	Lifetime comment.Lifetime
	// Earlier is the reports before this run's, in USD over every basis.
	Earlier policy.Lifetime
}

// Parse reads one result object. An object in a schema this release does not
// add up, with its amounts withheld or a figure left out, or with a figure
// comment.FigureCost refuses, is refused whole: a total without it would read
// as a smaller cost.
//
// The digest of the rows is read from a result/v0.6 or later object only. An
// older object states none, so it counts once per check run. The agent's own
// figure in a result/v0.7 object is never added: it is in the agent's unit.
func Parse(object []byte) (Report, error) {
	var block comment.Block
	if err := json.Unmarshal(object, &block); err != nil {
		return Report{}, fmt.Errorf("history: result object: %w", err)
	}
	switch {
	case !slices.Contains(schemas, block.Schema):
		return Report{}, fmt.Errorf("history: schema %q is not one this release adds up", string(comment.Clean(block.Schema)))
	case block.Amounts != "shown":
		return Report{}, errors.New("history: the result object withholds its amounts")
	case block.FiguresOmitted != 0:
		return Report{}, errors.New("history: the result object leaves figures out")
	case block.RowsSHA256 != nil && !digestPattern.MatchString(*block.RowsSHA256):
		return Report{}, errors.New("history: rows_sha256 is not a SHA-256 digest")
	}
	for _, figure := range block.Figures {
		if _, err := comment.FigureCost(figure); err != nil {
			return Report{}, fmt.Errorf("history: %w", err)
		}
	}
	report := Report{Block: block}
	if slices.Contains(digestSchemas, block.Schema) && block.RowsSHA256 != nil {
		report.RowsSHA256 = *block.RowsSHA256
	}
	return report, nil
}

// parseText reads the fenced object a check run's text carries.
func parseText(text string) (Report, error) {
	if len(text) > maxText {
		return Report{}, fmt.Errorf("history: the check run's text is longer than %d bytes", maxText)
	}
	object, fenced := strings.CutPrefix(text, fence)
	object, closed := strings.CutSuffix(object, fenceEnd)
	if !fenced || !closed {
		return Report{}, errors.New("history: the check run's text is not a fenced result object")
	}
	return Parse([]byte(object))
}

// CheckRunBlocks reads the result objects of the costroid check runs on one
// commit, every attempt included. unread counts the check runs whose object
// Parse refused.
func CheckRunBlocks(ctx context.Context, host scm.History, sha string) (reports []Report, unread int, err error) {
	checks, err := host.CheckRuns(ctx, sha, comment.CheckName)
	if err != nil {
		return nil, 0, err
	}
	for _, check := range checks {
		report, err := parseText(check.Text)
		if err != nil {
			unread++
			continue
		}
		report.CheckRun = check.ID
		reports = append(reports, report)
	}
	return reports, unread, nil
}

// Posted is the result object of one check run as the check run carries it.
type Posted struct {
	CheckRun int64
	Block    comment.Block
	// Object is the object's text, byte for byte.
	Object string
}

// ErrNoCheck reports a commit that no costroid check run reports on yet.
var ErrNoCheck = errors.New("history: no costroid check run is on the commit yet")

// Newest reads the result object of the newest costroid check run on the
// commit, every attempt included. GitHub numbers check runs in the order it
// creates them. An object comment.ParseResult refuses is an error, and an
// older check run is never read in its place: it would report a run the
// newest check has replaced.
func Newest(ctx context.Context, host scm.History, sha string) (Posted, error) {
	checks, err := host.CheckRuns(ctx, sha, comment.CheckName)
	if err != nil {
		return Posted{}, fmt.Errorf("history: read the check runs on %s: %w", sha, err)
	}
	if len(checks) == 0 {
		return Posted{}, ErrNoCheck
	}
	newest := slices.MaxFunc(checks, func(a, b scm.ListedCheck) int { return cmp.Compare(a.ID, b.ID) })
	block, err := comment.ParseResult(newest.Text)
	if err != nil {
		return Posted{}, fmt.Errorf("history: check run %d: %w", newest.ID, err)
	}
	if block.HeadSHA != sha {
		return Posted{}, fmt.Errorf("history: check run %d reports commit %s, not %s", newest.ID, string(comment.Clean(block.HeadSHA)), sha)
	}
	object := strings.TrimSuffix(strings.TrimPrefix(newest.Text, fence), fenceEnd)
	return Posted{CheckRun: newest.ID, Block: block, Object: object}, nil
}

// Read adds up the reports on every commit of the pull request and own, this
// run's report, whose check run is not posted yet. The head is read even when
// the host's list of commits leaves it out.
func Read(ctx context.Context, host scm.History, number int, head string, own Report) (Totals, error) {
	commits, capped, err := host.PullCommits(ctx, number)
	if err != nil {
		return Totals{}, err
	}
	if !slices.Contains(commits, head) {
		commits = append(commits, head)
	}
	reports, unread, err := readCommits(ctx, host, commits)
	if err != nil {
		return Totals{}, err
	}
	figures, err := Total(append(reports, own))
	if err != nil {
		return Totals{}, err
	}
	earlier, err := Earlier(reports, own)
	if err != nil {
		return Totals{}, err
	}
	return Totals{Lifetime: comment.Lifetime{Figures: figures, Capped: capped, Unread: unread}, Earlier: earlier}, nil
}

// Earlier adds up the reports before own, this run's, in USD over every
// basis, as the policy's pull request budget reads them. A report with own's
// digest is this run's rows reported again, so it is left out, and a repeated
// digest counts once. A report without a digest beside another report's
// figures may count the same rows twice, and the total says so. A check run
// is never more than a claim, so the total is unverified.
func Earlier(reports []Report, own Report) (policy.Lifetime, error) {
	earlier := policy.Lifetime{Trust: policy.Unverified}
	seen := map[string]bool{}
	if own.RowsSHA256 != "" {
		seen[own.RowsSHA256] = true
	}
	for _, report := range reports {
		if report.RowsSHA256 != "" {
			if seen[report.RowsSHA256] {
				continue
			}
			seen[report.RowsSHA256] = true
		}
		if len(report.Block.Figures) > 0 && (report.RowsSHA256 == "" || own.RowsSHA256 == "") {
			earlier.MayRepeat = true
		}
		for _, figure := range report.Block.Figures {
			cost, err := comment.FigureCost(figure)
			if err != nil {
				return policy.Lifetime{}, fmt.Errorf("history: %w", err)
			}
			if figure.Currency != usd {
				earlier.OtherCurrency = true
				continue
			}
			earlier.USD = earlier.USD.Add(cost)
		}
	}
	return earlier, nil
}

// readCommits reads the commits' check runs, parallel at a time, and keeps
// them in the commits' order. One commit that cannot be read fails the whole.
func readCommits(ctx context.Context, host scm.History, commits []string) ([]Report, int, error) {
	type read struct {
		reports []Report
		unread  int
		err     error
	}
	reads := make([]read, len(commits))
	slots := make(chan struct{}, parallel)
	var group sync.WaitGroup
	for index, sha := range commits {
		group.Go(func() {
			slots <- struct{}{}
			defer func() { <-slots }()
			reports, unread, err := CheckRunBlocks(ctx, host, sha)
			reads[index] = read{reports, unread, err}
		})
	}
	group.Wait()
	var reports []Report
	unread := 0
	for _, each := range reads {
		if each.err != nil {
			return nil, 0, each.err
		}
		reports = append(reports, each.reports...)
		unread += each.unread
	}
	return reports, unread, nil
}

// Total adds the reports up per currency and basis. A report whose digest an
// earlier report stated is a repeat and is dropped. A report without a digest
// counts once per check run, and its figures say so.
func Total(reports []Report) ([]comment.LifetimeFigure, error) {
	type key struct{ currency, basis string }
	sums := map[key]*comment.LifetimeFigure{}
	seen := map[string]bool{}
	for _, report := range reports {
		if report.RowsSHA256 != "" {
			if seen[report.RowsSHA256] {
				continue
			}
			seen[report.RowsSHA256] = true
		}
		counted := map[key]bool{}
		for _, figure := range report.Block.Figures {
			cost, err := comment.FigureCost(figure)
			if err != nil {
				return nil, fmt.Errorf("history: %w", err)
			}
			at := key{figure.Currency, figure.CostBasis}
			sum, found := sums[at]
			if !found {
				sum = &comment.LifetimeFigure{Currency: at.currency, Basis: at.basis}
				sums[at] = sum
			}
			sum.Cost = sum.Cost.Add(cost)
			if !counted[at] {
				counted[at] = true
				sum.Reports++
				sum.Undigested = sum.Undigested || report.RowsSHA256 == ""
			}
		}
	}
	figures := make([]comment.LifetimeFigure, 0, len(sums))
	for _, sum := range sums {
		figures = append(figures, *sum)
	}
	slices.SortFunc(figures, func(a, b comment.LifetimeFigure) int {
		return cmp.Or(cmp.Compare(a.Currency, b.Currency), cmp.Compare(a.Basis, b.Basis))
	})
	return figures, nil
}
