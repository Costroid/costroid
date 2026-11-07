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
var schemas = []string{"costroid.result/0.3", "costroid.result/0.4", "costroid.result/0.5"}

var digestPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)

// Report is one result object: an earlier run's, read from its check run, or
// this run's own.
type Report struct {
	// CheckRun is the check run that carried the object, 0 for this run's own.
	CheckRun int64
	Block    comment.Block
	// RowsSHA256 is the digest of the rows the object reports, empty when it
	// states none. No result object up to v0.5 states one.
	RowsSHA256 string
}

// Parse reads one result object. An object in a schema this release does not
// add up, with its amounts withheld or a figure left out, or with a figure
// comment.FigureCost refuses, is refused whole: a total without it would read
// as a smaller cost.
func Parse(object []byte) (Report, error) {
	var parsed struct {
		comment.Block
		RowsSHA256 string `json:"rows_sha256"`
	}
	if err := json.Unmarshal(object, &parsed); err != nil {
		return Report{}, fmt.Errorf("history: result object: %w", err)
	}
	block := parsed.Block
	switch {
	case !slices.Contains(schemas, block.Schema):
		return Report{}, fmt.Errorf("history: schema %q is not one this release adds up", string(comment.Clean(block.Schema)))
	case block.Amounts != "shown":
		return Report{}, errors.New("history: the result object withholds its amounts")
	case block.FiguresOmitted != 0:
		return Report{}, errors.New("history: the result object leaves figures out")
	case parsed.RowsSHA256 != "" && !digestPattern.MatchString(parsed.RowsSHA256):
		return Report{}, errors.New("history: rows_sha256 is not a SHA-256 digest")
	}
	for _, figure := range block.Figures {
		if _, err := comment.FigureCost(figure); err != nil {
			return Report{}, fmt.Errorf("history: %w", err)
		}
	}
	return Report{Block: block, RowsSHA256: parsed.RowsSHA256}, nil
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

// Read adds up the reports on every commit of the pull request and own, this
// run's report, whose check run is not posted yet. The head is read even when
// the host's list of commits leaves it out.
func Read(ctx context.Context, host scm.History, number int, head string, own Report) (comment.Lifetime, error) {
	commits, capped, err := host.PullCommits(ctx, number)
	if err != nil {
		return comment.Lifetime{}, err
	}
	if !slices.Contains(commits, head) {
		commits = append(commits, head)
	}
	reports, unread, err := readCommits(ctx, host, commits)
	if err != nil {
		return comment.Lifetime{}, err
	}
	figures, err := Total(append(reports, own))
	if err != nil {
		return comment.Lifetime{}, err
	}
	return comment.Lifetime{Figures: figures, Capped: capped, Unread: unread}, nil
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
