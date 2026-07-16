package knowledge

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"github.com/heron-ai/heron-engine/pkg/types"
)

// KnowledgeIndex manages knowledge entries with keyword-based search
type KnowledgeIndex struct {
	entries []types.KnowledgeEntry
	mu      sync.RWMutex
}

func NewKnowledgeIndex() *KnowledgeIndex {
	return &KnowledgeIndex{}
}

// Add adds a knowledge entry to the index
func (idx *KnowledgeIndex) Add(entry types.KnowledgeEntry) {
	idx.mu.Lock()
	defer idx.mu.Unlock()
	idx.entries = append(idx.entries, entry)
}

// Search finds knowledge entries matching the query keywords
func (idx *KnowledgeIndex) Search(ctx context.Context, query string) ([]types.KnowledgeEntry, error) {
	idx.mu.RLock()
	defer idx.mu.RUnlock()

	queryLower := strings.ToLower(query)
	var results []types.KnowledgeEntry

	for _, entry := range idx.entries {
		if entryMatches(entry, queryLower) {
			results = append(results, entry)
		}
	}

	return results, nil
}

// SearchWithScope filters by scope in addition to keyword matching
func (idx *KnowledgeIndex) SearchWithScope(ctx context.Context, query string, agentName string, teamName string) ([]types.KnowledgeEntry, error) {
	idx.mu.RLock()
	defer idx.mu.RUnlock()

	queryLower := strings.ToLower(query)
	var results []types.KnowledgeEntry

	for _, entry := range idx.entries {
		if !entryMatches(entry, queryLower) {
			continue
		}
		if !scopeAllows(entry.Scope, agentName, teamName) {
			continue
		}
		results = append(results, entry)
	}

	return results, nil
}

// List returns all knowledge entries
func (idx *KnowledgeIndex) List() []types.KnowledgeEntry {
	idx.mu.RLock()
	defer idx.mu.RUnlock()

	result := make([]types.KnowledgeEntry, len(idx.entries))
	copy(result, idx.entries)
	return result
}

// Count returns the number of entries
func (idx *KnowledgeIndex) Count() int {
	idx.mu.RLock()
	defer idx.mu.RUnlock()
	return len(idx.entries)
}

// entryMatches checks if a knowledge entry matches the query
func entryMatches(entry types.KnowledgeEntry, query string) bool {
	// Check content
	if strings.Contains(strings.ToLower(entry.Content), query) {
		return true
	}

	// Check keys
	for _, key := range entry.Keys {
		if strings.Contains(strings.ToLower(key), query) {
			return true
		}
	}

	return false
}

// scopeAllows checks if the scope permits access for the given agent/team
func scopeAllows(scope types.Scope, agentName, teamName string) bool {
	switch scope.Type {
	case "all":
		return true
	case "team":
		for _, t := range scope.Teams {
			if t == teamName {
				return true
			}
		}
		return false
	case "agents":
		for _, a := range scope.Agents {
			if a == agentName {
				return true
			}
		}
		return false
	default:
		return true
	}
}

// KnowledgeExtractor extracts knowledge from agent memories
type KnowledgeExtractor struct {
	index *KnowledgeIndex
}

func NewKnowledgeExtractor(index *KnowledgeIndex) *KnowledgeExtractor {
	return &KnowledgeExtractor{index: index}
}

// Extract converts memories into knowledge entries
func (e *KnowledgeExtractor) Extract(ctx context.Context, memories []types.MemoryEntry) ([]types.KnowledgeEntry, error) {
	var entries []types.KnowledgeEntry

	for _, mem := range memories {
		// Only extract high/critical importance memories
		if mem.Importance != "high" && mem.Importance != "critical" {
			continue
		}

		entry := types.KnowledgeEntry{
			ID:         fmt.Sprintf("mem-%s-%d", mem.Source, mem.Round),
			Content:    mem.Content,
			Keys:       extractKeywords(mem.Content),
			Scope:      types.Scope{Type: "all"},
			Confidence: mem.Importance,
			Source:     mem.Source,
			RoundNum:   mem.Round,
		}
		entries = append(entries, entry)
		e.index.Add(entry)
	}

	return entries, nil
}

func extractKeywords(content string) []string {
	words := strings.Fields(content)
	seen := make(map[string]bool)
	var keys []string

	for _, word := range words {
		word = strings.ToLower(strings.Trim(word, ".,!?;:\"'()[]{}"))
		if len(word) > 3 && !seen[word] {
			seen[word] = true
			keys = append(keys, word)
		}
	}

	return keys
}
