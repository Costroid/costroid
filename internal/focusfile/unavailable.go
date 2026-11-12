package focusfile

import (
	"encoding/json"
	"errors"
	"fmt"
)

// MaxUnavailableBytes bounds the note a reader takes. The note Write makes is
// under 100 bytes.
const MaxUnavailableBytes = 4096

// notAvailable is the one value the note's estimate field takes.
const notAvailable = "not_available"

// ReasonNoFile is why an optional estimate is not available: the file was not
// there.
const ReasonNoFile = "no_file"

// reasonSentences are the reasons a note can state, each with the words the
// comment and the job log print for it. The list is closed, so a note never
// carries text of its own to the comment.
var reasonSentences = map[string]string{
	ReasonNoFile: "the step that writes it did not run or wrote nothing, as on a fork's pull request that gets no secret",
}

// Unavailable is the note ingest-run --optional leaves beside the rows when
// the estimate it was given is not there, so the report can say so. The
// agent's job writes it, so a reader takes a reason from the closed list and
// a source that passes CheckSource, and nothing else.
type Unavailable struct {
	Estimate string `json:"estimate"`
	Reason   string `json:"reason"`
	Source   string `json:"source"`
}

// NotAvailable is the note for a source whose file was not there.
func NotAvailable(source string) Unavailable {
	return Unavailable{Estimate: notAvailable, Reason: ReasonNoFile, Source: source}
}

// Sentence is the words for the note's reason.
func (u Unavailable) Sentence() string {
	return reasonSentences[u.Reason]
}

// ParseUnavailable reads a note and holds it to the closed list.
func ParseUnavailable(data []byte) (Unavailable, error) {
	if len(data) > MaxUnavailableBytes {
		return Unavailable{}, fmt.Errorf("focusfile: the note is over %d bytes", MaxUnavailableBytes)
	}
	var note Unavailable
	if err := json.Unmarshal(data, &note); err != nil {
		return Unavailable{}, fmt.Errorf("focusfile: the note: %w", err)
	}
	switch {
	case note.Estimate != notAvailable:
		return Unavailable{}, errors.New("focusfile: the note states no estimate this release knows")
	case note.Sentence() == "":
		return Unavailable{}, errors.New("focusfile: the note states a reason this release does not know")
	}
	if err := CheckSource(note.Source); err != nil {
		return Unavailable{}, fmt.Errorf("focusfile: the note's source %w", err)
	}
	return note, nil
}
