// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Costroid Authors

// Command costroid is the Costroid binary. It serves the HTTP API with
// the embedded web dashboard, and ingests cost exports into the embedded
// store.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/Costroid/costroid/internal/api"
	"github.com/Costroid/costroid/internal/focus"
	"github.com/Costroid/costroid/internal/ingest"
	"github.com/Costroid/costroid/internal/ingest/awsfocus"
	"github.com/Costroid/costroid/internal/storage"
	"github.com/Costroid/costroid/internal/webdist"
)

// version is stamped at build time via -ldflags "-X main.version=...".
var version = "0.1.0-dev"

const usage = `usage: costroid <command> [flags]

commands:
  serve   serve the HTTP API and dashboard  (costroid serve [--addr host:port])
  ingest  ingest a cost export into the store
          (costroid ingest --connector aws-focus --path <file> [--tenant default])

The store location is $COSTROID_DATA_DIR (default ./data). The embedded
store allows a single process at a time: stop 'costroid serve' before
running 'costroid ingest'`

// errReported signals that the failure was already printed (e.g. by the
// FlagSet), so main must not print it a second time.
var errReported = errors.New("error already reported")

func main() {
	if err := run(os.Args[1:]); err != nil {
		if !errors.Is(err, errReported) {
			fmt.Fprintln(os.Stderr, "costroid:", err)
		}
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) == 0 {
		return errors.New("missing command\n" + usage)
	}
	switch args[0] {
	case "serve":
		return serve(args[1:])
	case "ingest":
		return ingestCmd(args[1:])
	default:
		return fmt.Errorf("unknown command %q\n%s", args[0], usage)
	}
}

// parseFlags parses args, mapping -h/--help to (stop, nil) after the
// FlagSet printed its usage once, and other parse errors — which the
// ContinueOnError FlagSet already printed — to errReported.
func parseFlags(flags *flag.FlagSet, args []string) (stop bool, err error) {
	switch err := flags.Parse(args); {
	case err == nil:
		return false, nil
	case errors.Is(err, flag.ErrHelp):
		return true, nil
	default:
		return true, errReported
	}
}

func serve(args []string) error {
	flags := flag.NewFlagSet("serve", flag.ContinueOnError)
	addrFlag := flags.String("addr", "", `listen address (overrides $COSTROID_ADDR; default ":8080")`)
	if stop, err := parseFlags(flags, args); stop || err != nil {
		return err
	}
	addr := resolveAddr(*addrFlag, os.Getenv("COSTROID_ADDR"))

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	store, err := storage.Open(ctx, dataDir())
	if err != nil {
		return err
	}
	defer func() { _ = store.Close() }()

	srv := &http.Server{
		Addr:    addr,
		Handler: api.NewHandler(version, webdist.FS(), store),
		// No blanket ReadTimeout: large ingest request bodies must be
		// able to stream longer than any fixed limit.
		ReadHeaderTimeout: 5 * time.Second,
		IdleTimeout:       120 * time.Second,
	}

	errc := make(chan error, 1)
	go func() {
		fmt.Printf("costroid %s listening on %s\n", version, addr)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errc <- fmt.Errorf("serving HTTP on %s: %w", addr, err)
			return
		}
		errc <- nil
	}()

	select {
	case err := <-errc:
		return err
	case <-ctx.Done():
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		return fmt.Errorf("shutting down: %w", err)
	}
	return <-errc
}

func ingestCmd(args []string) error {
	flags := flag.NewFlagSet("ingest", flag.ContinueOnError)
	connectorFlag := flags.String("connector", "", `connector name (available: "aws-focus")`)
	pathFlag := flags.String("path", "", "path to the export file to ingest")
	tenantFlag := flags.String("tenant", focus.DefaultTenant, "tenant identifier recorded on the ingested records")
	if stop, err := parseFlags(flags, args); stop || err != nil {
		return err
	}

	var conn ingest.Connector
	switch *connectorFlag {
	case awsfocus.Name:
		if *pathFlag == "" {
			return errors.New("--path is required for the aws-focus connector")
		}
		conn = awsfocus.New(*pathFlag)
	case "":
		return errors.New("--connector is required (available: \"aws-focus\")")
	default:
		return fmt.Errorf("unknown connector %q (available: \"aws-focus\")", *connectorFlag)
	}

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	store, err := storage.Open(ctx, dataDir())
	if err != nil {
		return err
	}
	defer func() { _ = store.Close() }()

	result, err := ingest.Run(ctx, conn, store, *tenantFlag)
	if err != nil {
		return err
	}
	if result.Unchanged {
		fmt.Printf("source content unchanged; batch %s/%s kept as is (%d record(s), tenant %s)\n",
			result.Batch.Connector, result.Batch.SourceIdentity, result.Records, result.Batch.TenantID)
		return nil
	}
	fmt.Printf("ingested %d record(s) as batch %s/%s (tenant %s, %s)\n",
		result.Records, result.Batch.Connector, result.Batch.SourceIdentity,
		result.Batch.TenantID, result.Batch.ContentHash)
	return nil
}

// dataDir resolves the data directory: $COSTROID_DATA_DIR or ./data.
func dataDir() string {
	if dir := os.Getenv("COSTROID_DATA_DIR"); dir != "" {
		return dir
	}
	return "data"
}

// resolveAddr picks the listen address: the --addr flag wins over
// $COSTROID_ADDR, which wins over the default.
func resolveAddr(flagAddr, envAddr string) string {
	if flagAddr != "" {
		return flagAddr
	}
	if envAddr != "" {
		return envAddr
	}
	return ":8080"
}
