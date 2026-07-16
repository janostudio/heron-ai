package config

import (
	"gopkg.in/yaml.v3"

	"github.com/heron-ai/heron-engine/pkg/types"
)

func (l *ConfigLoader) loadTeam(path string) (*types.TeamConfig, error) {
	data, err := l.fileStore.Read(path)
	if err != nil {
		return nil, err
	}

	var team types.TeamConfig
	if err := yaml.Unmarshal(data, &team); err != nil {
		return nil, err
	}

	return &team, nil
}
