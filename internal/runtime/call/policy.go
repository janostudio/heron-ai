package call

import (
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/heron-ai/heron-engine/pkg/types"
)

// commandEnv builds the environment for a command call: os.Environ() plus the
// call's configured env and the idempotency key. Values are template-expanded
// against the call's session identity and its input SharedRecords.
//
// It returns an error when a template references a record or field that does
// not exist. A missing reference is a configuration mistake (or a record the
// call did not declare as input); substituting an empty string would let the
// command run with a silently wrong value — exactly the failure mode this
// expansion exists to prevent.
func commandEnv(req types.CallRequest) ([]string, error) {
	env := make([]string, 0, len(req.Call.Command.Env)+1)
	for name, value := range req.Call.Command.Env {
		expanded, err := resolveTemplate(value, req)
		if err != nil {
			return nil, fmt.Errorf("env %s: %w", name, err)
		}
		env = append(env, name+"="+expanded)
	}
	if key := req.Call.Command.IdempotencyKey; key != "" {
		expanded, err := resolveTemplate(key, req)
		if err != nil {
			return nil, fmt.Errorf("idempotency_key: %w", err)
		}
		env = append(env, "HERON_IDEMPOTENCY_KEY="+expanded)
	}
	return append(os.Environ(), env...), nil
}

// resolveCommandArgs template-expands each configured argument.
func resolveCommandArgs(req types.CallRequest) ([]string, error) {
	args := req.Call.Command.Args
	if len(args) == 0 {
		return nil, nil
	}
	out := make([]string, 0, len(args))
	for i, arg := range args {
		expanded, err := resolveTemplate(arg, req)
		if err != nil {
			return nil, fmt.Errorf("args[%d]: %w", i, err)
		}
		out = append(out, expanded)
	}
	return out, nil
}

// resolveTemplate replaces ${...} placeholders in value. It supports the fixed
// session-identity placeholders (${flow_session_id} and friends) and record
// references of the form ${records.<Name>.Summary},
// ${records.<Name>.Data.<field>}, or the shorthand ${records.<Name>} for the
// record's Summary.
//
// A record reference that cannot be resolved is an error, never an empty
// string: see commandEnv for why.
func resolveTemplate(value string, req types.CallRequest) (string, error) {
	replacements := map[string]string{
		"${flow_session_id}": req.FlowSession.ID,
		"${flow_turn_id}":    req.FlowTurn.ID,
		"${team_id}":         req.TeamTurn.TeamID,
		"${team_turn_id}":    req.TeamTurn.ID,
		"${call_id}":         req.Call.ID,
		"${call_turn_id}":    req.CallTurnID,
		"${attempt}":         strconv.Itoa(req.Attempt),
		"${recovery_of}":     req.RecoveryOf,
	}
	for placeholder, replacement := range replacements {
		value = strings.ReplaceAll(value, placeholder, replacement)
	}

	value, err := expandRecordTemplates(value, req.Records)
	if err != nil {
		return "", err
	}
	return value, nil
}

// maxRecordTemplateExpansions bounds one expansion pass. A record's Summary and
// Data are model-authored, so they can contain a ${records....} placeholder of
// their own: "${records.A}" whose summary is literally "${records.A}" expands to
// itself forever. The bound turns that (and a two-record cycle) into a
// configuration error instead of a goroutine pinned at 100% CPU that no context
// cancellation can reach.
const maxRecordTemplateExpansions = 64

// expandRecordTemplates resolves every ${records....} placeholder against the
// given records.
func expandRecordTemplates(value string, records []types.SharedRecord) (string, error) {
	for expansions := 0; ; expansions++ {
		if expansions >= maxRecordTemplateExpansions {
			return "", fmt.Errorf("record placeholders did not converge after %d expansions: %q", maxRecordTemplateExpansions, value)
		}
		start := strings.Index(value, "${records.")
		if start < 0 {
			return value, nil
		}
		end := strings.IndexByte(value[start:], '}')
		if end < 0 {
			return "", fmt.Errorf("unterminated record placeholder in %q", value)
		}
		end += start
		placeholder := value[start : end+1]
		resolved, err := resolveRecordPlaceholder(placeholder, records)
		if err != nil {
			return "", err
		}
		expanded := value[:start] + resolved + value[end+1:]
		if expanded == value {
			// The placeholder resolved to itself: the record echoes the template
			// it was substituted into. No number of retries changes the answer.
			return "", fmt.Errorf("record placeholder %q resolves to itself", placeholder)
		}
		value = expanded
	}
}

// resolveRecordPlaceholder resolves one ${records....} placeholder.
func resolveRecordPlaceholder(placeholder string, records []types.SharedRecord) (string, error) {
	// Strip "${records." prefix and the trailing "}".
	inner := strings.TrimSuffix(strings.TrimPrefix(placeholder, "${records."), "}")
	parts := strings.Split(inner, ".")
	if len(parts) == 0 || strings.TrimSpace(parts[0]) == "" {
		return "", fmt.Errorf("malformed record placeholder %q", placeholder)
	}
	name := parts[0]

	record, ok := findRecord(records, name)
	if !ok {
		return "", fmt.Errorf("record %q not found among call inputs", name)
	}

	switch len(parts) {
	case 1:
		// ${records.Name} -> Summary
		return record.Summary, nil
	case 2:
		switch parts[1] {
		case "Summary":
			return record.Summary, nil
		case "RecordID":
			return record.RecordID, nil
		case "Name":
			return record.Name, nil
		default:
			return "", fmt.Errorf("record %q has no field %q (use Data.<field> for data fields)", name, parts[1])
		}
	case 3:
		if parts[1] != "Data" {
			return "", fmt.Errorf("record placeholder %q: expected %s.Summary or %s.Data.<field>", placeholder, name, name)
		}
		raw, ok := record.Data[parts[2]]
		if !ok {
			return "", fmt.Errorf("record %q has no data field %q", name, parts[2])
		}
		return stringifyRecordValue(raw), nil
	default:
		return "", fmt.Errorf("record placeholder %q is too deep (expected <Name>.Data.<field>)", placeholder)
	}
}

// findRecord returns the record with the given name. The last match wins so a
// record republished later in the turn supersedes an earlier one.
func findRecord(records []types.SharedRecord, name string) (types.SharedRecord, bool) {
	var found types.SharedRecord
	ok := false
	for _, record := range records {
		if record.Name == name {
			found = record
			ok = true
		}
	}
	return found, ok
}

// stringifyRecordValue renders a record data value for use in an env var or
// argv element. Non-scalar values are rendered as their JSON encoding so the
// result is still a single shell-safe token.
func stringifyRecordValue(v any) string {
	switch typed := v.(type) {
	case nil:
		return ""
	case string:
		return typed
	case bool:
		return strconv.FormatBool(typed)
	case float64:
		// JSON numbers arrive as float64; avoid a trailing ".0" for integers.
		if typed == float64(int64(typed)) {
			return strconv.FormatInt(int64(typed), 10)
		}
		return strconv.FormatFloat(typed, 'f', -1, 64)
	case int:
		return strconv.Itoa(typed)
	case int64:
		return strconv.FormatInt(typed, 10)
	default:
		// Non-scalar values (maps, slices) are rendered as their JSON
		// encoding so the consumer receives the value in its original
		// shape. fmt.Sprint would emit Go's debug format (e.g.
		// "map[a:1]"), which no consumer (shell/jq) can parse.
		encoded, err := json.Marshal(typed)
		if err != nil {
			return fmt.Sprint(typed)
		}
		return string(encoded)
	}
}
