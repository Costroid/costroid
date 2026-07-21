// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Costroid Authors

package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"testing/fstest"
	"time"

	"github.com/Costroid/costroid/internal/api"
	"github.com/Costroid/costroid/internal/focus"
	"github.com/Costroid/costroid/internal/nlquery"
	"github.com/Costroid/costroid/internal/nlquery/modelwire"
)

const (
	envModelEndpoint       = "COSTROID_MODEL_ENDPOINT"
	envModelName           = "COSTROID_MODEL"
	envModelCredentialFile = "COSTROID_MODEL_API_KEY_FILE"
)

const askUsage = `usage: costroid ask <question>

Translate one finance question into a visible, validated plan, then execute it
against the local store through the same API handler used by serve and export.

This feature is off unless COSTROID_MODEL_ENDPOINT and COSTROID_MODEL are set.
COSTROID_MODEL_API_KEY_FILE optionally names a file containing the endpoint
credential. There is no credential value flag. The question, reply, plan, and
credential are never logged.`

type modelSettings struct {
	endpoint   string
	model      string
	credential string
}

func (s modelSettings) configured() bool { return s.endpoint != "" }

func resolveModelSettings() (modelSettings, error) {
	endpoint := os.Getenv(envModelEndpoint)
	model := os.Getenv(envModelName)
	credentialFile := os.Getenv(envModelCredentialFile)
	if endpoint == "" && model == "" && credentialFile == "" {
		return modelSettings{}, nil
	}
	if endpoint == "" {
		return modelSettings{}, errors.New("model configuration requires COSTROID_MODEL_ENDPOINT")
	}
	if model == "" {
		return modelSettings{}, errors.New("model configuration requires COSTROID_MODEL")
	}
	parsed, err := url.ParseRequestURI(endpoint)
	if err != nil || parsed.Host == "" || parsed.User != nil || parsed.Scheme != "http" && parsed.Scheme != "https" {
		return modelSettings{}, errors.New("COSTROID_MODEL_ENDPOINT must be an absolute http or https URL without user information")
	}
	credential := ""
	if credentialFile != "" {
		data, err := os.ReadFile(credentialFile)
		if err != nil {
			return modelSettings{}, fmt.Errorf("reading model credential file %s: %w", credentialFile, err)
		}
		credential = trimOneTrailingNewline(string(data))
		if credential == "" {
			return modelSettings{}, errors.New("model credential file is empty")
		}
	}
	return modelSettings{endpoint: endpoint, model: model, credential: credential}, nil
}

type askStore interface {
	api.CostStore
	Close() error
}

type askDependencies struct {
	out        io.Writer
	logger     *slog.Logger
	httpClient *http.Client
	openStore  func(context.Context) (askStore, error)
}

func defaultAskDependencies() askDependencies {
	return askDependencies{
		out:        os.Stdout,
		logger:     slog.New(slog.NewJSONHandler(os.Stderr, nil)),
		httpClient: &http.Client{Timeout: 30 * time.Second},
		openStore: func(ctx context.Context) (askStore, error) {
			return openStore(ctx, "")
		},
	}
}

func askCmd(args []string) error {
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()
	return askCommand(ctx, args, defaultAskDependencies())
}

func askCommand(ctx context.Context, args []string, deps askDependencies) error {
	if len(args) != 1 || strings.TrimSpace(args[0]) == "" {
		return fmt.Errorf("ask requires exactly one non-empty question; %s", askUsage)
	}
	question := args[0]
	if len(question) > focus.MaxFreeTextBytes {
		// MaxFreeTextBytes is an ingest safety guard; reuse here is deliberate
		// as the outbound question bound, and questions are rejected, not truncated.
		return fmt.Errorf("question must be at most %d bytes", focus.MaxFreeTextBytes)
	}
	settings, err := resolveModelSettings()
	if err != nil {
		deps.logger.Error("natural-language query configuration failed")
		return err
	}
	if !settings.configured() {
		deps.logger.Error("natural-language query is not configured")
		return errors.New("natural-language queries are off; set COSTROID_MODEL_ENDPOINT and COSTROID_MODEL to configure them")
	}
	store, err := deps.openStore(ctx)
	if err != nil {
		deps.logger.Error("natural-language query store open failed")
		return err
	}
	defer func() { _ = store.Close() }()

	values, metrics, err := discoverPromptValues(ctx, store)
	if err != nil {
		deps.logger.Error("natural-language query metadata discovery failed")
		return err
	}
	prompt, err := nlquery.BuildPrompt(question, values)
	if err != nil {
		deps.logger.Error("natural-language query prompt encoding failed")
		return errors.New("encoding translation prompt failed")
	}
	client := modelwire.New(settings.endpoint, settings.model, settings.credential, deps.httpClient)
	reply, err := client.Complete(ctx, prompt)
	if err != nil {
		deps.logger.Error("natural-language query model request failed")
		return err
	}
	plan, err := nlquery.ParseReply(reply)
	if err != nil {
		deps.logger.Error("natural-language query reply parsing failed")
		return err
	}
	if err := api.ValidatePlan(plan, metrics); err != nil {
		deps.logger.Error("natural-language query plan validation failed")
		return errors.New("the model reply contained an invalid plan")
	}

	planJSON, err := json.Marshal(plan)
	if err != nil {
		return errors.New("encoding resolved plan failed")
	}
	if _, err := fmt.Fprintf(deps.out, "Resolved plan:\n%s\nResult:\n", planJSON); err != nil {
		return err
	}
	handler := api.NewHandler(version, fstest.MapFS{}, store, resolveAllocationRulesPath(""))
	result, err := executePlan(ctx, handler, plan)
	if err != nil {
		deps.logger.Error("natural-language query execution failed")
		return err
	}
	if _, err := deps.out.Write(result); err != nil {
		return err
	}
	deps.logger.Info("natural-language query completed")
	return nil
}

func discoverPromptValues(ctx context.Context, store api.CostStore) (nlquery.Values, []string, error) {
	providers, err := store.Providers(ctx, focus.DefaultTenant, time.Time{}, time.Time{})
	if err != nil {
		return nlquery.Values{}, nil, errors.New("querying provider names failed")
	}
	tagKeys, err := store.TagKeys(ctx, focus.DefaultTenant, time.Time{}, time.Time{})
	if err != nil {
		return nlquery.Values{}, nil, errors.New("querying tag keys failed")
	}
	currencies, err := store.BillingCurrencies(ctx, focus.DefaultTenant, time.Time{}, time.Time{}, "")
	if err != nil {
		return nlquery.Values{}, nil, errors.New("querying currency codes failed")
	}
	infos, err := store.BusinessMetricNames(ctx, focus.DefaultTenant)
	if err != nil {
		return nlquery.Values{}, nil, errors.New("querying business metric names failed")
	}
	metrics := make([]string, 0, len(infos))
	for _, info := range infos {
		metrics = append(metrics, info.Name)
	}
	return nlquery.Values{Providers: providers, TagKeys: tagKeys, Currencies: currencies, Metrics: metrics}, metrics, nil
}

func executePlan(ctx context.Context, handler http.Handler, plan nlquery.Plan) ([]byte, error) {
	spec, ok := nlquery.Endpoints[plan.Endpoint]
	if !ok {
		return nil, errors.New("executing plan with unknown endpoint")
	}
	requestURL := spec.Path
	if query := plan.Query(); query != "" {
		requestURL += "?" + query
	}
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequestWithContext(ctx, http.MethodGet, requestURL, nil))
	if recorder.Code != http.StatusOK {
		return nil, fmt.Errorf("plan endpoint returned HTTP %d", recorder.Code)
	}
	return append([]byte{}, recorder.Body.Bytes()...), nil
}
