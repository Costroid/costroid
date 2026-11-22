package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Costroid/costroid/internal/comment"
	"github.com/Costroid/costroid/internal/docstest"
	"github.com/Costroid/costroid/internal/gateway"
	"github.com/Costroid/costroid/internal/gateway/gatewaytest"
)

// place names the blocks of one language under one heading of a page, as
// docstest.Place does.
type place struct{ page, heading, language string }

// commandsAndFiles are the languages of a block a reader types or saves: a
// command line, a workflow, a costroid.yml, a query, a pack, a cost file.
// Such a block shows no output.
var commandsAndFiles = []string{"sh", "yaml", "sql", "rego", "csv"}

// held says what holds a block: the test that holds it to a run of the
// command that prints it, with the directory of that test's package, or, for
// a block that shows no output of Costroid's, what it shows.
type held struct{ test, in, shows string }

const (
	thisTest    = "TestEveryDocumentedOutputIsWhatTheCommandPrints"
	thisPackage = "cmd/costroid"
)

// documentedBlocks says, for every block of the documentation that is not a
// command or a file, what holds it.
var documentedBlocks = map[place]held{
	{"README.md", "Costroid", "text"}:                                             {test: "TestReadmeAndListingQuoteTheDemoCommentsFirstLine", in: thisPackage},
	{"README.md", "A run on your machine", "text"}:                                {test: "TestReadmeFirstCommandShowsWhatTheProgramPrints", in: thisPackage},
	{"README.md", "More", "text"}:                                                 {shows: "commands typed in Claude Code"},
	{"docs/adapters.md", "Recording a fixture", ""}:                               {shows: "commands a contributor types"},
	{"docs/agents.md", "Read the figures", "json"}:                                {test: thisTest, in: "internal/comment"},
	{"docs/agents.md", "Read the cap", "json"}:                                    {test: thisTest, in: "internal/comment"},
	{"docs/agents.md", "Read the pull request's total", "json"}:                   {test: thisTest, in: "internal/comment"},
	{"docs/agents.md", "Read the run's status", "json"}:                           {test: "TestAgentsPageStatusExampleIsWhatTheRunWrites", in: "internal/run"},
	{"docs/approval.md", "What the check shows", "text"}:                          {test: "TestApprovalPageQuotesTheApprovedSentence", in: "internal/comment"},
	{"docs/comment-example.md", "Costroid", "json"}:                               {test: "TestDocsExampleIsTheRenderedCommentForARealRowsFile", in: "internal/comment"},
	{"docs/existing-workflow.md", "On `openai/codex-action`", "diff"}:             {test: "TestInitExistingMatchesTheCodexDocs", in: thisPackage},
	{"docs/export.md", "Merge facts", "json"}:                                     {test: thisTest, in: thisPackage},
	{"docs/export.md", "The grade needs a protected branch", "text"}:              {test: thisTest, in: thisPackage},
	{"docs/export.md", "Cloudability", "text"}:                                    {shows: "the layout IBM Cloudability's page states"},
	{"docs/export.md", "Cloudability", "json"}:                                    {test: thisTest, in: thisPackage},
	{"docs/export.md", "Microsoft FinOps hubs", "text"}:                           {shows: "the path Microsoft's page states"},
	{"docs/gateway.md", "Commands a gateway run refuses", "text"}:                 {test: thisTest, in: thisPackage},
	{"docs/gateway.md", "How a budget passes on the gateway's count", "text"}:     {test: thisTest, in: thisPackage},
	{"docs/gateway.md", "What the rows say", "text"}:                              {test: thisTest, in: "internal/run"},
	{"docs/gateway.md", "The gateway holds the budget in its own prices", "text"}: {test: thisTest, in: "internal/run"},
	{"docs/gh-aw.md", "Read a run", "text"}:                                       {test: "TestGhAwDocShowsWhatIngestRunPrints", in: thisPackage},
	{"docs/infrastructure.md", "What the comment shows", "markdown"}:              {test: thisTest, in: "internal/comment"},
	{"docs/infrastructure.md", "What the comment shows", "json"}:                  {test: thisTest, in: "internal/comment"},
	{"docs/infrastructure.md", "A fork's pull request", "text"}:                   {test: thisTest, in: thisPackage},
	{"docs/infrastructure.md", "A fork's pull request", "markdown"}:               {test: thisTest, in: "internal/comment"},
	{"docs/ledger.md", "The report", ""}:                                          {test: thisTest, in: "internal/server"},
	{"docs/output.md", "`costroid doctor --json`", "json"}:                        {test: thisTest, in: thisPackage},
	{"docs/output.md", "`costroid lint --json`", "json"}:                          {test: thisTest, in: thisPackage},
	{"docs/output.md", "`costroid validate --json`", "json"}:                      {test: thisTest, in: thisPackage},
	{"docs/output.md", "`costroid version --json`", "json"}:                       {test: thisTest, in: thisPackage},
	{"docs/policy.md", "A repository's own pack", "json"}:                         {test: "TestPolicyPageRunsItsPackExample", in: "internal/policy"},
	{"docs/policy.md", "What the check says", "text"}:                             {test: "TestPolicyPageQuotesSentencesTheGoldensHold", in: "internal/comment"},
	{"docs/policy.md", "What a verdict records", "json"}:                          {test: thisTest, in: "internal/comment"},
	{"docs/receipt.md", "What the line says", "text"}:                             {test: thisTest, in: "internal/comment"},
	{"docs/reconcile.md", "Amounts and rounding", "text"}:                         {shows: "the formula FOCUS states"},
	{"docs/serve.md", "Which credential earns `exact`", "json"}:                   {shows: "the body of a request to the service"},
	{"docs/serve.md", "The gateway's count", "json"}:                              {shows: "the body of a request to the service"},
	{"docs/serve.md", "The verdict", "json"}:                                      {shows: "the body of a request to the service"},
	{"docs/serve.md", "A repository's own pack", "json"}:                          {shows: "the body of a request to the service"},
	{"docs/tags.md", "Tag a run with its work item, arm or team", "json"}:         {test: "TestTagsDocExampleRuns", in: thisPackage},
}

// printedHere runs, for each block this test holds, the command that prints
// it, and compares.
var printedHere = map[place]func(t *testing.T, shown string){
	{"docs/output.md", "`costroid doctor --json`", "json"}:                    doctorObjectIsPrinted,
	{"docs/output.md", "`costroid lint --json`", "json"}:                      lintObjectIsPrinted,
	{"docs/output.md", "`costroid validate --json`", "json"}:                  validateObjectIsPrinted,
	{"docs/output.md", "`costroid version --json`", "json"}:                   versionObjectIsPrinted,
	{"docs/export.md", "Merge facts", "json"}:                                 mergeFactIsSent,
	{"docs/export.md", "The grade needs a protected branch", "text"}:          mergedLineIsPrinted,
	{"docs/export.md", "Cloudability", "json"}:                                manifestIsWritten,
	{"docs/infrastructure.md", "A fork's pull request", "text"}:               missingEstimateIsPrinted,
	{"docs/gateway.md", "Commands a gateway run refuses", "text"}:             gatewayRefusalIsPrinted,
	{"docs/gateway.md", "How a budget passes on the gateway's count", "text"}: unsettledCountIsPrinted,
}

// TestEveryDocumentedOutputIsWhatTheCommandPrints reads every fenced block of
// the README and of docs/ that is not a command or a file. Each is in the
// table above: held to a run by this test, held to a run by the test the
// table names, or no output of Costroid's. A block this test holds is what
// the command prints, run here as the page describes it. Where an example
// names the release, the commit its tag names or the catalog, the run is of
// a build of the release the recipes pin, so a release that moves the pin, or
// a change that moves what a command prints, cannot leave an example behind.
// A block the table does not name fails here, so a new example is held from
// the day it is written.
func TestEveryDocumentedOutputIsWhatTheCommandPrints(t *testing.T) {
	clearCI(t)
	tests := testsByPackage(t)
	found := map[place]bool{}
	for _, block := range docstest.Blocks(t, repositoryRoot) {
		if slices.Contains(commandsAndFiles, block.Language) {
			continue
		}
		at := place{block.Page, block.Heading, block.Language}
		holder, listed := documentedBlocks[at]
		found[at] = true
		run, here := printedHere[at]
		switch {
		case !listed:
			t.Errorf("%s:%d: the %q block under %q is in no table of this test: hold it to a run of the command that prints it, or say what it shows", block.Page, block.Line, block.Language, block.Heading)
		case (holder.test == "") == (holder.shows == ""):
			t.Errorf("%s, %q: the table names a test and a reason, or neither", block.Page, block.Heading)
		case here != (holder.test == thisTest && holder.in == thisPackage):
			t.Errorf("%s, %q: the table names this test and no run prints the block, or the reverse", block.Page, block.Heading)
		case here:
			t.Run(block.Page+" "+block.Heading+" "+block.Language, func(t *testing.T) { run(t, block.Body) })
		case holder.test != "":
			if source := tests[holder.in][holder.test]; !strings.Contains(source, filepath.Base(block.Page)) {
				t.Errorf("%s, %q: %s holds no test %s that reads %s", block.Page, block.Heading, holder.in, holder.test, filepath.Base(block.Page))
			}
		}
	}
	for listed := range documentedBlocks {
		if !found[listed] {
			t.Errorf("the table names a %q block under %q of %s, and the page holds none", listed.language, listed.heading, listed.page)
		}
	}
}

// testFunction is the opening of a test's declaration.
var testFunction = regexp.MustCompile(`(?m)^func (Test\w+)\(`)

// testsByPackage maps each package directory under cmd/ and internal/ to its
// tests, each with the text of the file that declares it.
func testsByPackage(t *testing.T) map[string]map[string]string {
	t.Helper()
	tests := map[string]map[string]string{}
	for _, top := range []string{"cmd", "internal"} {
		err := filepath.WalkDir(filepath.Join(repositoryRoot, top), func(path string, entry fs.DirEntry, err error) error {
			if err != nil || entry.IsDir() || !strings.HasSuffix(path, "_test.go") {
				return err
			}
			source := readFile(t, path)
			directory := strings.TrimPrefix(filepath.ToSlash(filepath.Dir(path)), repositoryRoot+"/")
			if tests[directory] == nil {
				tests[directory] = map[string]string{}
			}
			for _, match := range testFunction.FindAllStringSubmatch(source, -1) {
				tests[directory][match[1]] = source
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	return tests
}

// recipePinAt is the commit and the release a recipe pins Costroid's action
// to.
var recipePinAt = regexp.MustCompile(regexp.QuoteMeta(costroidActionPath) + `@([0-9a-f]{40}) # v(\S+)`)

// pinnedBuild is a build of the one release every GitHub recipe pins, at the
// commit its tag names: what scripts/pin last moved the documentation to.
func pinnedBuild(t *testing.T) buildInfo {
	t.Helper()
	pins := map[pin]bool{}
	for _, file := range readerFiles(t, "recipes") {
		for _, match := range recipePinAt.FindAllStringSubmatch(readFile(t, file), -1) {
			pins[pin{commit: match[1], version: match[2]}] = true
		}
	}
	if len(pins) != 1 {
		t.Fatalf("the recipes pin %d releases, want 1: %v", len(pins), pins)
	}
	var build buildInfo
	for release := range pins {
		build = buildInfo{version: release.version, commit: release.commit}
	}
	return build
}

// asPinnedBuild runs the rest of the test as a build of the release the
// recipes pin.
func asPinnedBuild(t *testing.T) {
	t.Helper()
	build := pinnedBuild(t)
	previous := [2]string{version, commit}
	t.Cleanup(func() { version, commit = previous[0], previous[1] })
	version, commit = build.version, build.commit
}

// sameObject holds a block that shows JSON to the JSON a command printed,
// value for value. The page lays an object out over lines, and the command
// prints it on one.
func sameObject(t *testing.T, shown, printed string) {
	t.Helper()
	var want, got any
	if err := json.Unmarshal([]byte(shown), &want); err != nil {
		t.Fatalf("the page's block is not JSON: %v\n%s", err, shown)
	}
	if err := json.Unmarshal([]byte(printed), &got); err != nil {
		t.Fatalf("the command printed no JSON: %v\n%s", err, printed)
	}
	if !reflect.DeepEqual(want, got) {
		t.Errorf("the page shows\n%s\nand the command prints\n%s", shown, printed)
	}
}

// doctorObjectIsPrinted runs doctor --json, as the pinned release, where a
// valid costroid.yml is all there is: no workflow, and no agent on the
// machine.
func doctorObjectIsPrinted(t *testing.T, shown string) {
	root := doctorRepository(t, map[string]string{configFile: "policy:\n  run_budget_usd: \"5\"\n"})
	var stdout, stderr bytes.Buffer
	host := &fakeHost{env: map[string]string{}}
	if code := doctor(context.Background(), root, pinnedBuild(t), host.host(), []string{"--json"}, &stdout, &stderr); code != 0 {
		t.Fatalf("doctor --json exited %d: %s", code, stderr.String())
	}
	sameObject(t, shown, stdout.String())
}

// lintObjectIsPrinted runs lint --json on a workflow whose agent job holds
// id-token: write, under the name the page gives it.
func lintObjectIsPrinted(t *testing.T, shown string) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, defaultWorkflows, "review.yml"), readFile(t, filepath.Join(customerTestdata, "high-id-token.yml")))
	t.Chdir(root)
	code, printed, stderr := execute(t, "lint", "--json")
	if code != exitRejected {
		t.Fatalf("lint --json exited %d: %s", code, stderr)
	}
	sameObject(t, shown, printed)
}

// validateObjectIsPrinted runs validate --json on a costroid.yml with a key
// the policy does not have.
func validateObjectIsPrinted(t *testing.T, shown string) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, configFile), "policy:\n  no_such_key: 1\n")
	t.Chdir(root)
	code, printed, stderr := execute(t, "validate", "--json")
	if code != exitRejected {
		t.Fatalf("validate --json exited %d: %s", code, stderr)
	}
	sameObject(t, shown, printed)
}

// versionObject is what version --json prints as the pinned release, in a
// directory with no costroid.yml.
func versionObject(t *testing.T) buildFacts {
	t.Helper()
	asPinnedBuild(t)
	t.Chdir(t.TempDir())
	code, printed, stderr := execute(t, "version", "--json")
	if code != 0 {
		t.Fatalf("version --json exited %d: %s", code, stderr)
	}
	var facts buildFacts
	if err := json.Unmarshal([]byte(printed), &facts); err != nil {
		t.Fatal(err)
	}
	return facts
}

// versionObjectIsPrinted runs version --json as the pinned release. The
// system is the machine's own, so the page's is held to a system the install
// page names a binary for, and every other field to the run.
func versionObjectIsPrinted(t *testing.T, shown string) {
	var example buildFacts
	if err := json.Unmarshal([]byte(shown), &example); err != nil {
		t.Fatalf("the page's block is not the object: %v", err)
	}
	if binary := "costroid-" + example.OS + "-" + example.Arch; !strings.Contains(readFile(t, filepath.Join(repositoryRoot, "docs", "install.md")), "/"+binary+"\n") {
		t.Errorf("the page shows the system %s/%s, and docs/install.md downloads no %s", example.OS, example.Arch, binary)
	}
	facts := versionObject(t)
	facts.OS, facts.Arch = example.OS, example.Arch
	printed, err := json.Marshal(facts)
	if err != nil {
		t.Fatal(err)
	}
	sameObject(t, shown, string(printed))
}

// TestTheVersionExampleNamesWhatThisBuildWrites holds the object docs/output.md
// shows for version --json to this build and to the release the recipes pin:
// the release and the commit are the pin's, the result versions are the ones
// this build writes, in their order, and the catalog and the day its prices
// were read are the ones this build carries. A release that moves the pin, a
// change that writes one more version of the result object, and a catalog
// that moves each fail here until the example follows.
func TestTheVersionExampleNamesWhatThisBuildWrites(t *testing.T) {
	shown := docstest.At(t, repositoryRoot, docstest.Place{Page: "docs/output.md", Heading: "`costroid version --json`", Language: "json"})
	var example buildFacts
	if err := json.Unmarshal([]byte(shown[0]), &example); err != nil || len(shown) != 1 || example.Schema != versionSchema {
		t.Fatalf("docs/output.md shows no %s object: %v", versionSchema, err)
	}
	build := pinnedBuild(t)
	if build.version != pinnedRelease(t) || example.Release != build.version || example.Commit == nil || *example.Commit != build.commit {
		t.Errorf("the example names release %s at commit %v, and the recipes pin %s at %s", example.Release, example.Commit, build.version, build.commit)
	}
	if written := comment.WrittenSchemas(); !slices.Equal(example.ResultSchemas, written) {
		t.Errorf("the example lists the result versions %q, and this build writes %q", example.ResultSchemas, written)
	}
	facts := versionObject(t)
	if !reflect.DeepEqual(example.Catalog, facts.Catalog) || example.Catalog.PricesRead == nil {
		t.Errorf("the example names the catalog %+v, and this build carries %+v", example.Catalog, facts.Catalog)
	}
	if !slices.Equal(facts.ResultSchemas, comment.WrittenSchemas()) || facts.Release != build.version || facts.Commit == nil || *facts.Commit != build.commit {
		t.Errorf("version --json does not print the pinned build and the versions it writes: %+v", facts)
	}
}

// mergedPush runs merged on a merge queue's push, against a platform that
// states integration/v0.6, and answers what it sent and what it said.
func mergedPush(t *testing.T) (*mergeStub, string) {
	t.Helper()
	stub := newV06MergeStub(t)
	code, _, stderr := stub.merged(t, stub.pushEnv(t, mergeHost(t), groupCommit, tipCommit))
	if code != 0 || len(stub.bodies) != 2 {
		t.Fatalf("merged exited %d and sent %d facts: %s", code, len(stub.bodies), stderr)
	}
	return stub, stderr
}

// mergeFactIsSent holds the page's merge fact to the document merged sends
// for the push's second pull request.
func mergeFactIsSent(t *testing.T, shown string) {
	stub, _ := mergedPush(t)
	sameObject(t, shown, string(stub.bodies[1]))
}

// mergedLineIsPrinted holds the page's line to one merged prints for a fact
// the platform stored as token_verified.
func mergedLineIsPrinted(t *testing.T, shown string) {
	if _, stderr := mergedPush(t); !strings.Contains(stderr, shown+"\n") || strings.Contains(shown, "\n") {
		t.Errorf("the page shows\n%s\nand merged prints\n%s", shown, stderr)
	}
}

// manifestIsWritten writes the example rows as the cloudability format under
// the bucket the page names, and holds the page's manifest to the one
// written. The export's own time names the drop's folder and the manifest's
// updated_at, so the page's two are held to each other and read from the
// run.
func manifestIsWritten(t *testing.T, shown string) {
	var example struct {
		RootDir string   `json:"root_dir"`
		Keys    []string `json:"all_report_keys"`
		Updated string   `json:"updated_at"`
	}
	if err := json.Unmarshal([]byte(shown), &example); err != nil || len(example.Keys) != 1 {
		t.Fatalf("the page's manifest names %d report keys: %v", len(example.Keys), err)
	}
	updated, err := time.Parse(time.RFC3339, example.Updated)
	folders := strings.Split(example.Keys[0], "/")
	if err != nil || len(folders) != 4 || folders[2] != unixSeconds(updated) {
		t.Fatalf("the page's drop folder %q is not its updated_at, %s, in seconds since 1970: %v", example.Keys[0], example.Updated, err)
	}
	out := t.TempDir()
	if code, _, stderr := runExport(t, "--rows", exampleRows, "--format", "cloudability", "--out", out, "--root-dir", example.RootDir); code != 0 {
		t.Fatalf("export exited %d: %s", code, stderr)
	}
	manifests, err := filepath.Glob(filepath.Join(out, "*", "*", "*", "*-Manifest.json"))
	if err != nil || len(manifests) != 1 {
		t.Fatalf("export wrote %d manifests: %v", len(manifests), err)
	}
	written := readFile(t, manifests[0])
	var drop struct {
		Updated string `json:"updated_at"`
	}
	if err := json.Unmarshal([]byte(written), &drop); err != nil {
		t.Fatal(err)
	}
	wrote, err := time.Parse(time.RFC3339, drop.Updated)
	if err != nil {
		t.Fatal(err)
	}
	sameObject(t, strings.NewReplacer(example.Updated, drop.Updated, unixSeconds(updated), unixSeconds(wrote)).Replace(shown), written)
}

// unixSeconds is a time as the seconds since 1970, in digits.
func unixSeconds(at time.Time) string {
	return strconv.FormatInt(at.Unix(), 10)
}

// missingEstimateIsPrinted runs the page's own command where the estimator
// wrote no file, and holds the page's line to what the command says.
func missingEstimateIsPrinted(t *testing.T, shown string) {
	command := ""
	for _, line := range strings.Split(readFile(t, filepath.Join(repositoryRoot, "docs", "infrastructure.md")), "\n") {
		if strings.HasPrefix(line, "costroid ingest-run --from focus=") && strings.Contains(line, " --optional ") {
			command = line
		}
	}
	if command == "" {
		t.Fatal("docs/infrastructure.md shows no ingest-run --from focus line with --optional")
	}
	t.Chdir(t.TempDir())
	code, stdout, stderr := execute(t, strings.Fields(command)[1:]...)
	if code != 0 || stdout != "" || stderr != shown+"\n" {
		t.Errorf("%s exits %d and says\n%s\nand the page shows\n%s", command, code, stderr, shown)
	}
}

// gatewayRefusalIsPrinted runs an agent command that holds a flag which
// sends requests somewhere other than the gateway, under a gateway. run
// refuses it before it asks the gateway anything, and the page's line is the
// first it says, with the flag and the agent in place of the page's
// placeholders.
func gatewayRefusalIsPrinted(t *testing.T, shown string) {
	var asked atomic.Bool
	gate := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { asked.Store(true) }))
	defer gate.Close()
	dir := t.TempDir()
	key := filepath.Join(dir, "key")
	writeFile(t, key, "sk-a-run-key-of-sixteen-or-more\n")
	code, _, stderr := execute(t, "run", "--budget-usd", "5", "--gateway", gate.URL, "--gateway-key-file", key, "--out", filepath.Join(dir, "rows.jsonl"), "--", "claude", "--remote", "-p", "work")
	said, _, _ := strings.Cut(stderr, "\n")
	if code != exitFailure || asked.Load() || said != strings.NewReplacer("<flag>", "--remote", "<agent>", "claude-code").Replace(shown) {
		t.Errorf("run exits %d, asked the gateway: %t, and says\n%s\nand the page shows\n%s", code, asked.Load(), stderr, shown)
	}
}

// unsettledCountIsPrinted closes a key whose spend is still moving when the
// wait for it ends. The page's line is the warning close prints, with the
// figure it attests in place of the page's placeholder. The page states the
// two durations a job waits, and this run waits two shorter ones.
func unsettledCountIsPrinted(t *testing.T, shown string) {
	newGatewayJob(t)
	w := newAttestWorld(t)
	t.Setenv(ingestURLVariable, w.ledger.URL+"/ledger/v1/rows")
	stub := gatewaytest.New(t, gatewayAdmin)
	stub.Add(gatewaytest.Key{Key: "sk-a-run-key-of-sixteen-or-more", Alias: gatewayAlias})
	stub.BeforeList = func(call int) { stub.SetSpend(gatewayAlias, fmt.Sprintf("0.%d1", call)) }
	counter := gateway.Counter{
		Client: gateway.NewClient(stub.URL), AdminKey: gatewayAdmin,
		Run:  gateway.Binding{Repository: "github.com/acme/widgets", Run: gatewayRunID, Attempt: "1"},
		Poll: 5 * time.Millisecond, Stable: 100 * time.Millisecond, Settle: time.Second,
	}
	var stdout, stderr bytes.Buffer
	if err := closeKey(context.Background(), counter, 0, w.ledger.Client(), &stdout, &stderr); err != nil {
		t.Fatalf("%v: %s", err, stderr.String())
	}
	waits := " in the last " + gatewaySpendStable.String() + " of a " + gatewaySpendSettle.String() + " wait,"
	if !strings.Contains(shown, waits) {
		t.Fatalf("the page does not state the waits of a job, %s and %s:\n%s", gatewaySpendStable, gatewaySpendSettle, shown)
	}
	waited := strings.Replace(shown, waits, " in the last "+counter.Stable.String()+" of a "+counter.Settle.String()+" wait,", 1)
	if said := lineWith(stderr.String(), "costroid: warning: "); !docstest.Fills(waited, said) {
		t.Errorf("close says\n%s\nand the page shows\n%s", stderr.String(), shown)
	}
}
