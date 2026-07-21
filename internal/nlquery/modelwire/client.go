// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Costroid Authors

// Package modelwire posts translation prompts to an operator-configured model
// endpoint and returns the reply text without logging request or response data.
package modelwire

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
)

const responseBodyLimit = 1 << 20

// Client sends prompts using the widely supported chat-completions wire shape.
type Client struct {
	endpoint   string
	model      string
	credential string
	httpClient *http.Client
}

// New returns a model client. The caller owns configuration validation.
//
// Redirects are never followed. The operator chooses exactly one endpoint, and
// following a redirect would replay the prompt to a host they never chose, so
// the redirect response is returned as-is and rejected by the status check
// below. The copy keeps that guarantee even when the caller supplies its own
// client, without mutating the caller's value.
func New(endpoint, model, credential string, httpClient *http.Client) *Client {
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	noRedirect := *httpClient
	noRedirect.CheckRedirect = func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}
	return &Client{endpoint: endpoint, model: model, credential: credential, httpClient: &noRedirect}
}

type requestBody struct {
	Model    string    `json:"model"`
	Messages []message `json:"messages"`
}

type message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type responseBody struct {
	Choices []struct {
		Message message `json:"message"`
	} `json:"choices"`
}

// Complete posts prompt and returns the first reply. Errors identify only the
// failure class and never include endpoint paths, credentials, or body content.
func (c *Client) Complete(ctx context.Context, prompt []byte) ([]byte, error) {
	body, err := json.Marshal(requestBody{Model: c.model, Messages: []message{{Role: "user", Content: string(prompt)}}})
	if err != nil {
		return nil, errors.New("encoding model request failed")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, errors.New("building model request failed")
	}
	req.Header.Set("Content-Type", "application/json")
	if c.credential != "" {
		req.Header.Set("Authorization", "Bearer "+c.credential)
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, redactTransportError(err)
	}
	defer func() { _ = resp.Body.Close() }()
	limited, err := io.ReadAll(io.LimitReader(resp.Body, responseBodyLimit+1))
	if err != nil {
		return nil, errors.New("reading model reply failed")
	}
	if len(limited) > responseBodyLimit {
		return nil, errors.New("model reply exceeded the 1 MiB limit")
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("model endpoint returned HTTP %d", resp.StatusCode)
	}
	var decoded responseBody
	if err := json.Unmarshal(limited, &decoded); err != nil || len(decoded.Choices) != 1 || decoded.Choices[0].Message.Content == "" {
		return nil, errors.New("model endpoint returned an invalid reply envelope")
	}
	return []byte(decoded.Choices[0].Message.Content), nil
}

func redactTransportError(err error) error {
	var uerr *url.Error
	if errors.As(err, &uerr) {
		return fmt.Errorf("model %s request failed: %v", uerr.Op, uerr.Err)
	}
	return errors.New("model request failed")
}
