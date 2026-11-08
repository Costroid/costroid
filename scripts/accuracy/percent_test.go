package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/Costroid/costroid/internal/decimal"
	"github.com/Costroid/costroid/internal/focus"
	"github.com/Costroid/costroid/internal/reconcile"
)

// tableGap is the diff column the reconciliation table prints for one token
// class.
func tableGap(t *testing.T, delta, provider string) string {
	t.Helper()
	row := reconcile.Row{
		Key:   reconcile.Key{Source: "anthropic", Credential: credential, Day: firstDay},
		State: reconcile.Matched,
		Tokens: []reconcile.TokenCheck{{
			Class:    focus.Output,
			Ledger:   decimal.FromInt(1),
			Provider: mustNumber(t, provider),
			Delta:    mustNumber(t, delta),
		}},
	}
	var out bytes.Buffer
	if err := reconcile.Render(&out, reconcile.Result{Rows: []reconcile.Row{row}}); err != nil {
		t.Fatal(err)
	}
	for _, text := range strings.Split(out.String(), "\n") {
		if fields := strings.Fields(text); len(fields) > 7 && fields[0] == "anthropic" && fields[3] == string(focus.Output) {
			return fields[7]
		}
	}
	t.Fatalf("the table holds no token line:\n%s", out.String())
	return ""
}

// pageGap is the percentage the accuracy page prints beside one token class,
// and the largest token error it names for the agent.
func pageGap(delta, provider string) (string, string) {
	checks := []tokenFigure{{Class: string(focus.Output), Ledger: "1", Provider: provider, Delta: delta}}
	cell := tokensCell(checks)
	var version agentVersion
	version.add(line{tokens: checks})
	return cell[strings.LastIndex(cell, "(")+1 : len(cell)-1], version.tokens.cell()
}

func mustNumber(t *testing.T, text string) decimal.Decimal {
	t.Helper()
	value, err := decimal.ParseNumber(text)
	if err != nil {
		t.Fatal(err)
	}
	return value
}

func TestPercentOfAgreesWithThePage(t *testing.T) {
	for _, c := range []struct {
		name, delta, provider, want string
	}{
		{"zero provider, zero delta", "0", "0", "n/a"},
		{"zero provider", "5", "0", "n/a"},
		{"zero provider, negative delta", "-5", "0", "n/a"},
		{"negative provider", "1", "-4", "-25.00%"},
		{"negative provider, negative delta", "-1", "-4", "-25.00%"},
		{"negative delta", "-1", "4", "25.00%"},
		{"equal", "4", "4", "100.00%"},
		{"no delta", "0", "4", "0.00%"},
	} {
		t.Run(c.name, func(t *testing.T) {
			shared := reconcile.PercentOf(mustNumber(t, c.delta), mustNumber(t, c.provider)).String()
			table := tableGap(t, c.delta, c.provider)
			page, largest := pageGap(c.delta, c.provider)
			if shared != c.want || table != c.want || page != c.want || largest != c.want {
				t.Errorf("PercentOf %s, table %s, page %s, largest %s; want %s", shared, table, page, largest, c.want)
			}
		})
	}
}
