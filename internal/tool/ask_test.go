package tool

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

// A structured request is one pause for several values. The point is the shape
// the caller receives: every field keeps its name, type and constraints, so a
// renderer can draw a form instead of guessing from prose.
func TestAskUserQuestionStructuredFields(t *testing.T) {
	tool := NewAskUserQuestionTool()
	result, err := tool.Execute(context.Background(), map[string]any{
		"question": "Which repository and mode?",
		"header":   "Setup",
		"fields": []any{
			map[string]any{"name": "repo", "description": "target repository"},
			map[string]any{"name": "mode", "type": "enum", "options": []any{"read", "write"}, "required": true},
			map[string]any{"name": "shallow", "type": "boolean", "default": "true"},
		},
	})
	require.NoError(t, err)
	require.True(t, result.Success, result.Error)
	require.NotNil(t, result.PendingInput)

	fields := result.PendingInput.Fields
	require.Len(t, fields, 3)

	// An omitted type means string: a model that writes only a name must not
	// produce a field no renderer knows how to draw.
	require.Equal(t, "repo", fields[0].Name)
	require.Equal(t, "string", fields[0].Type)
	require.Equal(t, "target repository", fields[0].Description)
	require.False(t, fields[0].Required)

	require.Equal(t, "enum", fields[1].Type)
	require.Equal(t, []string{"read", "write"}, fields[1].Options)
	require.True(t, fields[1].Required)

	require.Equal(t, "boolean", fields[2].Type)
	require.Equal(t, "true", fields[2].Default)

	// The payload carries the same structure, so a client that only reads the
	// tool result (and not the checkpoint) still sees the form.
	require.Contains(t, result.Metadata, "fields")
}

func TestAskUserQuestionRejectsInvalidFields(t *testing.T) {
	tool := NewAskUserQuestionTool()
	cases := []struct {
		name   string
		fields []any
		want   string
	}{
		{
			name:   "missing name",
			fields: []any{map[string]any{"type": "string"}},
			want:   "missing a name",
		},
		{
			name: "duplicate name",
			fields: []any{
				map[string]any{"name": "repo"},
				map[string]any{"name": "repo"},
			},
			want: "duplicate name",
		},
		{
			name:   "unknown type",
			fields: []any{map[string]any{"name": "size", "type": "integer"}},
			want:   "unknown type",
		},
		{
			name:   "enum without options",
			fields: []any{map[string]any{"name": "mode", "type": "enum"}},
			want:   "requires options",
		},
		{
			name:   "options on non-enum",
			fields: []any{map[string]any{"name": "mode", "type": "string", "options": []any{"a"}}},
			want:   "only allowed when type is enum",
		},
		{
			name:   "default outside enum",
			fields: []any{map[string]any{"name": "mode", "type": "enum", "options": []any{"a", "b"}, "default": "c"}},
			want:   "is not one of the options",
		},
		{
			name:   "non-boolean default",
			fields: []any{map[string]any{"name": "flag", "type": "boolean", "default": "yes"}},
			want:   "not a boolean",
		},
		{
			name:   "non-numeric default",
			fields: []any{map[string]any{"name": "count", "type": "number", "default": "many"}},
			want:   "not a number",
		},
		{
			name:   "not an object",
			fields: []any{"repo"},
			want:   "must be an object",
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			result, err := tool.Execute(context.Background(), map[string]any{
				"question": "fill this in",
				"fields":   testCase.fields,
			})
			require.NoError(t, err)
			require.False(t, result.Success)
			// The message must name the problem, not just reject: the model is
			// composing a schema here, and this is its only feedback channel.
			require.Contains(t, result.Error, testCase.want)
		})
	}
}

// Mixing the two shapes leaves the caller unsure whether an answer is a choice
// or a form, so the tool refuses rather than picking a rule on their behalf.
func TestAskUserQuestionRejectsOptionsWithFields(t *testing.T) {
	tool := NewAskUserQuestionTool()
	result, err := tool.Execute(context.Background(), map[string]any{
		"question": "pick one",
		"options":  []any{"a", "b"},
		"fields":   []any{map[string]any{"name": "repo"}},
	})
	require.NoError(t, err)
	require.False(t, result.Success)
	require.Contains(t, result.Error, "cannot be combined")
}

// A single-choice question keeps working exactly as before: the structured
// form is an addition, not a replacement.
func TestAskUserQuestionSingleChoiceUnchanged(t *testing.T) {
	tool := NewAskUserQuestionTool()
	result, err := tool.Execute(context.Background(), map[string]any{
		"question": "Which port?",
		"options":  []any{"3000", "5173"},
	})
	require.NoError(t, err)
	require.True(t, result.Success, result.Error)
	require.Equal(t, []string{"3000", "5173"}, result.PendingInput.Options)
	require.Empty(t, result.PendingInput.Fields)
}
