package run

import (
	"cmp"
	"context"
	"fmt"
	"os"
	"slices"
	"sync"
	"time"

	"github.com/Costroid/costroid/internal/adapter"
	"github.com/Costroid/costroid/internal/decimal"
	"github.com/Costroid/costroid/internal/focus"
	"github.com/Costroid/costroid/internal/status"
)

// sessionWithStatus is the agent's session, which also writes the run's status
// file when the environment names one: when the agent's first line is read,
// after each request the watch sees settle, and when the agent ends. It hides
// every method of the session but Feed, Finish, Tail and RunWindow, so a run
// sets the session up before it calls this.
func (o Options) sessionWithStatus(agent string, plan adapter.Plan, session adapter.Session) adapter.Session {
	path := status.Path(os.Getenv)
	if path == "" {
		return session
	}
	tailer, tails := session.(adapter.Tailer)
	updates := status.AgentEnd
	if tails && plan.Enforcement.Watchdog {
		updates = status.EachRequest
	}
	file := &statusFile{
		path:    path,
		builder: o.builder(agent, plan.Enforcement),
		now:     o.Now,
		budget:  o.Budget,
		grade:   plan.Enforcement.Grade,
		status: status.Status{
			Agent:     agent,
			Updates:   updates,
			StartedAt: o.Now(),
		},
	}
	reporting := &statusSession{inner: session, file: file}
	if tails {
		return &tailingStatusSession{statusSession: reporting, tailer: tailer}
	}
	return reporting
}

// endStatus writes the grade the run earned once its rows were checked into the
// run's last status, so the file never states a grade above the result
// object's. A session that writes no status file is left as it is.
func endStatus(session adapter.Session, grade string) {
	if reporting, ok := session.(interface{ earned(string) }); ok {
		reporting.earned(grade)
	}
}

// statusFile is a run's status file and what it last said. Writes come from
// the agent's stdout and from the watch, so one is made at a time.
type statusFile struct {
	path    string
	builder rowBuilder
	now     func() time.Time
	budget  decimal.Decimal
	grade   string

	mu      sync.Mutex
	status  status.Status
	begun   bool
	ended   bool
	problem error
}

// begin writes the first status, once: the agent is running and nothing it
// spent has been priced yet. A run whose spend is read only at its end does
// not know its spend until then.
func (f *statusFile) begin() {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.begun {
		return
	}
	f.begun = true
	f.status.State = status.Running
	if f.status.Updates == status.EachRequest {
		none := 0
		f.status.Spend, f.status.Requests = []status.Spend{}, &none
	}
	f.write()
}

// settled writes the spend of the requests the watch has seen settle. A
// request the catalog cannot price leaves the last status as it was.
func (f *statusFile) settled(requests []adapter.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.ended {
		return
	}
	spend, err := f.price(adapter.Report{Requests: requests})
	if err != nil {
		return
	}
	f.begun = true
	f.status.State = status.Running
	f.status.Spend, f.status.Requests = spend, requestCount(adapter.Report{Requests: requests})
	f.write()
}

// end writes the run's spend once the agent has ended. A report the reader
// refused, or the catalog could not price, leaves the spend not known. It
// returns the first write that failed, if any.
func (f *statusFile) end(report adapter.Report, read bool) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.begun, f.ended = true, true
	f.status.State = status.Ended
	f.status.Spend, f.status.Requests = nil, nil
	if read {
		if report.Enforcement.Grade != "" {
			f.grade = report.Enforcement.Grade
		}
		if spend, err := f.price(report); err == nil {
			f.status.Spend, f.status.Requests = spend, requestCount(report)
		}
	}
	f.write()
	return f.problem
}

// earned rewrites the last status with the grade the run earned, when it
// differs from the one the file states.
func (f *statusFile) earned(grade string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if grade == "" || grade == f.grade {
		return
	}
	f.grade = grade
	f.write()
}

// write sets the cap and the time, and replaces the file. The first failure is
// kept for the run's warnings; the run goes on without its status.
func (f *statusFile) write() {
	f.status.UpdatedAt = f.now()
	f.status.Cap = status.NewCap(f.budget, f.status.Spend)
	f.status.EnforcementGrade = f.grade
	if err := status.Write(f.path, f.status); err != nil && f.problem == nil {
		f.problem = err
	}
}

// price is the report's rows, priced at the catalog, summed per currency and
// basis. Rows with no basis yet are summed apart from every basis.
func (f *statusFile) price(report adapter.Report) ([]status.Spend, error) {
	report, _ = containModels(report)
	rows, err := f.builder.rows(report)
	if err != nil {
		return nil, err
	}
	spend := []status.Spend{}
	for _, row := range rows {
		basis := focus.CostBasis(row.Tags[focus.TagCostBasis])
		at := slices.IndexFunc(spend, func(entry status.Spend) bool {
			return entry.Currency == row.BillingCurrency && sameBasis(entry.CostBasis, basis)
		})
		if at < 0 {
			entry := status.Spend{Currency: row.BillingCurrency}
			if basis != "" {
				entry.CostBasis = &basis
			}
			spend = append(spend, entry)
			at = len(spend) - 1
		}
		spend[at].ListCost = spend[at].ListCost.Add(row.ListCost)
	}
	slices.SortFunc(spend, func(a, b status.Spend) int {
		return cmp.Or(cmp.Compare(a.Currency, b.Currency), cmp.Compare(basisName(a.CostBasis), basisName(b.CostBasis)))
	})
	return spend, nil
}

func sameBasis(stated *focus.CostBasis, basis focus.CostBasis) bool {
	return basisName(stated) == basis
}

func basisName(basis *focus.CostBasis) focus.CostBasis {
	if basis == nil {
		return ""
	}
	return *basis
}

// requestCount counts the report's model requests, nested sessions included.
// It is nil when an entry is a turn's or a run's total, because the number of
// requests inside it is not known.
func requestCount(report adapter.Report) *int {
	count := 0
	for _, request := range report.Requests {
		if request.Grain != adapter.PerRequest {
			return nil
		}
		count++
	}
	for _, nested := range report.Nested {
		more := requestCount(nested)
		if more == nil {
			return nil
		}
		count += *more
	}
	return &count
}

// statusSession is the agent's session, writing the status file as the run
// reads it.
type statusSession struct {
	inner adapter.Session
	file  *statusFile
}

// Feed writes the first status when the agent's first line arrives, then
// reads the line.
func (s *statusSession) Feed(line []byte, at time.Time) error {
	s.file.begin()
	return s.inner.Feed(line, at)
}

// Finish reads the run's usage and writes the run's last status. A status file
// that could not be written is named in the report's warnings.
func (s *statusSession) Finish() (adapter.Report, error) {
	report, err := s.inner.Finish()
	problem := s.file.end(report, err == nil)
	if err == nil && problem != nil {
		report.Warnings = append(report.Warnings, fmt.Sprintf("the status file could not be written: %v", problem))
	}
	return report, err
}

func (s *statusSession) earned(grade string) {
	s.file.earned(grade)
}

// RunWindow passes the agent's process window to a session that counts other
// sessions inside it.
func (s *statusSession) RunWindow(start, end time.Time) {
	if windowed, ok := s.inner.(windowedSession); ok {
		windowed.RunWindow(start, end)
	}
}

// tailingStatusSession is a statusSession over a session that watches the
// agent's own file, and writes the status after each request it sees settle.
type tailingStatusSession struct {
	*statusSession
	tailer adapter.Tailer
}

// Tail follows the agent's file as the session does, and writes the status
// after the run has checked each settled request against the cap.
func (s *tailingStatusSession) Tail(ctx context.Context, settled func([]adapter.Request)) {
	s.tailer.Tail(ctx, func(requests []adapter.Request) {
		settled(requests)
		s.file.settled(requests)
	})
}
