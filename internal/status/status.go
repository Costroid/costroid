// Package status writes a run's status file: what the run has spent so far,
// its cap, what is left and what holds it, for the agent and the job to read
// while the agent works. The file holds amounts, counts, times and words from
// closed lists, never a prompt, a path or a model's answer. Its contract is
// status/v0.1 of Costroid/costroid-schemas.
package status

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"time"

	"github.com/Costroid/costroid/internal/decimal"
	"github.com/Costroid/costroid/internal/focus"
)

// Schema names the contract's version in every file.
const Schema = "costroid.status/0.1"

// FileVariable names the status file's path.
const FileVariable = "COSTROID_STATUS_FILE"

// fileName is the file's name in RUNNER_TEMP when FileVariable is unset.
const fileName = "costroid-status.json"

// capCurrency is the currency every cap is stated in.
const capCurrency = "USD"

// Path is where a run writes its status file: FileVariable, else
// costroid-status.json in GitHub Actions' RUNNER_TEMP. Empty means neither is
// set, and no file is written.
func Path(getenv func(string) string) string {
	if path := getenv(FileVariable); path != "" {
		return path
	}
	if temp := getenv("RUNNER_TEMP"); temp != "" {
		return filepath.Join(temp, fileName)
	}
	return ""
}

// State says whether the agent still works.
type State string

// The states a run's status file can be in.
const (
	Running State = "running"
	Ended   State = "ended"
)

// Updates says when the file's spend changes.
type Updates string

// The two ways a run's spend is read.
const (
	// EachRequest is a run whose spend is priced after each request it sees
	// settle while the agent works.
	EachRequest Updates = "each_request"
	// AgentEnd is a run whose agent states its usage only when it ends.
	AgentEnd Updates = "agent_end"
)

// Spend is what the run's priced usage comes to in one currency and basis, at
// the catalog's list prices. A nil CostBasis is a basis the agent's reader
// states only when the agent ends.
type Spend struct {
	Currency  string           `json:"currency"`
	CostBasis *focus.CostBasis `json:"cost_basis"`
	ListCost  decimal.Decimal  `json:"list_cost"`
}

// Cap is the run's cap beside its spend in USD. Remainder and Overshoot are
// nil together, when no one figure compares with the cap.
type Cap struct {
	Currency  string           `json:"currency"`
	Amount    decimal.Decimal  `json:"amount"`
	Remainder *decimal.Decimal `json:"remainder"`
	Overshoot *decimal.Decimal `json:"overshoot"`
}

// NewCap sets the cap beside the spend. Spend in no USD entry is zero; spend
// in two USD entries, or spend not known, compares with nothing.
func NewCap(amount decimal.Decimal, spend []Spend) Cap {
	held := Cap{Currency: capCurrency, Amount: amount}
	if spend == nil {
		return held
	}
	var cost decimal.Decimal
	entries := 0
	for _, entry := range spend {
		if entry.Currency == capCurrency {
			cost = entry.ListCost
			entries++
		}
	}
	if entries > 1 {
		return held
	}
	var remainder, overshoot decimal.Decimal
	if left := amount.Sub(cost); left.Sign() > 0 {
		remainder = left
	} else {
		overshoot = cost.Sub(amount)
	}
	held.Remainder, held.Overshoot = &remainder, &overshoot
	return held
}

// Status is one write of the file. A nil Spend is spend not known, and an
// empty one is a run that has spent nothing yet.
type Status struct {
	Schema           string    `json:"schema"`
	State            State     `json:"state"`
	Agent            string    `json:"agent"`
	Updates          Updates   `json:"updates"`
	StartedAt        time.Time `json:"started_at"`
	UpdatedAt        time.Time `json:"updated_at"`
	Spend            []Spend   `json:"spend"`
	Requests         *int      `json:"requests"`
	Cap              Cap       `json:"cap"`
	EnforcementGrade string    `json:"enforcement_grade"`
}

// Write replaces the file at path with the status. It writes a new file beside
// the old one and renames it over it, so a reader finds the last whole status
// or none, never part of one. A failed write leaves the old file as it was.
func Write(path string, s Status) error {
	s.Schema = Schema
	s.StartedAt, s.UpdatedAt = s.StartedAt.UTC(), s.UpdatedAt.UTC()
	data, err := json.Marshal(s)
	if err != nil {
		return fmt.Errorf("status: encode: %w", err)
	}
	temp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".*")
	if err != nil {
		return fmt.Errorf("status: %w", err)
	}
	if err := writeAndClose(temp, append(data, '\n')); err != nil {
		os.Remove(temp.Name())
		return err
	}
	if err := os.Rename(temp.Name(), path); err != nil {
		os.Remove(temp.Name())
		return fmt.Errorf("status: %w", err)
	}
	return nil
}

func writeAndClose(file *os.File, data []byte) error {
	if _, err := file.Write(data); err != nil {
		file.Close()
		return fmt.Errorf("status: write %s: %w", file.Name(), err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("status: close %s: %w", file.Name(), err)
	}
	return nil
}

// NoFileError is a status file that does not exist: no costroid run has
// started an agent that writes it in this job. An empty Path is an environment
// that names no status file.
type NoFileError struct {
	Path string
}

func (e *NoFileError) Error() string {
	if e.Path == "" {
		return fmt.Sprintf("no status file: %s and RUNNER_TEMP are unset, so no costroid run writes one", FileVariable)
	}
	return fmt.Sprintf("no status file at %s: no costroid run has started an agent in this job", e.Path)
}

// Read reads the status file where Path says it is. A file that does not
// exist is a *NoFileError; any other failure, a file that cannot be read or
// is not a status of this version, is an error of its own.
func Read(getenv func(string) string) (Status, error) {
	path := Path(getenv)
	if path == "" {
		return Status{}, &NoFileError{}
	}
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return Status{}, &NoFileError{Path: path}
	}
	if err != nil {
		return Status{}, fmt.Errorf("status: %w", err)
	}
	var read Status
	if err := json.Unmarshal(data, &read); err != nil {
		return Status{}, fmt.Errorf("status: read %s: %w", path, err)
	}
	if read.Schema != Schema {
		return Status{}, fmt.Errorf("status: %s is %q, and this release reads %s", path, read.Schema, Schema)
	}
	return read, nil
}

// UnmarshalJSON reads an entry whose amount is a decimal string.
func (s *Spend) UnmarshalJSON(data []byte) error {
	var wire struct {
		Currency  string           `json:"currency"`
		CostBasis *focus.CostBasis `json:"cost_basis"`
		ListCost  string           `json:"list_cost"`
	}
	if err := json.Unmarshal(data, &wire); err != nil {
		return err
	}
	cost, err := decimal.Parse(wire.ListCost)
	if err != nil {
		return fmt.Errorf("list_cost: %w", err)
	}
	*s = Spend{Currency: wire.Currency, CostBasis: wire.CostBasis, ListCost: cost}
	return nil
}

// UnmarshalJSON reads a cap whose amounts are decimal strings or null.
func (c *Cap) UnmarshalJSON(data []byte) error {
	var wire struct {
		Currency  string  `json:"currency"`
		Amount    string  `json:"amount"`
		Remainder *string `json:"remainder"`
		Overshoot *string `json:"overshoot"`
	}
	if err := json.Unmarshal(data, &wire); err != nil {
		return err
	}
	amount, err := decimal.Parse(wire.Amount)
	if err != nil {
		return fmt.Errorf("amount: %w", err)
	}
	remainder, err := optionalAmount(wire.Remainder)
	if err != nil {
		return fmt.Errorf("remainder: %w", err)
	}
	overshoot, err := optionalAmount(wire.Overshoot)
	if err != nil {
		return fmt.Errorf("overshoot: %w", err)
	}
	*c = Cap{Currency: wire.Currency, Amount: amount, Remainder: remainder, Overshoot: overshoot}
	return nil
}

func optionalAmount(text *string) (*decimal.Decimal, error) {
	if text == nil {
		return nil, nil
	}
	amount, err := decimal.Parse(*text)
	if err != nil {
		return nil, err
	}
	return &amount, nil
}
