package reconcile

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/Costroid/costroid/internal/focus"
)

// anthropicUsageOnly answers the usage endpoint with one bucket holding usage,
// and every other endpoint with an empty page.
func anthropicUsageOnly(usage string) Doer {
	return doerFunc(func(request *http.Request) (*http.Response, error) {
		body := `{"data":[],"has_more":false,"next_page":null}`
		if strings.HasSuffix(request.URL.Path, "/usage") {
			body = `{"data":[{"starting_at":"2026-10-14T00:00:00Z","group":{"api_key_id":"` + anthropicID + `","model":"` + sonnet + `"},"usage":` +
				usage + `}],"has_more":false,"next_page":null}`
		}
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(body))}, nil
	})
}

func TestAnthropicNegativeCountRefused(t *testing.T) {
	cases := map[focus.TokenClass]string{
		focus.InputUncached: `{"uncached_input_tokens":-1}`,
		focus.CacheRead:     `{"cache_read_input_tokens":-2}`,
		focus.CacheWrite5m:  `{"cache_creation":{"ephemeral_5m_input_tokens":-3}}`,
		focus.CacheWrite1h:  `{"cache_creation":{"ephemeral_1h_input_tokens":-4}}`,
		focus.Output:        `{"output_tokens":-5}`,
	}
	days, err := ParseRange(day, day)
	if err != nil {
		t.Fatal(err)
	}
	for class, usage := range cases {
		t.Run(string(class), func(t *testing.T) {
			_, err := NewAnthropic(anthropicUsageOnly(usage), sampleKey).Fetch(context.Background(), days, []string{anthropicID})
			if err == nil {
				t.Fatal("a count below zero was accepted")
			}
			for _, want := range []string{day, sonnet, string(class), "below zero"} {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("error %q does not name %s", err, want)
				}
			}
		})
	}
	zero := `{"uncached_input_tokens":0,"output_tokens":0}`
	if _, err := NewAnthropic(anthropicUsageOnly(zero), sampleKey).Fetch(context.Background(), days, []string{anthropicID}); err != nil {
		t.Errorf("a count of zero was refused: %v", err)
	}
}
