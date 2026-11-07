package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/Costroid/costroid/internal/decimal"
)

// ledgerScale is the most fractional digits the ledger keeps of a figure.
const ledgerScale = 12

// keyIDPattern is a key's id as LiteLLM stores it: the SHA-256 of the key in
// hex.
var keyIDPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)

// Count is what the gateway counted on a run's key: the key's id and its
// spend, in USD at the gateway's own prices.
type Count struct {
	KeyID string
	Spend decimal.Decimal
}

// Count reads the key with the alias, as the gateway's admin. It answers false
// when no key has the alias. LiteLLM keeps aliases unique, so a second key
// with the alias is an error.
func (c *Client) Count(ctx context.Context, adminKey, alias string) (Count, bool, error) {
	route := "/key/list?return_full_object=true&key_alias=" + url.QueryEscape(alias)
	var answer struct {
		Keys []struct {
			Token string      `json:"token"`
			Alias string      `json:"key_alias"`
			Spend json.Number `json:"spend"`
		} `json:"keys"`
	}
	if err := c.call(ctx, http.MethodGet, route, adminKey, nil, &answer); err != nil {
		return Count{}, false, err
	}
	var found []Count
	for _, key := range answer.Keys {
		if key.Alias != alias {
			continue
		}
		if !keyIDPattern.MatchString(key.Token) {
			return Count{}, false, errors.New("gateway: the key the gateway listed for the run has no SHA-256 id")
		}
		spend, err := spendOf(key.Spend)
		if err != nil {
			return Count{}, false, err
		}
		found = append(found, Count{KeyID: key.Token, Spend: spend})
	}
	switch len(found) {
	case 0:
		return Count{}, false, nil
	case 1:
		return found[0], true, nil
	}
	return Count{}, false, fmt.Errorf("gateway: the gateway lists %d keys with the run's alias", len(found))
}

// spendOf reads a key's spend field exactly, as the number LiteLLM wrote.
func spendOf(field json.Number) (decimal.Decimal, error) {
	spend, err := decimal.ParseNumber(field.String())
	if err != nil || spend.Sign() < 0 {
		return decimal.Decimal{}, fmt.Errorf("gateway: the key's spend field reads %.40q, which is not a number of zero or more", field.String())
	}
	return spend, nil
}

// Counter reads what the gateway counted on a run's key once the figure has
// settled. LiteLLM writes spend to its store in batches, so a key read once
// can still lack its last request.
type Counter struct {
	Client   *Client
	AdminKey string
	Run      Binding
	// Poll is how often the key is read. Stable is how long an unchanged
	// figure counts as settled, and Settle bounds the whole wait.
	Poll, Stable, Settle time.Duration
}

// Settled reads the run's key until its spend has not moved for Stable, or
// until Settle has passed, and answers the last figure read. It answers false
// when no key has the run's alias.
func (c Counter) Settled(ctx context.Context) (Count, bool, error) {
	deadline := time.Now().Add(c.Settle)
	count, found, err := c.Client.Count(ctx, c.AdminKey, c.Run.Alias())
	if err != nil || !found {
		return Count{}, found, err
	}
	since := time.Now()
	for time.Since(since) < c.Stable && time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			return Count{}, false, ctx.Err()
		case <-time.After(c.Poll):
		}
		next, found, err := c.Client.Count(ctx, c.AdminKey, c.Run.Alias())
		if err != nil || !found {
			return Count{}, found, err
		}
		if next.KeyID != count.KeyID || next.Spend.Cmp(count.Spend) != 0 {
			count, since = next, time.Now()
		}
	}
	return count, true, nil
}

// SpendAttestation tells the ledger what the gateway counted on a key minted
// for one run. The key's id is in the URL.
type SpendAttestation struct {
	Repository string `json:"repository"`
	RunID      string `json:"run_id"`
	RunAttempt string `json:"run_attempt"`
	Currency   string `json:"currency"`
	Spend      string `json:"spend"`
}

// SpendFor states a count for the run. The ledger keeps 12 fractional digits,
// so a figure with more is rounded up, and the ledger's figure is never below
// the gateway's.
func SpendFor(count Count, run Binding) SpendAttestation {
	return SpendAttestation{
		Repository: run.Repository, RunID: run.Run, RunAttempt: run.Attempt,
		Currency: "USD", Spend: roundUp(count.Spend, ledgerScale).String(),
	}
}

// roundUp rounds a figure of zero or more up to scale fractional digits.
func roundUp(figure decimal.Decimal, scale int) decimal.Decimal {
	if figure.Scale() <= scale {
		return figure
	}
	whole, fraction, _ := strings.Cut(figure.String(), ".")
	kept := decimal.MustParse(whole + "." + fraction[:scale])
	return kept.Add(decimal.MustParse("1").DivPow10(scale))
}

// SpendURL is where the ledger behind an ingest URL takes a key's spend.
func SpendURL(ingest, keyID string) (string, error) {
	credentials, err := CredentialsURL(ingest)
	if err != nil {
		return "", err
	}
	if !keyIDPattern.MatchString(keyID) {
		return "", errors.New("gateway: a key's id is 64 hexadecimal characters")
	}
	return credentials + "/" + keyID + "/spend", nil
}

// AttestSpend sends a key's spend to the ledger under the job's OIDC token.
func AttestSpend(ctx context.Context, client *http.Client, spendURL, token string, attestation SpendAttestation) error {
	return post(ctx, client, spendURL, token, attestation, "the ledger's spend attestation")
}
