package receipt

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/santhosh-tekuri/jsonschema/v6"

	"github.com/Costroid/costroid/internal/catalog"
	"github.com/Costroid/costroid/internal/focus"
	"github.com/Costroid/costroid/internal/policy"
)

// The row contract copied: focus-row/v0.6 and the tag set 0.5 it names, each
// held to its SHA-256 as the schemas' branch for those versions holds it,
// before they are published.
var (
	rowSchemaPath    = filepath.Join("testdata", "rows", "row-v0.6.schema.json")
	tagsSchemaPath   = filepath.Join("testdata", "rows", "tags-v0.5.schema.json")
	rowSchemaSHA256  = "be932f546143c401a995138f44470b981544c5872957ed2ecb9128152283dcad"
	tagsSchemaSHA256 = "db6c0be0ea4fcb809464d189aef1edd6fddadf21855ebef631f03bb835acceed"
	tagsSchemaID     = "https://raw.githubusercontent.com/Costroid/costroid-schemas/main/tags/v0.5/tags.schema.json"
)

// pullTags are the tags a CI system states, and a CI run's id, which a row
// from a developer's machine never names.
var pullTags = map[string]string{
	focus.TagRepo: "github.com/example/widgets", focus.TagPR: "7",
	focus.TagCommitSHA: "9f2c6e1ab47d03f58c1e7726d0b4a93e5f618c2d", focus.TagWorkflowRunID: "19203384756",
}

func prices(t *testing.T) *catalog.Catalog {
	t.Helper()
	loaded, err := catalog.Load()
	if err != nil {
		t.Fatal(err)
	}
	return loaded
}

func compileRowContract(t *testing.T) *jsonschema.Schema {
	t.Helper()
	compiler := jsonschema.NewCompiler()
	for path, id := range map[string]string{rowSchemaPath: "row.schema.json", tagsSchemaPath: tagsSchemaID} {
		document, err := jsonschema.UnmarshalJSON(strings.NewReader(readFile(t, path)))
		if err != nil {
			t.Fatal(err)
		}
		if err := compiler.AddResource(id, document); err != nil {
			t.Fatal(err)
		}
	}
	schema, err := compiler.Compile("row.schema.json")
	if err != nil {
		t.Fatal(err)
	}
	return schema
}

func TestRowContractCopiesMatchTheirDigests(t *testing.T) {
	for path, want := range map[string]string{rowSchemaPath: rowSchemaSHA256, tagsSchemaPath: tagsSchemaSHA256} {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if sum := sha256.Sum256(data); hex.EncodeToString(sum[:]) != want {
			t.Errorf("%s: the copy's SHA-256 is %x, want %s", path, sum, want)
		}
	}
}

// TestReceiptRowsHoldToTheRowContract builds the rows of a receipt and holds
// each to focus-row/v0.6 and its tag set: costroid/origin developer_machine,
// the one value of each tag that goes with it, the session, the agent and the
// model, the pull request from the CI system, and no CI run.
func TestReceiptRowsHoldToTheRowContract(t *testing.T) {
	schema := compileRowContract(t)
	rows := mustRows(t, sampleReceipt()).rows
	if len(rows) != 8 {
		t.Fatalf("%d rows, want one per entry and class that holds tokens, 8", len(rows))
	}
	var lines bytes.Buffer
	writer := focus.NewWriter(&lines)
	for _, row := range rows {
		if err := writer.Write(row); err != nil {
			t.Fatal(err)
		}
		if row.Tags[policy.TagOrigin] != policy.OriginDeveloperMachine || row.Tags[focus.TagWorkflowRunID] != "" || row.Tags[focus.TagPR] != "7" {
			t.Errorf("a row's tags: %v", row.Tags)
		}
		if row.EffectiveCost.Sign() != 0 || row.ListCost.Sign() <= 0 || !strings.Contains(row.ChargeDescription, rowNote) {
			t.Errorf("a row of %s: EffectiveCost %s, ListCost %s, %q", row.SkuMeter, row.EffectiveCost, row.ListCost, row.ChargeDescription)
		}
	}
	for number, line := range strings.Split(strings.TrimSuffix(lines.String(), "\n"), "\n") {
		instance, err := jsonschema.UnmarshalJSON(strings.NewReader(line))
		if err != nil {
			t.Fatal(err)
		}
		if err := schema.Validate(instance); err != nil {
			t.Errorf("row %d breaks focus-row/v0.6: %v\n%s", number+1, err, line)
		}
	}
	first, err := json.Marshal(rows[0])
	if err != nil || !bytes.Contains(first, []byte(`"ChargePeriodStart":"2026-11-18T00:00:00Z"`)) || !bytes.Contains(first, []byte(`"ChargePeriodEnd":"2026-11-20T00:00:00Z"`)) {
		t.Errorf("the first row's period: %s", first)
	}
	if again := mustRows(t, sampleReceipt()).rows; again[3].RowID != rows[3].RowID {
		t.Error("the same receipt gave another row id")
	}
}

// TestAReceiptIsPricedAtTheCatalogAlone prices a receipt's counts at the
// pinned catalog: Claude Sonnet 5.5 at USD 2, 0.10, 2.50 and 10 per million
// input, cache read, five-minute write and output tokens, and Claude Haiku
// 5.5 at its tier up to 100,000 input tokens, USD 0.10, 0.01, 0.20 and 0.50
// for input, cache read, one-hour write and output. A largest input past
// that tier prices the entry at the tier above it. A model the catalog does
// not name takes its fallback, which is never zero.
func TestAReceiptIsPricedAtTheCatalogAlone(t *testing.T) {
	figures := Figures(mustRows(t, sampleReceipt()))
	if len(figures) != 1 || figures[0].Currency != "USD" || figures[0].Cost.String() != "4.28481762" || figures[0].Rows != 8 {
		t.Errorf("the receipt's figures are %+v, want USD 4.28481762 over 8 rows", figures)
	}
	past := sampleReceipt()
	past.Sessions[1].LargestInput = "148230"
	if figures := Figures(mustRows(t, past)); figures[0].Cost.String() != "4.7707945" {
		t.Errorf("past the 100,000-token tier the receipt reads %s, want 4.7707945", figures[0].Cost)
	}
	unknown := sampleReceipt()
	unknown.Sessions = unknown.Sessions[:1]
	unknown.Sessions[0].Model = "claude-unnamed-9"
	unknown.Sessions[0].Tokens = Tokens{InputUncached: "1000", CacheRead: "0", CacheWrite5m: "0", CacheWrite1h: "0", Output: "2000"}
	priced := mustRows(t, unknown)
	if figures := Figures(priced); figures[0].Cost.String() != "0.176" || !strings.Contains(priced.rows[0].ChargeDescription, focus.NoteFallback) {
		t.Errorf("a model the catalog does not name reads %s: %q", figures[0].Cost, priced.rows[0].ChargeDescription)
	}
}

func mustRows(t *testing.T, stated Receipt) Priced {
	t.Helper()
	rows, err := Rows(stated, prices(t), pullTags, nil)
	if err != nil {
		t.Fatal(err)
	}
	return rows
}

// TestFiguresAddNoRowThatIsNotAReceipts gives Figures a run's row beside a
// receipt's: the figure holds the receipt's rows alone.
func TestFiguresAddNoRowThatIsNotAReceipts(t *testing.T) {
	rows := mustRows(t, sampleReceipt()).rows
	run := rows[0]
	run.Tags = map[string]string{focus.TagAgent: "claude-code", focus.TagCostSource: "agent_stream"}
	figures := Figures(Priced{rows: append(rows, run)})
	if figures[0].Rows != 8 || figures[0].Cost.String() != "4.28481762" {
		t.Errorf("a run's row joined the receipt's figure: %+v", figures)
	}
}
