package focus

import (
	"errors"
	"io"
	"strings"
	"testing"
)

// readAll reads source with EachLine and keeps each line it is handed.
func readAll(source io.Reader, refuse string) ([]string, error) {
	var lines []string
	err := EachLine(source, func(line []byte) error {
		if string(line) == refuse {
			return errors.New("refused")
		}
		lines = append(lines, string(line))
		return nil
	})
	return lines, err
}

func TestEachLineSkipsEmptyLinesAndStillCountsThem(t *testing.T) {
	lines, err := readAll(strings.NewReader("\na\r\n\n \nb\nbad\nc"), "bad")
	if want := []string{"a", " ", "b"}; strings.Join(lines, "|") != strings.Join(want, "|") {
		t.Errorf("handed %q, want %q", lines, want)
	}
	var fault *LineError
	if !errors.As(err, &fault) || fault.Line != 6 || fault.Err.Error() != "refused" {
		t.Fatalf("error %v, want the refusal at line 6", err)
	}
}

func TestEachLineNamesTheLinePastTheLimit(t *testing.T) {
	under := strings.Repeat("a", MaxLineBytes-1)
	if lines, err := readAll(strings.NewReader("a\n"+under+"\n"), ""); err != nil || len(lines) != 2 {
		t.Fatalf("a line of %d bytes: %d lines, error %v", len(under), len(lines), err)
	}
	_, err := readAll(strings.NewReader("a\n\n"+under+"a\n"), "")
	var fault *LineError
	if !errors.As(err, &fault) || fault.Line != 3 || !errors.Is(err, ErrLongLine) {
		t.Fatalf("error %v, want line 3 past the limit", err)
	}
	if got, want := err.Error(), "focus: rows line 3: the line is past 1048576 bytes"; got != want {
		t.Errorf("error %q, want %q", got, want)
	}
}

type failingReader struct{ err error }

func (r failingReader) Read([]byte) (int, error) { return 0, r.err }

func TestEachLineReturnsTheSourcesOwnError(t *testing.T) {
	gone := errors.New("disk gone")
	_, err := readAll(io.MultiReader(strings.NewReader("a\n"), failingReader{gone}), "")
	var fault *LineError
	if err != gone || errors.As(err, &fault) {
		t.Fatalf("error %v, want the source's own", err)
	}
}

func TestTagsTextReadsOnlyAString(t *testing.T) {
	tags := Tags{"string": []byte(`"x"`), "null": []byte(`null`), "number": []byte(`7`), "object": []byte(`{}`)}
	for key, want := range map[string]string{"string": "x", "null": "", "number": "", "object": "", "absent": ""} {
		got, err := tags.Text(key)
		if got != want || (err != nil) != (key == "number" || key == "object" || key == "absent") {
			t.Errorf("Text(%q) = %q, %v", key, got, err)
		}
	}
	if _, err := tags.Text("number"); err == nil || !strings.HasPrefix(err.Error(), "tag number: ") {
		t.Errorf("a number's error %v does not name the tag", err)
	}
}
