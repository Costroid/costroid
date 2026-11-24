package server

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/Costroid/costroid/internal/decimal"
	"github.com/Costroid/costroid/internal/policy"
	"github.com/Costroid/costroid/internal/scm"
	"github.com/Costroid/costroid/internal/store"
	"github.com/Costroid/costroid/internal/vcs"
)

const (
	// maxSpendLength bounds a stated spend, as the row contract bounds an
	// amount.
	maxSpendLength = decimal.MaxLength
	// maxConfigBytes bounds the costroid.yml a verdict request carries.
	maxConfigBytes = 64 << 10
	// maxMeasuredPullRequests bounds the stated count of measured pull
	// requests. Any count from the threshold up ends the period alike.
	maxMeasuredPullRequests = 1 << 20
	// maxPackChildren is how many customer packs serve runs at once. Each runs
	// in a child process of this one, which can hold up to 1 GiB before the
	// kernel stops it, and lives at most 2 seconds.
	maxPackChildren = 4
	// codePackBusy refuses a verdict whose pack found no free child in time.
	codePackBusy = "PACK_BUSY"
)

// packChildren holds a slot for each customer pack serve runs. The slots are
// the process's, as the children's memory is, so every Server shares them.
var packChildren = make(chan struct{}, maxPackChildren)

// packRetrySeconds is when a refused verdict is told to ask again: the
// deadline of a running child, by which every slot it holds is free again.
const packRetrySeconds = 2

// packWait is how long a verdict waits for a slot: the same deadline.
var packWait = packRetrySeconds * time.Second

// spendAttestation is what costroid gateway close sends once the run is over:
// the run its key was minted for and what the gateway counted on the key. The
// key's id is in the path.
type spendAttestation struct {
	Repository string `json:"repository"`
	RunID      string `json:"run_id"`
	RunAttempt string `json:"run_attempt"`
	Currency   string `json:"currency"`
	Spend      string `json:"spend"`
}

// spendFigure is a spend as the ledger keeps it: a plain decimal of zero or
// more, with at most 26 integer and 12 fractional digits. The figure is kept
// as the string sent, so a repeat is the same string, not the same number.
var spendFigure = regexp.MustCompile(`^(0|[1-9][0-9]{0,25})(\.[0-9]{1,12})?$`)

// handleSpend stores the gateway's count for a minted key, once. The token
// must be a protected branch's run that workflow_run started, and the key
// must be attested as minted for the run the body names. A sender cannot
// state spend from a run a pull request can edit, and a figure, once kept, is
// never replaced.
func (s *Server) handleSpend(w http.ResponseWriter, r *http.Request) {
	who, err := s.authenticate(r)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	attested, err := s.readSpend(w, r, who)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	err = s.Spend.AttestSpend(r.Context(), who.tenant, attested)
	switch {
	case errors.Is(err, store.ErrNotMinted):
		s.fail(w, r, refuse(http.StatusConflict, codeNotAttested,
			fmt.Sprintf("The key is not attested for run %s, attempt %s.", attested.Key.RunID, attested.Key.RunAttempt)))
	case errors.Is(err, store.ErrAttestingRunUsed):
		s.fail(w, r, refuse(http.StatusConflict, codeSpendHeld,
			fmt.Sprintf("Run %s, attempt %s, has written a spend already.", attested.AttestingRunID, attested.AttestingRunAttempt)))
	case errors.Is(err, store.ErrSpendHeld):
		s.fail(w, r, refuse(http.StatusConflict, codeSpendHeld, "The key holds a spend already."))
	case err != nil:
		s.fail(w, r, err)
	default:
		w.WriteHeader(http.StatusNoContent)
	}
}

func (s *Server) readSpend(w http.ResponseWriter, r *http.Request, who caller) (store.AttestedSpend, error) {
	id := who.identity
	if err := s.attestingRun(id); err != nil {
		return store.AttestedSpend{}, err
	}
	if s.Spend == nil {
		return store.AttestedSpend{}, refuse(http.StatusNotImplemented, codeNoSpend, "This ledger does not keep the gateway's spend.")
	}
	keyID := r.PathValue("key")
	if !gatewayKeyID.MatchString(keyID) {
		return store.AttestedSpend{}, refuse(http.StatusBadRequest, codeBadQuery, "The key id is not 64 hexadecimal characters.")
	}
	body, err := readBody(w, r)
	if err != nil {
		return store.AttestedSpend{}, err
	}
	var stated spendAttestation
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&stated); err != nil || stated.Currency != "USD" || !runTag.MatchString(stated.RunID) || !runAttempt.MatchString(stated.RunAttempt) {
		return store.AttestedSpend{}, refuse(http.StatusBadRequest, codeMalformed, "The body is not a spend attestation in USD for a run_id and a run_attempt.")
	}
	if len(stated.Spend) > maxSpendLength {
		return store.AttestedSpend{}, refuse(http.StatusBadRequest, codeMalformed, fmt.Sprintf("spend is longer than %d characters.", maxSpendLength))
	}
	if !spendFigure.MatchString(stated.Spend) {
		return store.AttestedSpend{}, refuse(http.StatusBadRequest, codeMalformed, "spend is not a decimal of at most 12 fractional digits, zero or more.")
	}
	named := attestation{Repository: stated.Repository, RunID: stated.RunID}
	if err := namedRun(named, id); err != nil {
		return store.AttestedSpend{}, err
	}
	return store.AttestedSpend{
		Key:            store.MintedCredential{KeySHA256: keyID, Repository: id.Repository, RunID: stated.RunID, RunAttempt: stated.RunAttempt},
		Spend:          stated.Spend,
		AttestingRunID: id.RunID, AttestingRunAttempt: id.RunAttempt,
	}, nil
}

// verdictRequest states what the service cannot read itself, because it holds
// no SCM token: costroid.yml at the base commit, when the workflow reached the
// base branch, and how many of the repository's pull requests have a measured
// cost. The caller reads each with the repository's own token.
type verdictRequest struct {
	// Config is costroid.yml in base64. Absent, the base commit holds none.
	Config *string `json:"config"`
	// ConfigRef is the base commit the file was read at.
	ConfigRef string `json:"config_ref"`
	// WorkflowAdded is when the workflow reached the base branch, RFC 3339.
	// Empty, the start is unknown and the period runs.
	WorkflowAdded     string `json:"workflow_added"`
	WorkflowNotOnBase bool   `json:"workflow_not_on_base"`
	// MeasuredPullRequests can only end the warn-only period early.
	MeasuredPullRequests int `json:"measured_pull_requests"`
	// Installation is the GitHub App's installation serve reads the
	// repository through. With it, serve reads costroid.yml at config_ref
	// itself, and when config_ref is the head of the pull request's base
	// branch, reads the policy pack there and runs it. Otherwise a pack the
	// stated costroid.yml names is not read.
	Installation int64 `json:"installation"`
}

type verdictAnswer struct {
	Repository  string         `json:"repository"`
	PullRequest string         `json:"pull_request"`
	Budget      budgetView     `json:"budget"`
	Policy      policy.Verdict `json:"policy"`
	// OverrideRecord says whether the verdict reached the ledger's override
	// record, and why not when it did not. Only a verdict that reached it
	// records a failure or demotes a rule. A reason that config_ref is not
	// the base's head, or was not read, is also why no pack was read and why
	// the conclusion is neutral at best.
	OverrideRecord overrideReach `json:"override_record"`
}

// overrideReach is whether a verdict reached the override record. The list is
// closed.
type overrideReach string

const (
	// reachRecorded: the verdict read and wrote the record.
	reachRecorded overrideReach = "recorded"
	// reachNoRecord: config_ref is the head of the pull request's base
	// branch, and this ledger keeps no override record.
	reachNoRecord overrideReach = "no_record"
	// reachConfigUnread: the service did not read costroid.yml at config_ref
	// itself, through the App's installation, or read other bytes than the
	// request states.
	reachConfigUnread overrideReach = "config_unread"
	// reachPullUnread: the host did not answer the pull request's head, its
	// base branch or that branch's head.
	reachPullUnread overrideReach = "pull_unread"
	// reachNotBase: config_ref is not the head of the pull request's base
	// branch as the host states it.
	reachNotBase overrideReach = "not_base"
)

func malformedVerdict(message string) *refusal {
	return refuse(http.StatusBadRequest, codeMalformed, message)
}

func (s *Server) readVerdictRequest(w http.ResponseWriter, r *http.Request) (verdictRequest, policy.ConfigFile, policy.Start, error) {
	body, err := readBody(w, r)
	if err != nil {
		return verdictRequest{}, policy.ConfigFile{}, policy.Start{}, err
	}
	var stated verdictRequest
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&stated); err != nil {
		return verdictRequest{}, policy.ConfigFile{}, policy.Start{}, malformedVerdict("The body is not a verdict request.")
	}
	config := policy.ConfigFile{Ref: stated.ConfigRef, FromBase: stated.ConfigRef != ""}
	if stated.ConfigRef != "" && !vcs.IsCommit(stated.ConfigRef) {
		return verdictRequest{}, policy.ConfigFile{}, policy.Start{}, malformedVerdict("config_ref is not a commit.")
	}
	if stated.Config != nil {
		data, err := base64.StdEncoding.DecodeString(*stated.Config)
		if err != nil || len(data) > maxConfigBytes {
			return verdictRequest{}, policy.ConfigFile{}, policy.Start{}, malformedVerdict("config is not costroid.yml in base64, at most 64 KiB.")
		}
		config.Data, config.Found = data, true
	}
	start := policy.Start{NotOnBase: stated.WorkflowNotOnBase}
	if stated.WorkflowAdded != "" {
		start.At, err = time.Parse(time.RFC3339, stated.WorkflowAdded)
		if err != nil {
			return verdictRequest{}, policy.ConfigFile{}, policy.Start{}, malformedVerdict("workflow_added is not an RFC 3339 time.")
		}
	}
	if stated.MeasuredPullRequests < 0 || stated.MeasuredPullRequests > maxMeasuredPullRequests {
		return verdictRequest{}, policy.ConfigFile{}, policy.Start{}, malformedVerdict("measured_pull_requests is not a count.")
	}
	return stated, config, start, nil
}

// handleVerdict runs the policy pack over the pull request's rows in this
// ledger. Rows a CI token proved are token-verified, and a budget rule passes
// on them only when the gateway's own count or a provider's report stands
// behind their spend.
func (s *Server) handleVerdict(w http.ResponseWriter, r *http.Request) {
	who, err := s.authenticate(r)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	repo, pr := r.URL.Query().Get("repo"), r.PathValue("pr")
	if !repoTag.MatchString(repo) || !prTag.MatchString(pr) {
		s.fail(w, r, refuse(http.StatusBadRequest, codeBadQuery, "repo must be a lower-case host and path, and the pull request a number."))
		return
	}
	if who.identity != nil && who.identity.Repository != repo {
		s.fail(w, r, refuse(http.StatusForbidden, codeWrongRepo, "This CI token reads its own repository only."))
		return
	}
	stated, config, start, err := s.readVerdictRequest(w, r)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	lines, err := s.Ledger.PullRequest(r.Context(), who.tenant, repo, pr)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	measured, err := s.measuredSpend(r.Context(), who.tenant, repo, pr)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	budget := budgetOf(lines, measured)
	base, reach := s.readBase(r.Context(), repo, pr, stated, config)
	request := policy.LedgerRequest{
		Lines:                ledgerLines(lines),
		Config:               config,
		Pack:                 readPack(r.Context(), base, reach),
		Start:                start,
		MeasuredPullRequests: stated.MeasuredPullRequests,
		At:                   s.Now(),
	}
	if reach == reachRecorded {
		request.Record, reach = s.verdictRecord(who.tenant, repo, pr, base)
	}
	request.Evidence, request.MeasuredUSD = evidenceOf(budget), measured.usd
	if request.Pack.Read() {
		release, admitted := admitPack(r.Context())
		if !admitted {
			w.Header().Set("Retry-After", strconv.Itoa(packRetrySeconds))
			s.fail(w, r, refuse(http.StatusServiceUnavailable, codePackBusy, fmt.Sprintf("The service runs %d policy packs at once, and all %d were busy. Retry in %d seconds.", maxPackChildren, maxPackChildren, packRetrySeconds)))
			return
		}
		defer release()
	}
	verdict, err := policy.EvaluateLedger(r.Context(), request)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, verdictAnswer{Repository: repo, PullRequest: pr, Budget: budget, Policy: heldOffTheBase(verdict, reach), OverrideRecord: reach})
}

// heldOffTheBase holds a verdict to neutral at best unless its settings are
// ones serve read itself at the head of the pull request's base branch.
// Settings a caller states, or that serve read at another commit, are the
// caller's to choose, a looser budget or no pack among them. A failure stays
// a failure, every result stays as computed, and override_record says why.
// A ledger that keeps no override record still read its settings at the
// base, so no_record holds nothing.
func heldOffTheBase(verdict policy.Verdict, reach overrideReach) policy.Verdict {
	if verdict.Conclusion == policy.Success && reach != reachRecorded && reach != reachNoRecord {
		verdict.Conclusion = policy.Neutral
	}
	return verdict
}

// admitPack takes a slot for one customer pack's child, waiting at most
// packWait, and returns what frees it. A verdict with no slot gets no answer
// rather than one without its pack: a pack can only raise a conclusion, so a
// verdict without it could read looser than the repository's own policy.
func admitPack(ctx context.Context) (func(), bool) {
	timer := time.NewTimer(packWait)
	defer timer.Stop()
	select {
	case packChildren <- struct{}{}:
		return func() { <-packChildren }, true
	case <-timer.C:
	case <-ctx.Done():
	}
	return nil, false
}

// baseRead is costroid.yml as serve read it itself, through a GitHub App's
// installation, the host it read it through, and the pull request as that
// host answered it.
type baseRead struct {
	host   ApprovalHost
	config policy.ConfigFile
	pull   scm.PullRequest
}

// readBase reads costroid.yml at config_ref through the GitHub App's
// installation the request names, then the pull request and the head of its
// base branch. It answers reachRecorded only when it found the bytes the
// caller stated, at the head of the base branch as the host states it, and
// otherwise why not. The installation must be held, not suspended, and reach
// the repository. Only a verdict on a file serve read this way runs the pack
// the file names and reaches the ledger's override record: a caller could
// state a looser file than the base holds, or name another commit whose file
// or pack is looser.
func (s *Server) readBase(ctx context.Context, repo, pr string, stated verdictRequest, config policy.ConfigFile) (baseRead, overrideReach) {
	name, onGitHub := strings.CutPrefix(repo, "github.com/")
	if stated.Installation <= 0 || !onGitHub || s.App == nil || s.App.Hosts == nil {
		return baseRead{}, reachConfigUnread
	}
	held, err := s.App.Installations.Installation(ctx, stated.Installation)
	if err != nil || held.Suspended {
		return baseRead{}, reachConfigUnread
	}
	index := slices.IndexFunc(held.Repositories, func(repository store.Repository) bool { return strings.EqualFold(repository.FullName, name) })
	if index < 0 {
		return baseRead{}, reachConfigUnread
	}
	host, err := s.App.Hosts.Repository(stated.Installation, held.Repositories[index].FullName)
	if err != nil {
		return baseRead{}, reachConfigUnread
	}
	own := policy.ReadConfig(ctx, host, policy.Commits{Base: stated.ConfigRef, PullRequest: true})
	if own.Unread != nil || own.Found != config.Found || !bytes.Equal(own.Data, config.Data) {
		return baseRead{}, reachConfigUnread
	}
	pull, reach := onBaseHead(ctx, host, pr, own.Ref)
	if reach != reachRecorded {
		return baseRead{}, reach
	}
	return baseRead{host: host, config: own, pull: pull}, reachRecorded
}

// onBaseHead reads the pull request through host, and answers reachRecorded
// when ref is the head of its base branch as the host states it. Otherwise
// it says why not: the host did not answer the pull request's head, its base
// branch or that branch's head, or ref is another commit.
func onBaseHead(ctx context.Context, host ApprovalHost, pr, ref string) (scm.PullRequest, overrideReach) {
	number, err := strconv.Atoi(pr)
	if err != nil {
		return scm.PullRequest{}, reachPullUnread
	}
	pull, err := host.PullRequest(ctx, number)
	if err != nil || !vcs.IsCommit(pull.HeadSHA) {
		return scm.PullRequest{}, reachPullUnread
	}
	head, err := policy.BaseHead(ctx, host, pull.BaseBranch)
	if err != nil {
		return scm.PullRequest{}, reachPullUnread
	}
	if ref != head {
		return scm.PullRequest{}, reachNotBase
	}
	return pull, reachRecorded
}

// readPack reads the customer pack the costroid.yml serve read names, at the
// head of the pull request's base branch, never at a commit only a caller
// states. A verdict on a file serve did not read there reads no pack, and
// states customer_pack pack_unread.
func readPack(ctx context.Context, base baseRead, reach overrideReach) policy.Pack {
	if reach != reachRecorded || base.config.PackPath() == "" {
		return policy.Pack{}
	}
	return policy.ReadPack(ctx, base.host, base.config)
}

// evidenceOf names what stands behind the budget's spend, for the pack.
func evidenceOf(budget budgetView) policy.Evidence {
	switch budget.Basis {
	case budgetBasisGateway:
		return policy.GatewayMeasured
	case budgetBasisReconciled:
		return policy.Reconciled
	}
	return policy.AgentCounts
}

// ledgerLines hands the pack every agent's line of the pull request, eligible
// or not. A line the ledger did not attribute can show spend over a limit, and
// keeps any rule from passing. An estimate's line is not an agent's, so it
// never raises or satisfies a budget.
func ledgerLines(lines []store.Line) []policy.LedgerLine {
	out := make([]policy.LedgerLine, 0, len(lines))
	for i := range lines {
		line := &lines[i]
		if !agentLine(line) {
			continue
		}
		out = append(out, policy.LedgerLine{
			Currency:   line.Currency,
			ListCost:   line.ListCost,
			Rows:       line.Rows,
			Model:      line.Model,
			Verified:   line.RunIdentity == identityToken,
			Attributed: line.AttributionGrade == gradeExact || line.AttributionGrade == gradeWindowed,
		})
	}
	return out
}

// overrideStore is a ledger's record of the failing verdicts serve answered,
// the approvals it verified and where each rule's demotion stands. Only serve
// writes it, and only from what it read from the host itself.
type overrideStore interface {
	AddFailure(ctx context.Context, tenant store.TenantID, verdict store.FailingVerdict) error
	AddApproval(ctx context.Context, tenant store.TenantID, approval store.PullRequestApproval) error
	Overrides(ctx context.Context, tenant store.TenantID, query store.OverrideQuery) (store.OverrideCount, error)
	Demotion(ctx context.Context, tenant store.TenantID, repository, branch, rule string) (store.RuleDemotion, bool, error)
	KeepDemotion(ctx context.Context, tenant store.TenantID, demotion store.RuleDemotion) (store.RuleDemotion, error)
}

// storeOverrides is a Postgres ledger's override and demotion records.
type storeOverrides struct {
	store.OverrideRecord
	demotions store.DemotionRecord
}

func (s storeOverrides) Demotion(ctx context.Context, tenant store.TenantID, repository, branch, rule string) (store.RuleDemotion, bool, error) {
	return s.demotions.Demotion(ctx, tenant, repository, branch, rule)
}

func (s storeOverrides) KeepDemotion(ctx context.Context, tenant store.TenantID, demotion store.RuleDemotion) (store.RuleDemotion, error) {
	return s.demotions.Keep(ctx, tenant, demotion)
}

// overrides is the ledger's override record, nil for a ledger that keeps
// none. With none, nothing is recorded and no rule is demoted.
func (s *Server) overrides() overrideStore {
	switch ledger := s.Ledger.(type) {
	case *store.Store:
		return storeOverrides{OverrideRecord: ledger.OverrideRecord(), demotions: ledger.DemotionRecord()}
	case overrideStore:
		return ledger
	}
	return nil
}

// verdictRecord is the override record of the pull request a verdict judges,
// on its head commit as the host answered it, for a verdict readBase found at
// the head of the base branch. It is nil when the ledger keeps none. A
// verdict without one records nothing and demotes nothing.
func (s *Server) verdictRecord(tenant store.TenantID, repo, pr string, base baseRead) (policy.OverrideLedger, overrideReach) {
	keeper := s.overrides()
	if keeper == nil {
		return nil, reachNoRecord
	}
	return judgedRecord{keeper: keeper, tenant: tenant, repo: repo, branch: base.pull.BaseBranch, pr: pr, head: base.pull.HeadSHA}, reachRecorded
}

// judgedRecord reads and writes the override record for one pull request,
// which its own counts never include, at the base branch it merges into: the
// record of a rule is of one base branch, whose costroid.yml sets its limit
// and whether it can be demoted.
type judgedRecord struct {
	keeper                 overrideStore
	tenant                 store.TenantID
	repo, branch, pr, head string
}

func (r judgedRecord) AddFailure(ctx context.Context, rule string, limit decimal.Decimal, at time.Time) error {
	return r.keeper.AddFailure(ctx, r.tenant, store.FailingVerdict{
		Repository: r.repo, BaseBranch: r.branch, PullRequest: r.pr, HeadSHA: r.head, Rule: rule, Limit: limit, At: at,
	})
}

func (r judgedRecord) Overrides(ctx context.Context, rule string, limit decimal.Decimal, rearmed, at time.Time) (int, int, int, error) {
	count, err := r.keeper.Overrides(ctx, r.tenant, store.OverrideQuery{
		Repository: r.repo, BaseBranch: r.branch, PullRequest: r.pr, Rule: rule, Limit: limit, Rearmed: rearmed, At: at,
	})
	return count.Failing, count.Approved, count.Approvers, err
}

func (r judgedRecord) Demotion(ctx context.Context, rule string) (policy.Demotion, bool, error) {
	kept, found, err := r.keeper.Demotion(ctx, r.tenant, r.repo, r.branch, rule)
	return policyDemotion(kept), found, err
}

func (r judgedRecord) KeepDemotion(ctx context.Context, rule string, demotion policy.Demotion) (policy.Demotion, error) {
	kept, err := r.keeper.KeepDemotion(ctx, r.tenant, store.RuleDemotion{
		Repository: r.repo, BaseBranch: r.branch, Rule: rule, Limit: demotion.Limit, Rearmed: demotion.Rearmed, Off: demotion.Off, From: demotion.From,
		Failing: demotion.Crossed.Failing, Approved: demotion.Crossed.Approved, Approvers: demotion.Crossed.Approvers,
	})
	return policyDemotion(kept), err
}

func policyDemotion(kept store.RuleDemotion) policy.Demotion {
	return policy.Demotion{
		Limit: kept.Limit, Rearmed: kept.Rearmed, From: kept.From, Off: kept.Off,
		Crossed: policy.Overrides{Failing: kept.Failing, Approved: kept.Approved, Approvers: kept.Approvers},
	}
}
