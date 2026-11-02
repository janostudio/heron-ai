package script

import (
	"fmt"
	"sort"
	"strings"

	"github.com/heron-ai/heron-engine/pkg/types"
)

// The catalog is what the model reads before writing code. It is rendered into
// the `code` parameter description, so every byte here is paid on every request
// that carries the Script tool.
//
// Two rules make it different from a standard tool listing:
//
//   - Tools with a mode enum are expanded into one line per mode. A model that
//     only sees "Write(file, content, mode)" writes whole-file overwrites even
//     when edit mode exists and is safer.
//   - Every line states the return shape. Inside a script the model writes
//     `r.stdout`, `r.metadata.revision`, and a wrong field name costs a whole
//     re-run rather than one correction turn.
const (
	// DefaultCatalogBudget matches the deferred-tool budget used elsewhere in
	// the engine: roughly 2k tokens.
	DefaultCatalogBudget = 8000
	// maxParamsPerTool caps the parameter list per line; the rest collapses to
	// "…+N" and is reachable through tools.describe.
	maxParamsPerTool = 6
	// summaryWidth truncates a tool description so one long description cannot
	// eat the whole budget.
	summaryWidth = 80
	serverWidth  = 100
)

// CatalogTool is one tool's contribution to the catalog.
type CatalogTool struct {
	Name        string
	Summary     string
	Parameters  map[string]any
	ReturnHint  string
	ModeHints   map[string]string // mode value -> extra requirement text
	ServerGroup string            // non-empty for MCP tools
}

// CatalogFromTools adapts registry tools to catalog entries. Return hints and
// mode hints come from the tables below because neither is expressible in
// Parameters(), which only describes inputs.
func CatalogFromTools(tools []types.Tool) []CatalogTool {
	out := make([]CatalogTool, 0, len(tools))
	for _, t := range tools {
		name := t.Name()
		entry := CatalogTool{
			Name:        name,
			Summary:     t.Description(),
			Parameters:  t.Parameters(),
			ReturnHint:  returnHints[name],
			ModeHints:   modeHints[name],
			ServerGroup: serverGroupOf(name),
		}
		out = append(out, entry)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// returnHints documents what each builtin returns. Kept as a table rather than
// derived from Execute() because the return shape is a contract with generated
// code, and a contract should be written down where it can be reviewed.
//
// Field names are copied verbatim from the Metadata each tool sets; a rename
// there without a rename here is the silent kind of breakage where the script
// keeps running and reads undefined.
var returnHints = map[string]string{
	"Read":            `{ok, content, metadata{file, revision, truncated, line_start, line_end, total_lines, total_bytes}}`,
	"Write":           `{ok, content, metadata{file, mode, revision, matched_count, changed_bytes}}`,
	"Bash":            `{ok, content, metadata{exit_code, stdout, stderr, timed_out, canceled, truncated}}`,
	"Grep":            `{ok, content, metadata{matches, truncated}}`,
	"Glob":            `{ok, content, metadata{matches, truncated}}`,
	"CodeNav":         `{ok, content, metadata{operation, file}}`,
	"WebSearch":       `{ok, content, metadata{provider, query, count, truncated}}`,
	"WebFetch":        `{ok, content, metadata{provider, url, truncated}}`,
	"State":           `{ok, content}`,
	"Define":          `{ok, content}`,
	"AskUserQuestion": `{ok, content}`,
}

// modeHints spells out the preconditions of each mode. They exist because the
// engine's own Write description is one sentence long and the mode enum carries
// no semantics; inside a script that silence becomes "always overwrite".
var modeHints = map[string]map[string]string{
	"Write": {
		"create":  "fails if the file already exists",
		"replace": "overwrites the whole file",
		"edit":    "requires old_text (must match exactly once) and base_revision from a prior Read",
	},
}

// serverGroupOf buckets MCP-style names so a large server collapses to one line.
func serverGroupOf(name string) string {
	if idx := strings.Index(name, "__"); idx > 0 {
		return name[:idx]
	}
	return ""
}

// RenderCatalog produces the text placed on the `code` parameter. Tools that do
// not fit are dropped as whole groups and listed by count, never truncated
// mid-line, so the model never sees half a signature.
func RenderCatalog(entries []CatalogTool, budget int) string {
	if budget <= 0 {
		budget = DefaultCatalogBudget
	}
	var b strings.Builder
	omitted := 0

	groups := map[string][]CatalogTool{}
	var order []string
	for _, e := range entries {
		key := e.ServerGroup
		if _, seen := groups[key]; !seen {
			order = append(order, key)
		}
		groups[key] = append(groups[key], e)
	}
	// Internal tools first (they are the common case), then servers by name.
	sort.SliceStable(order, func(i, j int) bool {
		if order[i] == "" {
			return true
		}
		if order[j] == "" {
			return false
		}
		return order[i] < order[j]
	})

	for _, key := range order {
		group := groups[key]
		if key != "" {
			line := renderServerLine(key, group)
			if b.Len()+len(line)+1 > budget {
				omitted += len(group)
				continue
			}
			b.WriteString(line)
			b.WriteString("\n")
			continue
		}
		for _, e := range group {
			lines := renderToolLines(e)
			size := 0
			for _, l := range lines {
				size += len(l) + 1
			}
			if b.Len()+size > budget {
				omitted++
				continue
			}
			for _, l := range lines {
				b.WriteString(l)
				b.WriteString("\n")
			}
		}
	}

	if omitted > 0 {
		fmt.Fprintf(&b, "Omitted %d tools for space. Call tools.describe(name) to see one.\n", omitted)
	}
	return strings.TrimRight(b.String(), "\n")
}

// renderToolLines renders one tool, expanding a mode enum into one line per
// mode. Everything else is a single line.
func renderToolLines(e CatalogTool) []string {
	modes := enumOf(e.Parameters, "mode")
	if len(modes) == 0 {
		return []string{renderSignature(e, "") + " — " + truncate(e.Summary, summaryWidth) + returnSuffix(e)}
	}
	lines := make([]string, 0, len(modes))
	for _, mode := range modes {
		note := ""
		if e.ModeHints != nil {
			note = e.ModeHints[mode]
		}
		line := renderSignature(e, mode) + " — " + truncate(e.Summary, summaryWidth)
		if note != "" {
			line += " (" + mode + ": " + note + ")"
		}
		lines = append(lines, line+returnSuffix(e))
	}
	return lines
}

// renderSignature lists parameter names with a trailing "?" for optional ones.
// Nested objects collapse to "object" and rely on tools.describe.
func renderSignature(e CatalogTool, mode string) string {
	names := make([]string, 0, len(e.Parameters))
	for name := range e.Parameters {
		names = append(names, name)
	}
	sort.Strings(names)

	parts := make([]string, 0, len(names))
	for i, name := range names {
		if i >= maxParamsPerTool {
			parts = append(parts, fmt.Sprintf("…+%d", len(names)-maxParamsPerTool))
			break
		}
		if name == "mode" && mode != "" {
			parts = append(parts, "mode:\""+mode+"\"")
			continue
		}
		if isRequired(e.Parameters, name) {
			parts = append(parts, name)
			continue
		}
		parts = append(parts, name+"?")
	}
	return "tools." + e.Name + "({" + strings.Join(parts, ", ") + "})"
}

func renderServerLine(server string, group []CatalogTool) string {
	desc := ""
	if len(group) > 0 {
		desc = truncate(group[0].Summary, serverWidth)
	}
	return fmt.Sprintf("tools.%s (server, %d tools) — %s [tools.describe(name) for signatures]", server, len(group), desc)
}

func returnSuffix(e CatalogTool) string {
	if e.ReturnHint == "" {
		return ""
	}
	return " → " + e.ReturnHint
}

func enumOf(params map[string]any, key string) []string {
	raw, ok := params[key]
	if !ok {
		return nil
	}
	m, ok := raw.(map[string]any)
	if !ok {
		return nil
	}
	list, ok := m["enum"].([]string)
	if !ok {
		return nil
	}
	return list
}

func isRequired(params map[string]any, key string) bool {
	raw, ok := params[key]
	if !ok {
		return false
	}
	m, ok := raw.(map[string]any)
	if !ok {
		return false
	}
	required, _ := m["required"].(bool)
	return required
}

func truncate(s string, width int) string {
	flat := strings.Join(strings.Fields(s), " ")
	if len(flat) <= width {
		return flat
	}
	return flat[:width-1] + "…"
}
