package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"html/template"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/Costroid/costroid/internal/scm"
)

const appHelp = `Usage:
  costroid app manifest --name NAME --webhook-url URL
  costroid app create --name NAME --webhook-url URL --kms-key KEY [--org ORG]

app registers the GitHub App that costroid serve posts the approve button's
checks as. docs/github-app.md walks through it.

manifest prints the App's manifest as JSON: --name, the webhook at
--webhook-url, the check_run event and the four permissions an installation
token asks for, checks and pull_requests to write, contents and metadata to
read. The App is private to the account that creates it.

create runs GitHub's manifest flow. It prints an address on this machine to
open in a browser signed in to GitHub. That page sends the manifest to
GitHub, where you create the App, and GitHub sends a code back to the
address. create exchanges the code for the App and imports the App's private
key into the AWS KMS key --kms-key names: a key ID or ARN. The private key
reaches no file, log or terminal, and its bytes are zeroed after the import.
--org creates the App for an organization you administer; without it, the
App is your account's.

Create the KMS key first, with origin EXTERNAL, key spec RSA_2048 and key
usage SIGN_VERIFY. create reads AWS_ACCESS_KEY_ID, AWS_SECRET_ACCESS_KEY,
AWS_SESSION_TOKEN and AWS_REGION, as costroid serve does. It asks KMS for
the import's wrapping key before GitHub creates the App, so a key KMS will
not import into stops it early.

Exit codes of app:
  0                 done
  125               not done, and the line on stderr says why
`

const (
	githubWeb = "https://github.com"
	// appHomepage is the manifest's required url, the App's homepage.
	appHomepage = "https://github.com/Costroid/costroid"
	appEvent    = "check_run"
	// maxAppName is the longest name GitHub gives an App.
	maxAppName = 34
	// manifestFlowLimit is how long GitHub's code lasts: "You must complete
	// all three steps in the GitHub App Manifest flow within one hour."
	manifestFlowLimit = time.Hour
	appCallTimeout    = 30 * time.Second
	// answerLimit bounds the wait for the browser's last answer to be sent.
	answerLimit = 5 * time.Second
	// maxConversion bounds the conversion's answer, which holds the key.
	maxConversion = 64 << 10
	stateBytes    = 32
	callbackPath  = "/callback"
)

var (
	ownerPattern = regexp.MustCompile(`^[A-Za-z0-9](?:[A-Za-z0-9-]{0,38})$`)
	codePattern  = regexp.MustCompile(`^[A-Za-z0-9_-]{1,100}$`)
	// appPermissions are what AppTokens asks an installation token for.
	appPermissions = map[string]string{"checks": "write", "pull_requests": "write", "contents": "read", "metadata": "read"}
)

// appWorld is where app create reaches: GitHub's site and API, AWS through
// the environment, and the local address the browser comes back to.
type appWorld struct {
	web, api string
	client   *http.Client
	getenv   func(string) string
	listen   string
}

func appCommand(ctx context.Context, args []string, std stdio) int {
	client := &http.Client{
		Timeout:       appCallTimeout,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	world := appWorld{web: githubWeb, api: scm.DefaultAPI, client: client, getenv: os.Getenv, listen: "127.0.0.1:0"}
	return appMain(ctx, args, world, std.stdout, std.stderr)
}

func appMain(ctx context.Context, args []string, world appWorld, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprint(stderr, appHelp)
		return exitFailure
	}
	var err error
	switch args[0] {
	case "manifest":
		err = appManifest(args[1:], stdout, stderr)
	case "create":
		err = appCreate(ctx, args[1:], world, stdout, stderr)
	case "help", "-h", "-help", "--help":
		fmt.Fprint(stdout, appHelp)
		return 0
	default:
		err = fmt.Errorf("unknown app command %q", args[0])
	}
	if errors.Is(err, flag.ErrHelp) {
		return 0
	}
	if err != nil {
		fmt.Fprintf(stderr, "costroid: %v\n", err)
		return exitFailure
	}
	return 0
}

// appFlags are the flags both commands read.
type appFlags struct {
	name, webhook, kmsKey, org string
}

func parseApp(command string, args []string, stdout, stderr io.Writer) (appFlags, error) {
	set := flag.NewFlagSet("app "+command, flag.ContinueOnError)
	set.SetOutput(stderr)
	var flags appFlags
	set.StringVar(&flags.name, "name", "", "the App's name")
	set.StringVar(&flags.webhook, "webhook-url", "", "where GitHub delivers the App's events")
	if command == "create" {
		set.StringVar(&flags.kmsKey, "kms-key", "", "the KMS key to import the App's private key into")
		set.StringVar(&flags.org, "org", "", "the organization that owns the App")
	}
	if err := parseFlags(set, args, appHelp, stdout, stderr); err != nil {
		return appFlags{}, err
	}
	if set.NArg() > 0 {
		return appFlags{}, fmt.Errorf("app %s takes no arguments", command)
	}
	return flags, flags.check(command)
}

func (f appFlags) check(command string) error {
	if f.name == "" || len(f.name) > maxAppName {
		return fmt.Errorf("--name must hold 1 to %d characters", maxAppName)
	}
	webhook, err := url.Parse(f.webhook)
	if err != nil || webhook.Scheme != "https" || webhook.Host == "" {
		return errors.New("--webhook-url must be the https address of costroid serve's /webhooks/github")
	}
	if command == "create" && f.kmsKey == "" {
		return errors.New("--kms-key is needed: the KMS key ID or ARN to import the App's private key into")
	}
	if f.org != "" && !ownerPattern.MatchString(f.org) {
		return fmt.Errorf("--org %q is not a GitHub organization's name", f.org)
	}
	return nil
}

// appManifestJSON is GitHub's manifest for the App. redirect is where GitHub
// sends the code, and only create has one.
func appManifestJSON(flags appFlags, redirect string) ([]byte, error) {
	manifest := struct {
		Name           string            `json:"name"`
		URL            string            `json:"url"`
		HookAttributes map[string]any    `json:"hook_attributes"`
		RedirectURL    string            `json:"redirect_url,omitempty"`
		Public         bool              `json:"public"`
		Events         []string          `json:"default_events"`
		Permissions    map[string]string `json:"default_permissions"`
	}{
		Name: flags.name, URL: appHomepage, HookAttributes: map[string]any{"url": flags.webhook, "active": true},
		RedirectURL: redirect, Events: []string{appEvent}, Permissions: appPermissions,
	}
	return json.MarshalIndent(manifest, "", "  ")
}

func appManifest(args []string, stdout, stderr io.Writer) error {
	flags, err := parseApp("manifest", args, stdout, stderr)
	if err != nil {
		return err
	}
	manifest, err := appManifestJSON(flags, "")
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(stdout, "%s\n", manifest)
	return err
}

// createdApp is what GitHub's conversion answers, less its secrets. pem is a
// buffer of its own, which the caller zeroes.
type createdApp struct {
	ID    int64  `json:"id"`
	Slug  string `json:"slug"`
	Owner struct {
		Login string `json:"login"`
		Type  string `json:"type"`
	} `json:"owner"`
	PEM pemField `json:"pem"`
}

// settings is the App's settings page on GitHub.
func (a createdApp) settings(web string) string {
	if a.Owner.Type == "Organization" {
		return web + "/organizations/" + a.Owner.Login + "/settings/apps/" + a.Slug
	}
	return web + "/settings/apps/" + a.Slug
}

func appCreate(ctx context.Context, args []string, world appWorld, stdout, stderr io.Writer) error {
	flags, err := parseApp("create", args, stdout, stderr)
	if err != nil {
		return err
	}
	kms, err := scm.KMSFromEnvironment(world.getenv, flags.kmsKey, world.client, time.Now)
	if err != nil {
		return err
	}
	prepared, err := kms.PrepareImport(ctx, flags.kmsKey)
	if err != nil {
		return fmt.Errorf("the KMS key cannot take the App's key, and no App was created: %w", err)
	}
	code, err := manifestCode(ctx, flags, world, stdout)
	if err != nil {
		return err
	}
	app, err := convertManifest(ctx, world, code)
	if err != nil {
		return err
	}
	if err := importPEM(ctx, prepared, app.PEM); err != nil {
		return fmt.Errorf("GitHub created the App %s, and its private key was not imported: %w. No copy of the key was kept: delete the App at %s and run app create again", app.Slug, err, app.settings(world.web))
	}
	fmt.Fprintf(stdout, "costroid: created the GitHub App %s, App id %d\n", app.Slug, app.ID)
	fmt.Fprintf(stdout, "costroid: imported its private key into the KMS key %s, and wrote no copy of it\n", flags.kmsKey)
	fmt.Fprintf(stdout, "costroid: GitHub set a webhook secret this command does not show. Set a new one of at least 32 bytes at %s, and give costroid serve the same value as COSTROID_GITHUB_WEBHOOK_SECRET\n", app.settings(world.web))
	fmt.Fprintf(stdout, "costroid: give costroid serve COSTROID_GITHUB_APP_ID=%d and COSTROID_GITHUB_APP_KMS_KEY=%s\n", app.ID, flags.kmsKey)
	return nil
}

// importPEM imports the App's private key and zeroes every buffer that held
// it, the PEM included, whether or not the import succeeds.
func importPEM(ctx context.Context, prepared *scm.KeyImport, pem []byte) error {
	defer clear(pem)
	material, err := pkcs8FromPEM(pem)
	if err != nil {
		return err
	}
	defer clear(material)
	return prepared.Import(ctx, material)
}

// startPage sends the manifest to GitHub when the person presses its button.
var startPage = template.Must(template.New("start").Parse(`<!doctype html>
<html lang="en">
<meta charset="utf-8">
<title>Create the GitHub App {{.Name}}</title>
<form method="post" action="{{.Action}}">
<input type="hidden" name="manifest" value="{{.Manifest}}">
<p>This page sends the App's manifest to GitHub, where you create the App.</p>
<button type="submit">Create {{.Name}} on GitHub</button>
</form>
</html>
`))

// manifestCode serves the start page on a loopback address until GitHub
// sends the browser back with the code for this run's state.
func manifestCode(ctx context.Context, flags appFlags, world appWorld, stdout io.Writer) (string, error) {
	state, err := randomState()
	if err != nil {
		return "", err
	}
	listener, err := (&net.ListenConfig{}).Listen(ctx, "tcp", world.listen)
	if err != nil {
		return "", fmt.Errorf("listen for GitHub's answer: %w", err)
	}
	defer listener.Close()
	origin := "http://" + listener.Addr().String()
	manifest, err := appManifestJSON(flags, origin+callbackPath)
	if err != nil {
		return "", err
	}
	action := world.web + "/settings/apps/new?state=" + state
	if flags.org != "" {
		action = world.web + "/organizations/" + flags.org + "/settings/apps/new?state=" + state
	}
	codes := make(chan string, 1)
	flow := &manifestFlow{host: listener.Addr().String(), web: world.web, state: state, action: action, name: flags.name, manifest: string(manifest), codes: codes}
	server := &http.Server{Handler: flow, ReadHeaderTimeout: 10 * time.Second}
	go func() { _ = server.Serve(listener) }()
	defer closeAfterAnswer(server)
	fmt.Fprintf(stdout, "costroid: open %s/ in a browser signed in to GitHub, and create the App there\n", origin)
	wait, cancel := context.WithTimeout(ctx, manifestFlowLimit)
	defer cancel()
	select {
	case code := <-codes:
		return code, nil
	case <-wait.Done():
		return "", fmt.Errorf("GitHub sent no code back: %w", wait.Err())
	}
}

// closeAfterAnswer closes the server once the browser has its last answer.
// Closing at once could cut the callback's page short, and a browser that
// retries the request then finds nothing listening.
func closeAfterAnswer(server *http.Server) {
	ctx, cancel := context.WithTimeout(context.Background(), answerLimit)
	defer cancel()
	if err := server.Shutdown(ctx); err != nil {
		_ = server.Close()
	}
}

func randomState() (string, error) {
	state := make([]byte, stateBytes)
	if _, err := rand.Read(state); err != nil {
		return "", fmt.Errorf("draw the state: %w", err)
	}
	return hex.EncodeToString(state), nil
}

// manifestFlow serves the start page and takes the code for its state. It
// answers only requests addressed to host, so a page that rebinds a name of
// its own to this address reads nothing.
type manifestFlow struct {
	host, web, state, action, name, manifest string
	codes                                    chan<- string
}

func (f *manifestFlow) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Host != f.host || r.Method != http.MethodGet {
		http.Error(w, "Not here.", http.StatusMisdirectedRequest)
		return
	}
	switch r.URL.Path {
	case "/":
		w.Header().Set("Content-Security-Policy", "default-src 'none'; form-action "+f.web)
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_ = startPage.Execute(w, map[string]string{"Name": f.name, "Action": f.action, "Manifest": f.manifest})
	case callbackPath:
		f.callback(w, r)
	default:
		http.NotFound(w, r)
	}
}

// callback takes GitHub's code when the state is this run's.
func (f *manifestFlow) callback(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()
	code := query.Get("code")
	if subtle.ConstantTimeCompare([]byte(query.Get("state")), []byte(f.state)) != 1 || !codePattern.MatchString(code) {
		http.Error(w, "This answer is not for this run of costroid app create.", http.StatusBadRequest)
		return
	}
	select {
	case f.codes <- code:
	default:
	}
	fmt.Fprintln(w, "GitHub created the App. costroid app create goes on in the terminal.")
}

// convertManifest exchanges the code for the App. The answer is read into one
// buffer, which is zeroed before this returns; the PEM is copied out into a
// buffer of its own.
func convertManifest(ctx context.Context, world appWorld, code string) (createdApp, error) {
	path := "/app-manifests/" + code + "/conversions"
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(world.api, "/")+path, nil)
	if err != nil {
		return createdApp{}, fmt.Errorf("build POST %s: %w", path, err)
	}
	request.Header.Set("Accept", "application/vnd.github+json")
	request.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	request.Header.Set("User-Agent", "costroid")
	response, err := world.client.Do(request)
	if err != nil {
		return createdApp{}, fmt.Errorf("POST /app-manifests/{code}/conversions: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusCreated {
		return createdApp{}, fmt.Errorf("GitHub answered %d to POST /app-manifests/{code}/conversions", response.StatusCode)
	}
	buffer := make([]byte, maxConversion)
	defer clear(buffer)
	read, err := io.ReadFull(response.Body, buffer)
	switch {
	case err == nil:
		return createdApp{}, fmt.Errorf("GitHub's answer passed %d bytes", maxConversion)
	case !errors.Is(err, io.ErrUnexpectedEOF):
		return createdApp{}, fmt.Errorf("read GitHub's answer: %w", err)
	}
	var app createdApp
	if err := json.Unmarshal(buffer[:read], &app); err != nil {
		clear(app.PEM)
		return createdApp{}, fmt.Errorf("read GitHub's answer: %w", err)
	}
	if app.ID <= 0 || app.Slug == "" || len(app.PEM) == 0 {
		clear(app.PEM)
		return createdApp{}, errors.New("GitHub's answer holds no App id, slug or private key")
	}
	return app, nil
}

// pemField is a JSON string read into bytes of its own, which can be zeroed
// as a Go string cannot.
type pemField []byte

// UnmarshalJSON reads the string's escapes. A private key's PEM is ASCII, so
// a \u escape past it is refused.
func (p *pemField) UnmarshalJSON(data []byte) error {
	if len(data) < 2 || data[0] != '"' || data[len(data)-1] != '"' {
		return errors.New("the private key is not a string")
	}
	out := make([]byte, 0, len(data))
	escapes := map[byte]byte{'"': '"', '\\': '\\', '/': '/', 'b': '\b', 'f': '\f', 'n': '\n', 'r': '\r', 't': '\t'}
	for i := 1; i < len(data)-1; i++ {
		if data[i] != '\\' {
			out = append(out, data[i])
			continue
		}
		i++
		if plain, ok := escapes[data[i]]; ok {
			out = append(out, plain)
			continue
		}
		if data[i] != 'u' || i+4 >= len(data) {
			clear(out)
			return errors.New("the private key holds an escape it cannot")
		}
		value, err := strconv.ParseUint(string(data[i+1:i+5]), 16, 8)
		if err != nil || value >= 0x80 {
			clear(out)
			return errors.New("the private key is not ASCII")
		}
		out = append(out, byte(value))
		i += 4
	}
	*p = out
	return nil
}

// pkcs8FromPEM reads an RSA private key's PEM into PKCS #8 DER, the form KMS
// imports, in a buffer of its own. GitHub issues PKCS #1, which this wraps in
// a PrivateKeyInfo, RFC 5208.
func pkcs8FromPEM(text []byte) ([]byte, error) {
	der, kind, err := decodePEM(text)
	if err != nil {
		return nil, err
	}
	switch kind {
	case "PRIVATE KEY":
		return der, nil
	case "RSA PRIVATE KEY":
		defer clear(der)
		return wrapPKCS1(der), nil
	}
	clear(der)
	return nil, fmt.Errorf("the App's key is a %s, not an RSA private key", kind)
}

// decodePEM reads one PEM block line by line into a buffer of its own, so no
// copy of the key is left that this program cannot zero.
func decodePEM(text []byte) ([]byte, string, error) {
	lines := bytes.Split(bytes.TrimSpace(text), []byte("\n"))
	for i := range lines {
		lines[i] = bytes.TrimRight(lines[i], "\r")
	}
	invalid := errors.New("the App's private key is not one PEM block")
	if len(lines) < 3 {
		return nil, "", invalid
	}
	kind, found := bytes.CutPrefix(lines[0], []byte("-----BEGIN "))
	kind, ended := bytes.CutSuffix(kind, []byte("-----"))
	if !found || !ended || string(lines[len(lines)-1]) != "-----END "+string(kind)+"-----" {
		return nil, "", invalid
	}
	body := lines[1 : len(lines)-1]
	size := 0
	for _, line := range body {
		size += len(line)
	}
	der := make([]byte, base64.StdEncoding.DecodedLen(size))
	written := 0
	for _, line := range body {
		n, err := base64.StdEncoding.Decode(der[written:], line)
		if err != nil {
			clear(der)
			return nil, "", invalid
		}
		written += n
	}
	return der[:written], string(kind), nil
}

// rsaEncryption is the AlgorithmIdentifier of an RSA key: the OID
// 1.2.840.113549.1.1.1 and NULL parameters.
var rsaEncryption = []byte{0x30, 0x0d, 0x06, 0x09, 0x2a, 0x86, 0x48, 0x86, 0xf7, 0x0d, 0x01, 0x01, 0x01, 0x05, 0x00}

// wrapPKCS1 returns PrivateKeyInfo{version 0, rsaEncryption, OCTET STRING
// key} in DER.
func wrapPKCS1(key []byte) []byte {
	octets := derLength(len(key))
	inner := 3 + len(rsaEncryption) + 1 + len(octets) + len(key)
	outer := derLength(inner)
	out := make([]byte, 0, 1+len(outer)+inner)
	out = append(append(out, 0x30), outer...)
	out = append(out, 0x02, 0x01, 0x00)
	out = append(out, rsaEncryption...)
	out = append(append(out, 0x04), octets...)
	return append(out, key...)
}

// derLength is a DER length octet run.
func derLength(n int) []byte {
	if n < 0x80 {
		return []byte{byte(n)}
	}
	var digits []byte
	for value := n; value > 0; value >>= 8 {
		digits = append([]byte{byte(value)}, digits...)
	}
	return append([]byte{0x80 | byte(len(digits))}, digits...)
}
