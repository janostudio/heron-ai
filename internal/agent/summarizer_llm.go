package agent

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/heron-ai/heron-engine/pkg/types"
)

const compactionSummarySystemPrompt = `You are summarizing a conversation that has been compacted out of the active context. Produce a concise, structured summary that preserves the information a future turn needs to continue correctly.

Requirements:
- Write the summary in the same language as the conversation.
- Preserve: the user's primary goals, key technical decisions and their rationale, concrete implementation details (file names, function names, values), completed progress, and remaining open items.
- Do NOT re-state tool-call mechanics (e.g. "assistant called tool X"). Focus on the information content and its consequences.
- Be dense and specific. Prefer exact identifiers over vague descriptions.
- Output ONLY the summary text. No preamble, no headings, no Markdown fences.`

const compactionSummaryUserTemplate = `Summarize the following dropped conversation segments:

<conversation_history>
%s
</conversation_history>`

// llmSummarizer 用 agent 自己的 model 生成结构化摘要。
type llmSummarizer struct {
	model  types.ModelProvider
	config types.ModelConfig
}

func (s *llmSummarizer) Summarize(ctx context.Context, groups [][]types.Message) (string, error) {
	material := buildContextSummary(groups, 0)
	messages := []types.Message{
		{Role: "system", Content: compactionSummarySystemPrompt},
		{Role: "user", Content: fmt.Sprintf(compactionSummaryUserTemplate, material)},
	}
	resp, err := s.model.Chat(ctx, messages, nil, s.config)
	if err != nil {
		return "", err
	}
	if resp == nil {
		return "", errors.New("llm summarizer: nil response")
	}
	return strings.TrimSpace(resp.Text), nil
}

// NewLLMSummarizer returns a Summarizer that uses the agent's own model to
// produce a structured summary of dropped message groups.
func NewLLMSummarizer(model types.ModelProvider, base types.ModelConfig) Summarizer {
	config := base
	maxOutputTokens := 1024
	config.MaxOutputTokens = &maxOutputTokens
	temperature := 0.0
	config.Temperature = &temperature
	config.Reasoning = nil
	config.ResponseFormat = nil
	return &llmSummarizer{model: model, config: config}
}
