package export

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"slices"

	"github.com/Costroid/costroid/internal/focus"
)

// FOCUS 1.5's candidate release, the tag v1.5-cr of the specification
// (https://github.com/FinOps-Open-Cost-and-Usage-Spec/FOCUS_Spec/releases/tag/v1.5-cr,
// FOCUS_spec-v1.5-cr.md and model-1.5-cr.json, retrieved 2026-11-12), adds
// the SkuPriceDetails properties of AI Model Billing and the columns of
// requester attribution. Its header reads "v1.5 Candidate Release" and "not
// an official publication", so the file declares the version as that tag
// names it, 1.5-cr, in the schema metadata the candidate's section 6.4 asks
// for.
const (
	focus15Version = "1.5-cr"
	// focus15SchemaDate is the day this schema was written. A change to the
	// columns makes a new schema, with a new id and a new date.
	focus15SchemaDate = "2026-11-12T00:00:00Z"
	focus15Generator  = "Costroid"
	focus15Instance   = "Costroid rows"
	focus15Dataset    = "CostAndUsage"
	focus15DataFile   = "data/cost-and-usage.csv"
	focus15DataIndex  = "data/cost-and-usage-metadata.json"
	focus15Generators = "metadata/data_generator.json"
	focus15Instances  = "metadata/dataset_instances.json"
	focus15TagPrefix  = "costroid/"
)

// focus15Columns are the file's columns: every candidate column a row fills,
// in the candidate's alphabetical order, then the custom columns. Each FOCUS
// column has the candidate's data type, from its Cost and Usage column table.
var focus15Columns = []struct{ name, kind string }{
	{"BilledCost", "DECIMAL"}, {"BillingAccountId", "STRING"}, {"BillingAccountName", "STRING"},
	{"BillingCurrency", "STRING"}, {"BillingPeriodEnd", "DATETIME"}, {"BillingPeriodStart", "DATETIME"},
	{"ChargeCategory", "STRING"}, {"ChargeClass", "STRING"}, {"ChargeDescription", "STRING"},
	{"ChargeFrequency", "STRING"}, {"ChargePeriodEnd", "DATETIME"}, {"ChargePeriodStart", "DATETIME"},
	{"ConsumedQuantity", "DECIMAL"}, {"ConsumedUnit", "STRING"}, {"ContractedCost", "DECIMAL"},
	{"ContractedUnitPrice", "DECIMAL"}, {"CredentialId", "STRING"}, {"EffectiveCost", "DECIMAL"},
	{"HostProviderName", "STRING"}, {"InvoiceIssuerName", "STRING"}, {"ListCost", "DECIMAL"},
	{"ListUnitPrice", "DECIMAL"}, {"PricingCategory", "STRING"}, {"PricingQuantity", "DECIMAL"},
	{"PricingUnit", "STRING"}, {"PrincipalId", "STRING"}, {"RegionId", "STRING"},
	{"RegionName", "STRING"}, {"RequesterDetails", "JSON"}, {"ServiceCategory", "STRING"},
	{"ServiceName", "STRING"}, {"ServiceProviderName", "STRING"}, {"ServiceSubcategory", "STRING"},
	{"SkuId", "STRING"}, {"SkuMeter", "STRING"}, {"SkuPriceDetails", "JSON"},
	{"SkuPriceId", "STRING"}, {"SubAccountId", "STRING"}, {"SubAccountName", "STRING"},
	{"Tags", "JSON"}, {"x_CostroidCatalogVersion", "STRING"}, {"x_CostroidOvershootBound", "DECIMAL"},
	{"x_CostroidRowId", "STRING"}, {"x_CostroidServiceTier", "STRING"},
}

// skuPriceDetails holds the properties a row states. ModelId is the row's
// costroid/model. The row states no model developer, family or version, so
// the file names none. TokenCacheAction has no lifetime, and the candidate
// says cache retention duration is not one of its values, so a cache write
// keeps its lifetime in x_CacheWriteLifetime.
type skuPriceDetails struct {
	ModelID            string `json:"ModelId,omitempty"`
	TokenCacheAction   string `json:"TokenCacheAction,omitempty"`
	TokenDirection     string `json:"TokenDirection,omitempty"`
	CacheWriteLifetime string `json:"x_CacheWriteLifetime,omitempty"`
}

// tokenProperties are the candidate's TokenDirection and TokenCacheAction of a
// class, and a cache write's lifetime. An informational class repeats tokens
// a billed class counts, so it carries neither property and no query that
// selects them counts its tokens twice. A class this version does not know
// carries none.
func tokenProperties(class focus.TokenClass) skuPriceDetails {
	switch {
	case class.Informational():
		return skuPriceDetails{}
	case class == focus.InputUncached:
		return skuPriceDetails{TokenDirection: "Input", TokenCacheAction: "Uncached"}
	case class == focus.CacheRead:
		return skuPriceDetails{TokenDirection: "Input", TokenCacheAction: "Read"}
	case class.Lifetime() != "":
		return skuPriceDetails{TokenDirection: "Input", TokenCacheAction: "Write", CacheWriteLifetime: class.Lifetime()}
	case class == focus.Output:
		return skuPriceDetails{TokenDirection: "Output"}
	}
	return skuPriceDetails{}
}

// columnDefinition is one column of the schema metadata. NumberScale is the
// row contract's 12 fractional digits.
type columnDefinition struct {
	ColumnName          string   `json:"ColumnName"`
	DataType            string   `json:"DataType"`
	NumberScale         int      `json:"NumberScale,omitempty"`
	ProviderTagPrefixes []string `json:"ProviderTagPrefixes,omitempty"`
}

type focus15Schema struct {
	SchemaID          string             `json:"SchemaId"`
	FocusVersion      string             `json:"FocusVersion"`
	CreationDate      string             `json:"CreationDate"`
	DatasetInstanceID string             `json:"DatasetInstanceId"`
	ColumnDefinition  []columnDefinition `json:"ColumnDefinition"`
}

type datasetInstance struct {
	DatasetInstanceID   string `json:"DatasetInstanceId"`
	DatasetInstanceName string `json:"DatasetInstanceName"`
	FocusDatasetID      string `json:"FocusDatasetId"`
}

type dataLocation struct {
	Filepath   string `json:"filepath"`
	TotalBytes int    `json:"total_bytes"`
	TotalRows  int    `json:"total_rows"`
}

type dataIndex struct {
	SchemaID     string         `json:"SchemaId"`
	DataLocation []dataLocation `json:"data_location"`
}

// writeFocus15CR writes the rows as the candidate's Cost and Usage dataset,
// with its metadata laid out as the candidate's metadata examples lay theirs
// out: the data generator, the dataset instance and the schema under
// metadata/, and beside the data, a file naming its schema.
func writeFocus15CR(rows table) (Export, error) {
	records := make([][]string, 0, len(rows.records))
	for _, source := range rows.rows() {
		record, err := focus15Record(source)
		if err != nil {
			return Export{}, err
		}
		records = append(records, record)
	}
	header := make([]string, len(focus15Columns))
	for i, column := range focus15Columns {
		header[i] = column.name
	}
	body, err := csvFile(header, records)
	if err != nil {
		return Export{}, err
	}
	instance := datasetInstance{DatasetInstanceID: derivedID(focus15Generator + " " + focus15Instance), DatasetInstanceName: focus15Instance, FocusDatasetID: focus15Dataset}
	schema, err := focus15SchemaOf(instance.DatasetInstanceID)
	if err != nil {
		return Export{}, err
	}
	parts := []struct {
		name, note string
		value      any
	}{
		{focus15Generators, "FOCUS data generator metadata", map[string]string{"DataGenerator": focus15Generator}},
		{focus15Instances, "FOCUS dataset instance metadata", []datasetInstance{instance}},
		{"metadata/schemas/schema-" + schema.SchemaID + ".json", "FOCUS schema metadata", schema},
		{focus15DataIndex, "FOCUS schema reference", dataIndex{SchemaID: schema.SchemaID, DataLocation: []dataLocation{{Filepath: focus15DataFile, TotalBytes: len(body), TotalRows: len(records)}}}},
	}
	files := []File{{Name: focus15DataFile, Body: body, Rows: len(records)}}
	for _, part := range parts {
		encoded, err := encodeJSON(part.value)
		if err != nil {
			return Export{}, err
		}
		files = append(files, File{Name: part.name, Body: encoded, Note: part.note})
	}
	return Export{Files: files}, nil
}

// focus15Record is a row's cells in the file's columns.
func focus15Record(source row) ([]string, error) {
	_, tags, err := orderedTags(source.cell("Tags"))
	if err != nil {
		return nil, fmt.Errorf("%s: %w", source.name(), err)
	}
	credential, err := credentialOf(source, tags)
	if err != nil {
		return nil, err
	}
	details, err := skuPriceDetailsOf(source, tags)
	if err != nil {
		return nil, err
	}
	requester := ""
	if credential != "" {
		requester = `[{"key":"Credential","value":{}}]`
	}
	record := make([]string, len(focus15Columns))
	for i, column := range focus15Columns {
		switch column.name {
		case "CredentialId":
			record[i] = credential
		case "PrincipalId":
			// No row states a principal, so PrincipalId is null.
		case "RequesterDetails":
			record[i] = requester
		case "SkuPriceDetails":
			record[i] = details
		default:
			record[i] = source.cell(column.name)
		}
	}
	return record, nil
}

// credentialOf is a row's CredentialId: the credential its SubAccountId
// states. The candidate states CredentialId "MUST NOT contain a credential
// value that authenticates a request", so a SubAccountId that reads like a key
// refuses the export, and the value is never repeated. A SubAccountId that is
// the session id names no credential, so CredentialId is null.
func credentialOf(source row, tags map[string]string) (string, error) {
	stated := source.cell("SubAccountId")
	if focus.ReadsLikeKey(stated) {
		return "", &RowsError{Format: Focus15CR, Rows: []string{source.name()}, Reason: "SubAccountId reads like a key, and a CredentialId never holds a value that authenticates a request"}
	}
	if stated == tags[focus.TagSessionID] {
		return "", nil
	}
	return stated, nil
}

// skuPriceDetailsOf is a row's SkuPriceDetails, null where the row has no
// SkuPriceId or states no property.
func skuPriceDetailsOf(source row, tags map[string]string) (string, error) {
	if source.cell("SkuPriceId") == "" {
		return "", nil
	}
	details := tokenProperties(focus.TokenClass(source.cell("SkuMeter")))
	details.ModelID = tags[focus.TagModel]
	if details == (skuPriceDetails{}) {
		return "", nil
	}
	var cell bytes.Buffer
	encoder := json.NewEncoder(&cell)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(details); err != nil {
		return "", fmt.Errorf("%s: write SkuPriceDetails: %w", source.name(), err)
	}
	return string(bytes.TrimSpace(cell.Bytes())), nil
}

// focus15SchemaOf is the schema of the file's columns. Its id is derived from
// the version and the columns, so the same columns keep the same id.
func focus15SchemaOf(instance string) (focus15Schema, error) {
	definitions := make([]columnDefinition, len(focus15Columns))
	for i, column := range focus15Columns {
		definitions[i] = columnDefinition{ColumnName: column.name, DataType: column.kind}
		if column.kind == "DECIMAL" {
			definitions[i].NumberScale = focus.MaxScale
		}
		if column.name == "Tags" {
			definitions[i].ProviderTagPrefixes = []string{focus15TagPrefix}
		}
	}
	shape, err := json.Marshal(definitions)
	if err != nil {
		return focus15Schema{}, fmt.Errorf("write the FOCUS schema: %w", err)
	}
	return focus15Schema{
		SchemaID:          derivedID(focus15Version + " " + string(shape)),
		FocusVersion:      focus15Version,
		CreationDate:      focus15SchemaDate,
		DatasetInstanceID: instance,
		ColumnDefinition:  definitions,
	}, nil
}

// derivedID is a GUID derived from text: the first 16 bytes of its SHA-256,
// marked as a version 8 UUID, the version RFC 9562 leaves to custom content.
func derivedID(text string) string {
	sum := sha256.Sum256([]byte(text))
	id := slices.Clone(sum[:16])
	id[6] = id[6]&0x0f | 0x80
	id[8] = id[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", id[0:4], id[4:6], id[6:8], id[8:10], id[10:16])
}

// encodeJSON is a value as indented JSON with a final newline, its strings
// as written.
func encodeJSON(value any) ([]byte, error) {
	var out bytes.Buffer
	encoder := json.NewEncoder(&out)
	encoder.SetEscapeHTML(false)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(value); err != nil {
		return nil, fmt.Errorf("write JSON: %w", err)
	}
	return out.Bytes(), nil
}
