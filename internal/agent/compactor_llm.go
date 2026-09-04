package agent

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/heron-ai/heron-engine/pkg/types"
)

const compactionSummarySystemPrompt = `You are summarizing a conversation that has been compacted out of the active context. Produce a structured summary that preserves everything a future turn needs to continue correctly.

Write the summary in the same language as the conversation, wrapped in a single <summary> tag. Inside it, emit exactly the following sections in order, each with a Markdown heading:

## Primary Request and Intent
The user's explicit goals and intent, in detail.

## Key Technical Concepts
Frameworks, APIs, and important technical points.

## Files and Code Sections
Files involved, code sections, function signatures, and why each matters.

## Errors and fixes
Every error encountered and how it was fixed.

## Problem Solving
Problems solved and investigations still in progress.

## All user messages
List EVERY non-tool user message verbatim (do not paraphrase or omit any). This prevents goal drift when the user changes or adds constraints mid-session.

## Pending Tasks
Explicitly requested tasks still outstanding.

## Current Work
What was being worked on right before compaction.

## Optional Next Step
The next step directly related to recent work.

Rules:
- Preserve exact identifiers, file names, function names, and values.
- Do NOT re-state tool-call mechanics (e.g. "assistant called tool X"). Focus on information content.
- The "All user messages" section must contain every user message verbatim.
- Output ONLY the <summary> block. No preamble, no Markdown fences.`

const compactionSummaryUserTemplate = `Summarize the following dropped conversation segments:

<conversation_history>
%s
</conversation_history>`

// llmCompactor 用 agent 自己的 model 生成结构化摘要。
type llmCompactor struct {
	model  types.ModelProvider
	config types.ModelConfig
}

func (s *llmCompactor) Compact(ctx context.Context, groups [][]types.Message) (string, error) {
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
		return "", errors.New("llm compactor: nil response")
	}
	return strings.TrimSpace(resp.Text), nil
}

// NewLLMCompactor returns a Compactor that uses the agent's own model to
// produce a structured summary of dropped message groups.
func NewLLMCompactor(model types.ModelProvider, base types.ModelConfig) Compactor {
	config := base
	maxOutputTokens := 1024
	config.MaxOutputTokens = &maxOutputTokens
	temperature := 0.0
	config.Temperature = &temperature
	config.Reasoning = nil
	config.ResponseFormat = nil
	return &llmCompactor{model: model, config: config}
}
