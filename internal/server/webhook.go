package server

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/Costroid/costroid/internal/comment"
	"github.com/Costroid/costroid/internal/history"
	"github.com/Costroid/costroid/internal/policy"
	"github.com/Costroid/costroid/internal/scm"
	"github.com/Costroid/costroid/internal/store"
)

const (
	// webhookPath is where GitHub delivers the App's events.
	webhookPath = "/webhooks/github"
	// webhookQueueSize is how many verified deliveries wait for the worker. A
	// delivery past it answers 503 and stays unclaimed, so a redelivery is
	// processed.
	webhookQueueSize = 256
)

// GitHubApp configures POST /webhooks/github. A Config without one does not
// serve the route.
type GitHubApp struct {
	// Secret is the webhook secret GitHub signs each delivery with.
	Secret        []byte
	Installations Installations
	// Deliveries keeps the delivery ids taken. Nil keeps them in this
	// process's memory, which a restart forgets.
	Deliveries scm.DeliveryStore
	// Hosts reaches a repository as one of the App's installations. Without
	// it, an approve press is refused and logged: the service holds no App
	// key to post a check with.
	Hosts RepositoryHosts
}

// Installations keeps the App's installations: ids and names, never a token.
type Installations interface {
	AddRepositories(ctx context.Context, installation int64, account string, repositories []store.Repository) error
	RemoveRepositories(ctx context.Context, installation int64, repositories []int64) error
	SuspendInstallation(ctx context.Context, installation int64, account string, suspended bool) error
	DeleteInstallation(ctx context.Context, installation int64) error
	// Installation answers store.ErrNotFound for an installation it does not
	// hold.
	Installation(ctx context.Context, installation int64) (store.Installation, error)
}

// ApprovalHost is what the approve button needs from one repository: the pull
// request, the approver's role, costroid.yml at the head of the base branch,
// and the commit's check runs.
type ApprovalHost interface {
	scm.Host
	scm.History
	policy.Branches
}

// RepositoryHosts reaches one repository, owner/name, as one installation.
type RepositoryHosts interface {
	Repository(installation int64, repository string) (ApprovalHost, error)
}

// The reasons an approve press changes nothing. A redelivery of the same
// press would change nothing either, so its delivery stays claimed.
var (
	errNotInstalled = errors.New("the App's installations table does not hold this installation")
	errSuspended    = errors.New("the installation is suspended")
	errNoAppKey     = errors.New("this service holds no GitHub App key to post a check with")
	errNoCheckYet   = errors.New("no costroid check is on the commit yet")
	errConcluded    = errors.New("the newest costroid check already concludes so")
	errNoBaseBranch = errors.New("the check run names no pull request's base branch")
	errSettings     = errors.New("costroid.yml at the head of the base branch holds no valid approval settings")
)

// finalRefusals are the errors a redelivery cannot change.
var finalRefusals = []error{
	scm.ErrNotAnApproval, scm.ErrStaleHead, scm.ErrAuthorApproval, scm.ErrNoWriteRole, scm.ErrNoApprover, scm.ErrBotApproval,
	errNotInstalled, errNoBaseBranch, errSettings, errSuspended, errNoAppKey, errNoCheckYet, errConcluded,
}

func final(err error) bool {
	for _, refusal := range finalRefusals {
		if errors.Is(err, refusal) {
			return true
		}
	}
	return false
}

// newWebhook returns the route's handler and the queue its worker drains.
func newWebhook(config Config) (*scm.Webhook, *scm.Queue) {
	queue := scm.NewQueue(webhookQueueSize)
	deliveries := config.App.Deliveries
	if deliveries == nil {
		deliveries = scm.NewMemoryDeliveries()
	}
	return &scm.Webhook{Secret: config.App.Secret, Deliveries: deliveries, Queue: queue, Now: config.Now}, queue
}

// ProcessWebhooks applies the verified deliveries until ctx ends. It returns
// at once when the route is not served.
func (s *Server) ProcessWebhooks(ctx context.Context) {
	if s.queue == nil {
		return
	}
	s.queue.Run(ctx, s.apply)
}

// apply applies one delivery. A delivery that failed for a reason a
// redelivery can change is released, so GitHub's redelivery is processed.
func (s *Server) apply(ctx context.Context, event scm.Event) {
	logger := s.Logger.With("delivery", event.Delivery, "event", event.Name, "action", event.Action, "installation", event.Installation)
	var err error
	switch event.Name {
	case scm.EventInstallation, scm.EventInstallationRepositories:
		err = s.keepInstallation(ctx, event)
	case scm.EventCheckRun:
		err = s.pressApprove(ctx, event, logger)
	default:
		return
	}
	switch {
	case err == nil:
	case final(err):
		logger.Info("webhook delivery changed nothing", "reason", err.Error())
	default:
		logger.Error("webhook delivery not applied; a redelivery is processed", "error", err)
		if err := s.webhook.Deliveries.Release(ctx, event.Delivery); err != nil {
			logger.Error("webhook delivery not released", "error", err)
		}
	}
}

// keepInstallation applies an installation or installation_repositories
// event to the installations table. A repositories event keeps its
// installation too, so a created event the service missed is made good.
func (s *Server) keepInstallation(ctx context.Context, event scm.Event) error {
	installations := s.App.Installations
	switch event.Action {
	case scm.InstallationCreated, scm.RepositoriesAdded, scm.RepositoriesRemoved:
		if err := installations.AddRepositories(ctx, event.Installation, event.Account, kept(event.Added)); err != nil {
			return err
		}
		if len(event.Removed) == 0 {
			return nil
		}
		ids := make([]int64, len(event.Removed))
		for i, repository := range event.Removed {
			ids[i] = repository.ID
		}
		return installations.RemoveRepositories(ctx, event.Installation, ids)
	case scm.InstallationSuspended, scm.InstallationUnsuspended:
		return installations.SuspendInstallation(ctx, event.Installation, event.Account, event.Action == scm.InstallationSuspended)
	case scm.InstallationDeleted:
		return installations.DeleteInstallation(ctx, event.Installation)
	}
	return fmt.Errorf("installation action %s is not kept", event.Action)
}

func kept(repositories []scm.InstalledRepository) []store.Repository {
	list := make([]store.Repository, len(repositories))
	for i, repository := range repositories {
		list[i] = store.Repository{ID: repository.ID, FullName: repository.FullName}
	}
	return list
}

// pressApprove applies a press of the check's approve button. An account the
// repository allows to approve re-concludes the newest costroid check on the
// head, as costroid approve does for a review. Anyone else is refused, and
// nothing changes.
func (s *Server) pressApprove(ctx context.Context, event scm.Event, logger *slog.Logger) error {
	request, err := scm.RequestFrom(event)
	if err != nil {
		return err
	}
	held, err := s.App.Installations.Installation(ctx, event.Installation)
	if errors.Is(err, store.ErrNotFound) {
		return errNotInstalled
	}
	if err != nil {
		return err
	}
	if held.Suspended {
		return errSuspended
	}
	if s.App.Hosts == nil {
		return errNoAppKey
	}
	host, err := s.App.Hosts.Repository(event.Installation, event.Repository)
	if err != nil {
		return err
	}
	request.AllowAuthor, err = allowAuthor(ctx, host, event.BaseBranch)
	if err != nil {
		return err
	}
	approval, err := scm.Approve(ctx, host, request, s.Now())
	if err != nil {
		return err
	}
	id, conclusion, err := s.reconclude(ctx, host, approval, logger)
	if err != nil {
		return err
	}
	logger.Info("check run posted", "repository", event.Repository, "pull_request", approval.PullRequest, "check_run", id, "conclusion", conclusion)
	return nil
}

// allowAuthor reads approval.allow_author from costroid.yml at the head of
// the base branch, so a pull request cannot widen who approves it.
func allowAuthor(ctx context.Context, host ApprovalHost, branch string) (bool, error) {
	if branch == "" {
		return false, errNoBaseBranch
	}
	base, err := policy.BaseHead(ctx, host, branch)
	if err != nil {
		return false, err
	}
	config := policy.ReadConfig(ctx, host, policy.Commits{Base: base, PullRequest: true})
	if config.Unread != nil {
		return false, config.Unread
	}
	settings, err := policy.ParseApproval(config.Data)
	if err != nil {
		return false, fmt.Errorf("%w: %w", errSettings, err)
	}
	return settings.AllowAuthor, nil
}

// reconclude posts the newest costroid check on the approved head again, with
// the conclusion the approval gives it, and names the new check run in its
// own result object. It posts nothing when the newest check already concludes
// so.
func (s *Server) reconclude(ctx context.Context, host ApprovalHost, approval scm.Approval, logger *slog.Logger) (int64, string, error) {
	newest, err := history.Newest(ctx, host, approval.HeadSHA)
	if errors.Is(err, history.ErrNoCheck) {
		return 0, "", errNoCheckYet
	}
	if err != nil {
		return 0, "", err
	}
	approved := &comment.Approval{Approver: approval.Approver, HeadSHA: approval.HeadSHA, At: approval.At}
	block := newest.Block
	conclusion := block.ApprovedConclusion(approved)
	if block.Conclusion != nil && *block.Conclusion == conclusion {
		return 0, "", fmt.Errorf("%w: check run %d concludes %s", errConcluded, newest.CheckRun, conclusion)
	}
	render := func(id *int64) (comment.CheckOutput, error) {
		named := block
		if id != nil {
			named.CheckRunID = id
		}
		return comment.Reconclude(named, approved, newest.CheckRun)
	}
	output, err := render(nil)
	if err != nil {
		return 0, "", err
	}
	id, err := host.PostCheck(ctx, scm.CheckRun{
		Name: comment.CheckName, HeadSHA: approval.HeadSHA, Conclusion: conclusion,
		Title: output.Title, Summary: output.Summary, Text: output.Text, Completed: s.Now(),
	})
	if err != nil {
		return 0, "", err
	}
	if output, err = render(&id); err == nil {
		err = host.UpdateCheck(ctx, id, scm.CheckRun{Title: output.Title, Summary: output.Summary, Text: output.Text})
	}
	if err != nil {
		logger.Warn("check run does not name itself", "check_run", id, "error", err)
	}
	return id, conclusion, nil
}
