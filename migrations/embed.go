// Package migrations embeds the forward-only schema migrations.
package migrations

import "embed"

// Files holds one NNNN_name.sql per version. There is no down migration.
//
//go:embed *.sql
var Files embed.FS
