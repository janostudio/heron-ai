package knowledge

import (
	"context"
	"path"
	"strings"

	"github.com/heron-ai/heron-engine/internal/storage"
)

// KnowledgePointer answers "where may this agent look for knowledge?"
//
// It replaces the per-query injector. Retrieval is agentic search now: the
// model greps the knowledge trees itself with Grep/Glob/Read, and the path
// filter in internal/tool/path_scope.go enforces who may see which tree
// (design doc skill-progressive-disclosure §3.4.1). What the model cannot
// discover on its own is *that a knowledge base exists and where it lives* —
// a workspace with no knowledge directory looks exactly like one whose
// knowledge the model simply did not think to search. So the engine's whole
// remaining job at prompt time is to name the readable directories and say
// they are searched, not read.
//
// # Why this is a fixed block and not a retrieval result
//
// The block this replaces was `- <title> [<id>]: <summary>` per matching
// entry, rebuilt every turn from the query. That had two costs that no
// implementation could remove:
//
//   - it grew with the corpus (a hundred entries meant a hundred lines in
//     every prompt, whether or not any was relevant), and
//   - it varied with the query, so it could never be a stable prefix and the
//     provider's prompt cache missed on it every turn.
//
// The pointer block is constant for a given agent: it mentions directories,
// never entries. Zero tokens grow with the knowledge base, and being
// query-independent is what lets the caller mark it `stable` and keep it in
// the cached prefix across turns.
//
// # The directory list is a path-filter restatement
//
// The private directories it names are exactly the ones the agent's
// Grep/Glob/Read can reach: .agents/knowledge for everyone, plus
// .agents/agents/<self>/knowledge and .agents/teams/<team>/knowledge, each
// only while it holds at least one loadable entry. Naming a directory the
// path filter denies would be worse than naming none: the model would grep
// it, get "no matches", and conclude the knowledge base is empty rather than
// that it is not its own.
//
// The entry requirement is the same rule internal/agent's Bash gate uses
// (hasPrivateKnowledge): an empty directory, or one holding only index.md or
// archived entries, protects nothing, grants nothing, and is not a place the
// model can find anything. It is therefore not advertised. Both sides read
// MarkdownStore.Load, so the gate and the pointer agree by construction.
type KnowledgePointer struct {
	files   storage.FileStore
	global  string
	private string
}

// NewKnowledgePointer builds a pointer over the same file store the knowledge
// trees live in. global and private are the two roots the engine derives from
// its workspace and config roots — ".agents/knowledge" and ".agents" in
// production (see internal/app). They are parameters rather than literals
// because the app may hold an absolute config root, and a block that named
// the wrong path would be an instruction to search the wrong tree.
func NewKnowledgePointer(files storage.FileStore, global, privateRoot string) *KnowledgePointer {
	return &KnowledgePointer{files: files, global: global, private: privateRoot}
}

// agentInputs is the resolved set of readable knowledge trees for one caller:
// the directories Text renders, plus whether any of them is a private tree
// (which is what decides the extra "these are yours" sentence).
type agentInputs struct {
	globalDir  string
	agentDir   string
	teamDir    string
	hasPrivate bool
}

// inputsFor resolves which of the three trees this caller may read.
//
// .agents/knowledge is named unconditionally and the two private trees only
// when they hold entries. The asymmetry is deliberate and is the R1 rule
// restated as prompt text: the shared tree is readable by everyone, so every
// caller "may see" it, while a private tree is readable only by its owner —
// and only worth naming when it actually holds something.
//
// Errors from a private probe are recorded as "has entries" — see hasEntries
// for why that direction is the safe one at a prompt-building call site.
//
// The shared tree is NOT probed, deliberately: see Text.
func (p *KnowledgePointer) inputsFor(ctx context.Context, agentID, teamID string) agentInputs {
	inputs := agentInputs{globalDir: p.global}
	if agentID != "" {
		dir := path.Join(p.private, knowledgeLocationRoots.Agents, agentID, knowledgeLocationRoots.Global)
		if hasEntries(ctx, p.files, dir) {
			inputs.agentDir = dir
			inputs.hasPrivate = true
		}
	}
	if teamID != "" {
		dir := path.Join(p.private, knowledgeLocationRoots.Teams, teamID, knowledgeLocationRoots.Global)
		if hasEntries(ctx, p.files, dir) {
			inputs.teamDir = dir
			inputs.hasPrivate = true
		}
	}
	return inputs
}

// Text renders the pointer block for one caller, or "" when the pointer is
// not configured.
//
// The empty return is a real outcome, not an error path: a TurnLoop built
// without a file store (most tests, and the CLI paths that build no
// knowledge-capable runtime) has no knowledge to point at, and a block
// reading "search .agents/knowledge/" there would cost tokens every turn to
// send the model at a tree the engine cannot even read.
//
// The block is otherwise unconditional, including for a workspace with no
// knowledge files yet. .agents/knowledge/ is readable by everyone regardless
// of whether it currently holds anything, and naming it is what lets the
// model tell "this workspace has a knowledge area and it is empty" from "this
// workspace has no such concept".
func (p *KnowledgePointer) Text(ctx context.Context, agentID, teamID string) string {
	if p == nil || p.files == nil || strings.TrimSpace(p.global) == "" {
		return ""
	}
	inputs := p.inputsFor(ctx, agentID, teamID)
	dirs := []string{inputs.globalDir}
	if inputs.agentDir != "" {
		dirs = append(dirs, inputs.agentDir)
	}
	if inputs.teamDir != "" {
		dirs = append(dirs, inputs.teamDir)
	}
	return renderPointerBlock(dirs, inputs.hasPrivate)
}

// renderPointerBlock is the block's exact text. It is a pure function of the
// directory list so the wording — which is the entire product of this file —
// is testable without building a tree.
//
// The wording carries three facts the model cannot infer:
//
//   - the directories, so it knows where to look;
//   - that knowledge is NOT pre-injected, so it does not answer "there is no
//     knowledge on this" from an empty prompt;
//   - that Grep/Glob/Read are the way in, which is the actual mechanism.
//
// The private-directory sentence is conditional because a private tree is
// usually absent, and claiming ownership of one that does not exist would
// invite a search of a non-existent path.
func renderPointerBlock(dirs []string, hasPrivate bool) string {
	var b strings.Builder
	b.WriteString("## Knowledge Base\n")
	b.WriteString("Reference knowledge for this workspace lives in files, not in this prompt: ")
	b.WriteString("knowledge entries are NOT injected here. Nothing in this section is an\n")
	b.WriteString("entry — it only says where to look.\n\n")
	b.WriteString("Search the knowledge directories with Grep (search by pattern), Glob\n")
	b.WriteString("(list files) and then Read (open one). Do this before concluding there\n")
	b.WriteString("is no guidance on a topic.\n\n")
	b.WriteString("Readable knowledge directories:\n")
	for _, dir := range dirs {
		b.WriteString("- ")
		b.WriteString(dir)
		b.WriteString("/\n")
	}
	if hasPrivate {
		b.WriteString("\nThe agent-private and team-private directories above are yours; other\n")
		b.WriteString("agents' and teams' private knowledge is not readable and will not match.\n")
	}
	b.WriteString("\nKnowledge files are Markdown with YAML frontmatter (id, title, summary,\n")
	b.WriteString("scope). Prefer an entry whose frontmatter matches the situation at hand\n")
	b.WriteString("over your own recollection; the body, not the summary, is the source.")
	return b.String()
}

// hasEntries reports whether dir holds at least one entry MarkdownStore.Load
// would return.
//
// "Loadable entries" and not "directory exists" is the boundary R4 already
// settled for the Bash gate, and for the same reason: an empty directory, or
// one holding only index.md, or one whose entries are all archived, is not
// somewhere the model can find anything, so advertising it would be an
// instruction to search a place with no content. Load is the oracle because
// it is also what decides what R1 makes visible — it skips index.md, skips
// archived entries and derives a summary — so "what the pointer advertises"
// and "what the tools can return" cannot drift apart.
//
// A read error counts as "has entries". That looks like the optimistic
// direction and is; the alternative is a pointer that silently omits a tree
// the model can in fact read whenever a transient read fails, and the model
// has no way to tell "no knowledge here" from "the probe failed". Naming a
// directory that turns out to be empty costs one Grep with no matches — a
// result the model already understands — so the two mistakes are not
// symmetric and this is the cheaper one.
//
// A missing directory is not an error and reports false: most agents have no
// private tree, which is the common case, not a failure.
func hasEntries(ctx context.Context, files storage.FileStore, dir string) bool {
	if files == nil || strings.TrimSpace(dir) == "" {
		return false
	}
	store := NewMarkdownStore(files, dir)
	if !store.Exists() {
		return false
	}
	entries, err := store.Load(ctx)
	if err != nil {
		return true
	}
	return len(entries) > 0
}
