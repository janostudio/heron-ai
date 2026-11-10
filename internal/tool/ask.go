package tool

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/heron-ai/heron-engine/pkg/types"
)

// AskUserQuestion is an Agent-facing pause request. The Tool itself does not
// block waiting for a UI; it returns a pending input request so the Agent
// saves a waiting checkpoint and FlowRuntime can suspend the current session
// and later resume it with user input.
//
// It carries two request shapes. `options` asks the person to pick from a list;
// `fields` asks for several values at once. They are mutually exclusive on
// purpose: mixing them leaves the caller unsure whether an answer is a choice
// or a form, and every renderer would have to invent its own rule.
type AskUserQuestionTool struct{}

func NewAskUserQuestionTool() *AskUserQuestionTool {
	return &AskUserQuestionTool{}
}

func (t *AskUserQuestionTool) Name() string { return "AskUserQuestion" }
func (t *AskUserQuestionTool) Description() string {
	return "Ask the user a question and pause until input is provided. Use `options` for a single choice, or `fields` to collect several values at once."
}
func (t *AskUserQuestionTool) NeedsApproval() bool { return false }
func (t *AskUserQuestionTool) Execution() types.ToolExecutionSpec {
	return types.ToolExecutionSpec{Class: types.ToolSerial}
}
func (t *AskUserQuestionTool) Parameters() map[string]any {
	return map[string]any{
		"question":     map[string]any{"type": "string", "description": "Question to show the user", "required": true},
		"options":      map[string]any{"type": "array", "description": "Optional answer choices. Use for a single choice; do not combine with fields"},
		"header":       map[string]any{"type": "string", "description": "Optional short label"},
		"multi_select": map[string]any{"type": "boolean", "description": "Whether multiple choices may be selected"},
		"fields": map[string]any{
			"type": "array",
			"description": "Structured form: collect several values in one pause instead of asking again per value. " +
				"Each item is {\"name\": string (required, unique), \"type\": \"string\"|\"number\"|\"boolean\"|\"enum\" (default string), " +
				"\"description\": string (optional, shown next to the field), \"options\": string[] (required when type is enum), " +
				"\"required\": bool (optional), \"default\": string (optional, must satisfy the type)}. " +
				"Example: [{\"name\":\"repo\",\"description\":\"target repository\"},{\"name\":\"mode\",\"type\":\"enum\",\"options\":[\"read\",\"write\"],\"required\":true}]",
		},
	}
}

func (t *AskUserQuestionTool) Execute(ctx context.Context, params map[string]any) (*types.ToolResult, error) {
	if err := contextError(ctx); err != nil {
		return &types.ToolResult{Success: false, Error: err.Error()}, nil
	}
	question := strings.TrimSpace(stringParam(params, "question"))
	if question == "" {
		return &types.ToolResult{Success: false, Error: "question parameter is required"}, nil
	}
	options := stringSliceParam(params, "options")
	if err := ensureQuestionOptions(options); err != nil {
		return &types.ToolResult{Success: false, Error: err.Error()}, nil
	}
	fields, err := parsePendingFields(params["fields"])
	if err != nil {
		return &types.ToolResult{Success: false, Error: err.Error()}, nil
	}
	if len(fields) > 0 && len(options) > 0 {
		return &types.ToolResult{
			Success: false,
			Error:   "options and fields cannot be combined: use options for a single choice, or fields to collect several values",
		}, nil
	}
	payload := map[string]any{
		"question":     question,
		"options":      options,
		"header":       stringParam(params, "header"),
		"multi_select": boolParam(params, "multi_select"),
	}
	pending := &types.AgentPendingInput{
		Question:    question,
		Options:     options,
		Header:      stringParam(params, "header"),
		MultiSelect: boolParam(params, "multi_select"),
		Fields:      fields,
	}
	if len(fields) > 0 {
		payload["fields"] = fields
	}
	data, _ := json.Marshal(payload)
	return &types.ToolResult{
		Success:      true,
		Content:      string(data),
		Metadata:     payload,
		PendingInput: pending,
	}, nil
}

func contextError(ctx context.Context) error {
	if ctx == nil {
		return nil
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
		return nil
	}
}

func stringSliceParam(params map[string]any, key string) []string {
	value, ok := params[key]
	if !ok || value == nil {
		return nil
	}
	switch typed := value.(type) {
	case []string:
		return typed
	case []any:
		result := make([]string, 0, len(typed))
		for _, item := range typed {
			if text, ok := item.(string); ok {
				result = append(result, text)
			}
		}
		return result
	default:
		return nil
	}
}

func ensureQuestionOptions(options []string) error {
	for _, option := range options {
		if strings.TrimSpace(option) == "" {
			return fmt.Errorf("question options must not contain empty values")
		}
	}
	return nil
}

// fieldTypes is the closed set a request may declare. An unknown type is an
// error rather than a passthrough: the renderer draws each type differently,
// so a typo would otherwise produce a field nobody can fill correctly.
var fieldTypes = map[string]bool{
	"string":  true,
	"number":  true,
	"boolean": true,
	"enum":    true,
}

// parsePendingFields validates the structured form of a request.
//
// Every rejection names the offending field and the reason. A model that gets
// "invalid fields" back has to guess; one that gets "fields[1] (mode): type
// enum requires options" can correct it in a single retry, and this is the
// only path where the model is composing a schema rather than using one.
func parsePendingFields(raw any) ([]types.PendingInputField, error) {
	if raw == nil {
		return nil, nil
	}
	switch typed := raw.(type) {
	case []types.PendingInputField:
		return validatePendingFields(typed)
	case []any:
		fields := make([]types.PendingInputField, 0, len(typed))
		for index, item := range typed {
			entry, ok := item.(map[string]any)
			if !ok {
				return nil, fmt.Errorf("fields[%d] must be an object with a name", index)
			}
			fields = append(fields, pendingFieldFromParams(entry))
		}
		return validatePendingFields(fields)
	case []map[string]any:
		fields := make([]types.PendingInputField, 0, len(typed))
		for _, entry := range typed {
			fields = append(fields, pendingFieldFromParams(entry))
		}
		return validatePendingFields(fields)
	default:
		return nil, fmt.Errorf("fields must be an array of field objects, got %T", raw)
	}
}

func pendingFieldFromParams(entry map[string]any) types.PendingInputField {
	return types.PendingInputField{
		Name:        strings.TrimSpace(stringParam(entry, "name")),
		Type:        strings.ToLower(strings.TrimSpace(stringParam(entry, "type"))),
		Description: stringParam(entry, "description"),
		Options:     stringSliceParam(entry, "options"),
		Required:    boolParam(entry, "required"),
		Default:     stringParam(entry, "default"),
	}
}

func validatePendingFields(fields []types.PendingInputField) ([]types.PendingInputField, error) {
	seen := make(map[string]int, len(fields))
	for index := range fields {
		field := &fields[index]
		if field.Name == "" {
			return nil, fmt.Errorf("fields[%d] is missing a name", index)
		}
		if previous, duplicated := seen[field.Name]; duplicated {
			return nil, fmt.Errorf("fields[%d] (%s): duplicate name, already used by fields[%d]", index, field.Name, previous)
		}
		seen[field.Name] = index

		if field.Type == "" {
			field.Type = "string"
		}
		if !fieldTypes[field.Type] {
			return nil, fmt.Errorf("fields[%d] (%s): unknown type %q, expected string, number, boolean or enum", index, field.Name, field.Type)
		}
		if field.Type == "enum" {
			if len(field.Options) == 0 {
				return nil, fmt.Errorf("fields[%d] (%s): type enum requires options listing the accepted values", index, field.Name)
			}
			if err := ensureQuestionOptions(field.Options); err != nil {
				return nil, fmt.Errorf("fields[%d] (%s): %w", index, field.Name, err)
			}
		} else if len(field.Options) > 0 {
			return nil, fmt.Errorf("fields[%d] (%s): options are only allowed when type is enum, but type is %s", index, field.Name, field.Type)
		}
		if field.Default != "" {
			if err := checkFieldDefault(*field); err != nil {
				return nil, fmt.Errorf("fields[%d] (%s): %w", index, field.Name, err)
			}
		}
	}
	return fields, nil
}

// checkFieldDefault rejects a default the field itself would not accept, so a
// caller never has to decide what "default 5 on an enum of two words" means.
func checkFieldDefault(field types.PendingInputField) error {
	switch field.Type {
	case "enum":
		for _, option := range field.Options {
			if option == field.Default {
				return nil
			}
		}
		return fmt.Errorf("default %q is not one of the options %v", field.Default, field.Options)
	case "boolean":
		if field.Default != "true" && field.Default != "false" {
			return fmt.Errorf("default %q is not a boolean, expected true or false", field.Default)
		}
	case "number":
		if _, err := strconv.ParseFloat(field.Default, 64); err != nil {
			return fmt.Errorf("default %q is not a number: %v", field.Default, err)
		}
	}
	return nil
}
