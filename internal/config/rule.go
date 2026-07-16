package config

import (
	"strings"

	"github.com/adrg/frontmatter"

	"github.com/heron-ai/heron-engine/pkg/types"
)

func (l *ConfigLoader) loadRule(path string) (*types.RuleItem, error) {
	data, err := l.fileStore.Read(path)
	if err != nil {
		return nil, err
	}

	var rule types.RuleItem
	body, err := frontmatter.Parse(strings.NewReader(string(data)), &rule)
	if err != nil {
		return nil, err
	}
	rule.Content = string(body)

	return &rule, nil
}
