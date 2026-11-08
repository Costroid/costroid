package server

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/Costroid/costroid/internal/focus"
)

// namedTags are the three tags a caller names, as costroid run --tag writes
// them.
var namedTags = map[string]string{focus.TagWorkItem: "ENG-142", focus.TagArm: "b", focus.TagTeam: "platform"}

func namedRow() map[string]any {
	r := row("req-1", focus.Output, 100)
	for tag, value := range namedTags {
		tags(r)[tag] = value
	}
	return r
}

// TestServeAcceptsNamedTags sends a row that carries the three tags a caller
// names. It is stored with them, and Postgres reads them back from the stored
// row. A row with a costroid/ key the contract does not define is still
// refused as before, and so is a named tag whose value misses its rule, whose
// value the refusal never repeats.
func TestServeAcceptsNamedTags(t *testing.T) {
	w, ledger := memoryWorld(t)
	w.rows(w.token(), lines(t, namedRow())).want(t, http.StatusOK)
	stored := ledger.only(t, tenantA).Row.Tags
	for tag, value := range namedTags {
		if stored[tag] != value {
			t.Errorf("%s stored as %q, want %q", tag, stored[tag], value)
		}
	}

	for name, tc := range map[string]struct{ tag, value string }{
		"an unnamed costroid key": {"costroid/branch", "main"},
		"a key as a value":        {focus.TagTeam, "sk-ant-api03-" + sentinel},
		"outside the allowlist":   {focus.TagArm, "<" + sentinel + ">"},
		"no issue key":            {focus.TagWorkItem, strings.ToLower(sentinel)},
	} {
		t.Run(name, func(t *testing.T) {
			w, ledger := memoryWorld(t)
			r := namedRow()
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
		r := namedRow()
		w.rows(w.token(), lines(t, r)).want(t, http.StatusOK)
		ctx := context.Background()
		conn, err := pgx.Connect(ctx, w.db.SuperURL)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = conn.Close(ctx) }()
		var document string
		if err := conn.QueryRow(ctx, `SELECT document->>'Tags' FROM focus_rows WHERE row_id = $1`, r["x_CostroidRowId"]).Scan(&document); err != nil {
			t.Fatal(err)
		}
		var read map[string]string
		if err := json.Unmarshal([]byte(document), &read); err != nil {
			t.Fatal(err)
		}
		for tag, value := range namedTags {
			if read[tag] != value {
				t.Errorf("%s reads back as %q, want %q", tag, read[tag], value)
			}
		}
	})
}
