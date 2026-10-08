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

// Exists reports whether the store's root directory exists.
//
// It exists so a caller can tell "no knowledge here" from "knowledge here that
// did not load", which the Load error alone cannot: an absent directory is a
// normal state for every workspace without a knowledge base, but a directory
// that is present and malformed is a broken tree that must not be mistaken for
// an empty one.
func (s *MarkdownStore) Exists() bool {
	return s.files.Exists(s.root)
}

// Load returns the active knowledge index: one entry per file, carrying
// metadata only.
//
// Content is deliberately left empty. The body is read off disk only when
// something actually asks for it — the model's own Read tool, or the write
// paths below via withBody — so nothing in the process holds a knowledge
// document it is not currently using. Loading bodies eagerly is what made
// every knowledge file's full text resident in the process; drop that and a
// store of a thousand entries costs a thousand metadata records instead of a
// thousand documents.
//
// The body is still parsed on this path for one reason: deriving a missing
// Summary (see deriveSummaryFromBody) requires it. It is discarded immediately
// after. Callers that must round-trip a file — Archive, findByID — use load
// with withBody=true instead.
//
// What Load is still the oracle for: it defines the set of entries that exist
// as far as the engine is concerned. The Bash gate
// (internal/agent/bash_gate.go) and the knowledge pointer block
// (internal/knowledge/pointer.go) both ask it "is there anything here?" so
// that what the engine withholds, what it advertises and what the path filter
// hides are all the same set.
func (s *MarkdownStore) Load(ctx context.Context) ([]types.KnowledgeEntry, error) {
	return s.load(ctx, loadOptions{})
}

// loadOptions selects the view load returns. It is a struct rather than two
// booleans because the two flags are independent and a positional call site
// (`load(ctx, true, false)`) reads as neither.
type loadOptions struct {
	// withBody keeps the parsed body on the returned entry. The default
	// view leaves it empty; the write paths (Archive, findByID) need it to
	// round-trip a file.
	withBody bool
	// includeArchived returns archived entries too, which the lifecycle
	// commands require and the default view must not.
	includeArchived bool
}

// load is the single reader behind Load, LoadAll and the write paths. The
// options select which view is wanted; everything else — frontmatter parsing,
// scope validation, ID/status defaults, summary derivation — is the same code
// for every caller, so no two views can disagree about what a file means.
func (s *MarkdownStore) load(ctx context.Context, opts loadOptions) ([]types.KnowledgeEntry, error) {
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
		content := strings.TrimSpace(string(body))
		if opts.withBody {
			entry.Content = content
		}
		if entry.Summary == "" {
			entry.Summary = deriveSummaryFromBody(content)
		}
		entry.Path = path
		// Validation runs before the archived filter below, not after: a
		// misplaced file is a broken tree whether or not its status hides it
		// from the index, and reporting it only once someone un-archives it
		// would surface the error long after the edit that caused it.
		if err := s.reconcileScope(&entry); err != nil {
			return nil, err
		}
		if entry.Status == "" {
			entry.Status = "active"
		}
		if entry.ID == "" {
			entry.ID = strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
		}
		if entry.Status == "archived" && !opts.includeArchived {
			continue
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
		entry.Path = s.placementPath(entry)
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

// Archive marks a knowledge entry as archived without deleting its file. The
// entry is located by ID (falling back to a <root>/<id>.md path lookup), its
// frontmatter Status is rewritten to "archived", and the file is re-written in
// place. Archived entries are filtered out of Load and search thereafter.
func (s *MarkdownStore) Archive(ctx context.Context, id string) error {
	if err := contextErr(ctx); err != nil {
		return err
	}
	if strings.TrimSpace(id) == "" {
		return fmt.Errorf("knowledge id is required")
	}

	// Read with bodies: this is a read-modify-write of the whole file, and
	// Save re-encodes whatever Content it is handed. Using the body-less Load
	// here would rewrite every archived entry as an empty document.
	entries, err := s.load(ctx, loadOptions{withBody: true})
	if err != nil {
		return err
	}
	var target *types.KnowledgeEntry
	for i := range entries {
		if entries[i].ID == id {
			target = &entries[i]
			break
		}
	}
	if target == nil {
		return fmt.Errorf("knowledge %q not found", id)
	}

	target.Status = "archived"
	return s.Save(ctx, *target)
}

// FindDuplicate scans the store for an active entry whose metadata overlaps
// the candidate by keyword matching (reusing entryMatches). It returns the
// first match or nil. Matching against empty candidate text is always a
// non-match.
//
// This is the sole remaining caller of entryMatches and is a WRITE-path
// question — "am I about to store a second copy of something already here?" —
// which is why it outlived the retrieval index that entryMatches was written
// for. `heron knowledge` calls it to detect a re-learned entry before writing
// one.
//
// The comparison is asymmetric on purpose: the candidate may be a freshly
// distilled document that still has its Content (that is how the learn path
// calls this), but the stored side is read body-less, so the overlap is
// established against metadata — Title/Summary/Keys. A candidate whose only
// distinguishing text lives in its body can therefore slip past; that is
// preferred to loading every stored body just to answer a dedup question, and
// the ID check below catches the ordinary re-learn case.
func (s *MarkdownStore) FindDuplicate(ctx context.Context, candidate types.KnowledgeEntry) (*types.KnowledgeEntry, error) {
	if err := contextErr(ctx); err != nil {
		return nil, err
	}
	entries, err := s.Load(ctx)
	if err != nil {
		return nil, err
	}
	query := strings.ToLower(strings.TrimSpace(candidate.Title + " " + candidate.Summary + " " + candidate.Content))
	if query == "" {
		return nil, nil
	}
	for i := range entries {
		entry := entries[i]
		if entry.Status != "active" {
			continue
		}
		if entry.ID == candidate.ID {
			continue
		}
		if entryMatches(entry, query) {
			return &entry, nil
		}
	}
	return nil, nil
}

// UpsertActive saves a knowledge entry to the active area, handling the
// lifecycle semantics of `heron learn`:
//   - If an entry with the same ID already exists (any status), the new entry's
//     version is bumped to oldVersion+1 and the old entry is marked deprecated.
//   - Otherwise the entry is written fresh with the given version (default 1).
func (s *MarkdownStore) UpsertActive(ctx context.Context, entry types.KnowledgeEntry) (types.KnowledgeEntry, error) {
	if err := contextErr(ctx); err != nil {
		return types.KnowledgeEntry{}, err
	}
	if strings.TrimSpace(entry.ID) == "" {
		return types.KnowledgeEntry{}, fmt.Errorf("knowledge id is required")
	}
	if entry.Status == "" {
		entry.Status = "active"
	}

	existing, err := s.findByID(ctx, entry.ID)
	if err != nil {
		return types.KnowledgeEntry{}, err
	}

	if existing != nil {
		// Bump version relative to the existing entry and deprecate it. The
		// deprecated version is persisted to a versioned filename so it does
		// not collide with the new active entry written to <id>.md.
		//
		// The version file goes beside the entry it supersedes, not to a
		// location re-derived from its scope: the two would agree for a
		// conformant tree, but re-deriving would silently relocate the history
		// of an entry that a caller had placed by hand, and the whole point of
		// the lookup path is that the old file is read, rewritten and left
		// where it was.
		old := *existing
		old.Status = "deprecated"
		old.Path = versionedSiblingPath(old)
		if err := s.Save(ctx, old); err != nil {
			return types.KnowledgeEntry{}, err
		}
		if entry.Version <= old.Version {
			entry.Version = old.Version + 1
		}
	}

	if entry.Version <= 0 {
		entry.Version = 1
	}
	entry.Path = ""
	if err := s.Save(ctx, entry); err != nil {
		return types.KnowledgeEntry{}, err
	}
	return entry, nil
}

// versionedSiblingPath returns the versioned filename a deprecated entry is
// written to: <id>.v<version>.md in the same directory as the active file.
//
// The directory is taken from the entry's current Path rather than reconstructed
// from its scope. A private entry's directory encodes its owner, and a caller
// that placed the file by hand (or an older tree whose scope the loader has
// since settled) would otherwise have its superseded versions moved somewhere
// the next lookup does not scan.
func versionedSiblingPath(entry types.KnowledgeEntry) string {
	name := fmt.Sprintf("%s.v%d.md", entry.ID, entry.Version)
	if entry.Path == "" {
		return name
	}
	return filepath.Join(filepath.Dir(entry.Path), name)
}

// findByID locates an entry by ID across all statuses (Load filters archived,
// so a raw disk scan is required to also detect archived entries).
//
// It returns a body-carrying entry: UpsertActive hands the result to Save to
// write the deprecated version file, which must keep the original text.
func (s *MarkdownStore) findByID(ctx context.Context, id string) (*types.KnowledgeEntry, error) {
	entries, err := s.load(ctx, loadOptions{withBody: true})
	if err != nil {
		return nil, err
	}
	for i := range entries {
		if entries[i].ID == id {
			return &entries[i], nil
		}
	}
	return nil, nil
}

// LoadAll is like Load but includes archived entries. It is used by the
// lifecycle commands (list/archive) which must observe archived knowledge.
// Like Load it returns metadata only; GC decides on Status/CreatedAt/hit
// counts, never on the body.
//
// It delegates to load rather than repeating the parse loop, which it used to
// do. The duplicate was a correctness hazard the moment validation was added at
// load: the copy did not validate, so a tree that Load rejected would be read
// happily by `heron knowledge gc` and `heron knowledge --list` — the same
// broken tree accepted or rejected depending on which command you ran. One
// reader means one verdict.
func (s *MarkdownStore) LoadAll(ctx context.Context) ([]types.KnowledgeEntry, error) {
	return s.load(ctx, loadOptions{includeArchived: true})
}

// RebuildIndex regenerates index.md, the human-readable listing of the store.
//
// No summary fallback is needed here any more: Load derives one from the body
// whenever frontmatter omits it, so entry.Summary is already the best text
// available by the time the index is written.
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
		relative, relErr := filepath.Rel(s.root, entry.Path)
		if relErr != nil {
			relative = entry.Path
		}
		fmt.Fprintf(&builder, "- [%s](%s) — %s\n", title, filepath.ToSlash(relative), entry.Summary)
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

// ensureWithinRoot is the write-path guard: no Save may place a file outside
// the tree the store belongs to.
//
// "The tree the store belongs to" is the *config* root, not the store root.
// The two were the same while every entry lived under the store root, but they
// are not any more: a private knowledge entry belongs at
// .agents/agents/<id>/knowledge/, one level sideways from the global store
// rooted at .agents/knowledge. Rejecting that would make Save unable to write
// the very layout the loader now requires, so the allowed region is widened —
// from "under the store root" to "under the config root that contains it".
//
// The escape this still prevents is the one that matters: a caller passing a
// path that leaves the configuration tree entirely (.agents/../../etc/passwd,
// or an unrelated absolute path). A store whose root is not a recognizable
// knowledge location keeps the old, stricter check, because no wider tree can
// be derived for it and widening to the filesystem root would be no check at
// all.
func (s *MarkdownStore) ensureWithinRoot(path string) error {
	if !s.withinRoot(s.root, path) {
		return fmt.Errorf("knowledge path escapes root: %s", path)
	}
	return nil
}

// withinRoot reports whether path sits at or under root, or under the config
// root that root belongs to.
func (s *MarkdownStore) withinRoot(root, path string) bool {
	if isWithinRoot(root, path) {
		return true
	}
	configRoot, ok := s.configRoot()
	if !ok {
		return false
	}
	return isWithinRoot(configRoot, path)
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

// maxDerivedSummaryRunes bounds a derived summary, in runes rather than bytes
// so a CJK body is cut at the same visual length as a Latin one.
const maxDerivedSummaryRunes = 200

// deriveSummaryFromBody builds a summary for a knowledge file whose
// frontmatter has none, taking the first paragraph that is neither a Markdown
// heading nor blank.
//
// # Why this survived the move to agentic search
//
// It used to exist because Summary was the only text in-memory retrieval could
// match on. Retrieval is gone, but the summary is not: it is what RebuildIndex
// writes into index.md, and what FindDuplicate compares a candidate against
// when deciding whether the learn path is about to write a second copy of
// something already stored. Both of those are *human- and write-facing*
// consumers that read entry.Summary directly.
//
// The field is optional in practice — across this repository's example and
// built-in knowledge, only one file out of 23 declares a summary, and three
// have no frontmatter at all — so without the fallback index.md would list
// most entries with a blank description and dedup would have nothing to
// compare. Deriving at load time fixes that without touching the files: the
// value lives in memory only and is never written back — writing is
// Save/UpsertActive's job, and a load must not mutate the store.
//
// Derivation is a fallback, never a replacement: an explicit frontmatter
// summary always wins, because it is authored deliberately and is a better
// signal than any mechanical first-paragraph cut.
//
// Deterministic by construction: same body in, same summary out. Newlines
// inside the paragraph collapse to single spaces so the result survives being
// rendered inline in a list. A body with no qualifying paragraph (empty, or
// headings only) yields an empty string, which the caller leaves as-is.
func deriveSummaryFromBody(body string) string {
	// Walk lines, not blank-line-separated paragraphs. A Markdown document
	// commonly has a heading immediately followed by its content with no blank
	// line between them (`## Title\n- point`), which is a single paragraph — so
	// a paragraph-level filter that rejects paragraphs *starting* with "#"
	// rejects the heading AND its content together. An entry whose body is
	// nothing but headings and lists then derives no summary at all, and an
	// entry with no summary renders as a blank line in index.md.
	//
	// The scan collects three progressively weaker candidates and stops at the
	// first blank line that follows real content:
	//
	//	prose   — sentences under the title; the best selection signal
	//	items   — list items; still concrete about what the entry covers
	//	heading — the title itself; beats an empty summary
	//
	// A leading heading is a label, not content, so it does NOT end the scan —
	// the scan continues to the prose beneath it. Only content (or a second
	// heading after content) terminates the opening block.
	var heading string
	var items []string
	var prose []string

	for _, line := range strings.Split(body, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			// A blank line ends the opening block only once something
			// substantive has been gathered; leading blanks are skipped.
			if len(prose) > 0 || len(items) > 0 {
				break
			}
			continue
		}

		switch {
		case strings.HasPrefix(trimmed, "#"):
			if heading == "" {
				heading = strings.TrimSpace(strings.TrimLeft(trimmed, "#"))
				continue
			}
			// A later heading marks the end of the lead-in section: whatever
			// the document opened with has been collected by now.
			if len(prose) > 0 || len(items) > 0 {
				goto done
			}
		case isListMarker(trimmed):
			if item := strings.TrimSpace(trimmed[1:]); item != "" {
				items = append(items, collapseWhitespace(item))
			}
		default:
			prose = append(prose, trimmed)
		}
	}

done:
	// Preference order: prose, then list items, then the heading. Each is
	// strictly less informative than the one before, but all three beat an
	// empty summary.
	if text := collapseWhitespace(strings.Join(prose, " ")); text != "" {
		return truncateSummary(text, maxDerivedSummaryRunes)
	}
	if len(items) > 0 {
		return truncateSummary(strings.Join(items, "; "), maxDerivedSummaryRunes)
	}
	return truncateSummary(collapseWhitespace(heading), maxDerivedSummaryRunes)
}

// isListMarker reports whether a line opens a Markdown list item. Only the
// leading delimiter is inspected; callers strip it.
func isListMarker(line string) bool {
	if len(line) == 0 {
		return false
	}
	switch line[0] {
	case '-', '*', '+':
		// Require a following space so a line like "-> arrow" is not a list.
		return len(line) > 1 && (line[1] == ' ' || line[1] == '\t')
	default:
		return false
	}
}

func collapseWhitespace(text string) string {
	return strings.Join(strings.Fields(text), " ")
}

// truncateSummary cuts text to limit runes, appending an ellipsis when it had
// to cut. The marker is a single ASCII "...", not "…", so truncation is
// visible in any encoding and the cost is a fixed one byte.
func truncateSummary(text string, limit int) string {
	runes := []rune(text)
	if len(runes) <= limit {
		return text
	}
	return string(runes[:limit]) + "..."
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

// entryMatches checks if a knowledge entry matches the query.
//
// Only metadata participates: ID, Title, Summary and Keys. The body does not —
// not as a deliberate scoring choice, but because it is not in memory to match
// against (see Load).
//
// # Who still uses this
//
// Not retrieval: since knowledge moved to agentic search (design doc
// skill-progressive-disclosure §3.4), the model finds entries with Grep, and
// this in-memory substring match — no regex, no line numbers, metadata only —
// was strictly weaker than the Grep the agent already has. The index that fed
// it is gone.
//
// One caller remains, and it is a different kind of question: FindDuplicate,
// which asks "does an entry covering this already exist?" before the learn
// path writes a new one. That is a write-path dedup, not a read path — a
// wrong answer costs a duplicate file, never a missing one — and it compares
// a candidate's text against the stored metadata for the same reason
// retrieval did: the stored side is read body-less, so metadata is all there
// is to compare.
//
// The comment is kept (rather than the function being inlined at its one call
// site) because the asymmetry it explains is not visible from FindDuplicate:
// the *candidate* may carry a body, the *stored* entry never does.
func entryMatches(entry types.KnowledgeEntry, query string) bool {
	query = strings.ToLower(strings.TrimSpace(query))
	if query == "" {
		return false
	}

	// The candidate text combines title, summary and body. Preserve exact
	// phrase matching, then fall back to meaningful terms so one extra
	// sentence does not hide an otherwise overlapping entry.
	fields := []string{entry.ID, entry.Title, entry.Summary}
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

// Location kinds, as returned by locationScopeOf. The string values are
// deliberately the same words the frontmatter scope uses for team/agent, so an
// error message can print one word for both and read as a single sentence.
const (
	locationGlobal = "global"
	locationAgent  = "agent"
	locationTeam   = "team"
)

// locationScope is the visibility a knowledge file's *location* implies, as
// opposed to the visibility its frontmatter *claims*.
//
// The two used to be independent: a file anywhere under the knowledge root
// could declare any scope, and a scope predicate enforced the declaration at
// retrieval time. That enforcement does not survive agentic search. Once the
// model greps the tree itself (design doc skill-progressive-disclosure §3.4),
// the decision "may this caller see this file?" is made by a path filter on
// the way in, and a path filter cannot read a YAML field out of the file it is
// deciding whether to hand back. A frontmatter scope is therefore
// unenforceable-by-construction, and the only complete guarantee left is that
// visibility is a property of where the file lives.
//
// This type is the single place that convention is encoded. Everything that
// needs to reason about "what does this path mean" — validation here, the
// Grep/Glob path filter in internal/tool/path_scope.go, the Bash gate in
// internal/agent/bash_gate.go, the pointer block in pointer.go — goes through
// locationScopeOf and knowledgeLocationRoots rather than re-deriving the
// layout, or the copies will drift and the drifting one will be the leak.
type locationScope struct {
	// Kind is locationGlobal, locationAgent or locationTeam; empty when the
	// path is not a knowledge location at all (see ok).
	Kind string
	// OwnerID is the agent or team directory name the file lives under, and
	// is empty for a global location.
	OwnerID string
}

// isPrivate reports whether the location names an owner. Private locations are
// the ones whose frontmatter must name the same owner, because the location —
// not the frontmatter — is what a path filter can enforce.
func (l locationScope) isPrivate() bool {
	return l.Kind == locationAgent || l.Kind == locationTeam
}

// String renders the location the way an error should name it: the frontmatter
// vocabulary plus the directory that would express it.
func (l locationScope) String() string {
	switch l.Kind {
	case locationAgent:
		return fmt.Sprintf("agent-private to %q (.agents/agents/%s/knowledge/)", l.OwnerID, l.OwnerID)
	case locationTeam:
		return fmt.Sprintf("team-private to %q (.agents/teams/%s/knowledge/)", l.OwnerID, l.OwnerID)
	default:
		return "global (.agents/knowledge/)"
	}
}

// knowledgeLocationRoots are the static prefixes of the three supported
// locations, given as path segments. The config root itself is not part of
// them: see locationScopeOf.
var knowledgeLocationRoots = struct{ Global, Agents, Teams string }{
	Global: "knowledge",
	Agents: "agents",
	Teams:  "teams",
}

// locationScopeOf determines what visibility the file at path implies, given
// the root of the store that listed it.
//
// The convention it encodes (one row per supported location):
//
//	knowledge/...               → global, visible to every agent and team
//	agents/<id>/knowledge/...   → private to agent <id>
//	teams/<id>/knowledge/...    → private to team <id>
//
// Anything else returns ok=false. That is a real outcome, not a bug: the same
// MarkdownStore type also serves directories that are not layout-conformant
// (a store rooted at .agents/knowledge lists files under flow/, team/ and
// agent/ — see scopeDir), and the caller decides whether an unrecognized
// location is a validation failure or merely nothing to validate. Keeping the
// "unrecognized" case explicit here is what stops a wrong-but-plausible guess
// from silently widening visibility.
//
// Matching is done on the *tail* of the path, not the prefix, so it holds for
// every root the engine actually builds — the process-relative
// ".agents/agents/<id>/knowledge" and the absolute config root a loader hands
// over — without either caller having to know which shape it holds. It is
// tolerant of a leading "./" and of cleaned or uncleaned input (filepath.Clean
// first), which is the difference between validating and spuriously rejecting
// a tree whose paths were joined differently upstream.
//
// The whole path is considered, not just its trailing segments: anchoring on
// segments that contain "knowledge" at the end is what keeps a store rooted at
// .agents/knowledge/agents/<id>/knowledge/... (a global store that happens to
// have a subdirectory called agents) from being misread as an agent-private
// one. See locationScopeOf's callers for why that matters.
func locationScopeOf(root, path string) (locationScope, bool) {
	segments := pathSegments(path)
	if len(segments) == 0 {
		return locationScope{}, false
	}

	// A private location is <owner-dir>/<id>/knowledge/... — three consecutive
	// segments, agents/<id>/knowledge or teams/<id>/knowledge. Scan from the
	// end so the innermost (rightmost) match wins: that is the knowledge
	// directory actually holding the file, and an outer one would name a
	// different owner.
	for i := len(segments) - 3; i >= 0; i-- {
		ownerDir := segments[i]
		if ownerDir != knowledgeLocationRoots.Agents && ownerDir != knowledgeLocationRoots.Teams {
			continue
		}
		if segments[i+2] != knowledgeLocationRoots.Global {
			continue
		}
		owner := segments[i+1]
		if owner == "" {
			continue
		}
		kind := locationAgent
		if ownerDir == knowledgeLocationRoots.Teams {
			kind = locationTeam
		}
		return locationScope{Kind: kind, OwnerID: owner}, true
	}

	// Global: no owner segment to match on, so the question is only whether
	// the path is inside a knowledge directory this function can attribute.
	// Two readings are accepted and both are genuinely global:
	//
	//   - under the store's own root, which is the reading that makes a store
	//     rooted at ".agents/knowledge" validate its own files (the generic
	//     matcher above is looking for an owner directory and those paths have
	//     none);
	//   - under a bare "knowledge/" prefix, which is the same thing spelled
	//     relative-ish, for a store built over an absolute config root.
	//
	// Deliberately NOT accepted: a path like ".agents/agents/<id>/knowledge/x.md"
	// resolving to agent <id> just because it contains those segments. It would
	// only be right when the store's root is exactly the prefix in question, and
	// a store built over some other tree that happens to contain
	// "agents/<id>/knowledge" would then be misattributed to <id> — an invented
	// owner is a worse failure than a missing one, because it grants access.
	// Such a path falls through to the global reading, where a private scope
	// claim fails loudly instead; internal/app settles the scope for the private
	// stores it builds itself, since it knows whose they are.
	if isWithinRoot(root, path) || isWithinRoot(knowledgeLocationRoots.Global, path) {
		return locationScope{Kind: locationGlobal}, true
	}
	return locationScope{}, false
}

// pathSegments splits a path into its segments, dropping "." and ".." entries a
// caller may have left in by joining a "." prefix onto a relative path. Path
// separators are normalized to forward slashes first so the same convention is
// recognized regardless of platform.
//
// A leading empty segment is PRESERVED for an absolute path — "/ws/.agents"
// yields ["", "ws", ".agents"]. It is the only record that the path was
// absolute, and joinSegments relies on it to rebuild one; dropping it here
// would make an absolute config root indistinguishable from a relative one and
// any path derived from it would resolve against the process cwd.
func pathSegments(path string) []string {
	var segments []string
	for i, segment := range strings.Split(filepath.ToSlash(filepath.Clean(path)), "/") {
		if segment == "." || segment == ".." {
			continue
		}
		if segment == "" {
			if i == 0 {
				segments = append(segments, "")
			}
			continue
		}
		segments = append(segments, segment)
	}
	return segments
}

// isWithinRoot reports whether path is root itself or sits underneath it. Both
// arguments may be relative or absolute; the comparison is made on path
// segments so a relative root and an absolute path never produce a spurious
// mismatch (filepath.Rel cannot compare across those).
func isWithinRoot(root, path string) bool {
	rootSegments := pathSegments(root)
	if len(rootSegments) == 0 {
		return false
	}
	pathSegments := pathSegments(path)
	if len(pathSegments) < len(rootSegments) {
		return false
	}
	for i, segment := range rootSegments {
		if pathSegments[i] != segment {
			return false
		}
	}
	return true
}

// reconcileScope validates an entry's frontmatter scope against the location it
// was loaded from, and fills in the effective visibility when the frontmatter
// declines to state one.
//
// It returns an error — not a warning — when the two disagree, because every
// disagreement is one of two failures and both are silent without it:
//
//   - A frontmatter scope *narrower* than its location (an entry in the global
//     directory declaring scope.type: agent) has no expression in the layout.
//     Nothing in a path filter can make that file private to one agent, so
//     converging scope onto path semantics turns a rule that used to be
//     enforced, and is now impossible to enforce, into a leak.
//   - A frontmatter scope claiming a *different* owner than the directory
//     (agents/a/knowledge/x.md declaring scope.agents: [b]) is worse: the
//     directory says A, the file says B, and the path filter will grant A.
//     Whichever of the two the author meant, one of them is being overruled
//     without notice.
//
// The error text is the whole product here. A validation failure on a tree the
// user did not write — the examples/ trees in this repository are exactly
// that — is only actionable if it names the file, quotes what the frontmatter
// says, states what the location implies, and says which one to change.
//
// Filling in the effective scope (rather than erroring) is the other half: a
// file with no scope at all sitting in agents/a/knowledge/ is unambiguous, and
// its *effective* visibility is A. Consumers that report or reason about an
// entry's visibility — describeScope in validation errors, the Scope field in
// an entry the write paths round-trip — read entry.Scope.Type, and would
// otherwise each have to re-derive the path convention, which is precisely the
// duplication this function exists to prevent. See settlePrivateScope for why
// the fill-in cannot be an error.
func (s *MarkdownStore) reconcileScope(entry *types.KnowledgeEntry) error {
	if entry == nil {
		return nil
	}
	location, ok := locationScopeOf(s.root, entry.Path)
	if !ok {
		// Not a layout-conformant knowledge location: nothing to validate.
		// Internal callers that already know which layer they loaded (the
		// app's per-agent private stores, whose root is
		// .agents/agents/<id>/knowledge) settle the scope themselves; doing
		// it for them here would mean guessing a location this function just
		// said it could not determine.
		return nil
	}

	conflict, fix := scopeLocationConflict(entry.Scope, location)
	if conflict == "" {
		if location.isPrivate() {
			settlePrivateScope(&entry.Scope, location)
		}
		return nil
	}
	return fmt.Errorf(
		"knowledge %s: frontmatter scope contradicts its location — scope says %s, "+
			"but the location is %s. %s. Fix: %s",
		entry.Path, describeScope(entry.Scope), location, conflict, fix)
}

// scopeLocationConflict compares a declared scope against the location that
// implies one, and returns a description of the disagreement plus the fix for
// it. An empty description means the two agree.
//
// It is a pure function of the two values — no store, no path — so the rule
// table is readable in one screen and testable without building a tree. The
// caller owns the decision to make a conflict fatal and the wording that names
// the file.
func scopeLocationConflict(scope types.Scope, location locationScope) (conflict, fix string) {
	scopeType := strings.ToLower(strings.TrimSpace(scope.Type))

	switch scopeType {
	case "", "all", "flow":
		// Visible to everyone: expressible from any location, and everywhere it
		// is stated it agrees with the global one. Inside a private directory
		// it is not a contradiction the caller should reject — see
		// settlePrivateScope, which reads it as "private, to the owner" because
		// that is the only reading a path filter can implement. Leaving it as
		// Type="" would not make the entry global either (the path still hides
		// it); it would only make the in-memory model disagree with the disk.
		return "", ""

	case "agent":
		if len(nonEmpty(scope.Agents)) == 0 {
			// Private to nobody. The entry is not a leak (the shared directory
			// is readable by everyone, so nothing here can hide it) but it is
			// a *dead* claim: the frontmatter says "one agent may read this"
			// and no path can express that, so what the file says and what the
			// filter does cannot be reconciled. It reaches here from the write
			// side (Save cannot derive a placement for an ownerless private
			// scope, so it puts the file in the shared directory) and from
			// hand-written frontmatter that trimmed the list. Either way the
			// fix is to name the owner or drop the scope, and saying so beats
			// letting the file sit there asserting something untrue.
			return "the file declares agent-private scope but names no agent in scope.agents, " +
					"and no path can make it private to nobody",
				"name the owner in scope.agents, or drop scope.type to make the entry visible to everyone"
		}
		if !location.isPrivate() {
			return "an agent-private entry cannot live in the shared knowledge directory, " +
					"where a path filter has no way to tell one agent's files from another's " +
					"and every agent would therefore see it",
				"move the file to .agents/agents/<agent-id>/knowledge/"
		}
		if location.Kind != locationAgent {
			return "the file declares agent-private scope but sits in a team-private directory",
				"move the file to .agents/agents/<agent-id>/knowledge/, " +
					"or change scope.type to team and list that team"
		}
		if !containsTrimmed(scope.Agents, location.OwnerID) {
			return fmt.Sprintf("the file declares agent-private scope for %s, but it sits in the private "+
					"directory of agent %q — and the directory, not the frontmatter, is what a path "+
					"filter grants access by", quoteList(scope.Agents), location.OwnerID),
				fmt.Sprintf("move the file to .agents/agents/%s/knowledge/ to match its scope, "+
					"or change scope.agents to [%s]", firstNonEmpty(scope.Agents), location.OwnerID)
		}
		return "", ""

	case "team":
		if len(nonEmpty(scope.Teams)) == 0 {
			// See the agent case above: a private claim naming no owner cannot
			// be expressed by any path, so it is rejected rather than left
			// asserting a visibility the layout does not implement.
			return "the file declares team-private scope but names no team in scope.teams, " +
					"and no path can make it private to nobody",
				"name the owner in scope.teams, or drop scope.type to make the entry visible to everyone"
		}
		if !location.isPrivate() {
			return "a team-private entry cannot live in the shared knowledge directory, " +
					"where a path filter cannot tell which team owns it and every team would " +
					"therefore see it",
				"move the file to .agents/teams/<team-id>/knowledge/"
		}
		if location.Kind != locationTeam {
			return "the file declares team-private scope but sits in an agent-private directory",
				"move the file to .agents/teams/<team-id>/knowledge/, " +
					"or change scope.type to agent and list that agent"
		}
		if !containsTrimmed(scope.Teams, location.OwnerID) {
			return fmt.Sprintf("the file declares team-private scope for %s, but it sits in the private "+
					"directory of team %q — and the directory, not the frontmatter, is what a path "+
					"filter grants access by", quoteList(scope.Teams), location.OwnerID),
				fmt.Sprintf("move the file to .agents/teams/%s/knowledge/ to match its scope, "+
					"or change scope.teams to [%s]", firstNonEmpty(scope.Teams), location.OwnerID)
		}
		return "", ""

	default:
		// An unrecognized scope.type is read as "visible to everyone" by the
		// location rule (there is no longer a frontmatter predicate to
		// consult), so continuing to do that here is not a widening change,
		// and rejecting it would break trees whose frontmatter still uses a
		// legacy spelling — a data problem this batch was told not to create.
		// The tree this repository ships is full of them: examples/ writes
		// `scope.type: agents` and the internal proposed/ format writes
		// `scope: workspace` / `scope: agent` as a bare string. Those fail the
		// frontmatter decode before they ever reach this function
		// (types.Scope is a struct; a scalar cannot unmarshal into it), which
		// is a louder and more precise failure than anything here could give.
		return "", ""
	}
}

// describeScope renders a declared scope for an error message, naming the owner
// lists only when the type makes them meaningful — quoting `agents: [b]` next to
// a flow scope would suggest the engine reads that field in cases where it does
// not.
func describeScope(scope types.Scope) string {
	scopeType := strings.ToLower(strings.TrimSpace(scope.Type))
	switch scopeType {
	case "agent":
		if len(nonEmpty(scope.Agents)) == 0 {
			return "agent-private, with no scope.agents owner"
		}
		return fmt.Sprintf("agent-private to %s", quoteList(scope.Agents))
	case "team":
		if len(nonEmpty(scope.Teams)) == 0 {
			return "team-private, with no scope.teams owner"
		}
		return fmt.Sprintf("team-private to %s", quoteList(scope.Teams))
	case "all":
		return "visible to everyone (scope.type: all)"
	case "flow":
		return "visible to everyone (scope.type: flow)"
	case "":
		return "unscoped (no scope.type)"
	default:
		return fmt.Sprintf("scope.type: %q, which is not one of flow/team/agent", scope.Type)
	}
}

// settlePrivateScope rewrites an entry's effective scope to the private owner
// its location names.
//
// This is a load-path normalization only: it touches the in-memory entry and
// never the file. Nothing on the read path writes back — that is Save's and
// UpsertActive's job, and a Load that rewrote files would make reading a tree
// mutate it. The distinction matters because Save writes entry.Scope back out
// as frontmatter: a normalized entry that a caller then saves does pick the
// scope up, which is the desired outcome (it now says what it already meant)
// and the reason this is done once, at load, rather than at every read.
//
// Both lists are set, not just the one matching the kind, because which list a
// consumer reads depends on Type alone; filling the other would advertise an
// owner that does not apply.
func settlePrivateScope(scope *types.Scope, location locationScope) {
	switch location.Kind {
	case locationAgent:
		scope.Type = "agent"
		scope.Agents = []string{location.OwnerID}
		scope.Teams = nil
	case locationTeam:
		scope.Type = "team"
		scope.Teams = []string{location.OwnerID}
		scope.Agents = nil
	}
}

// nonEmpty returns the trimmed, non-empty entries of values. Frontmatter lists
// are hand-written, so "agents: [ ]" and "agents: [\"\"]" both mean "nobody",
// and a length check alone would call the second one an owner.
func nonEmpty(values []string) []string {
	var result []string
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			result = append(result, trimmed)
		}
	}
	return result
}

// containsTrimmed reports whether values holds target, comparing with
// surrounding whitespace ignored. Frontmatter lists are hand-written, so
// "agents: [ a ]" and "agents: [a]" must mean the same thing.
func containsTrimmed(values []string, target string) bool {
	for _, value := range values {
		if strings.TrimSpace(value) == target {
			return true
		}
	}
	return false
}

// quoteList renders a name list for an error message, or a placeholder when
// the list is empty — an error that prints "scope.agents is []" reads as an
// engine bug rather than as "you forgot the owner".
func quoteList(values []string) string {
	if len(values) == 0 {
		return "no agent"
	}
	quoted := make([]string, 0, len(values))
	for _, value := range values {
		quoted = append(quoted, fmt.Sprintf("%q", strings.TrimSpace(value)))
	}
	return strings.Join(quoted, ", ")
}

// firstNonEmpty returns the first value, or a placeholder, so the suggested fix
// never contains an empty path segment.
func firstNonEmpty(values []string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return "<agent-id>"
}

// placementPath decides where a new entry's file goes when the caller did not
// name a path.
//
// It is derived from the entry's scope, because scope is what the loader checks
// the path against — write the file somewhere the reader will reject and
// `heron knowledge` (the learn path in cmd/server) produces entries that
// vanish on the next start with a validation error rather than a working
// knowledge base.
//
// The mapping is the layout convention from locationScopeOf, not the older
// type-scoped one:
//
//	flow / all / (unset)       → <root>/<id>.md              (global)
//	agent, agents: [a]         → <root>/agents/a/knowledge/<id>.md
//	team,  teams:  [t]         → <root>/teams/t/knowledge/<id>.md
//
// `<root>` is the global knowledge directory exactly as the loader sees it
// (".agents/knowledge" in the process-relative case), because a relative path
// is built by appending only forward segments to it. The agent and team
// branches therefore *do* reach outside the store's own root — the store is
// rooted at .agents/knowledge, and an agent-private entry lives at
// .agents/agents/<a>/knowledge — which is why ensureWithinRoot was widened to
// accept an entry that a from-root derivation places back inside the config
// root. See its comment: the invariant that matters is "no path escapes the
// config root", not "no path escapes the store root".
//
// The two previous shapes this replaces were:
//
//	<root>/flow/<id>.md, <root>/team/<id>.md, <root>/agent/<id>.md
//
// and they are gone for a reason worth stating, because they were not merely
// stale: `<root>/agent/<id>.md` put a file *inside the global knowledge tree*
// while its frontmatter claimed to be private to one agent. The loader now
// rejects exactly that (an agent-scoped entry in the shared directory cannot be
// enforced by a path filter, so it would leak to every agent), and a
// type-scoped directory cannot express an owner at all — nothing in
// "knowledge/agent/" says *which* agent. The owner-scoped layout is the one the
// reader can enforce, so the writer has to produce it; the type-scoped
// directories are no longer written, and files already in them simply load
// without a match.
//
// An agent- or team-scoped entry with an empty owner list has no owner to pick,
// so it falls back to the flow location. That is deliberate and it is the
// conservative choice available here: the alternative is inventing an owner.
// Such an entry then fails validation on load ("agent-private, with no
// scope.agents owner" in the shared directory), which is loud and correct —
// the frontmatter never named anyone who could be granted access.
func (s *MarkdownStore) placementPath(entry types.KnowledgeEntry) string {
	if entry.Path != "" {
		return entry.Path
	}
	root := s.globalRoot()
	name := entry.ID + ".md"

	switch strings.ToLower(strings.TrimSpace(entry.Scope.Type)) {
	case "agent":
		if owner := firstNonEmpty(entry.Scope.Agents); owner != "<agent-id>" {
			return filepath.Join(s.privateRoot(), "agents", owner, "knowledge", name)
		}
	case "team":
		if owner := firstNonEmpty(entry.Scope.Teams); owner != "<agent-id>" {
			return filepath.Join(s.privateRoot(), "teams", owner, "knowledge", name)
		}
	}
	return filepath.Join(root, name)
}

// globalRoot is the store's own root when the store is the global knowledge
// store, and the config root's knowledge directory when it is not — which is
// how a global store rooted at ".agents/knowledge" and a private store rooted
// at ".agents/agents/<id>/knowledge" both produce paths under the same
// ".agents" prefix.
func (s *MarkdownStore) globalRoot() string {
	if root, ok := s.configRoot(); ok {
		return filepath.Join(root, knowledgeLocationRoots.Global)
	}
	return s.root
}

// privateRoot is the config root (".agents"), derived from the store root so
// the same relative prefix is used no matter which of the two store shapes is
// in play.
func (s *MarkdownStore) privateRoot() string {
	if root, ok := s.configRoot(); ok {
		return root
	}
	return s.root
}

// configRoot derives the ".agents" directory or an equivalent absolute path
// from the store's root, by trimming the trailing knowledge-location segments
// (and, for a private store, the owner segments above them). It reports false
// when the root is not a recognizable knowledge location at all, in which case
// callers treat the store root as the base — the same fallback the old
// type-scoped layout used, and the only option that keeps a store built over
// an arbitrary directory usable.
func (s *MarkdownStore) configRoot() (string, bool) {
	segments := pathSegments(s.root)
	// Walk to the last "knowledge" segment, which is the directory holding the
	// files this store lists. Everything before it is the config root, or an
	// owner directory inside one.
	knowledgeAt := -1
	for i := len(segments) - 1; i >= 0; i-- {
		if segments[i] == knowledgeLocationRoots.Global {
			knowledgeAt = i
			break
		}
	}
	if knowledgeAt < 0 {
		return "", false
	}

	// A private store's root is <configRoot>/agents/<id>/knowledge (or
	// .../teams/<id>/knowledge): directly above "knowledge" sit the owner id and
	// the owner directory, so the config root is two segments further up than it
	// is for a global store. Checking ownership this way — rather than by "does
	// an agents/ segment appear anywhere" — keeps a global store rooted at
	// .agents/knowledge/configRoot from being mistaken for a private one.
	if knowledgeAt >= 2 {
		ownerDir := segments[knowledgeAt-2]
		if ownerDir == knowledgeLocationRoots.Agents || ownerDir == knowledgeLocationRoots.Teams {
			return joinSegments(segments[:knowledgeAt-2]), true
		}
	}
	return joinSegments(segments[:knowledgeAt]), true
}

// joinSegments rebuilds a path from segments. Segment lists produced by
// pathSegments carry a leading "" for an absolute path (the empty first field of
// "/ws/.agents"), and filepath.Join drops it — so that marker is turned back
// into a leading separator here. Losing it would silently turn an absolute
// config root into a relative one, and a path derived from it would then be
// resolved against the process cwd instead of the filesystem root.
//
// An empty list means the whole path was consumed, which only happens for a
// store rooted at the filesystem root. That is returned as "/" rather than ""
// because filepath.Join treats "" as its next argument.
func joinSegments(segments []string) string {
	if len(segments) == 0 {
		return "/"
	}
	if segments[0] == "" {
		return "/" + filepath.Join(segments[1:]...)
	}
	return filepath.Join(segments...)
}

// scopeDir is the legacy type-scoped directory mapping. It is no longer used by
// Save (see placementPath) and survives only because the layered-layout test
// pins its behaviour.
//
// Deprecated: the type-scoped layout cannot express an owner, and a private
// knowledge file without an owner directory is unenforceable by path.
func scopeDir(scopeType string) string {
	switch scopeType {
	case "team", "agent":
		return scopeType
	default:
		return "flow"
	}
}
