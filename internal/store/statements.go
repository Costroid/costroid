package store

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/Costroid/costroid/internal/focus"
)

// verifiedIdentity is costroid/run_identity on a row a CI token verified.
const verifiedIdentity = "token_verified"

// owner is who a statement of a row speaks for. A statement a CI token
// verified is owned by the token's repository and run, which the join took
// from the token. Every statement the ledger did not verify has one owner, the
// zero owner. The ledger keeps a row id once per owner: the unique index
// focus_rows_statement holds it, so a run's statement never meets another
// run's.
type owner struct {
	verified  bool
	repo, run string
}

func ownerOf(record *Record) owner {
	tags := record.Row.Tags
	if tags[focus.TagRunIdentity] != verifiedIdentity {
		return owner{}
	}
	return owner{verified: true, repo: tags[TagRepo], run: tags[TagWorkflowRunID]}
}

// leftOutBy reports whether a held statement keeps a statement of o out. Within
// one owner the first statement stands. A statement the ledger did not verify
// stands only where no statement is held at all, because a verified one
// outranks it and the shared owner holds one already. A verified statement is
// never kept out by another owner's.
func (o owner) leftOutBy(held owner) bool {
	return !o.verified || o == held
}

// readStatements reads the owners holding each id.
const readStatements = `
SELECT row_id, run_identity = '` + verifiedIdentity + `', coalesce(repo, ''), coalesce(workflow_run_id, '')
FROM focus_rows WHERE row_id = ANY($1::text[])`

// statements maps a row id to the owners of its stored statements.
type statements map[string][]owner

func heldStatements(ctx context.Context, tx pgx.Tx, ids []string) (statements, error) {
	held := statements{}
	rows, err := tx.Query(ctx, readStatements, ids)
	if err != nil {
		return nil, fmt.Errorf("store: read stored statements: %w", err)
	}
	var id string
	var o owner
	_, err = pgx.ForEachRow(rows, []any{&id, &o.verified, &o.repo, &o.run}, func() error {
		if !o.verified {
			o = owner{}
		}
		held[id] = append(held[id], o)
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("store: read stored statements: %w", err)
	}
	return held, nil
}

// meetings holds which of a batch's records met a statement of the same
// charge from another owner. Two runs that state one row id contest it. A
// statement the ledger verified and one it did not meet by rank, by the row id
// or by another lifetime of a request's cache writes, and the verified one
// outranks the other.
type meetings struct {
	contested, outranked map[int]bool
}

func newMeetings() meetings {
	return meetings{contested: map[int]bool{}, outranked: map[int]bool{}}
}

// byRank notes record i, of owner mine, meeting a held statement of the other
// rank.
func (m meetings) byRank(i int, mine, held owner) {
	if mine.verified != held.verified {
		m.outranked[i] = true
	}
}

// judge marks in skip each record a held statement of its own id keeps out,
// and notes in m each record that meets another owner's statement of its id.
func (held statements) judge(records []Record, skip map[int]bool, m meetings) {
	for i := range records {
		mine := ownerOf(&records[i])
		for _, h := range held[records[i].Row.RowID] {
			if mine.leftOutBy(h) {
				skip[i] = true
			}
			if mine.verified && h.verified && h != mine {
				m.contested[i] = true
			}
			m.byRank(i, mine, h)
		}
	}
}
