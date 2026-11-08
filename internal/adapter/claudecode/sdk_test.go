package claudecode

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Costroid/costroid/internal/adapter"
	"github.com/Costroid/costroid/internal/decimal"
	"github.com/Costroid/costroid/internal/focus"
)

const (
	sdkTS        = "testdata/2.1.316/sdk-claude-ts"
	sdkSession   = "00000000-0000-4000-8000-000000000001"
	recordedHour = "2026-11-08T04:14"
)

// sdkWindow is a window around the TypeScript recording's session, which ran
// from 04:14:52.108 to 04:14:54.021.
var (
	sdkFrom = time.Date(2026, 11, 8, 4, 14, 50, 0, time.UTC)
	sdkTo   = time.Date(2026, 11, 8, 4, 15, 0, 0, time.UTC)
)

// sdkCopy is the TypeScript recording's session file under another session
// id and other request ids, with its times moved to another minute and its
// working directory changed, as another query() would write it.
type sdkCopy struct {
	id, minute, cwd string
	// lastLine is a line added at the end, timed after the session.
	lastLine string
}

func (c sdkCopy) write(t *testing.T, config string) {
	t.Helper()
	text := strings.NewReplacer(
		sdkSession, c.id,
		"req_fixture_", "req_"+c.id[len(c.id)-2:]+"_",
		recordedHour, c.minute,
		`"cwd":"/work"`, `"cwd":"`+c.cwd+`"`,
	).Replace(string(mustReadFile(t, filepath.Join(sdkTS, "session.jsonl"))))
	if c.lastLine != "" {
		text += strings.ReplaceAll(c.lastLine, "@session", c.id) + "\n"
	}
	project := filepath.Join(config, "projects", "-"+strings.Trim(strings.ReplaceAll(c.cwd, "/", "-"), "-"))
	if err := os.MkdirAll(project, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(project, c.id+".jsonl"), []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
}

// sdkConfig lays out the recorded session and seven others around it.
func sdkConfig(t *testing.T) string {
	t.Helper()
	config := installFrom(t, sdkTS, sdkSession)
	for _, other := range []sdkCopy{
		{id: "00000000-0000-4000-8000-0000000000a2", minute: "2026-11-08T04:14", cwd: "/work"},
		{id: "00000000-0000-4000-8000-0000000000a3", minute: "2026-11-08T03:14", cwd: "/work"},
		{id: "00000000-0000-4000-8000-0000000000a4", minute: "2026-11-08T05:14", cwd: "/work"},
		{id: "00000000-0000-4000-8000-0000000000a5", minute: "2026-11-08T04:14", cwd: "/elsewhere"},
		{id: "00000000-0000-4000-8000-0000000000a6", minute: "2026-11-08T04:14", cwd: "/work",
			lastLine: `{"type":"user","sessionId":"@session","timestamp":"2026-11-08T04:16:00.000Z","cwd":"/work"}`},
		{id: "00000000-0000-4000-8000-0000000000a7", minute: "2026-11-08T03:14", cwd: "/work",
			lastLine: `{"type":"user","sessionId":"@session","timestamp":"2026-11-08T04:14:55.000Z","cwd":"/work"}`},
		{id: "00000000-0000-4000-8000-0000000000a8", minute: "2026-11-08T04:14", cwd: "/work",
			lastLine: `{"type":"user","sessionId":"@session","isSidechain":true,"timestamp":"2026-11-08T04:14:55.000Z","cwd":"/work"}`},
	} {
		other.write(t, config)
	}
	return config
}

func readSDK(t *testing.T, agent SDKAgent) adapter.Report {
	t.Helper()
	t.Setenv(oneHourCacheVariable, "")
	t.Setenv(apiKeyVariable, "a-key")
	session := agent.NewSession(adapter.Plan{})
	session.(*SDKSession).RunWindow(sdkFrom, sdkTo)
	report, err := session.Finish()
	if err != nil {
		t.Fatal(err)
	}
	return report
}

func sessionIDs(report adapter.Report) []string {
	ids := []string{report.SessionID}
	for _, nested := range report.Nested {
		ids = append(ids, nested.SessionID)
	}
	return ids
}

func holdsWarning(warnings []string, part string) bool {
	return slices.ContainsFunc(warnings, func(warning string) bool { return strings.Contains(warning, part) })
}

// Every session that started and ended while the program ran is the run's,
// the earliest naming it and each other under its own id. A session before or
// after the window, a subagent's, one that started before the run or wrote
// after it, and outside CI one in another working directory, are not, and
// those that wrote inside the window are named. In CI every directory counts.
func TestSDKSessionsInTheRunWindowAreTheRuns(t *testing.T) {
	t.Run("outside CI", func(t *testing.T) {
		outsideCI(t)
		report := readSDK(t, SDKAgent{ConfigDir: sdkConfig(t), Dir: "/work"})
		if got, want := sessionIDs(report), []string{sdkSession, "00000000-0000-4000-8000-0000000000a2"}; !slices.Equal(got, want) {
			t.Fatalf("sessions %v, want %v", got, want)
		}
		for _, want := range []string{
			"session 00000000-0000-4000-8000-0000000000a5 started while the program ran in another working directory, so it was not counted",
			"session 00000000-0000-4000-8000-0000000000a6 started while the program ran and wrote after it ended, so it was not counted",
			"session 00000000-0000-4000-8000-0000000000a7 started before the program ran and wrote while it ran, so it holds earlier spend and was not counted",
		} {
			if !holdsWarning(report.Warnings, want) {
				t.Errorf("no warning %q in %q", want, report.Warnings)
			}
		}
		for _, quiet := range []string{"0000000000a3", "0000000000a4", "0000000000a8"} {
			if holdsWarning(report.Warnings, quiet) {
				t.Errorf("a session outside the window, or a subagent's, is named: %q", report.Warnings)
			}
		}
	})
	t.Run("in CI", func(t *testing.T) {
		insideCI(t, "GITHUB_ACTIONS")
		report := readSDK(t, SDKAgent{ConfigDir: sdkConfig(t), Dir: "/work"})
		// The three began at one instant, so the order among them is the files'.
		want := []string{sdkSession, "00000000-0000-4000-8000-0000000000a2", "00000000-0000-4000-8000-0000000000a5"}
		if got := slices.Sorted(slices.Values(sessionIDs(report))); !slices.Equal(got, want) {
			t.Fatalf("sessions %v, want %v", got, want)
		}
	})
	t.Run("none", func(t *testing.T) {
		outsideCI(t)
		report := readSDK(t, SDKAgent{ConfigDir: t.TempDir(), Dir: "/work"})
		if len(report.Requests) != 0 || len(report.Warnings) != 1 || !strings.Contains(report.Warnings[0], "no Claude Code session under") {
			t.Fatalf("%+v", report)
		}
	})
}

// The recorded session's file holds two requests, and the totals Claude Code
// states for it count a third, made outside the conversation. The totals
// stand, one set of rows per model, read from the session file, on the key.
func TestSDKSessionReadsTheTotalsItsFileStates(t *testing.T) {
	outsideCI(t)
	report := readSDK(t, SDKAgent{ConfigDir: installFrom(t, sdkTS, sdkSession), Dir: "/work"})
	want := adapter.Tokens{InputUncached: 1204, CacheRead: 1522, CacheWrite5m: 2205, Output: 115, CachedInputReported: 1522}
	if got := sum(report.Requests); got != want {
		t.Errorf("tokens %+v, want %+v", got, want)
	}
	if report.CostSource != SourceSessionFile || report.CostBasis != focus.Measured || report.AgentVersion != "2.1.316" || len(report.Requests) != 1 {
		t.Errorf("%s, %s, version %s, %d requests", report.CostSource, report.CostBasis, report.AgentVersion, len(report.Requests))
	}
	if request := report.Requests[0]; request.ServiceTier != "standard" || request.Region != "global" || request.Note != "" {
		t.Errorf("%+v", request)
	}
	if len(report.Warnings) != 1 || !strings.Contains(report.Warnings[0], "differ from the per-model totals Claude Code states") {
		t.Errorf("warnings %q", report.Warnings)
	}
}

// A session whose file agrees with its totals keeps one row set per request.
// One that ended before Claude Code wrote its totals keeps its file's.
func TestSDKSessionKeepsItsRequestsWhenTheyAgree(t *testing.T) {
	outsideCI(t)
	for name, change := range map[string]func(string) string{
		"agreeing totals": func(text string) string {
			return strings.NewReplacer(`"inputTokens":1204,"outputTokens":115`, `"inputTokens":4,"outputTokens":92`).Replace(text)
		},
		"no totals": func(text string) string {
			lines := strings.Split(strings.TrimSpace(text), "\n")
			return strings.Join(slices.DeleteFunc(lines, func(line string) bool { return strings.Contains(line, `"cost-state"`) }), "\n") + "\n"
		},
	} {
		t.Run(name, func(t *testing.T) {
			config := installFrom(t, sdkTS, sdkSession)
			path := filepath.Join(config, "projects", "-work", sdkSession+".jsonl")
			if err := os.WriteFile(path, []byte(change(string(mustReadFile(t, path)))), 0o644); err != nil {
				t.Fatal(err)
			}
			report := readSDK(t, SDKAgent{ConfigDir: config, Dir: "/work"})
			want := adapter.Tokens{InputUncached: 4, CacheRead: 1522, CacheWrite5m: 2205, Output: 92, CachedInputReported: 1522}
			if got := sum(report.Requests); len(report.Requests) != 2 || got != want {
				t.Errorf("%d requests, tokens %+v, want 2 and %+v", len(report.Requests), got, want)
			}
		})
	}
}

// The program runs as it is, under a grade that says where its budget sits.
func TestSDKPlanLeavesTheProgramAlone(t *testing.T) {
	argv := []string{"node", "agent.js", "--max-budget-usd", "1"}
	plan, err := SDKAgent{}.Plan(argv, decimal.MustParse("5"))
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(plan.Argv, argv) || plan.Enforcement.Grade != adapter.GradeNone || plan.Enforcement.Watchdog ||
		plan.Enforcement.Note != "none: the budget sits in the program's maxBudgetUsd, which Costroid cannot see" {
		t.Errorf("%+v", plan)
	}
}

// Under a gateway the program's Claude Code gets the gateway and the run's
// key, and every other variable that picks where requests go is emptied,
// because no settings file of the run's reaches it.
func TestSDKGatewayEnvEmptiesEveryRoute(t *testing.T) {
	env := SDKAgent{}.GatewayEnv("http://gateway.test", "run-key")
	if !slices.Contains(env, "ANTHROPIC_BASE_URL=http://gateway.test") || !slices.Contains(env, "ANTHROPIC_AUTH_TOKEN=run-key") || !slices.Contains(env, "ANTHROPIC_API_KEY=") {
		t.Errorf("%q", env)
	}
	for _, variable := range []string{"CLAUDE_CODE_USE_BEDROCK", "CLAUDE_CODE_USE_VERTEX", "ANTHROPIC_FOUNDRY_BASE_URL"} {
		if !slices.Contains(env, variable+"=") {
			t.Errorf("%s is not emptied: %q", variable, env)
		}
	}
	if args := (SDKAgent{}).GatewayArgs("http://gateway.test"); len(args) != 0 {
		t.Errorf("%q", args)
	}
}

// A settings file the program's Claude Code may read, which sends requests
// around the gateway, refuses a gateway run.
func TestSDKGatewayRefusesASettingsFileThatRoutesAround(t *testing.T) {
	for name, settings := range map[string]string{
		"base URL":   `{"env":{"ANTHROPIC_BASE_URL":"https://elsewhere.test"}}`,
		"provider":   `{"env":{"CLAUDE_CODE_USE_BEDROCK":"1"}}`,
		"key helper": `{"apiKeyHelper":"print-key"}`,
	} {
		for _, file := range []string{".claude/settings.json", ".claude/settings.local.json", "config/settings.json"} {
			t.Run(name+"/"+file, func(t *testing.T) {
				root := t.TempDir()
				agent := SDKAgent{ConfigDir: filepath.Join(root, "config"), Dir: root}
				if flag, err := agent.GatewayBypass(nil); err != nil || flag != "" {
					t.Fatalf("no settings file: %q, %v", flag, err)
				}
				path := filepath.Join(root, filepath.FromSlash(file))
				if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, []byte(settings), 0o644); err != nil {
					t.Fatal(err)
				}
				flag, err := agent.GatewayBypass(nil)
				if err != nil || !strings.HasPrefix(flag, path+" with ") {
					t.Errorf("%q, %v", flag, err)
				}
			})
		}
	}
}
