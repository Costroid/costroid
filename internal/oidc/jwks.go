package oidc

import (
	"context"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"sync"
	"time"
)

const (
	maxJWKSBytes    = 1 << 20
	minRSABits      = 2048
	keySetLifetime  = time.Hour
	refetchInterval = time.Minute
)

// issuerKeys caches one issuer's key set. An unknown key id refetches, at most
// once per refetchInterval, so a stream of forged ids cannot hammer the issuer.
type issuerKeys struct {
	issuer Issuer
	client *http.Client
	now    func() time.Time

	mu      sync.Mutex
	keys    map[string]*rsa.PublicKey
	fetched time.Time
}

func (i *issuerKeys) key(ctx context.Context, id string) (*rsa.PublicKey, error) {
	i.mu.Lock()
	defer i.mu.Unlock()
	key, known := i.keys[id]
	age := i.now().Sub(i.fetched)
	if known && age < keySetLifetime {
		return key, nil
	}
	if !i.fetched.IsZero() && age < refetchInterval {
		return nil, fmt.Errorf("%w: kid %q", ErrKeyUnavailable, id)
	}
	fetched, err := i.fetch(ctx)
	if err != nil {
		if known {
			return key, nil
		}
		return nil, err
	}
	i.keys, i.fetched = fetched, i.now()
	key, known = i.keys[id]
	if !known {
		return nil, fmt.Errorf("%w: kid %q", ErrKeyUnavailable, id)
	}
	return key, nil
}

type keySet struct {
	Keys []struct {
		Type     string `json:"kty"`
		ID       string `json:"kid"`
		Modulus  string `json:"n"`
		Exponent string `json:"e"`
	} `json:"keys"`
}

func (i *issuerKeys) fetch(ctx context.Context) (map[string]*rsa.PublicKey, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, i.issuer.JWKSURL, nil)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrKeyUnavailable, err)
	}
	response, err := i.client.Do(request)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrKeyUnavailable, err)
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%w: status %d", ErrKeyUnavailable, response.StatusCode)
	}
	var set keySet
	if err := json.NewDecoder(io.LimitReader(response.Body, maxJWKSBytes)).Decode(&set); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrKeyUnavailable, err)
	}
	keys := make(map[string]*rsa.PublicKey, len(set.Keys))
	for _, entry := range set.Keys {
		if entry.Type != "RSA" || entry.ID == "" {
			continue
		}
		key, err := rsaKey(entry.Modulus, entry.Exponent)
		if err != nil {
			return nil, fmt.Errorf("%w: kid %q: %w", ErrKeyUnavailable, entry.ID, err)
		}
		keys[entry.ID] = key
	}
	return keys, nil
}

func rsaKey(modulus, exponent string) (*rsa.PublicKey, error) {
	n, err := base64.RawURLEncoding.DecodeString(modulus)
	if err != nil {
		return nil, fmt.Errorf("modulus: %w", err)
	}
	e, err := base64.RawURLEncoding.DecodeString(exponent)
	if err != nil {
		return nil, fmt.Errorf("exponent: %w", err)
	}
	key := &rsa.PublicKey{N: new(big.Int).SetBytes(n)}
	if key.N.BitLen() < minRSABits {
		return nil, fmt.Errorf("modulus of %d bits", key.N.BitLen())
	}
	value := new(big.Int).SetBytes(e)
	if !value.IsInt64() || value.Int64() < 3 || value.Int64() > 1<<31-1 {
		return nil, fmt.Errorf("exponent %s", value)
	}
	key.E = int(value.Int64())
	return key, nil
}
