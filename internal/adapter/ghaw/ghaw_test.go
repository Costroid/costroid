package ghaw_test

import (
	"bufio"
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/Costroid/costroid/internal/adapter"
	"github.com/Costroid/costroid/internal/adapter/ghaw"
	"github.com/Costroid/costroid/internal/catalog"
	"github.com/Costroid/costroid/internal/decimal"
	"github.com/Costroid/costroid/internal/focus"
	"github.com/Costroid/costroid/internal/run"
)

// The usage artifacts the reader is tested on. Each is laid out as gh-aw's
// usage artifact, and testdata/not-recorded/README.md says how they were made.
var (
	claudeArtifact = filepath.Join("testdata", "not-recorded", "claude")
	codexArtifact  = filepath.Join("testdata", "not-recorded", "codex")
)

func readArtifact(t *testing.T, dir string) adapter.Report {
	t.Helper()
	report, err := ghaw.ReadUsage(os.DirFS(dir), nil)
	if err != nil {
		t.Fatal(err)
	}
	return report
}

// row is the part of a FOCUS row these tests read.
type row struct {
	SkuMeter         string            `json:"SkuMeter"`
	ConsumedQuantity string            `json:"ConsumedQuantity"`
	ListCost         string            `json:"ListCost"`
	Tags             map[string]string `json:"Tags"`
}

// rowsOf prices the report as ingest-run does and returns its rows.
func rowsOf(t *testing.T, report adapter.Report) []row {
	t.Helper()
	var rows []row
	lines := bufio.NewScanner(bytes.NewReader(writtenRows(t, report)))
	for lines.Scan() {
		var r row
		if err := json.Unmarshal(lines.Bytes(), &r); err != nil {
			t.Fatal(err)
		}
		rows = append(rows, r)
	}
	return rows
}

// writtenRows is the rows file ingest-run writes for the report.
func writtenRows(t *testing.T, report adapter.Report) []byte {
	t.Helper()
	pinned, err := catalog.Load()
	if err != nil {
		t.Fatal(err)
	}
	var written bytes.Buffer
	if _, err := run.Record(run.Options{Rows: &written, Catalog: pinned}, ghaw.Name, report); err != nil {
		t.Fatal(err)
	}
	return written.Bytes()
}

// fileSums adds up each token field of every record in the artifact's
// token-usage files, read with no help from the reader.
func fileSums(t *testing.T, dir string) map[string]int64 {
	t.Helper()
	sums := map[string]int64{}
	for _, name := range []string{"agent/token_usage.jsonl", "detection/token_usage.jsonl"} {
		data, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(name)))
		if err != nil {
			t.Fatal(err)
		}
		for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
			if line == "" {
				continue
			}
			var record map[string]any
			decoder := json.NewDecoder(strings.NewReader(line))
			decoder.UseNumber()
			if err := decoder.Decode(&record); err != nil {
				t.Fatal(err)
			}
			for _, field := range []string{"input_tokens", "output_tokens", "cache_read_tokens", "cache_write_tokens", "reasoning_tokens"} {
				if count, ok := record[field].(json.Number); ok {
					value, err := count.Int64()
					if err != nil {
						t.Fatal(err)
					}
					sums[field] += value
				}
			}
		}
	}
	return sums
}

// rowSums adds up the rows' tokens per class.
func rowSums(t *testing.T, rows []row) map[focus.TokenClass]int64 {
	t.Helper()
	sums := map[focus.TokenClass]int64{}
	for _, r := range rows {
		count, err := strconv.ParseInt(r.ConsumedQuantity, 10, 64)
		if err != nil {
			t.Fatal(err)
		}
		sums[focus.TokenClass(r.SkuMeter)] += count
	}
	return sums
}

// TestGhAwUsageRows prices a Claude run's usage artifact and finds the rows
// hold every token the records state, class by class: Anthropic's input is
// the uncached input, and its cache writes take the five-minute class.
func TestGhAwUsageRows(t *testing.T) {
	report := readArtifact(t, claudeArtifact)
	file, rows := fileSums(t, claudeArtifact), rowSums(t, rowsOf(t, report))
	for class, field := range map[focus.TokenClass]string{
		focus.InputUncached:   "input_tokens",
		focus.CacheRead:       "cache_read_tokens",
		focus.CacheWrite5m:    "cache_write_tokens",
		focus.Output:          "output_tokens",
		focus.ReasoningOutput: "reasoning_tokens",
	} {
		if rows[class] != file[field] {
			t.Errorf("the rows hold %d %s tokens, and the records' %s add up to %d", rows[class], class, field, file[field])
		}
	}
	if len(report.Requests) != 5 {
		t.Errorf("%d requests, want the router's, the agent's three that carried tokens and the detection job's", len(report.Requests))
	}
	if report.CostBasis != focus.Measured || report.CostSource == "" {
		t.Errorf("basis %s from %q, want measured from gh-aw's records", report.CostBasis, report.CostSource)
	}
	for _, request := range report.Requests {
		if request.Grain != adapter.PerRequest || request.Start.After(request.End) {
			t.Errorf("request %s: grain %s from %s to %s", request.ID, request.Grain, request.Start, request.End)
		}
		if request.Tokens.CacheWrite5m > 0 && request.AssumedWrite != focus.CacheWrite5m {
			t.Errorf("request %s wrote to the cache without the assumed lifetime", request.ID)
		}
	}
}

// TestGhAwRequestTimesAndNotes reads each request's span from its completion
// time less its duration, and names the requests the agent did not make.
func TestGhAwRequestTimesAndNotes(t *testing.T) {
	requests := map[string]adapter.Request{}
	for _, request := range readArtifact(t, claudeArtifact).Requests {
		requests[request.ID] = request
	}
	agent := requests["msg_01HandWrittenAgent00001"]
	if got := agent.End.Sub(agent.Start).String(); got != "4.2505s" || agent.End.Format("15:04:05.000") != "10:00:09.250" {
		t.Errorf("the agent's request ran %s, ending %s", got, agent.End)
	}
	if agent.Note != "" || agent.Model != "claude-sonnet-5" || agent.Provider != "anthropic" {
		t.Errorf("the agent's own request reads %q, %s, %s", agent.Note, agent.Provider, agent.Model)
	}
	if note := requests["msg_01HandWrittenRouter0001"].Note; !strings.Contains(note, "router") {
		t.Errorf("the router's request notes %q", note)
	}
	if note := requests["msg_01HandWrittenDetect0001"].Note; !strings.Contains(note, "threat detection") {
		t.Errorf("the detection job's request notes %q", note)
	}
	if _, ok := requests["msg_01HandWrittenOverload01"]; ok {
		t.Error("a 529 that carried no tokens became a request")
	}
	if _, ok := requests["msg_01HandWrittenCutShort01"]; !ok {
		t.Error("a 500 that carried tokens was left out")
	}
}

// TestGhAwCodexInputMapping reads OpenAI's input as holding the cached tokens
// and the cache writes, so the uncached input is what remains, as the Codex
// reader does. The two requests are those of the recorded Codex 0.167.0 run,
// and they price to the figure that recording's README states.
func TestGhAwCodexInputMapping(t *testing.T) {
	report := readArtifact(t, codexArtifact)
	want := []adapter.Tokens{
		{InputUncached: 3, CacheWrite30m: 13181, Output: 51},
		{InputUncached: 3, CacheRead: 13181, CacheWrite30m: 74, Output: 5, ReasoningOutput: 2, CachedInputReported: 13181},
	}
	if len(report.Requests) != len(want) {
		t.Fatalf("%d requests, want %d", len(report.Requests), len(want))
	}
	for i, request := range report.Requests {
		if request.Tokens != want[i] {
			t.Errorf("request %s: %+v, want %+v", request.ID, request.Tokens, want[i])
		}
		if request.AssumedWrite != focus.CacheWrite30m || request.Provider != "openai" {
			t.Errorf("request %s: writes assumed %q, provider %s", request.ID, request.AssumedWrite, request.Provider)
		}
	}
	total := decimal.Decimal{}
	for _, r := range rowsOf(t, report) {
		cost, err := decimal.Parse(r.ListCost)
		if err != nil {
			t.Fatal(err)
		}
		total = total.Add(cost)
	}
	if total.String() != "0.0350276" {
		t.Errorf("the two requests come to USD %s, and the recording's README states 0.0350276", total)
	}
}

// TestGhAwOpenAIInputBelowItsCacheIsRefused refuses a record whose input
// cannot hold the tokens it says were cached.
func TestGhAwOpenAIInputBelowItsCacheIsRefused(t *testing.T) {
	artifact := artifactWith(record("openai", "gpt-6.1-sol", `"input_tokens":10,"cache_read_tokens":11`))
	if _, err := ghaw.ReadUsage(artifact, nil); err == nil || !strings.Contains(err.Error(), "line 1") {
		t.Errorf("err = %v", err)
	}
}

// TestGhAwStatedCreditsStayApart puts the run's AI credits in AgentStated, as
// gh-aw states them: the last total of each job, added up in gh-aw's own unit.
func TestGhAwStatedCreditsStayApart(t *testing.T) {
	stated := readArtifact(t, claudeArtifact).AgentStated
	if stated == nil || *stated != (adapter.Stated{Unit: "AIC", Quantity: "7.47", Source: "gh-aw"}) {
		t.Errorf("stated %+v, want 6.52 AIC from the agent's job and 0.95 from detection", stated)
	}
	float, err := ghaw.ReadUsage(artifactWith(record("anthropic", "claude-sonnet-5", `"input_tokens":1,"ai_credits_total":4.830000000000001`)), nil)
	if err != nil {
		t.Fatal(err)
	}
	if float.AgentStated == nil || float.AgentStated.Quantity != "4.830000000000001" {
		t.Errorf("a figure gh-aw wrote with every digit of its float reads %+v", float.AgentStated)
	}
	artifact := artifactWith(record("anthropic", "claude-sonnet-5", `"input_tokens":1`))
	artifact["detection/token_usage.jsonl"] = &fstest.MapFile{Data: []byte(record("anthropic", "claude-sonnet-5", `"request_id":"msg_detect","input_tokens":1,"ai_credits_total":0.5`) + "\n")}
	report, err := ghaw.ReadUsage(artifact, nil)
	if err != nil {
		t.Fatal(err)
	}
	if report.AgentStated != nil {
		t.Errorf("an agent job that states no credits gave %+v", report.AgentStated)
	}
	artifact = artifactWith(record("anthropic", "claude-sonnet-5", `"input_tokens":1,"ai_credits_total":2`))
	artifact["detection/token_usage.jsonl"] = &fstest.MapFile{Data: []byte(record("anthropic", "claude-sonnet-5", `"request_id":"msg_detect","input_tokens":1`) + "\n")}
	report, err = ghaw.ReadUsage(artifact, nil)
	if err != nil {
		t.Fatal(err)
	}
	if report.AgentStated != nil || !hasWarning(report, "detection/token_usage.jsonl states no AI credits") {
		t.Errorf("a detection job with no credits gave %+v and %q", report.AgentStated, report.Warnings)
	}
}

// TestGhAwGradeIsNoneWithTheProxyNote grades a gh-aw run none, because
// Costroid minted no cap, and names the cap gh-aw checks in the note. The
// run's 7.47 AI credits are under it, so the note states nothing past it.
func TestGhAwGradeIsNoneWithTheProxyNote(t *testing.T) {
	report := readArtifact(t, claudeArtifact)
	want := adapter.Enforcement{Grade: adapter.GradeNone, Note: "none: a cap of 10 AI credits that gh-aw checks, priced by gh-aw, not by Costroid"}
	if report.Enforcement != want {
		t.Errorf("enforcement %+v, want %+v", report.Enforcement, want)
	}
	for _, r := range rowsOf(t, report) {
		if r.Tags["costroid/enforcement_grade"] != adapter.GradeNone {
			t.Errorf("a row reads grade %q", r.Tags["costroid/enforcement_grade"])
		}
	}
	artifact := artifactWith(record("anthropic", "claude-sonnet-5", `"input_tokens":1`))
	unstated, err := ghaw.ReadUsage(artifact, nil)
	if err != nil {
		t.Fatal(err)
	}
	if unstated.Enforcement.Grade != adapter.GradeNone || strings.Contains(unstated.Enforcement.Note, "AI credits") {
		t.Errorf("with no aw_info.json, enforcement %+v", unstated.Enforcement)
	}
	artifact["aw_info.json"] = &fstest.MapFile{Data: []byte(`{"model_config":{"max_ai_credits":"ten"}}`)}
	if _, err := ghaw.ReadUsage(artifact, nil); err == nil {
		t.Error("a cap that is not a count was read")
	}
}

// TestGhAwFollowUpRefusesAnExtraArtifactEntry reads only what gh-aw puts in
// the usage artifact: any other file or directory, or a link, refuses it.
func TestGhAwFollowUpRefusesAnExtraArtifactEntry(t *testing.T) {
	for name, extra := range map[string]fstest.MapFS{
		"a file beside the records":   {"costroid": &fstest.MapFile{Data: []byte("#!/bin/sh\n"), Mode: 0o755}},
		"a file in a known directory": {"agent/run.sh": &fstest.MapFile{Data: []byte("#!/bin/sh\n")}},
		"an unknown directory":        {"bin/costroid": &fstest.MapFile{Data: []byte("#!/bin/sh\n")}},
		"a link in a record's place":  {"detection/token_usage.jsonl": &fstest.MapFile{Data: []byte("/etc/passwd"), Mode: 0o777 | os.ModeSymlink}},
	} {
		t.Run(name, func(t *testing.T) {
			artifact := artifactWith(record("anthropic", "claude-sonnet-5", `"input_tokens":1`))
			for path, file := range extra {
				artifact[path] = file
			}
			if _, err := ghaw.ReadUsage(artifact, nil); err == nil {
				t.Error("the artifact was read")
			}
		})
	}
	if _, err := ghaw.ReadUsage(os.DirFS(claudeArtifact), nil); err != nil {
		t.Errorf("the artifact as gh-aw writes it was refused: %v", err)
	}
	if _, err := ghaw.ReadUsage(fstest.MapFS{"aw_info.json": &fstest.MapFile{Data: []byte("{}")}}, nil); err == nil || !strings.Contains(err.Error(), "agent/token_usage.jsonl") {
		t.Errorf("an artifact with no agent records: %v", err)
	}
}

// TestGhAwCopilotPricesAsListEquivalent prices a request Copilot served at the
// list rate of the model's own provider, as a list-equivalent figure, and
// leaves out a request to a provider the catalog does not price.
func TestGhAwCopilotPricesAsListEquivalent(t *testing.T) {
	artifact := artifactWith(
		record("copilot", "claude-sonnet-5", `"request_id":"copilot-1","input_tokens":120,"cache_read_tokens":100`),
		record("copilot", "gpt-6.1-sol", `"request_id":"copilot-2","input_tokens":5,"cache_read_tokens":100`),
		record("gemini", "gemini-3-pro", `"request_id":"gemini-1","input_tokens":40`),
	)
	report, err := ghaw.ReadUsage(artifact, nil)
	if err != nil {
		t.Fatal(err)
	}
	if report.CostBasis != focus.ListEquivalent || len(report.Requests) != 2 {
		t.Fatalf("basis %s over %d requests", report.CostBasis, len(report.Requests))
	}
	lumped, split := report.Requests[0], report.Requests[1]
	if lumped.Provider != "anthropic" || lumped.Tokens.InputUncached != 20 || lumped.Tokens.CacheRead != 100 {
		t.Errorf("input that holds its cache reads %s %+v", lumped.Provider, lumped.Tokens)
	}
	if split.Provider != "openai" || split.Tokens.InputUncached != 5 || split.Tokens.CacheRead != 100 {
		t.Errorf("input below its cache reads %s %+v", split.Provider, split.Tokens)
	}
	if !strings.Contains(lumped.Note, "Copilot") {
		t.Errorf("note %q", lumped.Note)
	}
	if !hasWarning(report, "1 request in agent/token_usage.jsonl is left out: the catalog holds no price for provider gemini") {
		t.Errorf("warnings %q", report.Warnings)
	}
	for _, r := range rowsOf(t, report) {
		if r.Tags["costroid/cost_basis"] != string(focus.ListEquivalent) {
			t.Errorf("a row reads %q", r.Tags["costroid/cost_basis"])
		}
	}
	artifact = artifactWith(
		record("copilot", "claude-sonnet-5", `"request_id":"copilot-1","input_tokens":1`),
		record("anthropic", "claude-sonnet-5", `"request_id":"key-1","input_tokens":1`),
	)
	if report, err = ghaw.ReadUsage(artifact, nil); err != nil {
		t.Fatal(err)
	}
	if report.CostBasis != focus.ListEquivalent || !hasWarning(report, "the weaker claim") {
		t.Errorf("a run with both reads %s, %q", report.CostBasis, report.Warnings)
	}
}

// TestGhAwRefusesARecordItCannotRead refuses the whole artifact on a record
// that breaks the schema in a field the reader uses, and on two records with
// one request id, which would share a row id.
func TestGhAwRefusesARecordItCannotRead(t *testing.T) {
	for name, line := range map[string]string{
		"not JSON":               `{"_schema":`,
		"another schema":         record("anthropic", "claude-sonnet-5", `"_schema":"token-diag/v0.29.8"`),
		"no request id":          record("anthropic", "claude-sonnet-5", `"request_id":""`),
		"a count that is no int": record("anthropic", "claude-sonnet-5", `"output_tokens":1.5`),
		"a negative count":       record("anthropic", "claude-sonnet-5", `"output_tokens":-1`),
		"no duration":            record("anthropic", "claude-sonnet-5", `"duration_ms":null`),
		"no time":                record("anthropic", "claude-sonnet-5", `"timestamp":"yesterday"`),
		"credits past any count": record("anthropic", "claude-sonnet-5", `"ai_credits_total":1e300`),
		"credits below zero":     record("anthropic", "claude-sonnet-5", `"ai_credits_total":-0.5`),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := ghaw.ReadUsage(artifactWith(line), nil); err == nil {
				t.Error("the record was read")
			}
		})
	}
	twice := record("anthropic", "claude-sonnet-5", `"request_id":"msg_twice","input_tokens":1`)
	if _, err := ghaw.ReadUsage(artifactWith(twice, twice), nil); err == nil || !strings.Contains(err.Error(), "msg_twice") {
		t.Errorf("one request id twice: %v", err)
	}
}

// record is one token-usage record with every required field, the fields in
// override replacing or joining the defaults.
func record(provider, model, override string) string {
	fields := map[string]string{
		"_schema": `"token-usage/v0.29.8"`, "timestamp": `"2026-11-07T10:00:00.000Z"`, "event": `"token_usage"`,
		"request_id": `"req-` + provider + `"`, "provider": `"` + provider + `"`, "model": `"` + model + `"`,
		"path": `"/v1/messages"`, "status": "200", "streaming": "false", "input_tokens": "0", "output_tokens": "0",
		"cache_read_tokens": "0", "cache_write_tokens": "0", "duration_ms": "100",
	}
	if override != "" {
		var extra map[string]json.RawMessage
		if err := json.Unmarshal([]byte("{"+override+"}"), &extra); err != nil {
			panic(err)
		}
		for key, value := range extra {
			fields[key] = string(value)
		}
	}
	order := []string{"_schema", "timestamp", "event", "request_id", "provider", "model", "path", "status", "streaming",
		"input_tokens", "output_tokens", "cache_read_tokens", "cache_write_tokens", "duration_ms"}
	var parts []string
	for _, key := range order {
		parts = append(parts, `"`+key+`":`+fields[key])
		delete(fields, key)
	}
	for key, value := range fields {
		parts = append(parts, `"`+key+`":`+value)
	}
	return "{" + strings.Join(parts, ",") + "}"
}

// artifactWith is a usage artifact whose agent job wrote the given records.
func artifactWith(records ...string) fstest.MapFS {
	return fstest.MapFS{
		"agent/token_usage.jsonl":     &fstest.MapFile{Data: []byte(strings.Join(records, "\n") + "\n")},
		"detection/token_usage.jsonl": &fstest.MapFile{},
	}
}

func hasWarning(report adapter.Report, part string) bool {
	for _, warning := range report.Warnings {
		if strings.Contains(warning, part) {
			return true
		}
	}
	return false
}

// TestGhAwCopilotModelsPriceAtTheirPublisher prices each model Copilot served
// at the sheet of the provider that publishes it: o3 and codex-mini-latest at
// OpenAI's, gemini-3.8-flash at Google's. Google prices no cache write, so a
// write on gemini-3.8-flash counts as uncached input, and the request says
// so. A model whose publisher has no sheet is left out, and the warning
// names the model.
func TestGhAwCopilotModelsPriceAtTheirPublisher(t *testing.T) {
	artifact := artifactWith(
		record("copilot", "o3", `"request_id":"copilot-1","input_tokens":10`),
		record("copilot", "codex-mini-latest", `"request_id":"copilot-2","input_tokens":10`),
		record("copilot", "gemini-3.8-flash", `"request_id":"copilot-3","input_tokens":50,"cache_read_tokens":20,"cache_write_tokens":10`),
		record("copilot", "mai-code-1.1-flash", `"request_id":"copilot-4","input_tokens":10`),
	)
	report, err := ghaw.ReadUsage(artifact, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Requests) != 3 {
		t.Fatalf("%d requests", len(report.Requests))
	}
	for i, want := range []string{"openai", "openai", "google"} {
		if got := report.Requests[i].Provider; got != want {
			t.Errorf("%s priced at %s, want %s", report.Requests[i].Model, got, want)
		}
	}
	gemini := report.Requests[2]
	if gemini.Tokens != (adapter.Tokens{InputUncached: 30, CacheRead: 20}) || gemini.AssumedWrite != "" || !strings.Contains(gemini.Note, "cache writes are counted as uncached input") {
		t.Errorf("gemini-3.8-flash reads %+v at %q, noted %q", gemini.Tokens, gemini.AssumedWrite, gemini.Note)
	}
	if !hasWarning(report, `1 request in agent/token_usage.jsonl is left out: the catalog holds no sheet of the provider that publishes the model "mai-code-1.1-flash", which Copilot served`) {
		t.Errorf("warnings %q", report.Warnings)
	}
	if len(rowsOf(t, report)) == 0 {
		t.Error("the priced requests wrote no row")
	}
}
