package mcpserver

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"time"
)

const (
	// maxView bounds a ledger's answer. A pull request's view is one line per
	// account, agent, model, token class and grade.
	maxView = 8 << 20
	// ledgerTimeout bounds one read of the ledger.
	ledgerTimeout = 30 * time.Second
)

// amountKeys name every field that holds an amount in a ledger's answer.
var amountKeys = []string{"list_cost", "effective_cost", "gateway_usd"}

// Ledger is a costroid serve the person named when they started the server.
// The agent names a pull request, never the host, so the token goes nowhere
// else.
type Ledger struct {
	root *url.URL
	// token is the bearer: an ingest key or a CI job's OIDC token. It never
	// enters an answer.
	token string
	// repository is host/owner/name, the repository a request names when it
	// names none.
	repository string
	client     *http.Client
}

// NewLedger checks the ledger's URL. It must be https, or http on a loopback
// address, because the token travels with every request.
func NewLedger(root, token, repository string) (*Ledger, error) {
	parsed, err := url.Parse(root)
	if err != nil {
		return nil, fmt.Errorf("the ledger's URL: %w", err)
	}
	switch {
	case parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "":
		return nil, errors.New("the ledger's URL must be a scheme, a host and an optional path, with no user, query or fragment")
	case parsed.Scheme == "https":
	case parsed.Scheme == "http" && loopback(parsed.Hostname()):
	default:
		return nil, errors.New("the ledger's URL must be https, or http on a loopback address, because the token travels with every request")
	}
	client := &http.Client{
		Timeout: ledgerTimeout,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	return &Ledger{root: parsed, token: token, repository: repository, client: client}, nil
}

func loopback(host string) bool {
	if host == "localhost" {
		return true
	}
	address := net.ParseIP(host)
	return address != nil && address.IsLoopback()
}

// Pull reads GET /v1/pulls/{number}?repo=repository. An empty repository is
// the ledger's own. The answer is returned as the ledger wrote it, once every
// amount in it is a string.
func (l *Ledger) Pull(ctx context.Context, number int, repository string) (json.RawMessage, error) {
	repository = cmp.Or(repository, l.repository)
	if repository == "" {
		return nil, errors.New("name the repository as host/owner/name: the server was started without one")
	}
	endpoint := l.root.JoinPath("v1", "pulls", strconv.Itoa(number))
	endpoint.RawQuery = url.Values{"repo": {repository}}.Encode()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint.String(), nil)
	if err != nil {
		return nil, err
	}
	if l.token != "" {
		request.Header.Set("Authorization", "Bearer "+l.token)
	}
	response, err := l.client.Do(request)
	if err != nil {
		return nil, fmt.Errorf("GET /v1/pulls/%d: %w", number, err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, maxView+1))
	if err != nil {
		return nil, fmt.Errorf("GET /v1/pulls/%d: %w", number, err)
	}
	if len(body) > maxView {
		return nil, fmt.Errorf("GET /v1/pulls/%d: the answer is longer than %d bytes", number, maxView)
	}
	if response.StatusCode != http.StatusOK {
		return nil, refusal(number, response.StatusCode, body)
	}
	if err := stringAmounts(body); err != nil {
		return nil, fmt.Errorf("GET /v1/pulls/%d: %w", number, err)
	}
	return json.RawMessage(body), nil
}

// refusal reads the ledger's typed refusal. Its code is the contract.
func refusal(number, statusCode int, body []byte) error {
	var answer struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if json.Unmarshal(body, &answer) != nil || answer.Error.Code == "" {
		return fmt.Errorf("GET /v1/pulls/%d answered %d", number, statusCode)
	}
	return fmt.Errorf("GET /v1/pulls/%d answered %d %s: %s", number, statusCode, answer.Error.Code, answer.Error.Message)
}

// stringAmounts refuses an answer that is not a JSON object, or that holds an
// amount as anything but a string. A JSON number is read as a float by most
// clients, and a float changes the amount.
func stringAmounts(body []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.UseNumber()
	var view map[string]any
	if err := decoder.Decode(&view); err != nil {
		return fmt.Errorf("the answer is not a JSON object: %w", err)
	}
	return walkAmounts(view)
}

func walkAmounts(value any) error {
	switch typed := value.(type) {
	case map[string]any:
		for key, field := range typed {
			if _, isString := field.(string); slices.Contains(amountKeys, key) && !isString {
				return fmt.Errorf("the answer holds %s as something other than a decimal string", key)
			}
			if err := walkAmounts(field); err != nil {
				return err
			}
		}
	case []any:
		for _, item := range typed {
			if err := walkAmounts(item); err != nil {
				return err
			}
		}
	}
	return nil
}
