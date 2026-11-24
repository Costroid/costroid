package comment

import (
	"fmt"
	"regexp"
	"strconv"
	"time"

	"github.com/Costroid/costroid/internal/decimal"
	"github.com/Costroid/costroid/internal/focus"
)

// schemaV013 is the result object that holds a receipt.
const schemaV013 = "costroid.result/0.13"

// ReceiptReason is why a report read none of a receipt's sessions. The list
// is closed.
type ReceiptReason string

// The reasons a receipt is refused, each with the sentence that says why.
const (
	ReceiptEdited      ReceiptReason = "receipt_edited"
	ReceiptOtherBranch ReceiptReason = "receipt_other_branch"
	ReceiptInvalid     ReceiptReason = "receipt_invalid"
	ReceiptsOff        ReceiptReason = "receipts_off"
)

// receiptReasons says, after the login that posted it, why each refusal read
// nothing.
var receiptReasons = map[ReceiptReason]string{
	ReceiptEdited:      "the receipt %s posted was edited after it was posted, so its sessions were not read. A new count is a new comment.",
	ReceiptOtherBranch: "the receipt %s posted names another branch than the %s's, so its sessions were not read.",
	ReceiptInvalid:     "the comment %s posted is not a valid receipt, and `costroid receipt` prints a valid one, so its sessions were not read.",
	ReceiptsOff:        "`costroid.yml` on the base branch turns receipts off, so the receipt %s posted was not read.",
}

// receiptBasis is the one basis a receipt's figure is stated at: a session
// file does not state how the session was billed.
const receiptBasis = string(focus.ListEquivalent)

// receiptDay is a UTC day as a receipt states it.
var receiptDay = regexp.MustCompile(`^[0-9]{4}-[0-9]{2}-[0-9]{2}$`)

// Receipt is the receipt of developer-machine sessions the pull request's
// author posted as a comment, as the report read it: the comment it came
// from, and its sessions priced at the report's catalog, or why it was
// refused. Nothing a receipt states reaches a rule, a budget, a total, the
// verdict sentence, the title, the conclusion or an output: it is one line
// on each surface, and receipts in the result object.
type Receipt struct {
	// CommentID, CreatedAt and SHA256 pin the comment: its id and its
	// creation as the host states them, and the digest of its body.
	CommentID int64
	CreatedAt time.Time
	SHA256    string
	// Author is the login that posted it, as the host states it. The line
	// names it, and the result object names no login.
	Author string
	// Refused is why none of its sessions was read, empty when they were.
	Refused ReceiptReason
	// Sessions counts the sessions read, from FirstDay to LastDay, UTC days.
	Sessions          int
	FirstDay, LastDay string
	// Figures are its rows priced at the report's catalog, one per currency.
	Figures []ReceiptFigure
	// Held counts the sessions the receipt states that the run's own rows
	// also hold, which are left out of it.
	Held int
}

// ReceiptFigure is the exact sum of ListCost over a receipt's rows in one
// currency.
type ReceiptFigure struct {
	Currency string
	Cost     decimal.Decimal
	Rows     int
}

// BlockReceipt is one entry of a result/0.13 object's receipts: the comment,
// then either the sessions and their figures, or the reason it was refused.
type BlockReceipt struct {
	Comment  BlockReceiptComment  `json:"comment"`
	Sessions int                  `json:"sessions,omitempty"`
	FirstDay string               `json:"first_day,omitempty"`
	LastDay  string               `json:"last_day,omitempty"`
	Figures  []BlockReceiptFigure `json:"figures,omitempty"`
	Reason   ReceiptReason        `json:"reason,omitempty"`
}

// BlockReceiptComment pins the comment a receipt came from. It names no
// login.
type BlockReceiptComment struct {
	ID        int64  `json:"id"`
	CreatedAt string `json:"created_at"`
	SHA256    string `json:"sha256"`
}

// BlockReceiptFigure is one ReceiptFigure. ListCost is empty when amounts are
// withheld.
type BlockReceiptFigure struct {
	Currency  string `json:"currency"`
	CostBasis string `json:"cost_basis"`
	ListCost  string `json:"list_cost,omitempty"`
	Rows      int    `json:"rows"`
}

// receipt is the receipt when the workflow asked for receipts and every
// field the line and the object state is well formed, and nil otherwise: a
// malformed value is dropped whole.
func (i Input) receipt() *Receipt {
	r := i.Receipt
	if !i.ReceiptsAsked || r == nil || r.CommentID < 1 || r.CreatedAt.IsZero() || !digestPattern.MatchString(r.SHA256) || !loginPattern.MatchString(r.Author) {
		return nil
	}
	if r.Refused != "" {
		if _, known := receiptReasons[r.Refused]; !known {
			return nil
		}
		return r
	}
	if r.Sessions < 1 || len(r.Figures) == 0 || !receiptDay.MatchString(r.FirstDay) || !receiptDay.MatchString(r.LastDay) || r.LastDay < r.FirstDay {
		return nil
	}
	for _, figure := range r.Figures {
		if !currencyPattern.MatchString(figure.Currency) || figure.Rows < 1 || figure.Cost.Sign() < 0 {
			return nil
		}
	}
	return r
}

// blockReceipts are the receipts the result object states: none when the
// report read none, so an object without one is the object an earlier
// release wrote, byte for byte.
func (i Input) blockReceipts(shown bool) []BlockReceipt {
	r := i.receipt()
	if r == nil {
		return nil
	}
	entry := BlockReceipt{
		Comment: BlockReceiptComment{ID: r.CommentID, CreatedAt: r.CreatedAt.UTC().Format(focus.InstantLayout), SHA256: r.SHA256},
		Reason:  r.Refused,
	}
	if r.Refused == "" {
		entry.Sessions, entry.FirstDay, entry.LastDay = r.Sessions, r.FirstDay, r.LastDay
		for _, figure := range r.Figures {
			line := BlockReceiptFigure{Currency: figure.Currency, CostBasis: receiptBasis, Rows: figure.Rows}
			if shown {
				line.ListCost = figure.Cost.String()
			}
			entry.Figures = append(entry.Figures, line)
		}
	}
	return []BlockReceipt{entry}
}

// withReceipts is the object with the receipts the report read added, as
// result/0.13 when it holds one. The verdict sentence is built from the
// object before this, so it never reads a receipt.
func (i Input) withReceipts(block Block, shown bool) Block {
	block.Receipts = i.blockReceipts(shown)
	if len(block.Receipts) > 0 {
		block.Schema = schemaV013
	}
	return block
}

// receiptLine is the one line a receipt gets on each surface: its figure
// apart from the run's, who stated it and that nothing checked it, or why it
// was refused, or that none was posted where the workflow asked for one. It
// is empty where the workflow did not ask.
func (i Input) receiptLine(shown bool) string {
	if !i.ReceiptsAsked {
		return ""
	}
	r := i.receipt()
	if r == nil {
		return fmt.Sprintf("No developer-machine sessions stated for this %s. The figures here are the CI run's alone.", i.request())
	}
	author := Data(r.Author).Code()
	if r.Refused != "" {
		said := fmt.Sprintf(receiptReasons[r.Refused], author)
		if r.Refused == ReceiptOtherBranch {
			said = fmt.Sprintf(receiptReasons[r.Refused], author, i.request())
		}
		return fmt.Sprintf("No developer-machine figure (%s): %s The verdict reads no receipt.", r.Refused, said)
	}
	sessions := fmt.Sprintf("%d %s of Claude Code %s", r.Sessions, plural(r.Sessions, "session", "sessions"), receiptDays(r.FirstDay, r.LastDay))
	stated := fmt.Sprintf("as %s stated them in %s on %s", author, i.receiptComment(r.CommentID), r.CreatedAt.UTC().Format(time.DateOnly))
	line := "Developer-machine sessions, unchecked: "
	if shown {
		amounts := make([]string, len(r.Figures))
		for index, figure := range r.Figures {
			amounts[index] = Money(figure.Currency, figure.Cost.String()) + atList([]string{receiptBasis})
		}
		line += listed(amounts, 0) + ", " + sessions + ", " + stated + "."
	} else {
		line += sessions + ", priced" + atList([]string{receiptBasis}) + ", " + stated + "."
	}
	line += " No budget reads this figure, and it is not added to the run's."
	if r.Held > 0 {
		line += fmt.Sprintf(" %d %s the run's own rows also hold %s left out of it.", r.Held, plural(r.Held, "session", "sessions"), plural(r.Held, "is", "are"))
	}
	if !shown {
		line += " Amounts are withheld here."
	}
	return line
}

// receiptDays names the UTC days a receipt's sessions span.
func receiptDays(first, last string) string {
	if first == last {
		return "on " + first + " (UTC)"
	}
	return "from " + first + " to " + last + " (UTC)"
}

// receiptComment links the comment by the host's address for it, built from
// the repository, the pull request's number and the comment's id alone, and
// names it without a link when one of them is not well formed.
func (i Input) receiptComment(id int64) string {
	if i.PullRequest < 1 || !repositoryPattern.MatchString(i.Repository) || len(i.Repository) > maxRunURLLength {
		return "a comment"
	}
	return "[a comment](https://" + i.Repository + "/pull/" + strconv.Itoa(i.PullRequest) + "#issuecomment-" + strconv.FormatInt(id, 10) + ")"
}

// withoutReceiptAmounts drops each receipt figure's amount, as a copy that
// withholds amounts does.
func withoutReceiptAmounts(receipts []BlockReceipt) []BlockReceipt {
	if receipts == nil {
		return nil
	}
	copied := make([]BlockReceipt, len(receipts))
	for index, entry := range receipts {
		entry.Figures = append([]BlockReceiptFigure(nil), entry.Figures...)
		for figure := range entry.Figures {
			entry.Figures[figure].ListCost = ""
		}
		copied[index] = entry
	}
	return copied
}
