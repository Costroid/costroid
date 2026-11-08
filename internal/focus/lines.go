package focus

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
)

// MaxLineBytes bounds one line of a rows file.
const MaxLineBytes = 1 << 20

// ErrLongLine is the fault of a line past MaxLineBytes.
var ErrLongLine = fmt.Errorf("the line is past %d bytes", MaxLineBytes)

// LineError names the line that stopped a read and why: the caller's refusal
// of it, or ErrLongLine.
type LineError struct {
	Line int
	Err  error
}

func (e *LineError) Error() string {
	return fmt.Sprintf("focus: rows line %d: %v", e.Line, e.Err)
}

func (e *LineError) Unwrap() error {
	return e.Err
}

// EachLine calls read with each line of a rows file in order. The line is
// valid only until read returns. Lines count from one, and an empty line is
// counted and skipped. The first error read returns stops the file and comes
// back as a *LineError, as does a line past MaxLineBytes. An error of source
// itself comes back as it is.
func EachLine(source io.Reader, read func(line []byte) error) error {
	scanner := bufio.NewScanner(source)
	scanner.Buffer(nil, MaxLineBytes)
	number := 1
	for ; scanner.Scan(); number++ {
		if len(scanner.Bytes()) == 0 {
			continue
		}
		if err := read(scanner.Bytes()); err != nil {
			return &LineError{Line: number, Err: err}
		}
	}
	err := scanner.Err()
	if errors.Is(err, bufio.ErrTooLong) {
		return &LineError{Line: number, Err: ErrLongLine}
	}
	return err
}

// Tags is a row's Tags column, each value as its line states it. A user tag
// may hold any JSON value; the convention's own tags are strings.
type Tags map[string]json.RawMessage

// Text reads one tag as a string. A tag that is absent, or that holds a JSON
// value other than a string or null, is an error and reads as empty.
func (t Tags) Text(key string) (string, error) {
	var value string
	if err := json.Unmarshal(t[key], &value); err != nil {
		return "", fmt.Errorf("tag %s: %w", key, err)
	}
	return value, nil
}
