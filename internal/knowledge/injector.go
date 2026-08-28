package knowledge

import (
	"context"
	"fmt"
	"strings"

	"github.com/heron-ai/heron-engine/pkg/types"
)

// KnowledgeInjector injects knowledge into agent context
type KnowledgeInjector struct {
	index *KnowledgeIndex
}

func NewKnowledgeInjector(index *KnowledgeIndex) *KnowledgeInjector {
	return &KnowledgeInjector{index: index}
}

// Inject searches knowledge and formats it for prompt injection
func (i *KnowledgeInjector) Inject(ctx context.Context, query string, agentName string, teamName string) (string, error) {
	return i.InjectWithAllowlist(ctx, query, agentName, teamName, nil)
}

// InjectWithAllowlist applies the Agent's declared knowledge IDs after scope
// filtering. This keeps knowledge selection explicit without changing the
// existing injector API used by callers that intentionally search all visible
// entries.
func (i *KnowledgeInjector) InjectWithAllowlist(
	ctx context.Context,
	query string,
	agentName string,
	teamName string,
	allowlist []string,
) (string, error) {
	entries, err := i.index.SearchWithScopeAndAllowlist(ctx, query, agentName, teamName, allowlist)
	if err != nil {
		return "", fmt.Errorf("search knowledge: %w", err)
	}

	if len(entries) == 0 {
		return "", nil
	}

	return i.formatEntries(entries), nil
}

// InjectAll returns all knowledge entries for an agent
func (i *KnowledgeInjector) InjectAll(ctx context.Context, agentName string, teamName string) (string, error) {
	entries := i.index.List()

	var filtered []types.KnowledgeEntry
	for _, entry := range entries {
		if scopeAllows(entry.Scope, agentName, teamName) {
			filtered = append(filtered, entry)
		}
	}

	if len(filtered) == 0 {
		return "", nil
	}

	return i.formatEntries(filtered), nil
}

func (i *KnowledgeInjector) formatEntries(entries []types.KnowledgeEntry) string {
	var parts []string
	parts = append(parts, "## Knowledge Context\n")

	for _, entry := range entries {
		parts = append(parts, fmt.Sprintf("- %s", entry.Content))
	}

	return strings.Join(parts, "\n")
}
