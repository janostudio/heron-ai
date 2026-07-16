package model

import "github.com/heron-ai/heron-engine/pkg/types"

type PresetProvider struct {
	Type    string
	BaseURL string
	Models  []PresetModel
}

type PresetModel struct {
	ID        string
	MaxTokens int
}

var PresetProviders = map[string]PresetProvider{
	"openai": {
		Type:    "openai",
		BaseURL: "https://api.openai.com/v1",
		Models: []PresetModel{
			{ID: "gpt-4o", MaxTokens: 128000},
			{ID: "gpt-4o-mini", MaxTokens: 128000},
			{ID: "gpt-4-turbo", MaxTokens: 128000},
		},
	},
	"deepseek": {
		Type:    "openai",
		BaseURL: "https://api.deepseek.com/v1",
		Models: []PresetModel{
			{ID: "deepseek-chat", MaxTokens: 64000},
			{ID: "deepseek-reasoner", MaxTokens: 64000},
		},
	},
	"anthropic": {
		Type:    "anthropic",
		BaseURL: "https://api.anthropic.com/v1",
		Models: []PresetModel{
			{ID: "claude-3-5-sonnet-20241022", MaxTokens: 200000},
			{ID: "claude-3-opus-20240229", MaxTokens: 200000},
		},
	},
}

func (p PresetProvider) ToConfig(apiKey string) types.ProviderConfig {
	models := make([]types.ModelConfig, len(p.Models))
	for i, m := range p.Models {
		models[i] = types.ModelConfig{
			Model:     m.ID,
			MaxTokens: m.MaxTokens,
		}
	}
	return types.ProviderConfig{
		Type:    p.Type,
		BaseURL: p.BaseURL,
		APIKey:  apiKey,
		Models:  models,
	}
}
