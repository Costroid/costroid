package copilot

import (
	"bufio"
	"bytes"
	"cmp"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/Costroid/costroid/internal/adapter"
	"github.com/Costroid/costroid/internal/catalog"
	"github.com/Costroid/costroid/internal/decimal"
	"github.com/Costroid/costroid/internal/focus"
)

// UnitNanoAIU is Copilot's own unit for its AI credits: nano-AI units, as
// totalNanoAiu states them.
const UnitNanoAIU = "nano-AIU"

// costSource names the file the rows come from.
const costSource = "agent_session_file"

// recordedVersion is the Copilot CLI release a fixture records.
const recordedVersion = "1.0.103"

// The providers a request can be priced at.
const (
	providerAnthropic = catalog.ProviderAnthropic
	providerOpenAI    = catalog.ProviderOpenAI
	providerGoogle    = catalog.ProviderGoogle
)

// copilotServedNote says how a request Copilot served, not the user's own
// provider key, was priced.
const copilotServedNote = "Copilot served this request; priced at the list rate of the model's own provider"

// googleWriteNote says how a request's cache writes on a Google model were
// counted.
const googleWriteNote = "Google's sheet prices no cache write, so this request's cache writes are counted as uncached input"

// event is one line of events.jsonl.
type event struct {
	Type      string          `json:"type"`
	Timestamp string          `json:"timestamp"`
	Data      json.RawMessage `json:"data"`
}

// counts are the token fields a usage record and a shutdown's model metrics
// share.
type counts struct {
	InputTokens      int64 `json:"inputTokens"`
	OutputTokens     int64 `json:"outputTokens"`
	CacheReadTokens  int64 `json:"cacheReadTokens"`
	CacheWriteTokens int64 `json:"cacheWriteTokens"`
	ReasoningTokens  int64 `json:"reasoningTokens"`
}

// field is one count and its name in the session file.
type field struct {
	name  string
	value int64
}

func (c counts) fields() []field {
	return []field{
		{"inputTokens", c.InputTokens},
		{"outputTokens", c.OutputTokens},
		{"cacheReadTokens", c.CacheReadTokens},
		{"cacheWriteTokens", c.CacheWriteTokens},
		{"reasoningTokens", c.ReasoningTokens},
	}
}

func (c counts) plus(other counts) counts {
	return counts{
		InputTokens:      c.InputTokens + other.InputTokens,
		OutputTokens:     c.OutputTokens + other.OutputTokens,
		CacheReadTokens:  c.CacheReadTokens + other.CacheReadTokens,
		CacheWriteTokens: c.CacheWriteTokens + other.CacheWriteTokens,
		ReasoningTokens:  c.ReasoningTokens + other.ReasoningTokens,
	}
}

// usageRecord is a session.usage_record's usage: one model request.
type usageRecord struct {
	counts
	Model           string      `json:"model"`
	Duration        json.Number `json:"duration"`
	IsByok          *bool       `json:"isByok"`
	ByokKind        string      `json:"byokKind"`
	ModelProvider   string      `json:"modelProvider"`
	APICallID       string      `json:"apiCallId"`
	AICreditsStatus string      `json:"aiCreditsStatus"`
	CacheTTLSeconds int64       `json:"cachePolicyTtlSeconds"`
	Accounting      struct {
		UsageID string `json:"usageId"`
	} `json:"accounting"`
	// end is when Copilot wrote the record, the event's timestamp.
	end time.Time
}

// modelMetric is one model's totals in session.shutdown.
type modelMetric struct {
	Requests struct {
		Count *int64       `json:"count"`
		Cost  *json.Number `json:"cost"`
	} `json:"requests"`
	Usage counts `json:"usage"`
}

// shutdown is session.shutdown: the session's totals and Copilot's credits.
// Its codeChanges list file paths, and are never read.
type shutdown struct {
	TotalNanoAiu *json.Number           `json:"totalNanoAiu"`
	ModelMetrics map[string]modelMetric `json:"modelMetrics"`
}

// sessionFile is what one events.jsonl holds that the rows need.
type sessionFile struct {
	session   string
	version   string
	records   []usageRecord
	shutdowns []shutdown
	// cut says the file ends inside a line, as a stopped run can leave it.
	cut bool
}

// readSessionFile reads every line of events.jsonl. A line that is not JSON
// refuses the file, since a usage record could be the line that was lost. A
// last line with no line feed that is not JSON was cut short when Copilot
// CLI stopped, and is left unread.
func readSessionFile(events io.Reader) (sessionFile, error) {
	var file sessionFile
	reader := bufio.NewReaderSize(events, 1<<20)
	for number := 1; ; number++ {
		line, err := reader.ReadBytes('\n')
		if trimmed := bytes.TrimSpace(line); len(trimmed) > 0 {
			if errors.Is(err, io.EOF) && !json.Valid(trimmed) {
				file.cut = true
				return file, nil
			}
			if readErr := file.read(trimmed); readErr != nil {
				return file, fmt.Errorf("line %d: %w", number, readErr)
			}
		}
		if errors.Is(err, io.EOF) {
			return file, nil
		}
		if err != nil {
			return file, err
		}
	}
}

// read takes one event by its type. An event this reader does not use is
// left unread.
func (f *sessionFile) read(line []byte) error {
	var e event
	if err := json.Unmarshal(line, &e); err != nil {
		return errors.New("not a JSON event")
	}
	switch e.Type {
	case "session.start":
		var start struct {
			SessionID      string `json:"sessionId"`
			CopilotVersion string `json:"copilotVersion"`
		}
		if err := json.Unmarshal(e.Data, &start); err != nil {
			return fmt.Errorf("session.start: %w", err)
		}
		if f.session != "" && f.session != start.SessionID {
			return errors.New("a second session.start names another session")
		}
		f.session, f.version = start.SessionID, start.CopilotVersion
	case "session.usage_record":
		var record struct {
			Usage usageRecord `json:"usage"`
		}
		if err := json.Unmarshal(e.Data, &record); err != nil {
			return fmt.Errorf("session.usage_record: %w", err)
		}
		end, err := time.Parse(time.RFC3339Nano, e.Timestamp)
		if err != nil {
			return fmt.Errorf("session.usage_record: the timestamp %q is not an RFC 3339 time", e.Timestamp)
		}
		record.Usage.end = end
		f.records = append(f.records, record.Usage)
	case "session.shutdown":
		var totals shutdown
		if err := json.Unmarshal(e.Data, &totals); err != nil {
			return fmt.Errorf("session.shutdown: %w", err)
		}
		f.shutdowns = append(f.shutdowns, totals)
	}
	return nil
}

// ReadEvents reads one session's events.jsonl. Each session.usage_record is
// one request. The records' sums for each model must equal the model's
// totals in session.shutdown, or the file is refused. keyed says the
// provider variables named the user's own key, which decides the basis of a
// record that does not state isByok.
//
// Copilot's own credits, totalNanoAiu and each model's requests.cost, are
// its figures in its own units. They reach AgentStated and a warning, never a
// row, and never become dollars.
func ReadEvents(events io.Reader, keyed bool) (adapter.Report, error) {
	report, _, err := readEvents(events, keyed)
	return report, err
}

// readEvents is ReadEvents, which also lists the requests that wrote no row.
func readEvents(events io.Reader, keyed bool) (adapter.Report, []unpriced, error) {
	file, err := readSessionFile(events)
	if err != nil {
		return adapter.Report{}, nil, fmt.Errorf("copilot: read the session file: %w", err)
	}
	if file.session == "" {
		return adapter.Report{}, nil, errors.New("copilot: the session file holds no session.start naming the session")
	}
	if len(file.shutdowns) > 1 {
		return adapter.Report{}, nil, fmt.Errorf("copilot: the session file holds %d session.shutdown events: the session was resumed, and this run's spend cannot be told from the earlier runs'", len(file.shutdowns))
	}
	report := adapter.Report{
		SessionID:    file.session,
		AgentVersion: file.version,
		CostSource:   costSource,
	}
	if len(file.shutdowns) == 1 {
		if err := file.check(file.shutdowns[0]); err != nil {
			return adapter.Report{}, nil, err
		}
		report.AgentStated, report.Warnings = stated(file.shutdowns[0], file.records)
	} else {
		report.Warnings = append(report.Warnings, "the session file holds no session.shutdown: the run ended before Copilot CLI closed the session, so its usage records were not checked against Copilot's own totals, a request in flight at the end reached no row, and Copilot stated no credits for the run")
	}
	if file.cut {
		report.Warnings = append(report.Warnings, "the session file ends inside a line, as a stopped run leaves it; that line is not read, and a usage record in it reached no row")
	}
	requests, missed, err := file.requests(keyed)
	if err != nil {
		return adapter.Report{}, nil, err
	}
	report.Requests = requests
	report.Warnings = append(report.Warnings, unpricedWarnings(missed)...)
	var warnings []string
	report.CostBasis, warnings = basis(file.records, keyed)
	report.Warnings = append(report.Warnings, warnings...)
	if file.version != "" && file.version != recordedVersion {
		report.Warnings = append(report.Warnings, fmt.Sprintf("Copilot CLI %s is not a recorded version; its session file passed every check", file.version))
	}
	return report, missed, nil
}

// check holds the usage records to the shutdown's totals: per model, the
// records' sum of each count equals the model's usage, and their number
// equals its requests.count when it states one. Any difference refuses the
// file, by model and count.
func (f sessionFile) check(totals shutdown) error {
	sums := map[string]counts{}
	requests := map[string]int64{}
	for _, record := range f.records {
		sums[record.Model] = sums[record.Model].plus(record.counts)
		requests[record.Model]++
	}
	var differences []string
	for _, model := range slices.Sorted(maps.Keys(sums)) {
		if _, ok := totals.ModelMetrics[model]; !ok {
			differences = append(differences, fmt.Sprintf("%s has %s and no modelMetrics entry", model, plural(requests[model], "usage record")))
		}
	}
	for _, model := range slices.Sorted(maps.Keys(totals.ModelMetrics)) {
		metric := totals.ModelMetrics[model]
		recorded := sums[model]
		for i, total := range metric.Usage.fields() {
			if sum := recorded.fields()[i].value; sum != total.value {
				differences = append(differences, fmt.Sprintf("%s %s: %d in the usage records, %d in session.shutdown", model, total.name, sum, total.value))
			}
		}
		if count := metric.Requests.Count; count != nil && *count != requests[model] {
			differences = append(differences, fmt.Sprintf("%s requests: %d usage records, a requests.count of %d in session.shutdown", model, requests[model], *count))
		}
	}
	if len(differences) > 0 {
		return fmt.Errorf("copilot: the session's usage records disagree with its session.shutdown, so no row is written: %s", strings.Join(differences, "; "))
	}
	return nil
}

func plural(count int64, noun string) string {
	if count == 1 {
		return "1 " + noun
	}
	return fmt.Sprintf("%d %ss", count, noun)
}

// unpriced is a request whose spend no sheet states: its model, and the
// provider it ran on when that was the user's own key. Copilot's own routing
// leaves provider empty.
type unpriced struct {
	model, provider string
}

// requests maps each usage record to one request. A record that no sheet of
// the catalog prices writes no row, and is listed apart.
func (f sessionFile) requests(keyed bool) ([]adapter.Request, []unpriced, error) {
	var requests []adapter.Request
	var missed []unpriced
	seen := map[string]bool{}
	for i, record := range f.records {
		request, err := record.request(keyed)
		if err != nil {
			return nil, nil, fmt.Errorf("copilot: usage record %d: %w", i+1, err)
		}
		if request.ID == "" {
			request.ID = fmt.Sprintf("%s:request:%d", f.session, i+1)
		}
		if seen[request.ID] {
			return nil, nil, fmt.Errorf("copilot: usage record %d repeats the API call id of an earlier one, so one request would count twice", i+1)
		}
		seen[request.ID] = true
		if request.Provider == "" {
			missed = append(missed, unpriced{model: record.Model, provider: record.keyProvider(keyed)})
			continue
		}
		requests = append(requests, request)
	}
	return requests, missed, nil
}

// unpricedWarnings counts the requests that wrote no row, by model and by the
// provider of the user's own key.
func unpricedWarnings(missed []unpriced) []string {
	counts := map[unpriced]int64{}
	for _, request := range missed {
		counts[request]++
	}
	var warnings []string
	for _, request := range slices.SortedFunc(maps.Keys(counts), compareUnpriced) {
		count := plural(counts[request], "request")
		if request.provider == "" {
			warnings = append(warnings, fmt.Sprintf("%s on the model %q wrote no row: the catalog holds no sheet of the provider that publishes the model, so their spend is not stated", count, request.model))
			continue
		}
		warnings = append(warnings, fmt.Sprintf("%s on the model %q through the provider %q wrote no row: the catalog holds no sheet for that provider, so their spend is not stated", count, request.model, request.provider))
	}
	return warnings
}

func compareUnpriced(a, b unpriced) int {
	return cmp.Or(cmp.Compare(a.model, b.model), cmp.Compare(a.provider, b.provider))
}

// unpricedModels names the models of the requests that wrote no row, in a
// sentence.
func unpricedModels(missed []unpriced) string {
	var models []string
	for _, request := range missed {
		models = append(models, strconv.Quote(request.model))
	}
	slices.Sort(models)
	models = slices.Compact(models)
	if len(models) == 1 {
		return "the model " + models[0]
	}
	return "the models " + strings.Join(models[:len(models)-1], ", ") + " and " + models[len(models)-1]
}

// byok says whether the record ran on the user's own provider key. A record
// that does not say is read from the provider variables.
func (r usageRecord) byok(keyed bool) bool {
	if r.IsByok == nil {
		return keyed
	}
	return *r.IsByok
}

// billsKey says whether a provider bills the record to the user's key: one
// of their own, off the device.
func (r usageRecord) billsKey(keyed bool) bool {
	return r.byok(keyed) && !strings.HasPrefix(r.ByokKind, "local_")
}

// keyProvider is the provider of the user's own key that served the record,
// as modelProvider names it, and empty when Copilot's own routing served it
// or the record names none.
func (r usageRecord) keyProvider(keyed bool) string {
	if !r.byok(keyed) {
		return ""
	}
	return r.ModelProvider
}

// pricedAt is the catalog provider that prices the record, empty when no
// sheet does. A request on the user's key is priced at its provider's sheet;
// one Copilot served, at the sheet of the model's own provider.
func (r usageRecord) pricedAt(keyed bool) string {
	if !r.byok(keyed) {
		return catalog.OwnerOf(r.Model)
	}
	switch r.ModelProvider {
	case providerAnthropic, providerOpenAI:
		return r.ModelProvider
	case "":
		return catalog.OwnerOf(r.Model)
	}
	return ""
}

// request maps one usage record. Copilot wrote it when the call ended, and
// its duration says when the call began.
func (r usageRecord) request(keyed bool) (adapter.Request, error) {
	took, err := duration(r.Duration)
	if err != nil {
		return adapter.Request{}, err
	}
	provider := r.pricedAt(keyed)
	tokens, assumed, err := r.tokens(provider)
	if err != nil {
		return adapter.Request{}, err
	}
	request := adapter.Request{
		ID:                 r.APICallID,
		Provider:           provider,
		Model:              r.Model,
		Start:              r.end.Add(-took),
		End:                r.end,
		ServiceTier:        catalog.TierStandard,
		Region:             catalog.RegionGlobal,
		ContextInputTokens: r.InputTokens,
		Grain:              adapter.PerRequest,
		Tokens:             tokens,
		AssumedWrite:       assumed,
	}
	if request.ID == "" {
		request.ID = r.Accounting.UsageID
	}
	var notes []string
	if !r.byok(keyed) {
		notes = append(notes, copilotServedNote)
	}
	if provider == providerGoogle && r.CacheWriteTokens > 0 {
		notes = append(notes, googleWriteNote)
	}
	request.Note = strings.Join(notes, "; ")
	return request, nil
}

// tokens maps a record's counts. Copilot CLI 1.0.103 counts cache reads and
// cache writes inside inputTokens, as its recorded run shows on every
// request, so the uncached input is what remains. No field Copilot declares
// states how long a write lives: an Anthropic write takes the lifetime
// cachePolicyTtlSeconds names, five minutes unless it names an hour, and an
// OpenAI write takes OpenAI's default of 30 minutes. Either way the row says
// the lifetime was assumed. Google's sheet prices no cache write, so a write
// on a Google model counts as uncached input, and the row says so.
func (r usageRecord) tokens(provider string) (adapter.Tokens, focus.TokenClass, error) {
	for _, count := range r.fields() {
		if count.value < 0 {
			return adapter.Tokens{}, "", fmt.Errorf("%s is %d, below zero", count.name, count.value)
		}
	}
	if r.CacheReadTokens > r.InputTokens || r.CacheWriteTokens > r.InputTokens-r.CacheReadTokens {
		return adapter.Tokens{}, "", fmt.Errorf("inputTokens %d is below its %d cache reads and %d cache writes, which Copilot CLI counts inside it", r.InputTokens, r.CacheReadTokens, r.CacheWriteTokens)
	}
	tokens := adapter.Tokens{
		InputUncached:       r.InputTokens - r.CacheReadTokens - r.CacheWriteTokens,
		CacheRead:           r.CacheReadTokens,
		Output:              r.OutputTokens,
		ReasoningOutput:     r.ReasoningTokens,
		CachedInputReported: r.CacheReadTokens,
	}
	class := focus.CacheWrite30m
	switch {
	case provider == providerGoogle:
		class = ""
		tokens.InputUncached += r.CacheWriteTokens
	case provider == providerAnthropic && r.CacheTTLSeconds == int64(time.Hour/time.Second):
		class = focus.CacheWrite1h
		tokens.CacheWrite1h = r.CacheWriteTokens
	case provider == providerAnthropic:
		class = focus.CacheWrite5m
		tokens.CacheWrite5m = r.CacheWriteTokens
	default:
		tokens.CacheWrite30m = r.CacheWriteTokens
	}
	if r.CacheWriteTokens == 0 {
		class = ""
	}
	return tokens, class, nil
}

// maxDuration bounds a request's duration, past any call Copilot waits on.
const maxDuration = 24 * time.Hour

// duration reads a duration in milliseconds, to the microsecond, without a
// float. An absent duration reads as zero.
func duration(milliseconds json.Number) (time.Duration, error) {
	if milliseconds == "" {
		return 0, nil
	}
	refused := fmt.Errorf("duration %s is not a duration in milliseconds", milliseconds)
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

// basis is measured when every request billed the user's own provider key,
// and list_equivalent otherwise: Copilot's own routing bills a subscription
// or its AI credits, and a model on the device bills nobody. A run with both
// reads list_equivalent, the weaker claim, and says so.
func basis(records []usageRecord, keyed bool) (focus.CostBasis, []string) {
	if len(records) == 0 {
		if keyed {
			return focus.Measured, nil
		}
		return focus.ListEquivalent, nil
	}
	billed := 0
	for _, record := range records {
		if record.billsKey(keyed) {
			billed++
		}
	}
	switch billed {
	case len(records):
		return focus.Measured, nil
	case 0:
		return focus.ListEquivalent, nil
	}
	return focus.ListEquivalent, []string{fmt.Sprintf("%s billed the user's own provider key and %s did not, so every row reads list_equivalent, the weaker claim",
		plural(int64(billed), "request"), plural(int64(len(records)-billed), "request"))}
}

// stated is Copilot's own figure for the run and the sentence that states
// it. totalNanoAiu is AgentStated as Copilot wrote it, every digit kept; each
// model's requests.cost is named beside it. A figure Copilot wrote as 0 is
// stated as 0, and one it left out is said to be absent.
func stated(totals shutdown, records []usageRecord) (*adapter.Stated, []string) {
	var figure *adapter.Stated
	nano := "no totalNanoAiu"
	if totals.TotalNanoAiu != nil {
		written := totals.TotalNanoAiu.String()
		if quantity, err := decimal.ParseNumber(written); err == nil && quantity.Sign() >= 0 {
			figure = &adapter.Stated{Unit: UnitNanoAIU, Quantity: asWritten(written, quantity), Source: Name}
		}
		nano = "a totalNanoAiu of " + written + " nano-AI units"
	}
	var costs []string
	for _, model := range slices.Sorted(maps.Keys(totals.ModelMetrics)) {
		cost := totals.ModelMetrics[model].Requests.Cost
		if cost == nil {
			costs = append(costs, "no requests.cost for "+model)
			continue
		}
		costs = append(costs, "a requests.cost of "+cost.String()+" for "+model)
	}
	sentence := "Copilot CLI stated " + nano
	if len(costs) > 0 {
		sentence += ", and " + strings.Join(costs, ", ")
	}
	sentence += ". They are Copilot's own units: Costroid never converts them to dollars, and no row holds them"
	if allUnavailable(records) {
		sentence += ". Every request's aiCreditsStatus reads unavailable, which Copilot documents as no amount reported, not a zero cost"
	}
	return figure, []string{sentence}
}

// asWritten is a figure as Copilot wrote it when it wrote a plain decimal,
// trailing zeros and all, and otherwise, as with an exponent, the same value
// written out in full.
func asWritten(written string, quantity decimal.Decimal) string {
	if _, err := decimal.Parse(written); err == nil {
		return written
	}
	return quantity.String()
}

func allUnavailable(records []usageRecord) bool {
	if len(records) == 0 {
		return false
	}
	for _, record := range records {
		if record.AICreditsStatus != "unavailable" {
			return false
		}
	}
	return true
}
