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
	"io/fs"
	"net"
	"net/http"
	"net/netip"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/Costroid/costroid/internal/allocation"
	"github.com/Costroid/costroid/internal/api"
	"github.com/Costroid/costroid/internal/businessmetrics"
	"github.com/Costroid/costroid/internal/credentials"
	"github.com/Costroid/costroid/internal/demodata"
	"github.com/Costroid/costroid/internal/focus"
	"github.com/Costroid/costroid/internal/ingest"
	"github.com/Costroid/costroid/internal/ingest/anthropiccost"
	"github.com/Costroid/costroid/internal/ingest/awsfocus"
	"github.com/Costroid/costroid/internal/ingest/awsfocuss3"
	"github.com/Costroid/costroid/internal/ingest/azurefocus"
	"github.com/Costroid/costroid/internal/ingest/focuscsv"
	"github.com/Costroid/costroid/internal/ingest/openaicost"
	"github.com/Costroid/costroid/internal/storage"
	"github.com/Costroid/costroid/internal/webdist"
)

// version is stamped at build time via -ldflags "-X main.version=...".
var version = "0.1.0-dev"

const usage = `usage: costroid <command> [flags]

commands:
  demo    seed an isolated synthetic store and serve the real dashboard read-only
          costroid demo [--addr host:port] [--data-dir <empty-directory>]
          (uses a fresh temporary directory by default; never reads the normal
          data directory, credential store, or connectors. The synthetic API is
          unauthenticated, read-only, and binds 127.0.0.1:8080 by default.)
  serve   serve the HTTP API and dashboard
          costroid serve [--addr host:port] [--allocation-rules <path>]
                         (--auth-token-file <path> | --auth-trusted-header <name> | --no-auth)
          (binds 127.0.0.1:8080 by default — loopback only; pass a non-loopback
          --addr to expose it. serve refuses to start unless authentication is
          configured: a bearer token via --auth-token-file/$COSTROID_AUTH_TOKEN(_FILE),
          forward-auth via --auth-trusted-header (recommended header X-WEBAUTH-USER)
          behind a trusted reverse proxy, or --no-auth to opt out explicitly. See
          docs/security.md and 'costroid serve -h')
  allocation  validate the query-time cost-allocation (virtual tagging) rules file
          costroid allocation validate [--rules <path>]
          (the rules path resolves from --rules, then $COSTROID_ALLOCATION_RULES,
          then <config-dir>/costroid/allocation.json; reads only the JSON file —
          no store, so it is safe to run while 'costroid serve' is running)
  metrics  import user-authored business metrics for unit economics
          costroid metrics import --path <file.csv> [--source-label <label>]
                                  [--tenant default]
          (strict CSV format: date,metric,quantity; dates are YYYY-MM-DD and
          quantities are exact positive decimals. Re-importing under the same
          tenant and source label REPLACES that label entirely; a header-only
          file clears it. --source-label defaults to the file's base name.)
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
          AI vendors:  costroid ingest --connector anthropic-cost|openai-cost
                       [--credential <slot>] [--base-url <url>] [--since YYYY-MM]
                       [--period YYYY-MM] [--tenant default] [--force]
                       (one UTC calendar month per billing period; default window is the
                       last 12 months; the Admin API key comes from the encrypted
                       credential store — set it first with 'costroid credentials set
                       <slot>' (slot defaults to the connector name); --force is a
                       documented no-op for these connectors — they keep no sync state)
                       WARNING: an Anthropic Admin key is an UNSCOPEABLE full-org-admin
                       credential (it cannot be restricted to cost/usage reads), so the
                       encrypted credential store carries the whole least-privilege
                       burden — guard the key file accordingly (decisions D32, D17)
          FOCUS CSV:   costroid ingest --connector focus-csv --path <file>
                       --focus-version 1.0|1.0r2|1.1|1.2|1.3|1.4 [--source-label <label>]
                       [--period YYYY-MM] [--tenant default] [--force]
                       (the generic FOCUS import: a plain or gzip-compressed CSV export
                       whose FOCUS version you DECLARE — there is no sniffing; magic bytes
                       decide gzip vs plain. A strict importer: unknown non-x_ columns,
                       missing mandatory columns, and unparseable rows FAIL with an
                       actionable message; no gap-fill or column repair. 1.0/1.1 are
                       accepted for spec-conformant exports (RFC3339 timestamps, empty-cell
                       nulls); 1.0r2 canonicalizes to 1.0. Rows split into
                       one batch per BillingPeriodStart month, keyed <source-label>/<month>
                       (--source-label defaults to the file's base name); re-importing a
                       month under the same label REPLACES it. One import must carry the
                       COMPLETE data for each month it touches under that label — a
                       part-file replaces the month with that part alone. Takes no
                       credentials; --force is a documented no-op — it keeps no sync state)

The store location is $COSTROID_DATA_DIR (default ./data). The embedded
store allows a single process at a time: stop 'costroid serve' before
running 'costroid ingest' or 'costroid metrics import'`

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
	case "demo":
		return demo(args[1:])
	case "serve":
		return serve(args[1:])
	case "allocation":
		return allocationCmd(args[1:])
	case "metrics":
		return metricsCmd(args[1:])
	case "credentials":
		return credentialsCmd(args[1:])
	case "ingest":
		return ingestCmd(args[1:])
	default:
		return fmt.Errorf("unknown command %q\n%s", args[0], usage)
	}
}

const metricsUsage = `usage: costroid metrics <subcommand>

subcommands:
  import --path <file.csv> [--source-label <label>] [--tenant default]

The CSV header is exactly date,metric,quantity. Re-importing under the same
tenant and source label replaces that label entirely; a header-only file clears
it. Stop 'costroid serve' before importing because the embedded store is
single-writer.`

func metricsCmd(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("missing metrics subcommand\n%s", metricsUsage)
	}
	if args[0] != "import" {
		return fmt.Errorf("unknown metrics subcommand %q\n%s", args[0], metricsUsage)
	}
	return metricsImport(args[1:])
}

func metricsImport(args []string) error {
	flags := flag.NewFlagSet("metrics import", flag.ContinueOnError)
	pathFlag := flags.String("path", "", "path to the strict date,metric,quantity CSV")
	sourceLabelFlag := flags.String("source-label", "", "logical replace label (default: the file's base name)")
	tenantFlag := flags.String("tenant", focus.DefaultTenant, "tenant identifier recorded on the imported metrics")
	if stop, err := parseFlags(flags, args); stop || err != nil {
		return err
	}
	if *pathFlag == "" {
		return errors.New("--path is required for metrics import")
	}

	f, err := os.Open(*pathFlag)
	if err != nil {
		return fmt.Errorf("opening business metrics CSV %s: %w", *pathFlag, err)
	}
	rows, parseErr := businessmetrics.Parse(f)
	closeErr := f.Close()
	if parseErr != nil {
		return parseErr
	}
	if closeErr != nil {
		return fmt.Errorf("closing business metrics CSV %s: %w", *pathFlag, closeErr)
	}

	label := *sourceLabelFlag
	if label == "" {
		label = filepath.Base(*pathFlag)
	}
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()
	store, err := storage.Open(ctx, dataDir())
	if err != nil {
		return err
	}
	defer func() { _ = store.Close() }()
	if err := store.ReplaceBusinessMetricsBatch(ctx, *tenantFlag, label, rows); err != nil {
		return err
	}
	if len(rows) == 0 {
		fmt.Printf("cleared business metrics for source label %q (header-only import)\n", label)
		return nil
	}
	metrics := map[string]struct{}{}
	first, last := rows[0].MetricDay, rows[0].MetricDay
	for _, row := range rows {
		metrics[row.MetricName] = struct{}{}
		if row.MetricDay.Before(first) {
			first = row.MetricDay
		}
		if row.MetricDay.After(last) {
			last = row.MetricDay
		}
	}
	fmt.Printf("imported %d business metric row(s) across %d metric(s), %s through %s, replacing source label %q\n",
		len(rows), len(metrics), first.Format(time.DateOnly), last.Format(time.DateOnly), label)
	return nil
}

// allocationRulesEnvVar carries the PATH to the allocation rules file (never
// rule content), mirroring the credential key-file env-var convention (D32).
const allocationRulesEnvVar = "COSTROID_ALLOCATION_RULES"

// Authentication env vars for serve. The *_FILE variant carries a PATH (never
// the token); the bare token variant is documented as weaker (it leaks to child
// processes / `docker inspect` / core dumps, CWE-214) but is still never argv.
const (
	envAuthTokenFile      = "COSTROID_AUTH_TOKEN_FILE"
	envAuthToken          = "COSTROID_AUTH_TOKEN"
	envAuthTrustedHeader  = "COSTROID_AUTH_TRUSTED_HEADER"
	envAuthTrustedProxies = "COSTROID_AUTH_TRUSTED_PROXIES"
)

// defaultTrustedProxies is the forward-auth trusted-peer default: loopback only.
// It is applied LAST, inside the resolver (the resolveAddr empty-default
// pattern), so "operator set it" stays distinguishable from "default applied".
const defaultTrustedProxies = "127.0.0.0/8,::1/128"

// resolveAllocationRulesPath applies the allocation-rules path precedence: the
// flag wins over $COSTROID_ALLOCATION_RULES, which wins over
// os.UserConfigDir()/costroid/allocation.json (the credentials.key precedent,
// D32). On an os.UserConfigDir() error it resolves to "" so serve still starts
// (allocation requests then 400 as unconfigured) — the file's presence and
// validity are checked per request, never at startup, so the file may appear or
// be fixed while serving.
func resolveAllocationRulesPath(flagValue string) string {
	if flagValue != "" {
		return flagValue
	}
	if env := os.Getenv(allocationRulesEnvVar); env != "" {
		return env
	}
	dir, err := os.UserConfigDir()
	if err != nil {
		return ""
	}
	return filepath.Join(dir, "costroid", "allocation.json")
}

const allocationUsage = `usage: costroid allocation <subcommand>

subcommands:
  validate [--rules <path>]  parse and validate the allocation rules file

The rules path resolves from --rules, then $COSTROID_ALLOCATION_RULES (which
carries the path, never rule content), then <config-dir>/costroid/allocation.json.
validate reads only the JSON file — no store — so it is safe to run alongside
'costroid serve'`

// allocationCmd dispatches the query-time cost-allocation subcommands.
func allocationCmd(args []string) error {
	if len(args) == 0 {
		return errors.New("missing allocation subcommand\n" + allocationUsage)
	}
	switch args[0] {
	case "validate":
		return allocationValidate(args[1:])
	default:
		return fmt.Errorf("unknown allocation subcommand %q\n%s", args[0], allocationUsage)
	}
}

const allocationRulesFlagUsage = "allocation rules JSON path (overrides $COSTROID_ALLOCATION_RULES; default <config-dir>/costroid/allocation.json)"

func allocationValidate(args []string) error {
	flags := flag.NewFlagSet("allocation validate", flag.ContinueOnError)
	rulesFlag := flags.String("rules", "", allocationRulesFlagUsage)
	if stop, err := parseFlags(flags, args); stop || err != nil {
		return err
	}
	path := resolveAllocationRulesPath(*rulesFlag)
	if path == "" {
		return errors.New("no allocation rules path (pass --rules or set $COSTROID_ALLOCATION_RULES)")
	}
	f, err := os.Open(path)
	if err != nil {
		return err // actionable os error naming the path (e.g. no such file)
	}
	defer func() { _ = f.Close() }()
	dim, err := allocation.Parse(f)
	if err != nil {
		return err
	}
	fmt.Printf("allocation rules valid: dimension %q with %d rule(s)\n", dim.Name, len(dim.Rules))
	return nil
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
	s := trimOneTrailingNewline(string(data))
	if s == "" {
		return "", errors.New("the secret read from stdin is empty — pipe the key in, " +
			`e.g. printf %s "$KEY" | costroid credentials set <name>`)
	}
	return s, nil
}

// trimOneTrailingNewline strips exactly one trailing newline (bare LF or CRLF)
// and nothing else — NOT TrimSpace, so a secret's own leading/trailing spaces
// and interior newlines survive. This is the D32 stdin-secret hygiene rule,
// reused for the bearer-token file/env sources.
func trimOneTrailingNewline(s string) string {
	if strings.HasSuffix(s, "\n") {
		s = strings.TrimSuffix(s, "\n")
		s = strings.TrimSuffix(s, "\r")
	}
	return s
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

type serveSettings struct {
	addr                string
	allocationRulesPath string

	// noAuth is true only when --no-auth was passed: the sole way to serve
	// unauthenticated.
	noAuth bool
	// bearerToken is the resolved bearer token (from a file or env), non-empty
	// iff bearer mode is configured. serve hashes it via api.NewBearerAuth; it
	// is held here only long enough to build the AuthConfig.
	bearerToken string
	// trustedHeader is the resolved forward-auth identity header name, non-empty
	// iff forward-auth is configured (its presence enables the mode).
	trustedHeader string
	// trustedProxies is the resolved forward-auth trusted-peer allowlist.
	trustedProxies []netip.Prefix
	// authModeName is the access-log auth_mode label: "bearer", "forward-auth",
	// or "disabled" (--no-auth).
	authModeName string
}

// serveConfig parses serve's flags and resolves its environment-backed
// settings without opening the store or starting a listener. Allocation rules
// remain live-loaded per request; the warning only makes a known startup
// configuration problem visible without preventing serve from starting.
func serveConfig(args []string) (cfg serveSettings, warning string, stop bool, err error) {
	flags := flag.NewFlagSet("serve", flag.ContinueOnError)
	addrFlag := flags.String("addr", "", `listen address (overrides $COSTROID_ADDR; default "127.0.0.1:8080" — loopback. Pass a non-loopback address, e.g. 0.0.0.0:8080, to expose it on the network)`)
	allocationRulesFlag := flags.String("allocation-rules", "", allocationRulesFlagUsage)
	tokenFileFlag := flags.String("auth-token-file", "", "bearer auth: path to a file holding the API token (overrides $COSTROID_AUTH_TOKEN_FILE; preferred over the weaker $COSTROID_AUTH_TOKEN). There is no --auth-token value flag — argv is world-readable")
	trustedHeaderFlag := flags.String("auth-trusted-header", "", "forward-auth: the identity header your reverse proxy sets (overrides $COSTROID_AUTH_TRUSTED_HEADER; empty disables forward-auth; recommended value X-WEBAUTH-USER)")
	trustedProxiesFlag := flags.String("auth-trusted-proxies", "", "forward-auth: comma-separated trusted proxy CIDRs whose identity header is honored (overrides $COSTROID_AUTH_TRUSTED_PROXIES; default 127.0.0.0/8,::1/128; IPv4 prefixes broader than /8 and IPv6 broader than /16 are refused)")
	noAuthFlag := flags.Bool("no-auth", false, "serve WITHOUT authentication — the ONLY way to run unauthenticated (not recommended on a network-exposed address)")
	if stop, err = parseFlags(flags, args); stop || err != nil {
		return serveSettings{}, "", stop, err
	}

	cfg.addr = resolveAddr(*addrFlag, os.Getenv("COSTROID_ADDR"))

	bearerToken, err := resolveBearerToken(*tokenFileFlag)
	if err != nil {
		return serveSettings{}, "", false, err
	}
	trustedHeader, trustedProxies, err := resolveForwardAuth(*trustedHeaderFlag, *trustedProxiesFlag)
	if err != nil {
		return serveSettings{}, "", false, err
	}

	// Fail-closed: resolve exactly one mode, or refuse to start.
	bearerConfigured := bearerToken != ""
	forwardConfigured := trustedHeader != ""
	switch {
	case *noAuthFlag && (bearerConfigured || forwardConfigured):
		return serveSettings{}, "", false, errors.New("--no-auth cannot be combined with a configured auth mode: remove the bearer token (COSTROID_AUTH_TOKEN(_FILE)/--auth-token-file) or --auth-trusted-header, or drop --no-auth")
	case bearerConfigured && forwardConfigured:
		return serveSettings{}, "", false, errors.New("configure exactly one auth mode: bearer (COSTROID_AUTH_TOKEN(_FILE)/--auth-token-file) or forward-auth (--auth-trusted-header), not both")
	case !*noAuthFlag && !bearerConfigured && !forwardConfigured:
		return serveSettings{}, "", false, errors.New("no authentication configured: set COSTROID_AUTH_TOKEN(_FILE) for bearer auth, set --auth-trusted-header for forward-auth, or pass --no-auth to run without authentication (not recommended on a network-exposed address)")
	}

	cfg.noAuth = *noAuthFlag
	switch {
	case bearerConfigured:
		cfg.bearerToken = bearerToken
		cfg.authModeName = "bearer"
	case forwardConfigured:
		cfg.trustedHeader = trustedHeader
		cfg.trustedProxies = trustedProxies
		cfg.authModeName = "forward-auth"
	default:
		cfg.authModeName = "disabled" // --no-auth
	}

	cfg.allocationRulesPath = resolveAllocationRulesPath(*allocationRulesFlag)

	// Warnings ACCUMULATE (never clobber): the allocation-rules warning and the
	// --no-auth warning can co-occur, and the --no-auth warning must ALWAYS
	// surface — an allocation warning must not swallow it.
	var warnings []string
	if w := allocationWarning(cfg.allocationRulesPath); w != "" {
		warnings = append(warnings, w)
	}
	if cfg.noAuth {
		warnings = append(warnings, noAuthWarning(cfg.addr))
	}
	return cfg, strings.Join(warnings, "\n"), false, nil
}

// allocationWarning returns the startup warning for the resolved allocation
// rules path, or "" when the file is present and statable. Allocation rules are
// live-loaded per request, so a missing/inaccessible file is non-fatal — the
// warning only makes the misconfiguration visible at startup.
func allocationWarning(path string) string {
	if path == "" {
		return "no allocation rules path could be resolved — groupBy=allocation will return 400 as unconfigured"
	}
	switch _, statErr := os.Stat(path); {
	case statErr == nil:
		return ""
	case errors.Is(statErr, fs.ErrNotExist):
		return fmt.Sprintf("allocation rules file not found: %s — groupBy=allocation will return 400 until it exists", path)
	default:
		// Other stat errors (EACCES, ENOTDIR, …) are still non-fatal.
		return fmt.Sprintf("allocation rules file %s is not accessible: %v — groupBy=allocation will fail until it is fixed", path, statErr)
	}
}

// noAuthWarning is the loud --no-auth warning, escalated for a non-loopback
// bind. It always names WITHOUT AUTHENTICATION so operators cannot miss it.
func noAuthWarning(addr string) string {
	if !isLoopbackAddr(addr) {
		return fmt.Sprintf("WARNING: serving WITHOUT AUTHENTICATION on a network-exposed address (%s) — anyone who can reach it can read all billing data", addr)
	}
	return "WARNING: serving WITHOUT AUTHENTICATION — anyone who can reach this address can read all billing data"
}

// resolveBearerToken resolves the bearer token from a file (--auth-token-file >
// $COSTROID_AUTH_TOKEN_FILE, both preferred) or the weaker direct env value
// ($COSTROID_AUTH_TOKEN); it returns "" when bearer auth is unconfigured. A
// trailing newline is trimmed exactly once (the D32 stdin-secret rule, NOT
// TrimSpace). When an explicit FILE source is selected, a read failure is a
// config error naming the path — it never falls through to the env value.
func resolveBearerToken(fileFlag string) (string, error) {
	tokenFile := fileFlag
	if tokenFile == "" {
		tokenFile = os.Getenv(envAuthTokenFile)
	}
	if tokenFile != "" {
		data, err := os.ReadFile(tokenFile)
		if err != nil {
			return "", fmt.Errorf("reading bearer auth token file %s: %w", tokenFile, err)
		}
		token := trimOneTrailingNewline(string(data))
		if token == "" {
			return "", fmt.Errorf("bearer auth token file %s is empty", tokenFile)
		}
		return token, nil
	}
	if env := os.Getenv(envAuthToken); env != "" {
		token := trimOneTrailingNewline(env)
		if token == "" {
			return "", fmt.Errorf("%s is set but empty after trimming its trailing newline", envAuthToken)
		}
		return token, nil
	}
	return "", nil
}

// resolveForwardAuth resolves the forward-auth header name and trusted-proxy
// allowlist (flag > env, with the loopback default applied last). Forward-auth
// is enabled iff the resolved header name is non-empty. The CIDR set is parsed
// and validated whenever the mode is enabled OR the operator supplied any CIDRs
// — a bad or implausibly broad CIDR is a hard config error.
func resolveForwardAuth(headerFlag, proxiesFlag string) (header string, proxies []netip.Prefix, err error) {
	header = headerFlag
	if header == "" {
		header = os.Getenv(envAuthTrustedHeader)
	}
	proxiesRaw := proxiesFlag
	if proxiesRaw == "" {
		proxiesRaw = os.Getenv(envAuthTrustedProxies)
	}
	if header != "" || proxiesRaw != "" {
		src := proxiesRaw
		if src == "" {
			src = defaultTrustedProxies
		}
		proxies, err = parseTrustedProxies(src)
		if err != nil {
			return "", nil, err
		}
	}
	return header, proxies, nil
}

// parseTrustedProxies parses a comma-separated CIDR list into prefixes. A bad
// CIDR is a config error (never silently dropped); an implausibly broad CIDR
// (IPv4 shorter than /8 or IPv6 shorter than /16) is a HARD error — serve
// refuses (fail closed), because trusting a vast address range lets clients
// spoof the identity header (the Gitea CVE-2026-20896 class, §P5).
func parseTrustedProxies(s string) ([]netip.Prefix, error) {
	parts := strings.Split(s, ",")
	prefixes := make([]netip.Prefix, 0, len(parts))
	for _, part := range parts {
		p := strings.TrimSpace(part)
		if p == "" {
			continue
		}
		prefix, err := netip.ParsePrefix(p)
		if err != nil {
			return nil, fmt.Errorf("invalid --auth-trusted-proxies CIDR %q: %w", p, err)
		}
		minimumBits := 16
		if prefix.Addr().Is4() {
			minimumBits = 8
		}
		// WONTFIX: /16 is intentionally the broadest accepted IPv6 prefix. This
		// is a typo/misconfiguration tripwire, not a substitute for listing only
		// the reverse proxy's real address range as the error below requires.
		if prefix.Bits() < minimumBits {
			return nil, fmt.Errorf("--auth-trusted-proxies %q trusts an implausibly broad address range — refusing: any client in that range could spoof the trusted identity header; use IPv4 /8 or narrower, IPv6 /16 or narrower, and list only your reverse proxy's real address(es)", p)
		}
		prefixes = append(prefixes, prefix)
	}
	if len(prefixes) == 0 {
		return nil, fmt.Errorf("--auth-trusted-proxies %q lists no usable CIDR", s)
	}
	return prefixes, nil
}

// isLoopbackAddr reports whether a listen address binds only loopback (§P3). An
// empty host (":8080") and the unspecified addresses (0.0.0.0/::) are PUBLIC;
// 127.0.0.0/8 and ::1 and the literal "localhost" are loopback; a bare hostname
// or a specific routable IP is treated conservatively as public. Used only to
// escalate the --no-auth warning.
func isLoopbackAddr(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		host = addr
	}
	switch host {
	case "":
		return false // ":8080" binds every interface → public
	case "localhost":
		return true // ParseAddr fails on the literal name; special-case it
	}
	ip, err := netip.ParseAddr(host)
	if err != nil {
		return false // a bare hostname or unparseable host → treat as public
	}
	return ip.IsLoopback()
}

func serve(args []string) error {
	cfg, warning, stop, err := serveConfig(args)
	if stop || err != nil {
		return err
	}
	if warning != "" {
		fmt.Fprintln(os.Stderr, warning)
	}

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	store, err := storage.Open(ctx, dataDir())
	if err != nil {
		return err
	}
	defer func() { _ = store.Close() }()

	// Wire the handler as accessLog( auth( static+API ) ): the access log is
	// OUTERMOST and always on for serve; the auth middleware is installed only
	// when a mode is configured (--no-auth installs none).
	handler := api.NewHandler(version, webdist.FS(), store, cfg.allocationRulesPath, authOptions(cfg)...)
	handler = api.AccessLog(os.Stderr, cfg.authModeName)(handler)

	srv := &http.Server{
		Addr:    cfg.addr,
		Handler: handler,
		// No blanket ReadTimeout: large ingest request bodies must be
		// able to stream longer than any fixed limit.
		ReadHeaderTimeout: 5 * time.Second,
		IdleTimeout:       120 * time.Second,
	}

	errc := make(chan error, 1)
	go func() {
		fmt.Printf("costroid %s listening on %s\n", version, cfg.addr)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errc <- fmt.Errorf("serving HTTP on %s: %w", cfg.addr, err)
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

const demoAllocationRules = `{"dimensions":[{"name":"environment","rules":[{"label":"Production","match":[{"dimension":"tag:environment","operator":"exists"}]}]}]}`

type preparedDemo struct {
	store          *storage.DuckDB
	dataDir        string
	allocationPath string
	addr           string
	removeDataDir  bool
}

func (p *preparedDemo) close() {
	if p.store != nil {
		_ = p.store.Close()
	}
	if p.removeDataDir {
		_ = os.RemoveAll(p.dataDir)
	}
}

// prepareDemo owns the safety boundary before any listener starts: it resolves
// only demo-specific flags, refuses the resolved normal data directory,
// creates or validates an isolated empty directory, writes synthetic allocation
// rules there, opens that store directly, and seeds it without consulting auth,
// credential, or connector configuration.
func prepareDemo(ctx context.Context, args []string, asOf time.Time) (*preparedDemo, bool, error) {
	flags := flag.NewFlagSet("demo", flag.ContinueOnError)
	addrFlag := flags.String("addr", "", `listen address (overrides $COSTROID_ADDR; default "127.0.0.1:8080" — loopback. Pass a non-loopback address, e.g. 0.0.0.0:8080, to expose it on the network)`)
	dataDirFlag := flags.String("data-dir", "", "empty directory for the isolated synthetic store (default: fresh temporary directory)")
	if stop, err := parseFlags(flags, args); stop || err != nil {
		return nil, stop, err
	}

	prepared := &preparedDemo{addr: resolveAddr(*addrFlag, os.Getenv("COSTROID_ADDR"))}
	if *dataDirFlag == "" {
		dir, err := os.MkdirTemp("", "costroid-demo-")
		if err != nil {
			return nil, false, fmt.Errorf("creating isolated demo directory: %w", err)
		}
		prepared.dataDir = dir
		prepared.removeDataDir = true
	} else {
		prepared.dataDir = *dataDirFlag
		demoDirAbs, err := filepath.Abs(prepared.dataDir)
		if err != nil {
			return nil, false, fmt.Errorf("resolving demo data directory %s: %w", prepared.dataDir, err)
		}
		serveDirAbs, err := filepath.Abs(dataDir())
		if err != nil {
			return nil, false, fmt.Errorf("resolving serve data directory %s: %w", dataDir(), err)
		}
		if demoDirAbs == serveDirAbs {
			return nil, false, fmt.Errorf("refusing to seed the demo into the serve data directory %s", demoDirAbs)
		}
		entries, err := os.ReadDir(prepared.dataDir)
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return nil, false, fmt.Errorf("reading demo data directory %s: %w", prepared.dataDir, err)
		}
		if err == nil && len(entries) != 0 {
			return nil, false, fmt.Errorf("demo --data-dir %s is not empty; use an empty directory so the store stays synthetic-only", prepared.dataDir)
		}
	}

	fail := func(err error) (*preparedDemo, bool, error) {
		prepared.close()
		return nil, false, err
	}
	if err := os.MkdirAll(prepared.dataDir, 0o700); err != nil {
		return fail(fmt.Errorf("creating demo data directory %s: %w", prepared.dataDir, err))
	}
	prepared.allocationPath = filepath.Join(prepared.dataDir, "allocation.json")
	if err := os.WriteFile(prepared.allocationPath, []byte(demoAllocationRules+"\n"), 0o600); err != nil {
		return fail(fmt.Errorf("writing synthetic allocation rules: %w", err))
	}
	store, err := storage.Open(ctx, prepared.dataDir)
	if err != nil {
		return fail(err)
	}
	prepared.store = store
	if err := demodata.Seed(ctx, store, asOf, demodata.DefaultSeed); err != nil {
		return fail(fmt.Errorf("seeding demo data: %w", err))
	}
	return prepared, false, nil
}

func demo(args []string) error {
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	prepared, stop, err := prepareDemo(ctx, args, time.Now())
	if stop || err != nil {
		return err
	}
	defer prepared.close()

	handler := api.NewHandler(version, webdist.FS(), prepared.store, prepared.allocationPath, api.WithReadOnly(), api.WithDemo())
	handler = api.AccessLog(os.Stderr, "demo")(handler)
	srv := &http.Server{
		Addr:              prepared.addr,
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
		IdleTimeout:       120 * time.Second,
	}

	errCh := make(chan error, 1)
	go func() {
		fmt.Fprintln(os.Stderr, "DEMO MODE — synthetic data, read-only, not for production")
		fmt.Printf("costroid %s demo listening on %s\n", version, prepared.addr)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- fmt.Errorf("serving demo HTTP on %s: %w", prepared.addr, err)
			return
		}
		errCh <- nil
	}()

	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
	}
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer shutdownCancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		return fmt.Errorf("shutting down demo: %w", err)
	}
	return <-errCh
}

// authOptions translates the resolved serveSettings into the NewHandler auth
// option: a bearer or forward-auth AuthConfig, or nil for --no-auth (no auth
// middleware installed). The raw bearer token is hashed inside NewBearerAuth
// and never becomes a stored field.
func authOptions(cfg serveSettings) []api.HandlerOption {
	switch cfg.authModeName {
	case "bearer":
		return []api.HandlerOption{api.WithAuth(api.NewBearerAuth(cfg.bearerToken))}
	case "forward-auth":
		return []api.HandlerOption{api.WithAuth(api.NewForwardAuth(cfg.trustedHeader, cfg.trustedProxies))}
	default:
		return nil
	}
}

func ingestCmd(args []string) error {
	flags := flag.NewFlagSet("ingest", flag.ContinueOnError)
	connectorFlag := flags.String("connector", "", `connector name (available: "aws-focus", "aws-focus-s3", "azure-focus", "anthropic-cost", "openai-cost", "focus-csv")`)
	pathFlag := flags.String("path", "", "path to the export file to ingest (aws-focus, focus-csv)")
	bucketFlag := flags.String("bucket", "", "S3 bucket holding the AWS Data Export (aws-focus-s3)")
	accountURLFlag := flags.String("account-url", "", "Azure storage account blob endpoint, e.g. https://<account>.blob.core.windows.net/ (azure-focus)")
	containerFlag := flags.String("container", "", "Azure blob container holding the Cost Management export (azure-focus)")
	prefixFlag := flags.String("prefix", "", "export root prefix: the export's configured directory/prefix plus its name (aws-focus-s3, azure-focus)")
	periodFlag := flags.String("period", "", "ingest only this billing period, e.g. 2026-06 (aws-focus-s3, azure-focus, anthropic-cost, openai-cost, focus-csv; default: all discovered)")
	tenantFlag := flags.String("tenant", focus.DefaultTenant, "tenant identifier recorded on the ingested records")
	forceFlag := flags.Bool("force", false, "re-process every period even when unchanged (aws-focus-s3, azure-focus; a documented no-op for anthropic-cost/openai-cost/focus-csv, which keep no sync state)")
	focusVersionFlag := flags.String("focus-version", "", "declared FOCUS version of the export: 1.0, 1.0r2, 1.1, 1.2, 1.3, or 1.4 (focus-csv; REQUIRED, no sniffing; 1.0/1.1 accept spec-conformant exports only, 1.0r2 canonicalizes to 1.0)")
	sourceLabelFlag := flags.String("source-label", "", "logical source label for the per-month batch identity (focus-csv; default: the file's base name)")
	lenientFlag := flags.Bool("lenient", false, "focus-csv only, opt-in: tolerate UTC timestamp FORMAT variants "+
		"(missing seconds, space separator, 'UTC' suffix); still rejects zone-less timestamps, literal null tokens, and non-RFC3339 numbers")
	credentialFlag := flags.String("credential", "", "credential slot name holding the Admin API key (anthropic-cost, openai-cost; default: the connector name). "+
		"WARNING: an Anthropic Admin key is an unscopeable full-org-admin credential — the encrypted credential store carries the whole least-privilege burden (D32)")
	baseURLFlag := flags.String("base-url", "", "API base URL (anthropic-cost, openai-cost; default: the vendor's production endpoint)")
	sinceFlag := flags.String("since", "", "ingest calendar months from this one forward, YYYY-MM (anthropic-cost, openai-cost; default: the last 12 months)")
	keyFileFlag := flags.String("key-file", "", keyFileFlagUsage)
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
	case anthropiccost.Name:
		slot := firstNonEmpty(*credentialFlag, anthropiccost.Name)
		baseURL := firstNonEmpty(*baseURLFlag, anthropiccost.DefaultBaseURL)
		secret, store, err := openVaultSecret(ctx, *keyFileFlag, slot)
		if err != nil {
			return err
		}
		defer func() { _ = store.Close() }()
		periods, err := anthropiccost.Discover(ctx, aiHTTPClient(), baseURL, slot, secret, *sinceFlag, *periodFlag)
		if err != nil {
			return err
		}
		jobs := make([]ingestJob, 0, len(periods))
		for _, p := range periods {
			job := aiJob(p.Month, p.Conn, p.Err)
			if p.Conn != nil {
				if s := p.Conn.AnomalySummary(); s != "" {
					fmt.Printf("period %s: %s\n", p.Month, s)
				}
				// Stash the concrete connector's cost-orphaned usage metrics on
				// the job (non-nil, empty if none); runIngest writes them only
				// after this period's cost ingest succeeds. Mirrors how sync is
				// captured here and consumed later.
				job.usageMetrics = p.Conn.UsageMetrics()
			}
			jobs = append(jobs, job)
		}
		return runIngest(ctx, store, jobs, *tenantFlag)
	case openaicost.Name:
		slot := firstNonEmpty(*credentialFlag, openaicost.Name)
		baseURL := firstNonEmpty(*baseURLFlag, openaicost.DefaultBaseURL)
		secret, store, err := openVaultSecret(ctx, *keyFileFlag, slot)
		if err != nil {
			return err
		}
		defer func() { _ = store.Close() }()
		periods, err := openaicost.Discover(ctx, aiHTTPClient(), baseURL, slot, secret, *sinceFlag, *periodFlag)
		if err != nil {
			return err
		}
		jobs := make([]ingestJob, 0, len(periods))
		for _, p := range periods {
			job := aiJob(p.Month, p.Conn, p.Err)
			if p.Conn != nil {
				if s := p.Conn.AnomalySummary(); s != "" {
					fmt.Printf("period %s: %s\n", p.Month, s)
				}
				job.usageMetrics = p.Conn.UsageMetrics()
			}
			jobs = append(jobs, job)
		}
		return runIngest(ctx, store, jobs, *tenantFlag)
	case focuscsv.Name:
		if *pathFlag == "" {
			return errors.New("--path is required for the focus-csv connector")
		}
		// Discovery (version check, file read, header validation, per-month
		// split) runs BEFORE the store opens: a bad --focus-version or file
		// fails fast without taking the single-writer store lock. focus-csv
		// keeps no sync state, so --force is a documented no-op here (the
		// content-hash short-circuit still makes an unchanged re-import a
		// no-op). One import must carry the COMPLETE data for each month it
		// touches under a --source-label (a part-file replaces the month).
		periods, warnings, err := focuscsv.Discover(*pathFlag, focus.Version(*focusVersionFlag), *sourceLabelFlag, *lenientFlag)
		if err != nil {
			return err
		}
		for _, w := range warnings {
			fmt.Fprintln(os.Stderr, "costroid:", w)
		}
		store, err := storage.Open(ctx, dataDir())
		if err != nil {
			return err
		}
		defer func() { _ = store.Close() }()
		jobs, err := focusCSVJobs(periods, *periodFlag)
		if err != nil {
			return err
		}
		return runIngest(ctx, store, jobs, *tenantFlag)
	case "":
		return errors.New(`--connector is required (available: "aws-focus", "aws-focus-s3", "azure-focus", "anthropic-cost", "openai-cost", "focus-csv")`)
	default:
		return fmt.Errorf(`unknown connector %q (available: "aws-focus", "aws-focus-s3", "azure-focus", "anthropic-cost", "openai-cost", "focus-csv")`, *connectorFlag)
	}
}

// firstNonEmpty returns a if non-empty, else b.
func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

// aiHTTPClient is the HTTP client the AI-vendor connectors use: a per-request
// timeout, otherwise the stdlib default.
func aiHTTPClient() *http.Client {
	return &http.Client{Timeout: 60 * time.Second}
}

// aiJob builds one ingest job for an AI-vendor connector's discovered month.
// These connectors keep no sync state, so no tuple is upserted.
func aiJob(month string, conn ingest.Connector, discoveryErr error) ingestJob {
	if discoveryErr != nil {
		return ingestJob{period: month, discoveryErr: discoveryErr}
	}
	return ingestJob{period: month, conn: conn}
}

// openVaultSecret opens the store (taking the single-writer lock), opens the
// credential vault, and loads the named slot's secret — failing fast, BEFORE
// any network dial, if the key file or credential is missing. On success the
// caller owns closing the returned store.
func openVaultSecret(ctx context.Context, keyFileFlag, slot string) (credentials.Secret, *storage.DuckDB, error) {
	store, err := storage.Open(ctx, dataDir())
	if err != nil {
		return credentials.Secret{}, nil, err
	}
	keyPath, err := credentials.ResolveKeyPath(keyFileFlag)
	if err != nil {
		_ = store.Close()
		return credentials.Secret{}, nil, err
	}
	vault, err := credentials.Open(keyPath, store)
	if err != nil {
		_ = store.Close()
		return credentials.Secret{}, nil, err
	}
	secret, err := vault.Get(ctx, slot)
	if err != nil {
		_ = store.Close()
		return credentials.Secret{}, nil, err
	}
	return secret, store, nil
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
	// usageMetrics is the AI-vendor period's cost-orphaned usage metrics,
	// read from the concrete connector in the discovery loop and written
	// after ingest.Run succeeds (same identity as the cost batch). It is
	// NON-NIL for every AI job (empty when the month has no orphans) and nil
	// for non-AI connectors and discovery-error jobs — runIngest guards on
	// the field being non-nil, never on len>0, so a month whose orphans
	// vanished still clears its stale usage rows.
	usageMetrics []storage.Metric
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

// focusCSVJobs maps the discovered focus-csv months to jobs, filtered to one
// billing period when requested — the focus-csv twin of s3Jobs/azureJobs.
// focus-csv keeps no sync state, so its jobs carry no SyncState upsert.
func focusCSVJobs(periods []focuscsv.Period, period string) ([]ingestJob, error) {
	var jobs []ingestJob
	var available []string
	for _, p := range periods {
		available = append(available, p.Month)
		if period != "" && p.Month != period {
			continue
		}
		jobs = append(jobs, ingestJob{period: p.Month, conn: p.Conn})
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
		// Write the AI period's cost-orphaned usage metrics only AFTER ingest.Run
		// succeeded — the same identity as the cost batch, on every successful
		// outcome including the unchanged short-circuit, and even when the slice
		// is empty (so a month whose orphans vanished clears its stale rows). The
		// guard is on the field being non-nil (AI jobs only), never on len>0.
		if job.usageMetrics != nil {
			batch := storage.UsageBatch{
				Connector:      job.conn.Name(),
				SourceIdentity: job.conn.SourceIdentity(),
				TenantID:       tenant,
			}
			if err := store.ReplaceUsageBatch(ctx, batch, job.usageMetrics); err != nil {
				failed = append(failed, job.period)
				fmt.Fprintf(os.Stderr, "costroid: %sfailed recording usage metrics: %v\n", label, err)
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
// $COSTROID_ADDR, which wins over the default. The default binds LOOPBACK ONLY
// (127.0.0.1) — reaching a non-loopback interface requires the operator to set
// --addr/$COSTROID_ADDR explicitly, and that explicit choice is the public
// opt-in. The Go flag default is empty (see serveConfig) so "operator set it"
// stays distinguishable from "default applied".
func resolveAddr(flagAddr, envAddr string) string {
	if flagAddr != "" {
		return flagAddr
	}
	if envAddr != "" {
		return envAddr
	}
	return "127.0.0.1:8080"
}
