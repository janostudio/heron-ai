package storage

import (
	"fmt"
	"strings"
)

// maxPathSegmentLength bounds a session/agent/task identifier. The values are
// used as directory names under .agents/data, so an unbounded one is also a
// path-length hazard.
const maxPathSegmentLength = 128

// ValidatePathSegment rejects values that cannot safely name a single
// directory component.
//
// The identifiers reaching this function come from JSON-RPC params, HTTP query
// strings, configuration and (through Define) model output. All of them end up
// in filepath.Join as one path element; without this check a value like
// "../../etc" walks out of .agents/data and either writes or reads files
// outside the workspace. filepath.Join cleans ".." segments, it does not reject
// them, so the check has to happen before the join.
//
// The rule is an allowlist rather than a denylist: letters, digits, dot,
// underscore and hyphen. Dot is allowed because existing ids use it
// ("fs_001.v2"), but a value that is entirely dots is rejected — "." and ".."
// are the two segments Clean treats specially.
func ValidatePathSegment(kind, value string) error {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return fmt.Errorf("%s is required", kind)
	}
	if trimmed != value {
		return fmt.Errorf("%s %q must not have surrounding whitespace", kind, value)
	}
	if len(trimmed) > maxPathSegmentLength {
		return fmt.Errorf("%s %q exceeds %d characters", kind, trimmed, maxPathSegmentLength)
	}
	if strings.Trim(trimmed, ".") == "" {
		return fmt.Errorf("%s %q is not a valid path segment", kind, trimmed)
	}
	for _, r := range trimmed {
		switch {
		case r >= 'a' && r <= 'z':
		case r >= 'A' && r <= 'Z':
		case r >= '0' && r <= '9':
		case r == '-' || r == '_' || r == '.':
		default:
			return fmt.Errorf("%s %q contains %q; only letters, digits, '-', '_' and '.' are allowed", kind, trimmed, r)
		}
	}
	return nil
}
