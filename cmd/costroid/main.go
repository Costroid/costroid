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
	"strings"
	"syscall"
	"time"

	"github.com/Costroid/costroid/internal/api"
	"github.com/Costroid/costroid/internal/focus"
	"github.com/Costroid/costroid/internal/ingest"
	"github.com/Costroid/costroid/internal/ingest/awsfocus"
	"github.com/Costroid/costroid/internal/ingest/awsfocuss3"
	"github.com/Costroid/costroid/internal/storage"
	"github.com/Costroid/costroid/internal/webdist"
)

// version is stamped at build time via -ldflags "-X main.version=...".
var version = "0.1.0-dev"

const usage = `usage: costroid <command> [flags]

commands:
  serve   serve the HTTP API and dashboard  (costroid serve [--addr host:port])
  ingest  ingest a cost export into the store
          local file:  costroid ingest --connector aws-focus --path <file> [--tenant default]
          live S3:     costroid ingest --connector aws-focus-s3 --bucket <b> --prefix <p>
                       [--period YYYY-MM] [--tenant default]
                       (--prefix is the export root: the configured S3 prefix plus the
                       export name; auth via the ambient AWS credential chain only;
                       without --period every discovered billing period is ingested)

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
	connectorFlag := flags.String("connector", "", `connector name (available: "aws-focus", "aws-focus-s3")`)
	pathFlag := flags.String("path", "", "path to the export file to ingest (aws-focus)")
	bucketFlag := flags.String("bucket", "", "S3 bucket holding the AWS Data Export (aws-focus-s3)")
	prefixFlag := flags.String("prefix", "", "export root prefix: the export's configured S3 prefix plus its name (aws-focus-s3)")
	periodFlag := flags.String("period", "", "ingest only this billing period, e.g. 2026-06 (aws-focus-s3; default: all discovered)")
	tenantFlag := flags.String("tenant", focus.DefaultTenant, "tenant identifier recorded on the ingested records")
	if stop, err := parseFlags(flags, args); stop || err != nil {
		return err
	}

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	switch *connectorFlag {
	case awsfocus.Name:
		if *pathFlag == "" {
			return errors.New("--path is required for the aws-focus connector")
		}
		return runIngest(ctx, []ingestJob{{conn: awsfocus.New(*pathFlag)}}, *tenantFlag)
	case awsfocuss3.Name:
		if *bucketFlag == "" || *prefixFlag == "" {
			return errors.New("--bucket and --prefix are required for the aws-focus-s3 connector")
		}
		// Discover before opening (and locking) the store: a failed
		// discovery must not block a running server.
		conns, err := awsfocuss3.Discover(ctx, *bucketFlag, *prefixFlag)
		if err != nil {
			return err
		}
		jobs, err := s3Jobs(conns, *periodFlag)
		if err != nil {
			return err
		}
		return runIngest(ctx, jobs, *tenantFlag)
	case "":
		return errors.New(`--connector is required (available: "aws-focus", "aws-focus-s3")`)
	default:
		return fmt.Errorf(`unknown connector %q (available: "aws-focus", "aws-focus-s3")`, *connectorFlag)
	}
}

// ingestJob is one connector run; period labels multi-period output.
type ingestJob struct {
	conn   ingest.Connector
	period string
}

// s3Jobs maps discovered per-period connectors to jobs, filtered to one
// billing period when requested.
func s3Jobs(conns []*awsfocuss3.Connector, period string) ([]ingestJob, error) {
	var jobs []ingestJob
	var available []string
	for _, conn := range conns {
		available = append(available, conn.BillingPeriod())
		if period == "" || conn.BillingPeriod() == period {
			jobs = append(jobs, ingestJob{conn: conn, period: conn.BillingPeriod()})
		}
	}
	if len(jobs) == 0 {
		return nil, fmt.Errorf("billing period %s not found in the export (discovered: %s)",
			period, strings.Join(available, ", "))
	}
	return jobs, nil
}

// runIngest opens the store once and runs every job through the shared
// pipeline. Each period's replace is transactional and independent, so
// one failing period doesn't roll back the others; the exit status is
// non-zero if any failed, and every period's outcome is printed.
func runIngest(ctx context.Context, jobs []ingestJob, tenant string) error {
	store, err := storage.Open(ctx, dataDir())
	if err != nil {
		return err
	}
	defer func() { _ = store.Close() }()

	var failed []string
	for _, job := range jobs {
		label := ""
		if job.period != "" {
			label = "period " + job.period + ": "
		}
		result, err := ingest.Run(ctx, job.conn, store, tenant)
		if err != nil {
			failed = append(failed, job.period)
			fmt.Fprintf(os.Stderr, "costroid: %sfailed: %v\n", label, err)
			continue
		}
		if result.Unchanged {
			fmt.Printf("%ssource content unchanged; batch %s/%s kept as is (%d record(s), tenant %s)\n",
				label, result.Batch.Connector, result.Batch.SourceIdentity, result.Records, result.Batch.TenantID)
			continue
		}
		fmt.Printf("%singested %d record(s) as batch %s/%s (tenant %s, %s)\n",
			label, result.Records, result.Batch.Connector, result.Batch.SourceIdentity,
			result.Batch.TenantID, result.Batch.ContentHash)
	}
	if len(failed) > 0 {
		return fmt.Errorf("%d of %d period(s) failed (%s); each period replaces independently, so the successful ones are stored",
			len(failed), len(jobs), strings.Join(failed, ", "))
	}
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
