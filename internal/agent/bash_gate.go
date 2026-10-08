package agent

import (
	"context"
	"path/filepath"

	"github.com/heron-ai/heron-engine/internal/knowledge"
	"github.com/heron-ai/heron-engine/internal/storage"
	"github.com/heron-ai/heron-engine/pkg/types"
)

// Bash is the one builtin tool that reaches the filesystem without going
// through the workspace's path restriction.
//
// Read, Write, Grep, Glob and CodeNav all build a workspace.Restriction from
// ToolPathRestriction(ctx) (internal/tool/path_scope.go) and the workspace
// backend applies it. That filter is what makes knowledge private by location:
// the file tools cannot return .agents/agents/<other>/knowledge/** to a caller
// the path does not name. Run (internal/workspace/workspace.go) is different —
// it is exec.CommandContext(shell, "-c", req.Command) in the workspace root with
// no restriction of any kind, and there is no restriction it *could* apply,
// because the argument is an opaque shell program. Confirmed by reproduction:
//
//	Read   -> err=file not found: .agents/agents/b/knowledge/b.md   (denied)
//	Search -> 0 matches                                            (denied)
//	Bash   -> stdout="AGENT_B_SECRET\n"                            (LEAKED)
//
// and observed in a real session log, where a model whose Grep/Glob/Read
// against .agents/data had just failed reached for Bash instead and read
// another session's state file verbatim.
//
// # Why this is a tool grant decision and not a filter
//
// Analysing the command string is not an option: `cat $(echo ..)`, base64,
// variable concatenation and `find -exec` all defeat it, and a filter that is
// wrong is worse than none because it makes the hole look closed. Sandboxing
// the process (namespaces, chroot, seatbelt) is heavy and cross-platform pain.
// So the decision is made one level up, at the point where the engine decides
// what an agent may call: an agent that has private knowledge to protect does
// not get Bash.
//
// # Scope, deliberately narrow
//
// The gate fires only for an agent that BOTH declares Bash AND has private
// knowledge. An agent with no private knowledge keeps Bash — the risk being
// managed is the engine's own isolation guarantee, so an agent that is not
// party to it has nothing to lose, and denying it would be a regression for
// every existing workspace that uses Bash for ordinary work.

// bashToolName is the builtin tool the gate withholds. Spelled out rather than
// shared with internal/tool (which declares the same string on BashTool.Name)
// because internal/tool imports nothing from this package and the two are
// already coupled only by the string the model sees — the same coupling
// buildToolSchemas' map keys have.
const bashToolName = "Bash"

// privateKnowledgeReason is the refusal the model is given when it asks for a
// withheld Bash. It is written for the *author*, not the model: the model
// cannot act on it, but the message is the only place the configuration
// mistake becomes visible, and it names both ways out.
//
// "Not available to this agent" rather than "denied": a message that
// distinguishes "you were refused" from "there is no such thing" tells the
// model it discovered something it was not supposed to see. The path filter
// takes the same line — a denied Read reports "file not found", not "forbidden"
// (internal/tool/path_scope.go, internal/workspace).
//
// The message is written for the author, not the model: the model cannot act
// on it, but it is the only place the configuration mistake becomes visible,
// and it names both ways out.
const privateKnowledgeReason = "Bash is not available to an Agent that has private knowledge " +
	"(.agents/agents/<id>/knowledge). Bash grants unrestricted filesystem access, which path-scoped " +
	"knowledge isolation cannot restrict, so the two cannot be combined. Move the knowledge to the " +
	"shared .agents/knowledge tree, or remove Bash from this Agent's tools.builtin."

// hasPrivateKnowledge reports whether the agent with id has a private knowledge
// tree with at least one loadable entry, and whether that could be determined.
//
// # The boundary: loadable entries, not directory presence
//
// "Has private knowledge" is defined as: .agents/agents/<id>/knowledge contains
// at least one entry that MarkdownStore.Load would return. Not "the directory
// exists" — an empty directory, or one holding only index.md, protects nothing
// and grants nothing, so withholding Bash for it would trade a real capability
// for an empty guarantee. Load is the right oracle for this because it is the
// same function that decides what R1 makes visible: it skips index.md, skips
// archived entries, and derives a summary where the frontmatter has none. Its
// result is therefore exactly the set of documents a path filter is hiding from
// everyone else — which is precisely the thing Bash could reach.
//
// A consequence worth stating: an agent whose private tree holds only archived
// entries counts as having no private knowledge, because Load returns nothing
// for it, and a caller reading that directory through Grep/Glob cannot see those
// files either. The gate and the filter agree by construction rather than by two
// rules that happen to match.
//
// # Fail closed
//
// An error reading the tree is reported as "has private knowledge" (deny) with
// ok=false so the caller can tell the two apart in a log. The reasoning is the
// same as internal/tool's unknown-caller case: the two possible mistakes are not
// symmetric. Treating an unreadable tree as empty grants Bash in a state the
// engine has never inspected, and the leak that follows has no symptom — it
// looks like ordinary tool output. Treating it as non-empty costs one agent
// access to a tool for the duration of a broken tree, and that *does* have a
// symptom: the refusal message. Fail in the direction that is visible.
//
// # Cost
//
// One Load per call, and the caller decides how often that is. It is a
// directory walk plus a read per file — no model round trip, no subprocess. On
// the schema path it runs once per turn (buildToolSchemas is called once in
// Run), and on the policy path once per tool-call batch that could contain
// Bash. Neither is per-token; both are dwarfed by the model call they precede.
func hasPrivateKnowledge(ctx context.Context, files storage.FileStore, agentID string) (has bool, ok bool) {
	if files == nil || agentID == "" {
		// No store to ask, or nobody to ask about. The empty-agent-id case is
		// the same one ToolPathRestriction treats as "cannot be named": no
		// private tree can be attributed to it, so there is nothing to protect.
		// That is "no", not "unknown" — nothing failed, the caller simply has no
		// private tree by definition.
		return false, true
	}
	store := knowledge.NewMarkdownStore(files, privateKnowledgeDir(agentID))
	if !store.Exists() {
		// The overwhelmingly common case: most agents have no private tree.
		// Not an error, and must not be reported as an unknown — reporting it
		// as one would deny Bash to every agent without private knowledge,
		// which is the over-denial this gate is scoped to avoid.
		return false, true
	}
	entries, err := store.Load(ctx)
	if err != nil {
		// The tree exists and could not be read. See "Fail closed" above.
		return true, false
	}
	return len(entries) > 0, true
}

// privateKnowledgeDir is the agent-private knowledge root R1 defines:
// .agents/agents/<id>/knowledge. It matches the path
// internal/app/runtime.go builds for the per-agent index load and the prefix
// internal/tool/path_scope.go allows back to the owner; the three must agree or
// the gate would withhold Bash for a tree nothing is protecting.
func privateKnowledgeDir(agentID string) string {
	return filepath.Join(".agents", "agents", agentID, "knowledge")
}

// bashWithheld reports whether the Tool/Bash must be withheld from this agent,
// and the reason to report.
//
// It exists as one function because two enforcement points call it — the policy
// layer (which every tool call passes through) and the schema layer (which
// decides what the model is told exists) — and two inline copies of "declares
// Bash AND has private knowledge" would eventually disagree, which is the
// failure mode where the model is not told about a tool it can still call or,
// worse, is told about one the policy layer will refuse.
//
// # The owner id is the SCOPE's, not the definition's
//
// agentID must be resolved the same way ToolPathRestriction resolves it, i.e.
// agentIDOf(agent, req): the request's AgentID wins over the definition's Name.
// The two genuinely differ — a spawned child runs under the target agent's
// definition but carries its own request id — and using Name here would check a
// different directory than the one the path filter is actually protecting. That
// is the fail-open direction: the gate would look at the definition's tree,
// find it empty, and grant Bash while the filter was hiding the request's tree
// from everyone else.
//
// Callers pass it in rather than this function resolving it, because the two
// call sites have different things in hand (the schema path has the request,
// the policy path has the request too) and making the resolution explicit at
// each site is what keeps it from being silently defaulted.
func bashWithheld(ctx context.Context, files storage.FileStore, agent types.AgentConfig, agentID string) (bool, string) {
	if !declaresTool(agent.Tools.Builtin, bashToolName) {
		return false, ""
	}
	has, ok := hasPrivateKnowledge(ctx, files, agentID)
	if !ok {
		// Fail closed: unreadable tree, treated as "has private knowledge".
		// The reason names the read failure explicitly so an operator reading
		// the refusal is not left thinking the tree was empty.
		return true, privateKnowledgeReason + " (the private knowledge tree could not be read, so Bash was withheld rather than granted)"
	}
	if !has {
		return false, ""
	}
	return true, privateKnowledgeReason
}

// declaresTool reports whether name is in a tools.builtin (or custom/mcp) list.
func declaresTool(names []string, name string) bool {
	for _, declared := range names {
		if declared == name {
			return true
		}
	}
	return false
}

// withheldTools returns the tools this agent must not be offered, mapped to the
// reason to report if one is requested anyway. It never returns an error: every
// failure inside it resolves to "withhold", which is the fail-closed direction,
// so there is nothing left for a caller to handle. The error return exists
// purely to keep the call site in Run's shape symmetrical with the rest of that
// function and is always nil.
func (t *TurnLoop) withheldTools(ctx context.Context, agent types.AgentConfig, req types.AgentRequest) (map[string]string, error) {
	if t == nil || t.files == nil {
		// No file store wired: the gate cannot be evaluated, so it cannot
		// withhold. This is the state every TurnLoop built without
		// SetFileStore is in (most tests, and the CLI paths that do not build
		// a knowledge-capable runtime). Granting Bash here rather than denying
		// it is the deliberate choice: a loop with no file store cannot read
		// knowledge at all, so there is no private tree it could be exposing,
		// and denying would break every caller that supplies no store.
		return nil, nil
	}
	withheld, reason := bashWithheld(ctx, t.files, agent, agentIDOf(agent, req))
	if !withheld {
		return nil, nil
	}
	return map[string]string{bashToolName: reason}, nil
}

// knowledgeGateDecision reports whether the call must be refused by the
// knowledge gate, and the reason. denied is false for every call the gate has
// no opinion about, which is all of them except Bash on an agent with private
// knowledge.
//
// It re-evaluates rather than reusing the set Run computed, because it is
// called on paths Run does not own: the async dispatcher and the approval
// resume both run outside the per-turn scope, and the resume case in
// particular can happen long after the schema list was built. The cost is the
// directory walk described on hasPrivateKnowledge — once per tool-call batch
// that reaches here with a Bash call, not once per token.
func (t *TurnLoop) knowledgeGateDecision(ctx context.Context, agent types.AgentConfig, req types.AgentRequest, call types.ToolCall) (ToolDecision, string, bool) {
	if call.Name != bashToolName {
		return ToolAllow, "", false
	}
	if t == nil || t.files == nil {
		// Same reasoning as withheldTools: no store, nothing to protect.
		return ToolAllow, "", false
	}
	withheld, reason := bashWithheld(ctx, t.files, agent, agentIDOf(agent, req))
	if !withheld {
		return ToolAllow, "", false
	}
	return ToolDeny, reason, true
}
