package config

import (
	"encoding/json"
	"fmt"
	"reflect"
	"sort"
	"strings"

	"github.com/heron-ai/heron-engine/pkg/types"
)

// UnknownField is one key in a settings file that no loader will ever read.
//
// It is a report, not an error: the engine keeps starting. A misspelled key
// used to be invisible — every loader decodes only its own section, so a key
// that matches none of them simply disappears — which is how a configuration
// that looks applied ends up doing nothing.
type UnknownField struct {
	// Path is the key's location, e.g. `runtime.max_round` or `mcp[0].arg`.
	Path string
	// Suggestion names the closest accepted key at the same level, when one is
	// close enough to be a likely typo. Empty when nothing is close.
	Suggestion string
}

// String renders one line a person can act on.
func (f UnknownField) String() string {
	if f.Suggestion == "" {
		return fmt.Sprintf("unknown setting %q", f.Path)
	}
	return fmt.Sprintf("unknown setting %q (did you mean %q?)", f.Path, f.Suggestion)
}

// settingsSchema mirrors every section the Load* methods read from
// .agents/settings.json.
//
// It is the one place that knows the whole file. Each loader decodes its own
// section and ignores everything else, so only a schema covering all of them
// can tell "this key belongs to another section" from "no section reads this
// key at all".
type settingsSchema struct {
	Runtime   types.RuntimeLimits   `json:"runtime"`
	Logging   types.LoggingConfig   `json:"logging"`
	Knowledge types.KnowledgeConfig `json:"knowledge"`
	Web       types.WebConfig       `json:"web"`
	Tools     struct {
		RestrictedBashPhrases []string           `json:"restricted_bash_phrases"`
		Repl                  types.ScriptConfig `json:"repl"`
	} `json:"tools"`
	Env map[string]string       `json:"env"`
	MCP []types.MCPServerConfig `json:"mcp"`
}

// UnknownSettingsFields reports keys in .agents/settings.json that no loader
// reads. A missing file, or one that is not valid JSON, reports nothing: the
// first is a legal state and the second is LoadMCPServers' error to surface.
func (l *ConfigLoader) UnknownSettingsFields() []UnknownField {
	if l == nil {
		return nil
	}
	data, present := l.settingsBytes()
	if !present {
		return nil
	}
	return CheckSettingsFields(data)
}

// CheckSettingsFields reports keys in a settings document that no loader reads.
//
// Unknown keys are reported, never fatal: a settings file written for a newer
// build, or one carrying an extension's own section, still starts. The report
// exists so the typo is visible now rather than as "why did my setting not
// apply" later.
func CheckSettingsFields(data []byte) []UnknownField {
	if len(data) == 0 {
		return nil
	}
	var generic any
	if err := json.Unmarshal(data, &generic); err != nil {
		// Malformed JSON is LoadMCPServers' error to report; there is no field
		// structure to walk here.
		return nil
	}
	var out []UnknownField
	walkUnknownFields(generic, reflect.TypeOf(settingsSchema{}), "", &out)
	return out
}

// CheckFieldsAgainst reports keys in data that target's type does not accept.
// It is the generic form used by settings.json today and available to any other
// JSON config file with a struct schema.
func CheckFieldsAgainst(data []byte, target any) []UnknownField {
	if len(data) == 0 || target == nil {
		return nil
	}
	var generic any
	if err := json.Unmarshal(data, &generic); err != nil {
		return nil
	}
	var out []UnknownField
	walkUnknownFields(generic, reflect.TypeOf(target), "", &out)
	return out
}

func walkUnknownFields(value any, target reflect.Type, path string, out *[]UnknownField) {
	target = indirectType(target)
	if target == nil {
		return
	}
	switch typed := value.(type) {
	case map[string]any:
		if target.Kind() != reflect.Struct {
			// A map's keys are the user's own data (env names, headers), not a
			// schema to check against.
			return
		}
		fields := jsonFieldsOf(target)
		keys := make([]string, 0, len(typed))
		for key := range typed {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		accepted := make([]string, 0, len(fields))
		for name := range fields {
			accepted = append(accepted, name)
		}
		for _, key := range keys {
			if field, ok := fields[key]; ok {
				walkUnknownFields(typed[key], field.Type, joinFieldPath(path, key), out)
				continue
			}
			// encoding/json matches names case-insensitively, so `Web` reaches
			// the `web` section. Reporting it would be a false positive on a
			// file that works.
			if field, ok := lookupFieldFold(fields, key); ok {
				walkUnknownFields(typed[key], field.Type, joinFieldPath(path, key), out)
				continue
			}
			*out = append(*out, UnknownField{
				Path:       joinFieldPath(path, key),
				Suggestion: nearestName(key, accepted),
			})
		}
	case []any:
		elem := target
		if elem.Kind() != reflect.Slice && elem.Kind() != reflect.Array {
			return
		}
		elem = elem.Elem()
		for i, item := range typed {
			walkUnknownFields(item, elem, fmt.Sprintf("%s[%d]", path, i), out)
		}
	}
}

func indirectType(t reflect.Type) reflect.Type {
	for t != nil && t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	return t
}

// jsonFieldsOf maps the JSON name of each settable field to the field itself.
// It mirrors encoding/json: the tag name wins, `-` removes the field, and a
// field without a tag answers to its Go name.
func jsonFieldsOf(t reflect.Type) map[string]reflect.StructField {
	out := make(map[string]reflect.StructField, t.NumField())
	for i := 0; i < t.NumField(); i++ {
		field := t.Field(i)
		if field.PkgPath != "" { // unexported
			continue
		}
		tag := field.Tag.Get("json")
		if tag == "-" {
			continue
		}
		name := strings.Split(tag, ",")[0]
		if name == "" {
			name = field.Name
		}
		out[name] = field
	}
	return out
}

func joinFieldPath(parent, key string) string {
	if parent == "" {
		return key
	}
	return parent + "." + key
}

// lookupFieldFold finds a field whose JSON name matches key apart from case,
// which is what encoding/json does.
func lookupFieldFold(fields map[string]reflect.StructField, key string) (reflect.StructField, bool) {
	lowered := strings.ToLower(key)
	for name, field := range fields {
		if strings.ToLower(name) == lowered {
			return field, true
		}
	}
	return reflect.StructField{}, false
}

// nearestName returns the accepted name closest to key, when one is close
// enough to be a typo rather than a different word entirely.
//
// The thresholds are deliberately tight: suggesting "web.timeout" for
// "web.token" would be noise, and a wrong suggestion is worse than none.
func nearestName(key string, accepted []string) string {
	best := ""
	bestDistance := 1 << 30
	loweredKey := strings.ToLower(key)
	for _, candidate := range accepted {
		lowered := strings.ToLower(candidate)
		if lowered == loweredKey {
			// encoding/json matches names case-insensitively, so this key is
			// actually read. The walk already treats it as unknown only when
			// the case differs; keep the suggestion precise.
			return candidate
		}
		distance := editDistance(loweredKey, lowered)
		if distance < bestDistance {
			bestDistance = distance
			best = candidate
		}
	}
	if best == "" {
		return ""
	}
	limit := 2
	if len(key) <= 4 {
		limit = 1
	}
	if bestDistance > limit {
		return ""
	}
	return best
}

// editDistance is the optimal-string-alignment distance: insert, delete,
// substitute, and swap two adjacent characters all cost one.
//
// The swap rule matters here because transposing two letters is the most
// common way to mistype a short key (`nmae` for `name`), and plain
// Levenshtein scores it as two edits — enough to miss a suggestion entirely.
func editDistance(a, b string) int {
	if a == b {
		return 0
	}
	rows := len(a) + 1
	cols := len(b) + 1
	d := make([][]int, rows)
	for i := range d {
		d[i] = make([]int, cols)
		d[i][0] = i
	}
	for j := 0; j < cols; j++ {
		d[0][j] = j
	}
	for i := 1; i < rows; i++ {
		for j := 1; j < cols; j++ {
			cost := 1
			if a[i-1] == b[j-1] {
				cost = 0
			}
			d[i][j] = minInt(
				d[i-1][j]+1,
				d[i][j-1]+1,
				d[i-1][j-1]+cost,
			)
			if i > 1 && j > 1 && a[i-1] == b[j-2] && a[i-2] == b[j-1] {
				d[i][j] = minInt(d[i][j], d[i-2][j-2]+1)
			}
		}
	}
	return d[rows-1][cols-1]
}

func minInt(values ...int) int {
	smallest := values[0]
	for _, value := range values[1:] {
		if value < smallest {
			smallest = value
		}
	}
	return smallest
}
