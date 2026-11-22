package receipt

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/santhosh-tekuri/jsonschema/v6"
)

// contractDir holds receipt/v0.1 of Costroid/costroid-schemas, copied: the
// schema, the accepted examples, and the rejected ones under rejected/.
var contractDir = filepath.Join("testdata", "receipt-v0.1")

// contractSchemaSHA256 is the SHA-256 of receipt/v0.1/receipt.schema.json as
// the schemas' branch for that version holds it, before it is published:
//
//	git show receipt-contracts:receipt/v0.1/receipt.schema.json | sha256sum
const contractSchemaSHA256 = "382ffaf62cdeca4cc8ffb7e0257f6157408a3f862fbc944e9bcb89678748d1ee"

// body is a receipt object as a comment carries it.
func body(object string) string {
	return Marker + "\n" + fenceOpen + strings.TrimSpace(object) + fenceClose + "\n"
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func compileContract(t *testing.T) *jsonschema.Schema {
	t.Helper()
	file, err := os.Open(filepath.Join(contractDir, "receipt.schema.json"))
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	document, err := jsonschema.UnmarshalJSON(file)
	if err != nil {
		t.Fatal(err)
	}
	compiler := jsonschema.NewCompiler()
	if err := compiler.AddResource("receipt.schema.json", document); err != nil {
		t.Fatal(err)
	}
	schema, err := compiler.Compile("receipt.schema.json")
	if err != nil {
		t.Fatal(err)
	}
	return schema
}

func TestReceiptContractCopyMatchesItsDigest(t *testing.T) {
	sum := sha256.Sum256([]byte(readFile(t, filepath.Join(contractDir, "receipt.schema.json"))))
	if got := hex.EncodeToString(sum[:]); got != contractSchemaSHA256 {
		t.Errorf("the copy's SHA-256 is %s, and receipt/v0.1 is %s", got, contractSchemaSHA256)
	}
}

// TestEveryContractExampleIsReadAsTheContractSays reads each example of the
// contract as a comment's body: every accepted example is read, and every
// rejected one, Codex's among them, is refused.
func TestEveryContractExampleIsReadAsTheContractSays(t *testing.T) {
	accepted, _ := filepath.Glob(filepath.Join(contractDir, "*.json"))
	read := 0
	for _, path := range accepted {
		if filepath.Base(path) == "receipt.schema.json" {
			continue
		}
		if _, err := Parse(body(readFile(t, path))); err != nil {
			t.Errorf("%s: %v", filepath.Base(path), err)
			continue
		}
		read++
	}
	rejected, _ := filepath.Glob(filepath.Join(contractDir, "rejected", "*.json"))
	if read != 3 || len(rejected) != 32 {
		t.Fatalf("%d accepted examples read and %d rejected ones found, want 3 and 32", read, len(rejected))
	}
	for _, path := range rejected {
		if _, err := Parse(body(readFile(t, path))); err == nil {
			t.Errorf("%s was read", filepath.Base(path))
		}
	}
}

// TestAReceiptThatStatesACostIsRefused reads, as the newest receipt of a
// pull request, each of the contract's examples that state an amount: a cost,
// a price, a currency, and a cost as a token class. Each is refused as not a
// valid receipt, and none of its sessions is read.
func TestAReceiptThatStatesACostIsRefused(t *testing.T) {
	for _, name := range []string{"states-a-cost.json", "states-a-price.json", "states-a-currency.json", "states-a-cost-as-a-token-class.json"} {
		posted := Posted{ID: 1, Body: body(readFile(t, filepath.Join(contractDir, "rejected", name))), CreatedAt: created, UpdatedAt: created}
		reading, found := Newest([]Posted{posted}, "fix/retry-backoff", true)
		if !found || reading.Refused != Invalid || len(reading.Receipt.Sessions) != 0 {
			t.Errorf("%s: refused %q with %d sessions read", name, reading.Refused, len(reading.Receipt.Sessions))
		}
	}
	stated := strings.Replace(readFile(t, filepath.Join(contractDir, "one-session.json")), `"output": "88214"`, `"output": "88214", "cost": "4.21"`, 1)
	if _, err := Parse(body(stated)); err == nil || !strings.Contains(err.Error(), `"cost"`) {
		t.Errorf("a cost beside the tokens: %v", err)
	}
}

// TestABodyOutsideItsFormIsRefused holds the four faults the schema cannot
// see, and the comment's form, to a refusal.
func TestABodyOutsideItsFormIsRefused(t *testing.T) {
	object := strings.TrimSpace(readFile(t, filepath.Join(contractDir, "claude-code-on-a-branch.json")))
	good := body(object)
	if _, err := Parse(good); err != nil {
		t.Fatal(err)
	}
	if _, err := Parse(strings.TrimSuffix(good, "\n")); err != nil {
		t.Errorf("a body with no line feed after the fence: %v", err)
	}
	faults := map[string]string{
		"text before the marker":      "Here it is.\n" + good,
		"text after the fence":        good + "Thanks.\n",
		"a second fenced block":       strings.TrimSuffix(good, "\n") + "\n```json\n{}\n```\n",
		"no marker":                   strings.TrimPrefix(good, Marker+"\n"),
		"Costroid's own marker":       strings.Replace(good, Marker, "<!-- costroid:comment -->", 1),
		"a key stated twice":          body(strings.Replace(object, `"branch": "fix/retry-backoff",`, `"branch": "fix/retry-backoff", "branch": "main",`, 1)),
		"a key twice in an entry":     body(strings.Replace(object, `"requests": 214,`, `"requests": 214, "requests": 1,`, 1)),
		"an entry twice":              body(strings.Replace(object, `"model": "example-small-1"`, `"model": "example-large-1"`, 1)),
		"a first day after its last":  body(strings.Replace(object, `"first_day": "2026-11-19"`, `"first_day": "2026-11-20"`, 1)),
		"a last day after written":    body(strings.Replace(object, `"written": "2026-11-20"`, `"written": "2026-11-19"`, 1)),
		"a last day before the bound": body(strings.Replace(object, `"since": "2026-11-18"`, `"since": "2026-11-20"`, 1)),
		"a day of no calendar":        body(strings.Replace(object, `"written": "2026-11-20"`, `"written": "2026-02-30"`, 1)),
		"requests as a fraction":      body(strings.Replace(object, `"requests": 214,`, `"requests": 214.0,`, 1)),
		"two objects":                 body(object + "\n" + object),
		"past the size of a comment":  body("{" + strings.Repeat(" ", MaxBody) + strings.TrimPrefix(object, "{")),
	}
	for name, faulty := range faults {
		if _, err := Parse(faulty); err == nil {
			t.Errorf("%s was read", name)
		}
	}
}

// TestAReceiptPastedInABrowserIsRead reads a receipt's body in each case of
// the contract's table of bodies: CRLF line endings, some or all, are read as
// LF, a missing or doubled line feed after the closing fence is read, and
// blank lines there are ignored. A carriage return no line feed follows, a
// line of spaces or text after the fence, a space or a tab at the end of the
// marker or a fence, an opening fence that is not json, and a first line
// that is not the marker are each refused. The digest of a receipt read in
// CRLF is the digest of its body as posted.
func TestAReceiptPastedInABrowserIsRead(t *testing.T) {
	object := strings.TrimSpace(readFile(t, filepath.Join(contractDir, "one-session.json")))
	good := body(object)
	crlf := strings.ReplaceAll(good, "\n", "\r\n")
	firstHalf := strings.Index(good, `"sessions"`)
	mixed := strings.ReplaceAll(good[:firstHalf], "\n", "\r\n") + good[firstHalf:]
	read := map[string]string{
		"lines that end in LF":                good,
		"lines that end in CRLF":              crlf,
		"some lines in CRLF and some in LF":   mixed,
		"no line feed after the fence":        strings.TrimSuffix(good, "\n"),
		"no CRLF after the fence":             strings.TrimSuffix(crlf, "\r\n"),
		"blank lines after the fence in LF":   good + "\n\n",
		"blank lines after the fence in CRLF": crlf + "\r\n\r\n",
		"blank lines after the fence, both":   good + "\r\n\n",
	}
	for name, text := range read {
		if _, err := Parse(text); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
	refused := map[string]string{
		"a carriage return alone":                 strings.Replace(good, "\n", "\r", 1),
		"a carriage return inside the object":     strings.Replace(good, `"branch"`, "\"bra\rnch\"", 1),
		"a carriage return after the fence":       good + "\r",
		"a line of spaces after the fence":        good + "   \n",
		"text after the fence":                    good + "Thanks.\n",
		"a second fenced block":                   good + "```json\n{}\n```\n",
		"a space at the end of the marker":        strings.Replace(good, Marker+"\n", Marker+" \n", 1),
		"a tab at the end of the opening fence":   strings.Replace(good, "```json\n", "```json\t\n", 1),
		"a space at the end of the closing fence": strings.TrimSuffix(good, "```\n") + "``` \n",
		"an opening fence that is not json":       strings.Replace(good, "```json\n", "```\n", 1),
		"a blank first line":                      "\n" + good,
		"a blank first line in CRLF":              "\r\n" + crlf,
	}
	for name, text := range refused {
		if _, err := Parse(text); err == nil {
			t.Errorf("%s was read", name)
		}
	}
	at := time.Date(2026, 11, 20, 16, 42, 7, 0, time.UTC)
	reading, _ := Newest([]Posted{{ID: 1, Body: crlf, CreatedAt: at, UpdatedAt: at}}, "fix/retry-backoff", true)
	sum := sha256.Sum256([]byte(crlf))
	if reading.Refused != "" || reading.SHA256 != hex.EncodeToString(sum[:]) {
		t.Errorf("a receipt in CRLF: refused %q, digest %s", reading.Refused, reading.SHA256)
	}
}

// TestAPrintedReceiptHoldsToTheContract writes a receipt that holds every
// optional field and holds its object to the copied schema, and reads it back
// to the same receipt.
func TestAPrintedReceiptHoldsToTheContract(t *testing.T) {
	schema := compileContract(t)
	stated := sampleReceipt()
	stated.Sessions[0].AssumedTTL = []string{"1h"}
	printed, err := stated.Body()
	if err != nil {
		t.Fatal(err)
	}
	object := strings.TrimSuffix(strings.TrimPrefix(printed, Marker+"\n"+fenceOpen), fenceClose+"\n")
	instance, err := jsonschema.UnmarshalJSON(strings.NewReader(object))
	if err != nil {
		t.Fatal(err)
	}
	if err := schema.Validate(instance); err != nil {
		t.Errorf("the printed receipt breaks receipt/v0.1: %v\n%s", err, object)
	}
	again, err := Parse(printed)
	if err != nil {
		t.Fatal(err)
	}
	if reprinted, _ := again.Body(); reprinted != printed {
		t.Errorf("read back and printed again:\n%s\nwant\n%s", reprinted, printed)
	}
}

var created = time.Date(2026, 11, 20, 16, 42, 7, 0, time.UTC)

// sampleReceipt is a receipt of two sessions on the branch fix/retry-backoff.
// Every name and count is synthetic.
func sampleReceipt() Receipt {
	since, tier := "2026-11-18", "standard"
	return Receipt{
		Schema: Schema, Branch: "fix/retry-backoff", Written: "2026-11-21", Since: &since, Agents: []string{ClaudeCode},
		Sessions: []Session{
			{
				Agent: ClaudeCode, AgentVersion: "2.1.316", SessionID: "4f1c9a70-2b1e-4c55-9a1d-7be0a3c1d2ef", Model: "claude-sonnet-5-5",
				ServiceTier: &tier, FirstDay: "2026-11-18", LastDay: "2026-11-19", Requests: 214, LargestInput: "148230",
				Tokens: Tokens{InputUncached: "18402", CacheRead: "9120544", CacheWrite5m: "402118", CacheWrite1h: "0", Output: "220917"},
			},
			{
				Agent: ClaudeCode, AgentVersion: "2.1.319", SessionID: "b81d02e4-6c3a-4f0e-8d5b-19a4c7e2f360", Model: "claude-haiku-5-5",
				ServiceTier: &tier, FirstDay: "2026-11-20", LastDay: "2026-11-21", Requests: 96, LargestInput: "40112",
				Tokens: Tokens{InputUncached: "7311", CacheRead: "3904812", CacheWrite5m: "0", CacheWrite1h: "188040", Output: "88214"},
			},
		},
	}
}

func posted(id int64, at time.Time, edited time.Duration, stated Receipt) Posted {
	text, err := stated.Body()
	if err != nil {
		panic(err)
	}
	return Posted{ID: id, Body: text, CreatedAt: at, UpdatedAt: at.Add(edited)}
}

// TestAnEditedReceiptIsRefused reads a receipt whose comment was changed after
// it was posted: it is refused, none of its sessions is read, and the pin
// still names the comment and the digest of its body as the host returned it.
// A receipt changed in the same second it was posted is not edited.
func TestAnEditedReceiptIsRefused(t *testing.T) {
	edited := posted(7, created, 5*time.Second, sampleReceipt())
	reading, found := Newest([]Posted{edited}, "fix/retry-backoff", true)
	sum := sha256.Sum256([]byte(edited.Body))
	if !found || reading.Refused != Edited || len(reading.Receipt.Sessions) != 0 || reading.Comment.ID != 7 || reading.SHA256 != hex.EncodeToString(sum[:]) {
		t.Errorf("an edited receipt: %+v", reading)
	}
	unedited := posted(7, created, 0, sampleReceipt())
	if reading, _ := Newest([]Posted{unedited}, "fix/retry-backoff", true); reading.Refused != "" || len(reading.Receipt.Sessions) != 2 {
		t.Errorf("an unedited receipt: refused %q, %d sessions", reading.Refused, len(reading.Receipt.Sessions))
	}
}

// TestTheNewestReceiptDecides posts three receipts: the newest decides, by
// the time it was created and then by its id, whatever order the host lists
// them in. An older valid receipt is never read in place of a newer refused
// one.
func TestTheNewestReceiptDecides(t *testing.T) {
	older := sampleReceipt()
	older.Sessions = older.Sessions[:1]
	newest := sampleReceipt()
	comments := []Posted{
		posted(30, created.Add(time.Hour), 0, newest),
		posted(10, created, 0, older),
		posted(20, created.Add(time.Hour), 0, older),
	}
	reading, found := Newest(comments, "fix/retry-backoff", true)
	if !found || reading.Comment.ID != 30 || len(reading.Receipt.Sessions) != 2 {
		t.Errorf("the newest is comment %d with %d sessions, want 30 with 2", reading.Comment.ID, len(reading.Receipt.Sessions))
	}
	refused := posted(40, created.Add(2*time.Hour), time.Minute, older)
	if reading, _ := Newest(append(comments, refused), "fix/retry-backoff", true); reading.Comment.ID != 40 || reading.Refused != Edited {
		t.Errorf("a newer edited receipt: comment %d, refused %q", reading.Comment.ID, reading.Refused)
	}
	if _, found := Newest(nil, "fix/retry-backoff", true); found {
		t.Error("no comment gave a receipt")
	}
}

// TestAReceiptThatNamesAnotherBranchIsRefused reads a valid receipt for one
// branch on a pull request from another: it is refused and none of its
// sessions is read. Where costroid.yml turns receipts off, the receipt is
// refused for that before it is read at all, edited or not.
func TestAReceiptThatNamesAnotherBranchIsRefused(t *testing.T) {
	comment := posted(7, created, 0, sampleReceipt())
	for branch, want := range map[string]Refusal{"fix/retry-backoff": "", "fix/retry": OtherBranch, "main": OtherBranch, "fix/retry-backoff2": OtherBranch} {
		reading, _ := Newest([]Posted{comment}, branch, true)
		if reading.Refused != want || (want != "") != (len(reading.Receipt.Sessions) == 0) {
			t.Errorf("on %s: refused %q with %d sessions, want %q", branch, reading.Refused, len(reading.Receipt.Sessions), want)
		}
	}
	for _, off := range []Posted{comment, posted(8, created, time.Minute, sampleReceipt()), {ID: 9, Body: "not a receipt", CreatedAt: created, UpdatedAt: created}} {
		if reading, _ := Newest([]Posted{off}, "main", false); reading.Refused != Off {
			t.Errorf("receipts off: comment %d refused %q", off.ID, reading.Refused)
		}
	}
}

// TestBranchNamesAreHeldToTheirForm holds ValidBranch to git's rules over the
// contract's narrow form.
func TestBranchNamesAreHeldToTheirForm(t *testing.T) {
	for name, want := range map[string]bool{
		"main": true, "fix/retry-backoff": true, "release/1.0.0": true, "a+b_c-d.e": true,
		"HEAD": false, "": false, "/main": false, "main/": false, "fix/../main": false, "main.": false,
		"main.lock": false, "a.lock/b": false, "a/.hidden": false, "a b": false, "a:b": false, "a@{1}": false,
		strings.Repeat("a", 256): false, strings.Repeat("a", 255): true,
	} {
		if got := ValidBranch(name); got != want {
			t.Errorf("%q: %v, want %v", name, got, want)
		}
	}
}

// TestAReceiptStatesNoAmountWhateverItHolds prints a receipt and finds no
// amount, price or currency key in it.
func TestAReceiptStatesNoAmountWhateverItHolds(t *testing.T) {
	printed, err := sampleReceipt().Body()
	if err != nil {
		t.Fatal(err)
	}
	if found := amountKey.FindString(printed); found != "" || strings.Contains(printed, "USD") {
		t.Errorf("the receipt states an amount, at %q:\n%s", found, printed)
	}
}

// amountKey is a key that would state an amount.
var amountKey = regexp.MustCompile(`"[a-z_]*(cost|price|currency|amount)[a-z_]*"\s*:`)
