package reconcile

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"slices"
)

const maxStoredRow = 1 << 20

// Merge writes the rows of existing with each fresh row in its key's place.
// A rerun of a day replaces that day, and rerunning unchanged figures writes
// the same bytes.
func Merge(existing io.Reader, fresh []Row, out io.Writer) error {
	lines := map[Key][]byte{}
	scanner := bufio.NewScanner(existing)
	scanner.Buffer(nil, maxStoredRow)
	for scanner.Scan() {
		line := bytes.TrimSpace(scanner.Bytes())
		if len(line) == 0 {
			continue
		}
		var key Key
		if err := json.Unmarshal(line, &key); err != nil {
			return fmt.Errorf("reconcile: read stored row: %w", err)
		}
		lines[key] = bytes.Clone(line)
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("reconcile: read stored rows: %w", err)
	}
	for _, row := range fresh {
		line, err := json.Marshal(row)
		if err != nil {
			return fmt.Errorf("reconcile: write row: %w", err)
		}
		lines[row.Key] = line
	}
	keys := make([]Key, 0, len(lines))
	for key := range lines {
		keys = append(keys, key)
	}
	slices.SortFunc(keys, compareKeys)
	for _, key := range keys {
		if _, err := fmt.Fprintf(out, "%s\n", lines[key]); err != nil {
			return fmt.Errorf("reconcile: write rows: %w", err)
		}
	}
	return nil
}
