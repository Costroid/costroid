// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Costroid Authors

package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/Costroid/costroid/internal/focus"
	"github.com/Costroid/costroid/internal/ingest/anthropiccost"
	"github.com/Costroid/costroid/internal/ingest/awsfocus"
	"github.com/Costroid/costroid/internal/ingest/awsfocuss3"
	"github.com/Costroid/costroid/internal/ingest/azurefocus"
	"github.com/Costroid/costroid/internal/ingest/focuscsv"
	"github.com/Costroid/costroid/internal/ingest/gcpfocusbq"
	"github.com/Costroid/costroid/internal/ingest/openaicost"
	"github.com/Costroid/costroid/internal/storage"
)

// Connector source configurations mirror the connector-specific ingest flags.
// --period and --force remain one-shot CLI concerns and are passed separately
// to builders that use them.
type awsFocusSource struct {
	tenant string
	path   string
}

type awsFocusS3Source struct {
	tenant string
	bucket string
	prefix string
}

type azureFocusSource struct {
	tenant     string
	accountURL string
	container  string
	prefix     string
}

type gcpFocusBQSource struct {
	tenant         string
	datasetProject string
	dataset        string
	table          string
	location       string
	jobProject     string
	credential     string
	baseURL        string
	tokenURL       string
	since          string
	keyFile        string
}

type anthropicCostSource struct {
	tenant     string
	credential string
	baseURL    string
	since      string
	keyFile    string
}

type openAICostSource struct {
	tenant     string
	credential string
	baseURL    string
	since      string
	keyFile    string
}

type focusCSVSource struct {
	tenant       string
	path         string
	focusVersion string
	sourceLabel  string
	lenient      bool
}

var errInvalidSourceConfig = errors.New("invalid source configuration")

type sourceField string

const (
	sourceFieldPath           sourceField = "path"
	sourceFieldBucket         sourceField = "bucket"
	sourceFieldPrefix         sourceField = "prefix"
	sourceFieldAccountURL     sourceField = "accountURL"
	sourceFieldContainer      sourceField = "container"
	sourceFieldDatasetProject sourceField = "datasetProject"
	sourceFieldDataset        sourceField = "dataset"
	sourceFieldTable          sourceField = "table"
	sourceFieldLocation       sourceField = "location"
	sourceFieldFocusVersion   sourceField = "focusVersion"
)

// sourceValidationError preserves the connector and offending field identity
// independently of how a caller phrases the error for flags or JSON fields.
type sourceValidationError struct {
	connector string
	fields    []sourceField
}

func (e *sourceValidationError) Error() string { return errInvalidSourceConfig.Error() }
func (e *sourceValidationError) Unwrap() error { return errInvalidSourceConfig }

type sourceValidationPathError struct {
	message string
	cause   *sourceValidationError
}

func (e *sourceValidationPathError) Error() string { return e.message }
func (e *sourceValidationPathError) Unwrap() error { return e.cause }

func missingSourceFields(connector string, fields ...sourceField) error {
	if len(fields) == 0 {
		return nil
	}
	return &sourceValidationError{connector: connector, fields: fields}
}

func validateAWSFocusSource(cfg awsFocusSource) error {
	if cfg.path == "" {
		return missingSourceFields(awsfocus.Name, sourceFieldPath)
	}
	return nil
}

func validateAWSFocusS3Source(cfg awsFocusS3Source) error {
	var fields []sourceField
	if cfg.bucket == "" {
		fields = append(fields, sourceFieldBucket)
	}
	if cfg.prefix == "" {
		fields = append(fields, sourceFieldPrefix)
	}
	return missingSourceFields(awsfocuss3.Name, fields...)
}

func validateAzureFocusSource(cfg azureFocusSource) error {
	var fields []sourceField
	if cfg.accountURL == "" {
		fields = append(fields, sourceFieldAccountURL)
	}
	if cfg.container == "" {
		fields = append(fields, sourceFieldContainer)
	}
	if cfg.prefix == "" {
		fields = append(fields, sourceFieldPrefix)
	}
	return missingSourceFields(azurefocus.Name, fields...)
}

func validateGCPFocusBQSource(cfg gcpFocusBQSource) error {
	var fields []sourceField
	if cfg.datasetProject == "" {
		fields = append(fields, sourceFieldDatasetProject)
	}
	if cfg.dataset == "" {
		fields = append(fields, sourceFieldDataset)
	}
	if cfg.table == "" {
		fields = append(fields, sourceFieldTable)
	}
	if cfg.location == "" {
		fields = append(fields, sourceFieldLocation)
	}
	return missingSourceFields(gcpfocusbq.Name, fields...)
}

func validateAnthropicCostSource(anthropicCostSource) error { return nil }
func validateOpenAICostSource(openAICostSource) error       { return nil }

func validateFocusCSVSource(cfg focusCSVSource) error {
	if cfg.path == "" {
		return missingSourceFields(focuscsv.Name, sourceFieldPath)
	}
	return nil
}

// cliSourceValidation renders the pre-refactor flag-path text byte-for-byte
// while retaining the shared typed validation error in the unwrap chain.
func cliSourceValidation(err error) error {
	var validation *sourceValidationError
	if !errors.As(err, &validation) {
		return err
	}
	var message string
	switch validation.connector {
	case awsfocus.Name:
		message = "--path is required for the aws-focus connector"
	case awsfocuss3.Name:
		message = "--bucket and --prefix are required for the aws-focus-s3 connector"
	case azurefocus.Name:
		message = "--account-url, --container, and --prefix are required for the azure-focus connector"
	case gcpfocusbq.Name:
		message = "--dataset-project, --dataset, --table, and --location are required for the gcp-focus-bq connector"
	case focuscsv.Name:
		message = "--path is required for the focus-csv connector"
	default:
		return err
	}
	return &sourceValidationPathError{message: message, cause: validation}
}

type ingestOutput struct {
	stdout io.Writer
	stderr io.Writer
}

func (o ingestOutput) printf(format string, args ...any) {
	_, _ = fmt.Fprintf(o.stdout, format, args...)
}

func (o ingestOutput) errorf(format string, args ...any) {
	_, _ = fmt.Fprintf(o.stderr, format, args...)
}

func buildAWSFocusJobs(_ context.Context, cfg awsFocusSource, _ string, _ bool, _ ingestOutput) ([]ingestJob, error) {
	return []ingestJob{{conn: awsfocus.New(cfg.path)}}, nil
}

func buildAWSFocusS3Jobs(ctx context.Context, store *storage.DuckDB, cfg awsFocusS3Source, period string, force bool, _ ingestOutput) ([]ingestJob, error) {
	prior := map[string]awsfocuss3.ManifestState{}
	if !force {
		states, err := store.SyncStates(ctx, awsfocuss3.Name)
		if err != nil {
			return nil, err
		}
		for id, st := range states {
			if st.TenantID != cfg.tenant {
				continue
			}
			prior[id] = awsfocuss3.ManifestState{
				Key: st.ManifestKey, ETag: st.ManifestETag,
				LastModified: st.ManifestLastModified, Size: st.ManifestSize,
			}
		}
	}
	periods, err := awsfocuss3.Discover(ctx, cfg.bucket, cfg.prefix, prior)
	if err != nil {
		return nil, err
	}
	return s3Jobs(periods, period)
}

func buildAzureFocusJobs(ctx context.Context, store *storage.DuckDB, cfg azureFocusSource, period string, force bool, _ ingestOutput) ([]ingestJob, error) {
	prior := map[string]azurefocus.ManifestState{}
	if !force {
		states, err := store.SyncStates(ctx, azurefocus.Name)
		if err != nil {
			return nil, err
		}
		for id, st := range states {
			if st.TenantID != cfg.tenant {
				continue
			}
			prior[id] = azurefocus.ManifestState{
				Key: st.ManifestKey, ETag: st.ManifestETag,
				LastModified: st.ManifestLastModified, Size: st.ManifestSize,
			}
		}
	}
	periods, err := azurefocus.Discover(ctx, cfg.accountURL, cfg.container, cfg.prefix, prior, store)
	if err != nil {
		return nil, err
	}
	return azureJobs(periods, period)
}

func buildGCPFocusBQJobs(ctx context.Context, store *storage.DuckDB, cfg gcpFocusBQSource, period string, force bool, output ingestOutput) ([]ingestJob, error) {
	baseURL := firstNonEmpty(cfg.baseURL, gcpfocusbq.DefaultBaseURL)
	tokenURL := firstNonEmpty(cfg.tokenURL, gcpfocusbq.DefaultTokenURL)
	credentialJSON, err := gcpServiceAccountJSON(ctx, store, cfg.keyFile, cfg.credential)
	if err != nil {
		return nil, err
	}
	client, err := gcpfocusbq.NewClient(aiHTTPClient(), baseURL, tokenURL, credentialJSON)
	if err != nil {
		return nil, err
	}
	coords := gcpfocusbq.Coordinates{
		DatasetProject: cfg.datasetProject, Dataset: cfg.dataset, Table: cfg.table,
		Location: cfg.location, JobProject: cfg.jobProject, Since: cfg.since,
	}
	prior := map[string]gcpfocusbq.ChangeState{}
	if !force {
		states, err := store.SyncStates(ctx, gcpfocusbq.Name)
		if err != nil {
			return nil, err
		}
		for id, st := range states {
			if st.TenantID != cfg.tenant {
				continue
			}
			prior[id] = gcpfocusbq.ChangeState{
				Key: st.ManifestKey, Token: st.ManifestETag,
				LastModified: st.ManifestLastModified, Size: st.ManifestSize,
			}
		}
	}
	periods, err := gcpfocusbq.Discover(ctx, client, coords, prior)
	if err != nil {
		return nil, err
	}
	probe := client.ProbeResult()
	partitioning := "absent"
	if probe.TimePartitioning {
		partitioning = "present"
	}
	output.printf("gcp-focus-bq table probe: timePartitioning=%s\n", partitioning)
	if len(probe.AdditiveColumns) > 0 {
		output.errorf("costroid: gcp-focus-bq Preview schema added column(s) not selected by this connector: %s\n", strings.Join(probe.AdditiveColumns, ", "))
	}
	return gcpJobs(periods, period)
}

func buildAnthropicCostJobs(ctx context.Context, store *storage.DuckDB, cfg anthropicCostSource, period string, _ bool, output ingestOutput) ([]ingestJob, error) {
	slot := firstNonEmpty(cfg.credential, anthropiccost.Name)
	baseURL := firstNonEmpty(cfg.baseURL, anthropiccost.DefaultBaseURL)
	secret, err := vaultSecret(ctx, store, cfg.keyFile, slot)
	if err != nil {
		return nil, err
	}
	periods, err := anthropiccost.Discover(ctx, aiHTTPClient(), baseURL, slot, secret, cfg.since, period)
	if err != nil {
		return nil, err
	}
	jobs := make([]ingestJob, 0, len(periods))
	for _, p := range periods {
		job := aiJob(p.Month, p.Conn, p.Err)
		if p.Conn != nil {
			if summary := p.Conn.AnomalySummary(); summary != "" {
				output.printf("period %s: %s\n", p.Month, summary)
			}
			job.usageMetrics = p.Conn.UsageMetrics()
		}
		jobs = append(jobs, job)
	}
	return jobs, nil
}

func buildOpenAICostJobs(ctx context.Context, store *storage.DuckDB, cfg openAICostSource, period string, _ bool, output ingestOutput) ([]ingestJob, error) {
	slot := firstNonEmpty(cfg.credential, openaicost.Name)
	baseURL := firstNonEmpty(cfg.baseURL, openaicost.DefaultBaseURL)
	secret, err := vaultSecret(ctx, store, cfg.keyFile, slot)
	if err != nil {
		return nil, err
	}
	periods, err := openaicost.Discover(ctx, aiHTTPClient(), baseURL, slot, secret, cfg.since, period)
	if err != nil {
		return nil, err
	}
	jobs := make([]ingestJob, 0, len(periods))
	for _, p := range periods {
		job := aiJob(p.Month, p.Conn, p.Err)
		if p.Conn != nil {
			if summary := p.Conn.AnomalySummary(); summary != "" {
				output.printf("period %s: %s\n", p.Month, summary)
			}
			job.usageMetrics = p.Conn.UsageMetrics()
		}
		jobs = append(jobs, job)
	}
	return jobs, nil
}

func buildFocusCSVJobs(_ context.Context, cfg focusCSVSource, period string, _ bool, output ingestOutput) ([]ingestJob, error) {
	periods, warnings, err := focuscsv.Discover(cfg.path, focus.Version(cfg.focusVersion), cfg.sourceLabel, cfg.lenient)
	if err != nil {
		return nil, err
	}
	for _, warning := range warnings {
		output.errorf("costroid: %s\n", warning)
	}
	return focusCSVJobs(periods, period)
}
