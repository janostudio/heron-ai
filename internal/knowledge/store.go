package knowledge

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"sync"

	"github.com/adrg/frontmatter"
	"github.com/heron-ai/heron-engine/internal/storage"
	"github.com/heron-ai/heron-engine/pkg/types"
	"gopkg.in/yaml.v3"
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
	if entry.Status == "" {
		entry.Status = "active"
	}
	idx.mu.Lock()
	defer idx.mu.Unlock()
	for i, existing := range idx.entries {
		if existing.ID == entry.ID && entry.ID != "" {
			idx.entries[i] = entry
			return
		}
	}
	idx.entries = append(idx.entries, entry)
}

// Search finds knowledge entries matching the query keywords
func (idx *KnowledgeIndex) Search(ctx context.Context, query string) ([]types.KnowledgeEntry, error) {
	idx.mu.RLock()
	defer idx.mu.RUnlock()

	queryLower := strings.ToLower(query)
	var results []types.KnowledgeEntry

	for _, entry := range idx.entries {
		if entry.Status == "deprecated" {
			continue
		}
		if entryMatches(entry, queryLower) {
			results = append(results, entry)
		}
	}

	return results, nil
}

// SearchWithScope filters by scope in addition to keyword matching
func (idx *KnowledgeIndex) SearchWithScope(ctx context.Context, query string, agentName string, teamName string) ([]types.KnowledgeEntry, error) {
	return idx.SearchWithScopeAndAllowlist(ctx, query, agentName, teamName, nil)
}

// SearchWithScopeAndAllowlist filters by visibility and, when provided, by
// the Agent's explicit knowledge IDs. An empty allowlist preserves the
// default behavior of returning every visible matching entry.
func (idx *KnowledgeIndex) SearchWithScopeAndAllowlist(
	ctx context.Context,
	query string,
	agentName string,
	teamName string,
	allowlist []string,
) ([]types.KnowledgeEntry, error) {
	idx.mu.RLock()
	defer idx.mu.RUnlock()

	queryLower := strings.ToLower(query)
	var results []types.KnowledgeEntry

	for _, entry := range idx.entries {
		if entry.Status == "deprecated" {
			continue
		}
		if !entryMatches(entry, queryLower) {
			continue
		}
		if !scopeAllows(entry.Scope, agentName, teamName) {
			continue
		}
		if !knowledgeAllowed(entry, allowlist) {
			continue
		}
		results = append(results, entry)
	}

	return results, nil
}

// MarkdownStore persists KnowledgeEntry as Markdown files with YAML
// frontmatter. index.md is an index file and is not itself treated as a
// knowledge entry.
type MarkdownStore struct {
	files storage.FileStore
	root  string
	mu    sync.Mutex
}

func NewMarkdownStore(files storage.FileStore, root string) *MarkdownStore {
	return &MarkdownStore{files: files, root: filepath.Clean(root)}
}

func (s *MarkdownStore) Load(ctx context.Context) ([]types.KnowledgeEntry, error) {
	if err := contextErr(ctx); err != nil {
		return nil, err
	}
	paths, err := s.listMarkdown(s.root)
	if err != nil {
		return nil, err
	}

	entries := make([]types.KnowledgeEntry, 0, len(paths))
	for _, path := range paths {
		if filepath.Base(path) == "index.md" {
			continue
		}
		data, err := s.files.Read(path)
		if err != nil {
			return nil, fmt.Errorf("read knowledge %s: %w", path, err)
		}

		var entry types.KnowledgeEntry
		body, err := frontmatter.Parse(strings.NewReader(string(data)), &entry)
		if err != nil {
			return nil, fmt.Errorf("parse knowledge %s: %w", path, err)
		}
		entry.Content = strings.TrimSpace(string(body))
		entry.Path = path
		if entry.Status == "" {
			entry.Status = "active"
		}
		if entry.ID == "" {
			entry.ID = strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
		}
		entries = append(entries, entry)
	}
	return entries, nil
}

func (s *MarkdownStore) Save(ctx context.Context, entry types.KnowledgeEntry) error {
	if err := contextErr(ctx); err != nil {
		return err
	}
	if strings.TrimSpace(entry.ID) == "" {
		return fmt.Errorf("knowledge id is required")
	}
	if entry.Status == "" {
		entry.Status = "active"
	}
	if entry.Version <= 0 {
		entry.Version = 1
	}
	if entry.Path == "" {
		entry.Path = filepath.Join(s.root, entry.ID+".md")
	}
	if err := s.ensureWithinRoot(entry.Path); err != nil {
		return err
	}

	data, err := encodeMarkdown(entry)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.files.Write(entry.Path, data)
}

func (s *MarkdownStore) RebuildIndex(ctx context.Context) error {
	entries, err := s.Load(ctx)
	if err != nil {
		return err
	}

	var builder strings.Builder
	builder.WriteString("# Knowledge Index\n\n")
	builder.WriteString("> Generated index. Knowledge正文保存在同级 Markdown 文件中。\n\n")
	for _, entry := range entries {
		title := entry.Title
		if title == "" {
			title = entry.ID
		}
		summary := entry.Summary
		if summary == "" {
			summary = firstLine(entry.Content)
		}
		relative, relErr := filepath.Rel(s.root, entry.Path)
		if relErr != nil {
			relative = entry.Path
		}
		fmt.Fprintf(&builder, "- [%s](%s) — %s\n", title, filepath.ToSlash(relative), summary)
	}
	return s.files.Write(filepath.Join(s.root, "index.md"), []byte(builder.String()))
}

func (s *MarkdownStore) listMarkdown(dir string) ([]string, error) {
	names, err := s.files.List(dir)
	if err != nil {
		return nil, err
	}
	var result []string
	for _, name := range names {
		path := filepath.Join(dir, name)
		if s.files.Exists(path) {
			if filepath.Ext(name) == ".md" {
				result = append(result, path)
				continue
			}
			if filepath.Ext(name) == "" {
				children, err := s.listMarkdown(path)
				if err != nil {
					return nil, err
				}
				result = append(result, children...)
			}
		}
	}
	return result, nil
}

func (s *MarkdownStore) ensureWithinRoot(path string) error {
	root := filepath.Clean(s.root)
	target := filepath.Clean(path)
	relative, err := filepath.Rel(root, target)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return fmt.Errorf("knowledge path escapes root: %s", path)
	}
	return nil
}

func encodeMarkdown(entry types.KnowledgeEntry) ([]byte, error) {
	frontmatterData := struct {
		ID         string           `yaml:"id"`
		Title      string           `yaml:"title,omitempty"`
		Summary    string           `yaml:"summary,omitempty"`
		Keys       []string         `yaml:"keys,omitempty"`
		Scope      types.Scope      `yaml:"scope"`
		Status     string           `yaml:"status"`
		Path       string           `yaml:"path,omitempty"`
		Basis      []types.BasisRef `yaml:"basis,omitempty"`
		Version    int              `yaml:"version"`
		Confidence string           `yaml:"confidence,omitempty"`
		Source     string           `yaml:"source,omitempty"`
	}{
		ID: entry.ID, Title: entry.Title, Summary: entry.Summary,
		Keys: entry.Keys, Scope: entry.Scope, Status: entry.Status,
		Path: entry.Path, Basis: entry.Basis, Version: entry.Version,
		Confidence: entry.Confidence, Source: entry.Source,
	}
	data, err := yaml.Marshal(frontmatterData)
	if err != nil {
		return nil, fmt.Errorf("marshal knowledge frontmatter: %w", err)
	}
	return []byte("---\n" + string(data) + "---\n\n" + strings.TrimSpace(entry.Content) + "\n"), nil
}

func firstLine(content string) string {
	if index := strings.IndexByte(content, '\n'); index >= 0 {
		return strings.TrimSpace(content[:index])
	}
	return strings.TrimSpace(content)
}

func contextErr(ctx context.Context) error {
	if ctx == nil {
		return nil
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
		return nil
	}
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

func knowledgeAllowed(entry types.KnowledgeEntry, allowlist []string) bool {
	if len(allowlist) == 0 {
		return true
	}
	for _, allowed := range allowlist {
		if strings.TrimSpace(allowed) == entry.ID {
			return true
		}
	}
	return false
}

// entryMatches checks if a knowledge entry matches the query
func entryMatches(entry types.KnowledgeEntry, query string) bool {
	query = strings.ToLower(strings.TrimSpace(query))
	if query == "" {
		return false
	}

	// The runtime query combines responsibility and user input. Preserve exact
	// phrase matching, then fall back to meaningful terms so one extra sentence
	// in the user request does not hide an otherwise relevant entry.
	fields := []string{entry.ID, entry.Title, entry.Summary, entry.Content}
	fields = append(fields, entry.Keys...)
	searchText := strings.ToLower(strings.Join(fields, "\n"))
	if strings.Contains(searchText, query) {
		return true
	}

	for _, term := range knowledgeTerms(query) {
		if strings.Contains(searchText, term) {
			return true
		}
	}
	return false
}

func knowledgeTerms(query string) []string {
	var terms []string
	seen := make(map[string]struct{})
	for _, field := range strings.FieldsFunc(query, func(r rune) bool {
		switch r {
		case ' ', '\t', '\r', '\n', ',', '.', ':', ';', '!', '?',
			'(', ')', '[', ']', '{', '}', '"', '\'', '`', '/', '\\':
			return true
		default:
			return false
		}
	}) {
		field = strings.TrimSpace(field)
		if len([]rune(field)) < 2 {
			continue
		}
		if _, ok := seen[field]; ok {
			continue
		}
		seen[field] = struct{}{}
		terms = append(terms, field)
	}
	return terms
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

// KnowledgeExtractor extracts knowledge from agent state observations
type KnowledgeExtractor struct {
	index *KnowledgeIndex
}

func NewKnowledgeExtractor(index *KnowledgeIndex) *KnowledgeExtractor {
	return &KnowledgeExtractor{index: index}
}

// Extract converts state observations into knowledge entries
func (e *KnowledgeExtractor) Extract(ctx context.Context, states []types.StateObservation) ([]types.KnowledgeEntry, error) {
	var entries []types.KnowledgeEntry

	for _, mem := range states {
		// Only extract high/critical importance states
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
