// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Costroid Authors

// Package focuscsv implements the "focus-csv" connector (decisions D16, D29):
// the generic FOCUS/CSV importer that covers every source not served by a
// dedicated connector — a user's own FOCUS export (AWS Data Exports,
// Microsoft Cost Management, a warehouse dump, ...) as a plain or
// gzip-compressed CSV on a local path. The user DECLARES the export's FOCUS
// version (--focus-version 1.0 | 1.0r2 | 1.1 | 1.2 | 1.3 | 1.4); there is no
// version sniffing. 1.0r2 (an Azure-declarable alias, column-identical to 1.0)
// is canonicalized to 1.0.
//
// # Conformant-import scope for 1.0/1.1
//
// 1.0 and 1.1 are accepted only for SPEC-CONFORMANT exports — RFC3339
// timestamps and empty-cell nulls. The shared strict parser is unchanged: it
// still requires strict RFC3339 date/times and still treats a literal "null"
// as a value, not null (an empty cell is the only null). Real-world 1.0 emitters
// (OCI is 1.0-only; Azure emits 1.0/1.0r2; the FinOps sample) commonly use
// space-separated or seconds-less timestamps and literal NULL/NONE sentinels;
// those rows are REJECTED with the existing actionable, row-numbered error rather
// than silently coerced. Relaxing the parser for those quirks would touch every
// connector and reverse the empty-only-null / strict-timestamp calibration, so it
// is a deliberately separate, later piece of work — NOT this importer's job. The
// 1.0/1.1 → 1.4 transform is the 1.2 entity mapping (ProviderName/PublisherName
// are present and their 1.3+ successors are absent, so the mapping is add-only).
//
// # Strictness is the product (rules GEN-1…)
//
// This is a strict importer, not a repair tool. It applies no gap-fill, no
// column repair, and no value coercion beyond the documented tolerances
// below. Every failure is actionable: file-level failures name the offending
// column and the expectation; row-level failures carry the 1-based data-row
// number. A source that is merely version-skewed or vendor-quirky is a job
// for a dedicated connector (aws-focus, azure-focus, …), not this one.
//
//	rule    area                behaviour
//	------  ------------------  ------------------------------------------------
//	GEN-1   format              Magic bytes are authoritative: a 1f 8b prefix is
//	                            gunzipped regardless of file name; no magic is
//	                            read as plain CSV regardless of name — EXCEPT a
//	                            .gz-named file WITHOUT gzip magic, which errors
//	                            (name/content mismatch). Non-CSV binary
//	                            containers (Parquet, zip, …) are rejected naming
//	                            the accepted formats. UTF-8 only; a single
//	                            leading BOM is stripped; CRLF is tolerated;
//	                            full RFC 4180 quoting is honored (embedded
//	                            commas/newlines/quotes in Tags or
//	                            ChargeDescription survive — never split on ",").
//	GEN-2   header (strict)     Matching is exact PascalCase, case-sensitive, BY
//	                            NAME never by position. Unknown x_-prefixed
//	                            columns are accepted and dropped. Unknown
//	                            non-x_ columns FAIL (naming them, in file order,
//	                            with a mislabel hint). Duplicate header names
//	                            FAIL (by-name mapping is ambiguous — a Costroid
//	                            strictness choice with no normative basis).
//	GEN-3   mandatory presence  1.0/1.1/1.2/1.3-declared files must carry that tag's
//	                            full Mandatory-presence set (21 for 1.0/1.1/1.2, 23
//	                            for 1.3) or FAIL, listing the missing columns sorted.
//	                            1.4-declared files must carry the 15 not-null
//	                            columns or FAIL; other absent 1.4-Mandatory
//	                            (nullable) columns are a one-line WARNING, not a
//	                            failure — FOCUS 1.4's DatasetConfiguration lets a
//	                            conformant dataset expose a Mandatory column
//	                            SUBSET, so a Mandatory-but-nullable column may be
//	                            legitimately omitted (warn, do not reject); absent
//	                            Conditional columns are fine.
//	GEN-4   cells               An empty field is null — a Costroid calibration,
//	                            not a spec rule: FOCUS defines no CSV
//	                            serialization, so "empty cell == null" has no
//	                            normative basis and is our documented choice
//	                            (decision D34). A literal "null"/"NULL" string is
//	                            NOT null: it flows through and fails naturally as
//	                            a type/enum violation, row-numbered — the importer
//	                            never rewrites it.
//	GEN-5   batching (D26a)     Rows split by the UTC month of BillingPeriodStart;
//	                            each month is one batch keyed
//	                            <source-label>/<YYYY-MM>. Re-importing a month
//	                            under the same label REPLACES it. A row with an
//	                            unparseable BillingPeriodStart FAILS the whole
//	                            import (row-numbered) before anything is stored.
//	GEN-6   ContentHash         sha256 over the post-BOM header line PLUS the
//	                            month's raw record byte spans (decompressed
//	                            bytes, line endings AS-IS), captured via
//	                            csv.Reader.InputOffset. Including the header means
//	                            a header-only change invalidates every month's
//	                            hash; a CRLF→LF rewrite counts as changed; a
//	                            .csv and its identical .csv.gz hash the same.
//
// # Documented limitation
//
// One import must contain the COMPLETE data for each month it touches under a
// given --source-label: importing a single part-file REPLACES that month with
// that part alone (multi-part manifest stitching is a vendor connector's job,
// not this one's).
//
// # --lenient (opt-in timestamp-format tolerance)
//
// Strict RFC3339 is the DEFAULT. --lenient is an additive opt-in that tolerates
// real-world UTC timestamp FORMAT variants which are unambiguously UTC but not
// strict RFC3339, on the four Date/Time columns (BillingPeriodStart/End,
// ChargePeriodStart/End) ONLY. What it tolerates: a missing seconds field
// ("...T00:00Z"), a space date/time separator ("2024-01-01 00:00:00Z"), and a
// trailing named " UTC" ("2024-09-18 22:00:00 UTC", BigQuery) — provided the
// value carries an EXPLICIT zone (Z, ±hh:mm, or ±hhmm). Each such value is
// canonicalized to RFC3339 in the focus-csv package BEFORE the shared strict
// parser and validation see it, so record.go/validate.go stay byte-unchanged.
//
// What --lenient does NOT do (money-safety boundary, deliberate): it does NOT
// accept a genuinely ZONE-LESS timestamp ("2024-01-01 00:00:00") — a real
// emitter can write local wall-clock there (Alibaba Cloud's 1.0 Preview writes
// UTC+8), so assuming UTC would misbucket a charge; those still REJECT with the
// existing row-numbered ISO-8601 error. It does NOT coerce null tokens (a literal
// "null" still fails, GEN-4 unchanged) and does NOT touch numbers or their
// locale. It is format-only, zone-bearing-only, and per-connector: the AWS/Azure/
// AI connectors and the shared parser are untouched.
//
// # --force
//
// --force is accepted for CLI uniformity but is a documented NO-OP beyond
// re-reading the file: focus-csv keeps no incremental sync state (no
// storage.SyncState tuple to bypass), so every import always runs, and the
// store's unchanged short-circuit on a byte-identical re-import is
// unconditional and untouched (matching the AI-connector --force precedent).
//
// # Credentials
//
// focus-csv reads a local file and takes NO credentials, tokens, or network
// access — there is no secret for it to hold, log, or leak (secrets hygiene).
//
// The FOCUS Column ID tables below were generated from the spec column .md
// files (their `## Column ID` and Content-Constraints "Feature level"
// sections) at the version tags; the source URLs are cited beside each table.
package focuscsv

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/csv"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"io"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"

	"github.com/Costroid/costroid/internal/focus"
	"github.com/Costroid/costroid/internal/ingest"
	"github.com/Costroid/costroid/internal/ingest/csvstream"
)

// Name is the connector's registry name.
const Name = "focus-csv"

// utf8BOM is the UTF-8 byte order mark some tools prepend to CSV files.
var utf8BOM = []byte{0xEF, 0xBB, 0xBF}

// canonicalVersion rewrites declarable-but-aliased version strings to their
// canonical Version BEFORE anything downstream (ParseVersion, the known/mandatory
// tables, the transform registry, the Connector) consumes it. Azure can declare
// "1.0r2", which is column-identical to 1.0 (it differs only in timestamp form,
// irrelevant under the conformant-import scope), so it canonicalizes to V1_0.
// Every other value passes through unchanged. This is why it must run in Discover
// and not merely be accepted inside ParseVersion: ParseVersion returns an error
// and cannot rewrite the value the downstream maps consume.
func canonicalVersion(v focus.Version) focus.Version {
	if v == focus.Version("1.0r2") {
		return focus.V1_0
	}
	return v
}

// ParseVersion validates a canonical --focus-version. 1.0 and 1.1 are accepted
// under the conformant-import scope documented on the package (the strict parser
// is unchanged); 1.0r2 is canonicalized to 1.0 by canonicalVersion upstream and
// never reaches here.
func ParseVersion(v focus.Version) error {
	switch v {
	case focus.V1_0, focus.V1_1, focus.V1_2, focus.V1_3, focus.V1_4:
		return nil
	case "":
		return errors.New("--focus-version is required for the focus-csv connector (supported values: 1.0, 1.1, 1.2, 1.3, 1.4)")
	default:
		return fmt.Errorf("unsupported --focus-version %q; supported values are 1.0, 1.1, 1.2, 1.3, 1.4", v)
	}
}

// Period is one discovered billing month of the import.
type Period struct {
	Month string
	Conn  *Connector
}

// Discover validates the declared version and the file, splits the file into
// one Connector per UTC BillingPeriodStart month (oldest first), and returns
// any non-fatal header warnings. It reads no credentials and touches no
// network. When label is empty it defaults to the file's base name.
// When lenient is true, the connector tolerates UTC timestamp FORMAT variants
// (missing seconds, a space date/time separator, a trailing " UTC") on the four
// Date/Time columns, canonicalizing them to RFC 3339 before the shared strict
// parser and validation ever see them. It still REJECTS zone-less timestamps,
// literal null tokens, and non-RFC3339 numbers — leniency is a format-only,
// zone-bearing-only relaxation, and strict (lenient=false) stays the default.
func Discover(path string, version focus.Version, label string, lenient bool) (periods []Period, warnings []string, err error) {
	// Canonicalize aliases (e.g. 1.0r2 → 1.0) FIRST so the rewritten value flows
	// into every downstream consumer — ParseVersion, the header tables, and the
	// Connector's FOCUSVersion — not just past this validation gate.
	version = canonicalVersion(version)
	if err := ParseVersion(version); err != nil {
		return nil, nil, err
	}
	content, err := readAndDecompress(path)
	if err != nil {
		return nil, nil, err
	}
	if label == "" {
		label = filepath.Base(path)
	}
	months, hashes, warnings, err := analyze(content, version, lenient)
	if err != nil {
		return nil, nil, err
	}
	periods = make([]Period, 0, len(months))
	for _, m := range months {
		periods = append(periods, Period{
			Month: m,
			Conn: &Connector{
				version:     version,
				month:       m,
				label:       label,
				content:     content,
				contentHash: hashes[m],
				lenient:     lenient,
			},
		})
	}
	return periods, warnings, nil
}

// readAndDecompress loads the file, applies magic-byte-authoritative gzip
// handling, strips a single leading BOM, and rejects empty or binary input.
func readAndDecompress(path string) ([]byte, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("focus-csv: opening %s: %w", path, err)
	}
	if len(raw) == 0 {
		return nil, fmt.Errorf("focus-csv: %s is empty; expected a CSV export with a header row", path)
	}

	var content []byte
	switch {
	case hasGzipMagic(raw):
		gz, gzErr := gzip.NewReader(bytes.NewReader(raw))
		if gzErr != nil {
			return nil, fmt.Errorf("focus-csv: %s starts with gzip magic (1f 8b) but is not valid gzip: %w", path, gzErr)
		}
		defer func() { _ = gz.Close() }()
		content, err = io.ReadAll(gz)
		if err != nil {
			return nil, fmt.Errorf("focus-csv: decompressing %s: %w", path, err)
		}
	case strings.HasSuffix(strings.ToLower(path), ".gz"):
		// A .gz name with no gzip magic is a name/content mismatch — refuse
		// to guess rather than silently read the bytes as plain CSV.
		return nil, fmt.Errorf("focus-csv: %s is named .gz but has no gzip magic bytes (1f 8b); "+
			"refusing to guess — decompress it, or rename it if it is actually plain CSV", path)
	default:
		content = raw
	}

	if len(content) == 0 {
		return nil, fmt.Errorf("focus-csv: %s decompressed to nothing; expected a CSV export with a header row", path)
	}
	content = bytes.TrimPrefix(content, utf8BOM) // strip one leading BOM
	if looksBinary(content) {
		return nil, fmt.Errorf("focus-csv: %s does not look like text CSV (binary/NUL bytes found); "+
			"focus-csv accepts a plain CSV or a gzip-compressed CSV (UTF-8, optional BOM) — "+
			"Parquet, zip, and other formats are out of scope", path)
	}
	return content, nil
}

func hasGzipMagic(b []byte) bool { return len(b) >= 2 && b[0] == 0x1f && b[1] == 0x8b }

// looksBinary reports whether the first bytes contain a NUL, the tell of a
// binary container (Parquet, zip, …) — text CSV never does.
func looksBinary(content []byte) bool {
	n := min(len(content), 512)
	return bytes.IndexByte(content[:n], 0x00) >= 0
}

// analyze does the single authoritative pass over the decompressed,
// BOM-stripped content with csv.Reader.InputOffset span capture: it validates
// the header (strict), assigns every data row to a UTC month by
// BillingPeriodStart (failing the whole import, row-numbered, on an
// unparseable one), and folds the post-BOM header line plus each month's raw
// record byte spans into that month's ContentHash. It stores no rows — the
// per-month reader re-streams the same immutable content.
func analyze(content []byte, version focus.Version, lenient bool) (months []string, hashes map[string]string, warnings []string, err error) {
	cr := csv.NewReader(bytes.NewReader(content))
	// Default FieldsPerRecord (0): the header fixes the column count and every
	// data row must match it — a ragged row is a real malformation and fails.

	header, err := cr.Read()
	if errors.Is(err, io.EOF) {
		return nil, nil, nil, errors.New("focus-csv: the file has no header row")
	}
	if err != nil {
		return nil, nil, nil, fmt.Errorf("focus-csv: reading the header row: %w", err)
	}
	headerEnd := cr.InputOffset()
	headerBytes := content[:headerEnd]

	warnings, err = validateHeader(header, version)
	if err != nil {
		return nil, nil, nil, err
	}

	hashers := map[string]hash.Hash{}
	prev := headerEnd
	rowNum := 0
	for {
		fields, rerr := cr.Read()
		if errors.Is(rerr, io.EOF) {
			break
		}
		rowNum++
		if rerr != nil {
			return nil, nil, nil, fmt.Errorf("focus-csv: row %d: %w", rowNum, rerr)
		}
		cur := cr.InputOffset()
		span := content[prev:cur]
		prev = cur

		month, merr := monthOf(fieldByName(header, fields, "BillingPeriodStart"), lenient)
		if merr != nil {
			return nil, nil, nil, fmt.Errorf("focus-csv: row %d: %w", rowNum, merr)
		}
		h, ok := hashers[month]
		if !ok {
			h = sha256.New()
			h.Write(headerBytes) // header is part of every month's hash
			hashers[month] = h
		}
		h.Write(span)
	}
	if rowNum == 0 {
		return nil, nil, nil, errors.New("focus-csv: the file has a header but no data rows")
	}

	months = make([]string, 0, len(hashers))
	hashes = make(map[string]string, len(hashers))
	for m, h := range hashers {
		months = append(months, m)
		hashes[m] = "sha256:" + hex.EncodeToString(h.Sum(nil))
	}
	sort.Strings(months)
	return months, hashes, warnings, nil
}

// monthOf returns the UTC "YYYY-MM" of a BillingPeriodStart value, using the
// same time parser the pipeline uses. When lenient, a zone-bearing FORMAT variant
// is canonicalized to RFC 3339 first; the empty-null check and the error message
// still reference the ORIGINAL value (so a zone-less value fails with the same
// message and --lenient never falsely implies it rescued it). Because both the
// analyze/Discover month-split and the streaming reader route BillingPeriodStart
// through this one function, their bucketing agrees by construction.
func monthOf(billingPeriodStart string, lenient bool) (string, error) {
	if strings.TrimSpace(billingPeriodStart) == "" {
		return "", errors.New("BillingPeriodStart is null; a row cannot be assigned to a billing month without it")
	}
	parseInput := billingPeriodStart
	if lenient {
		parseInput = normalizeTimestamp(billingPeriodStart)
	}
	t, err := focus.ParseTime(parseInput)
	if err != nil {
		return "", fmt.Errorf("BillingPeriodStart %q is not a valid ISO 8601 date/time; "+
			"a row cannot be assigned to a billing month", billingPeriodStart)
	}
	return t.Format("2006-01"), nil
}

// fieldByName returns the value of the named column in a data row, or "" when
// the column is absent.
func fieldByName(header, fields []string, name string) string {
	if i := slices.Index(header, name); i >= 0 && i < len(fields) {
		return fields[i]
	}
	return ""
}

// validateHeader applies the strict header policy (GEN-2, GEN-3) for the
// declared version, returning any non-fatal warnings.
func validateHeader(header []string, version focus.Version) (warnings []string, err error) {
	// Duplicate names (in file order, unique) — ambiguous by-name mapping.
	counts := map[string]int{}
	for _, h := range header {
		counts[h]++
	}
	var dups []string
	for _, h := range header {
		if counts[h] > 1 && !slices.Contains(dups, h) {
			dups = append(dups, h)
		}
	}
	if len(dups) > 0 {
		return nil, fmt.Errorf("focus-csv: duplicate header column(s) %s; header→column mapping is by name "+
			"and would be ambiguous (a Costroid strict-import choice)", quoteList(dups))
	}

	// Unknown non-x_ columns (in file order).
	known := knownColumnsFor(version)
	var unknown []string
	for _, h := range header {
		if strings.HasPrefix(h, "x_") {
			continue // accept-and-drop (PascalCase after x_ is only a SHOULD)
		}
		if _, ok := known[h]; !ok {
			unknown = append(unknown, h)
		}
	}
	if len(unknown) > 0 {
		return nil, unknownHeaderError(version, unknown)
	}

	// Mandatory presence (GEN-3).
	if version == focus.V1_4 {
		var missing []string
		for _, c := range notNull14 {
			if !slices.Contains(header, c) {
				missing = append(missing, c)
			}
		}
		if len(missing) > 0 {
			return nil, fmt.Errorf("focus-csv: the FOCUS 1.4 export is missing required non-null column(s): %s "+
				"(these must be present and non-null for every row)", quoteList(missing))
		}
		var absentNullable []string
		for _, c := range mandatoryNullable14 {
			if !slices.Contains(header, c) {
				absentNullable = append(absentNullable, c)
			}
		}
		if len(absentNullable) > 0 {
			warnings = append(warnings, fmt.Sprintf("focus-csv: the FOCUS 1.4 export omits column(s) %s that the "+
				"1.4 DatasetConfiguration marks Mandatory; they allow nulls, so the import proceeds with them null",
				quoteList(absentNullable)))
		}
		return warnings, nil
	}

	mand := mandatoryFor(version) // already sorted
	var missing []string
	for _, c := range mand {
		if !slices.Contains(header, c) {
			missing = append(missing, c)
		}
	}
	if len(missing) > 0 {
		return nil, missingMandatoryError(version, missing, header)
	}
	return nil, nil
}

// unknownHeaderError names the unknown columns (file order) and adds a
// mislabel hint when the unknown set is characteristic of a different version.
func unknownHeaderError(version focus.Version, unknown []string) error {
	var b strings.Builder
	fmt.Fprintf(&b, "focus-csv: %d column(s) are not valid FOCUS %s columns: %s", len(unknown), version, quoteList(unknown))
	switch version {
	case focus.V1_4:
		if slices.Contains(unknown, "ProviderName") || slices.Contains(unknown, "PublisherName") {
			b.WriteString(" — ProviderName/PublisherName were removed in FOCUS 1.4; this looks like a " +
				"FOCUS 1.0, 1.1, 1.2, or 1.3 export — re-run with --focus-version 1.2 or 1.3 (or 1.0/1.1)")
		}
	case focus.V1_0, focus.V1_1, focus.V1_2:
		if slices.Contains(unknown, "ServiceProviderName") || slices.Contains(unknown, "HostProviderName") {
			b.WriteString(" — ServiceProviderName/HostProviderName were introduced in FOCUS 1.3; " +
				"did you mean --focus-version 1.3 (or 1.4)?")
		}
	}
	b.WriteString(". Unknown x_-prefixed columns are accepted and dropped; other unknown columns are " +
		"rejected (a Costroid strict-import choice).")
	return errors.New(b.String())
}

// missingMandatoryError names the missing columns (sorted) for a 1.2/1.3 file
// and adds the 1.3→1.2 mislabel hint when the file carries the deprecated
// entity columns but not the 1.3 successor.
func missingMandatoryError(version focus.Version, missing, header []string) error {
	var b strings.Builder
	fmt.Fprintf(&b, "focus-csv: the FOCUS %s export is missing mandatory column(s): %s", version, quoteList(missing))
	if version == focus.V1_3 && slices.Contains(missing, "ServiceProviderName") &&
		slices.Contains(header, "ProviderName") && slices.Contains(header, "PublisherName") {
		b.WriteString(" — the file carries the deprecated ProviderName/PublisherName but not the 1.3 successor " +
			"ServiceProviderName; this looks like a FOCUS 1.2 export — re-run with --focus-version 1.2")
	}
	return errors.New(b.String())
}

// quoteList renders names as `"a", "b", "c"` in the given order.
func quoteList(names []string) string {
	quoted := make([]string, len(names))
	for i, n := range names {
		quoted[i] = fmt.Sprintf("%q", n)
	}
	return strings.Join(quoted, ", ")
}

// Connector reads one billing month out of one declared-version FOCUS CSV
// file. Several months of one file yield several Connectors sharing the
// decompressed content (read-only); each streams only its own month's rows.
type Connector struct {
	version     focus.Version
	month       string
	label       string
	content     []byte // decompressed, BOM-stripped, shared read-only
	contentHash string
	lenient     bool // tolerate zone-bearing UTC timestamp format variants
}

var _ ingest.Connector = (*Connector)(nil)

func (c *Connector) Name() string { return Name }

func (c *Connector) FOCUSVersion() focus.Version { return c.version }

// Month returns the connector's billing month ("YYYY-MM").
func (c *Connector) Month() string { return c.month }

// SourceIdentity is "<source-label>/<YYYY-MM>" — the per-month replace key
// (decision D26a): re-importing a month under the same label replaces it.
func (c *Connector) SourceIdentity() string { return c.label + "/" + c.month }

func (c *Connector) ContentHash(context.Context) (string, error) { return c.contentHash, nil }

func (c *Connector) Records(context.Context) (ingest.RecordReader, error) {
	stream, err := csvstream.New(bytes.NewReader(c.content), 0)
	if err != nil {
		return nil, fmt.Errorf("focus-csv: re-reading %s: %w", c.SourceIdentity(), err)
	}
	return &reader{conn: c, stream: stream}, nil
}

// reader streams the whole file but yields only this connector's month's
// rows, keeping the file-global data-row numbers so a row-level pipeline error
// points at the actual line in the source.
type reader struct {
	conn   *Connector
	stream *csvstream.Stream
}

func (r *reader) Next() (ingest.Row, error) {
	for {
		row, err := r.stream.Next()
		if err != nil {
			return ingest.Row{}, err // io.EOF or a read error
		}
		if r.conn.lenient {
			// Rewrite only the four Date/Time columns to canonical RFC 3339 so the
			// pipeline's frozen Validate/ParseRecord see canonical strings; a
			// zone-less value is left as-is and still fails validation.
			normalizeRecordTimestamps(row.Record)
		}
		month, merr := monthOf(row.Record["BillingPeriodStart"], r.conn.lenient)
		if merr != nil {
			// analyze already validated every row's BillingPeriodStart before
			// anything was stored; a failure here is purely defensive.
			return ingest.Row{}, fmt.Errorf("focus-csv: row %d: %w", row.Number, merr)
		}
		if month == r.conn.month {
			return row, nil
		}
	}
}

func (r *reader) Close() error { return nil }
