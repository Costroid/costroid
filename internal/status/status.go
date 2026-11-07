// Package status writes a run's status file: what the run has spent so far,
// its cap, what is left and what holds it, for the agent and the job to read
// while the agent works. The file holds amounts, counts, times and words from
// closed lists, never a prompt, a path or a model's answer. Its contract is
// status/v0.1 of Costroid/costroid-schemas.
package status

import (
	"encoding/json"
	"fmt"
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
