package policy

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"go.yaml.in/yaml/v3"

	"github.com/Costroid/costroid/internal/decimal"
	"github.com/Costroid/costroid/internal/focus"
	"github.com/Costroid/costroid/internal/scm"
	"github.com/Costroid/costroid/internal/vcs"
)

// ConfigPath is where a repository keeps its settings.
const ConfigPath = "costroid.yml"

// Settings are the policy keys of costroid.yml. A key that is not set is a
// rule that does not run.
type Settings struct {
	// Amounts holds each key that is set and read as an amount is, by its
	// name in costroid.yml: the amounts, and the P95 limit's factor.
	Amounts map[string]decimal.Decimal
	// Models are the models a run may use: exact ids, or prefixes that end in
	// *. Empty when the key is not set.
	Models []string
	// Enforce ends the warn-only period early.
	Enforce bool
	// DemotionOff is policy.auto_demote: false. A ledger then demotes no
	// rule by its override rate, and every rule is re-armed.
	DemotionOff bool
	// Pack is the path of the customer pack policy_pack names, empty when it
	// names none.
	Pack string
}

// amountPattern is a plain decimal of at most the row contract's scale, the
// same pattern the schema states.
var amountPattern = regexp.MustCompile(`^(0|[1-9][0-9]*)(\.[0-9]{1,12})?$`)

// factorPattern is a plain decimal of at least 1 and below 10000, of at most
// the row contract's scale, the same pattern the schema states. Four digits
// before the point keep the P95 times the factor short enough to compare.
var factorPattern = regexp.MustCompile(`^[1-9][0-9]{0,3}(\.[0-9]{1,12})?$`)

// integerPattern is a plain non-negative decimal integer, the only form an
// integer setting takes.
var integerPattern = regexp.MustCompile(`^(0|[1-9][0-9]*)$`)

// modelEntryPattern is an entry of policy.models: a model id a comment may
// print, or such an id's prefix followed by *.
var modelEntryPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:/+-]*\*?$`)

// The keys under policy.
const (
	keyRunBudget          = "run_budget_usd"
	keyMaxRunCost         = "max_run_cost_usd"
	keyMaxIncrease        = "max_increase_usd"
	keyMaxIncreasePercent = "max_increase_percent"
	keyPullRequestBudget  = "pr_budget_usd"
	keyMonthlyBudget      = "monthly_budget_usd"
	keyMaxP95Factor       = "max_p95_factor"
	keyModels             = "models"
	keyEnforce            = "enforce"
	keyAutoDemote         = "auto_demote"
)

// amountKeys are the keys that hold an amount, each stated the same way in the
// schema.
var amountKeys = []string{
	keyRunBudget, keyMaxRunCost, keyMaxIncrease, keyMaxIncreasePercent,
	keyPullRequestBudget, keyMonthlyBudget,
}

// Keys are the keys under policy that ParseSettings reads, sorted.
func Keys() []string {
	keys := append([]string{keyModels, keyEnforce, keyAutoDemote, keyMaxP95Factor}, amountKeys...)
	slices.Sort(keys)
	return keys
}

// ParseSettings reads the policy mapping of costroid.yml, and policy_pack.
// Any key or value under policy that the schema refuses is an error, and so
// is a policy_pack the schema refuses, so a typo never runs a rule on a
// default. A policy key with no value sets no rule, as an absent one does.
// Other keys outside policy are not the policy's to judge.
func ParseSettings(data []byte) (Settings, error) {
	var root yaml.Node
	if err := yaml.Unmarshal(data, &root); err != nil {
		return Settings{}, fmt.Errorf("policy: parse costroid.yml: %w", err)
	}
	pack, err := readPackPath(&root)
	if err != nil {
		return Settings{}, err
	}
	mapping := policyMapping(&root)
	if mapping == nil || mapping.Tag == "!!null" {
		return Settings{Pack: pack}, nil
	}
	if mapping.Kind != yaml.MappingNode {
		return Settings{}, errors.New("policy: policy in costroid.yml is not a mapping")
	}
	settings := Settings{Amounts: map[string]decimal.Decimal{}, Pack: pack}
	for i := 0; i+1 < len(mapping.Content); i += 2 {
		key, value := resolved(mapping.Content[i]).Value, resolved(mapping.Content[i+1])
		switch {
		case slices.Contains(amountKeys, key):
			amount, err := readAmount(key, value)
			if err != nil {
				return Settings{}, err
			}
			settings.Amounts[key] = amount
		case key == keyMaxP95Factor:
			factor, err := readFactor(value)
			if err != nil {
				return Settings{}, err
			}
			settings.Amounts[key] = factor
		case key == keyModels:
			models, err := parseModels(value)
			if err != nil {
				return Settings{}, err
			}
			settings.Models = models
		case key == keyEnforce:
			on, read := readBool(value)
			if !read {
				return Settings{}, errors.New("policy: policy.enforce must be true or false")
			}
			settings.Enforce = on
		case key == keyAutoDemote:
			on, read := readBool(value)
			if !read {
				return Settings{}, errors.New("policy: policy.auto_demote must be true or false")
			}
			settings.DemotionOff = !on
		default:
			return Settings{}, errors.New("policy: costroid.yml names a policy key Costroid does not read")
		}
	}
	return settings, nil
}

func policyMapping(root *yaml.Node) *yaml.Node {
	return section(root, "policy")
}

// PackKey is the top-level key of costroid.yml that names a customer pack.
const PackKey = "policy_pack"

// maxPackPath is the longest path policy_pack takes, in bytes.
const maxPackPath = 256

// packPathPattern is a path from the repository's root: segments of letters,
// digits, ., _ and -, none of them . or .., the last ending .rego. It is the
// pattern result/v0.12 states for policy.pack.path and costroid.schema.json
// for policy_pack.
var packPathPattern = regexp.MustCompile(`^((\.?[A-Za-z0-9_-]|\.\.[A-Za-z0-9._-])[A-Za-z0-9._-]*/)*(\.?[A-Za-z0-9_-]|\.\.[A-Za-z0-9._-])[A-Za-z0-9._-]*\.rego$`)

var errPackPath = fmt.Errorf("policy: policy_pack in costroid.yml must be a path from the repository's root such as .costroid/policy.rego: at most %d characters of letters, digits, ., _, - and /, with no . or .. segment, ending .rego", maxPackPath)

// readPackPath reads policy_pack, empty when costroid.yml names no pack. A
// value with no text, as policy_pack: alone gives, is refused as the schema
// refuses it.
func readPackPath(root *yaml.Node) (string, error) {
	node := section(root, PackKey)
	if node == nil {
		return "", nil
	}
	if node.Kind != yaml.ScalarNode || node.Tag != "!!str" || len(node.Value) > maxPackPath || !packPathPattern.MatchString(node.Value) {
		return "", errPackPath
	}
	return node.Value, nil
}

// section is the value of a top-level key, nil when the document is not a
// mapping or lacks the key. An alias reads as the node it names, as validate
// reads it.
func section(root *yaml.Node, key string) *yaml.Node {
	if root.Kind != yaml.DocumentNode || len(root.Content) == 0 || root.Content[0].Kind != yaml.MappingNode {
		return nil
	}
	document := root.Content[0]
	for i := 0; i+1 < len(document.Content); i += 2 {
		if resolved(document.Content[i]).Value == key {
			return resolved(document.Content[i+1])
		}
	}
	return nil
}

// resolved is the node an alias names, and any other node itself. A key, a
// value and an entry of a list each read through it, so *name reads as the
// node &name marks wherever the file puts it, as validate reads it.
func resolved(node *yaml.Node) *yaml.Node {
	if node.Kind == yaml.AliasNode {
		return node.Alias
	}
	return node
}

// The keys under approval, and the bounds of approval.expires_days.
const (
	keyAllowAuthor = "allow_author"
	keyExpiresDays = "expires_days"
	// DefaultApprovalDays is how long an approval lasts when expires_days is
	// not set.
	DefaultApprovalDays = 7
	maxApprovalDays     = 90
)

// ApprovalSettings are the approval keys of costroid.yml.
type ApprovalSettings struct {
	// AllowAuthor lets a pull request's author approve it.
	AllowAuthor bool
	// ExpiresDays is how many days a review approves for.
	ExpiresDays int
}

// ApprovalKeys are the keys under approval that ParseApproval reads, sorted.
func ApprovalKeys() []string {
	return []string{keyAllowAuthor, keyExpiresDays}
}

// ParseApproval reads the approval mapping of costroid.yml. Without it, the
// author cannot approve and an approval lasts DefaultApprovalDays. Any key or
// value under it that the schema refuses is an error, so a typo never widens
// who can approve.
func ParseApproval(data []byte) (ApprovalSettings, error) {
	settings := ApprovalSettings{ExpiresDays: DefaultApprovalDays}
	var root yaml.Node
	if err := yaml.Unmarshal(data, &root); err != nil {
		return ApprovalSettings{}, fmt.Errorf("policy: parse costroid.yml: %w", err)
	}
	mapping := section(&root, "approval")
	if mapping == nil || mapping.Tag == "!!null" {
		return settings, nil
	}
	if mapping.Kind != yaml.MappingNode {
		return ApprovalSettings{}, errors.New("policy: approval in costroid.yml is not a mapping")
	}
	for i := 0; i+1 < len(mapping.Content); i += 2 {
		key, value := resolved(mapping.Content[i]).Value, resolved(mapping.Content[i+1])
		switch key {
		case keyAllowAuthor:
			on, read := readBool(value)
			if !read {
				return ApprovalSettings{}, errors.New("policy: approval.allow_author must be true or false")
			}
			settings.AllowAuthor = on
		case keyExpiresDays:
			days, err := readDays(value)
			if err != nil {
				return ApprovalSettings{}, err
			}
			settings.ExpiresDays = days
		default:
			return ApprovalSettings{}, errors.New("policy: costroid.yml names an approval key Costroid does not read")
		}
	}
	return settings, nil
}

// readBool reads a switch as the boolean YAML makes of it: true, True and
// TRUE are true, and false, False and FALSE false. A scalar of another tag,
// such as "true" quoted or a YAML 1.1 word, is not read, as the schema
// refuses it.
func readBool(value *yaml.Node) (on, read bool) {
	if value.Kind != yaml.ScalarNode || value.Tag != "!!bool" || value.Decode(&on) != nil {
		return false, false
	}
	return on, true
}

// readDays reads approval.expires_days from its source text. Only plain
// digits are read: YAML also reads 0x10, 0o17, 017, 1_000 and +7 as whole
// numbers, and none of them is written as the number it stands for: 017 is 15.
func readDays(value *yaml.Node) (int, error) {
	refused := fmt.Errorf("policy: approval.expires_days must be a whole number of days from 1 to %d, written in plain digits such as 7", maxApprovalDays)
	if value.Kind != yaml.ScalarNode || value.Tag != "!!int" || !integerPattern.MatchString(value.Value) {
		return 0, refused
	}
	days, err := strconv.Atoi(value.Value)
	if err != nil || days < 1 || days > maxApprovalDays {
		return 0, refused
	}
	return days, nil
}

// amountTags are the YAML tags a plain decimal resolves to: 5 is an integer,
// 2.50 a float and "2.50" a string. Only the node's text is read.
var amountTags = []string{"!!int", "!!float", "!!str"}

// readAmount reads an amount from its source text, quoted or not, so 2.50
// stays 2.50 and never passes through a float. Only a plain decimal is read.
// YAML also reads 0x10, 0o17, 017 and 1_000 as whole numbers and 1e3 as a
// float, and none of them is written as the figure it stands for: 017 is 15.
func readAmount(key string, value *yaml.Node) (decimal.Decimal, error) {
	refused := fmt.Errorf("policy: policy.%s must be a plain decimal of at most %d fractional digits, such as 5, 2.50 or \"2.50\"", key, focus.MaxScale)
	return readDecimal(value, amountPattern, refused)
}

// readFactor reads policy.max_p95_factor from its source text as an amount is
// read, and holds it to at least 1, since a limit below the P95 would fail a
// run that costs what most earlier runs cost, and below 10000.
func readFactor(value *yaml.Node) (decimal.Decimal, error) {
	refused := fmt.Errorf("policy: policy.%s must be a plain decimal of at least 1 and below 10000 with at most %d fractional digits, such as 2, 1.5 or \"1.5\"", keyMaxP95Factor, focus.MaxScale)
	return readDecimal(value, factorPattern, refused)
}

// readDecimal reads a scalar's text when the pattern takes it, and is the
// refusal given otherwise.
func readDecimal(value *yaml.Node, pattern *regexp.Regexp, refused error) (decimal.Decimal, error) {
	if value.Kind != yaml.ScalarNode || !slices.Contains(amountTags, value.Tag) || !pattern.MatchString(value.Value) {
		return decimal.Decimal{}, refused
	}
	number, err := decimal.Parse(value.Value)
	if err != nil {
		return decimal.Decimal{}, refused
	}
	return number, nil
}

// parseModels reads policy.models: a list of one or more distinct entries.
func parseModels(value *yaml.Node) ([]string, error) {
	refused := fmt.Errorf("policy: policy.models must list distinct model ids, each of at most %d characters, or prefixes that end in *", maxModelLength)
	if value.Kind != yaml.SequenceNode || len(value.Content) == 0 {
		return nil, refused
	}
	models := make([]string, 0, len(value.Content))
	for _, entry := range value.Content {
		entry = resolved(entry)
		if entry.Kind != yaml.ScalarNode || entry.Tag != "!!str" || len(entry.Value) > maxModelLength ||
			!modelEntryPattern.MatchString(entry.Value) || slices.Contains(models, entry.Value) {
			return nil, refused
		}
		models = append(models, entry.Value)
	}
	return models, nil
}

// allows says whether the list admits a model: an entry equal to it, or an
// entry ending in * whose prefix it starts with.
func (s Settings) allows(model string) bool {
	for _, entry := range s.Models {
		prefix, wildcard := strings.CutSuffix(entry, "*")
		if model == entry || (wildcard && strings.HasPrefix(model, prefix)) {
			return true
		}
	}
	return false
}

// ConfigState says what became of costroid.yml.
type ConfigState string

// The states of the settings file.
const (
	ConfigFound   ConfigState = "found"
	ConfigAbsent  ConfigState = "absent"
	ConfigInvalid ConfigState = "invalid"
	ConfigUnread  ConfigState = "unread"
)

// ConfigFile is costroid.yml as one commit holds it.
type ConfigFile struct {
	Data  []byte
	Found bool
	// Ref is the commit the file was read at, empty offline.
	Ref string
	// FromBase says Ref is a pull request's base, not its head.
	FromBase bool
	// Unread is why the file could not be read, nil when it was.
	Unread error

	// commit is the commit ReadConfig read the file at, and pullRequest says
	// it read it for a pull request. Nothing else sets them, so a pack is
	// read only at a commit ReadConfig chose.
	commit      string
	pullRequest bool
}

func (c ConfigFile) settings() (Settings, error) {
	if c.Unread != nil {
		return Settings{}, c.Unread
	}
	return ParseSettings(c.Data)
}

func (c ConfigFile) state(problem error) ConfigState {
	switch {
	case c.Unread != nil:
		return ConfigUnread
	case problem != nil:
		return ConfigInvalid
	case c.Found:
		return ConfigFound
	}
	return ConfigAbsent
}

// FileReader reads a file at a commit. A missing file is scm.ErrNotFound.
type FileReader interface {
	ReadFile(ctx context.Context, path, ref string) ([]byte, error)
}

// Commits names what one report is about: a pull request's base and head, or
// a push's head alone.
type Commits struct {
	Base, Head  string
	PullRequest bool
}

// ReadConfig reads costroid.yml at a pull request's base, never its head, so a
// pull request cannot change its own verdict. A push has only its head. A
// failed read is kept in Unread.
func ReadConfig(ctx context.Context, files FileReader, commits Commits) ConfigFile {
	ref := commits.Head
	if commits.PullRequest {
		ref = commits.Base
	}
	// An empty ref would read the default branch, which is neither the base
	// nor the head.
	if !vcs.IsCommit(ref) {
		return ConfigFile{FromBase: commits.PullRequest, Unread: errors.New("policy: no commit to read costroid.yml at")}
	}
	data, found, err := readOptional(ctx, files, ref)
	if err != nil {
		return ConfigFile{Ref: ref, FromBase: commits.PullRequest, Unread: fmt.Errorf("policy: read %s at %s: %w", ConfigPath, ref, err)}
	}
	return ConfigFile{Data: data, Found: found, Ref: ref, FromBase: commits.PullRequest, commit: ref, pullRequest: commits.PullRequest}
}

func readOptional(ctx context.Context, files FileReader, ref string) ([]byte, bool, error) {
	data, err := files.ReadFile(ctx, ConfigPath, ref)
	if errors.Is(err, scm.ErrNotFound) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	return data, true, nil
}

// ConfigFromFile reads costroid.yml from a path, as costroid policy does. A
// missing file is the defaults.
func ConfigFromFile(path string) ConfigFile {
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return ConfigFile{}
	}
	if err != nil {
		return ConfigFile{Unread: fmt.Errorf("policy: %w", err)}
	}
	return ConfigFile{Data: data, Found: true}
}

// PackPath is the path of the customer pack the file names, empty when it
// names none or cannot be read.
func (c ConfigFile) PackPath() string {
	settings, err := c.settings()
	if err != nil {
		return ""
	}
	return settings.Pack
}

// Pack is the customer pack a costroid.yml names, as one commit holds it, or
// as a file offline. Only ReadPack and PackFromFile make one that holds
// bytes, and each records the costroid.yml it was read for, so a pack read
// for one file never runs under another. The zero Pack is a pack not read.
type Pack struct {
	path string
	// ref is the commit the pack was read at, empty offline, and config the
	// SHA-256 of the costroid.yml that named it.
	ref, config string
	data        []byte
	// unread is why the pack cannot run, nil when it can, and absent says
	// the commit or the directory holds no such file.
	unread error
	absent bool
}

// ReadPack reads the customer pack config names at the commit ReadConfig read
// config at, and at no other: no parameter takes a commit. A configuration
// ReadConfig did not read is refused, and so, on a pull request, is one that
// is not the base's. A missing file, a file over 64 KiB and a host that did
// not answer each leave the pack unread.
func ReadPack(ctx context.Context, files FileReader, config ConfigFile) Pack {
	path := config.PackPath()
	if path == "" {
		return Pack{}
	}
	pack := Pack{path: path, ref: config.Ref, config: digest(config.Data)}
	switch {
	case config.commit == "":
		pack.unread = errors.New("policy: costroid.yml was not read at a commit, so the pack is not read at one either")
	case config.Ref != config.commit:
		pack.unread = fmt.Errorf("policy: costroid.yml was read at %s and names %s as its commit", config.commit, config.Ref)
	case config.pullRequest && !config.FromBase:
		pack.unread = errors.New("policy: on a pull request a pack is read at the base alone")
	default:
		pack.data, pack.unread = readPackFile(ctx, files, path, config.commit)
		pack.absent = errors.Is(pack.unread, scm.ErrNotFound)
	}
	return pack
}

func readPackFile(ctx context.Context, files FileReader, path, commit string) ([]byte, error) {
	data, err := files.ReadFile(ctx, path, commit)
	switch {
	case errors.Is(err, scm.ErrNotFound):
		return nil, fmt.Errorf("policy: %s at %s: %w", path, commit, err)
	case err != nil:
		return nil, fmt.Errorf("policy: read %s at %s: %w", path, commit, err)
	case len(data) > maxPackBytes:
		return data, fmt.Errorf("policy: %s at %s holds %d bytes, over %d", path, commit, len(data), maxPackBytes)
	}
	return data, nil
}

// PackFromFile reads the customer pack config names from a file, as costroid
// policy does offline: file holds the pack as the base branch holds it at the
// path config names. A missing file and a file over 64 KiB leave it unread,
// as they do online.
func PackFromFile(file string, config ConfigFile) Pack {
	path := config.PackPath()
	if path == "" {
		return Pack{}
	}
	pack := Pack{path: path, ref: config.Ref, config: digest(config.Data)}
	data, err := os.ReadFile(file)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		pack.unread, pack.absent = fmt.Errorf("policy: %w", err), true
	case err != nil:
		pack.unread = fmt.Errorf("policy: %w", err)
	case len(data) > maxPackBytes:
		pack.data, pack.unread = data, fmt.Errorf("policy: %s holds %d bytes, over %d", file, len(data), maxPackBytes)
	default:
		pack.data = data
	}
	return pack
}

// Read reports a pack whose bytes were read whole, which runs when a verdict
// reads the costroid.yml it was read for.
func (p Pack) Read() bool {
	return p.path != "" && p.unread == nil
}

// readFor reports a pack read for config: the pack config names, at the
// commit config names, for config's bytes.
func (p Pack) readFor(config ConfigFile) bool {
	return p.path != "" && p.path == config.PackPath() && p.ref == config.Ref && p.config == digest(config.Data)
}

// fault is why the pack cannot run under config, and nil when it can: a pack
// not read, not read whole, or read for another costroid.yml.
func (p Pack) fault(config ConfigFile) *PackFault {
	switch {
	case p.path == "":
		return fault(ReasonPackUnread, 0, errors.New("the pack was not read"))
	case !p.readFor(config):
		return fault(ReasonPackUnread, 0, errors.New("the pack was read for another costroid.yml"))
	case p.unread != nil:
		return fault(ReasonPackUnread, 0, p.unread)
	}
	return nil
}

// bytesFor is the bytes a verdict under config states the digest of: the
// pack's, when it was read for config, and none otherwise.
func (p Pack) bytesFor(config ConfigFile) []byte {
	if !p.readFor(config) {
		return nil
	}
	return p.data
}

// PackChange says what a pull request changes of its customer pack. The
// change applies after the merge, because the verdict reads the base.
type PackChange int

// What a pull request changes of its pack.
const (
	// PackUnchanged is no change, or one that could not be read.
	PackUnchanged PackChange = iota
	// PackKeyChanged is a policy_pack at the head that names another path,
	// or none.
	PackKeyChanged
	// PackFileChanged is a pack file at the head whose bytes differ from the
	// base's.
	PackFileChanged
)

// ChangedAtHead compares the pack config names with the pull request's head,
// and reports only what changed. It reads costroid.yml and the pack's path at
// head when config names a pack, and nothing leaves this function but the
// answer, so the head's bytes never run. A read the host did not answer is no
// change, since none is known. Off a pull request there is no head to
// compare.
func (p Pack) ChangedAtHead(ctx context.Context, files FileReader, config ConfigFile, head string) PackChange {
	if !p.readFor(config) || !config.pullRequest || !vcs.IsCommit(head) || head == config.commit {
		return PackUnchanged
	}
	headConfig, found, err := readOptional(ctx, files, head)
	if err != nil {
		return PackUnchanged
	}
	var root yaml.Node
	if !found || yaml.Unmarshal(headConfig, &root) != nil {
		return PackKeyChanged
	}
	if path, err := readPackPath(&root); err != nil || path != p.path {
		return PackKeyChanged
	}
	data, err := files.ReadFile(ctx, p.path, head)
	absent := errors.Is(err, scm.ErrNotFound)
	switch {
	case err != nil && !absent, p.data == nil && p.unread != nil && !p.absent:
		// One side could not be read, so whether it changed is not known.
		return PackUnchanged
	case absent != p.absent, !bytes.Equal(data, p.data):
		return PackFileChanged
	}
	return PackUnchanged
}
