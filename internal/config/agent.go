package config

import (
	"strings"

	"github.com/adrg/frontmatter"

	"github.com/heron-ai/heron-engine/pkg/types"
)

func (l *ConfigLoader) loadAgent(path string) (*types.AgentConfig, error) {
	data, err := l.fileStore.Read(path)
	if err != nil {
		return nil, err
	}

	var agent types.AgentConfig
	body, err := frontmatter.Parse(strings.NewReader(string(data)), &agent)
	if err != nil {
		return nil, err
	}
	agent.Body = string(body)

	return &agent, nil
}
