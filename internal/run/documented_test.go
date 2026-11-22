package run

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"regexp"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Costroid/costroid/internal/docstest"
	"github.com/Costroid/costroid/internal/gateway"
)

// movingGateway is a gateway whose count of the key's spend differs at every
// read, so it never settles.
func movingGateway(t *testing.T) *gateway.Session {
	t.Helper()
	var reads atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/key/info":
			spend := fmt.Sprintf(`"spend":0.0000%d1`, reads.Add(1))
			w.Write([]byte(strings.Replace(keyInfoHolding, `"spend":0.00060589`, spend, 1)))
		case "/health/readiness":
			w.Write([]byte(`{"status":"healthy","db":"connected"}`))
		}
	}))
	t.Cleanup(server.Close)
	return &gateway.Session{
		Client: gateway.NewClient(server.URL), Key: gatewayKey,
		Wait: time.Second, Every: 20 * time.Millisecond, Poll: 10 * time.Millisecond, Settle: 300 * time.Millisecond, Stable: 50 * time.Millisecond,
	}
}

// pageWaits are the two durations a page states for a job's wait on the
// gateway's count.
var pageWaits = regexp.MustCompile(` in the last \S+ of a \S+ wait,`)

// TestEveryDocumentedOutputIsWhatTheCommandPrints holds the three warnings
// docs/gateway.md shows for a run under a gateway to the warnings such a run
// gives: one whose rows pass the key's budget by more than their bound, one
// whose gateway counted another figure than the rows come to, and one whose
// gateway was still counting when the wait ended. Each is the page's line
// after "costroid: warning: ", which costroid run prints before a warning,
// with the run's own figure in place of each placeholder. The page states
// the durations a job waits, and the last run waits two shorter ones.
func TestEveryDocumentedOutputIsWhatTheCommandPrints(t *testing.T) {
	healthy := `{"status":"healthy","db":"connected"}`
	lower := strings.Replace(keyInfoHolding, `"spend":0.00060589`, `"spend":0.0004651`, 1)
	moving := movingGateway(t)
	for _, shown := range []struct {
		heading string
		runs    []*gateway.Session
	}{
		{"What the rows say", []*gateway.Session{fakeGateway(t, keyInfoWith("0.0001", "100"), healthy)}},
		{"The gateway holds the budget in its own prices", []*gateway.Session{fakeGateway(t, lower, healthy), moving}},
	} {
		lines := docstest.At(t, "../..", docstest.Place{Page: "docs/gateway.md", Heading: shown.heading, Language: "text"})
		if len(lines) != len(shown.runs) {
			t.Fatalf("docs/gateway.md shows %d lines under %q, and this test runs %d gateways for them", len(lines), shown.heading, len(shown.runs))
		}
		for i, session := range shown.runs {
			got, _ := gatewayRunWith(t, session)
			if got.err != nil {
				t.Fatal(got.err)
			}
			line, warned := strings.CutPrefix(lines[i], "costroid: warning: ")
			if session == moving {
				line = pageWaits.ReplaceAllString(line, " in the last "+session.Stable.String()+" of a "+session.Settle.String()+" wait,")
			}
			if !warned || !slices.ContainsFunc(got.result.Report.Warnings, func(warning string) bool { return docstest.Fills(line, warning) }) {
				t.Errorf("under %q the page shows\n%s\nand the run warns\n%s", shown.heading, lines[i], strings.Join(got.result.Report.Warnings, "\n"))
			}
		}
	}
}
