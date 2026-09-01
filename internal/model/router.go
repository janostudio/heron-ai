package model

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

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

	mu       sync.Mutex
	cooldown map[string]time.Time // modelName -> cooldown deadline
	now      func() time.Time     // injectable clock for tests
}

func NewProviderRouter(defaultModel string, profiles []types.ModelProfile) (*ProviderRouter, error) {
	return NewProviderRouterWithClock(defaultModel, profiles, time.Now)
}

func NewProviderRouterWithClock(defaultModel string, profiles []types.ModelProfile, now func() time.Time) (*ProviderRouter, error) {
	router := &ProviderRouter{
		defaultModel: strings.TrimSpace(defaultModel),
		profiles:     make(map[string]types.ModelProfile, len(profiles)),
		providers:    make(map[string]types.ModelProvider, len(profiles)),
		cooldown:     make(map[string]time.Time),
		now:          now,
	}
	if router.now == nil {
		router.now = time.Now
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
	return r.chatWithFallback(ctx, messages, tools, config)
}

func (r *ProviderRouter) ChatStream(ctx context.Context, messages []types.Message, tools []types.JSONSchema, config types.ModelConfig) (<-chan types.ChatChunk, error) {
	chain, err := r.resolveChain(config)
	if err != nil {
		return nil, err
	}

	var lastErr error
	for _, modelName := range chain {
		provider, effective, err := r.providerForModel(modelName, config)
		if err != nil {
			lastErr = err
			continue
		}
		ch, err := provider.ChatStream(ctx, messages, tools, effective)
		if err == nil {
			return ch, nil
		}
		lastErr = err
		var pe *types.ProviderError
		if errors.As(err, &pe) && pe.Retryable() {
			r.markCooling(modelName, r.cooldownSeconds(modelName))
			logFallback(ctx, modelName, pe, nextModel(chain, modelName))
			continue
		}
		return nil, err
	}
	if lastErr == nil {
		return nil, fmt.Errorf("no model available")
	}
	return nil, fmt.Errorf("all fallback models exhausted: %w", lastErr)
}

// chatWithFallback drives the fallback traversal for Chat. ChatStream keeps
// its own loop because its success type (a channel) differs; both follow the
// same decision rules.
func (r *ProviderRouter) chatWithFallback(ctx context.Context, messages []types.Message, tools []types.JSONSchema, config types.ModelConfig) (*types.ChatResponse, error) {
	chain, err := r.resolveChain(config)
	if err != nil {
		return nil, err
	}

	var lastErr error
	for _, modelName := range chain {
		provider, effective, err := r.providerForModel(modelName, config)
		if err != nil {
			lastErr = err
			continue
		}
		resp, err := provider.Chat(ctx, messages, tools, effective)
		if err == nil {
			return resp, nil
		}
		lastErr = err
		var pe *types.ProviderError
		if errors.As(err, &pe) && pe.Retryable() {
			r.markCooling(modelName, r.cooldownSeconds(modelName))
			logFallback(ctx, modelName, pe, nextModel(chain, modelName))
			continue
		}
		// Non-retryable ProviderError (auth/bad_request) or a non-ProviderError
		// (client-side build error) must not trigger fallback.
		return nil, err
	}
	if lastErr == nil {
		return nil, fmt.Errorf("no model available")
	}
	return nil, fmt.Errorf("all fallback models exhausted: %w", lastErr)
}

// resolveChain returns the ordered [primary, fallback...] model list. The
// primary is always included (explicit user intent outranks cooldown); passive
// fallbacks currently in cooldown are skipped. Fallback chains are flat: a
// fallback's own fallback is not recursively expanded.
func (r *ProviderRouter) resolveChain(config types.ModelConfig) ([]string, error) {
	primary := strings.TrimSpace(config.Model)
	if primary == "" {
		primary = r.defaultModel
	}
	chain := []string{primary}
	if profile, ok := r.profiles[primary]; ok {
		for _, fb := range profile.Fallback {
			fb = strings.TrimSpace(fb)
			if fb == "" {
				continue
			}
			if r.inCooldown(fb) {
				continue
			}
			chain = append(chain, fb)
		}
	}
	return chain, nil
}

// providerForModel resolves a provider for an explicit model name. Unlike
// providerFor (which falls back to the default model), this treats an unknown
// name as an error so the fallback loop can skip it.
func (r *ProviderRouter) providerForModel(modelName string, config types.ModelConfig) (types.ModelProvider, types.ModelConfig, error) {
	config.Model = modelName
	provider, ok := r.providers[modelName]
	if !ok {
		return nil, config, fmt.Errorf("model %q not found in models.json", modelName)
	}
	return provider, config, nil
}

// markCooling records modelName as cooling for seconds (default applied when
// the profile does not declare cooldown_seconds).
func (r *ProviderRouter) markCooling(modelName string, seconds int) {
	if seconds <= 0 {
		seconds = defaultCooldownSeconds
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.cooldown[modelName] = r.now().Add(time.Duration(seconds) * time.Second)
}

// inCooldown reports whether modelName is currently cooling. Expired entries
// are lazily removed.
func (r *ProviderRouter) inCooldown(modelName string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	until, ok := r.cooldown[modelName]
	if !ok {
		return false
	}
	if r.now().After(until) {
		delete(r.cooldown, modelName)
		return false
	}
	return true
}

// cooldownSeconds returns the configured cooldown for a model, or 0 to signal
// the default should be applied.
func (r *ProviderRouter) cooldownSeconds(modelName string) int {
	if profile, ok := r.profiles[modelName]; ok {
		return profile.CooldownSeconds
	}
	return 0
}

// SetMediaResolver wires the durable upload store into every provider owned by
// the router. Providers resolve media only while building their wire request.
func (r *ProviderRouter) SetMediaResolver(resolver types.MediaResolver) {
	for _, provider := range r.providers {
		if setter, ok := provider.(types.MediaResolverSetter); ok {
			setter.SetMediaResolver(resolver)
		}
	}
}

// MaxInputTokens exposes the selected profile's context capacity to the
// Agent ContextManager without expanding the ModelProvider interface.
func (r *ProviderRouter) MaxInputTokens(config types.ModelConfig) int {
	if config.MaxInputTokens > 0 {
		return config.MaxInputTokens
	}
	modelName := strings.TrimSpace(config.Model)
	if modelName == "" {
		modelName = r.defaultModel
	}
	if profile, ok := r.profiles[modelName]; ok {
		return profile.MaxInputTokens
	}
	return 0
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
