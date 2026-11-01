package focus

import (
	"encoding/json"
	"fmt"
	"io"
)

// Writer writes rows as JSON lines.
type Writer struct {
	encoder *json.Encoder
}

// NewWriter returns a Writer over w.
func NewWriter(w io.Writer) *Writer {
	encoder := json.NewEncoder(w)
	encoder.SetEscapeHTML(false)
	return &Writer{encoder: encoder}
}

// Write checks a row and writes it as one line.
func (w *Writer) Write(row Row) error {
	if err := row.Check(); err != nil {
		return err
	}
	if err := w.encoder.Encode(row); err != nil {
		return fmt.Errorf("focus: write row %s: %w", row.RowID, err)
	}
	return nil
}
