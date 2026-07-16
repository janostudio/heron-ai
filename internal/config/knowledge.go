package config

import (
	"strings"

	"github.com/adrg/frontmatter"

	"github.com/heron-ai/heron-engine/pkg/types"
)

func (l *ConfigLoader) loadKnowledge(path string) (*types.KnowledgeEntry, error) {
	data, err := l.fileStore.Read(path)
	if err != nil {
		return nil, err
	}

	var entry types.KnowledgeEntry
	body, err := frontmatter.Parse(strings.NewReader(string(data)), &entry)
	if err != nil {
		return nil, err
	}
	entry.Content = string(body)

	return &entry, nil
}
