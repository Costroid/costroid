// Package ghaw reads what a GitHub Agentic Workflows (gh-aw) run spent, from
// the usage artifact its conclusion job uploads. The artifact holds the
// records gh-aw's firewall proxy writes, one per model request, in the
// token-usage schema the firewall publishes with each release.
package ghaw

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/Costroid/costroid/internal/adapter"
	"github.com/Costroid/costroid/internal/decimal"
	"github.com/Costroid/costroid/internal/focus"
)

// Name is the costroid/agent tag of a gh-aw run's rows.
const Name = "gh-aw"

const (
	// agentUsage holds the agent job's records. The conclusion job copies the
	// proxy's token-usage.jsonl here byte for byte.
	agentUsage = "agent/token_usage.jsonl"
	// detectionUsage holds the threat detection job's records, empty when
	// that job made no request.
	detectionUsage = "detection/token_usage.jsonl"
	// runInfo states the cap the workflow was compiled with.
	runInfo = "aw_info.json"

	// costSource names where the rows' figures came from.
	costSource = "gh-aw token usage"
	// schemaPrefix starts the _schema of every record.
	schemaPrefix = "token-usage/v"
	// usageEvent is the event of every record.
	usageEvent = "token_usage"
	// routingPurpose marks a request the firewall's router made itself.
	routingPurpose = "routing_classification"

	// maxFile bounds each file the reader reads. The artifact is the run's
	// own upload, so its size is the run's choice.
	maxFile = 32 << 20
	// maxLine bounds one record.
	maxLine = 1 << 20
	// maxDuration bounds one request's duration_ms, past any job's own limit.
	maxDuration = 7 * 24 * time.Hour
)

// The notes a request's rows carry when the agent did not make it.
const (
	routingNote   = "gh-aw's router made this request to choose a model, not the agent"
	detectionNote = "gh-aw's threat detection job made this request, not the agent"
)

// creditsPattern bounds the AI credits a record states, written out with
// every digit gh-aw wrote, as the result object holds them.
var creditsPattern = regexp.MustCompile(`^[0-9]{1,26}(\.[0-9]{1,32})?$`)

// noneNote is the grade's note when the artifact does not state the cap.
const noneNote = "none: Costroid did not start this run, so no budget held it"

// knownFiles is every file gh-aw v1.2.1's conclusion job puts in the usage
// artifact, by its path inside the artifact. A downloaded artifact that holds
// any other entry is refused, so a follow-up job reads exactly what gh-aw
// wrote there and nothing a run added.
var knownFiles = []string{
	"aw_session.jsonl",
	runInfo,
	"aw-info.jsonl",
	"agent_usage.json",
	"agent_usage.jsonl",
	"detection_usage.jsonl",
	"evals.jsonl",
	"graders/grader_manifest.json",
	"graders/grader_results.json",
	"github_rate_limits.jsonl",
	agentUsage,
	"agent/model-routing.jsonl",
	"agent/execution.json",
	"agent/harness_retry.json",
	detectionUsage,
	"detection/execution.json",
	"detection/harness_retry.json",
	"detection/detection_result.json",
	"evals/token_usage.jsonl",
	"evals/execution.json",
	"evals/evals_status.json",
	"experiment/state.jsonl",
	"experiment/state.json",
	"experiment/assignments.json",
	"activity/summary.json",
}

// record is one line of token_usage.jsonl. Token counts are integers in the
// schema, so a count that is not one refuses the file. The other fields the
// schema names are not read.
type record struct {
	Schema           string      `json:"_schema"`
	Timestamp        string      `json:"timestamp"`
	Event            string      `json:"event"`
	RequestID        string      `json:"request_id"`
	Provider         string      `json:"provider"`
	Model            string      `json:"model"`
	Status           int         `json:"status"`
	InputTokens      *int64      `json:"input_tokens"`
	OutputTokens     *int64      `json:"output_tokens"`
	CacheReadTokens  *int64      `json:"cache_read_tokens"`
	CacheWriteTokens *int64      `json:"cache_write_tokens"`
	ReasoningTokens  int64       `json:"reasoning_tokens"`
	DurationMS       json.Number `json:"duration_ms"`
	Purpose          string      `json:"purpose"`
	AICreditsTotal   json.Number `json:"ai_credits_total"`
}

// usageFile is one job's records as the reader took them.
type usageFile struct {
	name     string
	note     string
	requests []adapter.Request
	// records counts every record in the file, kept or not.
	records int
	// credits is the last ai_credits_total the file states, nil for none.
	credits *decimal.Decimal
	// copilot counts the kept requests Copilot served.
	copilot int
	// unpriced counts the requests to each provider the catalog holds no
	// sheet for.
	unpriced map[string]int
}

// ReadUsage reads a gh-aw run from the usage artifact gh run download wrote to
// dir. Each record of the agent job and of the threat detection job is one
// request. The run's AI credits, gh-aw's own figure, go to AgentStated and
// never into a request.
//
// Costroid did not start the run, so the grade is none. gh-aw runs Claude and
// Codex on an API key alone, so their rows read measured. Copilot bills by
// subscription, so its rows read list_equivalent, and a run with both reads
// list_equivalent, the weaker claim.
func ReadUsage(dir fs.FS) (adapter.Report, error) {
	if err := checkEntries(dir); err != nil {
		return adapter.Report{}, err
	}
	agent, err := readFile(dir, agentUsage, "")
	if err != nil {
		return adapter.Report{}, err
	}
	detection, err := readFile(dir, detectionUsage, detectionNote)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return adapter.Report{}, err
	}
	files := []usageFile{agent}
	if err == nil {
		files = append(files, detection)
	}
	if err := uniqueIDs(files); err != nil {
		return adapter.Report{}, err
	}
	limit, err := statedLimit(dir)
	if err != nil {
		return adapter.Report{}, err
	}
	report := adapter.Report{
		CostBasis:   focus.Measured,
		CostSource:  costSource,
		Enforcement: enforcement(limit),
	}
	copilot := 0
	for _, file := range files {
		report.Requests = append(report.Requests, file.requests...)
		copilot += file.copilot
		report.Warnings = append(report.Warnings, file.unpricedWarnings()...)
	}
	if copilot > 0 {
		report.CostBasis = focus.ListEquivalent
	}
	if copilot > 0 && copilot < len(report.Requests) {
		report.Warnings = append(report.Warnings, fmt.Sprintf("%s Copilot served bill by subscription and the rest bill a key, so every row reads list_equivalent, the weaker claim", counted(copilot, "request")))
	}
	stated, warning := statedCredits(files)
	report.AgentStated = stated
	if warning != "" {
		report.Warnings = append(report.Warnings, warning)
	}
	return report, nil
}

// checkEntries refuses an artifact that holds an entry gh-aw does not put
// there, or one that is not a plain file or directory.
func checkEntries(dir fs.FS) error {
	return fs.WalkDir(dir, ".", func(name string, entry fs.DirEntry, err error) error {
		switch {
		case err != nil:
			return fmt.Errorf("gh-aw: read the usage artifact: %w", err)
		case entry.IsDir():
			if name != "." && !slices.ContainsFunc(knownFiles, func(known string) bool { return strings.HasPrefix(known, name+"/") }) {
				return fmt.Errorf("gh-aw: the usage artifact holds the directory %s, which gh-aw does not write there", name)
			}
			return nil
		case !entry.Type().IsRegular():
			return fmt.Errorf("gh-aw: the usage artifact's %s is not a plain file", name)
		case !slices.Contains(knownFiles, name):
			return fmt.Errorf("gh-aw: the usage artifact holds %s, which gh-aw does not write there", name)
		}
		return nil
	})
}

// readAll reads one file of the artifact, refusing one past maxFile.
func readAll(dir fs.FS, name string) ([]byte, error) {
	file, err := dir.Open(name)
	if err != nil {
		return nil, err
	}
	// A file opened for reading has nothing left to flush.
	defer func() { _ = file.Close() }()
	info, err := file.Stat()
	if err != nil {
		return nil, fmt.Errorf("gh-aw: read %s: %w", name, err)
	}
	if info.Size() > maxFile {
		return nil, fmt.Errorf("gh-aw: %s is larger than %d bytes", name, maxFile)
	}
	var data bytes.Buffer
	if _, err := data.ReadFrom(file); err != nil {
		return nil, fmt.Errorf("gh-aw: read %s: %w", name, err)
	}
	if data.Len() > maxFile {
		return nil, fmt.Errorf("gh-aw: %s is larger than %d bytes", name, maxFile)
	}
	return data.Bytes(), nil
}

// readFile reads one job's records. A line that is not a record the schema
// allows refuses the file, because it holds part of the run's spend.
func readFile(dir fs.FS, name, note string) (usageFile, error) {
	data, err := readAll(dir, name)
	if errors.Is(err, fs.ErrNotExist) {
		if name == agentUsage {
			return usageFile{}, fmt.Errorf("gh-aw: the usage artifact holds no %s: download the usage artifact of a gh-aw run whose firewall was on", agentUsage)
		}
		return usageFile{}, err
	}
	if err != nil {
		return usageFile{}, err
	}
	file := usageFile{name: name, note: note, unpriced: map[string]int{}}
	lines := bufio.NewScanner(bytes.NewReader(data))
	lines.Buffer(make([]byte, 0, 64<<10), maxLine)
	for number := 1; lines.Scan(); number++ {
		if len(bytes.TrimSpace(lines.Bytes())) == 0 {
			continue
		}
		if err := file.add(lines.Bytes()); err != nil {
			return usageFile{}, fmt.Errorf("gh-aw: %s line %d: %w", name, number, err)
		}
	}
	if err := lines.Err(); err != nil {
		return usageFile{}, fmt.Errorf("gh-aw: read %s: %w", name, err)
	}
	return file, nil
}

// add reads one record into the file.
func (f *usageFile) add(line []byte) error {
	var r record
	decoder := json.NewDecoder(bytes.NewReader(line))
	decoder.UseNumber()
	if err := decoder.Decode(&r); err != nil {
		return fmt.Errorf("not a token-usage record: %w", err)
	}
	if err := r.check(); err != nil {
		return err
	}
	f.records++
	if r.AICreditsTotal != "" {
		total, err := decimal.ParseNumber(r.AICreditsTotal.String())
		if err != nil || total.Sign() < 0 || !creditsPattern.MatchString(total.String()) {
			return fmt.Errorf("ai_credits_total %s is not a count of credits", r.AICreditsTotal)
		}
		f.credits = &total
	}
	request, priced, err := r.request()
	if err != nil {
		return err
	}
	if r.Status >= 400 && len(request.Tokens.Counts()) == 0 {
		return nil
	}
	if !priced {
		f.unpriced[r.Provider]++
		return nil
	}
	if r.Provider == providerCopilot {
		f.copilot++
	}
	request.Note = joinNotes(f.note, request.Note)
	f.requests = append(f.requests, request)
	return nil
}

// check refuses a record the schema does not allow, in the fields the reader
// uses.
func (r record) check() error {
	switch {
	case !strings.HasPrefix(r.Schema, schemaPrefix):
		return fmt.Errorf("_schema %q is not a token-usage schema", r.Schema)
	case r.Event != usageEvent:
		return fmt.Errorf("event %q is not %s", r.Event, usageEvent)
	case r.RequestID == "":
		return errors.New("the record names no request_id")
	case r.Provider == "" || r.Model == "":
		return errors.New("the record names no provider or model")
	case r.InputTokens == nil || r.OutputTokens == nil || r.CacheReadTokens == nil || r.CacheWriteTokens == nil:
		return errors.New("the record lacks a token count the schema requires")
	case r.Status < 100 || r.Status > 599:
		return fmt.Errorf("status %d is not an HTTP status", r.Status)
	case r.DurationMS == "":
		return errors.New("the record states no duration_ms")
	}
	return nil
}

// uniqueIDs refuses a request id two records share. A row's id is built from
// its request's, so two would share a row id.
func uniqueIDs(files []usageFile) error {
	seen := map[string]string{}
	for _, file := range files {
		for _, request := range file.requests {
			if other, ok := seen[request.ID]; ok {
				return fmt.Errorf("gh-aw: request_id %s appears in %s and %s", request.ID, other, file.name)
			}
			seen[request.ID] = file.name
		}
	}
	return nil
}

// unpricedWarnings names the requests left out because the catalog prices no
// model of their provider.
func (f usageFile) unpricedWarnings() []string {
	var warnings []string
	for _, provider := range slices.Sorted(maps.Keys(f.unpriced)) {
		warnings = append(warnings, fmt.Sprintf("%s in %s %s left out: the catalog holds no price for provider %s",
			counted(f.unpriced[provider], "request"), f.name, plural(f.unpriced[provider], "is", "are"), provider))
	}
	return warnings
}

// statedCredits is the run's AI credits as gh-aw stated them: the last
// ai_credits_total of each job's file, added up. Credits are gh-aw's unit, so
// adding them converts nothing. An agent job that states none leaves the run
// with no figure. A detection job that made requests and states none makes
// the run's total unknown, so no figure is stated and a warning says why.
func statedCredits(files []usageFile) (*adapter.Stated, string) {
	var total decimal.Decimal
	stated := false
	for _, file := range files {
		switch {
		case file.records == 0:
			continue
		case file.credits != nil:
			total, stated = total.Add(*file.credits), true
		case file.name == agentUsage:
			return nil, ""
		default:
			return nil, fmt.Sprintf("%s states no AI credits, so the run's credits are not stated", file.name)
		}
	}
	if !stated {
		return nil, ""
	}
	return &adapter.Stated{Unit: adapter.UnitAICredits, Quantity: total.String(), Source: Name}, ""
}

func joinNotes(notes ...string) string {
	return strings.Join(slices.DeleteFunc(notes, func(note string) bool { return note == "" }), "; ")
}

// counted is a count and its noun, plural past one.
func counted(count int, noun string) string {
	return fmt.Sprintf("%d %s", count, plural(count, noun, noun+"s"))
}

func plural(count int, one, many string) string {
	if count == 1 {
		return one
	}
	return many
}

// times reads when the request started and ended: the record's timestamp is
// when the call completed, and duration_ms how long it took.
func (r record) times() (start, end time.Time, err error) {
	end, err = time.Parse(time.RFC3339Nano, r.Timestamp)
	if err != nil {
		return time.Time{}, time.Time{}, fmt.Errorf("timestamp %q is not an RFC 3339 time", r.Timestamp)
	}
	took, err := duration(r.DurationMS)
	if err != nil {
		return time.Time{}, time.Time{}, err
	}
	return end.Add(-took), end, nil
}

// duration reads duration_ms, a JSON number of milliseconds, to the
// microsecond, without a float.
func duration(milliseconds json.Number) (time.Duration, error) {
	refused := fmt.Errorf("duration_ms %s is not a duration", milliseconds)
	value, err := decimal.ParseNumber(milliseconds.String())
	if err != nil || value.Sign() < 0 || value.Cmp(decimal.FromInt(maxDuration.Milliseconds())) > 0 {
		return 0, refused
	}
	whole, _, _ := strings.Cut(value.Mul(decimal.FromInt(1000)).String(), ".")
	micros, err := strconv.ParseInt(whole, 10, 64)
	if err != nil {
		return 0, refused
	}
	return time.Duration(micros) * time.Microsecond, nil
}
