package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"

	"github.com/Costroid/costroid/internal/focus"
	"github.com/Costroid/costroid/internal/scm"
)

// historyFileCommit names the one commit a history file stands for. The file
// holds the check runs of every commit of the pull request, and the history
// reader asks each commit once.
const historyFileCommit = "history-file"

// fileHistory answers the history reader from a file of result objects, one
// per line, as the check runs on a pull request's commits carry them. Each
// object names its check run, which orders the runs as the host does.
type fileHistory struct {
	checks []scm.ListedCheck
}

// readHistoryFile reads the file. A line that is not a JSON object naming its
// check run refuses the file, with the line's number: the window of a
// baseline is the latest runs, so a run without its number cannot be placed.
// An object the history reader does not add up is left to it, which skips the
// object as the report skips an unreadable check run.
func readHistoryFile(path string) (*fileHistory, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	history := &fileHistory{}
	err = focus.EachLine(file, func(line []byte) error {
		var named struct {
			CheckRunID *int64 `json:"check_run_id"`
		}
		if err := json.Unmarshal(line, &named); err != nil {
			return errors.New("not a result object")
		}
		if named.CheckRunID == nil || *named.CheckRunID < 1 {
			return errors.New("the result object names no check run")
		}
		history.checks = append(history.checks, scm.ListedCheck{
			ID:   *named.CheckRunID,
			Text: "```json\n" + string(bytes.TrimSpace(line)) + "\n```",
		})
		return nil
	})
	var fault *focus.LineError
	if errors.As(err, &fault) {
		return nil, fmt.Errorf("--history line %d: %w", fault.Line, fault.Err)
	}
	if err != nil {
		return nil, fmt.Errorf("--history: %w", err)
	}
	return history, nil
}

func (h *fileHistory) PullCommits(context.Context, int) ([]string, bool, error) {
	return []string{historyFileCommit}, false, nil
}

func (h *fileHistory) CheckRuns(context.Context, string, string) ([]scm.ListedCheck, error) {
	return h.checks, nil
}
