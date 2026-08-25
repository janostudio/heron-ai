package model

import (
	"context"
	"fmt"
	"strings"

	"github.com/heron-ai/heron-engine/pkg/types"
)

// ProviderRouter selects a provider from the model profile referenced by the
// current Agent. This keeps the Agent configuration provider-neutral while
// allowing one models.json to contain OpenAI-compatible and native Anthropic
// models at the same time.
type ProviderRouter struct {
	defaultModel string
	profiles     map[string]types.ModelProfile
	providers    map[string]types.ModelProvider
}

func NewProviderRouter(defaultModel string, profiles []types.ModelProfile) (*ProviderRouter, error) {
	router := &ProviderRouter{
		defaultModel: strings.TrimSpace(defaultModel),
		profiles:     make(map[string]types.ModelProfile, len(profiles)),
		providers:    make(map[string]types.ModelProvider, len(profiles)),
	}

	for _, profile := range profiles {
		if profile.Name == "" {
			profile.Name = profile.ID
		}
		if profile.ID == "" {
			profile.ID = profile.Name
		}
		if err := validateProfile(profile); err != nil {
			return nil, err
		}
		name := profile.Name
		if router.defaultModel == "" {
			router.defaultModel = name
		}
		router.profiles[name] = profile
		router.profiles[profile.ID] = profile

		provider, err := providerForProfile(profile)
		if err != nil {
			return nil, err
		}
		router.providers[name] = provider
		router.providers[profile.ID] = provider
	}

	if router.defaultModel == "" {
		return nil, fmt.Errorf("no model configured")
	}
	if _, ok := router.providers[router.defaultModel]; !ok {
		return nil, fmt.Errorf("default model %q not found", router.defaultModel)
	}
	return router, nil
}

func providerForProfile(profile types.ModelProfile) (types.ModelProvider, error) {
	switch profileProtocol(profile, types.ModelConfig{}) {
	case "anthropic_messages":
		return NewAnthropicProviderWithProfile(profile.APIKey, profile.BaseURL, profile.Name, profile), nil
	case "openai_chat":
		return NewOpenAIProviderWithProfile(profile.APIKey, profile.BaseURL, profile.Name, profile), nil
	default:
		return nil, fmt.Errorf("unsupported model protocol for %q", profile.Name)
	}
}

func (r *ProviderRouter) providerFor(config types.ModelConfig) (types.ModelProvider, types.ModelConfig, error) {
	modelName := strings.TrimSpace(config.Model)
	if modelName == "" {
		modelName = r.defaultModel
		config.Model = modelName
	}
	provider, ok := r.providers[modelName]
	if !ok {
		return nil, config, fmt.Errorf("model %q not found in models.json", modelName)
	}
	return provider, config, nil
}

func (r *ProviderRouter) Chat(ctx context.Context, messages []types.Message, tools []types.JSONSchema, config types.ModelConfig) (*types.ChatResponse, error) {
	provider, effective, err := r.providerFor(config)
	if err != nil {
		return nil, err
	}
	return provider.Chat(ctx, messages, tools, effective)
}

func (r *ProviderRouter) ChatStream(ctx context.Context, messages []types.Message, tools []types.JSONSchema, config types.ModelConfig) (<-chan types.ChatChunk, error) {
	provider, effective, err := r.providerFor(config)
	if err != nil {
		return nil, err
	}
	return provider.ChatStream(ctx, messages, tools, effective)
}

func (r *ProviderRouter) DefaultModel() string {
	return r.defaultModel
}

func (r *ProviderRouter) Profiles() map[string]types.ModelProfile {
	result := make(map[string]types.ModelProfile, len(r.profiles))
	for name, profile := range r.profiles {
		result[name] = profile
	}
	return result
}
