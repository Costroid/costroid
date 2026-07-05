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
	"io"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/Costroid/costroid/internal/api"
	"github.com/Costroid/costroid/internal/credentials"
	"github.com/Costroid/costroid/internal/focus"
	"github.com/Costroid/costroid/internal/ingest"
	"github.com/Costroid/costroid/internal/ingest/awsfocus"
	"github.com/Costroid/costroid/internal/ingest/awsfocuss3"
	"github.com/Costroid/costroid/internal/ingest/azurefocus"
	"github.com/Costroid/costroid/internal/storage"
	"github.com/Costroid/costroid/internal/webdist"
)

// version is stamped at build time via -ldflags "-X main.version=...".
var version = "0.1.0-dev"

const usage = `usage: costroid <command> [flags]

commands:
  serve   serve the HTTP API and dashboard  (costroid serve [--addr host:port])
  credentials  manage the encrypted credential store (decision D32)
          costroid credentials init [--key-file <path>]
          costroid credentials set <name>     (reads the secret from stdin)
          costroid credentials list
          costroid credentials delete <name>
  ingest  ingest a cost export into the store
          local file:  costroid ingest --connector aws-focus --path <file> [--tenant default]
          live S3:     costroid ingest --connector aws-focus-s3 --bucket <b> --prefix <p>
                       [--period YYYY-MM] [--tenant default] [--force]
                       (--prefix is the export root: the configured S3 prefix plus the
                       export name; auth via the ambient AWS credential chain only;
                       without --period every discovered billing period is ingested;
                       periods whose stored manifest state is unchanged are skipped
                       without fetching anything — --force re-processes them)
          live Azure:  costroid ingest --connector azure-focus --account-url <u>
                       --container <c> --prefix <p>
                       [--period YYYY-MM] [--tenant default] [--force]
                       (--account-url is the storage account's blob endpoint, e.g.
                       https://<account>.blob.core.windows.net/; --prefix is the export
                       root: the export's storage directory plus the export name; auth
                       via the ambient Azure credential chain only — no SAS, no keys;
                       the same --period/--force/skip semantics as aws-focus-s3)

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
	case "credentials":
		return credentialsCmd(args[1:])
	case "ingest":
		return ingestCmd(args[1:])
	default:
		return fmt.Errorf("unknown command %q\n%s", args[0], usage)
	}
}

const credentialsUsage = `usage: costroid credentials <subcommand>

subcommands:
  init [--key-file <path>]  generate the 256-bit key file (refuses to overwrite)
  set <name>                store/replace a secret, read from stdin only
  list                      list credential names and timestamps (no secrets)
  delete <name>             remove a credential

The key file defaults to ~/.config/costroid/credentials.key; override its
path with --key-file or $COSTROID_CREDENTIALS_KEY_FILE (the env var carries
the path, never key material). Secrets are AES-256-GCM encrypted at rest in
the store and never printed, logged, or passed via argv or the environment`

// credentialsCmd dispatches the credential-store subcommands (decision D32).
func credentialsCmd(args []string) error {
	if len(args) == 0 {
		return errors.New("missing credentials subcommand\n" + credentialsUsage)
	}
	switch args[0] {
	case "init":
		return credentialsInit(args[1:])
	case "set":
		return credentialsSet(args[1:])
	case "list":
		return credentialsList(args[1:])
	case "delete":
		return credentialsDelete(args[1:])
	default:
		return fmt.Errorf("unknown credentials subcommand %q\n%s", args[0], credentialsUsage)
	}
}

const keyFileFlagUsage = "key file path (overrides $COSTROID_CREDENTIALS_KEY_FILE; default ~/.config/costroid/credentials.key)"

func credentialsInit(args []string) error {
	flags := flag.NewFlagSet("credentials init", flag.ContinueOnError)
	keyFileFlag := flags.String("key-file", "", keyFileFlagUsage)
	if stop, err := parseFlags(flags, args); stop || err != nil {
		return err
	}
	path, err := credentials.ResolveKeyPath(*keyFileFlag)
	if err != nil {
		return err
	}
	if err := credentials.InitKeyFile(path); err != nil {
		return err
	}
	fmt.Printf("wrote a new 256-bit credential key file to %s\n"+
		"keep it safe and OUT of backups of the data directory — losing it makes every stored credential "+
		"undecryptable, and leaking it defeats the encryption\n", path)
	return nil
}

func credentialsSet(args []string) error {
	flags := flag.NewFlagSet("credentials set", flag.ContinueOnError)
	keyFileFlag := flags.String("key-file", "", keyFileFlagUsage)
	if stop, err := parseFlags(flags, args); stop || err != nil {
		return err
	}
	name := flags.Arg(0)
	if name == "" {
		return errors.New("usage: costroid credentials set <name> (the secret is read from stdin)")
	}
	// Stdin ONLY — never argv, never env (decisions D17, D32).
	secret, err := readSecretStdin()
	if err != nil {
		return err
	}
	path, err := credentials.ResolveKeyPath(*keyFileFlag)
	if err != nil {
		return err
	}

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()
	store, err := storage.Open(ctx, dataDir())
	if err != nil {
		return err
	}
	defer func() { _ = store.Close() }()

	vault, err := credentials.Open(path, store)
	if err != nil {
		return err
	}
	if err := vault.Set(ctx, name, secret); err != nil {
		return err
	}
	fmt.Printf("stored credential %q\n", name)
	return nil
}

// readSecretStdin reads the secret from stdin, trims exactly one trailing
// newline (bare LF or CRLF), and refuses an empty secret. It never echoes
// what it read.
func readSecretStdin() (string, error) {
	data, err := io.ReadAll(os.Stdin)
	if err != nil {
		return "", fmt.Errorf("reading the secret from stdin: %w", err)
	}
	s := string(data)
	if strings.HasSuffix(s, "\n") {
		s = strings.TrimSuffix(s, "\n")
		s = strings.TrimSuffix(s, "\r")
	}
	if s == "" {
		return "", errors.New("the secret read from stdin is empty — pipe the key in, " +
			`e.g. printf %s "$KEY" | costroid credentials set <name>`)
	}
	return s, nil
}

func credentialsList(args []string) error {
	flags := flag.NewFlagSet("credentials list", flag.ContinueOnError)
	if stop, err := parseFlags(flags, args); stop || err != nil {
		return err
	}
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()
	store, err := storage.Open(ctx, dataDir())
	if err != nil {
		return err
	}
	defer func() { _ = store.Close() }()

	infos, err := store.ListCredentials(ctx)
	if err != nil {
		return err
	}
	if len(infos) == 0 {
		fmt.Println("no credentials stored (add one with `costroid credentials set <name>`)")
		return nil
	}
	for _, info := range infos {
		fmt.Printf("%s\tcreated %s\tupdated %s\n", info.Name,
			info.CreatedAt.Format(time.RFC3339), info.UpdatedAt.Format(time.RFC3339))
	}
	return nil
}

func credentialsDelete(args []string) error {
	flags := flag.NewFlagSet("credentials delete", flag.ContinueOnError)
	if stop, err := parseFlags(flags, args); stop || err != nil {
		return err
	}
	name := flags.Arg(0)
	if name == "" {
		return errors.New("usage: costroid credentials delete <name>")
	}
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()
	store, err := storage.Open(ctx, dataDir())
	if err != nil {
		return err
	}
	defer func() { _ = store.Close() }()

	deleted, err := store.DeleteCredential(ctx, name)
	if err != nil {
		return err
	}
	if !deleted {
		return fmt.Errorf("no credential named %q is stored — nothing to delete", name)
	}
	fmt.Printf("deleted credential %q\n", name)
	return nil
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
	connectorFlag := flags.String("connector", "", `connector name (available: "aws-focus", "aws-focus-s3", "azure-focus")`)
	pathFlag := flags.String("path", "", "path to the export file to ingest (aws-focus)")
	bucketFlag := flags.String("bucket", "", "S3 bucket holding the AWS Data Export (aws-focus-s3)")
	accountURLFlag := flags.String("account-url", "", "Azure storage account blob endpoint, e.g. https://<account>.blob.core.windows.net/ (azure-focus)")
	containerFlag := flags.String("container", "", "Azure blob container holding the Cost Management export (azure-focus)")
	prefixFlag := flags.String("prefix", "", "export root prefix: the export's configured directory/prefix plus its name (aws-focus-s3, azure-focus)")
	periodFlag := flags.String("period", "", "ingest only this billing period, e.g. 2026-06 (aws-focus-s3, azure-focus; default: all discovered)")
	tenantFlag := flags.String("tenant", focus.DefaultTenant, "tenant identifier recorded on the ingested records")
	forceFlag := flags.Bool("force", false, "re-process every period even when its stored manifest state is unchanged (aws-focus-s3, azure-focus)")
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
		store, err := storage.Open(ctx, dataDir())
		if err != nil {
			return err
		}
		defer func() { _ = store.Close() }()
		return runIngest(ctx, store, []ingestJob{{conn: awsfocus.New(*pathFlag)}}, *tenantFlag)
	case awsfocuss3.Name:
		if *bucketFlag == "" || *prefixFlag == "" {
			return errors.New("--bucket and --prefix are required for the aws-focus-s3 connector")
		}
		// The store opens BEFORE discovery: discovery needs the stored
		// sync tuples to skip unchanged periods (migration 0003).
		// duckdb-go v2 is a DriverContext driver, so storage.Open takes
		// the single-writer file lock inside sql.Open itself — a running
		// `costroid serve` therefore fails fast right here with the
		// store's actionable in-use message.
		store, err := storage.Open(ctx, dataDir())
		if err != nil {
			return err
		}
		defer func() { _ = store.Close() }()

		// --force bypasses the tuple skip by discovering with no prior
		// state; every period then falls through to the content-hash
		// path, which still short-circuits byte-identical deliveries.
		prior := map[string]awsfocuss3.ManifestState{}
		if !*forceFlag {
			states, err := store.SyncStates(ctx, awsfocuss3.Name)
			if err != nil {
				return err
			}
			for id, st := range states {
				// The tuple skip is tenant-aware (slice-3 review fix-up):
				// a batch homed under a different tenant must not be
				// skipped. Dropping its tuple sends the period down the
				// content-hash path, whose tenant-sensitive short-circuit
				// re-homes the stored records.
				if st.TenantID != *tenantFlag {
					continue
				}
				prior[id] = awsfocuss3.ManifestState{
					Key:          st.ManifestKey,
					ETag:         st.ManifestETag,
					LastModified: st.ManifestLastModified,
					Size:         st.ManifestSize,
				}
			}
		}
		periods, err := awsfocuss3.Discover(ctx, *bucketFlag, *prefixFlag, prior)
		if err != nil {
			return err
		}
		jobs, err := s3Jobs(periods, *periodFlag)
		if err != nil {
			return err
		}
		return runIngest(ctx, store, jobs, *tenantFlag)
	case azurefocus.Name:
		if *accountURLFlag == "" || *containerFlag == "" || *prefixFlag == "" {
			return errors.New("--account-url, --container, and --prefix are required for the azure-focus connector")
		}
		// Same shape as aws-focus-s3: the store opens (and locks) before
		// discovery, which needs both the stored sync tuples and the
		// manifest-attribution cache (migration 0004).
		store, err := storage.Open(ctx, dataDir())
		if err != nil {
			return err
		}
		defer func() { _ = store.Close() }()

		prior := map[string]azurefocus.ManifestState{}
		if !*forceFlag {
			states, err := store.SyncStates(ctx, azurefocus.Name)
			if err != nil {
				return err
			}
			for id, st := range states {
				// Tenant-aware tuple skip, exactly as for aws-focus-s3.
				if st.TenantID != *tenantFlag {
					continue
				}
				prior[id] = azurefocus.ManifestState{
					Key:          st.ManifestKey,
					ETag:         st.ManifestETag,
					LastModified: st.ManifestLastModified,
					Size:         st.ManifestSize,
				}
			}
		}
		periods, err := azurefocus.Discover(ctx, *accountURLFlag, *containerFlag, *prefixFlag, prior, store)
		if err != nil {
			return err
		}
		jobs, err := azureJobs(periods, *periodFlag)
		if err != nil {
			return err
		}
		return runIngest(ctx, store, jobs, *tenantFlag)
	case "":
		return errors.New(`--connector is required (available: "aws-focus", "aws-focus-s3", "azure-focus")`)
	default:
		return fmt.Errorf(`unknown connector %q (available: "aws-focus", "aws-focus-s3", "azure-focus")`, *connectorFlag)
	}
}

// ingestJob is one connector run; period labels multi-period output. A
// job with a nil conn is a skipped period (unchanged sync tuple): nothing
// runs, only the skip line prints. A job with a discovery error runs
// nothing either — it prints its per-period failure and counts against
// the exit status, without blocking the other periods.
type ingestJob struct {
	conn   ingest.Connector
	period string
	// discoveryErr is the period's discovery failure (e.g. a manifest
	// anomaly); reported per period like a pipeline failure.
	discoveryErr error
	// skippedSince is the stored manifest LastModified of a skipped
	// period, printed on its skip line.
	skippedSince time.Time
	// sync, when non-nil, is upserted after the job runs successfully —
	// on EVERY outcome (fresh, replaced, and unchanged short-circuit) —
	// so a touched-but-identical delivery cannot permanently defeat the
	// tuple skip (see storage.SyncState).
	sync *storage.SyncState
}

// s3Jobs maps discovered billing periods to jobs, filtered to one
// billing period when requested. Skipped periods stay in the job list —
// they print their skip line and keep --period filtering working.
func s3Jobs(periods []awsfocuss3.Period, period string) ([]ingestJob, error) {
	var jobs []ingestJob
	var available []string
	for _, p := range periods {
		available = append(available, p.Billing)
		if period != "" && p.Billing != period {
			continue
		}
		job := ingestJob{period: p.Billing}
		switch {
		case p.Err != nil:
			job.discoveryErr = p.Err
		case p.Skipped():
			job.skippedSince = p.Manifest.LastModified
		default:
			job.conn = p.Conn
			job.sync = &storage.SyncState{
				Connector:            p.Conn.Name(),
				SourceIdentity:       p.Conn.SourceIdentity(),
				ManifestKey:          p.Manifest.Key,
				ManifestETag:         p.Manifest.ETag,
				ManifestLastModified: p.Manifest.LastModified,
				ManifestSize:         p.Manifest.Size,
			}
		}
		jobs = append(jobs, job)
	}
	if len(jobs) == 0 {
		return nil, fmt.Errorf("billing period %s not found in the export (discovered: %s)",
			period, strings.Join(available, ", "))
	}
	return jobs, nil
}

// azureJobs maps discovered Azure billing periods to jobs, filtered to
// one billing period when requested — the azure-focus twin of s3Jobs.
func azureJobs(periods []azurefocus.Period, period string) ([]ingestJob, error) {
	var jobs []ingestJob
	var available []string
	for _, p := range periods {
		available = append(available, p.Billing)
		if period != "" && p.Billing != period {
			continue
		}
		job := ingestJob{period: p.Billing}
		switch {
		case p.Err != nil:
			job.discoveryErr = p.Err
		case p.Skipped():
			job.skippedSince = p.Manifest.LastModified
		default:
			job.conn = p.Conn
			job.sync = &storage.SyncState{
				Connector:            p.Conn.Name(),
				SourceIdentity:       p.Conn.SourceIdentity(),
				ManifestKey:          p.Manifest.Key,
				ManifestETag:         p.Manifest.ETag,
				ManifestLastModified: p.Manifest.LastModified,
				ManifestSize:         p.Manifest.Size,
			}
		}
		jobs = append(jobs, job)
	}
	if len(jobs) == 0 {
		return nil, fmt.Errorf("billing period %s not found in the export (discovered: %s)",
			period, strings.Join(available, ", "))
	}
	return jobs, nil
}

// runIngest runs every job through the shared pipeline. Each period's
// replace is transactional and independent, so one failing period
// doesn't roll back the others; the exit status is non-zero if any
// failed, and every period's outcome is printed.
func runIngest(ctx context.Context, store storage.Store, jobs []ingestJob, tenant string) error {
	var failed []string
	for _, job := range jobs {
		label := ""
		if job.period != "" {
			label = "period " + job.period + ": "
		}
		if job.discoveryErr != nil {
			failed = append(failed, job.period)
			fmt.Fprintf(os.Stderr, "costroid: %sfailed: %v\n", label, job.discoveryErr)
			continue
		}
		if job.conn == nil {
			fmt.Printf("%sunchanged since %s; skipped\n", label, job.skippedSince.UTC().Format(time.RFC3339))
			continue
		}
		result, err := ingest.Run(ctx, job.conn, store, tenant)
		if err != nil {
			failed = append(failed, job.period)
			fmt.Fprintf(os.Stderr, "costroid: %sfailed: %v\n", label, err)
			continue
		}
		if job.sync != nil {
			if err := store.UpsertSyncState(ctx, *job.sync); err != nil {
				failed = append(failed, job.period)
				fmt.Fprintf(os.Stderr, "costroid: %sfailed recording sync state: %v\n", label, err)
				continue
			}
		}
		switch {
		case result.Unchanged:
			fmt.Printf("%ssource content unchanged; batch %s/%s kept as is (%d record(s), tenant %s)\n",
				label, result.Batch.Connector, result.Batch.SourceIdentity, result.Records, result.Batch.TenantID)
		case result.Replaced:
			// Restatement visibility (decision D26d): the period's stored
			// BilledCost total before → after the replace.
			fmt.Printf("%sreplaced (%d records; BilledCost %s → %s)\n",
				label, result.Records, result.PreviousBilledCost, result.NewBilledCost)
		default:
			fmt.Printf("%singested %d record(s) as batch %s/%s (tenant %s, %s)\n",
				label, result.Records, result.Batch.Connector, result.Batch.SourceIdentity,
				result.Batch.TenantID, result.Batch.ContentHash)
		}
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
