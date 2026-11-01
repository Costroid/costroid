package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/Costroid/costroid/internal/fanout"
	"github.com/Costroid/costroid/internal/focus"
)

// Record is one row and the facts about its run that have no FOCUS column.
type Record struct {
	Row        focus.Row
	RunAttempt string
	// Actor is an SCM login or a per-tenant pseudonym, never an address.
	Actor string
	Event string
	// TokenCommitSHA is the commit the CI token verifies, empty without one.
	TokenCommitSHA string
	// HeadBasis says where costroid/commit_sha came from, empty when the row
	// has none.
	HeadBasis string
}

// Values of HeadBasis.
const (
	HeadFromToken = "token"
	HeadStated    = "stated"
)

// Batch is one ingest request's writes.
type Batch struct {
	Records []Record
	// Drops counts fields dropped at the edge, by field name.
	Drops map[string]int64
	Day   time.Time
}

// Receipt says what a batch changed.
type Receipt struct {
	Inserted   int
	Duplicates int
}

// columns holds one array per inserted column, in insertRows' order.
type columns struct {
	ids, currencies, categories, meters, units, quantities, listCosts, effectiveCosts []string
	starts, ends, bases, sources, grades, enforcement, identities                     []string
	agents, models, repos, prs, shas, runs, attempts, actors, events, catalogs, docs  []string
	tokenCommits, headBases, accounts                                                 []string
}

// insertFrom selects a batch's rows. Its one source is a function scan, so a
// statement after it may name the rows as t.
const insertFrom = `
INSERT INTO focus_rows (
	tenant_id, row_id, billing_currency, service_category, sku_meter, consumed_unit,
	consumed_quantity, list_cost, effective_cost, charge_period_start, charge_period_end,
	cost_basis, cost_source, attribution_grade, enforcement_grade, run_identity,
	agent, model, repo, pr, commit_sha, workflow_run_id, run_attempt, actor, event,
	catalog_version, document, token_commit_sha, head_basis, billing_account_id)
SELECT costroid_tenant(), id, currency, category, meter, unit,
	quantity::numeric, list::numeric, effective::numeric, start::timestamptz, stop::timestamptz,
	basis, source, grade, enforcement, identity,
	NULLIF(agent, ''), NULLIF(model, ''), NULLIF(repo, ''), NULLIF(pr, ''), NULLIF(sha, ''),
	NULLIF(run, ''), NULLIF(attempt, ''), NULLIF(actor, ''), NULLIF(event, ''),
	NULLIF(catalog, ''), doc::json, NULLIF(token_commit, ''), NULLIF(head_basis, ''), account
FROM unnest($1::text[], $2::text[], $3::text[], $4::text[], $5::text[], $6::text[], $7::text[], $8::text[],
	$9::text[], $10::text[], $11::text[], $12::text[], $13::text[], $14::text[], $15::text[], $16::text[],
	$17::text[], $18::text[], $19::text[], $20::text[], $21::text[], $22::text[], $23::text[], $24::text[],
	$25::text[], $26::text[], $27::text[], $28::text[], $29::text[])
	AS t(id, currency, category, meter, unit, quantity, list, effective, start, stop, basis, source, grade,
	     enforcement, identity, agent, model, repo, pr, sha, run, attempt, actor, event, catalog, doc,
	     token_commit, head_basis, account)`

// A batch holds no stored row in the common case, and ON CONFLICT pays for a
// speculative insert on every row of it. So the first attempt reads the primary
// key instead. A repeat inside the batch, or another sender writing the same
// row at this moment, then fails the unique index, and the second attempt takes
// ON CONFLICT. The index is the authority on both.
const (
	insertNewRows = insertFrom + `
WHERE NOT EXISTS (SELECT FROM focus_rows f WHERE f.row_id = t.id)`
	insertRows = insertFrom + `
ON CONFLICT (tenant_id, row_id) DO NOTHING`
)

// uniqueViolation is Postgres's error code for a broken unique index.
const uniqueViolation = "23505"

// batchWorkMem keeps a batch in memory while the statement reads it. A batch
// past it spills to a temporary file, which is slower and changes nothing else.
const batchWorkMem = `SET LOCAL work_mem = '64MB'`

const addDrops = `
INSERT INTO ingest_drops (tenant_id, day, field, count)
SELECT costroid_tenant(), $1::date, field, count
FROM unnest($2::text[], $3::bigint[]) AS t(field, count)
ON CONFLICT (tenant_id, day, field) DO UPDATE SET count = ingest_drops.count + excluded.count`

const dayLayout = "2006-01-02"

// Ingest writes a batch whole or not at all. A row id already stored for this
// tenant is a duplicate and changes nothing.
func (s *Store) Ingest(ctx context.Context, tenant TenantID, batch Batch) (Receipt, error) {
	cols, err := columnsOf(batch.Records)
	if err != nil {
		return Receipt{}, err
	}
	receipt, err := s.writeBatch(ctx, tenant, batch, cols, insertNewRows)
	var refused *pgconn.PgError
	if errors.As(err, &refused) && refused.Code == uniqueViolation {
		return s.writeBatch(ctx, tenant, batch, cols, insertRows)
	}
	return receipt, err
}

func (s *Store) writeBatch(ctx context.Context, tenant TenantID, batch Batch, cols *columns, statement string) (Receipt, error) {
	var receipt Receipt
	err := s.inTenant(ctx, tenant, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, batchWorkMem); err != nil {
			return fmt.Errorf("store: set work_mem: %w", err)
		}
		tag, err := tx.Exec(ctx, statement, cols.args()...)
		if err != nil {
			return fmt.Errorf("store: insert rows: %w", err)
		}
		receipt.Inserted = int(tag.RowsAffected())
		receipt.Duplicates = len(batch.Records) - receipt.Inserted
		return writeDrops(ctx, tx, batch)
	})
	if err != nil {
		return Receipt{}, err
	}
	return receipt, nil
}

func writeDrops(ctx context.Context, tx pgx.Tx, batch Batch) error {
	if len(batch.Drops) == 0 {
		return nil
	}
	fields := slices.Sorted(maps.Keys(batch.Drops))
	counts := make([]int64, len(fields))
	for i, field := range fields {
		counts[i] = batch.Drops[field]
	}
	if _, err := tx.Exec(ctx, addDrops, batch.Day.UTC().Format(dayLayout), fields, counts); err != nil {
		return fmt.Errorf("store: add drop counters: %w", err)
	}
	return nil
}

func columnsOf(records []Record) (*columns, error) {
	docs := make([]string, len(records))
	err := fanout.Each(len(records), func(_, i int) error {
		doc, err := json.Marshal(&records[i].Row)
		if err != nil {
			return fmt.Errorf("store: encode row %s: %w", records[i].Row.RowID, err)
		}
		docs[i] = string(doc)
		return nil
	})
	if err != nil {
		return nil, err
	}
	c := &columns{docs: docs}
	for i := range records {
		c.add(&records[i])
	}
	return c, nil
}

func (c *columns) add(record *Record) {
	row := &record.Row
	tag := func(key string) string { return row.Tags[key] }
	catalogVersion := ""
	if row.CatalogVersion != nil {
		catalogVersion = *row.CatalogVersion
	}
	c.ids = append(c.ids, row.RowID)
	c.currencies = append(c.currencies, row.BillingCurrency)
	c.categories = append(c.categories, row.ServiceCategory)
	c.meters = append(c.meters, string(row.SkuMeter))
	c.units = append(c.units, row.ConsumedUnit)
	c.quantities = append(c.quantities, row.ConsumedQuantity.String())
	c.listCosts = append(c.listCosts, row.ListCost.String())
	c.effectiveCosts = append(c.effectiveCosts, row.EffectiveCost.String())
	c.starts = append(c.starts, row.ChargePeriodStart)
	c.ends = append(c.ends, row.ChargePeriodEnd)
	c.bases = append(c.bases, tag(focus.TagCostBasis))
	c.sources = append(c.sources, tag(focus.TagCostSource))
	c.grades = append(c.grades, tag(focus.TagAttributionGrade))
	c.enforcement = append(c.enforcement, tag(focus.TagEnforcementGrade))
	c.identities = append(c.identities, tag(focus.TagRunIdentity))
	c.agents = append(c.agents, tag(focus.TagAgent))
	c.models = append(c.models, tag(focus.TagModel))
	c.repos = append(c.repos, tag(TagRepo))
	c.prs = append(c.prs, tag(TagPR))
	c.shas = append(c.shas, tag(TagCommitSHA))
	c.runs = append(c.runs, tag(TagWorkflowRunID))
	c.attempts = append(c.attempts, record.RunAttempt)
	c.actors = append(c.actors, record.Actor)
	c.events = append(c.events, record.Event)
	c.catalogs = append(c.catalogs, catalogVersion)
	c.tokenCommits = append(c.tokenCommits, record.TokenCommitSHA)
	c.headBases = append(c.headBases, record.HeadBasis)
	c.accounts = append(c.accounts, row.BillingAccountID)
}

func (c *columns) args() []any {
	return []any{
		c.ids, c.currencies, c.categories, c.meters, c.units, c.quantities, c.listCosts, c.effectiveCosts,
		c.starts, c.ends, c.bases, c.sources, c.grades, c.enforcement, c.identities,
		c.agents, c.models, c.repos, c.prs, c.shas, c.runs, c.attempts, c.actors, c.events, c.catalogs, c.docs,
		c.tokenCommits, c.headBases, c.accounts,
	}
}

// Tag keys of the convention that the focus package leaves to the join.
const (
	TagRepo          = focus.TagRepo
	TagPR            = focus.TagPR
	TagCommitSHA     = focus.TagCommitSHA
	TagWorkflowRunID = focus.TagWorkflowRunID
)
