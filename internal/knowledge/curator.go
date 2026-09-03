package knowledge

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/heron-ai/heron-engine/pkg/types"
)

const curatorSystemPrompt = `You are a knowledge curator. Your task is to distill candidate sources into a single, precisely-formatted Knowledge entry.

## Input
You are given candidate sources: SharedRecords, Workspace diffs, test results, and Confirmed/Decisions from Memory. Not every source is worth keeping.

## Selection rules
- Only distill durable, reusable knowledge (facts, rules, preferences, procedures, decisions, lessons).
- Drop raw natural-language chatter, private drafts, and unreleased tool output.
- If a candidate lacks a verifiable basis, discard it — never invent provenance.
- If a candidate conflicts with active knowledge, keep it as proposed and mark the conflict; do not silently rewrite history.

## Output format (STRICT)
Emit exactly one Markdown document with a YAML frontmatter and the following sections. No preamble, no extra text outside the document.

---
schema_version: v1
kind: <fact|rule|preference|procedure|decision|lesson>
id: <stable-id>
scope: <flow|team|agent>
workspace_id: <workspace>
flow: <flow-id>
status: proposed
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

const curatorUserTemplate = `Distill the following candidate sources into one Knowledge entry:

<candidate_sources>
%s
</candidate_sources>`

// Curator 用 LLM 把候选来源提炼成固定格式 Knowledge 条目。
type Curator struct {
	model  types.ModelProvider
	config types.ModelConfig
}

// NewCurator 构造 Curator。curatorModel 为空时走默认模型（config.Model 留空，
// ProviderRouter 会自动回退 defaultModel）；非空则 config.Model = curatorModel。
// 同时覆盖 MaxOutputTokens=2048、Temperature=0.0、Reasoning=nil。
func NewCurator(model types.ModelProvider, curatorModel string) *Curator {
	config := types.ModelConfig{Model: curatorModel}
	maxOutputTokens := 2048
	config.MaxOutputTokens = &maxOutputTokens
	temperature := 0.0
	config.Temperature = &temperature
	config.Reasoning = nil
	return &Curator{model: model, config: config}
}

// Curate 提炼候选来源为一条 Knowledge Markdown（含 frontmatter + 正文）。
// 输入 sources 是候选文本片段。返回的 Markdown 由调用方决定落盘位置。
func (c *Curator) Curate(ctx context.Context, sources []string) (string, error) {
	if len(sources) == 0 {
		return "", errors.New("curator: no candidate sources")
	}

	material := strings.Join(sources, "\n\n")
	messages := []types.Message{
		{Role: "system", Content: curatorSystemPrompt},
		{Role: "user", Content: fmt.Sprintf(curatorUserTemplate, material)},
	}

	resp, err := c.model.Chat(ctx, messages, nil, c.config)
	if err != nil {
		return "", err
	}
	if resp == nil {
		return "", errors.New("curator: nil response")
	}
	return strings.TrimSpace(resp.Text), nil
}
