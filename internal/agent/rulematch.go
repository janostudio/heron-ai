package agent

import (
	"path"
	"strings"
)

// rulePathMatches reports whether one touched path satisfies one rule pattern.
//
// Three shapes, because the config is written by hand and each says something
// different:
//
//   - "docs/**" — everything under docs/, at any depth. Go's path.Match cannot
//     express this (its "*" does not cross "/"), which is why the suffix form is
//     handled as a prefix comparison rather than a glob.
//   - "docs/*" — one level under docs/.
//   - "docs/README.md" — that one file.
//
// Both sides are normalised first: config patterns and workspace paths arrive
// with different prefixes ("../docs", "/docs", "./docs"), and a rule that fails
// to match because of a leading "./" is a rule that silently never applies.
func rulePathMatches(pattern, target string) bool {
	pattern = normalizeRulePath(pattern)
	target = normalizeRulePath(target)
	if pattern == "" || target == "" {
		return false
	}
	if strings.HasSuffix(pattern, "/**") {
		base := strings.TrimSuffix(pattern, "/**")
		if base == "" || base == "." {
			return true
		}
		// The directory itself counts too: a tool that lists docs/ is working on
		// docs/, even though no file under it was named.
		return target == base || strings.HasPrefix(target, base+"/")
	}
	if matched, err := path.Match(pattern, target); err == nil && matched {
		return true
	}
	return false
}

// normalizeRulePath strips the shapes that mean "same place" but compare
// differently: leading "./", leading "/", trailing "/", and Windows separators.
// ".." is deliberately not collapsed — the workspace root is the reference, and
// a pattern that climbs out of it is a configuration mistake the loader reports,
// not something to silently reinterpret here.
func normalizeRulePath(value string) string {
	value = strings.TrimSpace(value)
	value = strings.ReplaceAll(value, "\\", "/")
	value = strings.TrimPrefix(value, "./")
	value = strings.TrimPrefix(value, "/")
	return strings.TrimSuffix(value, "/")
}

// firstMatchPath returns the first touched path that satisfies any of the rule's
// patterns, so the injected note can say what triggered it. Empty means no match.
func firstMatchPath(patterns, touched []string) string {
	for _, candidate := range touched {
		for _, pattern := range patterns {
			if rulePathMatches(pattern, candidate) {
				return normalizeRulePath(candidate)
			}
		}
	}
	return ""
}
