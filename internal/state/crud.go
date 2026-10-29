package state

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"

	"github.com/heron-ai/heron-engine/pkg/types"
)

// StateField identifies one field of an Agent's cross-session state. A field is
// backed by exactly one block file.
type StateField string

const (
	FieldGoal          StateField = types.StateBlockGoal
	FieldConfirmed     StateField = types.StateBlockConfirmed
	FieldOpenQuestions StateField = types.StateBlockOpenQuestions
	FieldDecisions     StateField = types.StateBlockDecisions
	FieldNextSteps     StateField = types.StateBlockNextSteps
	FieldWorkspace     StateField = types.StateBlockWorkspace
	FieldRecordIDs     StateField = types.StateBlockRecordIDs
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
// updates).
func (s *Store) NextItemID() string {
	return newItemID()
}

// withBlockLock runs fn while holding the write lock of one Agent's block.
// Block granularity is what makes the directory layout pay off: two instances
// editing different blocks do not contend. It never blocks — a busy block
// returns an error.
func (s *Store) withBlockLock(agentID, block string, fn func() error) error {
	unlock, acquired := s.locks.TryLock(agentID + "/" + block)
	if !acquired {
		return fmt.Errorf("state: agent %q block %q is busy", agentID, block)
	}
	defer unlock()
	return fn()
}

// Add appends a todo entry to the given field, generating a fresh id. agentID
// scopes the shared whiteboard; actor is the "agent(instance)" attribution
// recorded as AddedBy (it may be empty for non-instance callers).
//
// status is only meaningful for next_steps, the todo list. Empty reads as
// pending; at most one entry may be in_progress, matching the rule that one
// task is being worked on at a time.
func (s *Store) Add(ctx context.Context, agentID string, field StateField, text string, status string, actor string) error {
	if err := contextErr(ctx); err != nil {
		return err
	}
	switch field {
	case FieldWorkspace:
		return errors.New("state: workspace entries are structural; add them via AppendWorkspace")
	case FieldRecordIDs:
		return s.AppendRecordIDs(ctx, agentID, text)
	case FieldGoal:
		return errors.New("state: goal is single-valued; use SetGoal")
	case FieldConfirmed, FieldOpenQuestions, FieldDecisions, FieldNextSteps:
	default:
		return fmt.Errorf("state: unsupported field %q", field)
	}

	normalized, err := normalizeStatus(status)
	if err != nil {
		return err
	}
	if field != FieldNextSteps {
		// Only the todo list carries a lifecycle; a "completed" decision is
		// meaningless and would invite the model to manage a state that has no
		// meaning.
		normalized = ""
	}
	if normalized == types.StateStatusInProgress {
		if err := s.ensureSingleInProgress(ctx, agentID, field, ""); err != nil {
			return err
		}
	}

	return s.withBlockLock(agentID, string(field), func() error {
		block, err := s.LoadBlock(ctx, agentID, string(field))
		if err != nil {
			return err
		}
		block.Items = append(block.Items, types.StateItem{
			ID: newItemID(), Text: text, Status: normalized, AddedBy: actor,
		})
		return s.SaveBlock(ctx, agentID, block)
	})
}

// ensureSingleInProgress rejects a second in_progress entry in the same block.
// Silently demoting the older one would lose the fact that two things were
// started.
func (s *Store) ensureSingleInProgress(ctx context.Context, agentID string, field StateField, exceptID string) error {
	items, err := s.List(ctx, agentID, field)
	if err != nil {
		return err
	}
	for _, item := range items {
		if item.ID == exceptID {
			continue
		}
		if item.Status == types.StateStatusInProgress {
			return fmt.Errorf("state: %s already has an in_progress entry (%s); finish it or mark it completed first", field, item.ID)
		}
	}
	return nil
}

// Remove deletes one entry. For item lists id is the StateItem.ID; for
// workspace it is the Path; for record_ids it is the value.
func (s *Store) Remove(ctx context.Context, agentID string, field StateField, id string) error {
	if err := contextErr(ctx); err != nil {
		return err
	}
	blockName := string(field)
	if !validBlock(blockName) {
		return fmt.Errorf("state: unsupported field %q", field)
	}
	return s.withBlockLock(agentID, blockName, func() error {
		block, err := s.LoadBlock(ctx, agentID, blockName)
		if err != nil {
			return err
		}
		switch field {
		case FieldWorkspace:
			next := block.Workspace[:0]
			removed := false
			for _, item := range block.Workspace {
				if item.Path == id {
					removed = true
					continue
				}
				next = append(next, item)
			}
			if !removed {
				return fmt.Errorf("state: workspace entry %q not found", id)
			}
			block.Workspace = next
		case FieldRecordIDs:
			next := block.RecordIDs[:0]
			removed := false
			for _, value := range block.RecordIDs {
				if value == id {
					removed = true
					continue
				}
				next = append(next, value)
			}
			if !removed {
				return fmt.Errorf("state: record id %q not found", id)
			}
			block.RecordIDs = next
		default:
			next := block.Items[:0]
			removed := false
			for _, item := range block.Items {
				if item.ID == id {
					removed = true
					continue
				}
				next = append(next, item)
			}
			if !removed {
				return fmt.Errorf("state: item %q not found in %s", id, field)
			}
			block.Items = next
		}
		return s.SaveBlock(ctx, agentID, block)
	})
}

// Update rewrites one entry identified by id and records actor as HandledBy.
// Empty text keeps the current text; empty status keeps the current status, so
// "mark it in_progress" and "rewrite it" are the same call.
func (s *Store) Update(ctx context.Context, agentID string, field StateField, id, text, status, actor string) error {
	if err := contextErr(ctx); err != nil {
		return err
	}
	blockName := string(field)
	if !validBlock(blockName) {
		return fmt.Errorf("state: unsupported field %q", field)
	}
	if field != FieldNextSteps && strings.TrimSpace(status) != "" {
		return fmt.Errorf("state: status only applies to %s", FieldNextSteps)
	}
	normalized, err := normalizeStatus(status)
	if err != nil {
		return err
	}
	if normalized == types.StateStatusInProgress {
		if err := s.ensureSingleInProgress(ctx, agentID, field, id); err != nil {
			return err
		}
	}
	return s.withBlockLock(agentID, blockName, func() error {
		block, err := s.LoadBlock(ctx, agentID, blockName)
		if err != nil {
			return err
		}
		items, ok := listItems(&block)
		if !ok {
			return fmt.Errorf("state: field %q is not updatable", field)
		}
		for i := range *items {
			if (*items)[i].ID != id {
				continue
			}
			if strings.TrimSpace(text) != "" {
				(*items)[i].Text = text
			}
			if field == FieldNextSteps {
				if strings.TrimSpace(status) != "" {
					(*items)[i].Status = normalized
				} else if (*items)[i].Status == "" {
					(*items)[i].Status = types.StateStatusPending
				}
			}
			(*items)[i].HandledBy = actor
			return s.SaveBlock(ctx, agentID, block)
		}
		return fmt.Errorf("state: item %q not found in %s", id, field)
	})
}

// List returns every entry of the given field.
func (s *Store) List(ctx context.Context, agentID string, field StateField) ([]types.StateItem, error) {
	result, err := s.ListWithMeta(ctx, agentID, field)
	if err != nil {
		return nil, err
	}
	return result.Items, nil
}

// ListWithMeta returns one field's entries together with the block metadata.
// The State tool uses it so a reader can tell a three-day-old entry from one
// written this turn: the state is not injected into the prompt, so freshness
// has to be carried by the read itself.
func (s *Store) ListWithMeta(ctx context.Context, agentID string, field StateField) (types.StateList, error) {
	if err := contextErr(ctx); err != nil {
		return types.StateList{}, err
	}
	blockName := string(field)
	if !validBlock(blockName) {
		return types.StateList{}, fmt.Errorf("state: unsupported field %q", field)
	}
	block, err := s.LoadBlock(ctx, agentID, blockName)
	if err != nil {
		return types.StateList{}, err
	}
	list := types.StateList{
		UpdatedAt: block.UpdatedAt,
		Revision:  block.Revision,
	}
	if field == FieldGoal {
		list.Goal = block.Goal
	} else if goal, goalErr := s.LoadBlock(ctx, agentID, types.StateBlockGoal); goalErr == nil {
		// The goal travels with every read: it is the label that tells the
		// caller whether this block belongs to the task it is doing.
		list.Goal = goal.Goal
	}
	list.Items, err = itemsOf(block, field)
	if err != nil {
		return types.StateList{}, err
	}
	return list, nil
}

// itemsOf projects one block into the entry shape the tool returns.
func itemsOf(block types.StateBlock, field StateField) ([]types.StateItem, error) {
	switch field {
	case FieldGoal:
		if strings.TrimSpace(block.Goal) == "" {
			return nil, nil
		}
		return []types.StateItem{{ID: "goal", Text: block.Goal}}, nil
	case FieldWorkspace:
		items := make([]types.StateItem, 0, len(block.Workspace))
		for _, ref := range block.Workspace {
			items = append(items, types.StateItem{ID: ref.Path, Text: ref.Path + " (" + ref.Revision + ")"})
		}
		return items, nil
	case FieldRecordIDs:
		items := make([]types.StateItem, 0, len(block.RecordIDs))
		for _, value := range block.RecordIDs {
			items = append(items, types.StateItem{ID: value, Text: value})
		}
		return items, nil
	default:
		return block.Items, nil
	}
}

// listItems returns a pointer to the block's item slice for the fields that
// support entry updates.
func listItems(block *types.StateBlock) (*[]types.StateItem, bool) {
	switch block.Name {
	case types.StateBlockConfirmed, types.StateBlockOpenQuestions,
		types.StateBlockDecisions, types.StateBlockNextSteps:
		return &block.Items, true
	default:
		return nil, false
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
	return s.withBlockLock(agentID, types.StateBlockGoal, func() error {
		block, err := s.LoadBlock(ctx, agentID, types.StateBlockGoal)
		if err != nil {
			return err
		}
		block.Goal = text
		return s.SaveBlock(ctx, agentID, block)
	})
}

// ClearGoal clears the single-valued goal.
func (s *Store) ClearGoal(ctx context.Context, agentID string) error {
	return s.SetGoal(ctx, agentID, "")
}
