package knowledge

import (
	"context"
	"path"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/heron-ai/heron-engine/internal/storage"
)

// pointerFixture builds a pointer over a temporary workspace with the same
// roots internal/app derives: the shared tree and the per-owner trees are
// siblings under .agents.
func pointerFixture(t *testing.T) (*KnowledgePointer, storage.FileStore) {
	t.Helper()
	files := storage.NewFileStore(t.TempDir())
	pointer := NewKnowledgePointer(files, ".agents/knowledge", ".agents")
	return pointer, files
}

// writeEntry drops one active, loadable knowledge file at path.
func writeEntry(t *testing.T, files storage.FileStore, path, id string) {
	t.Helper()
	require.NoError(t, files.Write(path, []byte(
		"---\nid: "+id+"\ntitle: "+id+" title\nsummary: when to use "+id+"\nstatus: active\n---\n\n"+
			"The body of "+id+".\n",
	)))
}

// TestPointerBlockNamesOnlyReadableDirectories is the security-shaped half of
// the pointer's contract.
//
// The block is an instruction: whatever directory it names, the model will
// grep. So it may only name trees this caller's path filter actually grants,
// and in particular must never name another agent's. The agent's own tree is
// named only when it holds entries — an empty or index.md-only directory is
// not somewhere knowledge can be found, so naming it would be an instruction
// to search a place with nothing in it.
func TestPointerBlockNamesOnlyReadableDirectories(t *testing.T) {
	ctx := context.Background()

	t.Run("no private knowledge: shared tree only", func(t *testing.T) {
		pointer, _ := pointerFixture(t)

		text := pointer.Text(ctx, "agent-a", "team-a")
		require.Contains(t, text, ".agents/knowledge/")
		require.NotContains(t, text, ".agents/agents/")
		require.NotContains(t, text, ".agents/teams/")
	})

	t.Run("private knowledge: own directory is named too", func(t *testing.T) {
		pointer, files := pointerFixture(t)
		writeEntry(t, files, ".agents/agents/agent-a/knowledge/own.md", "own")

		text := pointer.Text(ctx, "agent-a", "team-a")
		require.Contains(t, text, ".agents/knowledge/")
		require.Contains(t, text, ".agents/agents/agent-a/knowledge/")
		require.NotContains(t, text, ".agents/agents/agent-b/knowledge/")
	})

	t.Run("another agent's tree is never named", func(t *testing.T) {
		pointer, files := pointerFixture(t)
		// Agent B has knowledge; agent A does not. A's block must not mention it.
		writeEntry(t, files, ".agents/agents/agent-b/knowledge/secret.md", "secret")

		text := pointer.Text(ctx, "agent-a", "team-a")
		require.NotContains(t, text, "agent-b",
			"a directory the caller cannot read must not be advertised: the model would grep it and read the empty result as 'no knowledge'")
	})

	t.Run("team tree is named only for its own team", func(t *testing.T) {
		pointer, files := pointerFixture(t)
		writeEntry(t, files, ".agents/teams/team-a/knowledge/shared.md", "shared")
		writeEntry(t, files, ".agents/teams/team-z/knowledge/other.md", "other")

		text := pointer.Text(ctx, "agent-a", "team-a")
		require.Contains(t, text, ".agents/teams/team-a/knowledge/")
		require.NotContains(t, text, "team-z")
	})

	t.Run("index.md only is not knowledge", func(t *testing.T) {
		pointer, files := pointerFixture(t)
		require.NoError(t, files.Write(".agents/agents/agent-a/knowledge/index.md",
			[]byte("# Knowledge Index\n")))

		text := pointer.Text(ctx, "agent-a", "team-a")
		require.NotContains(t, text, ".agents/agents/agent-a/knowledge/",
			"a directory holding only the navigation file has nothing to find")
	})

	t.Run("archived entries are not knowledge", func(t *testing.T) {
		pointer, files := pointerFixture(t)
		require.NoError(t, files.Write(".agents/agents/agent-a/knowledge/gone.md", []byte(
			"---\nid: gone\nstatus: archived\n---\n\nRetired.\n")))

		text := pointer.Text(ctx, "agent-a", "team-a")
		require.NotContains(t, text, ".agents/agents/agent-a/knowledge/",
			"Load skips archived entries, so the tools cannot return them either; advertising the tree would promise content the filter cannot deliver")
	})

	t.Run("no agent or team identity: shared tree only", func(t *testing.T) {
		pointer, files := pointerFixture(t)
		writeEntry(t, files, ".agents/agents/agent-a/knowledge/own.md", "own")

		text := pointer.Text(ctx, "", "")
		require.Contains(t, text, ".agents/knowledge/")
		require.NotContains(t, text, ".agents/agents/")
	})
}

// TestPointerBlockIsQueryIndependent is the property that replaces retrieval.
//
// The block the pointer replaces was rebuilt per query, so it changed shape
// every turn and could never sit in a provider-cached prefix. This asserts the
// replacement has the opposite property: for one agent, two unrelated queries
// produce byte-identical text, which is what lets the caller mark it `stable`.
// If anyone reintroduces query-dependent content here, this fails rather than
// silently costing a cache miss per turn.
func TestPointerBlockIsQueryIndependent(t *testing.T) {
	pointer, files := pointerFixture(t)
	writeEntry(t, files, ".agents/agents/agent-a/knowledge/own.md", "own")
	writeEntry(t, files, ".agents/knowledge/global.md", "global")

	ctx := context.Background()
	first := pointer.Text(ctx, "agent-a", "team-a")
	second := pointer.Text(ctx, "agent-a", "team-a")

	require.NotEmpty(t, first)
	require.Equal(t, first, second, "the block must not vary between turns for the same agent")

	// And it is not merely stable — it carries nothing derived from any query
	// or entry. Adding an entry to a tree the agent can already read must not
	// change the block's text at all.
	writeEntry(t, files, ".agents/knowledge/another.md", "another")
	require.Equal(t, first, pointer.Text(ctx, "agent-a", "team-a"),
		"a new entry in an already-advertised tree must not change the block")
}

// TestPointerBlockOmittedWhenNoKnowledge pins the "no empty section" rule: a
// caller with no configured knowledge location gets no block at all.
//
// The reachable case is an unwired runtime — a TurnLoop built without a file
// store, which is most tests and the CLI paths that build no
// knowledge-capable runtime. There the engine cannot read knowledge either, so
// a block directing the model to search would be an instruction the engine
// itself does not support.
func TestPointerBlockOmittedWhenNoKnowledge(t *testing.T) {
	ctx := context.Background()

	t.Run("nil file store", func(t *testing.T) {
		require.Empty(t, NewKnowledgePointer(nil, ".agents/knowledge", ".agents").Text(ctx, "agent-a", "team-a"))
	})

	t.Run("no global root configured", func(t *testing.T) {
		pointer, _ := pointerFixture(t)
		pointer.global = ""
		pointer.private = ""
		require.Empty(t, pointer.Text(ctx, "agent-a", "team-a"))
	})

	t.Run("nil pointer", func(t *testing.T) {
		var pointer *KnowledgePointer
		require.Empty(t, pointer.Text(ctx, "agent-a", "team-a"))
	})
}

// TestPointerBlockHasNoEntryContent is the token-cost guard.
//
// The block replaces a per-entry index, so the whole point is that its size is
// independent of the corpus. A title, summary or body appearing in it would
// mean the cost grows with the knowledge base again — the regression this
// batch exists to remove. The fixture writes entries with distinctive strings
// in all three fields and then asserts none of them reach the prompt text.
func TestPointerBlockHasNoEntryContent(t *testing.T) {
	pointer, files := pointerFixture(t)
	require.NoError(t, files.Write(".agents/knowledge/marked.md", []byte(
		"---\nid: marked-id\ntitle: MARKED_TITLE\nsummary: MARKED_SUMMARY\nstatus: active\n---\n\n"+
			"MARKED_BODY_TEXT.\n",
	)))
	writeEntry(t, files, ".agents/agents/agent-a/knowledge/own.md", "own-id")
	require.NoError(t, files.Write(".agents/agents/agent-a/knowledge/marked-private.md", []byte(
		"---\nid: marked-private-id\ntitle: MARKED_PRIVATE_TITLE\nsummary: MARKED_PRIVATE_SUMMARY\nstatus: active\n---\n\n"+
			"MARKED_PRIVATE_BODY.\n",
	)))

	text := pointer.Text(context.Background(), "agent-a", "team-a")
	require.NotEmpty(t, text)

	for _, leaked := range []string{
		"MARKED_TITLE", "MARKED_SUMMARY", "MARKED_BODY_TEXT", "marked-id",
		"MARKED_PRIVATE_TITLE", "MARKED_PRIVATE_SUMMARY", "MARKED_PRIVATE_BODY", "marked-private-id",
	} {
		require.NotContains(t, text, leaked, "no entry content may appear in the pointer block")
	}

	// The size must not track the number of entries either: the same block
	// after adding entries to a tree it already names.
	before := text
	for i := 0; i < 20; i++ {
		writeEntry(t, files, path.Join(".agents/knowledge", "bulk-"+string(rune('a'+i))+".md"), "bulk")
	}
	require.Equal(t, before, pointer.Text(context.Background(), "agent-a", "team-a"),
		"the block must not grow with the corpus")
}

// TestPointerBlockDirectsToFileTools asserts the block actually says what to
// do. The directories alone are not enough: without the directive the model
// may treat the section as an announcement and never search.
func TestPointerBlockDirectsToFileTools(t *testing.T) {
	pointer, files := pointerFixture(t)
	writeEntry(t, files, ".agents/knowledge/global.md", "global")

	text := pointer.Text(context.Background(), "agent-a", "team-a")
	for _, tool := range []string{"Grep", "Glob", "Read"} {
		require.Contains(t, text, tool, "the block must name %s as the way in", tool)
	}
	require.Contains(t, text, "NOT injected",
		"the block must say knowledge is not pre-injected, or an empty prompt reads as an empty knowledge base")
	require.True(t, strings.HasPrefix(text, "## Knowledge Base"))
}
