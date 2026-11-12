// Package focusfile reads a FOCUS cost file that a step of the customer's own
// CI wrote, such as an infrastructure estimator's output, and turns each of its
// rows into one Costroid row with the basis estimate.
//
// The file is the customer's. Its figure, currency, charge period, resource
// and service are kept. Its tags are not, and the pull request's join keys
// come from the CI system, never from the file.
package focusfile

import (
	"bytes"
	"cmp"
	"crypto/sha256"
	"encoding/csv"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/Costroid/costroid/internal/decimal"
	"github.com/Costroid/costroid/internal/focus"
)

const (
	// MaxBytes bounds the file: 4 MiB holds tens of thousands of resource rows.
	MaxBytes = 4 << 20
	// DefaultSource is the costroid/cost_source of a row when the caller names
	// none.
	DefaultSource = "infrastructure"
	// Basis is the costroid/cost_basis of every row this package writes.
	Basis = "estimate"

	// MaxText is the row contract's limit on a text column.
	MaxText = 512
	// maxSource is the tag convention's limit on costroid/cost_source.
	maxSource = 64
	// maxIntegerDigits is the row contract's limit on an amount's integer part.
	maxIntegerDigits = 26
	// maxQuoted bounds the part of a refused cell an error repeats.
	maxQuoted = 64

	// estimateUnit is PricingUnit and ConsumedUnit of a row: one estimate,
	// whose ListUnitPrice is the figure.
	estimateUnit = "Estimate"
	// notStated stands in for a text column the row contract requires and the
	// file does not fill.
	notStated = "unknown"
	// otherCategory is the FOCUS ServiceCategory and ServiceSubcategory of a
	// row whose file states none, or a category FOCUS does not define.
	otherCategory = "Other"
)

// The prefixes of the notes ChargeDescription opens with, which name the
// resource a row is for.
const (
	resourceNameNote = "resource name "
	resourceIDNote   = "resource id "
	noteSeparator    = "; "
)

// The FOCUS columns the reader reads. Any other column is not read.
const (
	columnListCost          = "ListCost"
	columnEffectiveCost     = "EffectiveCost"
	columnContractedCost    = "ContractedCost"
	columnBilledCost        = "BilledCost"
	columnCurrency          = "BillingCurrency"
	columnStart             = "ChargePeriodStart"
	columnEnd               = "ChargePeriodEnd"
	columnCategory          = "ChargeCategory"
	columnClass             = "ChargeClass"
	columnDescription       = "ChargeDescription"
	columnResourceID        = "ResourceId"
	columnResourceName      = "ResourceName"
	columnServiceName       = "ServiceName"
	columnServiceCategory   = "ServiceCategory"
	columnServiceSubcat     = "ServiceSubcategory"
	columnServiceProvider   = "ServiceProviderName"
	columnProvider          = "ProviderName"
	columnHostProvider      = "HostProviderName"
	columnInvoiceIssuer     = "InvoiceIssuerName"
	columnBillingAccount    = "BillingAccountId"
	columnBillingName       = "BillingAccountName"
	columnSubAccount        = "SubAccountId"
	columnSubAccountName    = "SubAccountName"
	columnRegionID          = "RegionId"
	columnRegionName        = "RegionName"
	columnSkuID             = "SkuId"
	columnSkuMeter          = "SkuMeter"
	columnSkuPriceID        = "SkuPriceId"
	columnTags              = "Tags"
	usageCategory           = "Usage"
	usageBasedFrequency     = "Usage-Based"
	standardPricingCategory = "Standard"
)

// CostColumns are the columns a figure is read from, in the order they are
// preferred. ListCost is where the row contract puts an estimate. Without it,
// the file must hold exactly one of the other three, because each means a
// different thing and nothing says which is the estimate.
var CostColumns = []string{columnListCost, columnEffectiveCost, columnContractedCost, columnBilledCost}

// serviceCategories are the values FOCUS 1.4 defines for ServiceCategory.
var serviceCategories = map[string]bool{
	"AI and Machine Learning": true, "Analytics": true, "Business Applications": true, "Compute": true,
	"Databases": true, "Developer Tools": true, "Multicloud": true, "Identity": true, "Integration": true,
	"Internet of Things": true, "Management and Governance": true, "Media": true, "Migration": true,
	"Mobile": true, "Networking": true, "Security": true, "Storage": true, "Web": true, otherCategory: true,
}

var (
	sourcePattern   = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)
	currencyPattern = regexp.MustCompile(`^[A-Z]{3}$`)
	regionPattern   = regexp.MustCompile(`^[a-z0-9-]{1,64}$`)
	amountPattern   = regexp.MustCompile(`^(0|[1-9][0-9]*)(\.[0-9]+)?$`)
)

// byteOrderMark opens a UTF-8 file that a spreadsheet saved.
var byteOrderMark = []byte{0xEF, 0xBB, 0xBF}

// reservedSources are the values of costroid/cost_source the tag convention
// defines for an agent's rows, which a file's rows never take.
var reservedSources = map[string]bool{"otlp": true}

// CheckSource holds a source name to its rule: 1 to 64 lower-case letters,
// digits and single hyphens, opening and closing with a letter or a digit.
func CheckSource(name string) error {
	switch {
	case len(name) > maxSource || !sourcePattern.MatchString(name):
		return fmt.Errorf("takes 1 to %d lower-case letters, digits and single hyphens, opening and closing with a letter or a digit", maxSource)
	case reservedSources[name]:
		return fmt.Errorf("takes a name of its own: %s names an agent's rows", name)
	}
	return nil
}

// Options are what the caller adds to every row.
type Options struct {
	// Source is costroid/cost_source, DefaultSource when empty.
	Source string
	// Tags are the CI system's join keys: repository, pull request, commit,
	// run and visibility. The file never sets them.
	Tags map[string]string
	// BillingAccount fills BillingAccountId on a row whose file states none.
	BillingAccount string
}

// File is what Read made of a file.
type File struct {
	Rows []focus.Row
	// CostColumn is the column every figure was read from.
	CostColumn string
	// TaggedRows counts the rows whose Tags the reader dropped.
	TaggedRows int
}

// Read reads a FOCUS cost file as CSV with a header, and returns one row per
// row of the file. One row the reader refuses refuses the file, so a partial
// estimate never reads as a smaller one. The same bytes and options give the
// same rows.
func Read(source io.Reader, options Options) (File, error) {
	data, err := io.ReadAll(io.LimitReader(source, MaxBytes+1))
	if err != nil {
		return File{}, fmt.Errorf("focusfile: read: %w", err)
	}
	if len(data) > MaxBytes {
		return File{}, &Refusal{Err: ErrTooLarge}
	}
	sourceName := options.Source
	if sourceName == "" {
		sourceName = DefaultSource
	}
	if err := CheckSource(sourceName); err != nil {
		return File{}, fmt.Errorf("focusfile: the source %w", err)
	}
	digest := sha256.Sum256(data)
	reader := csv.NewReader(bytes.NewReader(bytes.TrimPrefix(data, byteOrderMark)))
	names, err := reader.Read()
	if errors.Is(err, io.EOF) {
		return File{}, &Refusal{Err: ErrNoHeader}
	}
	if err != nil {
		return File{}, csvRefusal(err)
	}
	columns, err := readHeader(names)
	if err != nil {
		return File{}, err
	}
	file := File{CostColumn: columns.cost}
	writer := rowWriter{
		source:  sourceName,
		tags:    options.Tags,
		account: options.BillingAccount,
		standIn: sourceName + ":" + options.Tags[focus.TagCommitSHA] + ":" + options.Tags[focus.TagWorkflowRunID] + ":" + hex.EncodeToString(digest[:]),
	}
	for {
		record, err := reader.Read()
		if errors.Is(err, io.EOF) {
			return file, nil
		}
		if err != nil {
			return File{}, csvRefusal(err)
		}
		line, _ := reader.FieldPos(0)
		row, tagged, err := writer.row(fields{columns: columns, record: record, line: line})
		if err != nil {
			return File{}, err
		}
		if tagged {
			file.TaggedRows++
		}
		file.Rows = append(file.Rows, row)
	}
}

// header is where each column the reader reads sits in a record, and which
// column the figure is read from.
type header struct {
	index map[string]int
	cost  string
}

func readHeader(names []string) (header, error) {
	columns := header{index: map[string]int{}}
	for index, name := range names {
		if _, twice := columns.index[name]; twice {
			return header{}, &Refusal{Line: 1, Column: name, Err: ErrColumnTwice}
		}
		columns.index[name] = index
	}
	if _, found := columns.index[columnListCost]; found {
		columns.cost = columnListCost
	} else {
		var held []string
		for _, name := range CostColumns[1:] {
			if _, found := columns.index[name]; found {
				held = append(held, name)
			}
		}
		switch len(held) {
		case 0:
			return header{}, &Refusal{Line: 1, Err: ErrNoCostColumn}
		case 1:
			columns.cost = held[0]
		default:
			return header{}, &Refusal{Line: 1, Column: strings.Join(held, ", "), Err: ErrAmbiguousCost}
		}
	}
	for _, required := range []string{columnCurrency, columnStart, columnEnd} {
		if _, found := columns.index[required]; !found {
			return header{}, &Refusal{Line: 1, Column: required, Err: ErrMissingColumn}
		}
	}
	return columns, nil
}

// cell is a record's value in a column, empty when the header lacks it.
func (h header) cell(record []string, column string) string {
	index, found := h.index[column]
	if !found || index >= len(record) {
		return ""
	}
	return record[index]
}

// textColumns are the columns whose text a row keeps.
var textColumns = []string{
	columnDescription, columnResourceID, columnResourceName, columnServiceName, columnServiceCategory,
	columnServiceSubcat, columnServiceProvider, columnProvider, columnHostProvider, columnInvoiceIssuer,
	columnBillingAccount, columnBillingName, columnSubAccount, columnSubAccountName, columnRegionID,
	columnRegionName, columnSkuID, columnSkuMeter, columnSkuPriceID,
}

// rowWriter turns one record into one row.
type rowWriter struct {
	source  string
	tags    map[string]string
	account string
	standIn string
}

// fields is one record of the file, read by column name.
type fields struct {
	columns header
	record  []string
	line    int
}

func (f fields) cell(column string) string {
	return f.columns.cell(f.record, column)
}

// refuse names the line, the column and the value at fault.
func (f fields) refuse(column string, err error) error {
	return &Refusal{Line: f.line, Column: column, Value: f.cell(column), Err: err}
}

// charge is what every row must state: its figure, its currency and the period
// the figure covers.
type charge struct {
	figure     decimal.Decimal
	currency   string
	start, end time.Time
}

func (f fields) charge() (charge, error) {
	figure, err := readAmount(f.cell(f.columns.cost))
	if err != nil {
		return charge{}, f.refuse(f.columns.cost, err)
	}
	if !currencyPattern.MatchString(f.cell(columnCurrency)) {
		return charge{}, f.refuse(columnCurrency, ErrNoCurrency)
	}
	start, err := readInstant(f.cell(columnStart))
	if err != nil {
		return charge{}, f.refuse(columnStart, err)
	}
	end, err := readInstant(f.cell(columnEnd))
	if err != nil {
		return charge{}, f.refuse(columnEnd, err)
	}
	if !end.After(start) {
		return charge{}, f.refuse(columnEnd, ErrPeriodOrder)
	}
	return charge{figure: figure, currency: f.cell(columnCurrency), start: start, end: end}, nil
}

// check refuses text the row contract cannot hold, and a charge that is not
// usage.
func (f fields) check() error {
	for _, column := range textColumns {
		if utf8.RuneCountInString(f.cell(column)) > MaxText {
			return f.refuse(column, ErrTooLong)
		}
	}
	if category := f.cell(columnCategory); category != "" && category != usageCategory {
		return f.refuse(columnCategory, ErrNotUsage)
	}
	if f.cell(columnClass) != "" {
		return f.refuse(columnClass, ErrChargeClass)
	}
	return nil
}

// row builds one record's row. The second result says the record carried
// Tags, which the row drops.
func (w rowWriter) row(f fields) (focus.Row, bool, error) {
	if err := f.check(); err != nil {
		return focus.Row{}, false, err
	}
	stated, err := f.charge()
	if err != nil {
		return focus.Row{}, false, err
	}
	description := describe(f.cell(columnResourceName), f.cell(columnResourceID), f.cell(columnDescription), w.source)
	if utf8.RuneCountInString(description) > MaxText {
		return focus.Row{}, false, f.refuse(columnDescription, ErrTooLong)
	}
	meter := cmp.Or(f.cell(columnSkuMeter), notStated)
	estimate := Estimate{
		Figure: stated.figure, Currency: stated.currency, Start: stated.start, End: stated.end,
		Description: description, BillingAccount: cmp.Or(f.cell(columnBillingAccount), w.account),
		BillingAccountName: f.cell(columnBillingName), SubAccount: f.cell(columnSubAccount),
		SubAccountName: f.cell(columnSubAccountName),
		Provider:       cmp.Or(f.cell(columnServiceProvider), f.cell(columnProvider)),
		Host:           f.cell(columnHostProvider), InvoiceIssuer: f.cell(columnInvoiceIssuer),
		ServiceName: f.cell(columnServiceName), ServiceCategory: f.cell(columnServiceCategory),
		ServiceSubcategory: f.cell(columnServiceSubcat), SkuID: f.cell(columnSkuID), SkuMeter: meter,
		SkuPriceID: f.cell(columnSkuPriceID), Region: f.cell(columnRegionID), RegionName: f.cell(columnRegionName),
		Tags:  w.rowTags(),
		RowID: focus.RowIdentity(fmt.Sprintf("%s:%d", w.standIn, f.line), focus.TokenClass(meter)),
	}
	row := estimate.Row()
	return row, f.cell(columnTags) != "", nil
}

// Estimate is what one estimate row states. Row fills what the row contract
// requires and the estimate leaves empty, so the file's reader and the ledger
// write an estimate's row the same way.
type Estimate struct {
	Figure                                           decimal.Decimal
	Currency                                         string
	Start, End                                       time.Time
	Description                                      string
	BillingAccount, BillingAccountName               string
	SubAccount, SubAccountName                       string
	Provider, Host, InvoiceIssuer                    string
	ServiceName, ServiceCategory, ServiceSubcategory string
	SkuID, SkuMeter, SkuPriceID                      string
	Region, RegionName                               string
	Tags                                             map[string]string
	RowID                                            string
}

// Row is the estimate's row: the figure in ListCost, ListUnitPrice,
// ContractedCost and ContractedUnitPrice, 0 in BilledCost and EffectiveCost,
// one unit of Estimate, and a Usage charge.
func (e Estimate) Row() focus.Row {
	provider := cmp.Or(e.Provider, notStated)
	periodStart, periodEnd := focus.BillingPeriod(e.Start)
	row := focus.Row{
		BillingAccountID:    cmp.Or(e.BillingAccount, notStated),
		BillingAccountName:  optional(e.BillingAccountName),
		BillingCurrency:     e.Currency,
		BillingPeriodEnd:    periodEnd.Format(focus.InstantLayout),
		BillingPeriodStart:  periodStart.Format(focus.InstantLayout),
		ChargeCategory:      usageCategory,
		ChargeDescription:   e.Description,
		ChargeFrequency:     usageBasedFrequency,
		ChargePeriodEnd:     e.End.UTC().Format(focus.InstantLayout),
		ChargePeriodStart:   e.Start.UTC().Format(focus.InstantLayout),
		ConsumedQuantity:    decimal.FromInt(1),
		ConsumedUnit:        estimateUnit,
		ContractedCost:      e.Figure,
		ContractedUnitPrice: e.Figure,
		HostProviderName:    optional(cmp.Or(e.Host, provider)),
		InvoiceIssuerName:   cmp.Or(e.InvoiceIssuer, provider),
		ListCost:            e.Figure,
		ListUnitPrice:       e.Figure,
		PricingCategory:     standardPricingCategory,
		PricingQuantity:     decimal.FromInt(1),
		PricingUnit:         estimateUnit,
		ServiceCategory:     serviceCategory(e.ServiceCategory),
		ServiceName:         cmp.Or(e.ServiceName, notStated),
		ServiceProviderName: provider,
		ServiceSubcategory:  cmp.Or(e.ServiceSubcategory, otherCategory),
		SkuID:               cmp.Or(e.SkuID, notStated),
		SkuMeter:            focus.TokenClass(cmp.Or(e.SkuMeter, notStated)),
		SkuPriceID:          cmp.Or(e.SkuPriceID, notStated),
		SubAccountID:        optional(e.SubAccount),
		SubAccountName:      optional(e.SubAccountName),
		Tags:                e.Tags,
		RowID:               e.RowID,
	}
	if regionPattern.MatchString(e.Region) && e.Region != focus.RegionGlobal {
		region, name := e.Region, cmp.Or(e.RegionName, e.Region)
		row.RegionID, row.RegionName = &region, &name
	}
	return row
}

// ReadFigure reads an estimate's figure as the file states it: a plain
// decimal of at most 26 integer digits and 12 fractional digits, never
// negative. The error is one of the refusals.
func ReadFigure(text string) (decimal.Decimal, error) {
	return readAmount(text)
}

// rowTags are the tags of every row: the CI system's join keys, the source and
// the basis, and the grades of a row nothing holds and nothing verified.
func (w rowWriter) rowTags() map[string]string {
	tags := map[string]string{
		focus.TagCostSource:       w.source,
		focus.TagCostBasis:        Basis,
		focus.TagAttributionGrade: "unattributed",
		focus.TagEnforcementGrade: "none",
		focus.TagRunIdentity:      "unverified",
	}
	for key, value := range w.tags {
		tags[key] = value
	}
	return tags
}

// readAmount reads a figure: a plain decimal of at most 26 integer digits and
// the contract's 12 fractional digits, never negative.
func readAmount(text string) (decimal.Decimal, error) {
	switch {
	case text == "":
		return decimal.Decimal{}, ErrNoAmount
	case strings.HasPrefix(text, "-") && amountPattern.MatchString(text[1:]):
		return decimal.Decimal{}, ErrNegative
	case len(text) > decimal.MaxLength || !amountPattern.MatchString(text):
		return decimal.Decimal{}, ErrNotDecimal
	}
	whole, _, _ := strings.Cut(text, ".")
	if len(whole) > maxIntegerDigits {
		return decimal.Decimal{}, ErrNotDecimal
	}
	amount, err := decimal.Parse(text)
	if err != nil {
		return decimal.Decimal{}, ErrNotDecimal
	}
	if amount.Scale() > focus.MaxScale {
		return decimal.Decimal{}, ErrScale
	}
	return amount, nil
}

// readInstant reads FOCUS's Date/Time form, YYYY-MM-DDTHH:mm:ssZ.
func readInstant(text string) (time.Time, error) {
	if text == "" {
		return time.Time{}, ErrNoPeriod
	}
	at, err := time.Parse(focus.InstantLayout, text)
	if err != nil || at.Format(focus.InstantLayout) != text {
		return time.Time{}, ErrInstant
	}
	return at, nil
}

// describe writes ChargeDescription: the resource's name, its id, and the
// file's own description, as notes. A row with none of them says where the
// estimate came from.
func describe(name, id, description, source string) string {
	var notes []string
	if name != "" {
		notes = append(notes, resourceNameNote+name)
	}
	if id != "" {
		notes = append(notes, resourceIDNote+id)
	}
	if description != "" {
		notes = append(notes, description)
	}
	if len(notes) == 0 {
		return "estimate from " + source
	}
	return strings.Join(notes, noteSeparator)
}

// Resource is the resource a row's ChargeDescription names: its name, else its
// id, empty when it names neither.
func Resource(description string) string {
	var id string
	for _, note := range strings.Split(description, noteSeparator) {
		if name, found := strings.CutPrefix(note, resourceNameNote); found {
			return name
		}
		if stated, found := strings.CutPrefix(note, resourceIDNote); found && id == "" {
			id = stated
		}
	}
	return id
}

func serviceCategory(stated string) string {
	if serviceCategories[stated] {
		return stated
	}
	return otherCategory
}

func optional(text string) *string {
	if text == "" {
		return nil
	}
	return &text
}
