package tool

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/heron-ai/heron-engine/pkg/types"
)

// AskUserQuestion is an Agent-facing pause request. The Tool itself does not
// block waiting for a UI; it returns a wait_input route so FlowRuntime can
// suspend the current session and later resume it with user input.
type AskUserQuestionTool struct{}

func NewAskUserQuestionTool() *AskUserQuestionTool {
	return &AskUserQuestionTool{}
}

func (t *AskUserQuestionTool) Name() string { return "AskUserQuestion" }
func (t *AskUserQuestionTool) Description() string {
	return "Ask the user a question and pause until input is provided"
}
func (t *AskUserQuestionTool) NeedsApproval() bool { return false }
func (t *AskUserQuestionTool) Execution() types.ToolExecutionSpec {
	return types.ToolExecutionSpec{Class: types.ToolSerial}
}
func (t *AskUserQuestionTool) Parameters() map[string]any {
	return map[string]any{
		"question":     map[string]any{"type": "string", "description": "Question to show the user", "required": true},
		"options":      map[string]any{"type": "array", "description": "Optional answer choices"},
		"header":       map[string]any{"type": "string", "description": "Optional short label"},
		"multi_select": map[string]any{"type": "boolean", "description": "Whether multiple choices may be selected"},
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
	payload := map[string]any{
		"question":     question,
		"options":      options,
		"header":       stringParam(params, "header"),
		"multi_select": boolParam(params, "multi_select"),
	}
	data, _ := json.Marshal(payload)
	return &types.ToolResult{
		Success:  true,
		Content:  string(data),
		Metadata: payload,
		Next:     &types.Route{Action: types.NextWaitInput, Reason: "user question requires input"},
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
