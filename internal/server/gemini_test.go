package server

import (
	"bytes"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Costroid/costroid/internal/otlp"
)

// geminiLogs is the otlp package's Gemini CLI api_response fixture, built from
// Gemini CLI's source, with its stated total of 13350 tokens replaced and its
// records moved to an hour before the test clock.
func geminiLogs(t *testing.T, total string) []byte {
	t.Helper()
	body, err := os.ReadFile(filepath.Join("..", "otlp", "testdata", "gemini", "api-response-logs.from-source.json"))
	if err != nil {
		t.Fatal(err)
	}
	stated := []byte(`"intValue": "13350"`)
	if !bytes.Contains(body, stated) {
		t.Fatal("the fixture states no total of 13350")
	}
	body = bytes.Replace(body, stated, []byte(`"intValue": "`+total+`"`), 1)
	at := strconv.FormatInt(testNow.Add(-time.Hour).UnixNano(), 10)
	return bytes.ReplaceAll(body, []byte("1794132000000000000"), []byte(at))
}

func TestServeWarnsOnceOnAGeminiTotalThatDisagrees(t *testing.T) {
	for name, tc := range map[string]struct {
		total    string
		warnings int
	}{
		"a total that agrees": {"13350", 0},
		"a total under them":  {"13000", 1},
	} {
		t.Run(name, func(t *testing.T) {
			w, ledger := memoryWorld(t)
			var logged bytes.Buffer
			w.server.Logger = slog.New(slog.NewJSONHandler(&logged, nil))
			w.send(http.MethodPost, "/otlp/v1/logs", w.token(), "application/json", geminiLogs(t, tc.total)).want(t, http.StatusOK)
			records := ledger.stored(t, tenantA)
			if len(records) == 0 {
				t.Fatal("no row stored")
			}
			for _, record := range records {
				noted := strings.Contains(record.Row.ChargeDescription, otlp.NoteTotalDisagrees)
				if noted != (tc.warnings > 0) {
					t.Errorf("%s: %q", record.Row.SkuMeter, record.Row.ChargeDescription)
				}
			}
			warnings := strings.Count(logged.String(), "telemetry total is not the sum of its counts")
			if warnings != tc.warnings {
				t.Fatalf("%d warnings, want %d: %s", warnings, tc.warnings, logged.String())
			}
			if tc.warnings > 0 && !strings.Contains(logged.String(), `"request":"record:`) {
				t.Fatalf("the warning names no request: %s", logged.String())
			}
		})
	}
}
