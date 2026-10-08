package workspace

import (
	"path"
	"strings"
)

// Path restriction for the file tools.
//
// The workspace is the single place every file-reading tool passes through:
// Read, Search and Glob all resolve and then walk or open files here, and a
// future tool that uses the same three methods inherits whatever this layer
// enforces. That is why the restriction lives here rather than in
// internal/tool. Three tools filtering their own results would mean three
// copies of one rule, and the copy that drifts is the one that leaks.
//
// The workspace itself stays ignorant of *why* a path is forbidden. It is a
// filesystem backend, and caller identity is not its business — it takes a
// restriction and applies it. Deciding which restriction applies to a caller
// is done once, in internal/tool, from the scope in the context.
//
// # Prefix semantics
//
// A restricted path is a path *prefix* matched on whole segments, relative to
// the workspace root, slash-separated. Denying ".agents/agents/b/knowledge"
// denies that directory and everything under it, and does NOT deny a sibling
// whose name merely starts with the same characters:
//
//	deny .agents/agents/b/knowledge
//	  .agents/agents/b/knowledge/private.md   denied
//	  .agents/agents/b/knowledge/sub/x.md     denied
//	  .agents/agents/b/knowledgeable/x.md     ALLOWED
//	  .agents/agents/b-knowledge/x.md         ALLOWED
//
// The last two rows are the whole reason for a segment-aware comparison.
// strings.HasPrefix(".agents/agents/b-knowledge/x.md", ".agents/agents/b/knowledge")
// is false, but HasPrefix(".agents/agents/b/knowledgeable/x.md",
// ".agents/agents/b/knowledge") is TRUE — a naive prefix test would silently
// drop an unrelated directory. Silently, because denial produces an empty
// result rather than an error, so the symptom would be a file the model
// cannot find rather than a failure anyone can see.
//
// # Allow lists, and why both are needed
//
// A deny list alone cannot express the rule the file tools actually need:
// "every agent's private knowledge except my own". The owners of private trees
// are directory names, and a deny list built by enumerating them would depend
// on a directory listing succeeding — a listing that failed would deny nobody,
// which is the wrong direction for a security rule — and would miss an owner
// created after the list was built.
//
// So the restriction carries both sides: Denied is applied first, and Allowed
// then re-admits specific subtrees. The file tools deny the whole
// .agents/agents (or .agents/teams) directory and allow the caller's own
// knowledge prefix back. The rule is expressed without reading the filesystem,
// and it cannot silently widen: a path is reachable only if it is inside an
// allowed prefix or is not inside any denied one.
//
// Allow wins over deny. That is the only ordering that makes the two-list
// shape useful — deny-first would make every allow entry unreachable whenever
// it sits under a denied parent, which is precisely the case it exists for.

// Restriction is the path restriction carried by a file-tool request. It is a
// field on the request structs rather than a method or a Workspace constructor
// argument, because the answer depends on *who is calling*, which varies per
// call while a Workspace is built once per process.
//
// It is a plain value type with exported fields, so adding the restriction
// changed no interface signature — Read/Search/Glob keep their shape and
// existing callers keep compiling with a zero (unrestricted) value. That is
// what makes the restriction additive: a caller with no opinion gets the old
// behaviour and does not have to be found and updated.
type Restriction struct {
	// Denied paths are unreachable, as are everything beneath them.
	Denied []string
	// Allowed paths are reachable even when a Denied entry is their
	// ancestor. Checked first.
	Allowed []string
}

// permits reports whether the workspace-relative path may be read by the
// caller this restriction describes. relative must be slash-separated.
//
// # Why an allowed path also un-denies its ancestors
//
// The rule the file tools need is "deny .agents/agents, except
// .agents/agents/<me>/knowledge". Applying allow-wins literally to each path
// is not enough, because a walk sees the *directory* .agents/agents before it
// sees anything inside it: that path matches no allowed prefix, so it is
// denied, and the walk prunes it — never reaching the allowed subtree the
// allow entry exists to expose. The allow entry would be dead.
//
// So a path is reachable if it is under an Allowed prefix (the entry works)
// OR if an Allowed prefix is under *it* (it is an ancestor on the way to an
// entry, and must stay traversable). The second clause is what lets a walk
// descend through a denied directory to reach the one tree it may read. It
// only ever makes the walk visit more directories; it does not on its own
// re-admit a file, because a file is never an ancestor of anything.
func (r Restriction) permits(relative string) bool {
	cleaned := cleanRelative(relative)
	if cleaned == "" {
		// The workspace root itself, or a path that cleaned away to
		// nothing. Not a file anyone reads; treat as permitted so a
		// walk's own root is not pruned.
		return true
	}
	if matchesAnyPrefix(cleaned, r.Allowed) {
		return true
	}
	if isPrefixOfAllowed(cleaned, r.Allowed) {
		return true
	}
	return !matchesAnyPrefix(cleaned, r.Denied)
}

// matchesAnyPrefix reports whether relative is one of the prefixes or sits
// underneath one.
func matchesAnyPrefix(relative string, prefixes []string) bool {
	for _, prefix := range prefixes {
		candidate := cleanRelative(prefix)
		if candidate == "" {
			// An empty prefix is ignored, never read as a wildcard. Prefixes
			// are built by joining ids into paths, and an id that came back
			// empty through some upstream failure produces exactly this
			// value; treating it as "match everything" would turn a missing
			// id into a blanket denial of the whole workspace, which is a far
			// worse failure than ignoring it.
			continue
		}
		if relative == candidate || strings.HasPrefix(relative, candidate+"/") {
			return true
		}
	}
	return false
}

// isPrefixOfAllowed reports whether an Allowed entry lives under relative —
// i.e. relative is a directory on the way to something allowed and must not be
// pruned.
func isPrefixOfAllowed(relative string, allowed []string) bool {
	for _, prefix := range allowed {
		candidate := cleanRelative(prefix)
		if candidate == "" {
			continue
		}
		if strings.HasPrefix(candidate, relative+"/") {
			return true
		}
	}
	return false
}

// cleanRelative normalizes a slash-separated workspace-relative path for
// prefix comparison: forward slashes, no "." segments, no trailing slash.
//
// It deliberately does not resolve "..". Paths reaching it have already been
// through localWorkspace.resolve (or its SSH equivalent), which rejects
// anything escaping the root, so a ".." here would be a bug upstream rather
// than a path to interpret — and interpreting it could only widen access.
func cleanRelative(relative string) string {
	cleaned := path.Clean(strings.ReplaceAll(relative, `\`, "/"))
	if cleaned == "." || cleaned == "/" {
		return ""
	}
	return strings.TrimPrefix(cleaned, "./")
}
