package catalog

import (
	"testing"
	"testing/fstest"

	catalogdata "github.com/Costroid/costroid/catalog"
)

// TestPricesReadIsTheDayOfTheEmbeddedSheets reads the day of each release the
// binary carries from its sheets, and none for a release it does not carry.
func TestPricesReadIsTheDayOfTheEmbeddedSheets(t *testing.T) {
	for version, want := range map[string]string{"0.1.0": "2026-11-01", "0.2.0": "2026-11-07", "0.3.0": "2026-11-08", "0.4.1": "2026-11-12"} {
		if day, known := PricesRead(version); !known || day != want {
			t.Errorf("release %s: %q, %t, want %s", version, day, known, want)
		}
	}
	if _, known := PricesRead(catalogdata.Version); !known {
		t.Errorf("the pinned release %s has no day", catalogdata.Version)
	}
	for _, version := range []string{"", "9.9.9", ".", "0.3.0/anthropic-2026-11-08.json"} {
		if day, known := PricesRead(version); known {
			t.Errorf("release %q reads %s", version, day)
		}
	}
	if _, err := embeddedDays(); err != nil {
		t.Fatal(err)
	}
}

func dated(date string) *fstest.MapFile {
	return &fstest.MapFile{Data: []byte(`{"Provider":"p","SheetDate":"` + date + `","rows":[]}`)}
}

// TestPricesReadStatesTheOldestSheetOfARelease takes the oldest day when the
// sheets of one release state different days, and refuses a day it cannot
// read rather than guess one.
func TestPricesReadStatesTheOldestSheetOfARelease(t *testing.T) {
	days, err := releaseDays(fstest.MapFS{
		"1.0.0/a.json": dated("2026-11-08"),
		"1.0.0/b.json": dated("2026-10-30"),
		"1.0.0/c.json": dated("2026-11-09"),
		"1.0.0/SUMS":   {Data: []byte("not a sheet")},
		"1.1.0/a.json": dated("2026-11-12"),
		"notes.md":     {Data: []byte("not a release")},
	})
	if err != nil {
		t.Fatal(err)
	}
	if days["1.0.0"] != "2026-10-30" || days["1.1.0"] != "2026-11-12" || len(days) != 2 {
		t.Errorf("days %v", days)
	}
	for name, release := range map[string]fstest.MapFS{
		"a date that is no day": {"1.0.0/a.json": dated("2026-11-31")},
		"no date":               {"1.0.0/a.json": {Data: []byte(`{"Provider":"p","rows":[]}`)}},
		"not JSON":              {"1.0.0/a.json": {Data: []byte("{")}},
		"no sheet":              {"1.0.0/SUMS": {Data: []byte("x")}},
	} {
		if days, err := releaseDays(release); err == nil {
			t.Errorf("%s: read as %v", name, days)
		}
	}
}
