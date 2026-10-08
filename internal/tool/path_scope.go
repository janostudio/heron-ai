package tool

import (
	"context"
	"path"

	"github.com/heron-ai/heron-engine/internal/workspace"
	"github.com/heron-ai/heron-engine/pkg/types"
)

// Path visibility for the file tools.
//
// Two disjoint things are hidden here, and naming the helper after either one
// of them would mislead the next reader, which is why it is not called
// KnowledgeRestriction any more:
//
//  1. Knowledge is private by *location*: a file under
//     .agents/agents/<id>/knowledge belongs to agent <id>, one under
//     .agents/teams/<id>/knowledge to team <id>, and .agents/knowledge is
//     visible to everyone (design doc skill-progressive-disclosure §3.4, and
//     the locationScope convention in internal/knowledge/store.go). The
//     in-memory index enforces that by refusing to return an entry to a caller
//     the location does not name — but the index is no longer the only way to
//     read knowledge. Since retrieval moved to agentic search, the model
//     reaches knowledge through Grep/Glob/Read/Write, which never consult the
//     index and, before this file existed, happily returned
//     `.agents/agents/other-agent/knowledge/private.md` to anyone.
//
//  2. .agents/data is the engine's own runtime session data and is closed to
//     every caller, owner or not — see dataDirectory below.
//
// The rule is one helper because the file tools must not be able to disagree
// about what a caller may touch: Read, Grep, Glob, CodeNav and Write all call
// it, and the workspace layer enforces whatever it is given, so a further tool
// that goes through the same methods inherits the guarantee by calling the
// same helper.
//
// "Touch" and not "read" is deliberate: since batch R3 the same restriction
// is carried by WriteRequest, so a caller can no more create a file under
// another agent's private knowledge than read one there.

// knowledgeOwnerDirs are the directories whose children own private knowledge
// trees: .agents/agents/<id>/knowledge and .agents/teams/<id>/knowledge. The
// names match knowledgeLocationRoots in internal/knowledge/store.go; the two
// must agree, or the index and the path filter would disagree about which
// files are private knowledge.
var knowledgeOwnerDirs = []string{"agents", "teams"}

// ToolPathRestriction returns the path restriction the caller of ctx must have
// applied to its file-tool requests.
//
// The shape is a deny of each owner directory plus an allow of the caller's
// own knowledge prefix, combined with an unconditional deny of the engine's
// runtime data directory. Four identity cases, and the two middle ones are the
// reason the restriction carries both sides:
//
//	caller              denied                    allowed
//	agent A in team T   .agents/agents            .agents/agents/A/knowledge
//	                    .agents/teams             .agents/teams/T/knowledge
//	                    .agents/data              —
//	agent A, no team    .agents/agents            .agents/agents/A/knowledge
//	                    .agents/teams             —
//	                    .agents/data              —
//	no identity         .agents/agents            —
//	                    .agents/teams             —
//	                    .agents/data              —
//	nothing private     .agents/data              —
//
// The last row is not a real state today (the engine always publishes a
// scope), which is why the fail-closed row above it exists instead: an
// unknown caller gets every private tree denied rather than none.
//
// # Why deny a directory and allow back, rather than deny other owners
//
// Denying "every owner except me" by listing .agents/agents/* would make the
// guarantee depend on a directory listing succeeding: a failed stat would deny
// nobody, which is the wrong direction for a rule whose entire job is to
// withhold. It would also miss an owner directory created after the list was
// built. Denying the parent needs no listing and cannot go stale, and the
// allow entry restores exactly the one tree the caller is entitled to.
//
// # Unknown callers fail closed
//
// With no scope — a tool executed outside an AgentTurn — the identity is
// unknown, so every private tree is denied and only the shared
// .agents/knowledge stays readable. Denying nothing would make the guarantee
// conditional on the identity surviving the whole call chain: any path that
// loses the context, or any future entry point that builds a tool executor
// without going through the TurnLoop, would silently start handing out other
// agents' knowledge, and there is no symptom to notice because the results
// look exactly like ordinary ones. A denial surfaces as "the file is not
// there" and gets investigated; a leak does not surface at all.
//
// The cost is real but currently nil: every production caller reaches these
// tools through the TurnLoop, which always publishes a scope, and the direct
// callers in tests read only ordinary files. A future non-agent caller that
// genuinely needs a private tree should be given a scope rather than an
// exemption here — the exemption is what would rot, because nothing fails when
// it becomes wrong.
//
// This is a path filter, not a permission gate: it does not read the agent's
// declared `knowledge:` allowlist, because visibility is a property of where a
// file lives. A file in the shared directory is readable by everyone, and one
// in a private directory only by its owner — which is what a path filter can
// express and a frontmatter field cannot (see reconcileScope in
// internal/knowledge/store.go for why the two were converged).
func ToolPathRestriction(ctx context.Context) workspace.Restriction {
	scope, _ := types.ToolScopeFromContext(ctx)
	// No scope and no owner id are the same situation for this filter: the
	// caller cannot be named, so nothing private can be attributed to it. An
	// empty AgentID with a present scope happens when a turn ran without a
	// resolvable agent id — treated identically rather than assumed safe.
	ownerIDs := map[string]string{
		"agents": scope.AgentID,
		"teams":  scope.TeamID,
	}

	// Capacity is one more than the owner directories: the data directory
	// below appends an entry that is not an owner tree and gets no allow.
	restriction := workspace.Restriction{Denied: make([]string, 0, len(knowledgeOwnerDirs)+1)}
	for _, ownerDir := range knowledgeOwnerDirs {
		restriction.Denied = append(restriction.Denied, ownerDirectory(ownerDir))
		if ownerID := ownerIDs[ownerDir]; ownerID != "" {
			restriction.Allowed = append(restriction.Allowed, knowledgePrefixOf(ownerDir, ownerID))
		}
	}
	// Unconditional: no identity is consulted and no allow entry is added,
	// because no caller is entitled to it. Appended after the knowledge
	// denies so the two groups stay visually separate — one is a rule about
	// ownership, the other is a rule about the path being the engine's own.
	restriction.Denied = append(restriction.Denied, dataDirectory)
	return restriction
}

// dataDirectory is the engine's runtime session data: .agents/data/sessions/
// <id>/{flow,team,agent}.jsonl plus the per-caller state.md files. It is a
// sibling of the knowledge trees under .agents, and nothing about that
// location says "engine internal", so the file tools reach it by default —
// **/*.md finds the state files and Grep finds the raw model output in
// agent.jsonl.
//
// It is denied for every caller, the owner of a session included, for three
// separate reasons:
//
//   - It is not workspace content. The workspace is what the agent is asked
//     to work on; this is the record of the agent doing the work. Reading it
//     is a category error even when it happens to be readable.
//   - agent.jsonl holds raw model output and state.md holds the verbatim
//     todo state, so the directory is a complete transcript — including of
//     *other sessions*, which is a boundary nobody has defined: there is no
//     rule that says a turn may see the transcript of a session it is not
//     part of, and inventing one by accident is how leakage ships.
//   - The agent does not need it. The current turn's context is already in
//     its messages, and everything else it needs it can ask for.
//
// Writes are denied along with reads. The symmetry is not automatic here —
// the restriction is one value applied by both paths — which is exactly why
// it matters: an open write would let a turn append to the engine's own
// event log and forge the record it is being audited by.
//
// The engine's own writers are untouched by this: they go through
// storage.FileStore (see internal/state, internal/storage), which knows
// nothing about workspace requests, so this filter only ever sees the
// directory through an agent's tool call.
const dataDirectory = ".agents/data"

// ownerDirectory is ".agents/<ownerDir>" — the directory whose children are
// knowledge owners. This is the prefix the deny list uses, and it must NOT be
// confused with knowledgePrefix below.
//
// The distinction is the whole correctness of the filter:
//
//	.agents/agents              children are owners (a, b, …)   ← denied
//	.agents/agents/knowledge    a global-style tree named
//	                            "knowledge" that happens to sit
//	                            inside agents/ — NOT an owner
//	                            directory, and denying it would
//	                            not hide any owner's tree
//
// Denying ".agents/agents/knowledge" instead of ".agents/agents" looks right
// and is silently useless: no owner's private file lives under it, so the
// filter denies nothing while appearing to be configured.
func ownerDirectory(ownerDir string) string {
	return path.Join(".agents", ownerDir)
}

// knowledgePrefixOf builds ".agents/<ownerDir>/<ownerID>/knowledge", the
// caller's own private knowledge tree.
func knowledgePrefixOf(ownerDir, ownerID string) string {
	return path.Join(".agents", ownerDir, ownerID, "knowledge")
}
