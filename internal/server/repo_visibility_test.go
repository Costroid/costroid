package server

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/Costroid/costroid/internal/focus"
)

func visibilityRow(value string) map[string]any {
	r := row("req-1", focus.Output, 100)
	tags(r)[focus.TagRepoVisibility] = value
	return r
}

// TestServeAcceptsRepoVisibility sends rows that carry costroid/repo_visibility.
// Each of the three values is stored with the row, and Postgres reads it back
// from the stored row's document. A value outside the three is refused with
// 422 ROW_INVALID, and the refusal never repeats the value. A costroid/ key
// the contract does not define is still refused.
func TestServeAcceptsRepoVisibility(t *testing.T) {
	for _, value := range []string{focus.VisibilityPublic, focus.VisibilityPrivate, focus.VisibilityInternal} {
		t.Run(value, func(t *testing.T) {
			w, ledger := memoryWorld(t)
			w.rows(w.token(), lines(t, visibilityRow(value))).want(t, http.StatusOK)
			if stored := ledger.only(t, tenantA).Row.Tags[focus.TagRepoVisibility]; stored != value {
				t.Errorf("stored as %q, want %q", stored, value)
			}
		})
	}
	t.Run("a row that states none stores none", func(t *testing.T) {
		w, ledger := memoryWorld(t)
		w.rows(w.token(), lines(t, row("req-1", focus.Output, 100))).want(t, http.StatusOK)
		if stored, found := ledger.only(t, tenantA).Row.Tags[focus.TagRepoVisibility]; found {
			t.Errorf("stored %q", stored)
		}
	})

	for name, tc := range map[string]struct{ tag, value string }{
		"a value outside the three": {focus.TagRepoVisibility, sentinel},
		"upper case":                {focus.TagRepoVisibility, "Public"},
		"empty":                     {focus.TagRepoVisibility, ""},
		"an unnamed costroid key":   {"costroid/repo_private", "true"},
	} {
		t.Run(name, func(t *testing.T) {
			w, ledger := memoryWorld(t)
			r := row("req-1", focus.Output, 100)
			tags(r)[tc.tag] = tc.value
			got := w.rows(w.token(), lines(t, r)).want(t, http.StatusUnprocessableEntity)
			if got.code(t) != codeInvalidRow || len(ledger.rows) != 0 {
				t.Fatalf("code %s, %d rows stored: %s", got.code(t), len(ledger.rows), got.body)
			}
			if strings.Contains(strings.ToLower(string(got.body)), strings.ToLower(sentinel)) {
				t.Errorf("the refusal repeats the value: %s", got.body)
			}
		})
	}

	t.Run("postgres", func(t *testing.T) {
		w := postgresWorld(t)
		r := visibilityRow(focus.VisibilityPublic)
		w.rows(w.token(), lines(t, r)).want(t, http.StatusOK)
		ctx := context.Background()
		conn, err := pgx.Connect(ctx, w.db.SuperURL)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = conn.Close(ctx) }()
		var read string
		if err := conn.QueryRow(ctx, `SELECT document->'Tags'->>$2 FROM focus_rows WHERE row_id = $1`, r["x_CostroidRowId"], focus.TagRepoVisibility).Scan(&read); err != nil {
			t.Fatal(err)
		}
		if read != focus.VisibilityPublic {
			t.Errorf("reads back as %q", read)
		}
	})
}
