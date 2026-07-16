package model

import (
	"unicode/utf8"

	"github.com/heron-ai/heron-engine/pkg/types"
)

// TokenCounter provides rough token estimation
type TokenCounter struct{}

func NewTokenCounter() *TokenCounter {
	return &TokenCounter{}
}

// EstimateTokens provides a rough token count estimate (4 chars ≈ 1 token for English)
func (c *TokenCounter) EstimateTokens(text string) int {
	charCount := utf8.RuneCountInString(text)
	// Rough approximation: 4 characters per token for English text
	return (charCount + 3) / 4
}

// EstimateMessages estimates total tokens for a message list
func (c *TokenCounter) EstimateMessages(messages []types.Message) int {
	total := 0
	for _, msg := range messages {
		total += c.EstimateTokens(msg.Content)
		total += c.EstimateTokens(msg.Role)
		// Add overhead per message
		total += 4
	}
	return total
}

// EstimateTools estimates tokens for tool definitions
func (c *TokenCounter) EstimateTools(tools []types.JSONSchema) int {
	total := 0
	for _, tool := range tools {
		for name, prop := range tool.Properties {
			total += c.EstimateTokens(name)
			total += c.EstimateTokens(prop.Description)
		}
		total += len(tool.Required) * 4
		total += 8 // overhead per tool
	}
	return total
}
