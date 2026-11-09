package main

import (
	"errors"
	"fmt"
	"io/fs"
	"regexp"
	"strings"

	"github.com/Costroid/costroid/internal/decimal"
)

const (
	agentOpenCode = "opencode"
	// openCodeStream is the stdout of opencode run --format json, as the
	// recording workflow saves it and as a recording names it.
	openCodeStream = "stream.jsonl"
	// openCodeVersionText is the recording workflow's file of opencode
	// --version output, and openCodeVersionFile the file a recording keeps
	// the version in.
	openCodeVersionText = "version.txt"
	openCodeVersionFile = "version.json"
	openCodeVersionKey  = "opencode_version"
)

// openCodePaths are the keys of opencode run --format json that a recording
// keeps: each line's type, time and session, a step's tokens, its reason and
// OpenCode's own price, a tool's name and status, and an error's class. The
// text, the tool's input and output, the snapshot hash and the call id are
// dropped.
var openCodePaths = []string{
	"type",
	"timestamp",
	"part",
	"part.type",
	"part.reason",
	"part.tokens",
	"part.tokens.total",
	"part.tokens.input",
	"part.tokens.output",
	"part.tokens.reasoning",
	"part.tokens.cache",
	"part.tokens.cache.read",
	"part.tokens.cache.write",
	"part.cost",
	"part.tool",
	"part.state",
	"part.state.status",
	"part.time",
	"part.time.start",
	"part.time.end",
	"error",
	"error.name",
	"error.data",
	"error.data.statusCode",
	openCodeVersionKey,
}

var (
	openCodeSessions = idKind{"OpenCode session", regexp.MustCompile(`^ses_fixture_([0-9]{4,})$`), "ses_fixture_%04d", false}
	openCodeParts    = idKind{"OpenCode part", regexp.MustCompile(`^prt_fixture_([0-9]{4,})$`), "prt_fixture_%04d", false}
)

// openCodeIDs are the keys of a stream line that hold an identifier.
var openCodeIDs = map[string]*idKind{
	"sessionID":      &openCodeSessions,
	"part.sessionID": &openCodeSessions,
	"part.id":        &openCodeParts,
	"part.messageID": &messageIDs,
}

var openCodeAllowlist = newAllowlist(openCodePaths, nil, openCodeIDs, nil)

// collectOpenCode copies a run's stream and its version. Given --stream, it
// takes that stream; otherwise it reads stream.jsonl in --home, the directory
// the recording workflow writes. The version is the first line of version.txt
// in --home, the output of opencode --version, kept as version.json. OpenCode
// keeps its session in SQLite, which a recording does not hold.
func collectOpenCode(_ *agentEntry, c *collected, src source, stream, _ []byte) error {
	if stream == nil {
		data, err := fs.ReadFile(src.home, openCodeStream)
		if errors.Is(err, fs.ErrNotExist) {
			return errors.New("no stream: give --stream the stdout of opencode run --format json, or --home the directory that holds stream.jsonl")
		}
		if err != nil {
			return err
		}
		if err := c.add(openCodeStream, data); err != nil {
			return err
		}
	}
	text, err := fs.ReadFile(src.home, openCodeVersionText)
	if err != nil {
		return fmt.Errorf("read the run's version: %w", err)
	}
	version, _, _ := strings.Cut(strings.TrimSpace(string(text)), "\n")
	if !versionDirectory.MatchString(version) {
		return fmt.Errorf("the first line of %s is not a version", openCodeVersionText)
	}
	return c.add(openCodeVersionFile, []byte(fmt.Sprintf("{%q:%q}\n", openCodeVersionKey, version)))
}

// openCodeVersion is the version a recording keeps in version.json.
func openCodeVersion(files []*recordingFile) string {
	for _, file := range files {
		if file.name != openCodeVersionFile {
			continue
		}
		for _, line := range file.lines {
			if version := line.fieldText(openCodeVersionKey); version != "" {
				return version
			}
		}
	}
	return ""
}

// openCodeTotal is OpenCode's own figure for the run: the sum of the cost of
// each step_finish line, each priced from OpenCode's own price list.
func openCodeTotal(files []*recordingFile) string {
	var total decimal.Decimal
	steps := 0
	for _, file := range files {
		if file.name != openCodeStream {
			continue
		}
		for _, line := range file.lines {
			if line.fieldText("type") != "step_finish" {
				continue
			}
			cost := line.field("part").field("cost")
			if cost == nil || !cost.isNumber() {
				return "A step_finish line in `stream.jsonl` states no cost, so OpenCode's own figure for the run is not stated."
			}
			figure, err := decimal.ParseNumber(cost.raw)
			if err != nil {
				return "A step_finish line in `stream.jsonl` states a cost that is not a plain number, so OpenCode's own figure for the run is not stated."
			}
			total = total.Add(figure)
			steps++
		}
	}
	if steps == 0 {
		return "No step_finish line states OpenCode's own figure for the run."
	}
	return fmt.Sprintf("OpenCode's own figure for the run: %s USD, the sum of the `cost` of the %d `step_finish` lines in `stream.jsonl`. OpenCode prices each step from its own price list.", total, steps)
}
