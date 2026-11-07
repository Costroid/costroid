package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

// rowsPath ends the ingest URL a recipe names. The ledger takes attestations
// beside it.
const (
	rowsPath        = "/v1/rows"
	credentialsPath = "/v1/credentials"
)

// Attestation tells the ledger that a key was minted for one run. KeyID is
// the value the run's rows carry in SubAccountId. It holds no secret.
type Attestation struct {
	KeyID      string `json:"key_id"`
	Repository string `json:"repository"`
	RunID      string `json:"run_id"`
	RunAttempt string `json:"run_attempt"`
}

// AttestationFor names the run a binding is for.
func AttestationFor(keyID string, run Binding) Attestation {
	return Attestation{KeyID: keyID, Repository: run.Repository, RunID: run.Run, RunAttempt: run.Attempt}
}

// CredentialsURL is where the ledger behind an ingest URL takes attestations.
// The URL must be https, because the request carries the job's OIDC token.
func CredentialsURL(ingest string) (string, error) {
	parsed, err := url.Parse(ingest)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" || !strings.HasSuffix(parsed.Path, rowsPath) {
		return "", fmt.Errorf("gateway: the ingest URL must be an https URL ending in %s", rowsPath)
	}
	parsed.Path = strings.TrimSuffix(parsed.Path, rowsPath) + credentialsPath
	parsed.RawQuery = ""
	return parsed.String(), nil
}

// ActionsToken asks GitHub Actions for the job's OIDC token, as the ingest job
// does. It needs id-token: write, which sets the two variables it reads.
func ActionsToken(ctx context.Context, client *http.Client, getenv func(string) string, audience string) (string, error) {
	requestURL, requestToken := getenv("ACTIONS_ID_TOKEN_REQUEST_URL"), getenv("ACTIONS_ID_TOKEN_REQUEST_TOKEN")
	if requestURL == "" || requestToken == "" {
		return "", errors.New("gateway: this job has no id-token permission, so it cannot attest the key")
	}
	address, err := url.Parse(requestURL)
	if err != nil {
		return "", fmt.Errorf("gateway: ACTIONS_ID_TOKEN_REQUEST_URL is not a URL: %w", err)
	}
	if audience != "" {
		query := address.Query()
		query.Set("audience", audience)
		address.RawQuery = query.Encode()
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, address.String(), nil)
	if err != nil {
		return "", fmt.Errorf("gateway: %w", err)
	}
	request.Header.Set("Authorization", "Bearer "+requestToken)
	var answer struct {
		Value string `json:"value"`
	}
	if err := send(client, request, "GitHub's OIDC token", &answer); err != nil {
		return "", err
	}
	if answer.Value == "" {
		return "", errors.New("gateway: GitHub issued no OIDC token")
	}
	return answer.Value, nil
}

// Attest sends an attestation to the ledger under the job's OIDC token.
func Attest(ctx context.Context, client *http.Client, credentialsURL, token string, attestation Attestation) error {
	return post(ctx, client, credentialsURL, token, attestation, "the ledger's attestation")
}

// post sends a body to the ledger under the job's OIDC token.
func post(ctx context.Context, client *http.Client, target, token string, body any, what string) error {
	data, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("gateway: encode %s: %w", what, err)
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, target, bytes.NewReader(data))
	if err != nil {
		return fmt.Errorf("gateway: %w", err)
	}
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("Content-Type", "application/json")
	return send(client, request, what, nil)
}

// send names what it asked for and the status, and never the URL, which can
// carry a request token's query.
func send(client *http.Client, request *http.Request, what string, answer any) error {
	response, err := client.Do(request)
	if err != nil {
		var failed *url.Error
		if errors.As(err, &failed) {
			err = failed.Err
		}
		return fmt.Errorf("gateway: ask for %s: %w", what, err)
	}
	defer response.Body.Close()
	data, err := io.ReadAll(io.LimitReader(response.Body, maxAnswerBytes))
	if err != nil {
		return fmt.Errorf("gateway: read %s: %w", what, err)
	}
	if response.StatusCode/100 != 2 {
		return fmt.Errorf("gateway: %s answered %d: %s", what, response.StatusCode, refusalCode(data))
	}
	if answer == nil {
		return nil
	}
	if err := json.Unmarshal(data, answer); err != nil {
		return fmt.Errorf("gateway: parse %s: %w", what, err)
	}
	return nil
}

// refusalCode reads the ledger's typed refusal code, and nothing else of the
// body.
func refusalCode(data []byte) string {
	var answer struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if json.Unmarshal(data, &answer) != nil || answer.Error.Code == "" {
		return "no code"
	}
	return answer.Error.Code
}
