package model

import (
	"fmt"
	"strings"

	"github.com/heron-ai/heron-engine/pkg/types"
)

// MergeProfileDefaults applies the selected model's defaults first and then
// applies the Agent/request values on top. The Agent config is intentionally
// sparse: nil means "not configured", while a non-nil pointer is an explicit
// override (including zero).
func MergeProfileDefaults(profile types.ModelProfile, override types.ModelConfig) types.ModelConfig {
	result := override

	if result.Model == "" {
		result.Model = modelName(profile)
	}
	if result.Provider == "" {
		result.Provider = profile.Provider
	}
	if result.BaseURL == "" {
		result.BaseURL = profile.BaseURL
	}
	if result.APIKey == "" {
		result.APIKey = profile.APIKey
	}
	if result.MaxInputTokens <= 0 {
		result.MaxInputTokens = profile.MaxInputTokens
	}
	if result.Temperature == nil {
		result.Temperature = profile.Temperature
	}
	if result.TopP == nil {
		result.TopP = profile.TopP
	}
	if result.TopK == nil {
		result.TopK = profile.TopK
	}
	if result.RepetitionPenalty == nil {
		result.RepetitionPenalty = profile.RepetitionPenalty
	}
	if result.Reasoning == nil {
		result.Reasoning = profile.Reasoning
	}
	if result.OutputTokenLimit() == nil {
		value := profile.MaxOutputTokens
		if value <= 0 {
			value = profile.MaxTokens
		}
		if value > 0 {
			result.MaxOutputTokens = &value
		}
	}

	return result
}

func modelName(profile types.ModelProfile) string {
	if strings.TrimSpace(profile.Name) != "" {
		return profile.Name
	}
	return profile.ID
}

func profileProtocol(profile types.ModelProfile, override types.ModelConfig) string {
	value := strings.ToLower(strings.TrimSpace(profile.Protocol))
	if value == "" {
		value = strings.ToLower(strings.TrimSpace(profile.Provider))
	}
	if value == "" && strings.TrimSpace(override.Provider) != "" {
		value = strings.ToLower(strings.TrimSpace(override.Provider))
	}

	switch value {
	case "anthropic", "anthropic_messages", "messages":
		return "anthropic_messages"
	case "openai", "openai_chat", "openai-compatible", "openai_compatible", "chat":
		return "openai_chat"
	default:
		// Existing models.json files only have an OpenAI-compatible endpoint.
		// Keep that format as the safe backwards-compatible default.
		return "openai_chat"
	}
}

func validateProfile(profile types.ModelProfile) error {
	if strings.TrimSpace(modelName(profile)) == "" {
		return fmt.Errorf("model profile requires id or name")
	}
	if strings.TrimSpace(profile.BaseURL) == "" {
		return fmt.Errorf("model %q requires base_url", modelName(profile))
	}
	return nil
}
