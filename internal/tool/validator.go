package tool

import (
	"fmt"
	"reflect"
	"strings"
)

// ValidateParameters validates the small JSON-like parameter contract exposed
// by Tool.Parameters. It intentionally stays dependency-free and accepts the
// native Go values produced by the model adapters.
func ValidateParameters(schema map[string]any, params map[string]any) error {
	if schema == nil {
		return nil
	}
	if params == nil {
		params = map[string]any{}
	}

	for name, definition := range schema {
		property, ok := definition.(map[string]any)
		if !ok {
			continue
		}
		required, _ := property["required"].(bool)
		value, exists := params[name]
		emptyString := false
		if isStringType(property["type"]) {
			if text, ok := value.(string); ok {
				emptyString = exists && value != nil && strings.TrimSpace(text) == ""
			}
		}
		if required && (!exists || value == nil || emptyString) {
			return fmt.Errorf("parameter %q is required", name)
		}
		if !exists || value == nil {
			continue
		}
		if err := validateValue(name, value, property); err != nil {
			return err
		}
		if enum, ok := stringSlice(property["enum"]); ok && len(enum) > 0 {
			got := stringValue(value)
			found := false
			for _, candidate := range enum {
				if got == candidate {
					found = true
					break
				}
			}
			if !found {
				return fmt.Errorf("parameter %q must be one of %v", name, enum)
			}
		}
	}
	for name := range params {
		if _, declared := schema[name]; !declared {
			return fmt.Errorf("unexpected parameter %q", name)
		}
	}
	return nil
}

func validateValue(name string, value any, property map[string]any) error {
	expected, _ := property["type"].(string)
	switch expected {
	case "", "any":
		return nil
	case "string":
		if _, ok := value.(string); !ok {
			return fmt.Errorf("parameter %q must be a string", name)
		}
	case "integer":
		if !isInteger(value) {
			return fmt.Errorf("parameter %q must be an integer", name)
		}
	case "number":
		if !isNumber(value) {
			return fmt.Errorf("parameter %q must be a number", name)
		}
	case "boolean":
		if _, ok := value.(bool); !ok {
			return fmt.Errorf("parameter %q must be a boolean", name)
		}
	case "array":
		if reflect.ValueOf(value).Kind() != reflect.Slice && reflect.ValueOf(value).Kind() != reflect.Array {
			return fmt.Errorf("parameter %q must be an array", name)
		}
	case "object":
		if reflect.ValueOf(value).Kind() != reflect.Map {
			return fmt.Errorf("parameter %q must be an object", name)
		}
	default:
		return fmt.Errorf("parameter %q uses unsupported schema type %q", name, expected)
	}
	return nil
}

func isStringType(value any) bool {
	text, _ := value.(string)
	return text == "string"
}

func stringValue(value any) string {
	text, _ := value.(string)
	return text
}

func isInteger(value any) bool {
	switch typed := value.(type) {
	case int, int8, int16, int32, int64:
		return true
	case uint, uint8, uint16, uint32, uint64:
		return true
	case float64:
		return typed == float64(int64(typed))
	default:
		return false
	}
}

func isNumber(value any) bool {
	switch value.(type) {
	case int, int8, int16, int32, int64,
		uint, uint8, uint16, uint32, uint64,
		float32, float64:
		return true
	default:
		return false
	}
}

func stringSlice(value any) ([]string, bool) {
	raw, ok := value.([]string)
	if ok {
		return raw, true
	}
	rawAny, ok := value.([]any)
	if !ok {
		return nil, false
	}
	result := make([]string, 0, len(rawAny))
	for _, item := range rawAny {
		text, ok := item.(string)
		if !ok {
			return nil, false
		}
		result = append(result, text)
	}
	return result, true
}
