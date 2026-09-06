package state

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"

	"github.com/heron-ai/heron-engine/pkg/types"
)

// StateField identifies one field of an Agent's cross-session state. It is
// the unit of the CRUD operations (design doc 26 §4.2).
type StateField string

const (
	FieldGoal          StateField = "goal"
	FieldConfirmed     StateField = "confirmed"
	FieldOpenQuestions StateField = "open_questions"
	FieldDecisions     StateField = "decisions"
	FieldNextSteps     StateField = "next_steps"
	FieldWorkspace     StateField = "workspace"
	FieldRecordIDs     StateField = "record_ids"
)

// itemSeq produces unique, monotonically increasing ids for StateItems. It is
// process-local; combined with the agent scoping that is enough to keep ids
// unique within one agent's list.
var itemSeq atomic.Uint64

func newItemID() string {
	return fmt.Sprintf("i-%d", itemSeq.Add(1))
}

// NextItemID returns a fresh, unique StateItem id for callers that construct
// StateItems outside the CRUD methods (e.g. the runtime's automatic state
// updates). Ids are unique within the process, which is sufficient combined
// with agent scoping.
func (s *Store) NextItemID() string {
	return newItemID()
}

// fieldItems returns a pointer to the target StateItem slice for the given
// field. It reports false for fields that are not item lists.
func fieldItems(snapshot *types.StateSnapshot, field StateField) (*[]types.StateItem, bool) {
	switch field {
	case FieldConfirmed:
		return &snapshot.Confirmed, true
	case FieldOpenQuestions:
		return &snapshot.OpenQuestions, true
	case FieldDecisions:
		return &snapshot.Decisions, true
	case FieldNextSteps:
		return &snapshot.NextSteps, true
	default:
		return nil, false
	}
}

// withAgentLock runs fn while holding the agent-level state write lock. It
// never blocks: a busy agent returns an error.
func (s *Store) withAgentLock(agentID string, fn func() error) error {
	unlock, acquired := s.locks.TryLock(agentID)
	if !acquired {
		return fmt.Errorf("state: agent %q state is busy", agentID)
	}
	defer unlock()
	return fn()
}

// Add appends a todo entry to the given field, generating a fresh id.
func (s *Store) Add(ctx context.Context, agentID string, field StateField, text string) error {
	if err := contextErr(ctx); err != nil {
		return err
	}
	switch field {
	case FieldWorkspace:
		return errors.New("state: workspace entries are structural; add them via SaveAgentState")
	case FieldRecordIDs:
		return s.addRecordID(ctx, agentID, text)
	case FieldGoal:
		return errors.New("state: goal is single-valued; use SetGoal")
	case FieldConfirmed, FieldOpenQuestions, FieldDecisions, FieldNextSteps:
	default:
		return fmt.Errorf("state: unsupported field %q", field)
	}
	return s.withAgentLock(agentID, func() error {
		snapshot, err := s.LoadAgentState(ctx, agentID)
		if err != nil {
			return err
		}
		target, _ := fieldItems(&snapshot, field)
		*target = append(*target, types.StateItem{ID: newItemID(), Text: text})
		return s.SaveAgentState(ctx, agentID, snapshot)
	})
}

func (s *Store) addRecordID(ctx context.Context, agentID, value string) error {
	return s.withAgentLock(agentID, func() error {
		snapshot, err := s.LoadAgentState(ctx, agentID)
		if err != nil {
			return err
		}
		snapshot.RecordIDs = append(snapshot.RecordIDs, value)
		return s.SaveAgentState(ctx, agentID, snapshot)
	})
}

// Remove deletes one entry. For item lists id is the StateItem.ID; for
// workspace it is the Path; for record_ids it is the value.
func (s *Store) Remove(ctx context.Context, agentID string, field StateField, id string) error {
	if err := contextErr(ctx); err != nil {
		return err
	}
	return s.withAgentLock(agentID, func() error {
		snapshot, err := s.LoadAgentState(ctx, agentID)
		if err != nil {
			return err
		}
		switch field {
		case FieldWorkspace:
			next := snapshot.Workspace[:0]
			removed := false
			for _, item := range snapshot.Workspace {
				if item.Path == id {
					removed = true
					continue
				}
				next = append(next, item)
			}
			if !removed {
				return fmt.Errorf("state: workspace entry %q not found", id)
			}
			snapshot.Workspace = next
		case FieldRecordIDs:
			next := snapshot.RecordIDs[:0]
			removed := false
			for _, value := range snapshot.RecordIDs {
				if value == id {
					removed = true
					continue
				}
				next = append(next, value)
			}
			if !removed {
				return fmt.Errorf("state: record id %q not found", id)
			}
			snapshot.RecordIDs = next
		default:
			items, ok := fieldItems(&snapshot, field)
			if !ok {
				return fmt.Errorf("state: unsupported field %q", field)
			}
			next := (*items)[:0]
			removed := false
			for _, item := range *items {
				if item.ID == id {
					removed = true
					continue
				}
				next = append(next, item)
			}
			if !removed {
				return fmt.Errorf("state: item %q not found in %s", id, field)
			}
			*items = next
		}
		return s.SaveAgentState(ctx, agentID, snapshot)
	})
}

// Update rewrites the text of one item identified by id.
func (s *Store) Update(ctx context.Context, agentID string, field StateField, id, text string) error {
	if err := contextErr(ctx); err != nil {
		return err
	}
	return s.withAgentLock(agentID, func() error {
		snapshot, err := s.LoadAgentState(ctx, agentID)
		if err != nil {
			return err
		}
		items, ok := fieldItems(&snapshot, field)
		if !ok {
			return fmt.Errorf("state: field %q is not updatable", field)
		}
		for i := range *items {
			if (*items)[i].ID == id {
				(*items)[i].Text = text
				return s.SaveAgentState(ctx, agentID, snapshot)
			}
		}
		return fmt.Errorf("state: item %q not found in %s", id, field)
	})
}

// List returns every entry of the given field.
func (s *Store) List(ctx context.Context, agentID string, field StateField) ([]types.StateItem, error) {
	if err := contextErr(ctx); err != nil {
		return nil, err
	}
	snapshot, err := s.LoadAgentState(ctx, agentID)
	if err != nil {
		return nil, err
	}
	switch field {
	case FieldRecordIDs:
		items := make([]types.StateItem, 0, len(snapshot.RecordIDs))
		for _, value := range snapshot.RecordIDs {
			items = append(items, types.StateItem{ID: value, Text: value})
		}
		return items, nil
	case FieldWorkspace:
		items := make([]types.StateItem, 0, len(snapshot.Workspace))
		for _, ref := range snapshot.Workspace {
			items = append(items, types.StateItem{ID: ref.Path, Text: ref.Path + " (" + ref.Revision + ")"})
		}
		return items, nil
	case FieldGoal:
		if snapshot.Goal == "" {
			return nil, nil
		}
		return []types.StateItem{{ID: "goal", Text: snapshot.Goal}}, nil
	default:
		items, ok := fieldItems(&snapshot, field)
		if !ok {
			return nil, fmt.Errorf("state: unsupported field %q", field)
		}
		return *items, nil
	}
}

// Get returns a single entry by id (or Path for workspace, value for
// record_ids).
func (s *Store) Get(ctx context.Context, agentID string, field StateField, id string) (*types.StateItem, error) {
	items, err := s.List(ctx, agentID, field)
	if err != nil {
		return nil, err
	}
	for _, item := range items {
		if item.ID == id {
			return &item, nil
		}
	}
	return nil, fmt.Errorf("state: item %q not found in %s", id, field)
}

// SetGoal overwrites the single-valued goal.
func (s *Store) SetGoal(ctx context.Context, agentID, text string) error {
	if err := contextErr(ctx); err != nil {
		return err
	}
	return s.withAgentLock(agentID, func() error {
		snapshot, err := s.LoadAgentState(ctx, agentID)
		if err != nil {
			return err
		}
		snapshot.Goal = text
		return s.SaveAgentState(ctx, agentID, snapshot)
	})
}

// ClearGoal clears the single-valued goal.
func (s *Store) ClearGoal(ctx context.Context, agentID string) error {
	if err := contextErr(ctx); err != nil {
		return err
	}
	return s.withAgentLock(agentID, func() error {
		snapshot, err := s.LoadAgentState(ctx, agentID)
		if err != nil {
			return err
		}
		snapshot.Goal = ""
		return s.SaveAgentState(ctx, agentID, snapshot)
	})
}
