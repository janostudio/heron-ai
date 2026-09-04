package state

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/adrg/frontmatter"
	"gopkg.in/yaml.v3"

	"github.com/heron-ai/heron-engine/internal/storage"
	"github.com/heron-ai/heron-engine/pkg/types"
)

const (
	defaultTeamMaxChars   = 4000
	defaultAgentMaxChars  = 2000
	defaultEntityMaxChars = 4000
)

// Limits controls the hard size of state.md snapshots.
type Limits struct {
	TeamMaxChars   int
	AgentMaxChars  int
	EntityMaxChars int
	MaxItems       int
}

// Store persists the two V1 short-term state layers.
type Store struct {
	files  storage.FileStore
	limits Limits
}

func NewStore(files storage.FileStore, limits Limits) *Store {
	if limits.TeamMaxChars <= 0 {
		limits.TeamMaxChars = defaultTeamMaxChars
	}
	if limits.AgentMaxChars <= 0 {
		limits.AgentMaxChars = defaultAgentMaxChars
	}
	if limits.EntityMaxChars <= 0 {
		limits.EntityMaxChars = defaultEntityMaxChars
	}
	if limits.MaxItems <= 0 {
		limits.MaxItems = 40
	}
	return &Store{files: files, limits: limits}
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
	return &Store{files: s.files, limits: limits}
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

// LoadEntity reads the persistent state of one dynamic Agent entity (design
// doc 20). A missing snapshot is not an error.
func (s *Store) LoadEntity(ctx context.Context, agentID, key string) (types.StateSnapshot, error) {
	return s.load(ctx, s.entityPath(agentID, key), types.StateScopeEntity, "", "", "")
}

// SaveEntity persists one dynamic Agent entity's state. The snapshot is
// scoped by agent template + entity key and outlives any session.
func (s *Store) SaveEntity(ctx context.Context, agentID, key string, snapshot types.StateSnapshot) error {
	if strings.TrimSpace(agentID) == "" || strings.TrimSpace(key) == "" {
		return errors.New("entity state agent and key are required")
	}
	snapshot.Scope = types.StateScopeEntity
	snapshot.SessionID = ""
	snapshot.TeamID = ""
	snapshot.CallID = ""
	return s.save(ctx, s.entityPath(agentID, key), snapshot, s.limits.EntityMaxChars)
}

func (s *Store) entityPath(agentID, key string) string {
	return filepath.Join(".agents", "data", "agents", agentID, key, "state", "state.md")
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
	if snapshot.Scope != types.StateScopeEntity {
		if snapshot.SessionID == "" || snapshot.TeamID == "" {
			return errors.New("state session_id and team_id are required")
		}
		if snapshot.Scope == types.StateScopeAgent && snapshot.CallID == "" {
			return errors.New("agent state call_id is required")
		}
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
		snapshot.Confirmed[i] = trim(snapshot.Confirmed[i], maxChars/5)
	}
	for i := range snapshot.NextSteps {
		snapshot.NextSteps[i] = trim(snapshot.NextSteps[i], maxChars/5)
	}
	for i := range snapshot.Decisions {
		snapshot.Decisions[i] = trim(snapshot.Decisions[i], maxChars/5)
	}
	for i := range snapshot.OpenQuestions {
		snapshot.OpenQuestions[i] = trim(snapshot.OpenQuestions[i], maxChars/5)
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
		fmt.Fprintf(&body, "- %s (%s)\n", item.Path, item.Revision)
	}
	body.WriteString("\n# SharedRecord Refs\n\n")
	writeList(&body, snapshot.RecordIDs)
	return []byte(body.String()), nil
}

func writeList(builder *strings.Builder, items []string) {
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
