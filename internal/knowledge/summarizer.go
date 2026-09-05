package knowledge

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/heron-ai/heron-engine/pkg/types"
)

const summarizerSystemPrompt = `You are a knowledge summarizer. Your task is to distill candidate sources into a single, precisely-formatted Knowledge entry.

## Input
You are given candidate sources: SharedRecords, Workspace diffs, test results, and Confirmed/Decisions from State. Not every source is worth keeping.

## Selection rules
- Only distill durable, reusable knowledge (facts, rules, preferences, procedures, decisions, lessons).
- Drop raw natural-language chatter, private drafts, and unreleased tool output.
- If a candidate lacks a verifiable basis, discard it — never invent provenance.
- If a candidate conflicts with active knowledge, mark the conflict; do not silently rewrite history.

## Output format (STRICT)
Emit exactly one Markdown document with a YAML frontmatter and the following sections. No preamble, no extra text outside the document.

---
schema_version: v1
kind: <fact|rule|preference|procedure|decision|lesson>
id: <stable-id>
scope: <flow|team|agent>
workspace_id: <workspace>
flow: <flow-id>
status: active
confidence: <high|medium|low>
keywords: [ ... ]
---

# <title — one sentence>

## Statement
<the distilled claim in 1-3 sentences>

## Data
<structured facts: identifiers, paths, values>

## Basis
- SharedRecord: <id>
- Workspace: <path:lines @ sha256>

## Usage
<when this knowledge applies>

## Notes
<precedence, supersedes, or open caveats>`

const summarizerUserTemplate = `Distill the following candidate sources into one Knowledge entry:

<candidate_sources>
%s
</candidate_sources>`

// layeredSummarizerSystemPrompt instructs the model to emit multiple Knowledge
// entries, one or more per source layer. Each candidate source is prefixed
// with its layer (flow/team/agent), and the model must set each entry's scope
// to the layer its content originated from.
const layeredSummarizerSystemPrompt = `You are a knowledge summarizer. Your task is to distill candidate sources into one or more precisely-formatted Knowledge entries.

## Input
You are given candidate sources, each tagged with the layer it came from: flow, team, or agent. A single session may yield knowledge belonging to several layers.

## Selection rules
- Only distill durable, reusable knowledge (facts, rules, preferences, procedures, decisions, lessons).
- Drop raw natural-language chatter, private drafts, and unreleased tool output.
- If a candidate lacks a verifiable basis, discard it — never invent provenance.
- If a candidate conflicts with active knowledge, mark the conflict; do not silently rewrite history.
- Emit one entry per distinct, durable piece of knowledge. You MAY emit multiple entries when the sources contain more than one durable lesson. Set each entry's scope to the layer its source was tagged with.

## Output format (STRICT)
Emit one or more Markdown documents. Separate consecutive documents with a line containing exactly ---KNOWLEDGE---. Each document has a YAML frontmatter and the following sections. No preamble, no extra text outside the documents.

---
schema_version: v1
kind: <fact|rule|preference|procedure|decision|lesson>
id: <stable-id>
scope: <flow|team|agent>
workspace_id: <workspace>
flow: <flow-id>
status: active
confidence: <high|medium|low>
keywords: [ ... ]
---

# <title — one sentence>

## Statement
<the distilled claim in 1-3 sentences>

## Data
<structured facts: identifiers, paths, values>

## Basis
- SharedRecord: <id>
- Workspace: <path:lines @ sha256>

## Usage
<when this knowledge applies>

## Notes
<precedence, supersedes, or open caveats>`

// layeredSummarizerUserTemplate formats layer-tagged candidate sources.
const layeredSummarizerUserTemplate = `Distill the following candidate sources into one or more Knowledge entries. Each source is prefixed with its layer.

<candidate_sources>
%s
</candidate_sources>`

// layeredSeparator marks the boundary between consecutive Knowledge documents
// in a layered summarizer response.
const layeredSeparator = "---KNOWLEDGE---"

// LayeredSource is a candidate knowledge source tagged with the layer it came
// from (flow/team/agent).
type LayeredSource struct {
	Layer string // flow | team | agent
	Text  string
}

// KnowledgeSummarizer 用 LLM 把候选来源总结成固定格式 Knowledge 条目。
type KnowledgeSummarizer struct {
	model  types.ModelProvider
	config types.ModelConfig
}

// NewKnowledgeSummarizer 构造 KnowledgeSummarizer。summaryModel 为空时走默认模型
// （config.Model 留空，ProviderRouter 会自动回退 defaultModel）；非空则
// config.Model = summaryModel。同时覆盖 MaxOutputTokens=2048、Temperature=0.0、
// Reasoning=nil。
func NewKnowledgeSummarizer(model types.ModelProvider, summaryModel string) *KnowledgeSummarizer {
	config := types.ModelConfig{Model: summaryModel}
	maxOutputTokens := 2048
	config.MaxOutputTokens = &maxOutputTokens
	temperature := 0.0
	config.Temperature = &temperature
	config.Reasoning = nil
	return &KnowledgeSummarizer{model: model, config: config}
}

// Summarize 总结候选来源为一条 Knowledge Markdown（含 frontmatter + 正文）。
// 输入 sources 是候选文本片段。返回的 Markdown 由调用方决定落盘位置。
func (s *KnowledgeSummarizer) Summarize(ctx context.Context, sources []string) (string, error) {
	if len(sources) == 0 {
		return "", errors.New("knowledge summarizer: no candidate sources")
	}

	material := strings.Join(sources, "\n\n")
	messages := []types.Message{
		{Role: "system", Content: summarizerSystemPrompt},
		{Role: "user", Content: fmt.Sprintf(summarizerUserTemplate, material)},
	}

	resp, err := s.model.Chat(ctx, messages, nil, s.config)
	if err != nil {
		return "", err
	}
	if resp == nil {
		return "", errors.New("knowledge summarizer: nil response")
	}
	return strings.TrimSpace(resp.Text), nil
}

// SummarizeLayered distills layer-tagged candidate sources into one or more
// Knowledge Markdown documents. Each returned string is a standalone document
// (frontmatter + body); the caller parses each one to recover its scope.
func (s *KnowledgeSummarizer) SummarizeLayered(ctx context.Context, sources []LayeredSource) ([]string, error) {
	if len(sources) == 0 {
		return nil, errors.New("knowledge summarizer: no candidate sources")
	}

	var b strings.Builder
	for _, src := range sources {
		layer := strings.TrimSpace(src.Layer)
		if layer == "" {
			layer = "flow"
		}
		fmt.Fprintf(&b, "[layer: %s]\n%s\n\n", layer, strings.TrimSpace(src.Text))
	}
	material := strings.TrimSpace(b.String())

	messages := []types.Message{
		{Role: "system", Content: layeredSummarizerSystemPrompt},
		{Role: "user", Content: fmt.Sprintf(layeredSummarizerUserTemplate, material)},
	}

	resp, err := s.model.Chat(ctx, messages, nil, s.config)
	if err != nil {
		return nil, err
	}
	if resp == nil {
		return nil, errors.New("knowledge summarizer: nil response")
	}

	docs := splitLayeredDocuments(resp.Text)
	if len(docs) == 0 {
		return nil, errors.New("knowledge summarizer: empty output")
	}
	return docs, nil
}

// splitLayeredDocuments splits a layered summarizer response on the separator,
// trimming whitespace and dropping empty fragments.
func splitLayeredDocuments(text string) []string {
	parts := strings.Split(text, layeredSeparator)
	var docs []string
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			docs = append(docs, p)
		}
	}
	return docs
}
