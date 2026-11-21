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

const (
	// historyFileCommit names the one commit a history file stands for. The
	// file holds the check runs of every commit of the pull request, and the
	// history reader asks each commit once.
	historyFileCommit = "history-file"
	// baselineFileCommit names the one merged head a baseline file stands
	// for. The file holds the check runs on the heads of the merged pull
	// requests, and the baseline reader asks each head once.
	baselineFileCommit = "baseline-file"
)

// fileHistory answers the history reader from a file of result objects, one
// per line, as the check runs on a pull request's commits carry them.
type fileHistory struct {
	checks []scm.ListedCheck
}

// fileMerged answers the baseline reader from a file of result objects, one
// per line, as the check runs on the merged pull requests' heads carry them.
type fileMerged struct {
	checks []scm.ListedCheck
}

// readCheckFile reads a file of result objects for flag. Each object names
// its check run, which orders the runs as the host does. A line that is not a
// JSON object naming its check run refuses the file, with the line's number:
// the window of a baseline is the latest runs, so a run without its number
// cannot be placed. An object the history reader does not add up is left to
// it, as a check run's object is.
func readCheckFile(path, flag string) ([]scm.ListedCheck, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	var checks []scm.ListedCheck
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
		checks = append(checks, scm.ListedCheck{
			ID:   *named.CheckRunID,
			Text: "```json\n" + string(bytes.TrimSpace(line)) + "\n```",
		})
		return nil
	})
	var fault *focus.LineError
	if errors.As(err, &fault) {
		return nil, fmt.Errorf("%s line %d: %w", flag, fault.Line, fault.Err)
	}
	if err != nil {
		return nil, fmt.Errorf("%s: %w", flag, err)
	}
	return checks, nil
}

func (h *fileHistory) PullCommits(context.Context, int) ([]string, bool, error) {
	return []string{historyFileCommit}, false, nil
}

func (h *fileHistory) CheckRuns(context.Context, string, string) ([]scm.ListedCheck, error) {
	return h.checks, nil
}

func (m *fileMerged) MergedInto(context.Context, string, int) ([]scm.MergedHead, error) {
	return []scm.MergedHead{{HeadSHA: baselineFileCommit}}, nil
}

func (m *fileMerged) HeadCheckRuns(context.Context, string, string) ([]scm.ListedCheck, error) {
	return m.checks, nil
}
