package catalog

import (
	"crypto/sha256"
	"encoding/hex"
	"io/fs"
	"path"
	"regexp"
	"strings"
	"testing"

	catalogdata "github.com/Costroid/costroid/catalog"
)

// sumLine is one line of a SHA256SUMS file as sha256sum writes it: a
// lowercase hex digest, two spaces and a file name.
var sumLine = regexp.MustCompile(`^([0-9a-f]{64})  ([^/\s]+)$`)

// manifestDigests reads a release's SHA256SUMS into the digest it names for
// each file.
func manifestDigests(t *testing.T, version string) map[string]string {
	t.Helper()
	data, err := fs.ReadFile(catalogdata.Sheets, version+"/SHA256SUMS")
	if err != nil {
		t.Fatalf("release %s carries no manifest: %v", version, err)
	}
	digests := map[string]string{}
	for i, line := range strings.Split(strings.TrimSuffix(string(data), "\n"), "\n") {
		match := sumLine.FindStringSubmatch(line)
		if match == nil {
			t.Fatalf("%s/SHA256SUMS line %d is not a digest and a file name: %q", version, i+1, line)
		}
		if _, repeated := digests[match[2]]; repeated {
			t.Fatalf("%s/SHA256SUMS names %s twice", version, match[2])
		}
		digests[match[2]] = match[1]
	}
	return digests
}

// TestEmbeddedSheetsMatchTheManifest holds every embedded sheet of a release
// to the SHA-256 the catalog's SHA256SUMS for that release names, so each
// sheet is the catalog's own bytes. The manifest also names the catalog's
// sheets for providers no adapter prices, which the binary leaves out. Release
// 0.1.0 was published before the catalog wrote a manifest.
func TestEmbeddedSheetsMatchTheManifest(t *testing.T) {
	checked := 0
	for _, version := range embeddedVersions(t) {
		if version == "0.1.0" {
			continue
		}
		digests := manifestDigests(t, version)
		sheets, err := fs.Glob(catalogdata.Sheets, version+"/*.json")
		if err != nil {
			t.Fatal(err)
		}
		for _, file := range sheets {
			data, err := fs.ReadFile(catalogdata.Sheets, file)
			if err != nil {
				t.Fatal(err)
			}
			sum := sha256.Sum256(data)
			want, named := digests[path.Base(file)]
			switch {
			case !named:
				t.Errorf("%s is not named in its release's SHA256SUMS", file)
			case hex.EncodeToString(sum[:]) != want:
				t.Errorf("%s has SHA-256 %x, and its release's SHA256SUMS names %s", file, sum, want)
			}
			checked++
		}
	}
	if checked == 0 {
		t.Error("no sheet was checked against a manifest")
	}
}
