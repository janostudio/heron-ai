package model

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/heron-ai/heron-engine/pkg/types"
)

// --- Mock ModelProvider ---

type mockProvider struct {
	name    string
	chatFn  func(ctx context.Context, messages []types.Message, tools []types.JSONSchema, config types.ModelConfig) (*types.ChatResponse, error)
	streamFn func(ctx context.Context, messages []types.Message, tools []types.JSONSchema, config types.ModelConfig) (<-chan types.ChatChunk, error)
}

func (m *mockProvider) Chat(ctx context.Context, messages []types.Message, tools []types.JSONSchema, config types.ModelConfig) (*types.ChatResponse, error) {
	if m.chatFn != nil {
		return m.chatFn(ctx, messages, tools, config)
	}
	return &types.ChatResponse{Text: "mock response"}, nil
}

func (m *mockProvider) ChatStream(ctx context.Context, messages []types.Message, tools []types.JSONSchema, config types.ModelConfig) (<-chan types.ChatChunk, error) {
	if m.streamFn != nil {
		return m.streamFn(ctx, messages, tools, config)
	}
	ch := make(chan types.ChatChunk, 1)
	go func() {
		ch <- types.ChatChunk{Text: "mock", Finished: true}
		close(ch)
	}()
	return ch, nil
}

// --- ModelRegistry Tests ---

func TestModelRegistry_Register(t *testing.T) {
	reg := NewModelRegistry()
	err := reg.Register("test", &mockProvider{name: "test"})
	require.NoError(t, err)

	names := reg.List()
	assert.Len(t, names, 1)
	assert.Contains(t, names, "test")
}

func TestModelRegistry_Register_Duplicate(t *testing.T) {
	reg := NewModelRegistry()
	err := reg.Register("test", &mockProvider{name: "test"})
	require.NoError(t, err)

	err = reg.Register("test", &mockProvider{name: "test2"})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "already registered")
}

func TestModelRegistry_Get(t *testing.T) {
	reg := NewModelRegistry()
	p := &mockProvider{name: "test"}
	reg.Register("test", p)

	got, err := reg.Get("test")
	require.NoError(t, err)
	assert.Equal(t, p, got)
}

func TestModelRegistry_Get_NotFound(t *testing.T) {
	reg := NewModelRegistry()
	_, err := reg.Get("nonexistent")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "not found")
}

func TestModelRegistry_GetDefault(t *testing.T) {
	reg := NewModelRegistry()
	p := &mockProvider{name: "test"}
	reg.Register("test", p)

	got, err := reg.GetDefault()
	require.NoError(t, err)
	assert.Equal(t, p, got)
}

func TestModelRegistry_GetDefault_Empty(t *testing.T) {
	reg := NewModelRegistry()
	_, err := reg.GetDefault()
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "no default provider")
}

func TestModelRegistry_SetDefault(t *testing.T) {
	reg := NewModelRegistry()
	p1 := &mockProvider{name: "p1"}
	p2 := &mockProvider{name: "p2"}
	reg.Register("p1", p1)
	reg.Register("p2", p2)

	// First registered should be default
	got, err := reg.GetDefault()
	require.NoError(t, err)
	assert.Equal(t, p1, got)

	// Change default
	err = reg.SetDefault("p2")
	require.NoError(t, err)

	got, err = reg.GetDefault()
	require.NoError(t, err)
	assert.Equal(t, p2, got)
}

func TestModelRegistry_SetDefault_NotFound(t *testing.T) {
	reg := NewModelRegistry()
	err := reg.SetDefault("nonexistent")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "not found")
}

func TestModelRegistry_List(t *testing.T) {
	reg := NewModelRegistry()
	reg.Register("a", &mockProvider{name: "a"})
	reg.Register("b", &mockProvider{name: "b"})

	names := reg.List()
	assert.Len(t, names, 2)
	assert.Contains(t, names, "a")
	assert.Contains(t, names, "b")
}

func TestModelRegistry_Concurrent(t *testing.T) {
	reg := NewModelRegistry()
	var wg sync.WaitGroup

	// Concurrent registrations
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			name := "p" + string(rune('A'+id%26))
			_ = reg.Register(name, &mockProvider{})
		}(i)
	}
	wg.Wait()

	names := reg.List()
	assert.NotEmpty(t, names)
}

// --- RetryHandler Tests ---

func TestRetryHandler_Success(t *testing.T) {
	h := NewRetryHandler(3, 10*time.Millisecond)
	callCount := 0
	err := h.Retry(context.Background(), func() error {
		callCount++
		return nil
	})
	assert.NoError(t, err)
	assert.Equal(t, 1, callCount)
}

func TestRetryHandler_RetryThenSuccess(t *testing.T) {
	h := NewRetryHandler(3, 10*time.Millisecond)
	callCount := 0
	err := h.Retry(context.Background(), func() error {
		callCount++
		if callCount < 3 {
			return errors.New("temporary error")
		}
		return nil
	})
	assert.NoError(t, err)
	assert.Equal(t, 3, callCount)
}

func TestRetryHandler_Exhausted(t *testing.T) {
	h := NewRetryHandler(3, 10*time.Millisecond)
	callCount := 0
	err := h.Retry(context.Background(), func() error {
		callCount++
		return errors.New("persistent error")
	})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "retry exhausted")
	assert.Equal(t, 3, callCount)
}

func TestRetryHandler_ContextCancelled(t *testing.T) {
	h := NewRetryHandler(5, 100*time.Millisecond)
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // Cancel immediately

	err := h.Retry(ctx, func() error {
		return errors.New("some error")
	})
	assert.Error(t, err)
	assert.True(t, errors.Is(err, context.Canceled))
}

func TestRetryHandler_ContextCancelledDuringRetry(t *testing.T) {
	h := NewRetryHandler(5, 500*time.Millisecond)
	ctx, cancel := context.WithCancel(context.Background())

	callCount := 0
	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()

	err := h.Retry(ctx, func() error {
		callCount++
		return errors.New("some error")
	})
	assert.Error(t, err)
	assert.True(t, errors.Is(err, context.Canceled))
	assert.GreaterOrEqual(t, callCount, 1)
}

func TestRetryHandler_Defaults(t *testing.T) {
	h := NewRetryHandler(0, 0)
	assert.Equal(t, 3, h.maxRetries)
	assert.Equal(t, time.Second, h.backoff)
}

// --- TokenCounter Tests ---

func TestTokenCounter_EstimateTokens_Empty(t *testing.T) {
	c := NewTokenCounter()
	assert.Equal(t, 0, c.EstimateTokens(""))
}

func TestTokenCounter_EstimateTokens_English(t *testing.T) {
	c := NewTokenCounter()
	// "hello world" = 11 chars, (11+3)/4 = 3 tokens
	assert.Equal(t, 3, c.EstimateTokens("hello world"))
}

func TestTokenCounter_EstimateTokens_Chinese(t *testing.T) {
	c := NewTokenCounter()
	text := "你好世界"
	tokens := c.EstimateTokens(text)
	// 4 runes, (4+3)/4 = 1 token (rough)
	assert.Equal(t, 1, tokens)
}

func TestTokenCounter_EstimateMessages(t *testing.T) {
	c := NewTokenCounter()
	messages := []types.Message{
		{Role: "user", Content: "hello"},
		{Role: "assistant", Content: "hi there"},
	}
	tokens := c.EstimateMessages(messages)
	assert.Greater(t, tokens, 0)
}

func TestTokenCounter_EstimateTools(t *testing.T) {
	c := NewTokenCounter()
	tools := []types.JSONSchema{
		{
			Type: "object",
			Properties: map[string]types.JSONProperty{
				"city": {
					Type:        "string",
					Description: "The city name",
				},
			},
			Required: []string{"city"},
		},
	}
	tokens := c.EstimateTools(tools)
	assert.Greater(t, tokens, 0)
}

// --- PresetProviders Tests ---

func TestPresetProviders_Exists(t *testing.T) {
	assert.Contains(t, PresetProviders, "openai")
	assert.Contains(t, PresetProviders, "deepseek")
	assert.Contains(t, PresetProviders, "anthropic")
}

func TestPresetProviders_OpenAI(t *testing.T) {
	p := PresetProviders["openai"]
	assert.Equal(t, "openai", p.Type)
	assert.Equal(t, "https://api.openai.com/v1", p.BaseURL)
	assert.Len(t, p.Models, 3)

	modelIDs := make([]string, len(p.Models))
	for i, m := range p.Models {
		modelIDs[i] = m.ID
	}
	assert.Contains(t, modelIDs, "gpt-4o")
	assert.Contains(t, modelIDs, "gpt-4o-mini")
	assert.Contains(t, modelIDs, "gpt-4-turbo")
}

func TestPresetProviders_DeepSeek(t *testing.T) {
	p := PresetProviders["deepseek"]
	assert.Equal(t, "openai", p.Type)
	assert.Len(t, p.Models, 2)
}

func TestPresetProviders_Anthropic(t *testing.T) {
	p := PresetProviders["anthropic"]
	assert.Equal(t, "anthropic", p.Type)
	assert.Len(t, p.Models, 2)
}

func TestPresetProviders_ToConfig(t *testing.T) {
	p := PresetProviders["openai"]
	cfg := p.ToConfig("test-api-key")

	assert.Equal(t, "openai", cfg.Type)
	assert.Equal(t, "https://api.openai.com/v1", cfg.BaseURL)
	assert.Equal(t, "test-api-key", cfg.APIKey)
	assert.Len(t, cfg.Models, 3)

	// Verify model data
	modelMap := make(map[string]int)
	for _, m := range cfg.Models {
		modelMap[m.Model] = m.MaxTokens
	}
	assert.Equal(t, 128000, modelMap["gpt-4o"])
	assert.Equal(t, 128000, modelMap["gpt-4o-mini"])
	assert.Equal(t, 128000, modelMap["gpt-4-turbo"])
}
