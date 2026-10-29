package state

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/adrg/frontmatter"
	"gopkg.in/yaml.v3"

	"github.com/heron-ai/heron-engine/internal/agentstore"
	"github.com/heron-ai/heron-engine/internal/storage"
	"github.com/heron-ai/heron-engine/pkg/types"
)

const (
	defaultTeamMaxChars       = 4000
	defaultAgentMaxChars      = 2000
	defaultAgentStateMaxChars = 4000
)

// Limits controls the hard size of state.md snapshots.
type Limits struct {
	TeamMaxChars       int
	AgentMaxChars      int
	AgentStateMaxChars int
	MaxItems           int
}

// Store persists the two V1 state layers (team and agent).
type Store struct {
	files  storage.FileStore
	limits Limits
	locks  *agentstore.AgentStateLocks
}

func NewStore(files storage.FileStore, limits Limits) *Store {
	if limits.TeamMaxChars <= 0 {
		limits.TeamMaxChars = defaultTeamMaxChars
	}
	if limits.AgentMaxChars <= 0 {
		limits.AgentMaxChars = defaultAgentMaxChars
	}
	if limits.AgentStateMaxChars <= 0 {
		limits.AgentStateMaxChars = defaultAgentStateMaxChars
	}
	if limits.MaxItems <= 0 {
		limits.MaxItems = 40
	}
	return &Store{files: files, limits: limits, locks: agentstore.NewAgentStateLocks()}
}

// SetLocks shares an external agent-level write lock set. When nil, the Store
// uses its own private lock set. The CRUD operations use it to guard the
// read-modify-write cycle of one agent's state.
func (s *Store) SetLocks(locks *agentstore.AgentStateLocks) {
	if locks != nil {
		s.locks = locks
	}
}

// ForConfig returns a view of the Store with the limits configured by one
// Team. The underlying FileStore is shared, while the limits remain scoped to
// the current TeamTurn. This keeps StateConfig effective even when a
// runtime contains several Teams with different bounds.
func (s *Store) ForConfig(config types.StateConfig) *Store {
	if s == nil {
		return nil
	}
	limits := s.limits
	if config.MaxItems > 0 {
		limits.MaxItems = config.MaxItems
	}
	if config.MaxChars > 0 {
		// StateConfig intentionally exposes one compact size budget. Apply it
		// to both bounded snapshots so Team and Agent state obey the same
		// project-level contract.
		limits.TeamMaxChars = config.MaxChars
		limits.AgentMaxChars = config.MaxChars
	}
	return &Store{files: s.files, limits: limits, locks: s.locks}
}

func (s *Store) LoadTeam(ctx context.Context, sessionID, teamID string) (types.StateSnapshot, error) {
	return s.load(ctx, s.path(sessionID, "teams", teamID, "state.md"), types.StateScopeTeam, sessionID, teamID, "")
}

func (s *Store) SaveTeam(ctx context.Context, snapshot types.StateSnapshot) error {
	snapshot.Scope = types.StateScopeTeam
	return s.save(ctx, s.path(snapshot.SessionID, "teams", snapshot.TeamID, "state.md"), snapshot, s.limits.TeamMaxChars)
}

func (s *Store) LoadAgent(ctx context.Context, sessionID, teamID, callID string) (types.StateSnapshot, error) {
	return s.load(ctx, s.path(sessionID, "agents", teamID, callID, "state.md"), types.StateScopeAgent, sessionID, teamID, callID)
}

func (s *Store) SaveAgent(ctx context.Context, snapshot types.StateSnapshot) error {
	snapshot.Scope = types.StateScopeAgent
	return s.save(ctx, s.path(snapshot.SessionID, "agents", snapshot.TeamID, snapshot.CallID, "state.md"), snapshot, s.limits.AgentMaxChars)
}

// LoadAgentState reads the persistent cross-session state of one Agent
// (design doc 26). The state is a directory of blocks; this assembles them into
// one snapshot for callers that think in fields. A missing state is not an
// error: it returns an empty snapshot.
func (s *Store) LoadAgentState(ctx context.Context, agentID string) (types.StateSnapshot, error) {
	if err := s.ensureAgentState(ctx, agentID); err != nil {
		return types.StateSnapshot{}, err
	}
	blocks, index, err := s.loadAgentBlocks(ctx, agentID)
	if err != nil {
		return types.StateSnapshot{}, err
	}
	return snapshotFromBlocks(blocks, index), nil
}

// SaveAgentState persists a whole snapshot. It exists for callers that already
// hold every field; block-scoped writes are the normal path and avoid
// overwriting blocks another instance changed in between.
func (s *Store) SaveAgentState(ctx context.Context, agentID string, snapshot types.StateSnapshot) error {
	if strings.TrimSpace(agentID) == "" {
		return errors.New("agent state agent is required")
	}
	if err := s.ensureAgentState(ctx, agentID); err != nil {
		return err
	}
	blocks := blocksFromSnapshot(snapshot)
	// One save is one revision. The blocks share it, so a snapshot round-trip
	// reports the number of saves rather than the number of files.
	revision := snapshot.Revision + 1
	now := time.Now().UTC()
	for _, name := range orderedBlocks {
		block := blocks[name]
		block.Revision = revision
		block.UpdatedAt = now
		if err := s.writeBlockFile(ctx, agentID, block, s.limits.AgentStateMaxChars); err != nil {
			return err
		}
		blocks[name] = block
	}
	return s.writeIndex(ctx, agentID, indexFromBlocks(blocks, revision, now))
}

// LoadIndex reads the summary that State(action="list") returns by default.
func (s *Store) LoadIndex(ctx context.Context, agentID string) (types.StateIndex, error) {
	if err := s.ensureAgentState(ctx, agentID); err != nil {
		return types.StateIndex{}, err
	}
	index, err := s.readIndexFile(ctx, agentID)
	if err != nil {
		return types.StateIndex{}, err
	}
	if len(index.Blocks) > 0 || index.Revision > 0 {
		return index, nil
	}
	// No index yet (a partially written directory, or a hand-made one): derive
	// it from whatever block files exist rather than pretending the state is
	// empty.
	blocks, _, err := s.loadAgentBlocks(ctx, agentID)
	if err != nil {
		return types.StateIndex{}, err
	}
	revision, updatedAt := 0, time.Time{}
	for _, block := range blocks {
		if block.Revision > revision {
			revision = block.Revision
		}
		if block.UpdatedAt.After(updatedAt) {
			updatedAt = block.UpdatedAt
		}
	}
	return indexFromBlocks(blocks, revision, updatedAt), nil
}

// LoadBlock reads one block. Unknown names are an error: a typo must not read
// as "empty block".
func (s *Store) LoadBlock(ctx context.Context, agentID, name string) (types.StateBlock, error) {
	if !validBlock(name) {
		return types.StateBlock{}, fmt.Errorf("state: unsupported block %q", name)
	}
	if err := s.ensureAgentState(ctx, agentID); err != nil {
		return types.StateBlock{}, err
	}
	return s.readBlockFile(ctx, agentID, name)
}

// SaveBlock writes one block and refreshes its index line. Only this block's
// file is rewritten, which is the point of the directory layout: two instances
// editing different blocks do not overwrite each other.
func (s *Store) SaveBlock(ctx context.Context, agentID string, block types.StateBlock) error {
	if strings.TrimSpace(agentID) == "" {
		return errors.New("agent state agent is required")
	}
	if !validBlock(block.Name) {
		return fmt.Errorf("state: unsupported block %q", block.Name)
	}
	if err := s.ensureAgentState(ctx, agentID); err != nil {
		return err
	}
	index, err := s.readIndexFile(ctx, agentID)
	if err != nil {
		return err
	}
	revision := index.Revision + 1
	block.Revision = revision
	block.UpdatedAt = time.Now().UTC()
	if err := s.writeBlockFile(ctx, agentID, block, s.limits.AgentStateMaxChars); err != nil {
		return err
	}
	return s.updateIndexLine(ctx, agentID, block, revision)
}

// SetGoalIfEmpty seeds the goal block without overwriting work the Agent has
// already claimed.
func (s *Store) SetGoalIfEmpty(ctx context.Context, agentID, text string) error {
	if strings.TrimSpace(text) == "" {
		return nil
	}
	block, err := s.LoadBlock(ctx, agentID, types.StateBlockGoal)
	if err != nil {
		return err
	}
	if strings.TrimSpace(block.Goal) != "" {
		return nil
	}
	block.Goal = text
	return s.SaveBlock(ctx, agentID, block)
}

// AppendWorkspace adds file references, skipping duplicates of the same path
// with the same revision.
func (s *Store) AppendWorkspace(ctx context.Context, agentID string, refs ...types.StateWorkspaceRef) error {
	block, err := s.LoadBlock(ctx, agentID, types.StateBlockWorkspace)
	if err != nil {
		return err
	}
	changed := false
	for _, ref := range refs {
		if ref.Path == "" {
			continue
		}
		duplicate := false
		for _, existing := range block.Workspace {
			if existing.Path == ref.Path && existing.Revision == ref.Revision {
				duplicate = true
				break
			}
		}
		if duplicate {
			continue
		}
		block.Workspace = append(block.Workspace, ref)
		changed = true
	}
	if !changed {
		return nil
	}
	return s.SaveBlock(ctx, agentID, block)
}

// AppendRecordIDs adds SharedRecord references, skipping ids already present.
func (s *Store) AppendRecordIDs(ctx context.Context, agentID string, ids ...string) error {
	block, err := s.LoadBlock(ctx, agentID, types.StateBlockRecordIDs)
	if err != nil {
		return err
	}
	seen := make(map[string]bool, len(block.RecordIDs))
	for _, id := range block.RecordIDs {
		seen[id] = true
	}
	changed := false
	for _, id := range ids {
		if strings.TrimSpace(id) == "" || seen[id] {
			continue
		}
		block.RecordIDs = append(block.RecordIDs, id)
		seen[id] = true
		changed = true
	}
	if !changed {
		return nil
	}
	return s.SaveBlock(ctx, agentID, block)
}

// loadAgentBlocks reads every block file plus the index.
func (s *Store) loadAgentBlocks(ctx context.Context, agentID string) (map[string]types.StateBlock, types.StateIndex, error) {
	blocks := make(map[string]types.StateBlock, len(orderedBlocks))
	for _, name := range orderedBlocks {
		block, err := s.readBlockFile(ctx, agentID, name)
		if err != nil {
			return nil, types.StateIndex{}, err
		}
		blocks[name] = block
	}
	index, err := s.readIndexFile(ctx, agentID)
	if err != nil {
		return nil, types.StateIndex{}, err
	}
	return blocks, index, nil
}

func (s *Store) readBlockFile(ctx context.Context, agentID, name string) (types.StateBlock, error) {
	if err := contextErr(ctx); err != nil {
		return types.StateBlock{}, err
	}
	data, err := s.files.Read(agentBlockPath(agentID, name))
	if errors.Is(err, storage.ErrNotFound) {
		return types.StateBlock{Name: name}, nil
	}
	if err != nil {
		return types.StateBlock{}, err
	}
	return decodeBlock(name, data)
}

// writeBlockFile encodes and stores one block, reducing it when it exceeds the
// per-block budget. The order is deliberate: drop finished entries first, then
// shorten text, then drop references. A block that still does not fit is an
// error rather than a silent truncation.
func (s *Store) writeBlockFile(ctx context.Context, agentID string, block types.StateBlock, maxChars int) error {
	if err := contextErr(ctx); err != nil {
		return err
	}
	if maxChars <= 0 {
		maxChars = defaultAgentStateMaxChars
	}
	encode := func() ([]byte, error) { return encodeBlock(block) }
	if listBlocks[block.Name] {
		block.Items = reduceItems(block.Items, s.limits.MaxItems)
	}
	data, err := encode()
	if err != nil {
		return err
	}
	if len(data) > maxChars && listBlocks[block.Name] {
		block.Items = reduceItems(block.Items, 10)
		if data, err = encode(); err != nil {
			return err
		}
	}
	if len(data) > maxChars && listBlocks[block.Name] {
		for i := range block.Items {
			block.Items[i].Text = trimText(block.Items[i].Text, maxInt(maxChars/10, 20))
		}
		if data, err = encode(); err != nil {
			return err
		}
	}
	if len(data) > maxChars && block.Name == types.StateBlockGoal {
		block.Goal = trimText(block.Goal, maxInt(maxChars/2, 40))
		if data, err = encode(); err != nil {
			return err
		}
	}
	if len(data) > maxChars && block.Name == types.StateBlockWorkspace {
		for len(block.Workspace) > 0 && len(data) > maxChars {
			block.Workspace = block.Workspace[:len(block.Workspace)/2]
			if data, err = encode(); err != nil {
				return err
			}
		}
	}
	if len(data) > maxChars && block.Name == types.StateBlockRecordIDs {
		for len(block.RecordIDs) > 0 && len(data) > maxChars {
			block.RecordIDs = block.RecordIDs[:len(block.RecordIDs)/2]
			if data, err = encode(); err != nil {
				return err
			}
		}
	}
	if len(data) > maxChars {
		return fmt.Errorf("state block %s exceeds max_chars=%d after reduction", block.Name, maxChars)
	}
	return s.files.Write(agentBlockPath(agentID, block.Name), data)
}

func trimText(text string, limit int) string {
	if limit <= 0 || len(text) <= limit {
		return text
	}
	return text[:limit] + "…"
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func (s *Store) readIndexFile(ctx context.Context, agentID string) (types.StateIndex, error) {
	if err := contextErr(ctx); err != nil {
		return types.StateIndex{}, err
	}
	data, err := s.files.Read(agentIndexPath(agentID))
	if errors.Is(err, storage.ErrNotFound) {
		return types.StateIndex{}, nil
	}
	if err != nil {
		return types.StateIndex{}, err
	}
	return decodeIndex(data)
}

func (s *Store) writeIndex(ctx context.Context, agentID string, index types.StateIndex) error {
	if err := contextErr(ctx); err != nil {
		return err
	}
	return s.files.Write(agentIndexPath(agentID), encodeIndex(index))
}

// updateIndexLine rewrites the index with one block's new metadata. The whole
// index is small and derived, so rewriting it is cheaper than maintaining a
// merge algorithm.
func (s *Store) updateIndexLine(ctx context.Context, agentID string, block types.StateBlock, revision int) error {
	index, err := s.readIndexFile(ctx, agentID)
	if err != nil {
		return err
	}
	meta := types.StateBlockMeta{
		Name: block.Name, Revision: block.Revision, UpdatedAt: block.UpdatedAt,
	}
	switch {
	case block.Name == types.StateBlockGoal:
		index.Goal = block.Goal
		if strings.TrimSpace(block.Goal) != "" {
			meta.Entries = 1
		}
	case block.Name == types.StateBlockWorkspace:
		meta.Entries = len(block.Workspace)
	case block.Name == types.StateBlockRecordIDs:
		meta.Entries = len(block.RecordIDs)
	default:
		meta.Entries = len(block.Items)
		for _, item := range block.Items {
			if item.Status == types.StateStatusInProgress {
				meta.InProgress++
			}
		}
	}
	replaced := false
	for i := range index.Blocks {
		if index.Blocks[i].Name == block.Name {
			index.Blocks[i] = meta
			replaced = true
			break
		}
	}
	if !replaced {
		index.Blocks = append(index.Blocks, meta)
		sort.Slice(index.Blocks, func(i, j int) bool {
			return blockOrder(index.Blocks[i].Name) < blockOrder(index.Blocks[j].Name)
		})
	}
	index.Revision = revision
	index.UpdatedAt = block.UpdatedAt
	return s.writeIndex(ctx, agentID, index)
}

func blockOrder(name string) int {
	for i, block := range orderedBlocks {
		if block == name {
			return i
		}
	}
	return len(orderedBlocks)
}

// ensureAgentState migrates the pre-directory single file on first access.
func (s *Store) ensureAgentState(ctx context.Context, agentID string) error {
	if strings.TrimSpace(agentID) == "" {
		return errors.New("agent state agent is required")
	}
	if s.files.Exists(agentIndexPath(agentID)) {
		return nil
	}
	legacy := agentLegacyPath(agentID)
	if !s.files.Exists(legacy) {
		return nil
	}
	return s.migrateLegacy(ctx, agentID, legacy)
}

// migrateLegacy splits state.md into blocks and keeps the original as
// state.md.bak. The backup is deliberate: migration is the only point where
// data changes shape, and the old file is the only way back.
func (s *Store) migrateLegacy(ctx context.Context, agentID, legacy string) error {
	snapshot, err := s.load(ctx, legacy, types.StateScopeAgent, "", "", "")
	if err != nil {
		return fmt.Errorf("migrate legacy state: %w", err)
	}
	data, err := s.files.Read(legacy)
	if err != nil {
		return err
	}
	blocks := blocksFromSnapshot(snapshot)
	revision := snapshot.Revision
	updatedAt := snapshot.UpdatedAt.UTC()
	if updatedAt.IsZero() {
		updatedAt = time.Now().UTC()
	}
	for _, name := range orderedBlocks {
		block := blocks[name]
		block.Revision = revision
		block.UpdatedAt = updatedAt
		if err := s.writeBlockFile(ctx, agentID, block, s.limits.AgentStateMaxChars); err != nil {
			return err
		}
		blocks[name] = block
	}
	if err := s.writeIndex(ctx, agentID, indexFromBlocks(blocks, revision, updatedAt)); err != nil {
		return err
	}
	if err := s.files.Write(filepath.Join(agentStateDir(agentID), legacyBackup), data); err != nil {
		return err
	}
	return s.files.Delete(legacy)
}

// snapshotFromBlocks assembles the field view used by callers that still think
// in one snapshot.
func snapshotFromBlocks(blocks map[string]types.StateBlock, index types.StateIndex) types.StateSnapshot {
	updatedAt := index.UpdatedAt
	if updatedAt.IsZero() {
		for _, block := range blocks {
			if block.UpdatedAt.After(updatedAt) {
				updatedAt = block.UpdatedAt
			}
		}
	}
	return types.StateSnapshot{
		Scope:         types.StateScopeAgent,
		Revision:      index.Revision,
		Goal:          blocks[types.StateBlockGoal].Goal,
		Confirmed:     blocks[types.StateBlockConfirmed].Items,
		OpenQuestions: blocks[types.StateBlockOpenQuestions].Items,
		Decisions:     blocks[types.StateBlockDecisions].Items,
		NextSteps:     blocks[types.StateBlockNextSteps].Items,
		Workspace:     blocks[types.StateBlockWorkspace].Workspace,
		RecordIDs:     blocks[types.StateBlockRecordIDs].RecordIDs,
		UpdatedAt:     updatedAt,
	}
}

func blocksFromSnapshot(snapshot types.StateSnapshot) map[string]types.StateBlock {
	return map[string]types.StateBlock{
		types.StateBlockGoal:          {Name: types.StateBlockGoal, Goal: snapshot.Goal},
		types.StateBlockConfirmed:     {Name: types.StateBlockConfirmed, Items: snapshot.Confirmed},
		types.StateBlockOpenQuestions: {Name: types.StateBlockOpenQuestions, Items: snapshot.OpenQuestions},
		types.StateBlockDecisions:     {Name: types.StateBlockDecisions, Items: snapshot.Decisions},
		types.StateBlockNextSteps:     {Name: types.StateBlockNextSteps, Items: snapshot.NextSteps},
		types.StateBlockWorkspace:     {Name: types.StateBlockWorkspace, Workspace: snapshot.Workspace},
		types.StateBlockRecordIDs:     {Name: types.StateBlockRecordIDs, RecordIDs: snapshot.RecordIDs},
	}
}

// reduceItems keeps unfinished entries first. A completed entry survives only
// while there is budget left; dropping one loses a status, not a task.
func reduceItems(items []types.StateItem, max int) []types.StateItem {
	if max <= 0 {
		max = 40
	}
	if len(items) <= max {
		return items
	}
	var open, done []types.StateItem
	for _, item := range items {
		if item.Status == types.StateStatusCompleted {
			done = append(done, item)
			continue
		}
		open = append(open, item)
	}
	if len(open) >= max {
		return tail(open, max)
	}
	return append(open, tail(done, max-len(open))...)
}

func (s *Store) path(sessionID string, parts ...string) string {
	all := append([]string{".agents", "data", "sessions", sessionID}, parts...)
	return filepath.Join(all...)
}

func (s *Store) load(
	ctx context.Context,
	path string,
	scope types.StateScope,
	sessionID string,
	teamID string,
	callID string,
) (types.StateSnapshot, error) {
	if err := contextErr(ctx); err != nil {
		return types.StateSnapshot{}, err
	}
	data, err := s.files.Read(path)
	if errors.Is(err, storage.ErrNotFound) {
		return types.StateSnapshot{
			Scope:     scope,
			SessionID: sessionID,
			TeamID:    teamID,
			CallID:    callID,
			Revision:  0,
		}, nil
	}
	if err != nil {
		return types.StateSnapshot{}, err
	}

	var snapshot types.StateSnapshot
	if _, err := frontmatter.Parse(strings.NewReader(string(data)), &snapshot); err != nil {
		return types.StateSnapshot{}, fmt.Errorf("parse state %s: %w", path, err)
	}
	if snapshot.Scope == "" {
		snapshot.Scope = scope
	}
	if snapshot.SessionID == "" {
		snapshot.SessionID = sessionID
	}
	if snapshot.TeamID == "" {
		snapshot.TeamID = teamID
	}
	if snapshot.CallID == "" {
		snapshot.CallID = callID
	}
	return snapshot, nil
}

func (s *Store) save(ctx context.Context, path string, snapshot types.StateSnapshot, maxChars int) error {
	if err := contextErr(ctx); err != nil {
		return err
	}
	if snapshot.Scope == types.StateScopeTeam {
		if snapshot.SessionID == "" || snapshot.TeamID == "" {
			return errors.New("state session_id and team_id are required")
		}
	}
	if snapshot.Scope == types.StateScopeAgent && snapshot.SessionID != "" && snapshot.CallID == "" {
		return errors.New("agent state call_id is required")
	}
	snapshot = Reduce(snapshot, s.limits.MaxItems)
	snapshot.Revision++
	snapshot.UpdatedAt = time.Now().UTC()

	data, err := encode(snapshot)
	if err != nil {
		return err
	}
	if len(data) > maxChars {
		snapshot = ReduceAggressively(snapshot)
		data, err = encode(snapshot)
		if err != nil {
			return err
		}
	}
	if len(data) > maxChars {
		// State is a bounded hint, not a reason to fail the whole TeamTurn.
		// Keep the newest compact facts and truncate individual text fields as
		// a final safety valve. The complete reply remains in session/evidence.
		snapshot = ReduceForSize(snapshot, maxChars)
		data, err = encode(snapshot)
		if err != nil {
			return err
		}
	}
	if len(data) > maxChars {
		// The YAML/body headings have a fixed overhead. If the configured cap
		// is smaller than that overhead, persist an empty bounded snapshot
		// rather than failing the call execution.
		snapshot.Goal = ""
		snapshot.Confirmed = nil
		snapshot.OpenQuestions = nil
		snapshot.Decisions = nil
		snapshot.NextSteps = nil
		snapshot.Workspace = nil
		snapshot.RecordIDs = nil
		data, err = encode(snapshot)
		if err != nil {
			return err
		}
	}
	if len(data) > maxChars {
		return fmt.Errorf("state exceeds max_chars=%d after reduction", maxChars)
	}
	return s.files.Write(path, data)
}

// Reduce keeps the fixed snapshot bounded without turning state into a log.
func Reduce(snapshot types.StateSnapshot, maxItems int) types.StateSnapshot {
	if maxItems <= 0 {
		maxItems = 40
	}
	snapshot.Confirmed = tail(snapshot.Confirmed, maxItems)
	snapshot.OpenQuestions = tail(snapshot.OpenQuestions, maxItems)
	snapshot.Decisions = tail(snapshot.Decisions, maxItems)
	snapshot.NextSteps = tail(snapshot.NextSteps, maxItems)
	snapshot.Workspace = tail(snapshot.Workspace, maxItems)
	snapshot.RecordIDs = tail(snapshot.RecordIDs, maxItems)
	return snapshot
}

func ReduceAggressively(snapshot types.StateSnapshot) types.StateSnapshot {
	snapshot.Confirmed = tail(snapshot.Confirmed, 10)
	snapshot.OpenQuestions = tail(snapshot.OpenQuestions, 10)
	snapshot.Decisions = tail(snapshot.Decisions, 10)
	snapshot.NextSteps = tail(snapshot.NextSteps, 10)
	snapshot.Workspace = tail(snapshot.Workspace, 10)
	snapshot.RecordIDs = tail(snapshot.RecordIDs, 10)
	if len(snapshot.Goal) > 1000 {
		snapshot.Goal = snapshot.Goal[:1000]
	}
	return snapshot
}

// ReduceForSize makes state best-effort bounded even when a model returns a
// long explanatory reply. It intentionally drops old lists before shortening
// the current goal, because session.jsonl and SharedRecord remain authoritative.
func ReduceForSize(snapshot types.StateSnapshot, maxChars int) types.StateSnapshot {
	if maxChars <= 0 {
		maxChars = defaultAgentMaxChars
	}
	snapshot.Confirmed = tail(snapshot.Confirmed, 2)
	snapshot.OpenQuestions = tail(snapshot.OpenQuestions, 2)
	snapshot.Decisions = tail(snapshot.Decisions, 2)
	snapshot.NextSteps = tail(snapshot.NextSteps, 2)
	snapshot.Workspace = tail(snapshot.Workspace, 2)
	snapshot.RecordIDs = tail(snapshot.RecordIDs, 5)

	trim := func(value string, limit int) string {
		if len(value) <= limit {
			return value
		}
		return value[:limit] + "…"
	}
	snapshot.Goal = trim(snapshot.Goal, maxChars/5)
	for i := range snapshot.Confirmed {
		snapshot.Confirmed[i].Text = trim(snapshot.Confirmed[i].Text, maxChars/5)
	}
	for i := range snapshot.NextSteps {
		snapshot.NextSteps[i].Text = trim(snapshot.NextSteps[i].Text, maxChars/5)
	}
	for i := range snapshot.Decisions {
		snapshot.Decisions[i].Text = trim(snapshot.Decisions[i].Text, maxChars/5)
	}
	for i := range snapshot.OpenQuestions {
		snapshot.OpenQuestions[i].Text = trim(snapshot.OpenQuestions[i].Text, maxChars/5)
	}
	return snapshot
}

func tail[T any](items []T, max int) []T {
	if len(items) <= max {
		return items
	}
	return items[len(items)-max:]
}

func encode(snapshot types.StateSnapshot) ([]byte, error) {
	front, err := yaml.Marshal(snapshot)
	if err != nil {
		return nil, fmt.Errorf("marshal state frontmatter: %w", err)
	}
	var body strings.Builder
	body.WriteString("---\n")
	body.Write(front)
	body.WriteString("---\n\n")
	body.WriteString("# Goal\n\n")
	body.WriteString(snapshot.Goal)
	body.WriteString("\n\n# Confirmed\n\n")
	writeList(&body, snapshot.Confirmed)
	body.WriteString("\n# Open Questions\n\n")
	writeList(&body, snapshot.OpenQuestions)
	body.WriteString("\n# Decisions\n\n")
	writeList(&body, snapshot.Decisions)
	body.WriteString("\n# Next Steps\n\n")
	writeList(&body, snapshot.NextSteps)
	body.WriteString("\n# Workspace\n\n")
	for _, item := range snapshot.Workspace {
		_, _ = fmt.Fprintf(&body, "- %s (%s)\n", item.Path, item.Revision)
	}
	body.WriteString("\n# SharedRecord Refs\n\n")
	writeStrings(&body, snapshot.RecordIDs)
	return []byte(body.String()), nil
}

// writeList renders a StateItem todo list as markdown bullets "id: text". The
// id is preserved so the CRUD tools can reference entries precisely. AddedBy
// and HandledBy are rendered only when non-empty so the whiteboard stays
// traceable without cluttering ordinary entries.
func writeList(builder *strings.Builder, items []types.StateItem) {
	for _, item := range items {
		if item.ID != "" {
			builder.WriteString("- ")
			builder.WriteString(item.ID)
			builder.WriteString(": ")
		} else {
			builder.WriteString("- ")
		}
		builder.WriteString(item.Text)
		if item.AddedBy != "" || item.HandledBy != "" {
			builder.WriteString(" [")
			if item.AddedBy != "" {
				builder.WriteString("added_by=")
				builder.WriteString(item.AddedBy)
			}
			if item.AddedBy != "" && item.HandledBy != "" {
				builder.WriteString(", ")
			}
			if item.HandledBy != "" {
				builder.WriteString("handled_by=")
				builder.WriteString(item.HandledBy)
			}
			builder.WriteString("]")
		}
		builder.WriteByte('\n')
	}
}

func writeStrings(builder *strings.Builder, items []string) {
	for _, item := range items {
		builder.WriteString("- ")
		builder.WriteString(item)
		builder.WriteByte('\n')
	}
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
