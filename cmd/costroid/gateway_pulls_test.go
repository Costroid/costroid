package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/Costroid/costroid/internal/gateway"
	"github.com/Costroid/costroid/internal/gateway/gatewaytest"
)

// eventPull is one entry of the pull requests a workflow_run event lists, in
// the shape recorded GitHub Actions run 45377273123 read on the requested
// event: its number, its head's commit and branch, and its base's branch and
// commit.
func eventPull(number int) map[string]any {
	return map[string]any{
		"number": number,
		"head":   map[string]any{"sha": headCommit, "ref": "fix-the-test"},
		"base":   map[string]any{"ref": "main", "sha": baseCommit},
	}
}

// TestMintStatesThePullRequestsTheEventLists mints from the run workflow_run
// started after a pull request's run, as the gateway recipe does, for each
// list GitHub's event can hold. The attestation names every pull request the
// event lists: the one a branch of this repository opened, and both of two
// that share a head. For a run the event lists none for, the body leaves the
// field out, as an earlier release sent it. A fork's run gets no key, and no
// attestation is sent.
func TestMintStatesThePullRequestsTheEventLists(t *testing.T) {
	job := newGatewayJob(t)
	job.request(t)
	w := newAttestWorld(t)
	t.Setenv(ingestURLVariable, w.ledger.URL+"/ledger/v1/rows")
	trusted := http.DefaultTransport
	http.DefaultTransport = w.ledger.Client().Transport
	t.Cleanup(func() { http.DefaultTransport = trusted })
	request := filepath.Join(job.dir, gateway.RequestFile)

	for name, tc := range map[string]struct {
		listed []any
		want   []string
	}{
		"a branch of this repository":            {[]any{eventPull(7)}, []string{"7"}},
		"a head two pull requests share":         {[]any{eventPull(7), eventPull(9)}, []string{"7", "9"}},
		"a run GitHub lists no pull request for": {[]any{}, nil},
	} {
		t.Run(name, func(t *testing.T) {
			job.triggered(t, func(f map[string]any) { f["pull_requests"] = tc.listed })
			w.raw = nil
			code, _, stderr := job.mint(t, gatewaytest.New(t, gatewayAdmin), "--for-triggering-run", "--request", request)
			if code != 0 || !strings.Contains(stderr, "attested to the ledger") {
				t.Fatalf("exit %d: %s", code, stderr)
			}
			if !reflect.DeepEqual(w.body.PullRequests, tc.want) {
				t.Fatalf("the attestation names %q, and the event lists %v", w.body.PullRequests, tc.listed)
			}
			if tc.want == nil && bytes.Contains(w.raw, []byte("pull_requests")) {
				t.Fatalf("an attestation naming no pull request states the field: %s", w.raw)
			}
		})
	}

	t.Run("a fork", func(t *testing.T) {
		job.triggered(t, func(f map[string]any) {
			f["head_repository"] = map[string]any{"full_name": "fork/widgets"}
		})
		w.raw = nil
		code, _, stderr := job.mint(t, gatewaytest.New(t, gatewayAdmin), "--for-triggering-run", "--request", request)
		if code != exitFailure || w.raw != nil {
			t.Fatalf("exit %d, the ledger read %s: %s", code, w.raw, stderr)
		}
	})
}

// earlierAttestation is the body costroid serve read before an attestation
// named its pull requests.
type earlierAttestation struct {
	KeyID      string `json:"key_id"`
	Repository string `json:"repository"`
	RunID      string `json:"run_id"`
	RunAttempt string `json:"run_attempt"`
	HeadSHA    string `json:"head_sha"`
}

// answerAsAnEarlierRelease answers an attestation as costroid serve did
// before it kept the pull requests one names: it refuses any field it does
// not know, with the code and the message it gives every body it cannot read.
func answerAsAnEarlierRelease(rw http.ResponseWriter, body []byte) {
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&earlierAttestation{}) != nil {
		refuseAs(rw, http.StatusBadRequest, gateway.CodeMalformed)
		return
	}
	rw.WriteHeader(http.StatusNoContent)
}

func refuseAs(rw http.ResponseWriter, status int, code string) {
	rw.Header().Set("Content-Type", "application/json")
	rw.WriteHeader(status)
	_ = json.NewEncoder(rw).Encode(map[string]any{"error": map[string]string{
		"code": code, "message": "The body is not an attestation with a 64-character key_id, a run_id, a run_attempt and a head_sha.",
	}})
}

// answeringLedger is a ledger that answers each attestation as answer does,
// and keeps every body in the order it came.
type answeringLedger struct {
	*httptest.Server
	mu     sync.Mutex
	bodies [][]byte
}

func newAnsweringLedger(t *testing.T, answer func(http.ResponseWriter, []byte)) *answeringLedger {
	t.Helper()
	ledger := &answeringLedger{}
	ledger.Server = httptest.NewTLSServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		ledger.mu.Lock()
		ledger.bodies = append(ledger.bodies, body)
		ledger.mu.Unlock()
		answer(rw, body)
	}))
	t.Cleanup(ledger.Close)
	return ledger
}

// TestMintAttestsToAnOlderLedgerWithoutThem attests a key naming a pull
// request to a ledger of an earlier release, which refuses the field it does
// not know as a body it cannot read. Mint sends the attestation again without
// the pull requests, the ledger takes it, and mint warns in one line that the
// ledger is older, what that means, and to upgrade it. Only that refusal, of
// an attestation that names pull requests, is sent again; a second refusal
// is the answer, and every other refusal stands as it came.
func TestMintAttestsToAnOlderLedgerWithoutThem(t *testing.T) {
	refusing := func(status int, code string) func(http.ResponseWriter, []byte) {
		return func(rw http.ResponseWriter, _ []byte) { refuseAs(rw, status, code) }
	}
	taking := func(rw http.ResponseWriter, _ []byte) { rw.WriteHeader(http.StatusNoContent) }
	for name, tc := range map[string]struct {
		answer  func(http.ResponseWriter, []byte)
		pulls   []string
		sends   int
		refused bool
		warned  string
	}{
		"an earlier release, naming a pull request":      {answerAsAnEarlierRelease, []string{"7"}, 2, false, olderLedger},
		"an earlier release, naming none":                {answerAsAnEarlierRelease, nil, 1, false, ""},
		"this release":                                   {taking, []string{"7", "9"}, 1, false, ""},
		"a ledger that refuses every body as unreadable": {refusing(http.StatusBadRequest, gateway.CodeMalformed), []string{"7"}, 2, true, ""},
		"a ledger that refuses a body without its code":  {refusing(http.StatusBadRequest, ""), []string{"7"}, 1, true, ""},
		"a ledger that refuses a body naming none":       {refusing(http.StatusBadRequest, gateway.CodeMalformed), nil, 1, true, ""},
		"a ledger that holds the key attested otherwise": {refusing(http.StatusConflict, "CREDENTIAL_ATTESTED_ELSEWHERE"), []string{"7"}, 1, true, ""},
		"a ledger that takes no run of an open branch":   {refusing(http.StatusForbidden, gateway.CodeRunUnprotected), []string{"7"}, 1, false, unprotectedBranchKey},
		"a ledger that gives the code under 422":         {refusing(http.StatusUnprocessableEntity, gateway.CodeMalformed), []string{"7"}, 1, true, ""},
	} {
		t.Run(name, func(t *testing.T) {
			w := newAttestWorld(t)
			ledger := newAnsweringLedger(t, tc.answer)
			t.Setenv(ingestURLVariable, ledger.URL+"/v1/rows")
			attestation := gateway.AttestationFor(attestedKeyID, gateway.Binding{Repository: "github.com/acme/widgets", Run: attestedRunID, Attempt: attestedAttempt}, headCommit)
			attestation.PullRequests = tc.pulls
			var stderr bytes.Buffer
			_, err := attestKey(context.Background(), ledger.Client(), attestation, true, &stderr)
			var refused ledgerRefusal
			if errors.As(err, &refused) != tc.refused || (!tc.refused && err != nil) {
				t.Fatalf("attest: %v", err)
			}
			if len(ledger.bodies) != tc.sends {
				t.Fatalf("mint sent %d bodies, want %d: %q", len(ledger.bodies), tc.sends, ledger.bodies)
			}
			if tc.sends == 2 {
				var first, second gateway.Attestation
				if json.Unmarshal(ledger.bodies[0], &first) != nil || json.Unmarshal(ledger.bodies[1], &second) != nil ||
					!reflect.DeepEqual(first, attestation) || bytes.Contains(ledger.bodies[1], []byte("pull_requests")) {
					t.Fatalf("mint sent %q", ledger.bodies)
				}
				attestation.PullRequests = nil
				if !reflect.DeepEqual(second, attestation) {
					t.Fatalf("the second body is not the first without its pull requests: %s", ledger.bodies[1])
				}
			}
			if got := strings.Count(stderr.String(), "costroid: warning:"); tc.warned == "" && got != 0 || tc.warned != "" && (got != 1 || !strings.Contains(stderr.String(), tc.warned)) {
				t.Fatalf("mint said %q", stderr.String())
			}
			if strings.Contains(stderr.String(), issuedToken) || w.audience != ingestAudience {
				t.Fatalf("mint asked for audience %q and said %q", w.audience, stderr.String())
			}
		})
	}
}
