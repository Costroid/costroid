package gateway

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/Costroid/costroid/internal/cicontext"
)

// RequestFile is the name of the sealed request inside its artifact.
const RequestFile = "costroid-gateway-request.json"

// maxRequestBytes bounds a request file. A real one is under 1 KiB.
const maxRequestBytes = 64 << 10

// Binding names the run a key is for. A key sealed for one binding opens for
// no other.
type Binding struct {
	Repository string `json:"repository"`
	Run        string `json:"run_id"`
	Attempt    string `json:"run_attempt"`
}

// ReadBinding reads the run from GitHub Actions' variables.
func ReadBinding(getenv func(string) string) (Binding, error) {
	if cicontext.Detect(getenv) != cicontext.GitHubActions {
		return Binding{}, errors.New("gateway: a run key is bound to a GitHub Actions run, and this is not one")
	}
	ci, _ := cicontext.Read(getenv)
	binding := Binding{Repository: ci.Repository, Run: ci.Run, Attempt: getenv("GITHUB_RUN_ATTEMPT")}
	if binding.Repository == "" || binding.Run == "" || binding.Attempt == "" {
		return Binding{}, errors.New("gateway: GITHUB_REPOSITORY, GITHUB_RUN_ID and GITHUB_RUN_ATTEMPT must all be set")
	}
	return binding, nil
}

func (b Binding) info() []byte {
	return []byte(strings.Join([]string{requestFormat, b.Repository, b.Run, b.Attempt}, "\n"))
}

// Alias is the key's alias on the gateway. It names the run and the attempt,
// so the job that deletes the key needs nothing from the jobs before it.
func (b Binding) Alias() string {
	return strings.Join([]string{"costroid", b.Repository, b.Run, b.Attempt}, ":")
}

// legacyRequestFormat is the format of versions that sealed to a public key
// derived from the admin key.
const legacyRequestFormat = "costroid gateway request v1"

// Request is the sealed key and the run it is for. It holds no secret: only a
// holder of the sealing key's private half can open it.
type Request struct {
	Format string `json:"format"`
	Binding
	// SealingKey is the public key the key was sealed to.
	SealingKey string `json:"sealing_key"`
	KeyID      string `json:"key_id"`
	Sealed     string `json:"sealed"`
}

// NewRequest draws a key for the run and seals it to the gateway's public key.
// The key is returned to be kept on the runner, and never written to the request.
func NewRequest(publicKey string, binding Binding) (Request, string, error) {
	key := NewKey()
	sealed, err := seal(publicKey, binding, key)
	if err != nil {
		return Request{}, "", err
	}
	return Request{Format: requestFormat, Binding: binding, SealingKey: publicKey, KeyID: KeyID(key), Sealed: sealed}, key, nil
}

// ReadRequest parses a request file.
func ReadRequest(source io.Reader) (Request, error) {
	data, err := io.ReadAll(io.LimitReader(source, maxRequestBytes+1))
	if err != nil {
		return Request{}, fmt.Errorf("gateway: read the request: %w", err)
	}
	if len(data) > maxRequestBytes {
		return Request{}, fmt.Errorf("gateway: the request is over %d bytes", maxRequestBytes)
	}
	var request Request
	if err := json.Unmarshal(data, &request); err != nil {
		return Request{}, fmt.Errorf("gateway: parse the request: %w", err)
	}
	if request.Format == legacyRequestFormat {
		return Request{}, ErrDerivedSealingKey
	}
	if request.Format != requestFormat {
		return Request{}, fmt.Errorf("gateway: the request's format is %q, not %q", request.Format, requestFormat)
	}
	return request, nil
}

// Open checks that the request names the run the caller is in and was sealed
// to this sealing key, then opens it. A request copied from another run is
// refused before it is opened.
func (r Request) Open(sealingKey string, run Binding) (string, error) {
	if r.Binding != run {
		return "", fmt.Errorf("gateway: the request is for %q, run %q, attempt %q, and this job is in %s, run %s, attempt %s",
			r.Repository, r.Run, r.Attempt, run.Repository, run.Run, run.Attempt)
	}
	public, err := PublicKey(sealingKey)
	if err != nil {
		return "", err
	}
	if r.SealingKey != public {
		return "", fmt.Errorf("gateway: the request is sealed to another public key than the sealing key's public half, %s: set COSTROID_GATEWAY_PUBLIC_KEY to the public half costroid gateway keygen printed with COSTROID_GATEWAY_SEALING_KEY", public)
	}
	key, err := open(sealingKey, run, r.Sealed)
	if err != nil {
		return "", err
	}
	if !strings.HasPrefix(key, keyPrefix) || KeyID(key) != r.KeyID {
		return "", errors.New("gateway: the opened key does not match the request's key id")
	}
	return key, nil
}
