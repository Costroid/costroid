package policy

import (
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
)

// ConfigPath is where a repository keeps its settings.
const ConfigPath = "costroid.yml"

// Settings are the policy keys of costroid.yml. A key that is not set is a
// rule that does not run.
type Settings struct {
	// Amounts holds each amount key that is set, by its name in costroid.yml.
	Amounts map[string]decimal.Decimal
	// Models are the models a run may use: exact ids, or prefixes that end in
	// *. Empty when the key is not set.
	Models []string
	// Enforce ends the warn-only period early.
	Enforce bool
}

// amountPattern is a plain decimal of at most the row contract's scale, the
// same pattern the schema states.
var amountPattern = regexp.MustCompile(`^(0|[1-9][0-9]*)(\.[0-9]{1,12})?$`)

// commitPattern is a full commit SHA. An empty ref would read the default
// branch, which is neither the base nor the head.
var commitPattern = regexp.MustCompile(`^[0-9a-f]{40}([0-9a-f]{24})?$`)

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
	keyModels             = "models"
	keyEnforce            = "enforce"
)

// amountKeys are the keys that hold an amount, each stated the same way in the
// schema.
var amountKeys = []string{
	keyRunBudget, keyMaxRunCost, keyMaxIncrease, keyMaxIncreasePercent,
	keyPullRequestBudget, keyMonthlyBudget,
}

// Keys are the keys under policy that ParseSettings reads, sorted.
func Keys() []string {
	keys := append([]string{keyModels, keyEnforce}, amountKeys...)
	slices.Sort(keys)
	return keys
}

// ParseSettings reads the policy mapping of costroid.yml. Any key or value
// under it that the schema refuses is an error, so a typo never runs a rule
// on a default. Keys outside it are not the policy's to judge.
func ParseSettings(data []byte) (Settings, error) {
	var root yaml.Node
	if err := yaml.Unmarshal(data, &root); err != nil {
		return Settings{}, fmt.Errorf("policy: parse costroid.yml: %w", err)
	}
	mapping := policyMapping(&root)
	if mapping == nil {
		return Settings{}, nil
	}
	if mapping.Kind != yaml.MappingNode {
		return Settings{}, errors.New("policy: policy in costroid.yml is not a mapping")
	}
	settings := Settings{Amounts: map[string]decimal.Decimal{}}
	for i := 0; i+1 < len(mapping.Content); i += 2 {
		key, value := mapping.Content[i].Value, mapping.Content[i+1]
		switch {
		case slices.Contains(amountKeys, key):
			amount, err := parseAmount(key, value)
			if err != nil {
				return Settings{}, err
			}
			settings.Amounts[key] = amount
		case key == keyModels:
			models, err := parseModels(value)
			if err != nil {
				return Settings{}, err
			}
			settings.Models = models
		case key == keyEnforce:
			if value.Kind != yaml.ScalarNode || value.Tag != "!!bool" {
				return Settings{}, errors.New("policy: policy.enforce must be true or false")
			}
			settings.Enforce = value.Value == "true"
		default:
			return Settings{}, errors.New("policy: costroid.yml names a policy key Costroid does not read")
		}
	}
	return settings, nil
}

func policyMapping(root *yaml.Node) *yaml.Node {
	if root.Kind != yaml.DocumentNode || len(root.Content) == 0 || root.Content[0].Kind != yaml.MappingNode {
		return nil
	}
	document := root.Content[0]
	for i := 0; i+1 < len(document.Content); i += 2 {
		if document.Content[i].Value == "policy" {
			return document.Content[i+1]
		}
	}
	return nil
}

// parseAmount takes a quoted scalar's own text, so 0.1 stays 0.1. A YAML
// integer is exact, so it is taken too. A number YAML reads as a float is
// refused, because its text and its value can differ.
func parseAmount(key string, value *yaml.Node) (decimal.Decimal, error) {
	if value.Kind == yaml.ScalarNode && value.Tag == "!!int" {
		var whole uint64
		if value.Decode(&whole) == nil {
			return decimal.Parse(strconv.FormatUint(whole, 10))
		}
	}
	if value.Kind != yaml.ScalarNode || value.Tag != "!!str" || !amountPattern.MatchString(value.Value) {
		return decimal.Decimal{}, fmt.Errorf("policy: policy.%s must be a whole number or a quoted decimal of at most %d fractional digits, such as 5 or \"5.00\"", key, focus.MaxScale)
	}
	return decimal.Parse(value.Value)
}

// parseModels reads policy.models: a list of one or more distinct entries.
func parseModels(value *yaml.Node) ([]string, error) {
	refused := fmt.Errorf("policy: policy.models must list distinct model ids, each of at most %d characters, or prefixes that end in *", maxModelLength)
	if value.Kind != yaml.SequenceNode || len(value.Content) == 0 {
		return nil, refused
	}
	models := make([]string, 0, len(value.Content))
	for _, entry := range value.Content {
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
	if !commitPattern.MatchString(ref) {
		return ConfigFile{FromBase: commits.PullRequest, Unread: errors.New("policy: no commit to read costroid.yml at")}
	}
	data, found, err := readOptional(ctx, files, ref)
	if err != nil {
		return ConfigFile{Ref: ref, FromBase: commits.PullRequest, Unread: fmt.Errorf("policy: read %s at %s: %w", ConfigPath, ref, err)}
	}
	return ConfigFile{Data: data, Found: found, Ref: ref, FromBase: commits.PullRequest}
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
