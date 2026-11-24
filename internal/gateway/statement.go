package gateway

import (
	"errors"
	"regexp"

	"github.com/Costroid/costroid/internal/vcs"
)

// The two writes integration/v0.7 of Costroid Platform's contract takes about
// a key: PUT /credentials/{key_id} names the run the key is for, and
// PUT /credentials/{key_id}/spend states what the gateway counted on it. Each
// goes under a run token whose audience names the tenant, the key, the run
// the key is for and its attempt, and the write.

// tenantsAudience opens every audience of Costroid Platform's run tokens.
const tenantsAudience = "https://app.costroid.com/tenants/"

// spendFigure is a spend as the contract writes it: zero or more, with no
// leading zero, no sign, no exponent and at most 12 fractional digits.
var spendFigure = regexp.MustCompile(`^(0|[1-9][0-9]*)(\.[0-9]{1,12})?$`)

// maxSpendCharacters bounds a spend's decimal string.
const maxSpendCharacters = 64

// KeyStatement is the body of PUT /credentials/{key_id}: the run the key is
// for, its attempt and its head commit, as the workflow_run event names them.
// The key's id is in the path, and the repository is the attesting run's.
type KeyStatement struct {
	RunID      string `json:"run_id"`
	RunAttempt string `json:"run_attempt"`
	HeadSHA    string `json:"head_sha"`
}

// SpendStatement is the body of PUT /credentials/{key_id}/spend: the run the
// key is for, its attempt, and the gateway's settled figure in USD, as a
// decimal string.
type SpendStatement struct {
	RunID      string `json:"run_id"`
	RunAttempt string `json:"run_attempt"`
	Currency   string `json:"currency"`
	Spend      string `json:"spend"`
}

// errStatementForm is a statement no write of the contract takes, which is
// never sent.
var errStatementForm = errors.New("gateway: the key's id, the run, its attempt or the head commit is not of the form Costroid Platform takes, so nothing was stated")

// NewKeyStatement states that a key is for one run at one head. A key id, a
// run, an attempt or a head of another form is refused before anything is
// sent.
func NewKeyStatement(keyID string, run Binding, head string) (KeyStatement, error) {
	if !statable(keyID, run) || !vcs.IsCommit(head) {
		return KeyStatement{}, errStatementForm
	}
	return KeyStatement{RunID: run.Run, RunAttempt: run.Attempt, HeadSHA: head}, nil
}

// NewSpendStatement states a count for the run, rounded up to 12 fractional
// digits as SpendFor rounds it.
func NewSpendStatement(count Count, run Binding) (SpendStatement, error) {
	spend := SpendFor(count, run).Spend
	if !statable(count.KeyID, run) || !spendFigure.MatchString(spend) || len(spend) > maxSpendCharacters {
		return SpendStatement{}, errStatementForm
	}
	return SpendStatement{RunID: run.Run, RunAttempt: run.Attempt, Currency: "USD", Spend: spend}, nil
}

func statable(keyID string, run Binding) bool {
	return keyIDPattern.MatchString(keyID) && digits.MatchString(run.Run) && digits.MatchString(run.Attempt)
}

// KeyAudience is the audience of the run token that attests a key: the
// tenant, the key, the run the key is for and its attempt, compared whole.
func KeyAudience(tenant, keyID string, run Binding) string {
	return tenantsAudience + tenant + "/credentials/" + keyID + "/" + run.Run + "/" + run.Attempt
}

// SpendAudience is the audience of the run token that states a key's spend:
// the attestation's, and /spend.
func SpendAudience(tenant, keyID string, run Binding) string {
	return KeyAudience(tenant, keyID, run) + "/spend"
}
