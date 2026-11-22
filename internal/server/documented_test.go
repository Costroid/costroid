package server

import (
	"bytes"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/Costroid/costroid/internal/docstest"
	"github.com/Costroid/costroid/internal/egress"
)

// TestEveryDocumentedOutputIsWhatTheCommandPrints holds the table
// docs/ledger.md shows for costroid egress report to the report's own
// printer. The page's lines, read back as the counters they state, print as
// the page's block, byte for byte, and each outcome is one the service
// counts.
func TestEveryDocumentedOutputIsWhatTheCommandPrints(t *testing.T) {
	shown := docstest.At(t, "../..", docstest.Place{Page: "docs/ledger.md", Heading: "The report"})
	if len(shown) != 1 {
		t.Fatalf("docs/ledger.md shows %d reports", len(shown))
	}
	lines := strings.Split(shown[0], "\n")
	var counts []egress.Count
	for _, line := range lines[1:] {
		cells := strings.Fields(line)
		if len(cells) != 4 {
			t.Fatalf("the page's line %q is not a day, a host, an outcome and a count", line)
		}
		count, err := strconv.ParseInt(cells[3], 10, 64)
		if err != nil || !slices.Contains([]string{egress.Completed, egress.Failed, egress.Refused}, cells[2]) {
			t.Fatalf("the page's line %q states an outcome the service does not count, or no count: %v", line, err)
		}
		counts = append(counts, egress.Count{Day: cells[0], Host: cells[1], Outcome: cells[2], Count: count})
	}
	if len(counts) == 0 {
		t.Fatal("the page's report holds no line")
	}
	var printed bytes.Buffer
	if err := printEgress(&printed, counts); err != nil {
		t.Fatal(err)
	}
	if printed.String() != shown[0]+"\n" {
		t.Errorf("the page shows\n%s\nand egress report prints\n%s", shown[0], printed.String())
	}
}
