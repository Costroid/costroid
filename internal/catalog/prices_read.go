package catalog

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"sync"
	"time"

	catalogdata "github.com/Costroid/costroid/catalog"
)

// PricesRead is the day the prices of a release the binary carries were read,
// as YYYY-MM-DD: the date its sheets state, and the oldest one when its sheets
// state several. It is false for a release the binary does not carry.
func PricesRead(version string) (string, bool) {
	days, err := embeddedDays()
	if err != nil {
		return "", false
	}
	day, known := days[version]
	return day, known
}

// embeddedDays reads, once, the day each embedded release's prices were read.
var embeddedDays = sync.OnceValues(func() (map[string]string, error) {
	return releaseDays(catalogdata.Sheets)
})

// releaseDays reads the day of every release directory in sheets, by its
// version.
func releaseDays(sheets fs.FS) (map[string]string, error) {
	entries, err := fs.ReadDir(sheets, ".")
	if err != nil {
		return nil, fmt.Errorf("catalog: list releases: %w", err)
	}
	days := map[string]string{}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		release, err := fs.Sub(sheets, entry.Name())
		if err != nil {
			return nil, fmt.Errorf("catalog: open release %s: %w", entry.Name(), err)
		}
		day, err := oldestSheetDate(release)
		if err != nil {
			return nil, fmt.Errorf("catalog: release %s: %w", entry.Name(), err)
		}
		days[entry.Name()] = day
	}
	return days, nil
}

// oldestSheetDate is the oldest SheetDate of a release's sheets. A sheet
// whose date is not a calendar day is refused, so no day is ever guessed.
func oldestSheetDate(release fs.FS) (string, error) {
	names, err := fs.Glob(release, "*.json")
	if err != nil {
		return "", fmt.Errorf("list sheets: %w", err)
	}
	oldest := ""
	for _, name := range names {
		data, err := fs.ReadFile(release, name)
		if err != nil {
			return "", fmt.Errorf("read %s: %w", name, err)
		}
		var dated struct {
			SheetDate string `json:"SheetDate"`
		}
		if err := json.Unmarshal(data, &dated); err != nil {
			return "", fmt.Errorf("parse %s: %w", name, err)
		}
		if _, err := time.Parse(dayLayout, dated.SheetDate); err != nil {
			return "", fmt.Errorf("%s: SheetDate %q is not a calendar day", name, dated.SheetDate)
		}
		if oldest == "" || dated.SheetDate < oldest {
			oldest = dated.SheetDate
		}
	}
	if oldest == "" {
		return "", errors.New("no sheet")
	}
	return oldest, nil
}
