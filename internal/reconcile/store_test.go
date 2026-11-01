package reconcile

import (
	"bytes"
	"strings"
	"testing"
)

func merge(t *testing.T, existing string, fresh []Row) string {
	t.Helper()
	var out bytes.Buffer
	if err := Merge(strings.NewReader(existing), fresh, &out); err != nil {
		t.Fatal(err)
	}
	return out.String()
}

func TestRerunningAPassWritesTheSameBytes(t *testing.T) {
	fresh := pass(t, readRows(t, anthropicRows(), ""), anthropicReplay(t, "usage.json", "cost-contract-rate.json"), settledNow).Rows
	first := merge(t, "", fresh)
	if second := merge(t, first, fresh); second != first {
		t.Errorf("rerun moved the file:\n%s\n%s", first, second)
	}
	if lines := strings.Count(first, "\n"); lines != 1 {
		t.Errorf("%d lines, want 1", lines)
	}
}

func TestARestatedDayReplacesItsRowAndNoOther(t *testing.T) {
	other := Row{Key: Key{SourceAnthropic, "apikey_01Other", day}, State: Matched}
	first := merge(t, "", []Row{other})
	early := pass(t, readRows(t, anthropicRows(), ""), anthropicReplay(t, "usage.json", "cost-at-list.json"), settledNow).Rows
	second := merge(t, first, early)
	restated := pass(t, readRows(t, anthropicRows(), ""), anthropicReplay(t, "usage.json", "cost-contract-rate.json"), settledNow).Rows
	third := merge(t, second, restated)
	if lines := strings.Count(third, "\n"); lines != 2 {
		t.Fatalf("%d lines, want 2:\n%s", lines, third)
	}
	if !strings.Contains(third, `"provider":"0.7695"`) || strings.Contains(third, `"provider":"0.81"`) {
		t.Errorf("the restated figure did not replace the first:\n%s", third)
	}
	if !strings.Contains(third, "apikey_01Other") {
		t.Errorf("another key's row went:\n%s", third)
	}
}

func TestAPendingDayIsReplacedOnceTheProviderSettlesIt(t *testing.T) {
	waiting := pass(t, readRows(t, anthropicRows(), ""), anthropicReplay(t, "usage-refreshing.json", "cost-refreshing.json"), settledNow).Rows
	settled := pass(t, readRows(t, anthropicRows(), ""), anthropicReplay(t, "usage.json", "cost-at-list.json"), settledNow).Rows
	file := merge(t, merge(t, "", waiting), settled)
	if strings.Contains(file, `"state":"pending"`) || !strings.Contains(file, `"state":"matched"`) {
		t.Errorf("file:\n%s", file)
	}
}

func TestAStoredRowThatIsNotJSONIsAnError(t *testing.T) {
	var out bytes.Buffer
	if err := Merge(strings.NewReader("not json\n"), nil, &out); err == nil {
		t.Error("a broken store was overwritten")
	}
}
