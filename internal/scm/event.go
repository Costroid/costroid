package scm

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/Costroid/costroid/internal/vcs"
)

// The events the App queues. Every other event is acknowledged and dropped.
const (
	EventPullRequest              = "pull_request"
	EventCheckRun                 = "check_run"
	EventInstallation             = "installation"
	EventInstallationRepositories = "installation_repositories"
)

// The installation actions the App queues. Each changes what the App's
// installations table holds.
const (
	InstallationCreated     = "created"
	InstallationDeleted     = "deleted"
	InstallationSuspended   = "suspend"
	InstallationUnsuspended = "unsuspend"
	RepositoriesAdded       = "added"
	RepositoriesRemoved     = "removed"
)

// installationActions are the actions queued for each installation event.
var installationActions = map[string][]string{
	EventInstallation:             {InstallationCreated, InstallationDeleted, InstallationSuspended, InstallationUnsuspended},
	EventInstallationRepositories: {RepositoriesAdded, RepositoriesRemoved},
}

const (
	// ApproveAction is the identifier of the check's approve button.
	ApproveAction = "approve"
	// requestedAction is the only check_run action the App queues. The rest
	// include the App's own check being created.
	requestedAction = "requested_action"
)

// ErrIgnoredEvent reports an event the App does not process.
var ErrIgnoredEvent = errors.New("scm: event not processed")

var (
	loginPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9-]{0,38}(\[bot\])?$`)
	// accountPattern is a person's or an organization's login, which an App
	// is installed on.
	accountPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9-]{0,38}$`)
	actionPattern  = regexp.MustCompile(`^[a-z_]{1,64}$`)
	buttonPattern  = regexp.MustCompile(`^[A-Za-z0-9_-]{1,20}$`)
	branchPattern  = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._/+-]{0,254}$`)
)

// InstalledRepository is one repository an installation reaches: its id and
// owner/name.
type InstalledRepository struct {
	ID       int64
	FullName string
}

// Event is the part of a webhook the App acts on. Every string passed a check
// against its own format, because the rest of the App treats them as safe.
type Event struct {
	Delivery        string
	Name            string
	Action          string
	Installation    int64
	Repository      string
	Sender          string
	PullRequest     int
	HeadSHA         string
	RequestedAction string
	// BaseBranch is the base branch of the pull request a check_run names,
	// empty when the payload states none in its format.
	BaseBranch string
	// Account is the login of the account an installation event's
	// installation belongs to.
	Account string
	// Added and Removed are the repositories an installation event gives the
	// installation and takes from it. A created installation lists its
	// repositories in Added.
	Added, Removed []InstalledRepository
	// At is the instant the signed payload states for this event. It is zero
	// for a check_run, whose payload holds no instant of the click.
	At time.Time
}

type repositoryPayload struct {
	ID       int64  `json:"id"`
	FullName string `json:"full_name"`
}

type eventPayload struct {
	Action       string `json:"action"`
	Installation *struct {
		ID      int64 `json:"id"`
		Account *struct {
			Login string `json:"login"`
		} `json:"account"`
	} `json:"installation"`
	Repository *struct {
		FullName string `json:"full_name"`
	} `json:"repository"`
	Sender *struct {
		Login string `json:"login"`
	} `json:"sender"`
	PullRequest *struct {
		Number    int       `json:"number"`
		UpdatedAt time.Time `json:"updated_at"`
		Head      struct {
			SHA string `json:"sha"`
		} `json:"head"`
	} `json:"pull_request"`
	CheckRun *struct {
		HeadSHA      string `json:"head_sha"`
		PullRequests []struct {
			Number int `json:"number"`
			Base   struct {
				Ref string `json:"ref"`
			} `json:"base"`
		} `json:"pull_requests"`
	} `json:"check_run"`
	RequestedAction *struct {
		Identifier string `json:"identifier"`
	} `json:"requested_action"`
	Repositories        []repositoryPayload `json:"repositories"`
	RepositoriesAdded   []repositoryPayload `json:"repositories_added"`
	RepositoriesRemoved []repositoryPayload `json:"repositories_removed"`
}

// ParseEvent reads a verified webhook body.
func ParseEvent(name string, body []byte) (Event, error) {
	_, installation := installationActions[name]
	if name != EventPullRequest && name != EventCheckRun && !installation {
		return Event{}, ErrIgnoredEvent
	}
	var payload eventPayload
	if err := json.Unmarshal(body, &payload); err != nil {
		return Event{}, fmt.Errorf("scm: parse %s: %w", name, err)
	}
	if installation {
		return payload.installationChange(name)
	}
	event, err := payload.common(name)
	if err != nil {
		return Event{}, err
	}
	if name == EventPullRequest {
		return payload.pullRequest(event)
	}
	return payload.checkRun(event)
}

func (p eventPayload) common(name string) (Event, error) {
	switch {
	case !actionPattern.MatchString(p.Action):
		return Event{}, fmt.Errorf("scm: %s has no valid action", name)
	case p.Installation == nil || p.Installation.ID <= 0:
		return Event{}, fmt.Errorf("scm: %s has no installation", name)
	case p.Repository == nil || !repositoryPattern.MatchString(p.Repository.FullName):
		return Event{}, fmt.Errorf("scm: %s has no valid repository", name)
	case p.Sender == nil || !loginPattern.MatchString(p.Sender.Login):
		return Event{}, fmt.Errorf("scm: %s has no valid sender", name)
	}
	return Event{
		Name:         name,
		Action:       p.Action,
		Installation: p.Installation.ID,
		Repository:   p.Repository.FullName,
		Sender:       p.Sender.Login,
	}, nil
}

func (p eventPayload) pullRequest(event Event) (Event, error) {
	pr := p.PullRequest
	if pr == nil || pr.Number <= 0 || !vcs.IsCommit(pr.Head.SHA) || pr.UpdatedAt.IsZero() {
		return Event{}, errors.New("scm: pull_request has no valid pull request")
	}
	event.PullRequest = pr.Number
	event.HeadSHA = pr.Head.SHA
	event.At = pr.UpdatedAt
	return event, nil
}

func (p eventPayload) checkRun(event Event) (Event, error) {
	run := p.CheckRun
	if run == nil || !vcs.IsCommit(run.HeadSHA) {
		return Event{}, errors.New("scm: check_run has no valid check run")
	}
	if p.Action != requestedAction {
		return Event{}, ErrIgnoredEvent
	}
	event.HeadSHA = run.HeadSHA
	if len(run.PullRequests) > 0 {
		event.PullRequest = run.PullRequests[0].Number
		if base := run.PullRequests[0].Base.Ref; branchPattern.MatchString(base) && !strings.Contains(base, "..") {
			event.BaseBranch = base
		}
	}
	if event.PullRequest < 0 {
		return Event{}, errors.New("scm: check_run names an invalid pull request")
	}
	if p.RequestedAction != nil {
		if !buttonPattern.MatchString(p.RequestedAction.Identifier) {
			return Event{}, errors.New("scm: check_run has no valid requested action")
		}
		event.RequestedAction = p.RequestedAction.Identifier
	}
	return event, nil
}

// installationChange reads an installation or installation_repositories
// event. Neither names a repository or a pull request of its own.
func (p eventPayload) installationChange(name string) (Event, error) {
	if !actionPattern.MatchString(p.Action) {
		return Event{}, fmt.Errorf("scm: %s has no valid action", name)
	}
	if !slices.Contains(installationActions[name], p.Action) {
		return Event{}, ErrIgnoredEvent
	}
	switch {
	case p.Installation == nil || p.Installation.ID <= 0:
		return Event{}, fmt.Errorf("scm: %s has no installation", name)
	case p.Installation.Account == nil || !accountPattern.MatchString(p.Installation.Account.Login):
		return Event{}, fmt.Errorf("scm: %s has no valid account", name)
	}
	added, removed := p.Repositories, []repositoryPayload(nil)
	if name == EventInstallationRepositories {
		added, removed = p.RepositoriesAdded, p.RepositoriesRemoved
	}
	event := Event{Name: name, Action: p.Action, Installation: p.Installation.ID, Account: p.Installation.Account.Login}
	var err error
	if event.Added, err = installed(name, added); err != nil {
		return Event{}, err
	}
	if event.Removed, err = installed(name, removed); err != nil {
		return Event{}, err
	}
	return event, nil
}

func installed(name string, listed []repositoryPayload) ([]InstalledRepository, error) {
	repositories := make([]InstalledRepository, 0, len(listed))
	for _, repository := range listed {
		if repository.ID <= 0 || !repositoryPattern.MatchString(repository.FullName) {
			return nil, fmt.Errorf("scm: %s lists a repository with no valid id or name", name)
		}
		repositories = append(repositories, InstalledRepository(repository))
	}
	return repositories, nil
}
