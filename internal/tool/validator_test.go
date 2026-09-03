package tool

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestIsInteger(t *testing.T) {
	tests := []struct {
		name  string
		value any
		want  bool
	}{
		{"int", 42, true},
		{"int negative", -42, true},
		{"int8", int8(8), true},
		{"int16", int16(16), true},
		{"int32", int32(32), true},
		{"int64", int64(64), true},
		{"uint", uint(7), true},
		{"uint8", uint8(8), true},
		{"uint16", uint16(16), true},
		{"uint32", uint32(32), true},
		{"uint64", uint64(64), true},
		{"float64 integral", 3.0, true},
		{"float64 negative integral", -2.0, true},
		{"float64 zero", 0.0, true},
		{"float64 fractional", 3.5, false},
		{"float64 tiny fraction", 0.1, false},
		{"float32", float32(3.0), false},
		{"string", "3", false},
		{"bool", true, false},
		{"nil", nil, false},
		{"slice", []int{1}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, isInteger(tt.value))
		})
	}
}

func TestIsNumber(t *testing.T) {
	tests := []struct {
		name  string
		value any
		want  bool
	}{
		{"int", 42, true},
		{"int64", int64(64), true},
		{"uint", uint(7), true},
		{"float32", float32(1.5), true},
		{"float64", 3.14, true},
		{"string", "3.14", false},
		{"bool", false, false},
		{"nil", nil, false},
		{"map", map[string]any{}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, isNumber(tt.value))
		})
	}
}

func TestIsStringType(t *testing.T) {
	require.True(t, isStringType("string"))
	require.False(t, isStringType("integer"))
	require.False(t, isStringType(nil))
	require.False(t, isStringType(42))
}

func TestStringValue(t *testing.T) {
	require.Equal(t, "hello", stringValue("hello"))
	require.Equal(t, "", stringValue(42))
	require.Equal(t, "", stringValue(nil))
	require.Equal(t, "", stringValue(""))
}

func TestStringSlice(t *testing.T) {
	t.Run("native string slice", func(t *testing.T) {
		got, ok := stringSlice([]string{"a", "b"})
		require.True(t, ok)
		require.Equal(t, []string{"a", "b"}, got)
	})

	t.Run("any slice of strings", func(t *testing.T) {
		got, ok := stringSlice([]any{"a", "b"})
		require.True(t, ok)
		require.Equal(t, []string{"a", "b"}, got)
	})

	t.Run("empty native slice", func(t *testing.T) {
		got, ok := stringSlice([]string{})
		require.True(t, ok)
		require.Empty(t, got)
	})

	t.Run("empty any slice", func(t *testing.T) {
		got, ok := stringSlice([]any{})
		require.True(t, ok)
		require.Empty(t, got)
	})

	t.Run("mixed any slice", func(t *testing.T) {
		_, ok := stringSlice([]any{"a", 42})
		require.False(t, ok)
	})

	t.Run("non-slice", func(t *testing.T) {
		_, ok := stringSlice("a")
		require.False(t, ok)
	})

	t.Run("nil", func(t *testing.T) {
		_, ok := stringSlice(nil)
		require.False(t, ok)
	})

	t.Run("int slice", func(t *testing.T) {
		_, ok := stringSlice([]int{1, 2})
		require.False(t, ok)
	})
}

func TestValidateValue(t *testing.T) {
	stringProp := map[string]any{"type": "string"}
	integerProp := map[string]any{"type": "integer"}
	numberProp := map[string]any{"type": "number"}
	boolProp := map[string]any{"type": "boolean"}
	arrayProp := map[string]any{"type": "array"}
	objectProp := map[string]any{"type": "object"}
	anyProp := map[string]any{"type": "any"}
	emptyProp := map[string]any{}

	t.Run("string valid", func(t *testing.T) {
		require.NoError(t, validateValue("p", "hello", stringProp))
	})
	t.Run("string invalid", func(t *testing.T) {
		require.Error(t, validateValue("p", 42, stringProp))
	})
	t.Run("integer valid", func(t *testing.T) {
		require.NoError(t, validateValue("p", 42, integerProp))
	})
	t.Run("integer invalid", func(t *testing.T) {
		require.Error(t, validateValue("p", "42", integerProp))
		require.Error(t, validateValue("p", 3.5, integerProp))
	})
	t.Run("number valid int", func(t *testing.T) {
		require.NoError(t, validateValue("p", 42, numberProp))
	})
	t.Run("number valid float", func(t *testing.T) {
		require.NoError(t, validateValue("p", 3.14, numberProp))
	})
	t.Run("number invalid", func(t *testing.T) {
		require.Error(t, validateValue("p", "3.14", numberProp))
	})
	t.Run("boolean valid", func(t *testing.T) {
		require.NoError(t, validateValue("p", true, boolProp))
	})
	t.Run("boolean invalid", func(t *testing.T) {
		require.Error(t, validateValue("p", "true", boolProp))
	})
	t.Run("array valid slice", func(t *testing.T) {
		require.NoError(t, validateValue("p", []any{1, 2}, arrayProp))
	})
	t.Run("array valid array", func(t *testing.T) {
		require.NoError(t, validateValue("p", [2]int{1, 2}, arrayProp))
	})
	t.Run("array invalid", func(t *testing.T) {
		require.Error(t, validateValue("p", "not-array", arrayProp))
	})
	t.Run("object valid", func(t *testing.T) {
		require.NoError(t, validateValue("p", map[string]any{"k": "v"}, objectProp))
	})
	t.Run("object invalid", func(t *testing.T) {
		require.Error(t, validateValue("p", []any{1}, objectProp))
	})
	t.Run("any type", func(t *testing.T) {
		require.NoError(t, validateValue("p", 42, anyProp))
		require.NoError(t, validateValue("p", "x", anyProp))
		require.NoError(t, validateValue("p", nil, anyProp))
	})
	t.Run("empty type treated as any", func(t *testing.T) {
		require.NoError(t, validateValue("p", 42, emptyProp))
	})
	t.Run("unsupported type", func(t *testing.T) {
		require.Error(t, validateValue("p", 42, map[string]any{"type": "unknown"}))
	})
}

func TestValidateParameters(t *testing.T) {
	t.Run("nil schema", func(t *testing.T) {
		require.NoError(t, ValidateParameters(nil, map[string]any{"x": 1}))
	})
	t.Run("nil params", func(t *testing.T) {
		require.NoError(t, ValidateParameters(map[string]any{}, nil))
	})
	t.Run("required present", func(t *testing.T) {
		schema := map[string]any{"file": map[string]any{"type": "string", "required": true}}
		require.NoError(t, ValidateParameters(schema, map[string]any{"file": "a.txt"}))
	})
	t.Run("required missing", func(t *testing.T) {
		schema := map[string]any{"file": map[string]any{"type": "string", "required": true}}
		require.Error(t, ValidateParameters(schema, map[string]any{}))
	})
	t.Run("required empty string", func(t *testing.T) {
		schema := map[string]any{"file": map[string]any{"type": "string", "required": true}}
		require.Error(t, ValidateParameters(schema, map[string]any{"file": "  "}))
	})
	t.Run("required nil value", func(t *testing.T) {
		schema := map[string]any{"file": map[string]any{"type": "string", "required": true}}
		require.Error(t, ValidateParameters(schema, map[string]any{"file": nil}))
	})
	t.Run("enum match", func(t *testing.T) {
		schema := map[string]any{"mode": map[string]any{"type": "string", "enum": []string{"a", "b"}}}
		require.NoError(t, ValidateParameters(schema, map[string]any{"mode": "a"}))
	})
	t.Run("enum mismatch", func(t *testing.T) {
		schema := map[string]any{"mode": map[string]any{"type": "string", "enum": []string{"a", "b"}}}
		require.Error(t, ValidateParameters(schema, map[string]any{"mode": "c"}))
	})
	t.Run("enum native string slice", func(t *testing.T) {
		schema := map[string]any{"mode": map[string]any{"type": "string", "enum": []string{"a", "b"}}}
		require.NoError(t, ValidateParameters(schema, map[string]any{"mode": "b"}))
	})
	t.Run("unexpected param", func(t *testing.T) {
		schema := map[string]any{"file": map[string]any{"type": "string"}}
		require.Error(t, ValidateParameters(schema, map[string]any{"extra": 1}))
	})
	t.Run("non-map definition skipped", func(t *testing.T) {
		schema := map[string]any{"file": "not-a-map"}
		require.NoError(t, ValidateParameters(schema, map[string]any{"file": "x"}))
	})
}
