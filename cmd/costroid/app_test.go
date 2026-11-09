package main

import (
	"bytes"
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"html"
	"io"
	"io/fs"
	"maps"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Costroid/costroid/internal/scm"
	"github.com/Costroid/costroid/internal/scm/kmstest"
)

const (
	appWebhook      = "https://costroid.example/webhooks/github"
	appCode         = "a1b2c3d4e5f6a7b8c9d0"
	appWebhookValue = "the webhook secret GitHub generated for this test"
	appClientValue  = "the client secret GitHub generated for this test"
	appWeb          = "https://github.example"
)

var (
	appKeyOnce sync.Once
	appKey     *rsa.PrivateKey
	appKeyErr  error
)

// githubAppKey is the private key the stub GitHub issues, in PKCS #1 PEM as
// GitHub's are. It lives in memory only, so a file that holds it was written
// by the command.
func githubAppKey(t *testing.T) (*rsa.PrivateKey, []byte) {
	t.Helper()
	appKeyOnce.Do(func() { appKey, appKeyErr = rsa.GenerateKey(rand.Reader, 2048) })
	if appKeyErr != nil {
		t.Fatal(appKeyErr)
	}
	return appKey, pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(appKey)})
}

// keySigner signs locally, to read what AppTokens asks GitHub for.
type keySigner struct{ key *rsa.PrivateKey }

func (s keySigner) Sign(_ context.Context, digest [sha256.Size]byte) ([]byte, error) {
	return rsa.SignPKCS1v15(rand.Reader, s.key, crypto.SHA256, digest[:])
}

// tokenPermissions are the permissions AppTokens asks an installation token
// for.
func tokenPermissions(t *testing.T) map[string]string {
	t.Helper()
	key, _ := githubAppKey(t)
	var asked map[string]string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			Permissions map[string]string `json:"permissions"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
		}
		asked = request.Permissions
		_, _ = io.WriteString(w, `{"token":"t","expires_at":"2099-01-01T00:00:00Z"}`)
	}))
	defer server.Close()
	tokens := &scm.AppTokens{API: server.URL, AppID: "1", Installation: 1, Signer: keySigner{key}, Now: time.Now, Client: server.Client()}
	if _, err := tokens.Token(context.Background()); err != nil {
		t.Fatal(err)
	}
	return asked
}

func TestManifestNamesMinimumPermissions(t *testing.T) {
	code, stdout, stderr := execute(t, "app", "manifest", "--name", "Costroid for example", "--webhook-url", appWebhook)
	if code != 0 || stderr != "" {
		t.Fatalf("exit %d: %s", code, stderr)
	}
	var manifest struct {
		Name           string            `json:"name"`
		URL            string            `json:"url"`
		HookAttributes map[string]any    `json:"hook_attributes"`
		RedirectURL    *string           `json:"redirect_url"`
		Public         *bool             `json:"public"`
		Events         []string          `json:"default_events"`
		Permissions    map[string]string `json:"default_permissions"`
	}
	decoder := json.NewDecoder(strings.NewReader(stdout))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&manifest); err != nil {
		t.Fatalf("%v:\n%s", err, stdout)
	}
	want := map[string]string{"checks": "write", "pull_requests": "write", "contents": "read", "metadata": "read"}
	if !maps.Equal(manifest.Permissions, want) {
		t.Errorf("permissions = %v, want %v", manifest.Permissions, want)
	}
	if asked := tokenPermissions(t); !maps.Equal(manifest.Permissions, asked) {
		t.Errorf("the manifest grants %v, and an installation token asks for %v", manifest.Permissions, asked)
	}
	if !slices.Equal(manifest.Events, []string{"check_run"}) {
		t.Errorf("events = %v", manifest.Events)
	}
	if manifest.Public == nil || *manifest.Public || manifest.RedirectURL != nil {
		t.Errorf("public %v, redirect %v", manifest.Public, manifest.RedirectURL)
	}
	if manifest.Name != "Costroid for example" || manifest.URL == "" || manifest.HookAttributes["url"] != appWebhook || manifest.HookAttributes["active"] != true {
		t.Errorf("manifest = %+v", manifest)
	}
	doc, err := os.ReadFile(filepath.Join("..", "..", "docs", "github-app.md"))
	if err != nil {
		t.Fatal(err)
	}
	for name, level := range manifest.Permissions {
		if !regexp.MustCompile("(?m)^\\| `" + name + "` +\\| " + level + " +\\| \\S").Match(doc) {
			t.Errorf("docs/github-app.md does not justify %s: %s", name, level)
		}
	}
	if !regexp.MustCompile("(?m)^\\| `check_run` +\\| \\S").Match(doc) {
		t.Error("docs/github-app.md does not justify the check_run event")
	}
}

func TestAppRefusesFlagsItCannotUse(t *testing.T) {
	for name, args := range map[string][]string{
		"no name":          {"app", "manifest", "--webhook-url", appWebhook},
		"a long name":      {"app", "manifest", "--name", strings.Repeat("n", 35), "--webhook-url", appWebhook},
		"a plain webhook":  {"app", "manifest", "--name", "Costroid", "--webhook-url", "http://costroid.example/webhooks/github"},
		"no KMS key":       {"app", "create", "--name", "Costroid", "--webhook-url", appWebhook},
		"an org with a /":  {"app", "create", "--name", "Costroid", "--webhook-url", appWebhook, "--kms-key", "k", "--org", "a/b"},
		"an argument":      {"app", "manifest", "--name", "Costroid", "--webhook-url", appWebhook, "extra"},
		"another command":  {"app", "delete"},
		"manifest and org": {"app", "manifest", "--name", "Costroid", "--webhook-url", appWebhook, "--org", "example"},
	} {
		if code, stdout, stderr := execute(t, args...); code != exitFailure || stdout != "" || !strings.HasPrefix(stderr, "costroid: ") && !strings.Contains(stderr, "flag provided but not defined") {
			t.Errorf("%s: exit %d, stdout %q, stderr %q", name, code, stdout, stderr)
		}
	}
}

// githubConversions answers the manifest flow's conversion for appCode as
// GitHub's REST schema describes it, with the private key in pemText.
func githubConversions(t *testing.T, pemText []byte) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/app-manifests/"+appCode+"/conversions" || r.Header.Get("Authorization") != "" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.WriteHeader(http.StatusCreated)
		err := json.NewEncoder(w).Encode(map[string]any{
			"id": 987654, "slug": "costroid-for-example", "node_id": "A_kwHOexample", "name": "Costroid for example",
			"owner":       map[string]any{"login": "example", "id": 9, "type": "Organization"},
			"html_url":    "https://github.com/apps/costroid-for-example",
			"permissions": map[string]string{"checks": "write"}, "events": []string{"check_run"},
			"client_id": "Iv1.example", "client_secret": appClientValue, "webhook_secret": appWebhookValue, "pem": string(pemText),
		})
		if err != nil {
			t.Error(err)
		}
	}))
	t.Cleanup(server.Close)
	return server
}

// browse plays the browser: it opens the address create printed, reads the
// form GitHub would take, and comes back with the code for the form's state.
func browse(t *testing.T, client *http.Client, stdout *lockedBuffer) {
	t.Helper()
	address := regexp.MustCompile(`costroid: open (http://127\.0\.0\.1:[0-9]+)/ in a browser`)
	var origin string
	for deadline := time.Now().Add(10 * time.Second); origin == ""; time.Sleep(10 * time.Millisecond) {
		if match := address.FindStringSubmatch(stdout.String()); match != nil {
			origin = match[1]
		} else if time.Now().After(deadline) {
			t.Fatalf("no address was printed:\n%s", stdout.String())
		}
	}
	page := get(t, client, origin+"/", http.StatusOK)
	action := regexp.MustCompile(`action="([^"]+)"`).FindStringSubmatch(page)
	manifestValue := regexp.MustCompile(`name="manifest" value="([^"]+)"`).FindStringSubmatch(page)
	if action == nil || manifestValue == nil {
		t.Fatalf("the page holds no form:\n%s", page)
	}
	formAction := html.UnescapeString(action[1])
	state, found := strings.CutPrefix(formAction, appWeb+"/organizations/example/settings/apps/new?state=")
	if !found || len(state) != 64 {
		t.Fatalf("the form posts to %s", formAction)
	}
	var manifest struct {
		RedirectURL string            `json:"redirect_url"`
		Permissions map[string]string `json:"default_permissions"`
	}
	if err := json.Unmarshal([]byte(html.UnescapeString(manifestValue[1])), &manifest); err != nil {
		t.Fatal(err)
	}
	if manifest.RedirectURL != origin+"/callback" || len(manifest.Permissions) != 4 {
		t.Fatalf("manifest = %+v", manifest)
	}
	get(t, client, manifest.RedirectURL+"?code="+appCode+"&state="+strings.Repeat("0", 64), http.StatusBadRequest)
	get(t, client, manifest.RedirectURL+"?code="+appCode+"&state="+state, http.StatusOK)
}

func get(t *testing.T, client *http.Client, address string, status int) string {
	t.Helper()
	response, err := client.Get(address)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != status {
		t.Fatalf("GET %s answered %d, want %d: %s", address, response.StatusCode, status, body)
	}
	return string(body)
}

// keyForms are the forms of the private key a leak could take.
func keyForms(t *testing.T, key *rsa.PrivateKey, pemText []byte) [][]byte {
	t.Helper()
	pkcs8, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	forms := [][]byte{[]byte("PRIVATE KEY"), x509.MarshalPKCS1PrivateKey(key), pkcs8, key.D.Bytes()}
	for _, line := range bytes.Split(pemText, []byte("\n")) {
		if len(line) == 64 {
			forms = append(forms, line)
		}
	}
	return forms
}

// leaks names each form of the key that data holds.
func leaks(data []byte, forms [][]byte) []int {
	var found []int
	for i, form := range forms {
		if bytes.Contains(data, form) {
			found = append(found, i)
		}
	}
	return found
}

func TestAppCreateNeverWritesPEM(t *testing.T) {
	key, pemText := githubAppKey(t)
	work, home, temp := t.TempDir(), t.TempDir(), t.TempDir()
	t.Chdir(work)
	t.Setenv("HOME", home)
	t.Setenv("TMPDIR", temp)
	kms := kmstest.New(t)
	github := githubConversions(t, pemText)
	client := &http.Client{Timeout: 10 * time.Second}
	world := appWorld{web: appWeb, api: github.URL, client: client, getenv: kms.Getenv, listen: "127.0.0.1:0"}
	stdout, stderr := &lockedBuffer{}, &lockedBuffer{}
	exit := make(chan int, 1)
	go func() {
		exit <- appMain(context.Background(), []string{"create", "--name", "Costroid for example", "--webhook-url", appWebhook,
			"--kms-key", kmstest.ImportKey, "--org", "example"}, world, stdout, stderr)
	}()
	browse(t, client, stdout)
	if code := <-exit; code != 0 {
		t.Fatalf("exit %d:\n%s%s", code, stdout.String(), stderr.String())
	}
	if imported := kms.Key(kmstest.ImportKey); imported == nil || !imported.Equal(key) {
		t.Fatal("KMS does not hold the App's key")
	}
	forms := keyForms(t, key, pemText)
	for name, output := range map[string]string{"stdout": stdout.String(), "stderr": stderr.String()} {
		if found := leaks([]byte(output), forms); len(found) > 0 {
			t.Errorf("%s holds the key, forms %v", name, found)
		}
		for _, secret := range []string{appWebhookValue, appClientValue} {
			if strings.Contains(output, secret) {
				t.Errorf("%s holds %q", name, secret)
			}
		}
	}
	for _, call := range kms.Calls() {
		if found := leaks(call.Body, forms); len(found) > 0 {
			t.Errorf("KMS %s was sent the key in the clear, forms %v", call.Action, found)
		}
	}
	for _, root := range []string{work, home, temp} {
		err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
			if err != nil || entry.IsDir() {
				return err
			}
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			if found := leaks(data, forms); len(found) > 0 {
				t.Errorf("%s holds the key, forms %v", path, found)
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	for _, want := range []string{
		"costroid: created the GitHub App costroid-for-example, App id 987654\n",
		"costroid: imported its private key into the KMS key " + kmstest.ImportKey + ", and wrote no copy of it\n",
		"at " + appWeb + "/organizations/example/settings/apps/costroid-for-example, and give costroid serve",
		"costroid: give costroid serve COSTROID_GITHUB_APP_ID=987654 and COSTROID_GITHUB_APP_KMS_KEY=" + kmstest.ImportKey + "\n",
	} {
		if !strings.Contains(stdout.String(), want) {
			t.Errorf("stdout does not say %q:\n%s", want, stdout.String())
		}
	}
}

func TestAppCreateStopsBeforeGitHubWhenKMSRefuses(t *testing.T) {
	kms := kmstest.New(t)
	kms.Fail["GetParametersForImport"] = "AccessDeniedException"
	world := appWorld{web: appWeb, api: "https://api.github.example", client: &http.Client{}, getenv: kms.Getenv, listen: "127.0.0.1:0"}
	var stdout, stderr bytes.Buffer
	code := appMain(context.Background(), []string{"create", "--name", "Costroid", "--webhook-url", appWebhook, "--kms-key", kmstest.ImportKey}, world, &stdout, &stderr)
	want := "costroid: the KMS key cannot take the App's key, and no App was created: scm: KMS GetParametersForImport answered 400 AccessDeniedException\n"
	if code != exitFailure || stdout.String() != "" || stderr.String() != want {
		t.Errorf("exit %d, stdout %q, stderr %q", code, stdout.String(), stderr.String())
	}
}

func TestImportZeroesTheKey(t *testing.T) {
	key, pemText := githubAppKey(t)
	kms := kmstest.New(t)
	client, err := scm.KMSFromEnvironment(kms.Getenv, kmstest.ImportKey, kms.Server.Client(), time.Now)
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := client.PrepareImport(context.Background(), kmstest.ImportKey)
	if err != nil {
		t.Fatal(err)
	}
	for name, text := range map[string][]byte{"a key": bytes.Clone(pemText), "a block that is no key": []byte("-----BEGIN CERTIFICATE-----\nAAAA\n-----END CERTIFICATE-----\n")} {
		err := importPEM(context.Background(), prepared, text)
		if (err == nil) != (name == "a key") {
			t.Errorf("%s: %v", name, err)
		}
		if !bytes.Equal(text, make([]byte, len(text))) {
			t.Errorf("%s: the PEM was not zeroed", name)
		}
	}
	if imported := kms.Key(kmstest.ImportKey); imported == nil || !imported.Equal(key) {
		t.Error("KMS does not hold the key")
	}
}

func TestPKCS1IsWrappedAsX509WrapsIt(t *testing.T) {
	key, pemText := githubAppKey(t)
	got, err := pkcs8FromPEM(pemText)
	if err != nil {
		t.Fatal(err)
	}
	want, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Error("the PKCS #8 form differs from crypto/x509's")
	}
	pkcs8PEM := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: want})
	if got, err := pkcs8FromPEM(bytes.ReplaceAll(pkcs8PEM, []byte("\n"), []byte("\r\n"))); err != nil || !bytes.Equal(got, want) {
		t.Errorf("a PKCS #8 PEM with CRLF lines: %v", err)
	}
	for _, length := range []int{0x7f, 0x80, 0xff, 0x100, 0x10000} {
		encoded := derLength(length)
		value := 0
		if encoded[0] < 0x80 {
			value = int(encoded[0])
		} else {
			for _, digit := range encoded[1:] {
				value = value<<8 | int(digit)
			}
		}
		if value != length || length >= 0x80 && int(encoded[0]&0x7f) != len(encoded)-1 {
			t.Errorf("derLength(%d) = %x", length, encoded)
		}
	}
}

func TestPEMFieldReadsJSONStringEscapes(t *testing.T) {
	var field pemField
	if err := json.Unmarshal([]byte(`"-----BEGIN\n\/\"\\\u0041\t"`), &field); err != nil || string(field) != "-----BEGIN\n/\"\\A\t" {
		t.Errorf("%q, %v", field, err)
	}
	for _, text := range []string{`"caf\u00e9"`, `"\x"`, `42`, `"\u00"`} {
		if err := json.Unmarshal([]byte(text), &field); err == nil {
			t.Errorf("%s was read", text)
		}
	}
}
