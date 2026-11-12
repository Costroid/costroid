package server

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/Costroid/costroid/internal/decimal"
	"github.com/Costroid/costroid/internal/focus"
	"github.com/Costroid/costroid/internal/policy"
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
)

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

// handleSpend stores the gateway's count for a minted key. The token must come
// from a job in the gateway's environment, name the run the attestation names,
// and that run must be the one the key was attested as minted for. A sender
// cannot state spend for a key it did not mint.
func (s *Server) handleSpend(w http.ResponseWriter, r *http.Request) {
	who, err := s.authenticate(r)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	key, err := s.readSpend(w, r, who)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	minted, err := s.Ledger.MintedCredentials(r.Context(), who.tenant, []string{key.credential.KeySHA256})
	if err != nil {
		s.fail(w, r, err)
		return
	}
	stored, found := minted[key.credential.KeySHA256]
	if !found || !stored.SameRun(key.credential) {
		s.fail(w, r, refuse(http.StatusConflict, codeJoinKey, "The key is not attested as minted for this run."))
		return
	}
	err = s.Spend.AttestSpend(r.Context(), who.tenant, stored, key.usd)
	if errors.Is(err, store.ErrAttestedElsewhere) || errors.Is(err, store.ErrNotMinted) {
		s.fail(w, r, refuse(http.StatusConflict, codeJoinKey, "The key is not attested as minted for this run."))
		return
	}
	if err != nil {
		s.fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

type statedSpend struct {
	credential store.MintedCredential
	usd        decimal.Decimal
}

func (s *Server) readSpend(w http.ResponseWriter, r *http.Request, who caller) (statedSpend, error) {
	id := who.identity
	switch {
	case id == nil:
		return statedSpend{}, attestationRefused("An attestation needs a CI token.")
	case s.GatewayEnvironment == "" || id.Environment != s.GatewayEnvironment:
		return statedSpend{}, attestationRefused("The token's job did not run in the gateway's environment.")
	case s.Spend == nil:
		return statedSpend{}, refuse(http.StatusNotImplemented, codeNoSpend, "This ledger does not keep the gateway's spend.")
	}
	keyID := r.PathValue("key")
	if !gatewayKeyID.MatchString(keyID) {
		return statedSpend{}, refuse(http.StatusBadRequest, codeBadQuery, "The key id is not 64 hexadecimal characters.")
	}
	body, err := readBody(w, r)
	if err != nil {
		return statedSpend{}, err
	}
	var stated spendAttestation
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&stated); err != nil || stated.Currency != "USD" {
		return statedSpend{}, refuse(http.StatusBadRequest, codeMalformed, "The body is not a spend attestation in USD.")
	}
	if len(stated.Spend) > maxSpendLength {
		return statedSpend{}, refuse(http.StatusBadRequest, codeMalformed, fmt.Sprintf("spend is longer than %d characters.", maxSpendLength))
	}
	usd, err := decimal.Parse(stated.Spend)
	if err != nil || usd.Sign() < 0 || usd.Scale() > focus.MaxScale {
		return statedSpend{}, refuse(http.StatusBadRequest, codeMalformed, "spend is not a decimal of at most 12 fractional digits, zero or more.")
	}
	for _, pair := range []struct{ key, stated, token string }{
		{"repository", strings.ToLower(stated.Repository), id.Repository},
		{"run", stated.RunID, id.RunID},
		{"attempt", stated.RunAttempt, id.RunAttempt},
	} {
		if pair.token == "" || pair.stated != pair.token {
			return statedSpend{}, refuse(http.StatusForbidden, codeJoinKey, "The attestation's "+pair.key+" disagrees with the CI token.")
		}
	}
	return statedSpend{
		credential: store.MintedCredential{KeySHA256: keyID, Repository: id.Repository, RunID: id.RunID, RunAttempt: id.RunAttempt},
		usd:        usd,
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
}

type verdictAnswer struct {
	Repository  string         `json:"repository"`
	PullRequest string         `json:"pull_request"`
	Budget      budgetView     `json:"budget"`
	Policy      policy.Verdict `json:"policy"`
}

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
	request := policy.LedgerRequest{
		Lines:                ledgerLines(lines),
		Evidence:             evidenceOf(budget),
		Config:               config,
		Start:                start,
		MeasuredPullRequests: stated.MeasuredPullRequests,
		At:                   s.Now(),
	}
	if budget.Basis == budgetBasisGateway {
		request.MeasuredUSD = measured.usd
	}
	verdict, err := policy.EvaluateLedger(r.Context(), request)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, verdictAnswer{Repository: repo, PullRequest: pr, Budget: budget, Policy: verdict})
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
