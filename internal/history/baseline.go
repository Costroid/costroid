package history

import (
	"context"
	"slices"

	"github.com/Costroid/costroid/internal/comment"
	"github.com/Costroid/costroid/internal/policy"
	"github.com/Costroid/costroid/internal/scm"
)

// Reports reads the reports on every commit of the pull request, the head
// included, as Read does. One commit that cannot be read fails the whole.
func Reports(ctx context.Context, host scm.History, number int, head string) ([]Report, error) {
	commits, _, err := host.PullCommits(ctx, number)
	if err != nil {
		return nil, err
	}
	if !slices.Contains(commits, head) {
		commits = append(commits, head)
	}
	reports, _, err := readCommits(ctx, host, commits)
	return reports, err
}

// RunCosts are the costs a baseline reads for own's agent: one per earlier
// run of that agent with a cost in one currency. A run counts once, at the
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
