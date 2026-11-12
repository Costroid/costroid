package copilot

import (
	"io"

	"github.com/Costroid/costroid/internal/adapter"
)

// ReadEvents is readEvents for the package's tests, without the list of
// requests that wrote no row.
func ReadEvents(events io.Reader, keyed bool) (adapter.Report, error) {
	report, _, err := readEvents(events, keyed)
	return report, err
}
