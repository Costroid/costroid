package comment

import (
	"crypto/sha256"
	"encoding/hex"
	"flag"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
)

// updateBlocks rewrites the record of the machine blocks. Only a release that
// changes the result object reruns it, because a reader of older comments and
// costroid approve read the block as it is.
var updateBlocks = flag.Bool("update-blocks", false, "rewrite testdata/machine-blocks.sha256, for a release that changes the result object")

// The records the golden states are held to, apart from the goldens.
var (
	// factsFile holds every fact a golden comment states, one per line, after
	// its state. -update adds the facts a comment newly states and never drops
	// one: a change that removes a fact deletes its line by hand.
	factsFile = filepath.Join("testdata", "golden-facts.txt")
	// blocksFile holds the SHA-256 of each state's machine block, as the
	// comment, the check run and the result output carry it.
	blocksFile = filepath.Join("testdata", "machine-blocks.sha256")
)

// machineBlockStart opens the machine block's collapsed section in a comment.
var machineBlockStart = regexp.MustCompile(`(?m)^<details>\n<summary>Machine block`)

// readable is the comment without its machine block: everything a reader can
// see, folded or not.
func readable(body string) string {
	if at := machineBlockStart.FindStringIndex(body); at != nil {
		return body[:at[0]]
	}
	return body
}

// factPatterns find the facts a comment states: an amount beside its currency,
// a number standing alone, a code span (a model, an agent, a catalog, a
// setting or a reason), a link's target, a table cell that names an
// enforcement grade and its note or an attribution grade, and a policy rule's
// sentence.
var factPatterns = []*regexp.Regexp{
	regexp.MustCompile(`\b[A-Z]{3} [0-9]+(?:\.[0-9]+)?`),
	regexp.MustCompile(`(?m)(?:^|[\s(|])([0-9]+(?:[.,:-][0-9]+)*%?)(?:$|[\s).,:;|])`),
	regexp.MustCompile("`[^`\n]+`"),
	regexp.MustCompile(`\]\([^)\s]+\)`),
	regexp.MustCompile(`\| (?:none|provider|agent-flag|wrapper-watchdog|gateway-precheck): [^|\n]+ \|`),
	regexp.MustCompile(`\| (?:unattributed|windowed|exact) \|`),
	regexp.MustCompile(`(?m)(?:Run budget|Run cost threshold|Increase over baseline|Percent increase over baseline|Pull request budget|Monthly budget|Model allowlist|Attribution): .*?\.(?: |$)`),
}

// factsOf lists the facts a comment states, sorted and each once.
func factsOf(body string) []string {
	text := readable(body)
	found := map[string]bool{}
	for _, pattern := range factPatterns {
		for _, match := range pattern.FindAllStringSubmatch(text, -1) {
			found[strings.TrimSuffix(match[len(match)-1], " ")] = true
		}
	}
	for _, label := range basisLabels {
		if strings.Contains(text, label) {
			found[label] = true
		}
	}
	return slices.Sorted(maps.Keys(found))
}

// readRecord reads a record of "<key><sep><value>" lines into a map from key
// to values. A missing file reads as empty.
func readRecord(t *testing.T, path, sep string, valueFirst bool) map[string][]string {
	t.Helper()
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return map[string][]string{}
	}
	if err != nil {
		t.Fatal(err)
	}
	record := map[string][]string{}
	for line := range strings.Lines(string(data)) {
		key, value, ok := strings.Cut(strings.TrimSuffix(line, "\n"), sep)
		if !ok {
			t.Fatalf("%s: %q is not a record line", path, line)
		}
		if valueFirst {
			key, value = value, key
		}
		record[key] = append(record[key], value)
	}
	return record
}

// TestNoFactLeavesTheComment holds every fact each golden comment states, as
// testdata/golden-facts.txt records them, to the comment the state renders
// now, folded or not. Moving a line under a fold keeps its facts. Dropping one
// fails here, so a figure, a grade, a basis or a reason never leaves the
// comment without a line removed from the record by hand.
func TestNoFactLeavesTheComment(t *testing.T) {
	states := goldenStates(t)
	recorded := readRecord(t, factsFile, "\t", false)
	if len(recorded) == 0 && !*update {
		t.Fatalf("%s records no fact, rerun with -update", factsFile)
	}
	var lines []string
	for _, name := range slices.Sorted(maps.Keys(states)) {
		body, err := CommentWithLifetime(states[name], states[name].Lifetime)
		if err != nil {
			t.Fatal(err)
		}
		text := readable(body)
		facts := recorded[name]
		for _, fact := range facts {
			if !strings.Contains(text, fact) {
				t.Errorf("%s: the comment no longer states %q", name, fact)
			}
		}
		if *update {
			facts = slices.Compact(slices.Sorted(slices.Values(append(slices.Clone(facts), factsOf(body)...))))
		}
		for _, fact := range facts {
			lines = append(lines, name+"\t"+fact+"\n")
		}
	}
	for name := range recorded {
		if _, known := states[name]; !known && !*update {
			t.Errorf("%s records facts of %s, which is no golden state", factsFile, name)
		}
	}
	if *update {
		if err := os.WriteFile(factsFile, []byte(strings.Join(lines, "")), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// fencedBlock is the result object inside a comment's machine block.
var fencedBlock = regexp.MustCompile("(?s)```json\n.*?\n```")

// machineBlocks are the three copies of a state's result object: the comment's,
// the check run's text, and the result output.
func machineBlocks(t *testing.T, input Input) map[string]string {
	t.Helper()
	r := renderState(t, input)
	output, err := Check(input)
	if err != nil {
		t.Fatal(err)
	}
	blocks := map[string]string{goldenComment: fencedBlock.FindString(r.comment), goldenCheck: output.Text}
	for _, out := range r.outputs {
		if out.Name == "result" {
			blocks[goldenOutputs] = out.Value
		}
	}
	return blocks
}

// TestMachineBlockIsUnchanged holds every golden state's machine block, in the
// comment, the check run and the result output, to the SHA-256 that
// testdata/machine-blocks.sha256 records for it, byte for byte. A reader of
// older comments and costroid approve read that block, so a change to how the
// comment reads never changes it.
func TestMachineBlockIsUnchanged(t *testing.T) {
	recorded := readRecord(t, blocksFile, "  ", true)
	var lines []string
	states := goldenStates(t)
	for _, name := range slices.Sorted(maps.Keys(states)) {
		blocks := machineBlocks(t, states[name])
		for _, file := range slices.Sorted(maps.Keys(blocks)) {
			key := name + "/" + file
			if blocks[file] == "" {
				t.Errorf("%s holds no machine block", key)
				continue
			}
			sum := sha256.Sum256([]byte(blocks[file]))
			digest := hex.EncodeToString(sum[:])
			lines = append(lines, fmt.Sprintf("%s  %s\n", digest, key))
			if *updateBlocks {
				continue
			}
			if want := recorded[key]; len(want) != 1 || want[0] != digest {
				t.Errorf("%s: the machine block hashes to %s, the record holds %v", key, digest, want)
			}
		}
	}
	if *updateBlocks {
		if err := os.WriteFile(blocksFile, []byte(strings.Join(lines, "")), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	if len(recorded) != len(lines) {
		t.Errorf("%s records %d blocks, the states render %d", blocksFile, len(recorded), len(lines))
	}
}
