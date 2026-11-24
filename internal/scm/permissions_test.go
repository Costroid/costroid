package scm_test

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Costroid/costroid/internal/docstest"
	"github.com/Costroid/costroid/internal/gateway"
	"github.com/Costroid/costroid/internal/scm"
)

// The hosts a walk sends to. No request leaves the process: the recorder
// answers each one.
const (
	walkGitHub    = "https://api.github.test"
	walkGitLab    = "https://gitlab.test/api/v4"
	walkOIDC      = "https://token.actions.test/idtoken"
	walkCommit    = "9f2c6e1ab47d03f58c1e7726d0b4a93e5f618c2d"
	walkMarker    = "<!-- costroid -->"
	repositoryDir = "../.."
)

// sent is one request a walk sent, by the method or function that sent it.
type sent struct {
	by, method, base, path, body string
}

// walker runs each walk through a recording transport. Each request is kept
// under the name of the walk that sent it and answered by answer.
type walker struct {
	t      *testing.T
	by     string
	answer func(*http.Request) string
	sent   []sent
}

type roundTrip func(*http.Request) (*http.Response, error)

func (f roundTrip) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}

// client answers every request through the walker. A request to a host no
// walk names fails the test, so no walk reaches past the recorder.
func (w *walker) client() *http.Client {
	return &http.Client{Transport: roundTrip(func(request *http.Request) (*http.Response, error) {
		var body []byte
		if request.Body != nil {
			read, err := io.ReadAll(request.Body)
			if err != nil {
				return nil, err
			}
			body = read
		}
		base, path := w.split(request.URL)
		w.sent = append(w.sent, sent{w.by, request.Method, base, path, string(body)})
		return &http.Response{
			StatusCode: http.StatusOK, Header: http.Header{}, Request: request,
			Body: io.NopCloser(strings.NewReader(w.answer(request))),
		}, nil
	})}
}

// split names the base a request was sent to and the path after it.
func (w *walker) split(address *url.URL) (string, string) {
	root := address.Scheme + "://" + address.Host
	full := root + address.EscapedPath()
	for _, base := range []struct{ name, prefix string }{{"api", walkGitHub}, {"api", walkGitLab}} {
		if path, found := strings.CutPrefix(full, base.prefix); found && (path == "" || strings.HasPrefix(path, "/")) {
			return base.name, path
		}
	}
	if full == walkOIDC {
		return "ACTIONS_ID_TOKEN_REQUEST_URL", ""
	}
	w.t.Errorf("the walk of %s reached %s, which no walk serves", w.by, full)
	return "", full
}

// answering sets the body every request answers with until the next call.
func (w *walker) answering(body string) {
	w.answer = func(*http.Request) string { return body }
}

func (w *walker) github() *scm.GitHub {
	host, err := scm.NewGitHub(scm.GitHubOptions{API: walkGitHub, Repository: "example/widgets", Tokens: scm.StaticToken("token"), Client: w.client()})
	if err != nil {
		w.t.Fatal(err)
	}
	return host
}

func (w *walker) gitlab() *scm.GitLab {
	host, err := scm.NewGitLab(scm.GitLabOptions{API: walkGitLab, Project: "42", Tokens: scm.StaticToken("token"), Client: w.client()})
	if err != nil {
		w.t.Fatal(err)
	}
	return host
}

type fixedSigner struct{}

func (fixedSigner) Sign(context.Context, [sha256.Size]byte) ([]byte, error) {
	return []byte("signature"), nil
}

// walks holds a walk for each method or function, keyed as its package names
// it. A walk sends every request its method can send: where the method sends
// one request or another on the host's answer, the walk runs it once for each. The answers
// hold only enough for each request to be sent, so what a method returns is
// not this test's to judge.
func walks() map[string]func(context.Context, *walker) {
	gh := func(answer string, call func(context.Context, *scm.GitHub)) func(context.Context, *walker) {
		return func(ctx context.Context, w *walker) {
			w.answering(answer)
			call(ctx, w.github())
		}
	}
	checkRuns := `{"total_count":0,"check_runs":[]}`
	own := `[{"id":5,"body":"` + walkMarker + `","user":{"login":"github-actions[bot]"}}]`
	return map[string]func(context.Context, *walker){
		"scm.GitHub.PostCheck": gh(`{"id":1}`, func(ctx context.Context, g *scm.GitHub) {
			_, _ = g.PostCheck(ctx, scm.CheckRun{Name: "costroid", HeadSHA: walkCommit, Completed: time.Unix(0, 0)})
		}),
		"scm.GitHub.UpdateCheck": gh(`{}`, func(ctx context.Context, g *scm.GitHub) { _ = g.UpdateCheck(ctx, 1, scm.CheckRun{}) }),
		"scm.GitHub.UpsertComment": func(ctx context.Context, w *walker) {
			for _, comments := range []string{`[]`, own} {
				w.answering(comments)
				_ = w.github().UpsertComment(ctx, 3, walkMarker, "body")
			}
		},
		"scm.GitHub.HasComment": gh(`[]`, func(ctx context.Context, g *scm.GitHub) { _, _ = g.HasComment(ctx, 3, walkMarker) }),
		"scm.GitHub.AuthorComments": gh(`[]`, func(ctx context.Context, g *scm.GitHub) {
			_, _ = g.AuthorComments(ctx, 3, "octocat", walkMarker)
		}),
		"scm.GitHub.PullOrigin": gh(`{}`, func(ctx context.Context, g *scm.GitHub) { _, _ = g.PullOrigin(ctx, 3) }),
		"scm.GitHub.FirstCommit": gh(`[]`, func(ctx context.Context, g *scm.GitHub) {
			_, _ = g.FirstCommit(ctx, ".github/workflows/costroid.yml", "main")
		}),
		"scm.GitHub.BranchHead":    gh(`{}`, func(ctx context.Context, g *scm.GitHub) { _, _ = g.BranchHead(ctx, "release/1") }),
		"scm.GitHub.OpenPullsFrom": gh(`[]`, func(ctx context.Context, g *scm.GitHub) { _, _ = g.OpenPullsFrom(ctx, "octocat", "feature") }),
		"scm.GitHub.MergedPullsAt": gh(`[]`, func(ctx context.Context, g *scm.GitHub) { _, _ = g.MergedPullsAt(ctx, walkCommit) }),
		"scm.GitHub.BranchesAt":    gh(`[]`, func(ctx context.Context, g *scm.GitHub) { _, _ = g.BranchesAt(ctx, walkCommit) }),
		"scm.GitHub.ReadFile":      gh(`{}`, func(ctx context.Context, g *scm.GitHub) { _, _ = g.ReadFile(ctx, "policy/costroid.rego", "main") }),
		"scm.GitHub.Visibility":    gh(`{}`, func(ctx context.Context, g *scm.GitHub) { _, _ = g.Visibility(ctx) }),
		"scm.GitHub.PullRequest":   gh(`{}`, func(ctx context.Context, g *scm.GitHub) { _, _ = g.PullRequest(ctx, 3) }),
		"scm.GitHub.CanWrite":      gh(`{}`, func(ctx context.Context, g *scm.GitHub) { _, _ = g.CanWrite(ctx, "octocat") }),
		"scm.GitHub.PullCommits":   gh(`[]`, func(ctx context.Context, g *scm.GitHub) { _, _, _ = g.PullCommits(ctx, 3) }),
		"scm.GitHub.CheckRuns":     gh(checkRuns, func(ctx context.Context, g *scm.GitHub) { _, _ = g.CheckRuns(ctx, walkCommit, "costroid") }),
		"scm.GitHub.HeadCheckRuns": gh(checkRuns, func(ctx context.Context, g *scm.GitHub) { _, _ = g.HeadCheckRuns(ctx, walkCommit, "costroid") }),
		"scm.GitHub.MergedInto":    gh(`[]`, func(ctx context.Context, g *scm.GitHub) { _, _ = g.MergedInto(ctx, "main", 5) }),
		"scm.GitHub.Reviews":       gh(`[]`, func(ctx context.Context, g *scm.GitHub) { _, _ = g.Reviews(ctx, 3) }),

		"scm.GitLab.PostCheck":   func(ctx context.Context, w *walker) { _, _ = w.gitlab().PostCheck(ctx, scm.CheckRun{}) },
		"scm.GitLab.UpdateCheck": func(ctx context.Context, w *walker) { _ = w.gitlab().UpdateCheck(ctx, 1, scm.CheckRun{}) },
		"scm.GitLab.UpsertComment": func(ctx context.Context, w *walker) {
			for _, notes := range []string{`[]`, `[{"id":9,"body":"` + walkMarker + `","author":{"id":7}}]`} {
				w.answer = func(request *http.Request) string {
					if strings.HasSuffix(request.URL.Path, "/user") {
						return `{"id":7}`
					}
					return notes
				}
				_ = w.gitlab().UpsertComment(ctx, 3, walkMarker, "body")
			}
		},
		"scm.GitLab.BranchHead": func(ctx context.Context, w *walker) {
			w.answering(`{}`)
			_, _ = w.gitlab().BranchHead(ctx, "release/1")
		},
		"scm.GitLab.ReadFile": func(ctx context.Context, w *walker) {
			w.answering(`{}`)
			_, _ = w.gitlab().ReadFile(ctx, "policy/costroid.rego", "main")
		},
		"scm.GitLab.Visibility": func(ctx context.Context, w *walker) {
			w.answering(`{}`)
			_, _ = w.gitlab().Visibility(ctx)
		},

		"scm.AppTokens.Token": func(ctx context.Context, w *walker) {
			w.answering(`{"token":"installation","expires_at":"2030-01-01T00:00:00Z"}`)
			tokens := &scm.AppTokens{API: walkGitHub, AppID: "1", Installation: 9, Signer: fixedSigner{}, Now: time.Now, Client: w.client()}
			_, _ = tokens.Token(ctx)
		},

		"gateway.Artifacts.Wait": func(ctx context.Context, w *walker) {
			download := walkGitHub + "/repos/example/widgets/actions/artifacts/11/zip"
			for _, listed := range []string{`{"artifacts":[]}`, `{"artifacts":[{"archive_download_url":"` + download + `"}]}`} {
				w.answer = func(request *http.Request) string {
					if strings.HasSuffix(request.URL.Path, "/artifacts") {
						return listed
					}
					return `{"status":"completed"}`
				}
				artifacts := gateway.Artifacts{API: walkGitHub, Repository: "example/widgets", Run: "12", Token: "token", HTTP: w.client()}
				_, _ = artifacts.Wait(ctx, "costroid-request-1")
			}
		},
		"gateway.ActionsToken": func(ctx context.Context, w *walker) {
			w.answering(`{"value":"oidc"}`)
			variables := map[string]string{"ACTIONS_ID_TOKEN_REQUEST_URL": walkOIDC + "?api-version=2.0", "ACTIONS_ID_TOKEN_REQUEST_TOKEN": "request"}
			_, _ = gateway.ActionsToken(ctx, w.client(), func(name string) string { return variables[name] }, "audience")
		},
	}
}

// walkedTypes are the types whose every exported method must have a walk.
// gateway.ActionsToken is a function, so its walk is named on its own.
var walkedTypes = []reflect.Type{
	reflect.TypeFor[*scm.GitHub](),
	reflect.TypeFor[*scm.GitLab](),
	reflect.TypeFor[*scm.AppTokens](),
	reflect.TypeFor[gateway.Artifacts](),
}

var walkedFunctions = []string{"gateway.ActionsToken"}

// methodNames names each exported method of the walked types as walks keys
// them.
func methodNames() []string {
	names := slices.Clone(walkedFunctions)
	for _, typ := range walkedTypes {
		owner := strings.TrimPrefix(typ.String(), "*")
		for index := range typ.NumMethod() {
			names = append(names, owner+"."+typ.Method(index).Name)
		}
	}
	return names
}

// ghAPI is a gh api command in a recipe's script, with its flags and its
// path.
var ghAPI = regexp.MustCompile(`gh api([^"\n]*)"([^"]+)"`)

// shellVariable is $NAME or ${NAME} in a script.
var shellVariable = regexp.MustCompile(`\$\{?[A-Za-z_][A-Za-z0-9_]*\}?`)

// recipeCalls reads every gh api command the recipes run, as the request it
// sends: GET unless its flags name another method.
func recipeCalls(t *testing.T) []sent {
	t.Helper()
	files, err := filepath.Glob(filepath.Join(repositoryDir, "recipes", "*.yml"))
	if err != nil || len(files) == 0 {
		t.Fatalf("no recipes: %v", err)
	}
	method := regexp.MustCompile(`(?:--method|-X)[ =]([A-Z]+)`)
	var calls []sent
	for _, file := range files {
		text, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		for _, found := range ghAPI.FindAllStringSubmatch(string(text), -1) {
			verb := http.MethodGet
			if named := method.FindStringSubmatch(found[1]); named != nil {
				verb = named[1]
			}
			path, _, _ := strings.Cut(found[2], "?")
			path = strings.ReplaceAll(path, "$GITHUB_REPOSITORY", "example/widgets")
			path = "/" + shellVariable.ReplaceAllString(path, "11")
			calls = append(calls, sent{"gh api", verb, "api", path, ""})
		}
	}
	if len(calls) == 0 {
		t.Fatal("no recipe runs gh api, so this walk reads nothing")
	}
	return calls
}

// curlToTokenURL is a curl command that ends at the job's OIDC request URL.
var curlToTokenURL = regexp.MustCompile(`(?s)curl"?((?:[^"]|"[^$])*?)"\$ACTIONS_ID_TOKEN_REQUEST_URL"`)

// scriptCalls reads each request a script of the action sends to the job's
// OIDC request URL: a GET when curl is told --get, and a POST otherwise,
// since curl posts the data it is given.
func scriptCalls(t *testing.T) []sent {
	t.Helper()
	scripts, err := filepath.Glob(filepath.Join(repositoryDir, "action", "*.sh"))
	if err != nil || len(scripts) == 0 {
		t.Fatalf("no scripts in action/: %v", err)
	}
	var calls []sent
	for _, script := range scripts {
		text, err := os.ReadFile(script)
		if err != nil {
			t.Fatal(err)
		}
		for _, found := range curlToTokenURL.FindAllStringSubmatch(string(text), -1) {
			verb := http.MethodPost
			if strings.Contains(found[1], "--get") {
				verb = http.MethodGet
			}
			calls = append(calls, sent{"action/" + filepath.Base(script), verb, "ACTIONS_ID_TOKEN_REQUEST_URL", "", ""})
		}
	}
	return calls
}

// TestScopeManifest holds permissions.json to the code in both directions.
// Every exported method of the GitHub and GitLab adapters, of the App's
// tokens and of the gateway's artifact reader runs through a recording
// transport, and a method with no walk fails. Each request must be one call
// of the manifest that names the method in by; a call marked read must be a
// GET or a HEAD; and every call must be reached by each code it names. A
// GraphQL query would be a POST that changes nothing: there is none, and a
// read call that posts fails here until this test reads its query for a
// mutation. The gh api commands of the recipes and the action's request for
// the job's OIDC token are read from their text. The App's token must ask for
// what the manifest states the App asks, and no more than its calls need.
func TestScopeManifest(t *testing.T) {
	manifest := docstest.Permissions(t, repositoryDir)
	walked := walks()
	for _, name := range methodNames() {
		if walked[name] == nil {
			t.Errorf("%s has no walk, so the calls it makes are not held to %s", name, docstest.ManifestFile)
		}
	}
	for name := range walked {
		if !slices.Contains(methodNames(), name) {
			t.Errorf("the walk of %s walks no method or function this test names", name)
		}
	}
	w := &walker{t: t}
	ctx := context.Background()
	for _, name := range slices.Sorted(maps.Keys(walked)) {
		w.by = name
		walked[name](ctx, w)
	}
	requests := slices.Concat(w.sent, recipeCalls(t), scriptCalls(t))
	reached := map[[2]string]bool{}
	for _, request := range requests {
		call, found := matchCall(manifest, request)
		if !found {
			t.Errorf("%s sends %s %s on %s, and %s lists no such call", request.by, request.method, request.path, request.base, docstest.ManifestFile)
			continue
		}
		if call.Access == "read" && request.method != http.MethodGet && request.method != http.MethodHead {
			t.Errorf("%s sends %s %s, and %s marks it read", request.by, request.method, request.path, call.ID)
		}
		if !slices.Contains(call.By, request.by) {
			t.Errorf("%s sends %s, and the call does not name it in by", request.by, call.ID)
		}
		reached[[2]string{call.ID, request.by}] = true
	}
	for _, call := range manifest.Calls {
		read := call.Method == http.MethodGet || call.Method == http.MethodHead
		if read != (call.Access == "read") {
			t.Errorf("%s is %s and marked %s", call.ID, call.Method, call.Access)
		}
		for _, by := range call.By {
			if !reached[[2]string{call.ID, by}] {
				t.Errorf("%s names %s in by, and no walk of %s reaches it", call.ID, by, by)
			}
		}
	}
	holdTheApp(t, manifest, w.sent)
	holdLocalCommands(t, manifest)
	holdBases(t, manifest)
}

// githubTitles are GitHub's names for the permissions a workflow writes.
var githubTitles = map[string]string{
	"actions": "Actions", "checks": "Checks", "contents": "Contents", "metadata": "Metadata", "pull-requests": "Pull requests",
}

// stated is how a basis states one permission at one level: in GitHub's
// words on a page, as a workflow writes it for a run, and as GitLab names the
// scope.
func stated(call docstest.Call, name, level string) string {
	switch {
	case call.Source.Kind == "run", name == "id-token":
		return name + ": " + level
	case name == "api" && level == "read":
		return "read_api: Grants read access to the API"
	case name == "api":
		return "api: Grants complete read and write access to the API"
	}
	return fmt.Sprintf("%q repository permissions (%s)", githubTitles[name], level)
}

// holdBases holds each call's basis to its permissions: the page's own words,
// or the run's answers, name every permission at the level the call states.
// A call with no permission rests on the App's own token.
func holdBases(t *testing.T, manifest docstest.Manifest) {
	t.Helper()
	for _, call := range manifest.Calls {
		if call.Source == nil {
			continue
		}
		for name, level := range call.Permissions {
			if want := stated(call, name, level); !strings.Contains(call.Source.Says, want) {
				t.Errorf("%s needs %s: %s, and its basis does not say %s", call.ID, name, level, want)
			}
		}
		if len(call.Permissions) == 0 && !strings.Contains(call.Source.Says, "You must use a JWT") {
			t.Errorf("%s needs no permission, and its basis does not say it takes the App's own token", call.ID)
		}
	}
}

// matchCall finds the manifest's call a request is.
func matchCall(manifest docstest.Manifest, request sent) (docstest.Call, bool) {
	host := "github"
	if strings.HasPrefix(request.by, "scm.GitLab.") {
		host = "gitlab"
	}
	for _, call := range manifest.Calls {
		if call.Host == host && call.Base == request.base && call.Matches(request.method, request.path) {
			return call, true
		}
	}
	return docstest.Call{}, false
}

// holdTheApp reads the permissions the App's token asks for from the request
// that mints it, and holds them to the App's grant and to the calls the App
// makes.
func holdTheApp(t *testing.T, manifest docstest.Manifest, requests []sent) {
	t.Helper()
	index := slices.IndexFunc(requests, func(request sent) bool { return request.by == "scm.AppTokens.Token" })
	if index < 0 {
		t.Fatal("the walk of scm.AppTokens.Token minted no token")
	}
	var minted struct {
		Permissions map[string]string `json:"permissions"`
	}
	if err := json.Unmarshal([]byte(requests[index].body), &minted); err != nil {
		t.Fatal(err)
	}
	asked := map[string]string{}
	for name, level := range minted.Permissions {
		asked[strings.ReplaceAll(name, "_", "-")] = level
	}
	grants := slices.DeleteFunc(slices.Clone(manifest.Grants), func(grant docstest.Grant) bool { return grant.File != "" })
	if len(grants) != 1 {
		t.Fatalf("%s states %d grants outside a recipe, and the App is the one", docstest.ManifestFile, len(grants))
	}
	for _, problem := range manifest.Problems(grants[0], "github", asked, nil) {
		t.Error(problem)
	}
}

// remoteGit are the git subcommands that reach a remote.
var remoteGit = []string{"push", "fetch", "pull", "clone", "ls-remote"}

var (
	// literalCommand is gh or git run with its subcommand written as string
	// literals, as in "gh", "secret", "set".
	literalCommand = regexp.MustCompile(`"(gh|git)"((?:,\s*"[^"]*")+)`)
	stringLiteral  = regexp.MustCompile(`"([^"]*)"`)
	// forwarded is gh run with the arguments a function was given.
	forwarded = regexp.MustCompile(`"gh",\s*args\.\.\.`)
	funcName  = regexp.MustCompile(`(?m)^func (?:\([^)]*\) )?(\w+)\(`)
	// scriptCommand is gh or a remote git command in a shell script.
	scriptCommand = regexp.MustCompile(`\b(?:gh [a-z-]+ [a-z-]+|git (?:` + strings.Join(remoteGit, "|") + `)\b)`)
)

// commandOf names a command as the manifest does: gh with its two words, or
// git with its subcommand, and nothing for a git command that stays local.
func commandOf(tool string, words []string) string {
	words = slices.DeleteFunc(slices.Clone(words), func(word string) bool { return strings.HasPrefix(word, "-") })
	switch {
	case tool == "git" && len(words) > 0 && slices.Contains(remoteGit, words[0]):
		return "git " + words[0]
	case tool == "gh" && len(words) >= 2:
		return "gh " + words[0] + " " + words[1]
	}
	return ""
}

// localCommands reads every gh command, and every git command that reaches
// a remote, that the Go code under cmd/ or a script a release ships runs, by
// the file that runs it. A function that runs gh with the arguments it was
// given is read where it is called.
func localCommands(t *testing.T) map[[2]string]bool {
	t.Helper()
	found := map[[2]string]bool{}
	var sources []string
	err := filepath.WalkDir(filepath.Join(repositoryDir, "cmd"), func(path string, entry os.DirEntry, err error) error {
		if err == nil && strings.HasSuffix(path, ".go") && !strings.HasSuffix(path, "_test.go") {
			sources = append(sources, path)
		}
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range sources {
		text := string(read(t, path))
		file := strings.TrimPrefix(filepath.ToSlash(path), "../../")
		for _, match := range literalCommand.FindAllStringSubmatch(text, -1) {
			var words []string
			for _, literal := range stringLiteral.FindAllStringSubmatch(match[2], -1) {
				words = append(words, literal[1])
			}
			if command := commandOf(match[1], words); command != "" {
				found[[2]string{command, file}] = true
			}
		}
		for _, at := range forwarded.FindAllStringIndex(text, -1) {
			names := funcName.FindAllStringSubmatch(text[:at[0]], -1)
			if len(names) == 0 {
				t.Fatalf("%s runs gh with forwarded arguments outside a function", file)
			}
			call := regexp.MustCompile(`\b` + names[len(names)-1][1] + `\(([^)]*)\)`)
			for _, match := range call.FindAllStringSubmatch(text, -1) {
				var words []string
				for _, literal := range stringLiteral.FindAllStringSubmatch(match[1], -1) {
					words = append(words, literal[1])
				}
				if command := commandOf("gh", words); command != "" {
					found[[2]string{command, file}] = true
				}
			}
		}
	}
	scripts, err := filepath.Glob(filepath.Join(repositoryDir, "action", "*.sh"))
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range append(scripts, filepath.Join(repositoryDir, "scripts", "install.sh")) {
		for _, command := range scriptCommand.FindAllString(string(read(t, path)), -1) {
			found[[2]string{command, strings.TrimPrefix(filepath.ToSlash(path), "../../")}] = true
		}
	}
	return found
}

func read(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

// holdLocalCommands holds the manifest's local commands to the gh and git commands
// the code and the shipped scripts run, in both directions.
func holdLocalCommands(t *testing.T, manifest docstest.Manifest) {
	t.Helper()
	found := localCommands(t)
	if len(found) == 0 {
		t.Fatal("no gh command found under cmd/, so this walk reads nothing")
	}
	listed := map[[2]string]bool{}
	for _, entry := range manifest.Local {
		for _, file := range entry.By {
			listed[[2]string{entry.Command, file}] = true
			if !found[[2]string{entry.Command, file}] {
				t.Errorf("%s names %s in by, and %s does not run %s", entry.ID, file, file, entry.Command)
			}
		}
	}
	for key := range found {
		if !listed[key] {
			t.Errorf("%s runs %s, and %s lists no such local command", key[1], key[0], docstest.ManifestFile)
		}
	}
}
