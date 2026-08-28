package agent

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"unicode/utf8"

	"github.com/heron-ai/heron-engine/pkg/types"
)

// buildModelRequestStats creates a privacy-preserving summary of the exact
// message/tool payload that is about to be sent to a provider. Hashes allow
// cache-prefix analysis without persisting prompt contents.
func buildModelRequestStats(
	round int,
	messages []types.Message,
	tools []types.JSONSchema,
	estimatedTokens int,
	compacted bool,
) types.ModelRequestStats {
	stats := types.ModelRequestStats{
		Round:                 round,
		MessageCount:          len(messages),
		ToolSchemaCount:       len(tools),
		EstimatedPromptTokens: estimatedTokens,
		PromptHash: hashJSON(struct {
			Messages []types.Message    `json:"messages"`
			Tools    []types.JSONSchema `json:"tools"`
		}{Messages: messages, Tools: tools}),
		StablePrefixHash: hashJSON(struct {
			Messages []types.Message    `json:"messages"`
			Tools    []types.JSONSchema `json:"tools"`
		}{
			Messages: stablePrefix(messages),
			Tools:    tools,
		}),
		ToolSchemaHash: hashJSON(tools),
		Compacted:      compacted,
	}

	for _, message := range messages {
		charCount := utf8.RuneCountInString(message.Content)
		stats.MediaPartCount += len(message.Parts)
		switch message.Role {
		case "system":
			stats.SystemChars += charCount
		case "user":
			stats.UserChars += charCount
		case "assistant":
			stats.AssistantChars += charCount
		case "tool":
			stats.ToolMessageChars += charCount
		}
	}
	return stats
}

func stablePrefix(messages []types.Message) []types.Message {
	var prefix []types.Message
	for _, message := range messages {
		if message.Role != "system" {
			break
		}
		prefix = append(prefix, message)
	}
	return prefix
}

func hashJSON(value any) string {
	data, err := json.Marshal(value)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(data)
	return "sha256:" + hex.EncodeToString(sum[:])
}
