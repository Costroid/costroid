// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Costroid Authors

package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/Costroid/costroid/internal/focus"
	"github.com/Costroid/costroid/internal/ingest/anthropiccost"
	"github.com/Costroid/costroid/internal/ingest/awsfocus"
	"github.com/Costroid/costroid/internal/ingest/awsfocuss3"
	"github.com/Costroid/costroid/internal/ingest/azurefocus"
	"github.com/Costroid/costroid/internal/ingest/focuscsv"
	"github.com/Costroid/costroid/internal/ingest/gcpfocusbq"
	"github.com/Costroid/costroid/internal/ingest/openaicost"
)

const (
	sourcesEnvVar         = "COSTROID_SOURCES"
	defaultSourceInterval = 24 * time.Hour
	minimumSourceInterval = 15 * time.Minute
)

var sourceNamePattern = regexp.MustCompile(`^[a-z0-9-]+$`)

type sourcesConfig struct {
	defaultIntervalText string
	sources             []scheduledSource
}

type scheduledSource struct {
	name         string
	connector    string
	tenant       string
	interval     time.Duration
	intervalText string
	config       any
}

type sourcesDocument struct {
	DefaultInterval string             `json:"defaultInterval"`
	Sources         *[]json.RawMessage `json:"sources"`
}

type sourceCommonJSON struct {
	Name      string `json:"name"`
	Connector string `json:"connector"`
	Tenant    string `json:"tenant"`
	Interval  string `json:"interval"`
}

type awsFocusJSON struct {
	sourceCommonJSON
	Path string `json:"path"`
}

type awsFocusS3JSON struct {
	sourceCommonJSON
	Bucket string `json:"bucket"`
	Prefix string `json:"prefix"`
}

type azureFocusJSON struct {
	sourceCommonJSON
	AccountURL string `json:"accountURL"`
	Container  string `json:"container"`
	Prefix     string `json:"prefix"`
}

type gcpFocusBQJSON struct {
	sourceCommonJSON
	DatasetProject string `json:"datasetProject"`
	Dataset        string `json:"dataset"`
	Table          string `json:"table"`
	Location       string `json:"location"`
	JobProject     string `json:"jobProject"`
	Credential     string `json:"credential"`
	BaseURL        string `json:"baseURL"`
	TokenURL       string `json:"tokenURL"`
	Since          string `json:"since"`
	KeyFile        string `json:"keyFile"`
}

type anthropicCostJSON struct {
	sourceCommonJSON
	Credential string `json:"credential"`
	BaseURL    string `json:"baseURL"`
	Since      string `json:"since"`
	KeyFile    string `json:"keyFile"`
}

type openAICostJSON struct {
	sourceCommonJSON
	Credential string `json:"credential"`
	BaseURL    string `json:"baseURL"`
	Since      string `json:"since"`
	KeyFile    string `json:"keyFile"`
}

type focusCSVJSON struct {
	sourceCommonJSON
	Path         string `json:"path"`
	FocusVersion string `json:"focusVersion"`
	SourceLabel  string `json:"sourceLabel"`
	Lenient      bool   `json:"lenient"`
}

func resolveSourcesPath(flagValue string) string {
	if flagValue != "" {
		return flagValue
	}
	if env := os.Getenv(sourcesEnvVar); env != "" {
		return env
	}
	dir, err := os.UserConfigDir()
	if err != nil {
		return ""
	}
	return filepath.Join(dir, "costroid", "sources.json")
}

func loadSourcesConfig(path string) (sourcesConfig, error) {
	if path == "" {
		return sourcesConfig{}, errors.New("no sources path (pass --sources or set $COSTROID_SOURCES)")
	}
	f, err := os.Open(path)
	if err != nil {
		return sourcesConfig{}, err
	}
	defer func() { _ = f.Close() }()
	cfg, err := parseSources(f)
	if err != nil {
		return sourcesConfig{}, fmt.Errorf("parsing sources file %s: %w", path, err)
	}
	return cfg, nil
}

func parseSources(r io.Reader) (sourcesConfig, error) {
	dec := json.NewDecoder(r)
	dec.DisallowUnknownFields()
	var document sourcesDocument
	if err := dec.Decode(&document); err != nil {
		if errors.Is(err, io.EOF) {
			return sourcesConfig{}, errors.New("sources file is empty; expected a JSON object with a \"sources\" array")
		}
		return sourcesConfig{}, err
	}
	var extra json.RawMessage
	if err := dec.Decode(&extra); !errors.Is(err, io.EOF) {
		return sourcesConfig{}, errors.New("trailing data after the sources object; the file must contain exactly one JSON object")
	}
	if document.Sources == nil {
		return sourcesConfig{}, errors.New(`missing required top-level field "sources"`)
	}

	defaultText := document.DefaultInterval
	if defaultText == "" {
		defaultText = "24h"
	}
	defaultInterval, err := parseSourceInterval("defaultInterval", defaultText)
	if err != nil {
		return sourcesConfig{}, err
	}

	result := sourcesConfig{defaultIntervalText: defaultText, sources: make([]scheduledSource, 0, len(*document.Sources))}
	names := make(map[string]struct{}, len(*document.Sources))
	for index, raw := range *document.Sources {
		source, err := parseScheduledSource(raw, index+1, defaultText, defaultInterval)
		if err != nil {
			return sourcesConfig{}, err
		}
		if _, exists := names[source.name]; exists {
			return sourcesConfig{}, fmt.Errorf("source %d name %q is duplicated; source names must be unique", index+1, source.name)
		}
		names[source.name] = struct{}{}
		result.sources = append(result.sources, source)
	}
	return result, nil
}

func parseScheduledSource(raw json.RawMessage, index int, defaultText string, defaultInterval time.Duration) (scheduledSource, error) {
	var probe sourceCommonJSON
	if err := json.Unmarshal(raw, &probe); err != nil {
		return scheduledSource{}, fmt.Errorf("source %d is not a valid JSON object: %w", index, err)
	}
	if probe.Name == "" {
		return scheduledSource{}, fmt.Errorf("source %d field \"name\" is required", index)
	}
	if !sourceNamePattern.MatchString(probe.Name) {
		return scheduledSource{}, fmt.Errorf("source %d field \"name\" must match [a-z0-9-]+; got %q", index, probe.Name)
	}
	if probe.Connector == "" {
		return scheduledSource{}, fmt.Errorf("source %q field \"connector\" is required", probe.Name)
	}

	tenant := probe.Tenant
	if tenant == "" {
		tenant = focus.DefaultTenant
	}
	intervalText := probe.Interval
	interval := defaultInterval
	if intervalText == "" {
		intervalText = defaultText
	} else {
		var err error
		interval, err = parseSourceInterval(fmt.Sprintf("source %q field \"interval\"", probe.Name), intervalText)
		if err != nil {
			return scheduledSource{}, err
		}
	}

	source := scheduledSource{
		name: probe.Name, connector: probe.Connector, tenant: tenant,
		interval: interval, intervalText: intervalText,
	}
	var validation error
	switch probe.Connector {
	case awsfocus.Name:
		var value awsFocusJSON
		if err := decodeStrictSource(raw, &value); err != nil {
			return scheduledSource{}, sourceDecodeError(probe.Name, err)
		}
		cfg := awsFocusSource{tenant: tenant, path: value.Path}
		validation, source.config = validateAWSFocusSource(cfg), cfg
	case awsfocuss3.Name:
		var value awsFocusS3JSON
		if err := decodeStrictSource(raw, &value); err != nil {
			return scheduledSource{}, sourceDecodeError(probe.Name, err)
		}
		cfg := awsFocusS3Source{tenant: tenant, bucket: value.Bucket, prefix: value.Prefix}
		validation, source.config = validateAWSFocusS3Source(cfg), cfg
	case azurefocus.Name:
		var value azureFocusJSON
		if err := decodeStrictSource(raw, &value); err != nil {
			return scheduledSource{}, sourceDecodeError(probe.Name, err)
		}
		cfg := azureFocusSource{tenant: tenant, accountURL: value.AccountURL, container: value.Container, prefix: value.Prefix}
		validation, source.config = validateAzureFocusSource(cfg), cfg
	case gcpfocusbq.Name:
		var value gcpFocusBQJSON
		if err := decodeStrictSource(raw, &value); err != nil {
			return scheduledSource{}, sourceDecodeError(probe.Name, err)
		}
		cfg := gcpFocusBQSource{
			tenant: tenant, datasetProject: value.DatasetProject, dataset: value.Dataset,
			table: value.Table, location: value.Location, jobProject: value.JobProject,
			credential: value.Credential, baseURL: value.BaseURL, tokenURL: value.TokenURL,
			since: value.Since, keyFile: value.KeyFile,
		}
		validation, source.config = validateGCPFocusBQSource(cfg), cfg
	case anthropiccost.Name:
		var value anthropicCostJSON
		if err := decodeStrictSource(raw, &value); err != nil {
			return scheduledSource{}, sourceDecodeError(probe.Name, err)
		}
		cfg := anthropicCostSource{tenant: tenant, credential: value.Credential, baseURL: value.BaseURL, since: value.Since, keyFile: value.KeyFile}
		validation, source.config = validateAnthropicCostSource(cfg), cfg
	case openaicost.Name:
		var value openAICostJSON
		if err := decodeStrictSource(raw, &value); err != nil {
			return scheduledSource{}, sourceDecodeError(probe.Name, err)
		}
		cfg := openAICostSource{tenant: tenant, credential: value.Credential, baseURL: value.BaseURL, since: value.Since, keyFile: value.KeyFile}
		validation, source.config = validateOpenAICostSource(cfg), cfg
	case focuscsv.Name:
		var value focusCSVJSON
		if err := decodeStrictSource(raw, &value); err != nil {
			return scheduledSource{}, sourceDecodeError(probe.Name, err)
		}
		cfg := focusCSVSource{
			tenant: tenant, path: value.Path, focusVersion: value.FocusVersion,
			sourceLabel: value.SourceLabel, lenient: value.Lenient,
		}
		validation = validateFocusCSVSource(cfg)
		if validation == nil {
			validation = validateConfigFocusVersion(cfg.focusVersion)
		}
		source.config = cfg
	default:
		return scheduledSource{}, fmt.Errorf("source %q has unknown connector %q; available connectors: %s", probe.Name, probe.Connector, strings.Join(availableConnectorNames(), ", "))
	}
	if validation != nil {
		return scheduledSource{}, configSourceValidation(probe.Name, validation)
	}
	return source, nil
}

func decodeStrictSource(raw json.RawMessage, dst any) error {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		return err
	}
	var extra json.RawMessage
	if err := dec.Decode(&extra); !errors.Is(err, io.EOF) {
		return errors.New("source must contain exactly one JSON object")
	}
	return nil
}

func sourceDecodeError(name string, err error) error {
	return fmt.Errorf("source %q: %w", name, err)
}

func parseSourceInterval(field, value string) (time.Duration, error) {
	interval, err := time.ParseDuration(value)
	if err != nil {
		return 0, fmt.Errorf("%s must be a Go duration such as 24h or 30m; got %q: %w", field, value, err)
	}
	if interval < minimumSourceInterval {
		return 0, fmt.Errorf("%s %q is below the 15m minimum because scheduled runs re-query the source", field, value)
	}
	return interval, nil
}

func validateConfigFocusVersion(value string) error {
	if value == "" {
		validation := &sourceValidationError{connector: focuscsv.Name, fields: []sourceField{sourceFieldFocusVersion}}
		return validation
	}
	// This list must stay in lockstep with focuscsv.ParseVersion (the
	// authoritative acceptor, which canonicalizes 1.0r2 to 1.0); the CLI
	// defers version validation to discovery, a sources file rejects it at
	// parse time.
	switch value {
	case "1.0", "1.0r2", "1.1", "1.2", "1.3", "1.4":
		return nil
	default:
		return fmt.Errorf("field \"focusVersion\" has unsupported value %q; supported values are 1.0, 1.0r2, 1.1, 1.2, 1.3, 1.4", value)
	}
}

func configSourceValidation(name string, err error) error {
	var validation *sourceValidationError
	if !errors.As(err, &validation) {
		// Non-field-shaped validation failures (e.g. an unsupported
		// focusVersion value) still need the source name so a multi-source
		// file identifies the offending entry.
		return fmt.Errorf("source %q: %w", name, err)
	}
	fields := make([]string, 0, len(validation.fields))
	for _, field := range validation.fields {
		fields = append(fields, fmt.Sprintf("%q", field))
	}
	message := fmt.Sprintf("source %q field(s) %s are required for connector %q", name, strings.Join(fields, ", "), validation.connector)
	return &sourceValidationPathError{message: message, cause: validation}
}

func availableConnectorNames() []string {
	return []string{
		awsfocus.Name, awsfocuss3.Name, azurefocus.Name, gcpfocusbq.Name,
		anthropiccost.Name, openaicost.Name, focuscsv.Name,
	}
}

const sourcesUsage = `usage: costroid sources <subcommand>

subcommands:
  validate [--sources <path>]  parse and structurally validate the sources file

The path resolves from --sources, then $COSTROID_SOURCES, then
<config-dir>/costroid/sources.json. Validation reads only the JSON file. It
does not open the store, check credential slots, or contact remote sources`

const sourcesFlagUsage = "sources JSON path (overrides $COSTROID_SOURCES; default <config-dir>/costroid/sources.json)"

func sourcesCmd(args []string) error {
	if len(args) == 0 {
		return errors.New("missing sources subcommand\n" + sourcesUsage)
	}
	if args[0] != "validate" {
		return fmt.Errorf("unknown sources subcommand %q\n%s", args[0], sourcesUsage)
	}
	flags := flag.NewFlagSet("sources validate", flag.ContinueOnError)
	sourcesFlag := flags.String("sources", "", sourcesFlagUsage)
	if stop, err := parseFlags(flags, args[1:]); stop || err != nil {
		return err
	}
	path := resolveSourcesPath(*sourcesFlag)
	cfg, err := loadSourcesConfig(path)
	if err != nil {
		return err
	}
	fmt.Printf("sources file valid: %d source(s); structural validation only, credential slots and remote reachability were not checked\n", len(cfg.sources))
	return nil
}
