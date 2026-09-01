package agent

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/heron-ai/heron-engine/pkg/types"
)

type StructuredOutputManager struct{}

const defaultStructuredOutputTokens = 4096

// structuredModelConfig prevents a model registry's large general-purpose
// output/reasoning defaults from consuming the entire budget before a small
// machine-readable JSON decision is emitted.
func structuredModelConfig(agent types.AgentConfig) types.ModelConfig {
	config := agent.Model
	config.ResponseFormat = agent.Structured
	if agent.Structured == nil {
		return config
	}

	limit := defaultStructuredOutputTokens
	if agent.Structured.MaxOutputTokens > 0 {
		limit = agent.Structured.MaxOutputTokens
	} else if explicit := config.OutputTokenLimit(); explicit != nil && *explicit > 0 {
		limit = *explicit
	}
	config.MaxOutputTokens = &limit
	if config.Temperature == nil {
		value := 0.0
		config.Temperature = &value
	}
	if config.TopP == nil {
		value := 1.0
		config.TopP = &value
	}

	// Structured routing Agents should not inherit high reasoning defaults
	// unless they explicitly configure reasoning on the Agent.
	if config.Reasoning == nil {
		config.Reasoning = &types.ReasoningConfig{Type: "none"}
	}
	return config
}

func NewStructuredOutputManager() *StructuredOutputManager {
	return &StructuredOutputManager{}
}

func (m *StructuredOutputManager) ParseAndValidate(raw string, schema *types.StructuredOutput) (any, error) {
	if schema == nil {
		return raw, nil
	}

	result, err := parseJSONDocument(raw)
	if err != nil {
		return nil, fmt.Errorf("parse structured output: %w", err)
	}

	// If schema has required fields, validate them
	if schemaMap, ok := result.(map[string]any); ok {
		normalizeStructuredAliases(schemaMap)
		for key, val := range schema.Schema {
			properties, ok := val.(map[string]any)
			if !ok {
				continue
			}
			required, ok := properties["required"].(bool)
			if ok && required {
				if _, exists := schemaMap[key]; !exists {
					return nil, fmt.Errorf("missing required field: %s", key)
				}
			}
		}
	} else if len(schema.Schema) > 0 {
		return nil, fmt.Errorf("structured output must be a JSON object")
	}

	return result, nil
}

// parseJSONDocument accepts strict JSON as well as the common model response
// forms where JSON is surrounded by a short explanation or a Markdown
// ```json fenced block. The OpenAI-compatible gateway does not guarantee
// response_format enforcement for every upstream model, so parsing belongs at
// this boundary rather than in every Agent definition.
func parseJSONDocument(raw string) (any, error) {
	text := strings.TrimSpace(raw)
	if text == "" {
		return nil, fmt.Errorf("empty response")
	}
	// A model can hit max_tokens while emitting a JSON object. In that case a
	// prefix of the object is not useful as a structured result; return a
	// clear error so the caller can coordinate rather than guessing fields.

	candidates := make([]string, 0, 4)
	appendCandidate := func(candidate string) {
		candidate = strings.TrimSpace(candidate)
		if candidate == "" {
			return
		}
		for _, existing := range candidates {
			if existing == candidate {
				return
			}
		}
		candidates = append(candidates, candidate)
	}

	appendCandidate(text)
	for _, candidate := range fencedJSONCandidates(text) {
		appendCandidate(candidate)
	}
	for _, candidate := range balancedJSONCandidates(text) {
		appendCandidate(candidate)
	}

	var lastErr error
	for _, candidate := range candidates {
		var result any
		if err := json.Unmarshal([]byte(candidate), &result); err != nil {
			lastErr = err
			continue
		}
		return result, nil
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("no JSON object found")
	}
	return nil, lastErr
}

func fencedJSONCandidates(text string) []string {
	var candidates []string
	for offset := 0; ; {
		start := strings.Index(text[offset:], "```")
		if start < 0 {
			break
		}
		start += offset
		headerEnd := strings.IndexByte(text[start+3:], '\n')
		if headerEnd < 0 {
			break
		}
		bodyStart := start + 3 + headerEnd + 1
		end := strings.Index(text[bodyStart:], "```")
		if end < 0 {
			break
		}
		body := text[bodyStart : bodyStart+end]
		if strings.HasPrefix(strings.TrimSpace(text[start+3:start+3+headerEnd]), "json") {
			candidates = append(candidates, body)
		} else {
			candidates = append(candidates, body)
		}
		offset = bodyStart + end + 3
	}
	return candidates
}

func balancedJSONCandidates(text string) []string {
	var candidates []string
	start := -1
	depth := 0
	inString := false
	escaped := false

	for index, char := range text {
		if start < 0 {
			if char == '{' || char == '[' {
				start = index
				depth = 1
				inString = false
				escaped = false
			}
			continue
		}

		if inString {
			if escaped {
				escaped = false
				continue
			}
			if char == '\\' {
				escaped = true
				continue
			}
			if char == '"' {
				inString = false
			}
			continue
		}

		switch char {
		case '"':
			inString = true
		case '{', '[':
			depth++
		case '}', ']':
			depth--
			if depth == 0 {
				candidates = append(candidates, text[start:index+1])
				start = -1
			}
		}
	}
	return candidates
}

func normalizeStructuredAliases(object map[string]any) {
	if _, exists := object["reply"]; exists {
		return
	}
	for _, alias := range []string{"message_to_user", "reply_to_user", "message"} {
		if value, ok := object[alias].(string); ok && strings.TrimSpace(value) != "" {
			object["reply"] = value
			return
		}
	}
}

func (m *StructuredOutputManager) ToProviderFormat(schema *types.StructuredOutput) map[string]any {
	if schema == nil {
		return nil
	}

	return map[string]any{
		"type":        "json_schema",
		"json_schema": schema.Schema,
	}
}
