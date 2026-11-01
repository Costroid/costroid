package comment

import (
	"fmt"

	"go.yaml.in/yaml/v3"
)

// Config is what the comment reads from costroid.yml.
type Config struct {
	ShowCost bool
}

// ParseConfig reads costroid.yml. An empty document or an absent key leaves
// the default, and keys the comment does not read are ignored.
func ParseConfig(data []byte) (Config, error) {
	var file struct {
		Comment struct {
			ShowCost *bool `yaml:"show_cost"`
		} `yaml:"comment"`
	}
	if err := yaml.Unmarshal(data, &file); err != nil {
		return Config{}, fmt.Errorf("comment: parse costroid.yml: %w", err)
	}
	if file.Comment.ShowCost == nil {
		return Config{ShowCost: true}, nil
	}
	return Config{ShowCost: *file.Comment.ShowCost}, nil
}
