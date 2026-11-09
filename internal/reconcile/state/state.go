// Package state names a reconciliation's verdict on its tokens. It imports
// nothing, so a package that renders a verdict, as the comment does, reaches
// no provider client through it.
package state

// State is a row's verdict on its tokens.
type State string

// The states of a reconciled row.
const (
	// Matched means every class landed within tolerance.
	Matched State = "matched"
	// TokensMiss means a class missed the tolerance. It is a defect.
	TokensMiss State = "tokens_miss"
	// PriceMiss means every class matched and a price missed the catalog.
	// It is a defect.
	PriceMiss State = "price_miss"
	// Pending means the provider has not finished reporting the day. A
	// pending row carries no figure, because a partial report is not a miss.
	Pending State = "pending"
)
