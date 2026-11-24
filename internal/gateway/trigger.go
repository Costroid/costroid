package gateway

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"regexp"
	"strings"

	"github.com/Costroid/costroid/internal/vcs"
)

// triggeringEvent is the event a run that workflow_run starts holds, and the
// event of the run it follows must be pull_request: no other names the pull
// request a key is minted for.
const (
	triggeringEvent = "workflow_run"
	pullRequestRun  = "pull_request"
)

// digits is a run id or a run attempt as GitHub's event writes it.
var digits = regexp.MustCompile(`^[1-9][0-9]{0,19}$`)

// TriggeringRun is the run a workflow_run event follows, as GitHub's event
// names it: the run, its attempt and its head commit. A pull request's own
// run cannot edit the run that reads it, because workflow_run runs the
// default branch's workflow file.
type TriggeringRun struct {
	Binding
	// Head is the head commit GitHub's event names for the run, the pull
	// request's head and not the merge commit its own token names.
	Head string
	// PullRequest is the one pull request the event names, and Base its base
	// commit. Both are empty when the event names none or more than one.
	PullRequest string
	Base        string
	// PullRequests is every pull request the event names, in its order: one
	// for a run of a pull request's branch, two or more for a head that two
	// pull requests share, and none for a run GitHub names none for.
	PullRequests []string
}

// ReadTriggeringRun reads the run a workflow_run event follows from the event
// file GITHUB_EVENT_PATH names. It refuses an event other than workflow_run,
// a run another event than pull_request started, and a run on a head in
// another repository: a fork's run gets no key, because a run that
// workflow_run starts holds this repository's secrets.
func ReadTriggeringRun(getenv func(string) string) (TriggeringRun, error) {
	if name := getenv("GITHUB_EVENT_NAME"); name != triggeringEvent {
		return TriggeringRun{}, fmt.Errorf("gateway: a key is minted for the triggering run in a run that %s starts, and this run's event is %q", triggeringEvent, name)
	}
	own, err := ReadBinding(getenv)
	if err != nil {
		return TriggeringRun{}, err
	}
	var event struct {
		WorkflowRun *struct {
			ID             json.Number `json:"id"`
			Attempt        json.Number `json:"run_attempt"`
			Event          string      `json:"event"`
			HeadSHA        string      `json:"head_sha"`
			HeadRepository *struct {
				FullName string `json:"full_name"`
			} `json:"head_repository"`
			PullRequests []struct {
				Number json.Number `json:"number"`
				Base   struct {
					SHA string `json:"sha"`
				} `json:"base"`
			} `json:"pull_requests"`
		} `json:"workflow_run"`
	}
	path := getenv("GITHUB_EVENT_PATH")
	data, err := os.ReadFile(path)
	if err != nil {
		return TriggeringRun{}, fmt.Errorf("gateway: read the event file: %w", err)
	}
	if err := json.Unmarshal(data, &event); err != nil || event.WorkflowRun == nil {
		return TriggeringRun{}, fmt.Errorf("gateway: the event file %q holds no workflow_run", path)
	}
	followed := event.WorkflowRun
	switch {
	case followed.Event != pullRequestRun:
		return TriggeringRun{}, fmt.Errorf("gateway: the triggering run's event is %q, and a key is minted for a %s run alone", followed.Event, pullRequestRun)
	case followed.HeadRepository == nil || !strings.EqualFold(followed.HeadRepository.FullName, getenv("GITHUB_REPOSITORY")):
		return TriggeringRun{}, errors.New("gateway: the triggering run's head is in another repository, and a fork's run gets no key")
	case !digits.MatchString(followed.ID.String()) || !digits.MatchString(followed.Attempt.String()) || !vcs.IsCommit(followed.HeadSHA):
		return TriggeringRun{}, errors.New("gateway: the event names no triggering run, attempt and head commit")
	}
	run := TriggeringRun{
		Binding: Binding{Repository: own.Repository, Run: followed.ID.String(), Attempt: followed.Attempt.String()},
		Head:    followed.HeadSHA,
	}
	for _, pull := range followed.PullRequests {
		if number := pull.Number.String(); digits.MatchString(number) {
			run.PullRequests = append(run.PullRequests, number)
		}
	}
	if len(followed.PullRequests) == 1 && len(run.PullRequests) == 1 {
		run.PullRequest = run.PullRequests[0]
		if vcs.IsCommit(followed.PullRequests[0].Base.SHA) {
			run.Base = followed.PullRequests[0].Base.SHA
		}
	}
	return run, nil
}
