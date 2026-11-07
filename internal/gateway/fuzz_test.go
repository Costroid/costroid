package gateway

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/Costroid/costroid/internal/decimal"
)

// sealedSeed is a request sealed to the tests' key for the tests' run, as
// costroid gateway request writes it.
func sealedSeed(f *testing.F) []byte {
	f.Helper()
	request, _, err := NewRequest(sealingPublic, run)
	if err != nil {
		f.Fatal(err)
	}
	encoded, err := json.Marshal(request)
	if err != nil {
		f.Fatal(err)
	}
	return encoded
}

// FuzzReadRequest reads a request file another job uploaded. A request read
// states this release's format, and it opens for the run it names only to the
// key its own id names: a request no one sealed never yields a key.
func FuzzReadRequest(f *testing.F) {
	sealed := sealedSeed(f)
	f.Add(sealed)
	f.Add(bytes.Replace(sealed, []byte(`"run_id":"42"`), []byte(`"run_id":"43"`), 1))
	f.Add([]byte(`{"format":"` + legacyRequestFormat + `"}`))
	f.Add([]byte(`{"format":"` + requestFormat + `","sealed":"","key_id":""}`))
	f.Add(bytes.Repeat([]byte(" "), maxRequestBytes+1))
	f.Fuzz(func(t *testing.T, data []byte) {
		request, err := ReadRequest(bytes.NewReader(data))
		if err != nil {
			return
		}
		if request.Format != requestFormat || len(data) > maxRequestBytes {
			t.Fatalf("read a request of format %q from %d bytes", request.Format, len(data))
		}
		key, err := request.Open(sealing, request.Binding)
		if err != nil {
			return
		}
		if !strings.HasPrefix(key, keyPrefix) || KeyID(key) != request.KeyID || request.Binding != run || request.SealingKey != sealingPublic {
			t.Fatalf("a request for %+v sealed to %q opened to a key of id %s", request.Binding, request.SealingKey, KeyID(key))
		}
	})
}

// FuzzRequestArtifact reads the zip archive the request job uploaded. It
// reads only the request file, and never more than a request's bound.
func FuzzRequestArtifact(f *testing.F) {
	for _, name := range []string{RequestFile, "other.json"} {
		var archive bytes.Buffer
		writer := zip.NewWriter(&archive)
		entry, err := writer.Create(name)
		if err != nil {
			f.Fatal(err)
		}
		if _, err := entry.Write(sealedSeed(f)); err != nil {
			f.Fatal(err)
		}
		if err := writer.Close(); err != nil {
			f.Fatal(err)
		}
		f.Add(archive.Bytes())
	}
	f.Fuzz(func(t *testing.T, archive []byte) {
		request, err := requestIn(archive)
		if err == nil && request.Format != requestFormat {
			t.Fatalf("read a request of format %q", request.Format)
		}
	})
}

// FuzzReadBinding reads the run from the job's variables. A binding read
// names a repository, a run and an attempt, and only on GitHub Actions.
func FuzzReadBinding(f *testing.F) {
	f.Add("true", "acme/widgets", "42", "1")
	f.Add("true", "", "42", "1")
	f.Add("false", "acme/widgets", "42", "1")
	f.Add("true", "acme/widgets:x", "4\n2", "1")
	f.Fuzz(func(t *testing.T, actions, repository, runID, attempt string) {
		env := map[string]string{"GITHUB_ACTIONS": actions, "GITHUB_REPOSITORY": repository, "GITHUB_RUN_ID": runID, "GITHUB_RUN_ATTEMPT": attempt}
		binding, err := ReadBinding(func(name string) string { return env[name] })
		if err != nil {
			return
		}
		if actions != "true" || binding.Repository == "" || binding.Run == "" || binding.Attempt != attempt {
			t.Fatalf("read %+v from %q", binding, env)
		}
		if !strings.HasPrefix(binding.Alias(), "costroid:") || !strings.HasSuffix(binding.Alias(), ":"+attempt) {
			t.Fatalf("alias %q for %+v", binding.Alias(), binding)
		}
	})
}

// FuzzInspect holds a key the gateway answered for to the run's needs. A key
// the inspection grants has a budget above zero and at or under the run's, a
// parallel limit, a largest request, and the run's own id.
func FuzzInspect(f *testing.F) {
	f.Add([]byte(`{"max_budget":5,"spend":0,"expires":"2026-11-07T12:00:00Z","max_parallel_requests":2,"metadata":{"costroid_max_request_tokens":"200000","costroid_run_id":"42"}}`))
	f.Add([]byte(`{"max_budget":"5","expires":"2026-11-07 12:00:00","max_parallel_requests":0,"metadata":{"costroid_run_id":42}}`))
	f.Add([]byte(`{"max_budget":1e-9,"expires":null}`))
	f.Add([]byte(`{"max_budget":50,"expires":"2026-11-07T12:00:00Z","max_parallel_requests":2,"metadata":{"costroid_max_request_tokens":"200000","costroid_run_id":"42"}}`))
	budget := decimal.MustParse("5")
	now := time.Date(2026, 11, 7, 11, 0, 0, 0, time.UTC)
	f.Fuzz(func(t *testing.T, answer []byte) {
		var info KeyInfo
		if json.Unmarshal(answer, &info) != nil {
			return
		}
		grant := Inspect("sk-costroid-fuzz", info, Expect{Budget: budget, Run: "42", Now: now})
		if len(grant.Problems) > 0 {
			return
		}
		if grant.MaxBudget.Sign() <= 0 || grant.MaxBudget.Cmp(budget) > 0 || grant.MaxParallel < 1 || grant.MaxRequestTokens < 1 {
			t.Fatalf("granted %+v from %s", grant, answer)
		}
		if expires, set, err := info.ExpiresAt(); err != nil || !set || !expires.After(now) {
			t.Fatalf("granted a key that expires at %v, %t, %v", expires, set, err)
		}
		if metadata(info, MetaRun) != "42" {
			t.Fatalf("granted a key minted for run %q", metadata(info, MetaRun))
		}
	})
}
