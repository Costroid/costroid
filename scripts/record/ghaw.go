package main

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"path"
	"strings"
)

const (
	// ghawSchemaPrefix starts the _schema of every token-usage record.
	ghawSchemaPrefix = "token-usage/v"
	// ghawUsageFile is the file gh-aw's API proxy writes, one record per
	// model request, and the name it takes in a recording.
	ghawUsageFile = "token-usage.jsonl"
)

// collectGhAw copies the token-usage.jsonl of a gh-aw artifact that gh run
// download wrote to src.home. The agent artifact keeps it under
// sandbox/firewall/logs/api-proxy-logs/ and older runs under
// firewall-audit-logs/api-proxy-logs/, so every file of that name is found.
// Copies that differ are refused, since the run's records cannot be told apart.
func collectGhAw(e *agentEntry, c *collected, src source, _, _ []byte) error {
	if src.stream != "" || src.executionFile != "" {
		return fmt.Errorf("%s leaves no stream or execution file: give --home the downloaded artifact's directory alone", e.name)
	}
	var found []string
	err := fs.WalkDir(src.home, ".", func(name string, entry fs.DirEntry, err error) error {
		if err == nil && entry.Type().IsRegular() && path.Base(name) == ghawUsageFile {
			found = append(found, name)
		}
		return err
	})
	if err != nil {
		return err
	}
	if len(found) == 0 {
		return errors.New("no token-usage.jsonl under --home: download the run's agent artifact with gh run download RUN -n agent")
	}
	data, err := fs.ReadFile(src.home, found[0])
	if err != nil {
		return err
	}
	for _, other := range found[1:] {
		copied, err := fs.ReadFile(src.home, other)
		if err != nil {
			return err
		}
		if !bytes.Equal(copied, data) {
			return fmt.Errorf("%s and %s differ: download one artifact", found[0], other)
		}
	}
	return c.add(ghawUsageFile, data)
}

// ghawVersion is the version the records' _schema states: 0.29.8 for
// token-usage/v0.29.8.
func ghawVersion(files []*recordingFile) string {
	for _, file := range files {
		for _, line := range file.lines {
			if version, ok := strings.CutPrefix(line.fieldText("_schema"), ghawSchemaPrefix); ok && version != "" {
				return version
			}
		}
	}
	return ""
}

// ghawTotal is gh-aw's own figure for the run: the AI credits it counted, in
// its own unit, which Costroid never converts.
func ghawTotal(files []*recordingFile) string {
	last := lastLine(files, ghawUsageFile, func(v *value) bool {
		total := v.field("ai_credits_total")
		return total != nil && total.isNumber()
	})
	if last == nil {
		return "No line states gh-aw's AI credits for the run."
	}
	return fmt.Sprintf("gh-aw's own figure for the run: %s AI credits, the `ai_credits_total` of the last line in `%s` that states it. Costroid does not convert credits.", last.field("ai_credits_total").raw, ghawUsageFile)
}
