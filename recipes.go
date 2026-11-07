package costroid

import "embed"

// Recipes holds the CI recipes as this release ships them, under recipes/.
// A recipe's Costroid pin can name an earlier release, or none, because the
// commit that pins a release follows its tag. costroid init rewrites each pin
// to the release the binary belongs to.
//
//go:embed recipes/*.yml
var Recipes embed.FS
