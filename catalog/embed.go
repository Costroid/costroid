// Package catalogdata embeds the price sheets of each release the binary
// carries.
package catalogdata

import "embed"

// Version is the release a run prices with.
const Version = "0.4.1"

// Sheets holds one directory per release, named for its version, with one
// dated file per provider. From 0.2.0, a release directory also holds
// SHA256SUMS, the catalog's manifest of the release's sheets.
//
//go:embed 0.1.0/*.json 0.2.0/*.json 0.2.0/SHA256SUMS 0.3.0/*-2026-11-08.json 0.3.0/SHA256SUMS 0.4.1/*-2026-11-12.json 0.4.1/SHA256SUMS
var Sheets embed.FS

// Signatures holds, from 0.3.0, the Sigstore bundle the catalog's release
// workflow made for a release's SHA256SUMS, at
// <version>/SHA256SUMS.sigstore.json. It is kept out of Sheets, because a
// release is parsed from every JSON file in its directory. Only tests read it,
// and no command does, so the linker leaves it out of the binary.
//
//go:embed 0.3.0/SHA256SUMS.sigstore.json 0.4.1/SHA256SUMS.sigstore.json
var Signatures embed.FS
