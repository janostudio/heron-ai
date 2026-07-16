package config

import (
	"gopkg.in/yaml.v3"

	"github.com/heron-ai/heron-engine/pkg/types"
)

func (l *ConfigLoader) loadFlow(path string) (types.FlowConfig, error) {
	data, err := l.fileStore.Read(path)
	if err != nil {
		return types.FlowConfig{}, err
	}

	var flow types.FlowConfig
	if err := yaml.Unmarshal(data, &flow); err != nil {
		return types.FlowConfig{}, err
	}

	return flow, nil
}
