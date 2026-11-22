package receipt

import (
	"cmp"
	"crypto/sha256"
	"encoding/hex"
	"slices"
	"time"
)

// Posted is a comment the pull request's author posted that opens with
// Marker, as the host states it.
type Posted struct {
	ID        int64
	Body      string
	CreatedAt time.Time
	UpdatedAt time.Time
}

// Refusal is why a report read none of a receipt's sessions. The list is
// closed, and each value is the machine block's.
type Refusal string

// The refusals.
const (
	// Edited is a comment edited after it was posted. A new count is a new
	// comment.
	Edited Refusal = "receipt_edited"
	// OtherBranch is a receipt for another branch than the pull request's.
	OtherBranch Refusal = "receipt_other_branch"
	// Invalid is a comment that is not a valid receipt.
	Invalid Refusal = "receipt_invalid"
	// Off is a receipt costroid.yml on the base branch turns off.
	Off Refusal = "receipts_off"
)

// Reading is the receipt that decides: the newest the pull request's author
// posted, read or refused.
type Reading struct {
	Comment Posted
	// SHA256 is the digest of the comment's body, byte for byte, as the host
	// returned it.
	SHA256 string
	// Receipt is the receipt read, the zero value when it was refused.
	Receipt Receipt
	// Refused is why none of its sessions was read, empty when they were.
	Refused Refusal
}

// Newest reads the newest of the comments the pull request's author posted
// with the marker, and is false when there is none. Only that comment
// decides: an older one is never read in its place. allowed is false when
// costroid.yml on the base branch turns receipts off, and branch is the pull
// request's head branch.
func Newest(posted []Posted, branch string, allowed bool) (Reading, bool) {
	if len(posted) == 0 {
		return Reading{}, false
	}
	newest := slices.MaxFunc(posted, func(a, b Posted) int {
		return cmp.Or(a.CreatedAt.Compare(b.CreatedAt), cmp.Compare(a.ID, b.ID))
	})
	sum := sha256.Sum256([]byte(newest.Body))
	reading := Reading{Comment: newest, SHA256: hex.EncodeToString(sum[:])}
	if !allowed {
		reading.Refused = Off
		return reading, true
	}
	if !newest.UpdatedAt.Equal(newest.CreatedAt) {
		reading.Refused = Edited
		return reading, true
	}
	parsed, err := Parse(newest.Body)
	switch {
	case err != nil:
		reading.Refused = Invalid
	case parsed.Branch != branch:
		reading.Refused = OtherBranch
	default:
		reading.Receipt = parsed
	}
	return reading, true
}
