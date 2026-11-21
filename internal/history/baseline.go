package history

import (
	"context"
	"fmt"
	"slices"

	"github.com/Costroid/costroid/internal/comment"
	"github.com/Costroid/costroid/internal/policy"
	"github.com/Costroid/costroid/internal/scm"
)

// MergedWindow is how many of the latest pull requests merged into the base
// branch a baseline reads, each at its head commit. Thirty leave room for
// pull requests whose runs join no baseline beside the window of 20 runs.
const MergedWindow = 30

// Merged reads the reports on the heads of the latest MergedWindow pull
// requests merged into base, in one request for the list and one for each
// head. Pull request number, the one the verdict is about, never counts,
// whatever its state, and nor does a merged pull request whose head is head:
// that commit's check runs are this pull request's own. A number of 0 names
// no pull request. A baseline read from
// part of its window could hold other runs than the latest, so any part that
// cannot be read fails the whole: the list, a head's check runs, or a
// costroid check run whose object Parse refuses.
func Merged(ctx context.Context, host scm.Merged, base string, number int, head string) ([]Report, error) {
	pulls, err := host.MergedInto(ctx, base, MergedWindow)
	if err != nil {
		return nil, fmt.Errorf("history: list the pull requests merged into %s: %w", base, err)
	}
	pulls = slices.DeleteFunc(pulls, func(pull scm.MergedHead) bool {
		return number > 0 && pull.Number == number || pull.HeadSHA == head
	})
	heads := make([]string, len(pulls))
	for index, pull := range pulls {
		heads[index] = pull.HeadSHA
	}
	reports, _, err := readEach(ctx, heads, func(ctx context.Context, sha string) ([]Report, int, error) {
		checks, err := host.HeadCheckRuns(ctx, sha, comment.CheckName)
		if err != nil {
			return nil, 0, fmt.Errorf("history: read the check runs on %s: %w", sha, err)
		}
		reports, unread := checkReports(checks)
		if unread > 0 {
			return nil, 0, fmt.Errorf("history: %d costroid check runs on %s hold no result object this release reads", unread, sha)
		}
		return reports, 0, nil
	})
	return reports, err
}

// RunCosts are the costs a baseline reads for own's agent: one per earlier
// run of that agent with a cost in one currency. Every run counts, not a pull
// request's last alone: the window is the agent's latest runs, so a pull
// request whose agent ran three times adds three. A run counts once, at the
// first check run that reported its rows, so an approval that posts the
// report again adds nothing. A report that states no rows digest may repeat
// another, a report on own's rows is this run, and a run that names no agent,
// several agents or another agent, or whose figures span two currencies, has
// no single cost of this agent's: none of them counts. own naming no single
// agent gives no costs.
func RunCosts(reports []Report, own Report) ([]policy.RunCost, error) {
	agents := own.Block.Data.Agents
	if len(agents) != 1 {
		return nil, nil
	}
	first := map[string]Report{}
	for _, report := range reports {
		digest := report.RowsSHA256
		if digest == "" || digest == own.RowsSHA256 {
			continue
		}
		if earlier, seen := first[digest]; !seen || report.CheckRun < earlier.CheckRun {
			first[digest] = report
		}
	}
	var costs []policy.RunCost
	for _, report := range first {
		if !slices.Equal(report.Block.Data.Agents, agents) {
			continue
		}
		cost, found, err := runCost(report.Block.Figures)
		if err != nil {
			return nil, err
		}
		if found {
			cost.CheckRun = report.CheckRun
			costs = append(costs, cost)
		}
	}
	return costs, nil
}

// runCost adds a run's figures up at list in their one currency. A run with
// no figure, or with figures in two currencies, has no single cost.
func runCost(figures []comment.BlockFigure) (policy.RunCost, bool, error) {
	if len(figures) == 0 {
		return policy.RunCost{}, false, nil
	}
	cost := policy.RunCost{Currency: figures[0].Currency}
	for _, figure := range figures {
		if figure.Currency != cost.Currency {
			return policy.RunCost{}, false, nil
		}
		amount, err := comment.FigureCost(figure)
		if err != nil {
			return policy.RunCost{}, false, err
		}
		cost.Cost = cost.Cost.Add(amount)
	}
	return cost, true, nil
}
