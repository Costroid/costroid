package policy

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"regexp"

	"go.yaml.in/yaml/v3"

	"github.com/Costroid/costroid/internal/decimal"
	"github.com/Costroid/costroid/internal/focus"
	"github.com/Costroid/costroid/internal/scm"
)

// ConfigPath is where a repository keeps its settings.
const ConfigPath = "costroid.yml"

// Settings are the policy keys of costroid.yml. A nil limit is a rule that
// does not run.
type Settings struct {
	RunBudget  *decimal.Decimal
	MaxRunCost *decimal.Decimal
	// Enforce ends the warn-only period early.
	Enforce bool
}

// amountPattern is a plain decimal of at most the row contract's scale, the
// same pattern the schema states.
var amountPattern = regexp.MustCompile(`^(0|[1-9][0-9]*)(\.[0-9]{1,12})?$`)

// commitPattern is a full commit SHA. An empty ref would read the default
// branch, which is neither the base nor the head.
var commitPattern = regexp.MustCompile(`^[0-9a-f]{40}([0-9a-f]{24})?$`)

var amountKeys = map[string]bool{"run_budget_usd": true, "max_run_cost_usd": true}

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
	var settings Settings
	for i := 0; i+1 < len(mapping.Content); i += 2 {
		key, value := mapping.Content[i].Value, mapping.Content[i+1]
		switch {
		case amountKeys[key]:
			amount, err := parseAmount(key, value)
			if err != nil {
				return Settings{}, err
			}
			if key == "run_budget_usd" {
				settings.RunBudget = &amount
			} else {
				settings.MaxRunCost = &amount
			}
		case key == "enforce":
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

// parseAmount takes the scalar's own text, so 0.1 stays 0.1. A number YAML
// would read as a float or an integer is refused: amounts are quoted strings.
func parseAmount(key string, value *yaml.Node) (decimal.Decimal, error) {
	if value.Kind != yaml.ScalarNode || value.Tag != "!!str" || !amountPattern.MatchString(value.Value) {
		return decimal.Decimal{}, fmt.Errorf("policy: policy.%s must be a quoted decimal of at most %d fractional digits, such as \"5.00\"", key, focus.MaxScale)
	}
	return decimal.Parse(value.Value)
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

// ReadConfig reads costroid.yml at ref. On a pull request ref is the base,
// never the head, so a pull request cannot change its own verdict. A failed
// read is kept in Unread.
func ReadConfig(ctx context.Context, files FileReader, ref string, fromBase bool) ConfigFile {
	if !commitPattern.MatchString(ref) {
		return ConfigFile{FromBase: fromBase, Unread: errors.New("policy: no commit to read costroid.yml at")}
	}
	data, found, err := readOptional(ctx, files, ref)
	if err != nil {
		return ConfigFile{Ref: ref, FromBase: fromBase, Unread: fmt.Errorf("policy: read %s at %s: %w", ConfigPath, ref, err)}
	}
	return ConfigFile{Data: data, Found: found, Ref: ref, FromBase: fromBase}
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
