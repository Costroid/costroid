package claudecode

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Costroid/costroid/internal/adapter"
	"github.com/Costroid/costroid/internal/focus"
)

const (
	subscriptionRecording = "testdata/2.1.316/action-subscription"
	rateLimitLine         = `{"type":"rate_limit_event","session_id":"s"}`
	noKeyInit             = `{"type":"system","subtype":"init","session_id":"s","apiKeySource":"none"}`
)

// readSubscriptionRecording reads the recorded subscription login's execution
// file beside its session file, with its rate_limit_event kept or cut.
func readSubscriptionRecording(t *testing.T, keepRateLimit bool) adapter.Report {
	t.Helper()
	outsideCI(t)
	data, err := os.ReadFile(filepath.Join(subscriptionRecording, "claude-execution-output.json"))
	if err != nil {
		t.Fatal(err)
	}
	body := string(data)
	if !keepRateLimit {
		event := "  {\n    \"type\": \"rate_limit_event\",\n    \"uuid\": \"00000000-0000-4000-8000-000000000003\",\n    \"session_id\": \"00000000-0000-4000-8000-000000000001\"\n  },\n"
		if !strings.Contains(body, event) {
			t.Fatal("the recording's rate_limit_event is not where the test cuts it")
		}
		body = strings.Replace(body, event, "", 1)
	}
	config := installFrom(t, subscriptionRecording, "00000000-0000-4000-8000-000000000001")
	report, err := Agent{ConfigDir: config}.ReadExecutionFile(strings.NewReader(body), corpusSaved)
	if err != nil {
		t.Fatal(err)
	}
	return report
}

// TestTheRecordedSubscriptionLoginIsMarkedByItsRateLimitEvent reads the
// recorded claude-code-action run on claude_code_oauth_token. It is marked,
// and with its rate_limit_event cut the same file reads unmarked at the same
// basis and the same tokens: apiKeySource none alone proves nothing.
func TestTheRecordedSubscriptionLoginIsMarkedByItsRateLimitEvent(t *testing.T) {
	marked := readSubscriptionRecording(t, true)
	if !marked.Subscription || marked.CostBasis != focus.ListEquivalent {
		t.Fatalf("the recorded subscription login reads subscription %t, basis %s", marked.Subscription, marked.CostBasis)
	}
	unmarked := readSubscriptionRecording(t, false)
	if unmarked.Subscription {
		t.Error("apiKeySource none without a rate_limit_event marked the run")
	}
	if unmarked.CostBasis != marked.CostBasis || sum(unmarked.Requests) != sum(marked.Requests) {
		t.Errorf("the mark changed the reading: %s %+v, then %s %+v", marked.CostBasis, sum(marked.Requests), unmarked.CostBasis, sum(unmarked.Requests))
	}
}

// TestOnlyNoKeyAndARateLimitEventMarkASubscription feeds a stream each way
// one of the two proofs can be missing, and the one way a gateway bills the
// run, beside the two that hold. Order does not matter.
func TestOnlyNoKeyAndARateLimitEventMarkASubscription(t *testing.T) {
	cases := []struct {
		name      string
		lines     []string
		gatewayed bool
		want      bool
	}{
		{"none and a rate_limit_event", []string{noKeyInit, rateLimitLine}, false, true},
		{"a rate_limit_event before the init", []string{rateLimitLine, noKeyInit}, false, true},
		{"none alone", []string{noKeyInit}, false, false},
		{"an API key and a rate_limit_event", []string{executionInit, rateLimitLine}, false, false},
		{"no init and a rate_limit_event", []string{rateLimitLine}, false, false},
		{"an init that states no source", []string{`{"type":"system","subtype":"init","session_id":"s"}`, rateLimitLine}, false, false},
		{"a gateway's key", []string{noKeyInit, rateLimitLine}, true, false},
		{"a rate_limit_event as a subtype", []string{noKeyInit, `{"type":"system","subtype":"rate_limit_event"}`}, false, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			session := Agent{ConfigDir: t.TempDir()}.newSession()
			if tc.gatewayed {
				session.BilledThroughGateway()
			}
			for _, line := range append(tc.lines, executionResult) {
				if err := session.Feed([]byte(line), recorded); err != nil {
					t.Fatal(err)
				}
			}
			report, err := session.Finish()
			if err != nil {
				t.Fatal(err)
			}
			if report.Subscription != tc.want {
				t.Errorf("subscription %t, want %t", report.Subscription, tc.want)
			}
		})
	}
}

// TestNoWrittenFixtureIsMarked reads the execution files written by hand,
// one of them on apiKeySource none, none with a rate_limit_event. None is
// marked.
func TestNoWrittenFixtureIsMarked(t *testing.T) {
	outsideCI(t)
	for _, path := range []string{
		"testdata/not-recorded/execution-file/claude-execution-output.json",
		"testdata/synthetic/execution/odd-elements.json",
	} {
		file, err := os.Open(path)
		if err != nil {
			t.Fatal(err)
		}
		report, err := Agent{ConfigDir: t.TempDir()}.ReadExecutionFile(file, corpusSaved)
		file.Close()
		if err != nil {
			t.Fatalf("%s: %v", path, err)
		}
		if report.Subscription {
			t.Errorf("%s reads as a subscription", path)
		}
	}
}
