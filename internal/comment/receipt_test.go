package comment

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Costroid/costroid/internal/decimal"
	"github.com/Costroid/costroid/internal/policy"
)

// resultV013SchemaPath is result/v0.13 of Costroid/costroid-schemas, copied.
var resultV013SchemaPath = filepath.Join("testdata", "result-v0.13.schema.json")

// resultV013SchemaSHA256 is the SHA-256 of result/v0.13/result.schema.json as
// the schemas' branch for that version holds it, before it is published:
//
//	git show receipt-contracts:result/v0.13/result.schema.json | sha256sum
const resultV013SchemaSHA256 = "dc6cf848600ccdc6075f8bb5ada58d878c71569653e8f5e8210735fd1096e82b"

// The receipt the contract's examples read, as a report states it: the
// comment, and the figure of its 12 rows at the catalog. The comment's id,
// time and digest and the figure are synthetic.
const (
	receiptCommentID = 2471000001
	receiptDigest    = "09a3ca8ff98a5921fc951e52265e8de2ef3739115f1603dfc09c44171906df13"
	receiptAuthor    = "octocat"
)

var receiptCreated = time.Date(2026, 11, 20, 16, 42, 7, 0, time.UTC)

func readReceipt() *Receipt {
	return &Receipt{
		CommentID: receiptCommentID, CreatedAt: receiptCreated, SHA256: receiptDigest, Author: receiptAuthor,
		Sessions: 2, FirstDay: "2026-11-18", LastDay: "2026-11-20",
		Figures: []ReceiptFigure{{Currency: "USD", Cost: decimal.MustParse("11.4619213"), Rows: 12}},
	}
}

func refusedReceipt(reason ReceiptReason) *Receipt {
	return &Receipt{CommentID: receiptCommentID, CreatedAt: receiptCreated, SHA256: receiptDigest, Author: receiptAuthor, Refused: reason}
}

// withReceipt is the report on pull request 7 of example/widgets, whose
// workflow asks for receipts, beside the receipt given, nil for none posted.
func withReceipt(input Input, stated *Receipt) Input {
	input.Repository, input.PullRequest = "github.com/example/widgets", 7
	input.ReceiptsAsked, input.Receipt = true, stated
	return input
}

// receiptStates are the four golden states of a receipt, beside the neutral
// report: read, none posted, refused because it was edited, and read with
// amounts withheld.
func receiptStates(base Input) map[string]Input {
	read := withReceipt(base, readReceipt())
	return map[string]Input{
		"receipt":          read,
		"receipt-none":     withReceipt(base, nil),
		"receipt-refused":  withReceipt(base, refusedReceipt(ReceiptEdited)),
		"receipt-withheld": withheld(read),
	}
}

// receiptVariants are what a report can hold beside its own rows: a receipt
// read, each refusal, and none posted where the workflow asked.
func receiptVariants() map[string]*Receipt {
	variants := map[string]*Receipt{"read": readReceipt(), "none posted": nil}
	for reason := range receiptReasons {
		variants[string(reason)] = refusedReceipt(reason)
	}
	return variants
}

// holdsReceipt reports an input whose result object holds a receipt, the one
// value of result/v0.13.
func holdsReceipt(input Input) bool {
	return input.receipt() != nil
}

// isReceiptState reports a golden state that holds a receipt or asks for one.
func isReceiptState(name string) bool {
	return strings.HasPrefix(name, "receipt")
}

// apart is a result object with its receipts and its schema taken out, as
// the keys every other field states.
func apart(t *testing.T, object string) map[string]json.RawMessage {
	t.Helper()
	var fields map[string]json.RawMessage
	if err := json.Unmarshal([]byte(object), &fields); err != nil {
		t.Fatal(err)
	}
	delete(fields, "receipts")
	delete(fields, "schema")
	return fields
}

// TestTheVerdictReadsTheSameWithOrWithoutReceipts puts each receipt variant
// beside every golden state that holds none: the verdict sentence, the
// check's title, the conclusion, every output but result, and every key of
// the machine block but receipts and schema read the same bytes as the state
// without it, in the check run's object, the comment's copy and a withheld
// copy.
func TestTheVerdictReadsTheSameWithOrWithoutReceipts(t *testing.T) {
	compared := 0
	for name, input := range goldenStates(t) {
		if isReceiptState(name) {
			continue
		}
		plainCheck, plainOutputs, plainObjects := surfaces(t, input)
		for variant, stated := range receiptVariants() {
			with := withReceipt(input, stated)
			with.Repository, with.PullRequest = input.Repository, input.PullRequest
			if got, want := VerdictSentence(with), VerdictSentence(input); got != want {
				t.Errorf("%s, %s: the sentence reads %q, without it %q", name, variant, got, want)
			}
			if with.Conclusion() != input.Conclusion() {
				t.Errorf("%s, %s: the conclusion moved", name, variant)
			}
			check, outputs, objects := surfaces(t, with)
			if check.Title != plainCheck.Title {
				t.Errorf("%s, %s: the title reads %q, without it %q", name, variant, check.Title, plainCheck.Title)
			}
			for index, output := range outputs {
				if output.Name != "result" && output != plainOutputs[index] {
					t.Errorf("%s, %s: output %s reads %q, without it %q", name, variant, output.Name, output.Value, plainOutputs[index].Value)
				}
			}
			for copied, object := range objects {
				if got, want := apart(t, object), apart(t, plainObjects[copied]); !maps.EqualFunc(got, want, func(a, b json.RawMessage) bool { return string(a) == string(b) }) {
					t.Errorf("%s, %s, %s: a key but receipts moved:\n%s\nwithout it:\n%s", name, variant, copied, object, plainObjects[copied])
				}
			}
			compared++
		}
	}
	if compared < 100*len(receiptVariants()) {
		t.Fatalf("%d comparisons, fewer than every golden state with every variant", compared)
	}
}

// surfaces are what a report writes for an input that a receipt could move:
// the check, the step's outputs, and the result object as the check run, the
// comment and a withheld comment carry it.
func surfaces(t *testing.T, input Input) (CheckOutput, []Output, map[string]string) {
	t.Helper()
	check, err := Check(input)
	if err != nil {
		t.Fatal(err)
	}
	outputs, err := Outputs(input)
	if err != nil {
		t.Fatal(err)
	}
	return check, outputs, copies(t, input)
}

// TestCommentWithoutAReceiptIsUnchanged renders every golden state with a
// receipt beside it that the workflow did not ask for: the comment, the job
// summary, the check and the outputs are the golden's, byte for byte, with no
// line and no receipts. Asked for, with none posted, the machine block, the
// title and the outputs are still the golden's; only the one line is added.
func TestCommentWithoutAReceiptIsUnchanged(t *testing.T) {
	for name, input := range goldenStates(t) {
		if isReceiptState(name) {
			continue
		}
		unasked := input
		unasked.Receipt = readReceipt()
		for file, text := range renderState(t, unasked).files() {
			saved, err := os.ReadFile(filepath.Join(goldenDir, name, file))
			if err != nil {
				t.Fatal(err)
			}
			if text != string(saved) {
				t.Errorf("%s, %s: a receipt the workflow did not ask for changed it", name, file)
			}
		}
		asked := withReceipt(input, nil)
		asked.Repository, asked.PullRequest = input.Repository, input.PullRequest
		rendered := renderState(t, asked)
		saved := map[string]string{}
		for _, file := range []string{goldenOutputs, goldenCheck} {
			data, err := os.ReadFile(filepath.Join(goldenDir, name, file))
			if err != nil {
				t.Fatal(err)
			}
			saved[file] = string(data)
		}
		title, _, _ := strings.Cut(rendered.check, "\n\n# Summary\n\n")
		_, text, _ := strings.Cut(rendered.check, "\n\n# Text\n\n")
		savedTitle, _, _ := strings.Cut(saved[goldenCheck], "\n\n# Summary\n\n")
		_, savedText, _ := strings.Cut(saved[goldenCheck], "\n\n# Text\n\n")
		if title != savedTitle || text != savedText || rendered.files()[goldenOutputs] != saved[goldenOutputs] {
			t.Errorf("%s: asking for receipts with none posted moved the title, the machine block or an output", name)
		}
		if !strings.Contains(rendered.comment, noReceiptLine(input)) || !strings.Contains(rendered.summary, noReceiptLine(input)) {
			t.Errorf("%s: no line says that no receipt was posted", name)
		}
	}
}

func noReceiptLine(input Input) string {
	return fmt.Sprintf("No developer-machine sessions stated for this %s. The figures here are the CI run's alone.", input.request())
}

// TestMachineBlockPinsTheReceiptsComment reads the receipt's entry in each
// copy of the result object: the comment's id, its creation time in UTC and
// the digest of its body, and no login. A read receipt states its sessions,
// days and figure, and a withheld copy drops the amount alone. A refused one
// states the reason and no session, day or amount.
func TestMachineBlockPinsTheReceiptsComment(t *testing.T) {
	states := receiptStates(goldenStates(t)["neutral-shown"])
	pin := BlockReceiptComment{ID: receiptCommentID, CreatedAt: "2026-11-20T16:42:07Z", SHA256: receiptDigest}
	for name, input := range map[string]Input{"read": states["receipt"], "withheld": states["receipt-withheld"], "refused": states["receipt-refused"]} {
		for copied, object := range copies(t, input) {
			var block Block
			if err := json.Unmarshal([]byte(object), &block); err != nil {
				t.Fatal(err)
			}
			if strings.Contains(object, receiptAuthor) {
				t.Errorf("%s, %s: the object names the login that posted the receipt", name, copied)
			}
			if block.Schema != schemaV013 || len(block.Receipts) != 1 || block.Receipts[0].Comment != pin {
				t.Fatalf("%s, %s: written as %s with receipts %+v", name, copied, block.Schema, block.Receipts)
			}
			entry := block.Receipts[0]
			shown := block.Amounts == amountsShown
			switch {
			case name == "refused" && (entry.Reason != ReceiptEdited || entry.Sessions != 0 || entry.FirstDay != "" || entry.Figures != nil):
				t.Errorf("refused, %s: %+v", copied, entry)
			case name != "refused" && (entry.Reason != "" || entry.Sessions != 2 || entry.FirstDay != "2026-11-18" || entry.LastDay != "2026-11-20" || len(entry.Figures) != 1):
				t.Errorf("%s, %s: %+v", name, copied, entry)
			case name != "refused" && (entry.Figures[0] != BlockReceiptFigure{Currency: "USD", CostBasis: "list_equivalent", ListCost: map[bool]string{true: "11.4619213"}[shown], Rows: 12}):
				t.Errorf("%s, %s: the figure is %+v, shown %t", name, copied, entry.Figures[0], shown)
			}
		}
	}
	malformed := readReceipt()
	malformed.Author = "helper[bot]"
	if block := withReceipt(states["receipt-none"], malformed).withReceipts(Block{}, true); block.Receipts != nil {
		t.Errorf("a receipt whose login is no person's was stated: %+v", block.Receipts)
	}
}

// TestResultWithoutAReceiptIsWhatItWas holds every golden state's objects,
// with receipts asked for and none posted, to the objects the state writes
// without them, byte for byte, in the version they were, and every receipt
// state's object, with its receipts taken out and its schema set back, to
// the object the same report writes with none.
func TestResultWithoutAReceiptIsWhatItWas(t *testing.T) {
	for name, input := range goldenStates(t) {
		if isReceiptState(name) {
			continue
		}
		asked := withReceipt(input, nil)
		asked.Repository, asked.PullRequest = input.Repository, input.PullRequest
		plain, with := copies(t, input), copies(t, asked)
		for copied, object := range with {
			if object != plain[copied] || strings.Contains(object, schemaV013) {
				t.Errorf("%s, %s: asked for receipts with none posted, the object is\n%s\nand without asking\n%s", name, copied, object, plain[copied])
			}
		}
	}
	for name, input := range goldenStates(t) {
		if !isReceiptState(name) || input.Receipt == nil {
			continue
		}
		bare := input
		bare.Receipt = nil
		plain := copies(t, bare)
		for copied, object := range copies(t, input) {
			block, err := ParseResult(fenced(object))
			if err != nil {
				t.Fatalf("%s, %s: %v", name, copied, err)
			}
			block.Receipts, block.Schema = nil, schemaV09
			again, err := block.encode()
			if err != nil {
				t.Fatal(err)
			}
			if again != plain[copied] {
				t.Errorf("%s, %s: without its receipt the object is\n%s\nand the report without one writes\n%s", name, copied, again, plain[copied])
			}
		}
	}
}

// TestResultV013SchemaCopyMatchesItsDigest holds the copied result/v0.13
// schema to the file that adds it byte for byte, Block's keys to its
// properties, and a receipt's keys to the properties it states.
func TestResultV013SchemaCopyMatchesItsDigest(t *testing.T) {
	data, err := os.ReadFile(resultV013SchemaPath)
	if err != nil {
		t.Fatal(err)
	}
	if got := fmt.Sprintf("%x", sha256.Sum256(data)); got != resultV013SchemaSHA256 {
		t.Errorf("the copy's SHA-256 is %s, and result/v0.13 is %s", got, resultV013SchemaSHA256)
	}
	type properties struct {
		Properties map[string]json.RawMessage `json:"properties"`
	}
	var document struct {
		ID         string                     `json:"$id"`
		Properties map[string]json.RawMessage `json:"properties"`
		Defs       struct {
			ReceiptRead    properties `json:"receiptRead"`
			ReceiptRefused properties `json:"receiptRefused"`
			ReceiptComment properties `json:"receiptComment"`
			ReceiptFigure  properties `json:"receiptFigure"`
		} `json:"$defs"`
	}
	if err := json.Unmarshal(data, &document); err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(document.ID, "/result/v0.13/result.schema.json") {
		t.Errorf("the copied schema is %s, want result/v0.13", document.ID)
	}
	if fromSchema, fromBlock := slices.Sorted(maps.Keys(document.Properties)), allBlockKeys(); !slices.Equal(fromSchema, fromBlock) {
		t.Errorf("Block's keys %v, the schema's %v", fromBlock, fromSchema)
	}
	entry := slices.Sorted(slices.Values(slices.Concat(slices.Collect(maps.Keys(document.Defs.ReceiptRead.Properties)), []string{"reason"})))
	for name, pair := range map[string]struct {
		code   []string
		schema []string
	}{
		"a receipt":       {jsonKeys(reflect.TypeFor[BlockReceipt]()), entry},
		"its comment":     {jsonKeys(reflect.TypeFor[BlockReceiptComment]()), slices.Sorted(maps.Keys(document.Defs.ReceiptComment.Properties))},
		"its figure":      {jsonKeys(reflect.TypeFor[BlockReceiptFigure]()), slices.Sorted(maps.Keys(document.Defs.ReceiptFigure.Properties))},
		"a refused entry": {[]string{"comment", "reason"}, slices.Sorted(maps.Keys(document.Defs.ReceiptRefused.Properties))},
	} {
		if !slices.Equal(pair.code, pair.schema) {
			t.Errorf("%s: the code writes %v, result/v0.13 states %v", name, pair.code, pair.schema)
		}
	}
}

// TestAnObjectWithAReceiptIsWrittenAsV013 holds every golden state's objects
// to one rule: an object is result/v0.13 exactly when the report holds a
// receipt, read or refused, and then it holds to that version's copied
// schema, beside an estimate, a pack and a run_p95 result too.
func TestAnObjectWithAReceiptIsWrittenAsV013(t *testing.T) {
	schema := compileSchemaFile(t, resultV013SchemaPath)
	held := 0
	states := goldenStates(t)
	for name, base := range map[string]Input{"infrastructure": states["infrastructure"], "pack-rule-warns": states["pack-rule-warns"], "p95-over": states["p95-over"], "no-figures-agent_skipped": states["no-figures-agent_skipped"]} {
		for _, reason := range []ReceiptReason{"", ReceiptOtherBranch} {
			stated := readReceipt()
			if reason != "" {
				stated = refusedReceipt(reason)
			}
			states[name+" with a receipt "+string(reason)] = withReceipt(base, stated)
		}
	}
	for name, input := range states {
		objects := copies(t, input)
		for copied, object := range objects {
			var block Block
			if err := json.Unmarshal([]byte(object), &block); err != nil {
				t.Fatal(err)
			}
			if (block.Schema == schemaV013) != (input.receipt() != nil) {
				t.Errorf("%s, %s: written as %s, holding a receipt %t", name, copied, block.Schema, input.receipt() != nil)
			}
			if block.Schema != schemaV013 {
				continue
			}
			held++
			if err := resultSchemaErrors(schema, object); err != nil {
				t.Errorf("%s, %s breaks result/v0.13: %v\n%s", name, copied, err, object)
			}
		}
	}
	if held < 20 {
		t.Fatalf("%d objects written as result/v0.13", held)
	}
}

// TestV013ObjectsReadAndWriteAgain reads each receipt state's object as
// costroid approve and costroid result do, writes it again byte for byte, and
// repeats it under an approval with the receipt kept and the title the
// report's own. An object of an earlier version that holds receipts, and a
// result/0.13 object that holds none, are refused.
func TestV013ObjectsReadAndWriteAgain(t *testing.T) {
	states := goldenStates(t)
	for _, name := range []string{"receipt", "receipt-refused"} {
		object, err := Result(states[name])
		if err != nil {
			t.Fatal(err)
		}
		block, err := ParseResult(fenced(object))
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if again, err := block.encode(); err != nil || again != object {
			t.Errorf("%s: written again as\n%s\nwant\n%s (%v)", name, again, object, err)
		}
		output, err := Reconclude(block, nil, AmountsShown, exampleCheckRun)
		if err != nil {
			t.Fatal(err)
		}
		shown := states[name]
		shown.Amounts = AmountsShown
		if !strings.Contains(output.Text, `"receipts": [`) || output.Title != VerdictSentence(shown) {
			t.Errorf("%s: the repeated check lost its receipt, or its title %q", name, output.Title)
		}
	}
	read, err := Result(states["receipt"])
	if err != nil {
		t.Fatal(err)
	}
	plain, err := Result(states["neutral-shown"])
	if err != nil {
		t.Fatal(err)
	}
	for name, refused := range map[string]struct{ object, says string }{
		"a 0.12 object that holds receipts": {strings.Replace(read, `"`+schemaV013+`"`, `"`+schemaV012+`"`, 1), "which only costroid.result/0.13 defines"},
		"a 0.13 object that holds none":     {strings.Replace(plain, `"`+schemaV09+`"`, `"`+schemaV013+`"`, 1), "holds nothing only costroid.result/0.13 defines"},
	} {
		if _, err := ParseResult(fenced(refused.object)); err == nil || !strings.Contains(err.Error(), refused.says) {
			t.Errorf("%s: %v", name, err)
		}
	}
}

// TestEveryVersionAReleaseWritesIsRead holds comment.ParseResult, which
// costroid approve, costroid result, the MCP server and serve's webhook read
// through, to every version this release writes: an object of each, the
// receipt's version with its receipt, is read and written again byte for
// byte.
func TestEveryVersionAReleaseWritesIsRead(t *testing.T) {
	states := goldenStates(t)
	examples := map[string]Input{
		schemaV09: states["neutral-shown"], schema: states["infrastructure"], schemaV011: states["p95-over"],
		schemaV012: states["pack-rule-warns"], schemaV013: states["receipt"], schemaV014: states["demoted-run-budget"],
	}
	for _, version := range WrittenSchemas() {
		input, known := examples[version]
		if !known {
			t.Errorf("%s: no golden state writes it", version)
			continue
		}
		object, err := Result(input)
		if err != nil {
			t.Fatal(err)
		}
		block, err := ParseResult(fenced(object))
		if err != nil || block.Schema != version {
			t.Errorf("%s: read as %s: %v", version, block.Schema, err)
			continue
		}
		if again, err := block.encode(); err != nil || again != object {
			t.Errorf("%s: not written again byte for byte: %v", version, err)
		}
	}
}

// TestTheReceiptLineSaysWhoStatedItAndThatNothingCheckedIt reads the line of
// each receipt state, and of each refusal, in the words the comment, the
// check and the job summary share.
func TestTheReceiptLineSaysWhoStatedItAndThatNothingCheckedIt(t *testing.T) {
	states := receiptStates(goldenStates(t)["neutral-shown"])
	want := map[string]string{
		"receipt":          "Developer-machine sessions, unchecked: USD 11.4619213 at list (List-equivalent, not billed), 2 sessions of Claude Code from 2026-11-18 to 2026-11-20 (UTC), as `octocat` stated them in [a comment](https://github.com/example/widgets/pull/7#issuecomment-2471000001) on 2026-11-20. No budget reads this figure, and it is not added to the run's.",
		"receipt-withheld": "Developer-machine sessions, unchecked: 2 sessions of Claude Code from 2026-11-18 to 2026-11-20 (UTC), priced at list (List-equivalent, not billed), as `octocat` stated them in [a comment](https://github.com/example/widgets/pull/7#issuecomment-2471000001) on 2026-11-20. No budget reads this figure, and it is not added to the run's. Amounts are withheld here.",
		"receipt-refused":  "No developer-machine figure (receipt_edited): the receipt `octocat` posted was edited after it was posted, so its sessions were not read. A new count is a new comment. The verdict reads no receipt.",
		"receipt-none":     "No developer-machine sessions stated for this pull request. The figures here are the CI run's alone.",
	}
	for name, line := range want {
		r := renderState(t, states[name])
		for surface, text := range map[string]string{"comment": r.comment, "summary": r.summary, "check": r.check} {
			if !strings.Contains(text, "\n"+line+"\n") {
				t.Errorf("%s, %s lacks the line %q", name, surface, line)
			}
		}
	}
	for reason, said := range map[ReceiptReason]string{
		ReceiptOtherBranch: "No developer-machine figure (receipt_other_branch): the receipt `octocat` posted names another branch than the pull request's, so its sessions were not read. The verdict reads no receipt.",
		ReceiptInvalid:     "No developer-machine figure (receipt_invalid): the comment `octocat` posted is not a valid receipt, and `costroid receipt` prints a valid one, so its sessions were not read. The verdict reads no receipt.",
		ReceiptsOff:        "No developer-machine figure (receipts_off): `costroid.yml` on the base branch turns receipts off, so the receipt `octocat` posted was not read. The verdict reads no receipt.",
	} {
		if got := withReceipt(Input{}, refusedReceipt(reason)).receiptLine(true); got != said {
			t.Errorf("%s reads %q", reason, got)
		}
	}
	held := readReceipt()
	held.Held = 1
	if got := withReceipt(Input{}, held).receiptLine(true); !strings.HasSuffix(got, " 1 session the run's own rows also hold is left out of it.") {
		t.Errorf("a held session: %q", got)
	}
}

// TestARowsFileThatStatesAnOriginIsRefused reads a rows file whose second
// row states costroid/origin, with each value a job could write: the file is
// refused on that line as a row that is not valid, so the run's own rows
// never vanish from its figure by a tag. The report then states no figure
// for invalid_row, with the line that names the tag, and its machine block
// is the one any invalid row gives.
func TestARowsFileThatStatesAnOriginIsRefused(t *testing.T) {
	run := sampleFields().line()
	for _, value := range []string{`"developer_machine"`, `"ci_runner"`, `null`, `7`} {
		tagged := strings.Replace(run, `"team":7}`, `"team":7,"costroid/origin":`+value+`}`, 1)
		ledger, err := ReadRows(strings.NewReader(run + "\n" + tagged))
		var fault *RowError
		if !errors.As(err, &fault) || fault.Line != 2 || !errors.Is(err, policy.ErrOriginRow) || ledger.Rows != 0 {
			t.Errorf("costroid/origin %s: %+v, %v", value, ledger, err)
		}
	}
	states := goldenStates(t)
	origin, plain := states["no-figures-invalid_row-origin"], states["no-figures-invalid_row"]
	body, err := Comment(origin)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(body, "\n"+originRowLine+"\n") || VerdictSentence(origin) != VerdictSentence(plain) {
		t.Errorf("the refusal's comment:\n%s", body)
	}
	if mine, theirs := copies(t, origin), copies(t, plain); !maps.Equal(mine, theirs) {
		t.Errorf("the refusal's object differs from any invalid row's:\n%v\n%v", mine, theirs)
	}
}
