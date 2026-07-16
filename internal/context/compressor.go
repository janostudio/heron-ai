package context

import (
	"context"
	"fmt"
	"strings"

	"github.com/heron-ai/heron-engine/pkg/types"
)

// ContextCompressor manages context window limits
type ContextCompressor struct {
	maxTokens    int
	tokenCounter interface {
		EstimateTokens(text string) int
		EstimateMessages(messages []types.Message) int
	}
}

func NewContextCompressor(maxTokens int) *ContextCompressor {
	return &ContextCompressor{maxTokens: maxTokens}
}

// CompressMessages trims messages to fit within maxTokens
// Keeps system messages and most recent messages
func (c *ContextCompressor) CompressMessages(ctx context.Context, messages []types.Message) ([]types.Message, error) {
	if c.maxTokens <= 0 {
		return messages, nil
	}

	// Separate system messages (always keep)
	var system []types.Message
	var others []types.Message
	for _, msg := range messages {
		if msg.Role == "system" {
			system = append(system, msg)
		} else {
			others = append(others, msg)
		}
	}

	// Estimate system tokens
	systemTokens := estimateTokens(system)
	remaining := c.maxTokens - systemTokens

	if remaining <= 0 {
		return system, nil
	}

	// Keep most recent messages that fit
	result := make([]types.Message, len(system))
	copy(result, system)

	usedTokens := systemTokens
	for i := len(others) - 1; i >= 0; i-- {
		msgTokens := estimateTokenSingle(others[i])
		if usedTokens+msgTokens > c.maxTokens {
			break
		}
		result = append(result, others[i])
		usedTokens += msgTokens
	}

	// Reverse to maintain chronological order
	nonSystem := result[len(system):]
	for i, j := 0, len(nonSystem)-1; i < j; i, j = i+1, j-1 {
		nonSystem[i], nonSystem[j] = nonSystem[j], nonSystem[i]
	}

	return result, nil
}

// CompressContent truncates content to fit maxTokens
func (c *ContextCompressor) CompressContent(ctx context.Context, content string) (string, error) {
	if c.maxTokens <= 0 {
		return content, nil
	}

	tokens := estimateTokenCount(content)
	if tokens <= c.maxTokens {
		return content, nil
	}

	// Truncate by character count (rough approximation)
	chars := len(content)
	ratio := float64(c.maxTokens) / float64(tokens)
	truncateAt := int(float64(chars) * ratio)

	if truncateAt >= chars {
		return content, nil
	}

	// Try to truncate at a word boundary
	truncated := content[:truncateAt]
	if idx := strings.LastIndex(truncated, " "); idx > 0 {
		truncated = truncated[:idx]
	}

	return truncated + "\n... [truncated]", nil
}

// CompressToolResult truncates tool results
func (c *ContextCompressor) CompressToolResult(ctx context.Context, toolName string, result string) (string, error) {
	maxResultTokens := c.maxTokens / 4 // reserve 25% for tool results
	if maxResultTokens <= 0 {
		maxResultTokens = 1000
	}

	tokens := estimateTokenCount(result)
	if tokens <= maxResultTokens {
		return result, nil
	}

	return fmt.Sprintf("%s\n... [result truncated, %d tokens]",
		result[:min(len(result), maxResultTokens*4)], tokens), nil
}

func estimateTokens(messages []types.Message) int {
	total := 0
	for _, msg := range messages {
		total += estimateTokenSingle(msg)
	}
	return total
}

func estimateTokenSingle(msg types.Message) int {
	// Rough approximation: 4 chars ≈ 1 token
	return (len(msg.Content) + len(msg.Role) + 7) / 4
}

func estimateTokenCount(text string) int {
	return (len(text) + 3) / 4
}
