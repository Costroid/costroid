package catalog

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/asn1"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"io/fs"
	"maps"
	"path"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

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
		checked += len(holdSheetsTo(t, version, manifestDigests(t, version)))
	}
	if checked == 0 {
		t.Error("no sheet was checked against a manifest")
	}
}

// holdSheetsTo holds each sheet the binary carries for a release to the
// SHA-256 its release's SHA256SUMS names, and returns the sheets it checked.
func holdSheetsTo(t *testing.T, version string, digests map[string]string) []string {
	t.Helper()
	sheets, err := fs.Glob(catalogdata.Sheets, version+"/*.json")
	if err != nil {
		t.Fatal(err)
	}
	var names []string
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
		names = append(names, path.Base(file))
	}
	return names
}

// signedReleases are the embedded releases whose SHA256SUMS the catalog's
// release workflow signed, each with the workflow run that signed it.
var signedReleases = map[string]string{"0.3.0": "42444947345"}

const (
	catalogRepository = "https://github.com/Costroid/costroid-catalog"
	actionsIssuer     = "https://token.actions.githubusercontent.com"
)

// Fulcio's certificate extensions, as
// https://github.com/sigstore/fulcio/blob/main/docs/oid-info.md lists them.
// The first issuer extension holds the bare string; the others hold a DER
// UTF8String.
var (
	oidIssuerV1         = asn1.ObjectIdentifier{1, 3, 6, 1, 4, 1, 57264, 1, 1}
	oidIssuer           = asn1.ObjectIdentifier{1, 3, 6, 1, 4, 1, 57264, 1, 8}
	oidSourceRepository = asn1.ObjectIdentifier{1, 3, 6, 1, 4, 1, 57264, 1, 12}
	oidSourceRef        = asn1.ObjectIdentifier{1, 3, 6, 1, 4, 1, 57264, 1, 14}
	oidRunInvocation    = asn1.ObjectIdentifier{1, 3, 6, 1, 4, 1, 57264, 1, 21}
)

// bundle is the part of a Sigstore bundle, media type v0.3, that the tests
// read. JSON carries each byte field in base64.
type bundle struct {
	MediaType            string `json:"mediaType"`
	VerificationMaterial struct {
		Certificate struct {
			RawBytes []byte `json:"rawBytes"`
		} `json:"certificate"`
		TlogEntries []struct {
			IntegratedTime string `json:"integratedTime"`
			KindVersion    struct {
				Kind    string `json:"kind"`
				Version string `json:"version"`
			} `json:"kindVersion"`
			CanonicalizedBody []byte `json:"canonicalizedBody"`
		} `json:"tlogEntries"`
	} `json:"verificationMaterial"`
	MessageSignature struct {
		MessageDigest struct {
			Algorithm string `json:"algorithm"`
			Digest    []byte `json:"digest"`
		} `json:"messageDigest"`
		Signature []byte `json:"signature"`
	} `json:"messageSignature"`
}

// hashedRekord is the transparency log's entry for a signed blob.
type hashedRekord struct {
	Kind string `json:"kind"`
	Spec struct {
		Data struct {
			Hash struct {
				Algorithm string `json:"algorithm"`
				Value     string `json:"value"`
			} `json:"hash"`
		} `json:"data"`
		Signature struct {
			Content   []byte `json:"content"`
			PublicKey struct {
				Content []byte `json:"content"`
			} `json:"publicKey"`
		} `json:"signature"`
	} `json:"spec"`
}

// signedFiles reads a signed release's SHA256SUMS, its Sigstore bundle and the
// bundle's signing certificate.
func signedFiles(t *testing.T, version string) ([]byte, *bundle, *x509.Certificate) {
	t.Helper()
	sums, err := fs.ReadFile(catalogdata.Sheets, version+"/SHA256SUMS")
	if err != nil {
		t.Fatalf("release %s carries no manifest: %v", version, err)
	}
	data, err := fs.ReadFile(catalogdata.Signatures, version+"/SHA256SUMS.sigstore.json")
	if err != nil {
		t.Fatalf("release %s carries no bundle: %v", version, err)
	}
	var b bundle
	if err := json.Unmarshal(data, &b); err != nil {
		t.Fatalf("release %s bundle: %v", version, err)
	}
	if b.MediaType != "application/vnd.dev.sigstore.bundle.v0.3+json" {
		t.Fatalf("release %s bundle has media type %q", version, b.MediaType)
	}
	cert, err := x509.ParseCertificate(b.VerificationMaterial.Certificate.RawBytes)
	if err != nil {
		t.Fatalf("release %s bundle certificate: %v", version, err)
	}
	return sums, &b, cert
}

// signedSums is the digest a signed release's SHA256SUMS names for each file,
// once the file is shown to be the one its bundle signed: its SHA-256 is the
// bundle's message digest, and the bundle's signature over it verifies under
// the bundle certificate's key.
func signedSums(t *testing.T, version string) map[string]string {
	t.Helper()
	sums, b, cert := signedFiles(t, version)
	digest := sha256.Sum256(sums)
	if signed := b.MessageSignature.MessageDigest; signed.Algorithm != "SHA2_256" || !bytes.Equal(signed.Digest, digest[:]) {
		t.Fatalf("release %s SHA256SUMS has SHA-256 %x, and its bundle signed %s %x", version, digest, signed.Algorithm, signed.Digest)
	}
	key, ok := cert.PublicKey.(*ecdsa.PublicKey)
	if !ok {
		t.Fatalf("release %s bundle certificate holds a %T key, not ECDSA", version, cert.PublicKey)
	}
	if !ecdsa.VerifyASN1(key, digest[:], b.MessageSignature.Signature) {
		t.Fatalf("release %s bundle signature does not verify over SHA256SUMS under its certificate's key", version)
	}
	return manifestDigests(t, version)
}

// TestEmbeddedSheetsMatchTheSignedSums holds every sheet of a signed release
// to the SHA256SUMS the catalog's release workflow signed, and holds that the
// binary carries every sheet that file names, so a signed release is embedded
// whole. It also holds that the release a run prices with is a signed one.
func TestEmbeddedSheetsMatchTheSignedSums(t *testing.T) {
	if _, signed := signedReleases[catalogdata.Version]; !signed {
		t.Errorf("a run prices with release %s, which carries no signed SHA256SUMS", catalogdata.Version)
	}
	bundles, err := fs.Glob(catalogdata.Signatures, "*/SHA256SUMS.sigstore.json")
	if err != nil {
		t.Fatal(err)
	}
	for _, file := range bundles {
		if _, listed := signedReleases[path.Dir(file)]; !listed {
			t.Errorf("%s is embedded, and its release is not one this test checks", file)
		}
	}
	checked := 0
	for _, version := range slices.Sorted(maps.Keys(signedReleases)) {
		digests := signedSums(t, version)
		embedded := holdSheetsTo(t, version, digests)
		for _, name := range slices.Sorted(maps.Keys(digests)) {
			if !slices.Contains(embedded, name) {
				t.Errorf("release %s signed SHA256SUMS names %s, which the binary does not carry", version, name)
			}
		}
		checked += len(embedded)
	}
	if checked == 0 {
		t.Error("no sheet was checked against a signed manifest")
	}
}

// extensionText reads one of Fulcio's text extensions from a certificate.
func extensionText(t *testing.T, cert *x509.Certificate, oid asn1.ObjectIdentifier) string {
	t.Helper()
	for _, extension := range cert.Extensions {
		if !extension.Id.Equal(oid) {
			continue
		}
		if oid.Equal(oidIssuerV1) {
			return string(extension.Value)
		}
		var text string
		if rest, err := asn1.Unmarshal(extension.Value, &text); err != nil || len(rest) > 0 {
			t.Fatalf("extension %s is not one DER string: %v", oid, err)
		}
		return text
	}
	return ""
}

// TestBundleNamesTheCatalogReleaseWorkflow holds that each signed release's
// bundle was made by the catalog's release.yml at the release's own tag, in
// the run that published it, with a GitHub Actions identity token: the
// certificate's identity, issuer, repository, ref and run, as cosign
// verify-blob --certificate-identity and --certificate-oidc-issuer read them.
// The transparency log entry the bundle carries must name the same digest,
// signature and certificate.
//
// It does not prove the bundle authentic. The certificate is not chained to
// Sigstore's root, and neither the log entry's inclusion proof and signed
// checkpoint nor the timestamp is verified, because the binary carries no
// Sigstore trust root and the module no Sigstore verifier.
func TestBundleNamesTheCatalogReleaseWorkflow(t *testing.T) {
	for _, version := range slices.Sorted(maps.Keys(signedReleases)) {
		sums, b, cert := signedFiles(t, version)
		identity := catalogRepository + "/.github/workflows/release.yml@refs/tags/v" + version
		if len(cert.URIs) != 1 || cert.URIs[0].String() != identity {
			t.Errorf("release %s bundle certificate names %v, want %s", version, cert.URIs, identity)
		}
		for _, extension := range []struct {
			oid  asn1.ObjectIdentifier
			want string
		}{
			{oidIssuer, actionsIssuer},
			{oidIssuerV1, actionsIssuer},
			{oidSourceRepository, catalogRepository},
			{oidSourceRef, "refs/tags/v" + version},
		} {
			if got := extensionText(t, cert, extension.oid); got != extension.want {
				t.Errorf("release %s bundle certificate extension %s reads %q, want %q", version, extension.oid, got, extension.want)
			}
		}
		run := catalogRepository + "/actions/runs/" + signedReleases[version] + "/attempts/"
		if got := extensionText(t, cert, oidRunInvocation); !strings.HasPrefix(got, run) {
			t.Errorf("release %s bundle certificate names run %q, want one under %s", version, got, run)
		}
		if !slices.Contains(cert.ExtKeyUsage, x509.ExtKeyUsageCodeSigning) {
			t.Errorf("release %s bundle certificate is not for code signing: %v", version, cert.ExtKeyUsage)
		}

		entries := b.VerificationMaterial.TlogEntries
		if len(entries) != 1 || entries[0].KindVersion.Kind != "hashedrekord" {
			t.Fatalf("release %s bundle carries %d log entries, want one hashedrekord", version, len(entries))
		}
		seconds, err := strconv.ParseInt(entries[0].IntegratedTime, 10, 64)
		if err != nil {
			t.Fatal(err)
		}
		if logged := time.Unix(seconds, 0); logged.Before(cert.NotBefore) || logged.After(cert.NotAfter) {
			t.Errorf("release %s was logged at %s, outside its certificate's %s to %s", version, logged, cert.NotBefore, cert.NotAfter)
		}
		var entry hashedRekord
		if err := json.Unmarshal(entries[0].CanonicalizedBody, &entry); err != nil {
			t.Fatal(err)
		}
		digest := sha256.Sum256(sums)
		logged, _ := pem.Decode(entry.Spec.Signature.PublicKey.Content)
		switch {
		case entry.Kind != "hashedrekord" || entry.Spec.Data.Hash.Algorithm != "sha256" || entry.Spec.Data.Hash.Value != hex.EncodeToString(digest[:]):
			t.Errorf("release %s log entry names %s %s, want SHA256SUMS's sha256 %x", version, entry.Spec.Data.Hash.Algorithm, entry.Spec.Data.Hash.Value, digest)
		case !bytes.Equal(entry.Spec.Signature.Content, b.MessageSignature.Signature):
			t.Errorf("release %s log entry holds another signature than its bundle", version)
		case logged == nil || logged.Type != "CERTIFICATE" || !bytes.Equal(logged.Bytes, cert.Raw):
			t.Errorf("release %s log entry holds another certificate than its bundle", version)
		}
	}
}
