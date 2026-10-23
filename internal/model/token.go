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

// EstimateTokens provides a rough token count estimate: 4 ASCII characters per
// token, one token per non-ASCII rune.
//
// The second half matters: CJK text is roughly one token per character, so the
// flat 4:1 ratio under-reported Chinese prompts by three to four times. This
// mirrors estimateContextText in internal/agent, which is what the compaction
// budget is built on.
func (c *TokenCounter) EstimateTokens(text string) int {
	if text == "" {
		return 0
	}
	ascii := 0
	other := 0
	for _, r := range text {
		if r < utf8.RuneSelf {
			ascii++
			continue
		}
		other++
	}
	return (ascii+3)/4 + other
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
