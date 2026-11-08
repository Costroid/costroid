package main

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// ghawRecord holds every property of gh-aw's token-usage schema, v0.29.8,
// with values written by hand in the schema's types. None came from a run.
const ghawRecord = `{"_schema":"token-usage/v0.29.8","timestamp":"2026-11-07T10:00:01.000Z","event":"token_usage",` +
	`"request_id":"msg_01PlantedRequest","provider":"anthropic","model":"claude-haiku-5-5","path":"/v1/messages",` +
	`"requested_endpoint":"/responses","upstream_endpoint":"/chat/completions","status":200,"streaming":true,` +
	`"input_tokens":12,"output_tokens":5,"cache_read_tokens":100,"cache_write_tokens":40,"reasoning_tokens":2,` +
	`"duration_ms":812.5,"response_bytes":2048,"purpose":"subagent","agent_name":"reviewer","caller_source":"caller_header",` +
	`"steering":{"type":"ai_credit","threshold":80},` +
	`"model_fallback":{"requested_model":"claude-sonnet-5","requested_provider":"anthropic","model":"claude-haiku-5-5",` +
	`"provider":"anthropic","from_model":"claude-sonnet-5","from_provider":"anthropic",` +
	`"failures":[{"provider":"anthropic","model":"claude-sonnet-5","reason":"upstream_5xx","status":529}],` +
	`"attempt":2,"reason":"upstream_5xx","status":529},"x_initiator":"agent",` +
	`"billing":{"quota_chat":{"ent":"-1","ov":"0","ovPerm":"false","rem":"100","rst":"2026-12-01T00:00:00Z"},` +
	`"quota_premium-chat":{"ent":"300","ov":"0","ovPerm":"true","rem":"95","rst":"2026-12-01T00:00:00Z"},` +
	`"rate_limit":"5000","rate_remaining":"4999","rate_reset":"1796083200"},` +
	`"ai_credits_accounting_policy":"concrete_model","ai_credits_dynamic_selector":"auto",` +
	`"ai_credits_fallback_pricing_used":false,"ai_credits_pricing_api_version":"2026-11-01",` +
	`"ai_credits_pricing_catalog_id":"shared","ai_credits_pricing_catalog_version":"7",` +
	`"ai_credits_pricing_discount_percent":0,"ai_credits_pricing_observed_at":"2026-11-07T09:00:00Z",` +
	`"ai_credits_pricing_source":"curated","ai_credits_pricing_tier":"default",` +
	`"ai_credits_this_response":0.42,"ai_credits_total":1.37}`

// ghawPlanted are keys outside the schema, each as a raw run could carry it,
// by the path the scrub names when it drops one.
var ghawPlanted = []struct{ path, json string }{
	{"prompt", `"` + plantedPrompt + `"`},
	{"messages", `[{"role":"user","content":"` + plantedPrompt + `"}]`},
	{"headers", `{"authorization":"Bearer ` + plantedKey + `"}`},
	{"cwd", `"` + plantedPath + `"`},
	{"token_usage_detail", `{"new_tokens":9}`},
}

// ghawUnknownInSchemaObjects are keys the schema's open objects admit and
// its properties do not name.
var ghawUnknownInSchemaObjects = []string{
	"model_fallback.note",
	"model_fallback.failures[].message",
	"billing.quota_chat.login",
}

// withPlanted is ghawRecord with every planted key at the top and one
// unnamed key inside each open object the schema allows to hold one.
func withPlanted(t *testing.T) string {
	t.Helper()
	line, err := parseValue([]byte(ghawRecord))
	if err != nil {
		t.Fatal(err)
	}
	for _, planted := range ghawPlanted {
		item, err := parseValue([]byte(planted.json))
		if err != nil {
			t.Fatal(err)
		}
		setField(line, planted.path, item)
	}
	fallback := line.field("model_fallback")
	setField(fallback, "note", stringValue(plantedPrompt))
	setField(fallback.field("failures").items[0], "message", stringValue("upstream said: "+plantedPrompt))
	setField(line.field("billing").field("quota_chat"), "login", stringValue("someone"))
	var out bytes.Buffer
	line.compact(&out)
	return out.String()
}

// pathsOf lists every key path a line holds, as the allowlist names them.
func pathsOf(v *value, path string, into map[string]bool) {
	switch v.kind {
	case kindObject:
		for i, key := range v.keys {
			p := child(path, key)
			into[p] = true
			pathsOf(v.items[i], p, into)
		}
	case kindArray:
		into[path+"[]"] = true
		for _, item := range v.items {
			pathsOf(item, path+"[]", into)
		}
	}
}

func TestGhAwAllowlistDropsUnknownFields(t *testing.T) {
	// The list is the schema less its free text: ghawRecord holds every
	// property, and the list keeps every key ghawRecord holds but those in
	// ghawFreeText, and no other.
	line, err := parseValue([]byte(ghawRecord))
	if err != nil {
		t.Fatal(err)
	}
	recordPaths := map[string]bool{}
	pathsOf(line, "", recordPaths)
	for _, path := range ghawFreeText {
		if !recordPaths[path] {
			t.Errorf("ghawRecord holds no %s", path)
		}
		delete(recordPaths, path)
	}
	if !sameSet(recordPaths, ghawAllowlist.kept) {
		t.Errorf("the gh-aw allowlist is not the schema's properties:\n list %v\nrecord %v", sortedSet(ghawAllowlist.kept), sortedSet(recordPaths))
	}

	raw := t.TempDir()
	artifact := filepath.Join(raw, "agent", "sandbox", "firewall", "logs", "api-proxy-logs")
	write(t, filepath.Join(artifact, ghawUsageFile), withPlanted(t), ghawRecord)
	write(t, filepath.Join(artifact, "model-routing.jsonl"), `{"prompt":"`+plantedPrompt+`"}`)
	collected := filepath.Join(t.TempDir(), "collected")
	var stderr bytes.Buffer
	if code := run(context.Background(), []string{"collect", "--agent", agentGhAw, "--home", raw, "--out", collected}, os.Getenv, nil, io.Discard, &stderr); code != 0 {
		t.Fatalf("collect exited %d: %s", code, stderr.String())
	}
	files, err := readRecording(collected)
	if err != nil || len(files) != 1 || files[0].name != ghawUsageFile {
		t.Fatalf("collect kept %v, want token-usage.jsonl alone: %v", files, err)
	}

	out := filepath.Join(t.TempDir(), "scrubbed")
	report, err := scrubDirectory(collected, out, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, planted := range ghawPlanted {
		if report.dropped[planted.path] != 1 {
			t.Errorf("the scrub dropped %s %d times, want once", planted.path, report.dropped[planted.path])
		}
	}
	for _, path := range ghawUnknownInSchemaObjects {
		if report.dropped[path] != 1 {
			t.Errorf("the scrub dropped %s %d times, want once", path, report.dropped[path])
		}
	}
	for _, path := range ghawFreeText {
		if report.dropped[path] != 2 {
			t.Errorf("the scrub dropped %s %d times, want once in each record", path, report.dropped[path])
		}
	}
	scrubbedRecord := strings.Replace(strings.Replace(ghawRecord, "msg_01PlantedRequest", "req_fixture_0001", 1), `"reason":"upstream_5xx","status":529}]`, `"status":529}]`, 1)
	want := scrubbedRecord + "\n" + scrubbedRecord + "\n"
	if got, _ := os.ReadFile(filepath.Join(out, ghawUsageFile)); string(got) != want {
		t.Errorf("scrubbed\n got %s\nwant %s", got, want)
	}
	m, err := readManifest(out)
	if err != nil || m.Agent != agentGhAw || m.Version != "0.29.8" {
		t.Errorf("manifest %+v, %v", m, err)
	}
	if problems, err := verifyDirectory(out, []string{plantedPrompt}); err != nil || len(problems) > 0 {
		t.Errorf("the scrubbed recording fails verify: %v %v", problems, err)
	}

	// The same raw file, unscrubbed, is refused key by key.
	unscrubbed := filepath.Join(t.TempDir(), "unscrubbed")
	write(t, filepath.Join(unscrubbed, ghawUsageFile), strings.Replace(withPlanted(t), "msg_01PlantedRequest", "req_fixture_0001", 1))
	if err := writeManifest(unscrubbed, m); err != nil {
		t.Fatal(err)
	}
	problems, err := verifyDirectory(unscrubbed, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range slices.Concat(ghawUnknownInSchemaObjects, ghawFreeText, []string{"prompt", "messages", "headers", "cwd", "token_usage_detail"}) {
		if !slices.Contains(problems, ghawUsageFile+":1: key outside the allowlist: "+path) {
			t.Errorf("verify did not refuse %s: %v", path, problems)
		}
	}
}

func TestGhAwCollectFindsOneUsageFile(t *testing.T) {
	for _, tc := range []struct {
		name  string
		files map[string]string
		want  string
	}{
		{"an older run's artifact", map[string]string{"firewall-audit-logs/api-proxy-logs/token-usage.jsonl": ghawRecord}, ""},
		{"two equal copies", map[string]string{"agent/token-usage.jsonl": ghawRecord, "usage/token-usage.jsonl": ghawRecord}, ""},
		{"two copies that differ", map[string]string{"agent/token-usage.jsonl": ghawRecord, "usage/token-usage.jsonl": "{}"}, "agent/token-usage.jsonl and usage/token-usage.jsonl differ"},
		{"no usage file", map[string]string{"agent/agent_usage.json": "{}"}, "no token-usage.jsonl under --home"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home := t.TempDir()
			for name, data := range tc.files {
				write(t, filepath.Join(home, name), data)
			}
			files, err := collect(source{agent: agentGhAw, home: os.DirFS(home)})
			switch {
			case tc.want == "" && err != nil:
				t.Fatal(err)
			case tc.want == "" && string(files.files[ghawUsageFile]) != ghawRecord+"\n":
				t.Errorf("collected %q", files.files[ghawUsageFile])
			case tc.want != "" && (err == nil || !strings.Contains(err.Error(), tc.want)):
				t.Errorf("collect said %v, want %q", err, tc.want)
			}
		})
	}
	stream := filepath.Join(t.TempDir(), "stream.jsonl")
	write(t, stream, `{}`)
	if _, err := collect(source{agent: agentGhAw, stream: stream, home: os.DirFS(t.TempDir())}); err == nil || !strings.Contains(err.Error(), "leaves no stream") {
		t.Errorf("a stream was taken for gh-aw: %v", err)
	}
}

func TestGhAwReadmeStatesTheCreditsAsGhAwsFigure(t *testing.T) {
	entry, err := agentFor(agentGhAw)
	if err != nil {
		t.Fatal(err)
	}
	line, _ := parseValue([]byte(ghawRecord))
	files := []*recordingFile{{name: ghawUsageFile, lines: []*value{line}}}
	m := manifest{Recorded: true, Agent: agentGhAw, Version: "0.29.8", Scenario: "gh-aw-claude", Run: "19283746501"}
	text := readme(m, entry, files)
	for _, want := range []string{
		"A recording of gh-aw's token-usage records, schema 0.29.8, workflow run 19283746501",
		"gh-aw's own figure for the run: 1.37 AI credits, the `ai_credits_total` of the last line in `token-usage.jsonl` that states it. Costroid does not convert credits.",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("README lacks %q:\n%s", want, text)
		}
	}
}

func sameSet(a, b map[string]bool) bool {
	return slices.Equal(sortedSet(a), sortedSet(b))
}

func sortedSet(m map[string]bool) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
}
