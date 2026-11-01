package server

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/Costroid/costroid/internal/catalog"
	"github.com/Costroid/costroid/internal/decimal"
	"github.com/Costroid/costroid/internal/fanout"
	"github.com/Costroid/costroid/internal/focus"
	"github.com/Costroid/costroid/internal/otlp"
)

// Row columns the result-stream path reads. A sender's cost columns are not
// among them: they are never read, so they cannot change anything.
const (
	columnSku       = "SkuId"
	columnPrice     = "SkuPriceId"
	columnMeter     = "SkuMeter"
	columnQuantity  = "ConsumedQuantity"
	columnStart     = "ChargePeriodStart"
	columnEnd       = "ChargePeriodEnd"
	columnTags      = "Tags"
	columnOvershoot = "x_CostroidOvershootBound"
	columnRowID     = "x_CostroidRowId"
	columnRegion    = "RegionId"
	columnTier      = "x_CostroidServiceTier"
	columnAccount   = "SubAccountId"
	columnBilling   = "BillingAccountId"
)

// ignoredColumns are the row contract's other columns. The server computes or
// fixes each one, so a sender's value is left behind without being a drop.
var ignoredColumns = columnSet(
	"BillingAccountName", "BillingCurrency", "BillingPeriodEnd", "BillingPeriodStart",
	"ChargeCategory", "ChargeClass", "ChargeDescription", "ChargeFrequency", "ConsumedUnit", "HostProviderName",
	"InvoiceIssuerName", "PricingCategory", "PricingQuantity", "PricingUnit", "ServiceCategory", "ServiceName",
	"RegionName", "ServiceProviderName", "ServiceSubcategory", "SubAccountName", "x_CostroidCatalogVersion",
)

// costColumns are a sender's own figures. Each one seen is counted.
var costColumns = columnSet(
	"BilledCost", "ContractedCost", "ContractedUnitPrice", "EffectiveCost", "ListCost", "ListUnitPrice",
)

func columnSet(names ...string) map[string]bool {
	m := make(map[string]bool, len(names))
	for _, name := range names {
		m[name] = true
	}
	return m
}

var (
	rowIDPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)
	skuPattern   = regexp.MustCompile(`^([a-z0-9-]+)/(.+)/([a-z0-9_]+)$`)
	overTier     = regexp.MustCompile(`^over-([0-9]{1,12})$`)

	sourcePattern = regexp.MustCompile(`^[a-z0-9]+([_-][a-z0-9]+)*$`)

	regionPattern = regexp.MustCompile(`^[a-z0-9-]{1,64}$`)
	tierPattern   = regexp.MustCompile(`^[a-z0-9_-]{1,64}$`)
)

func invalidRow(line int, reason string) *refusal {
	return refuse(http.StatusUnprocessableEntity, codeInvalidRow, fmt.Sprintf("Line %d: %s.", line, reason))
}

// parseRows reads the JSON lines costroid run writes. A refusal names the
// earliest bad line, however the work was divided.
func parseRows(body []byte, drops map[string]int64) ([]charge, error) {
	lines, scanErr := splitLines(body)
	charges := make([]charge, len(lines))
	workerDrops := make([]map[string]int64, fanout.Workers(len(lines)))
	for worker := range workerDrops {
		workerDrops[worker] = map[string]int64{}
	}
	err := fanout.Each(len(lines), func(worker, i int) error {
		var err error
		charges[i], err = parseRow(lines[i].text, lines[i].number, workerDrops[worker])
		return err
	})
	if err != nil {
		return nil, err
	}
	if scanErr != nil {
		return nil, scanErr
	}
	for _, counted := range workerDrops {
		for field, count := range counted {
			drops[field] += count
		}
	}
	return charges, nil
}

type bodyLine struct {
	number int
	text   []byte
}

// splitLines cuts a body into its non-blank lines. The error is for what stopped
// the scan, and every line before it is still returned, so a bad row above an
// oversized one is the one reported.
func splitLines(body []byte) ([]bodyLine, error) {
	scanner := bufio.NewScanner(bytes.NewReader(body))
	scanner.Buffer(make([]byte, 0, 4096), maxRowBytes)
	lines := make([]bodyLine, 0, bytes.Count(body, []byte{'\n'})+1)
	number := 0
	for scanner.Scan() {
		number++
		if text := bytes.TrimSpace(scanner.Bytes()); len(text) > 0 {
			lines = append(lines, bodyLine{number: number, text: slices.Clone(text)})
		}
	}
	if errors.Is(scanner.Err(), bufio.ErrTooLong) {
		return lines, invalidRow(number+1, "the row is over the size limit")
	}
	if err := scanner.Err(); err != nil {
		return lines, fmt.Errorf("server: scan rows: %w", err)
	}
	return lines, nil
}

func parseRow(text []byte, line int, drops map[string]int64) (charge, error) {
	var columns map[string]json.RawMessage
	if err := json.Unmarshal(text, &columns); err != nil {
		return charge{}, invalidRow(line, "not a JSON object")
	}
	countLeftBehind(columns, drops)
	fields := rowFields{columns: columns, line: line}
	c := charge{source: sourceStream}
	var err error
	if c.rowID, err = fields.matching(columnRowID, rowIDPattern); err != nil {
		return charge{}, err
	}
	if err := fields.readSku(&c); err != nil {
		return charge{}, err
	}
	if err := fields.readQuantity(&c); err != nil {
		return charge{}, err
	}
	if err := fields.readPeriod(&c); err != nil {
		return charge{}, err
	}
	if err := fields.readTags(&c, drops); err != nil {
		return charge{}, err
	}
	if err := fields.readRouting(&c); err != nil {
		return charge{}, err
	}
	return c, fields.readOvershoot(&c)
}

func countLeftBehind(columns map[string]json.RawMessage, drops map[string]int64) {
	for name := range columns {
		switch {
		case costColumns[name]:
			drops[otlp.DropClientCost]++
		case ignoredColumns[name], name == columnSku, name == columnPrice, name == columnMeter, name == columnQuantity,
			name == columnStart, name == columnEnd, name == columnTags, name == columnOvershoot, name == columnRowID,
			name == columnRegion, name == columnTier, name == columnAccount, name == columnBilling:
		default:
			drops[otlp.DropLabel(name)]++
		}
	}
}

type rowFields struct {
	columns map[string]json.RawMessage
	line    int
}

func (f rowFields) text(column string) (string, error) {
	raw, ok := f.columns[column]
	if !ok {
		return "", nil
	}
	var value *string
	if err := json.Unmarshal(raw, &value); err != nil {
		return "", invalidRow(f.line, column+" is not a string")
	}
	if value == nil {
		return "", nil
	}
	return *value, nil
}

func (f rowFields) matching(column string, pattern *regexp.Regexp) (string, error) {
	text, err := f.text(column)
	if err != nil {
		return "", err
	}
	if !pattern.MatchString(text) {
		return "", invalidRow(f.line, column+" is not valid")
	}
	return text, nil
}

// readSku takes provider, model and class from the row's SkuId, and service
// tier and context tier from its SkuPriceId. These are usage facts on a par
// with the token count, and reconciliation checks them the same way. The price
// the id names is never read, and an id for another model or class is ignored.
func (f rowFields) readSku(c *charge) error {
	sku, err := f.text(columnSku)
	if err != nil {
		return err
	}
	parts := skuPattern.FindStringSubmatch(sku)
	if parts == nil {
		return invalidRow(f.line, "SkuId is not provider/model/class")
	}
	c.provider, c.model, c.class = parts[1], parts[2], focus.TokenClass(parts[3])
	meter, err := f.text(columnMeter)
	if err != nil {
		return err
	}
	if meter != string(c.class) || !classes[c.class] {
		return invalidRow(f.line, "SkuMeter is not a token class of this SkuId")
	}
	if !modelTag.MatchString(c.model) {
		return invalidRow(f.line, "the model is not valid")
	}
	priceID, err := f.text(columnPrice)
	if err != nil {
		return err
	}
	c.serviceTier, c.context = tierOf(priceID, c)
	return nil
}

// tierOf reads provider:model:region:tier:context:class. Anything else prices
// at the standard tier and the lowest context tier.
func tierOf(priceID string, c *charge) (string, int64) {
	parts := strings.Split(priceID, ":")
	if len(parts) != 6 || parts[0] != c.provider || parts[1] != c.model || parts[5] != string(c.class) {
		return catalog.TierStandard, 0
	}
	var context int64
	if over := overTier.FindStringSubmatch(parts[4]); over != nil {
		threshold, err := strconv.ParseInt(over[1], 10, 64)
		if err == nil {
			context = threshold + 1
		}
	}
	return parts[3], context
}

// readRouting takes what row version 0.2 states outright: the region, the
// service tier and the credential, and the billing account any row may state.
// Each is a usage fact, like the token count. A version 0.1 row states none of
// the first three, and reads global at the SkuPriceId's tier with no credential.
// The account is a claim about who pays. The tenant never comes from it.
func (f rowFields) readRouting(c *charge) error {
	region, err := f.optional(columnRegion, regionPattern)
	if err != nil {
		return err
	}
	tier, err := f.optional(columnTier, tierPattern)
	if err != nil {
		return err
	}
	credential, err := f.limited(columnAccount)
	if err != nil {
		return err
	}
	account, err := f.limited(columnBilling)
	if err != nil {
		return err
	}
	c.region, c.credential, c.account = region, credential, account
	if tier != "" {
		c.serviceTier = tier
	}
	return nil
}

func (f rowFields) limited(column string) (string, error) {
	text, err := f.text(column)
	if err != nil {
		return "", err
	}
	if utf8.RuneCountInString(text) > maxTextColumn {
		return "", invalidRow(f.line, column+" is past the contract's limit")
	}
	return text, nil
}

func (f rowFields) optional(column string, pattern *regexp.Regexp) (string, error) {
	text, err := f.text(column)
	if err != nil || text == "" {
		return "", err
	}
	if !pattern.MatchString(text) {
		return "", invalidRow(f.line, column+" is not valid")
	}
	return text, nil
}

func (f rowFields) readQuantity(c *charge) error {
	text, err := f.text(columnQuantity)
	if err != nil {
		return err
	}
	tokens, err := strconv.ParseInt(text, 10, 64)
	if err != nil || tokens < 0 || tokens > otlp.MaxTokens {
		return invalidRow(f.line, "ConsumedQuantity is not a whole token count")
	}
	c.tokens = tokens
	return nil
}

func (f rowFields) readPeriod(c *charge) error {
	start, err := f.instant(columnStart)
	if err != nil {
		return err
	}
	end, err := f.instant(columnEnd)
	if err != nil {
		return err
	}
	if !end.After(start) {
		return invalidRow(f.line, "the charge period does not end after it starts")
	}
	c.start, c.end = start, end
	return nil
}

func (f rowFields) instant(column string) (time.Time, error) {
	text, err := f.text(column)
	if err != nil {
		return time.Time{}, err
	}
	at, err := time.Parse(focus.InstantLayout, text)
	if err != nil {
		return time.Time{}, invalidRow(f.line, column+" is not a UTC instant")
	}
	return at, nil
}

func (f rowFields) readTags(c *charge, drops map[string]int64) error {
	raw, ok := f.columns[columnTags]
	if !ok {
		return invalidRow(f.line, "Tags is absent")
	}
	var tags map[string]json.RawMessage
	if err := json.Unmarshal(raw, &tags); err != nil {
		return invalidRow(f.line, "Tags is not an object")
	}
	read := map[string]string{}
	for key, value := range tags {
		if !strings.HasPrefix(key, "costroid/") {
			f.userTag(key, value, c, drops)
			continue
		}
		if !definedTags[key] {
			return invalidRow(f.line, "a costroid/ tag this version does not define")
		}
		var text string
		if err := json.Unmarshal(value, &text); err != nil {
			return invalidRow(f.line, "a costroid/ tag is not a string")
		}
		read[key] = text
	}
	return f.applyTags(c, read, drops)
}

// userTag drops a tag outside the convention: a free-form key and value are
// where content would hide. An address among them becomes a pseudonym.
func (f rowFields) userTag(key string, value json.RawMessage, c *charge, drops map[string]int64) {
	drops[otlp.DropLabel(key)]++
	if key != "user.email" {
		return
	}
	var email string
	if json.Unmarshal(value, &email) == nil {
		c.email = email
	}
}

func (f rowFields) applyTags(c *charge, tags map[string]string, drops map[string]int64) error {
	if tags[focus.TagModel] != c.model {
		return invalidRow(f.line, "costroid/model disagrees with SkuId")
	}
	c.agent = tags[focus.TagAgent]
	if !agentTag.MatchString(c.agent) || len(c.agent) > 64 {
		return invalidRow(f.line, "costroid/agent is not valid")
	}
	switch focus.CostBasis(tags[focus.TagCostBasis]) {
	case focus.Measured:
		c.basis = focus.Measured
	case focus.ListEquivalent:
		c.basis = focus.ListEquivalent
	default:
		return invalidRow(f.line, "costroid/cost_basis is not measured or list_equivalent")
	}
	c.source = sourceOf(tags[focus.TagCostSource])
	c.repo = strings.ToLower(tags["costroid/repo"])
	c.pr, c.sha, c.run = tags["costroid/pr"], tags["costroid/commit_sha"], tags["costroid/workflow_run_id"]
	c.enforcement = tags[focus.TagEnforcementGrade]
	c.weakJoin = tags[focus.TagAttributionGrade] == gradeWindowed
	c.session = optionalTag(tags[focus.TagSessionID], sessionTag, drops)
	c.agentVersion = optionalTag(tags[focus.TagAgentVersion], versionTag, drops)
	return nil
}

// sourceOf keeps an adapter's own name for itself. Two names belong to other
// paths, and a row on this one cannot claim them.
func sourceOf(stated string) string {
	if !sourcePattern.MatchString(stated) || stated == sourceTelemetry || stated == sourceProviderReport {
		return sourceStream
	}
	return stated
}

func optionalTag(value string, pattern *regexp.Regexp, drops map[string]int64) string {
	if value == "" {
		return ""
	}
	if !pattern.MatchString(value) {
		drops[dropInvalidTag]++
		return ""
	}
	return value
}

// dropInvalidTag counts an optional tag whose value misses its pattern.
const dropInvalidTag = "invalid_tag_value"

var definedTags = columnSet(
	"costroid/repo", "costroid/pr", "costroid/commit_sha", "costroid/workflow_run_id", focus.TagSessionID,
	focus.TagAgent, focus.TagAgentVersion, focus.TagModel, focus.TagCostSource, focus.TagCostBasis,
	focus.TagAttributionGrade, focus.TagEnforcementGrade, focus.TagRunIdentity,
)

func (f rowFields) readOvershoot(c *charge) error {
	text, err := f.text(columnOvershoot)
	if err != nil || text == "" {
		return err
	}
	bound, err := decimal.Parse(text)
	if err != nil || bound.Scale() > focus.MaxScale {
		return invalidRow(f.line, "x_CostroidOvershootBound is not a decimal")
	}
	c.overshoot = &bound
	return nil
}

type ingestAnswer struct {
	Accepted   int              `json:"accepted"`
	Duplicates int              `json:"duplicates"`
	Dropped    map[string]int64 `json:"dropped,omitempty"`
}

func (s *Server) handleRows(w http.ResponseWriter, r *http.Request) {
	who, err := s.authenticate(r)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	body, err := readBody(w, r)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	drops := map[string]int64{}
	charges, err := parseRows(body, drops)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	receipt, err := s.write(r, who, charges, drops)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, ingestAnswer{Accepted: receipt.Inserted, Duplicates: receipt.Duplicates, Dropped: drops})
}
