// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Costroid Authors

package gcpfocusbq

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/Costroid/costroid/internal/focus"
	"github.com/Costroid/costroid/internal/ingest"
)

// Name is the connector registry and default credential-slot name.
const Name = "gcp-focus-bq"

// Field describes one selected top-level BigQuery field. Kind is the scalar
// wire type or RECORD; Repeated marks the nested wrapper-array encoding.
type Field struct {
	Name     string
	Kind     string
	Repeated bool
}

// PinnedFields is Google's 2026-07-10 Preview schema: 37 FOCUS fields and 18
// x_ extensions. Every query uses this explicit list; tables.get may add fields
// but may not remove any of these without an actionable failure.
var PinnedFields = []Field{
	{Name: "AvailabilityZone", Kind: "STRING"},
	{Name: "BilledCost", Kind: "NUMERIC"},
	{Name: "BillingAccountId", Kind: "STRING"},
	{Name: "BillingCurrency", Kind: "STRING"},
	{Name: "BillingPeriodStart", Kind: "TIMESTAMP"},
	{Name: "BillingPeriodEnd", Kind: "TIMESTAMP"},
	{Name: "ChargeCategory", Kind: "STRING"},
	{Name: "ChargeClass", Kind: "STRING"},
	{Name: "ChargeDescription", Kind: "STRING"},
	{Name: "ChargePeriodStart", Kind: "TIMESTAMP"},
	{Name: "ChargePeriodEnd", Kind: "TIMESTAMP"},
	{Name: "ConsumedQuantity", Kind: "NUMERIC"},
	{Name: "ConsumedUnit", Kind: "STRING"},
	{Name: "ContractedCost", Kind: "NUMERIC"},
	{Name: "ContractedUnitPrice", Kind: "NUMERIC"},
	{Name: "ListCost", Kind: "NUMERIC"},
	{Name: "ListUnitPrice", Kind: "NUMERIC"},
	{Name: "PricingCategory", Kind: "STRING"},
	{Name: "PricingQuantity", Kind: "NUMERIC"},
	{Name: "PricingUnit", Kind: "STRING"},
	{Name: "ProviderName", Kind: "STRING"},
	{Name: "PublisherName", Kind: "STRING"},
	{Name: "RegionId", Kind: "STRING"},
	{Name: "RegionName", Kind: "STRING"},
	{Name: "ResourceId", Kind: "STRING"},
	{Name: "ResourceName", Kind: "STRING"},
	{Name: "ServiceName", Kind: "STRING"},
	{Name: "SkuId", Kind: "STRING"},
	{Name: "SkuPriceId", Kind: "STRING"},
	{Name: "SubAccountId", Kind: "STRING"},
	{Name: "SubAccountName", Kind: "STRING"},
	{Name: "EffectiveCost", Kind: "NUMERIC"},
	{Name: "PricingCurrency", Kind: "STRING"},
	{Name: "PricingCurrencyContractedUnitPrice", Kind: "NUMERIC"},
	{Name: "PricingCurrencyEffectiveCost", Kind: "NUMERIC"},
	{Name: "PricingCurrencyListUnitPrice", Kind: "NUMERIC"},
	{Name: "BillingAccountType", Kind: "STRING"},
	{Name: "x_CostType", Kind: "STRING"},
	{Name: "x_Credits", Kind: "RECORD", Repeated: true},
	{Name: "x_CurrencyConversionRate", Kind: "FLOAT"},
	{Name: "x_ExportTime", Kind: "TIMESTAMP"},
	{Name: "x_Labels", Kind: "RECORD", Repeated: true},
	{Name: "x_Location", Kind: "STRING"},
	{Name: "x_Project", Kind: "RECORD"},
	{Name: "x_ProjectLabels", Kind: "RECORD", Repeated: true},
	{Name: "x_ServiceId", Kind: "STRING"},
	{Name: "x_SystemLabels", Kind: "RECORD", Repeated: true},
	{Name: "x_Tags", Kind: "RECORD", Repeated: true},
	{Name: "x_SubscriptionInstanceId", Kind: "STRING"},
	{Name: "x_PriceEffectivePriceDefault", Kind: "NUMERIC"},
	{Name: "x_PriceListPriceConsumptionModel", Kind: "NUMERIC"},
	{Name: "x_CostAtEffectivePriceDefault", Kind: "NUMERIC"},
	{Name: "x_CostAtListConsumptionModel", Kind: "NUMERIC"},
	{Name: "x_ConsumptionModelId", Kind: "STRING"},
	{Name: "x_ConsumptionModelDescription", Kind: "STRING"},
}

// PinnedColumnNames returns a copy of the explicit SELECT set.
func PinnedColumnNames() []string {
	out := make([]string, len(PinnedFields))
	for i, f := range PinnedFields {
		out[i] = f.Name
	}
	return out
}

// Coordinates identifies one linked export and its query execution location.
type Coordinates struct {
	DatasetProject string
	Dataset        string
	Table          string
	Location       string
	JobProject     string
	Since          string
}

var (
	projectID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$`)
	datasetID = regexp.MustCompile(`^[A-Za-z0-9_]+$`)
	tableID   = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)
)

func (c *Coordinates) validate() error {
	if c.JobProject == "" {
		c.JobProject = c.DatasetProject
	}
	switch {
	case !projectID.MatchString(c.DatasetProject):
		return errors.New("--dataset-project is required and must be a BigQuery project ID")
	case len(c.Dataset) > 1024 || !datasetID.MatchString(c.Dataset):
		return errors.New("--dataset is required and must contain only letters, digits, or underscores")
	case len(c.Table) > 1024 || !tableID.MatchString(c.Table):
		return errors.New("--table is required and must contain only letters, digits, underscores, or hyphens")
	case !projectID.MatchString(c.JobProject):
		return errors.New("--job-project must be a BigQuery project ID")
	case strings.TrimSpace(c.Location) == "" || strings.ContainsAny(c.Location, "?&#/\\"):
		return errors.New("--location is required and must be the BigQuery dataset location")
	}
	if c.Since != "" {
		if _, err := time.Parse("2006-01", c.Since); err != nil {
			return fmt.Errorf("invalid --since %q, want YYYY-MM", c.Since)
		}
	}
	return nil
}

func (c Coordinates) tableCoordinates() string {
	return c.DatasetProject + "/" + c.Dataset + "/" + c.Table
}

func (c Coordinates) tableSQL() string {
	return "`" + c.DatasetProject + "." + c.Dataset + "." + c.Table + "`"
}

func sourceIdentity(c Coordinates, month string) string {
	return c.tableCoordinates() + "/" + month
}

// ChangeState is one month's persisted change tuple. Token is MAX export time
// plus row count, not a digest; LastModified is MAX(x_ExportTime), or the
// content-derived table last-modified timestamp when every row's watermark is
// null. Size is the period row count.
type ChangeState struct {
	Key          string
	Token        string
	LastModified time.Time
	Size         int64
}

// Equal reports whether all four persisted tuple fields match.
func (s ChangeState) Equal(other ChangeState) bool {
	return s.Key == other.Key && s.Token == other.Token && s.LastModified.Equal(other.LastModified) && s.Size == other.Size
}

// Period is one invoice month discovered by the aggregate query.
type Period struct {
	Billing string
	State   ChangeState
	Conn    *Connector
	Err     error
}

// Skipped reports whether the prior tenant-scoped tuple matched.
func (p Period) Skipped() bool { return p.Conn == nil && p.Err == nil }

// Discover probes the table and executes exactly one aggregate query, returning
// invoice months oldest-first. A malformed aggregate row degrades to its month
// when the month itself is readable; source-level probe/query failures abort.
func Discover(ctx context.Context, client *Client, coords Coordinates, prior map[string]ChangeState) ([]Period, error) {
	if client == nil {
		return nil, errors.New("BigQuery client must not be nil")
	}
	if err := coords.validate(); err != nil {
		return nil, err
	}
	tableModified, err := client.probeTable(ctx, coords)
	if err != nil {
		return nil, err
	}
	query := "SELECT FORMAT_DATE('%Y-%m', DATE(BillingPeriodStart, 'UTC')) AS billing_month, " +
		"MAX(x_ExportTime) AS max_export_time, COUNT(*) AS row_count FROM " + coords.tableSQL() +
		" GROUP BY billing_month ORDER BY billing_month"
	resp, err := client.startQuery(ctx, coords, query)
	if err != nil {
		return nil, fmt.Errorf("discovering BigQuery FOCUS billing periods: %w", err)
	}
	if resp.PageToken != "" {
		return nil, errors.New("BigQuery aggregate query unexpectedly paginated; reduce the export's distinct billing-period count or re-run")
	}
	periods := make([]Period, 0, len(resp.Rows))
	for _, row := range resp.Rows {
		month, err := aggregateMonth(row)
		if err != nil {
			return nil, fmt.Errorf("decoding BigQuery aggregate row: %w", err)
		}
		if coords.Since != "" && month < coords.Since {
			continue
		}
		state, err := aggregateState(row, coords, tableModified)
		if err != nil {
			periods = append(periods, Period{Billing: month, Err: err})
			continue
		}
		if stored, ok := prior[sourceIdentity(coords, month)]; ok && stored.Equal(state) {
			periods = append(periods, Period{Billing: month, State: state})
			continue
		}
		periods = append(periods, Period{
			Billing: month,
			State:   state,
			Conn: &Connector{
				client: client, coords: coords, month: month, contentHash: state.Token,
			},
		})
	}
	sort.Slice(periods, func(i, j int) bool { return periods[i].Billing < periods[j].Billing })
	if len(periods) == 0 {
		return nil, errors.New("BigQuery FOCUS table contains no billing periods in the requested --since window")
	}
	return periods, nil
}

func aggregateMonth(row bqRow) (string, error) {
	if len(row.F) != 3 {
		return "", fmt.Errorf("aggregate row has %d cells, want 3", len(row.F))
	}
	month, err := scalarString(row.F[0].V, "billing_month")
	if err != nil {
		return "", err
	}
	if _, err := time.Parse("2006-01", month); err != nil {
		return "", fmt.Errorf("aggregate billing_month %q is not YYYY-MM", month)
	}
	return month, nil
}

func aggregateState(row bqRow, coords Coordinates, tableModified time.Time) (ChangeState, error) {
	maxRaw, err := scalarString(row.F[1].V, "max_export_time")
	if err != nil {
		return ChangeState{}, err
	}
	countRaw, err := scalarString(row.F[2].V, "row_count")
	if err != nil {
		return ChangeState{}, err
	}
	count, err := strconv.ParseInt(countRaw, 10, 64)
	if err != nil || count < 0 {
		return ChangeState{}, fmt.Errorf("aggregate row_count %q is not a non-negative integer", countRaw)
	}
	lastModified := tableModified
	tokenTime := "null"
	if maxRaw != "" {
		lastModified, err = timestampFromMicros(maxRaw, "max_export_time")
		if err != nil {
			return ChangeState{}, err
		}
		tokenTime = maxRaw
	}
	return ChangeState{
		Key: coords.tableCoordinates(), Token: tokenTime + "|" + countRaw,
		LastModified: lastModified, Size: count,
	}, nil
}

// Connector reads one discovered invoice month.
type Connector struct {
	client      *Client
	coords      Coordinates
	month       string
	contentHash string
}

var _ ingest.Connector = (*Connector)(nil)

func (c *Connector) Name() string                { return Name }
func (c *Connector) FOCUSVersion() focus.Version { return focus.V1_2 }
func (c *Connector) BillingPeriod() string       { return c.month }
func (c *Connector) SourceIdentity() string      { return sourceIdentity(c.coords, c.month) }
func (c *Connector) ContentHash(context.Context) (string, error) {
	return c.contentHash, nil
}

// Records starts a constant-literal month query. Timestamp conversion remains
// Go-side; SQL does not FORMAT the selected timestamp cells.
func (c *Connector) Records(ctx context.Context) (ingest.RecordReader, error) {
	start, err := time.Parse("2006-01", c.month)
	if err != nil {
		return nil, fmt.Errorf("invalid discovered month %q", c.month)
	}
	end := start.AddDate(0, 1, 0)
	cols := make([]string, len(PinnedFields))
	for i, f := range PinnedFields {
		cols[i] = "`" + f.Name + "`"
	}
	query := "SELECT " + strings.Join(cols, ", ") + " FROM " + c.coords.tableSQL() +
		" WHERE BillingPeriodStart >= TIMESTAMP('" + start.UTC().Format(time.RFC3339) + "')" +
		" AND BillingPeriodStart < TIMESTAMP('" + end.UTC().Format(time.RFC3339) + "')" +
		" ORDER BY BillingPeriodStart, ChargePeriodStart, x_ExportTime, SkuId, ResourceId"
	resp, err := c.client.startQuery(ctx, c.coords, query)
	if err != nil {
		return nil, fmt.Errorf("querying BigQuery billing period %s: %w", c.month, err)
	}
	jobID := resp.JobReference.JobID
	if resp.PageToken != "" && jobID == "" {
		return nil, errors.New("BigQuery paginated response omitted jobReference.jobId")
	}
	return &recordReader{ctx: ctx, conn: c, rows: resp.Rows, pageToken: resp.PageToken, jobID: jobID}, nil
}

type recordReader struct {
	ctx       context.Context
	conn      *Connector
	rows      []bqRow
	index     int
	rowNumber int
	pageToken string
	jobID     string
	closed    bool
}

func (r *recordReader) Next() (ingest.Row, error) {
	if r.closed {
		return ingest.Row{}, io.EOF
	}
	for r.index >= len(r.rows) {
		if r.pageToken == "" {
			return ingest.Row{}, io.EOF
		}
		resp, err := r.conn.client.pollQueryResults(r.ctx, r.conn.coords, r.jobID, r.pageToken)
		if err != nil {
			return ingest.Row{}, fmt.Errorf("paging BigQuery billing period %s: %w", r.conn.month, err)
		}
		r.rows, r.index, r.pageToken = resp.Rows, 0, resp.PageToken
		if resp.JobReference.JobID != "" {
			r.jobID = resp.JobReference.JobID
		}
	}
	row := r.rows[r.index]
	r.index++
	r.rowNumber++
	record, err := decodeRecord(row)
	if err != nil {
		return ingest.Row{}, fmt.Errorf("BigQuery row %d: %w", r.rowNumber, err)
	}
	GapFill(record)
	return ingest.Row{Number: r.rowNumber, Record: record}, nil
}

func (r *recordReader) Close() error {
	r.closed = true
	r.rows = nil
	return nil
}

func decodeRecord(row bqRow) (focus.RawRecord, error) {
	if len(row.F) != len(PinnedFields) {
		return nil, fmt.Errorf("row has %d cells, want %d selected columns", len(row.F), len(PinnedFields))
	}
	rec := make(focus.RawRecord, len(PinnedFields))
	for i, field := range PinnedFields {
		if field.Name == "x_Labels" {
			tags, err := labelsToTags(row.F[i].V)
			if err != nil {
				return nil, err
			}
			if tags != "" {
				rec["Tags"] = tags
			}
			continue
		}
		if field.Repeated || field.Kind == "RECORD" {
			// GBQ-5/6: x_Credits and every unselected extension record are
			// deliberately unread; their wrapper shape is still enforced by the fake.
			continue
		}
		value, err := scalarString(row.F[i].V, field.Name)
		if err != nil {
			return nil, err
		}
		if value == "" {
			continue
		}
		if field.Kind == "TIMESTAMP" {
			t, err := timestampFromMicros(value, field.Name)
			if err != nil {
				return nil, err
			}
			value = t.Format(time.RFC3339Nano)
		}
		rec[field.Name] = value
	}
	return rec, nil
}

func scalarString(raw json.RawMessage, column string) (string, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return "", nil
	}
	var value string
	if err := json.Unmarshal(raw, &value); err != nil {
		return "", fmt.Errorf("column %s: expected a string scalar or null in the BigQuery v2 row envelope", column)
	}
	return value, nil
}

func timestampFromMicros(raw, column string) (time.Time, error) {
	micros, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		return time.Time{}, fmt.Errorf("column %s: expected an int64-microsecond TIMESTAMP string (formatOptions.useInt64Timestamp=true), got %q; scientific/float timestamps are rejected to preserve exactness", column, raw)
	}
	return time.UnixMicro(micros).UTC(), nil
}

func labelsToTags(raw json.RawMessage) (string, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return "", nil
	}
	var wrappers []struct {
		V json.RawMessage `json:"v"`
	}
	if err := json.Unmarshal(raw, &wrappers); err != nil {
		return "", errors.New("column x_Labels: expected a repeated-record wrapper array")
	}
	tags := make(map[string]string, len(wrappers))
	for i, wrapper := range wrappers {
		var record struct {
			F []bqCell `json:"f"`
		}
		if err := json.Unmarshal(wrapper.V, &record); err != nil || len(record.F) != 2 {
			return "", fmt.Errorf("column x_Labels element %d: expected a Key/Value record", i+1)
		}
		key, err := scalarString(record.F[0].V, "x_Labels.Key")
		if err != nil {
			return "", err
		}
		value, err := scalarString(record.F[1].V, "x_Labels.Value")
		if err != nil {
			return "", err
		}
		if _, duplicate := tags[key]; duplicate {
			return "", fmt.Errorf("column x_Labels: duplicate label key %q; FOCUS Tags requires unique keys", key)
		}
		tags[key] = value
	}
	if len(tags) == 0 {
		return "", nil
	}
	encoded, err := json.Marshal(tags)
	if err != nil {
		return "", fmt.Errorf("encoding x_Labels as FOCUS Tags: %w", err)
	}
	return string(encoded), nil
}

// GapFill applies GBQ-1 through GBQ-3 in place. GBQ-4 is enforced by the
// shared validator; GBQ-5/6 are handled while decoding; GBQ-7 needs no code.
func GapFill(rec focus.RawRecord) {
	if rec["ServiceCategory"] == "" {
		rec["ServiceCategory"] = "Other"
	}
	if rec["InvoiceIssuerName"] == "" {
		rec["InvoiceIssuerName"] = "Google Cloud"
	}
	if rec["ProviderName"] == "" && rec["PublisherName"] == "" {
		rec["ProviderName"] = "Google Cloud"
	}
}
