package prompt

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/heron-ai/heron-engine/pkg/types"
)

// Provenance rendering.
//
// The model has to be able to tell who produced each piece of context. That
// information lives in the protocol (role + OpenAI's `name`), never in prose:
// a text prefix such as "[owner]" costs tokens on every entry and has to be
// taught to the model. What this file decides is which message each block
// becomes.
//
//	records / mailbox / mailbox_result -> assistant + name=<producing team>
//	team_state / agent_state           -> assistant, no name (the agent's own notes)
//	knowledge                          -> user, wrapped in <system-reminder>
//	input / fanout_item / responsibility -> the trailing user message
const (
	systemReminderOpen  = "<system-reminder>"
	systemReminderClose = "</system-reminder>"

	// dataLineLimit caps a flattened Data line. A business record can carry a
	// large map; the prompt only needs enough of it to reason about.
	dataLineLimit = 500
	// dataDepthLimit stops nested maps from expanding without bound.
	dataDepthLimit = 3
	// dataListLimit caps array elements rendered per key.
	dataListLimit = 5
)

func isCollaboratorKind(kind string) bool {
	switch kind {
	case "records", "mailbox", "mailbox_result":
		return true
	}
	return false
}

func isMemoryKind(kind string) bool {
	return kind == "team_state" || kind == "agent_state"
}

// wrapSystemReminder marks a block as context supplied by the harness rather
// than something the user or a collaborator said. It labels the channel, not a
// speaker, which is why it does not conflict with the no-text-prefix rule.
func wrapSystemReminder(text string) string {
	return systemReminderOpen + "\n" + strings.TrimSpace(text) + "\n" + systemReminderClose
}

// formatRecordData renders record.Data as a single line of key=value pairs.
// JSON structure is deliberately avoided: a map rendered with braces reads like
// a quotable conclusion, and models have echoed it verbatim as their answer.
func formatRecordData(data map[string]any) string {
	if len(data) == 0 {
		return ""
	}
	pairs := make([]string, 0, len(data))
	flattenData(&pairs, "", data, 0)
	line := strings.Join(pairs, " ")
	if len(line) > dataLineLimit {
		line = truncateRunes(line, dataLineLimit) + " …"
	}
	return line
}

func flattenData(pairs *[]string, prefix string, value any, depth int) {
	switch typed := value.(type) {
	case map[string]any:
		if depth >= dataDepthLimit {
			*pairs = append(*pairs, prefix+"=…")
			return
		}
		keys := make([]string, 0, len(typed))
		for key := range typed {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			flattenData(pairs, joinKey(prefix, key), typed[key], depth+1)
		}
	case map[string]string:
		if depth >= dataDepthLimit {
			*pairs = append(*pairs, prefix+"=…")
			return
		}
		keys := make([]string, 0, len(typed))
		for key := range typed {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			*pairs = append(*pairs, joinKey(prefix, key)+"="+quoteValue(typed[key]))
		}
	case []any:
		parts := make([]string, 0, len(typed))
		for i, item := range typed {
			if i >= dataListLimit {
				parts = append(parts, "…")
				break
			}
			parts = append(parts, quoteValue(scalarText(item)))
		}
		*pairs = append(*pairs, prefix+"="+strings.Join(parts, ","))
	case []string:
		parts := make([]string, 0, len(typed))
		for i, item := range typed {
			if i >= dataListLimit {
				parts = append(parts, "…")
				break
			}
			parts = append(parts, quoteValue(item))
		}
		*pairs = append(*pairs, prefix+"="+strings.Join(parts, ","))
	case nil:
		*pairs = append(*pairs, prefix+"=")
	default:
		*pairs = append(*pairs, prefix+"="+quoteValue(scalarText(typed)))
	}
}

func joinKey(prefix, key string) string {
	if prefix == "" {
		return key
	}
	return prefix + "." + key
}

func scalarText(value any) string {
	if value == nil {
		return ""
	}
	if text, ok := value.(string); ok {
		return text
	}
	return fmt.Sprintf("%v", value)
}

// quoteValue keeps a value single-token so the key=value line stays parseable.
func quoteValue(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return "''"
	}
	if strings.ContainsAny(value, " ='\n\t") {
		return "'" + strings.ReplaceAll(value, "'", "’") + "'"
	}
	return value
}

func truncateRunes(text string, limit int) string {
	runes := []rune(text)
	if len(runes) <= limit {
		return text
	}
	return string(runes[:limit])
}

// provenanceGroup is everything one producing team contributed to this turn.
type provenanceGroup struct {
	source string
	text   string
}

// collaboratorGroups folds the provenance-bearing blocks into one entry per
// producing team, preserving first-appearance order.
//
// A result delivered through the mailbox and the record it produced describe
// the same thing. The record wins: it is structured, and a mailbox copy
// degrades to a placeholder when the payload lacks a rendered reply. Dispatch
// instructions (work messages) are kept either way — they are what this Team
// was asked to do, and no record carries them.
func collaboratorGroups(blocks []types.ContextBlock) []provenanceGroup {
	type bucket struct {
		records []types.SharedRecord
		work    []string
		results []string
	}
	var order []string
	buckets := make(map[string]*bucket, len(blocks))
	get := func(source string) *bucket {
		if existing, ok := buckets[source]; ok {
			return existing
		}
		created := &bucket{}
		buckets[source] = created
		order = append(order, source)
		return created
	}

	for _, block := range blocks {
		if !isCollaboratorKind(block.Kind) {
			continue
		}
		target := get(strings.TrimSpace(block.Source))
		switch block.Kind {
		case "records":
			var records []types.SharedRecord
			if err := json.Unmarshal([]byte(block.Text), &records); err != nil {
				return nil
			}
			target.records = append(target.records, records...)
		case "mailbox_result":
			if text := strings.TrimSpace(block.Text); text != "" {
				target.results = append(target.results, text)
			}
		default: // "mailbox": work instructions
			if text := strings.TrimSpace(block.Text); text != "" {
				target.work = append(target.work, text)
			}
		}
	}

	groups := make([]provenanceGroup, 0, len(order))
	for _, source := range order {
		current := buckets[source]
		parts := make([]string, 0, 3)
		if text := recordsText(current.records); text != "" {
			parts = append(parts, text)
		}
		parts = append(parts, current.work...)
		if len(current.records) == 0 {
			parts = append(parts, current.results...)
		}
		text := strings.Join(parts, "\n\n")
		if text == "" {
			continue
		}
		groups = append(groups, provenanceGroup{source: source, text: text})
	}
	return groups
}

// recordsText renders one source's records without type headers: no
// "## Shared Records", no "### Name (kind)". The message already carries the
// producer; repeating the record's internal type adds noise, not information.
func recordsText(records []types.SharedRecord) string {
	lines := make([]string, 0, len(records))
	for _, record := range records {
		parts := make([]string, 0, 3)
		if summary := strings.TrimSpace(record.Summary); summary != "" {
			parts = append(parts, summary)
		}
		if data := formatRecordData(record.Data); data != "" {
			parts = append(parts, data)
		}
		if len(record.Basis) > 0 {
			paths := make([]string, 0, len(record.Basis))
			for _, basis := range record.Basis {
				if strings.TrimSpace(basis.Path) == "" {
					continue
				}
				entry := basis.Path
				if len(basis.Lines) > 0 {
					entry += fmt.Sprintf(":%d", basis.Lines[0])
				}
				paths = append(paths, entry)
			}
			if len(paths) > 0 {
				parts = append(parts, "basis="+strings.Join(paths, ","))
			}
		}
		if len(parts) == 0 {
			continue
		}
		lines = append(lines, strings.Join(parts, "\n"))
	}
	return strings.Join(lines, "\n\n")
}
