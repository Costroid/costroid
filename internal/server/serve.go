package server

import (
	"cmp"
	"context"
	"encoding/base64"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/Costroid/costroid/internal/catalog"
	"github.com/Costroid/costroid/internal/egress"
	"github.com/Costroid/costroid/internal/oidc"
	"github.com/Costroid/costroid/internal/store"
)

const (
	envDatabase   = "COSTROID_DATABASE_URL"
	envMigration  = "COSTROID_MIGRATION_DATABASE_URL"
	envAudience   = "COSTROID_OIDC_AUDIENCE"
	envPseudonym  = "COSTROID_PSEUDONYM_KEY"
	envEgress     = "COSTROID_EGRESS_ALLOW"
	envGatewayEnv = "COSTROID_GATEWAY_ENVIRONMENT"

	defaultGatewayEnvironment = "costroid-gateway"

	minPseudonymKeyBytes = 32
	keySetTimeout        = 10 * time.Second
	egressFlushInterval  = 5 * time.Second
	shutdownTimeout      = 20 * time.Second
	exitFailure          = 125
)

// ServeUsage is serve's help page. costroid help serve prints it on stdout,
// and a flag serve cannot parse prints it on stderr.
const ServeUsage = `Usage:
  costroid serve [--addr :8080] [--gitlab-host HOST]... [--bind HOST:OWNER]...
                 [--egress-allow HOST]...

serve runs the ingest service. --addr is the address it listens on, :8080 by
default.

Environment:
  COSTROID_DATABASE_URL            the application role's connection
  COSTROID_MIGRATION_DATABASE_URL  the owner role's connection; when set,
                                   migrations run before the service starts
  COSTROID_OIDC_AUDIENCE           the audience CI tokens must name, never
                                   GitHub's default, https://github.com/<owner>
  COSTROID_PSEUDONYM_KEY           at least 32 bytes in base64, the master key
                                   for per-tenant address pseudonyms
  COSTROID_EGRESS_ALLOW            hosts for the egress allowlist,
                                   comma-separated
  COSTROID_GATEWAY_ENVIRONMENT     the GitHub environment whose jobs may attest
                                   a minted gateway key, costroid-gateway by
                                   default

--gitlab-host trusts the CI tokens of one more GitLab instance. GitHub Actions
and gitlab.com are trusted by default.

--bind gives an SCM owner to this instance's tenant, and needs the owner role.

--egress-allow adds one exact host to the egress allowlist, and adds to
COSTROID_EGRESS_ALLOW. The list starts empty, and an empty list refuses every
outbound call. OIDC ingest needs each issuer's host on it, the default issuers
included. Without one, a token from that issuer answers EGRESS_REFUSED. costroid
egress report prints what was attempted.

A row reads exact only when its SubAccountId is a gateway key that a job in
that environment attested as minted for the run the row's CI token names.
docs/serve.md says why.

Exit codes of serve:
  0                 the service stopped on a signal
  125               the service could not start, or stopped on an error
`

type serveOptions struct {
	addr        string
	gitlabHosts []string
	bindings    []store.Binding
	egressAllow []string
}

type repeated []string

func (r *repeated) String() string { return strings.Join(*r, ",") }

// Set appends one value.
func (r *repeated) Set(value string) error {
	*r = append(*r, value)
	return nil
}

func parseServe(args []string, stderr io.Writer) (serveOptions, error) {
	set := flag.NewFlagSet("serve", flag.ContinueOnError)
	set.SetOutput(stderr)
	set.Usage = func() { fmt.Fprint(stderr, ServeUsage) }
	options := serveOptions{}
	var gitlab, binds, allow repeated
	set.StringVar(&options.addr, "addr", ":8080", "the address to listen on")
	set.Var(&gitlab, "gitlab-host", "a GitLab host whose CI tokens are trusted")
	set.Var(&binds, "bind", "an SCM owner for this instance's tenant, host:owner")
	set.Var(&allow, "egress-allow", "an exact host the service may call")
	if err := set.Parse(args); err != nil {
		return serveOptions{}, err
	}
	options.gitlabHosts = gitlab
	for _, text := range binds {
		binding, err := parseBinding("--bind", text)
		if err != nil {
			return serveOptions{}, err
		}
		options.bindings = append(options.bindings, binding)
	}
	var err error
	options.egressAllow, err = egress.ParseList(allow, os.Getenv(envEgress))
	if err != nil {
		return serveOptions{}, err
	}
	return options, nil
}

func parseBinding(flagName, text string) (store.Binding, error) {
	host, owner, found := strings.Cut(text, ":")
	if !found || host == "" || owner == "" {
		return store.Binding{}, fmt.Errorf("%s %q is not host:owner", flagName, text)
	}
	return store.Binding{Host: host, Owner: owner}, nil
}

// Main runs costroid serve until ctx ends and returns an exit code.
func Main(ctx context.Context, args []string, stderr io.Writer) int {
	options, err := parseServe(args, stderr)
	if errors.Is(err, flag.ErrHelp) {
		return 0
	}
	if err == nil {
		err = run(ctx, options, stderr)
	}
	if err != nil {
		fmt.Fprintf(stderr, "costroid: %v\n", err)
		return exitFailure
	}
	return 0
}

func required(name string) (string, error) {
	value := os.Getenv(name)
	if value == "" {
		return "", fmt.Errorf("%s is not set", name)
	}
	return value, nil
}

func pseudonymKey() ([]byte, error) {
	text, err := required(envPseudonym)
	if err != nil {
		return nil, err
	}
	key, err := base64.StdEncoding.DecodeString(text)
	if err != nil {
		return nil, fmt.Errorf("%s is not base64", envPseudonym)
	}
	if len(key) < minPseudonymKeyBytes {
		return nil, fmt.Errorf("%s holds %d bytes, under %d", envPseudonym, len(key), minPseudonymKeyBytes)
	}
	return key, nil
}

func run(ctx context.Context, options serveOptions, stderr io.Writer) error {
	databaseURL, err := required(envDatabase)
	if err != nil {
		return err
	}
	audience, err := required(envAudience)
	if err != nil {
		return err
	}
	if err := ownAudience(audience); err != nil {
		return err
	}
	key, err := pseudonymKey()
	if err != nil {
		return err
	}
	pinned, err := catalog.Load()
	if err != nil {
		return err
	}
	ownerURL := os.Getenv(envMigration)
	if ownerURL != "" {
		if err := store.Migrate(ctx, ownerURL); err != nil {
			return err
		}
	}
	ledger, err := store.Open(ctx, databaseURL)
	if err != nil {
		return err
	}
	defer ledger.Close()
	if err := bindOwners(ctx, ledger, ownerURL, options.bindings); err != nil {
		return err
	}
	logger := slog.New(slog.NewJSONHandler(stderr, nil))
	counters := egress.NewCounters(ledger, time.Now, logger)
	gate := egress.New(options.egressAllow, counters)
	issuers := issuersOf(options)
	logAllowlist(logger, gate, issuers)
	config := Config{
		Ledger:             ledger,
		Verifier:           oidc.NewVerifier(audience, gate.Client(keySetTimeout), nil, issuers...),
		Catalog:            pinned,
		PseudonymKey:       key,
		GatewayEnvironment: cmp.Or(os.Getenv(envGatewayEnv), defaultGatewayEnvironment),
		Spend:              ledger,
		Logger:             logger,
	}
	service := New(config)
	background, stop := context.WithCancel(ctx)
	flushed := make(chan struct{})
	go func() {
		defer close(flushed)
		counters.Run(background, egressFlushInterval)
	}()
	err = listen(ctx, options.addr, service.Handler(), logger)
	stop()
	<-flushed
	return err
}

// ownAudience refuses GitHub's default audience, the URL of the repository's
// owner on github.com. A workflow that asks for a token without naming an
// audience gets that one, whatever service it meant the token for, so a token
// minted for any other service would pass here too.
func ownAudience(audience string) error {
	text := strings.ToLower(strings.TrimSpace(audience))
	if !strings.Contains(text, "://") {
		text = "https://" + text
	}
	parsed, err := url.Parse(text)
	if err == nil && (parsed.Hostname() == "github.com" || parsed.Hostname() == "www.github.com") {
		return fmt.Errorf("%s is GitHub's default audience, which a token for any other service also carries. Set an audience of this service's own, such as https://costroid.example", envAudience)
	}
	return nil
}

func issuersOf(options serveOptions) []oidc.Issuer {
	issuers := []oidc.Issuer{oidc.GitHub(), oidc.GitLab("gitlab.com")}
	for _, host := range options.gitlabHosts {
		issuers = append(issuers, oidc.GitLab(host))
	}
	return issuers
}

// logAllowlist states the list once, and names each trusted issuer whose keys
// it cannot reach, so a refusal at the first token is no surprise.
func logAllowlist(logger *slog.Logger, gate *egress.Gate, issuers []oidc.Issuer) {
	logger.Info("egress allowlist", "hosts", gate.Hosts())
	seen := make([]string, 0, len(issuers))
	for _, issuer := range issuers {
		host := issuer.KeySetHost()
		if gate.Allows(host) || slices.Contains(seen, host) {
			continue
		}
		seen = append(seen, host)
		logger.Warn("egress allowlist does not cover this issuer", "issuer", issuer.URL, "host", host, "add", egress.Flag)
	}
}

// bindOwners provisions the owners no tenant holds yet into one new tenant.
func bindOwners(ctx context.Context, ledger *store.Store, ownerURL string, bindings []store.Binding) error {
	var missing []store.Binding
	for _, binding := range bindings {
		_, err := ledger.TenantForOwner(ctx, binding.Host, binding.Owner)
		if errors.Is(err, store.ErrNotFound) {
			missing = append(missing, binding)
		} else if err != nil {
			return err
		}
	}
	if len(missing) == 0 {
		return nil
	}
	if ownerURL == "" {
		return fmt.Errorf("--bind needs %s", envMigration)
	}
	id, err := store.NewTenantID()
	if err != nil {
		return err
	}
	return store.Provision(ctx, ownerURL, store.Tenant{ID: id, Name: "self-hosted", Bindings: missing})
}

func listen(ctx context.Context, addr string, handler http.Handler, logger *slog.Logger) error {
	listener, err := (&net.ListenConfig{}).Listen(ctx, "tcp", addr)
	if err != nil {
		return fmt.Errorf("listen on %s: %w", addr, err)
	}
	server := &http.Server{
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       time.Minute,
		WriteTimeout:      time.Minute,
		IdleTimeout:       2 * time.Minute,
	}
	done := make(chan error, 1)
	go func() {
		<-ctx.Done()
		stop, cancel := context.WithTimeout(context.WithoutCancel(ctx), shutdownTimeout)
		defer cancel()
		done <- server.Shutdown(stop)
	}()
	logger.Info("listening", "addr", listener.Addr().String())
	if err := server.Serve(listener); !errors.Is(err, http.ErrServerClosed) {
		return fmt.Errorf("serve: %w", err)
	}
	return <-done
}
