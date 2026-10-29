package state

import (
	"fmt"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/adrg/frontmatter"
	"gopkg.in/yaml.v3"

	"github.com/heron-ai/heron-engine/pkg/types"
)

// The cross-session state of one Agent is a directory, not a file:
//
//	state/
//	├── index.md          summary: which blocks exist, how old, what is in flight
//	├── goal.md
//	├── next_steps.md
//	├── confirmed.md
//	├── decisions.md
//	├── open_questions.md
//	├── workspace.md
//	└── record_ids.md
//
// One file per block keeps a read-modify-write cycle inside one small file,
// gives each block its own size budget, lets two instances edit different
// blocks without contending, and keeps diffs readable. The index is what
// State(action="list") returns by default, so a reader can decide what is worth
// loading without loading any of it.
const (
	indexFileName   = "index.md"
	legacyStateName = "state.md"
	legacyBackup    = "state.md.bak"
)

// orderedBlocks is the order the index reports. Goal first (what the work is),
// then the todo list, then the supporting material.
var orderedBlocks = []string{
	types.StateBlockGoal,
	types.StateBlockNextSteps,
	types.StateBlockConfirmed,
	types.StateBlockDecisions,
	types.StateBlockOpenQuestions,
	types.StateBlockWorkspace,
	types.StateBlockRecordIDs,
}

var listBlocks = map[string]bool{
	types.StateBlockConfirmed:     true,
	types.StateBlockOpenQuestions: true,
	types.StateBlockDecisions:     true,
	types.StateBlockNextSteps:     true,
}

// validBlock reports whether name is a block a caller may address.
func validBlock(name string) bool {
	for _, block := range orderedBlocks {
		if block == name {
			return true
		}
	}
	return false
}

func agentStateDir(agentID string) string {
	return filepath.Join(".agents", "data", "agents", agentID, "state")
}

func agentIndexPath(agentID string) string {
	return filepath.Join(agentStateDir(agentID), indexFileName)
}

func agentBlockPath(agentID, block string) string {
	return filepath.Join(agentStateDir(agentID), block+".md")
}

func agentLegacyPath(agentID string) string {
	return filepath.Join(agentStateDir(agentID), legacyStateName)
}

// blockFront is the YAML frontmatter of one block file.
type blockFront struct {
	Block     string    `yaml:"block"`
	Revision  int       `yaml:"revision"`
	UpdatedAt time.Time `yaml:"updated_at"`
}

func encodeBlock(block types.StateBlock) ([]byte, error) {
	front, err := yaml.Marshal(blockFront{
		Block: block.Name, Revision: block.Revision, UpdatedAt: block.UpdatedAt,
	})
	if err != nil {
		return nil, fmt.Errorf("marshal %s frontmatter: %w", block.Name, err)
	}
	var body strings.Builder
	body.WriteString("---\n")
	body.Write(front)
	body.WriteString("---\n\n")

	switch block.Name {
	case types.StateBlockGoal:
		body.WriteString("# Goal\n\n")
		body.WriteString(block.Goal)
		body.WriteString("\n")
	case types.StateBlockWorkspace:
		body.WriteString("# Workspace\n\n")
		for _, item := range block.Workspace {
			fmt.Fprintf(&body, "- %s (%s)\n", item.Path, item.Revision)
		}
	case types.StateBlockRecordIDs:
		body.WriteString("# SharedRecord Refs\n\n")
		for _, id := range block.RecordIDs {
			fmt.Fprintf(&body, "- %s\n", id)
		}
	default:
		body.WriteString("# " + blockTitle(block.Name) + "\n\n")
		writeItems(&body, block.Items)
	}
	return []byte(body.String()), nil
}

func blockTitle(name string) string {
	switch name {
	case types.StateBlockNextSteps:
		return "Next Steps"
	case types.StateBlockConfirmed:
		return "Confirmed"
	case types.StateBlockDecisions:
		return "Decisions"
	case types.StateBlockOpenQuestions:
		return "Open Questions"
	default:
		return name
	}
}

// writeItems renders entries as "- id: text [status=…, added_by=…, handled_by=…]".
// The id is preserved so CRUD can address one entry; attribution is rendered
// only when present so an ordinary entry stays readable.
func writeItems(builder *strings.Builder, items []types.StateItem) {
	for _, item := range items {
		builder.WriteString("- ")
		if item.ID != "" {
			builder.WriteString(item.ID)
			builder.WriteString(": ")
		}
		builder.WriteString(item.Text)
		var attrs []string
		if item.Status != "" {
			attrs = append(attrs, "status="+item.Status)
		}
		if item.AddedBy != "" {
			attrs = append(attrs, "added_by="+item.AddedBy)
		}
		if item.HandledBy != "" {
			attrs = append(attrs, "handled_by="+item.HandledBy)
		}
		if len(attrs) > 0 {
			builder.WriteString(" [" + strings.Join(attrs, ", ") + "]")
		}
		builder.WriteByte('\n')
	}
}

// decodeBlock parses one block file. A missing or empty file yields an empty
// block, which is what "the Agent has not written this yet" looks like.
func decodeBlock(name string, data []byte) (types.StateBlock, error) {
	block := types.StateBlock{Name: name}
	if len(strings.TrimSpace(string(data))) == 0 {
		return block, nil
	}
	var front blockFront
	body, err := frontmatter.Parse(strings.NewReader(string(data)), &front)
	if err != nil {
		return types.StateBlock{}, fmt.Errorf("parse state block %s: %w", name, err)
	}
	block.Revision = front.Revision
	block.UpdatedAt = front.UpdatedAt

	switch name {
	case types.StateBlockGoal:
		block.Goal = strings.TrimSpace(string(body))
		if rest := strings.TrimPrefix(block.Goal, "# Goal"); rest != block.Goal {
			block.Goal = strings.TrimSpace(rest)
		}
	case types.StateBlockWorkspace:
		for _, line := range bulletLines(string(body)) {
			path, revision := parseWorkspaceLine(line)
			if path == "" {
				continue
			}
			block.Workspace = append(block.Workspace, types.StateWorkspaceRef{Path: path, Revision: revision})
		}
	case types.StateBlockRecordIDs:
		for _, line := range bulletLines(string(body)) {
			if line != "" {
				block.RecordIDs = append(block.RecordIDs, line)
			}
		}
	default:
		for _, line := range bulletLines(string(body)) {
			if item := parseItemLine(line); item.Text != "" || item.ID != "" {
				block.Items = append(block.Items, item)
			}
		}
	}
	return block, nil
}

// bulletLines returns the "- ..." payloads of a block body, skipping headings.
func bulletLines(body string) []string {
	var lines []string
	for _, raw := range strings.Split(body, "\n") {
		line := strings.TrimSpace(raw)
		if !strings.HasPrefix(line, "- ") {
			continue
		}
		lines = append(lines, strings.TrimSpace(strings.TrimPrefix(line, "- ")))
	}
	return lines
}

// parseItemLine parses "- id: text [status=…, added_by=…]" defensively: a
// hand-edited file with no attributes still yields the text.
func parseItemLine(line string) types.StateItem {
	item := types.StateItem{}
	text := line
	if closing := strings.LastIndex(text, "]"); closing == len(text)-1 {
		if opening := strings.LastIndex(text, " ["); opening >= 0 {
			for _, attr := range strings.Split(text[opening+2:closing], ",") {
				key, value, ok := strings.Cut(strings.TrimSpace(attr), "=")
				if !ok {
					continue
				}
				switch strings.TrimSpace(key) {
				case "status":
					item.Status = strings.TrimSpace(value)
				case "added_by":
					item.AddedBy = strings.TrimSpace(value)
				case "handled_by":
					item.HandledBy = strings.TrimSpace(value)
				}
			}
			text = strings.TrimSpace(text[:opening])
		}
	}
	if id, rest, ok := strings.Cut(text, ": "); ok && strings.HasPrefix(id, "i-") {
		item.ID = id
		text = rest
	}
	item.Text = strings.TrimSpace(text)
	return item
}

func parseWorkspaceLine(line string) (string, string) {
	closing := strings.LastIndex(line, ")")
	opening := strings.LastIndex(line, " (")
	if opening < 0 || closing != len(line)-1 {
		return strings.TrimSpace(line), ""
	}
	return strings.TrimSpace(line[:opening]), strings.TrimSpace(line[opening+2 : closing])
}

// --- index ---

// encodeIndex renders the index. The frontmatter carries the machine fields
// (whole-directory revision and last write); the table is for humans and for
// the model to read at a glance.
func encodeIndex(index types.StateIndex) []byte {
	var front strings.Builder
	front.WriteString("---\n")
	front.WriteString(fmt.Sprintf("revision: %d\n", index.Revision))
	front.WriteString(fmt.Sprintf("updated_at: %s\n", index.UpdatedAt.UTC().Format(time.RFC3339Nano)))
	if strings.TrimSpace(index.Goal) != "" {
		// The goal is repeated here on purpose: the index is the default read,
		// and "which task is this" is the first thing a reader needs.
		front.WriteString("goal: " + yamlScalar(index.Goal) + "\n")
	}
	front.WriteString("---\n\n# State Index\n\n")
	front.WriteString("| block | entries | in_progress | updated_at | revision |\n")
	front.WriteString("|---|---|---|---|---|\n")
	var body strings.Builder
	body.WriteString(front.String())
	for _, block := range index.Blocks {
		updated := "—"
		if !block.UpdatedAt.IsZero() {
			updated = block.UpdatedAt.UTC().Format(time.RFC3339Nano)
		}
		body.WriteString(fmt.Sprintf("| %s | %d | %d | %s | %d |\n",
			block.Name, block.Entries, block.InProgress, updated, block.Revision))
	}
	return []byte(body.String())
}

// yamlScalar renders one scalar safely inside a hand-written frontmatter.
func yamlScalar(value string) string {
	encoded, err := yaml.Marshal(value)
	if err != nil {
		return `""`
	}
	return strings.TrimSpace(string(encoded))
}

func decodeIndex(data []byte) (types.StateIndex, error) {
	var index types.StateIndex
	if len(strings.TrimSpace(string(data))) == 0 {
		return index, nil
	}
	var front struct {
		Revision  int       `yaml:"revision"`
		UpdatedAt time.Time `yaml:"updated_at"`
		Goal      string    `yaml:"goal"`
	}
	body, err := frontmatter.Parse(strings.NewReader(string(data)), &front)
	if err != nil {
		return types.StateIndex{}, fmt.Errorf("parse state index: %w", err)
	}
	index.Revision = front.Revision
	index.UpdatedAt = front.UpdatedAt
	index.Goal = front.Goal
	for _, raw := range strings.Split(string(body), "\n") {
		line := strings.TrimSpace(raw)
		if !strings.HasPrefix(line, "|") || strings.Contains(line, "---") {
			continue
		}
		cells := strings.Split(strings.Trim(line, "|"), "|")
		if len(cells) < 5 {
			continue
		}
		meta := types.StateBlockMeta{
			Name:      strings.TrimSpace(cells[0]),
			UpdatedAt: parseIndexTime(strings.TrimSpace(cells[3])),
		}
		meta.Entries, _ = strconv.Atoi(strings.TrimSpace(cells[1]))
		meta.InProgress, _ = strconv.Atoi(strings.TrimSpace(cells[2]))
		meta.Revision, _ = strconv.Atoi(strings.TrimSpace(cells[4]))
		if meta.Name == "block" || !validBlock(meta.Name) {
			continue
		}
		index.Blocks = append(index.Blocks, meta)
	}
	return index, nil
}

func parseIndexTime(value string) time.Time {
	if value == "" || value == "—" {
		return time.Time{}
	}
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return time.Time{}
	}
	return parsed
}

// indexFromBlocks builds the summary for a set of blocks. Blocks appear in the
// canonical order regardless of how they were passed in.
func indexFromBlocks(blocks map[string]types.StateBlock, revision int, updatedAt time.Time) types.StateIndex {
	index := types.StateIndex{Revision: revision, UpdatedAt: updatedAt}
	for _, name := range orderedBlocks {
		block, ok := blocks[name]
		if !ok {
			continue
		}
		meta := types.StateBlockMeta{
			Name: name, Revision: block.Revision, UpdatedAt: block.UpdatedAt,
		}
		switch {
		case name == types.StateBlockGoal:
			if strings.TrimSpace(block.Goal) == "" {
				continue
			}
			meta.Entries = 1
			index.Goal = block.Goal
		case name == types.StateBlockWorkspace:
			meta.Entries = len(block.Workspace)
		case name == types.StateBlockRecordIDs:
			meta.Entries = len(block.RecordIDs)
		default:
			meta.Entries = len(block.Items)
			for _, item := range block.Items {
				if item.Status == types.StateStatusInProgress {
					meta.InProgress++
				}
			}
		}
		index.Blocks = append(index.Blocks, meta)
	}
	return index
}

// sortedBlockNames is used by tests and migration.
func sortedBlockNames() []string {
	names := append([]string(nil), orderedBlocks...)
	sort.Strings(names)
	return names
}

// normalizeStatus validates one entry status. Empty reads as pending.
func normalizeStatus(status string) (string, error) {
	switch strings.TrimSpace(status) {
	case "", types.StateStatusPending:
		return types.StateStatusPending, nil
	case types.StateStatusInProgress:
		return types.StateStatusInProgress, nil
	case types.StateStatusCompleted:
		return types.StateStatusCompleted, nil
	default:
		return "", fmt.Errorf("state: unknown status %q (want pending, in_progress or completed)", status)
	}
}
