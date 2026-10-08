package definitions

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/heron-ai/heron-engine/pkg/types"
)

// Render and knowledge-index helpers used by the writer. Kept out of writer.go
// so the pipeline there reads as the 8 steps it is, without the encoding detail
// in the middle of it.

// knowledgeIDs lists the knowledge entry IDs visible anywhere in the config
// tree, so an agent spec that references one can be checked before it is
// written.
//
// Both locations are scanned because a knowledge entry's ID is what the agent's
// `knowledge` list matches on, and the loader never validates that list: an ID
// that resolves to nothing simply filters every entry out, leaving an agent
// that silently answers with no knowledge. Returning an empty set is
// meaningful — it means "no knowledge directory at all", which
// checkAgentSpecSupport treats as "cannot validate" rather than "nothing is
// allowed".
func knowledgeIDs(root string) map[string]struct{} {
	ids := map[string]struct{}{}

	collect := func(dir string) {
		_ = filepath.WalkDir(dir, func(path string, entry os.DirEntry, err error) error {
			if err != nil {
				// A missing directory is normal; any other error would be
				// reported by the loader during validation, so it is not worth
				// failing the request over here.
				return nil
			}
			if entry.IsDir() {
				return nil
			}
			name := entry.Name()
			if filepath.Ext(name) != ".md" || name == "index.md" {
				return nil
			}
			ids[strings.TrimSuffix(name, ".md")] = struct{}{}
			return nil
		})
	}

	collect(filepath.Join(root, "knowledge"))

	agentsDir := filepath.Join(root, "agents")
	entries, err := os.ReadDir(agentsDir)
	if err == nil {
		for _, entry := range entries {
			if !entry.IsDir() {
				continue
			}
			collect(filepath.Join(agentsDir, entry.Name(), "knowledge"))
		}
	}

	if len(ids) == 0 {
		return nil
	}
	return ids
}

// renderAgent writes the canonical agent file: YAML frontmatter from the merged
// spec (which is what actually round-trips through the loader) followed by the
// markdown body (which is the system prompt).
//
// The frontmatter is emitted from the merged MAP, not from the decoded struct:
// re-encoding the struct would drop the absent-vs-zero distinction the merge
// worked to preserve, because every omitted optional field would be written
// back as an explicit key.
func renderAgent(spec map[string]any, body string) ([]byte, error) {
	encoded, err := marshalYAML(spec)
	if err != nil {
		return nil, fmt.Errorf("encode frontmatter: %w", err)
	}

	var builder strings.Builder
	builder.WriteString("---\n")
	builder.Write(encoded)
	builder.WriteString("---\n")

	if strings.TrimSpace(body) != "" {
		builder.WriteString("\n")
		builder.WriteString(strings.Trim(body, "\n"))
		builder.WriteString("\n")
	}
	return []byte(builder.String()), nil
}

// renderTeam serializes the typed team.
//
// The template is NOT re-applied here even though it was used to build the team.
// buildTeam already folded the template's structure into the typed struct, and
// types.Team is the shape the loader will decode — so re-layering the raw
// template on top could only add back keys the typed form deliberately dropped
// (a placeholder `calls` entry, an `output.from` pointing at it), which is the
// one thing a generated team must never inherit.
func renderTeam(team types.Team) ([]byte, error) {
	result, err := marshalYAML(team)
	if err != nil {
		return nil, fmt.Errorf("encode team %q: %w", team.ID, err)
	}
	return result, nil
}

// splitFrontmatter returns the YAML between the leading "---" delimiters and
// the markdown body after them.
//
// It re-reads the delimiters from the source rather than asking
// frontmatter.Parse for them, because the merge needs the frontmatter as a
// generic map while validation needs it decoded into types.AgentConfig — and
// the map form can only be recovered from the text.
func splitFrontmatter(text string) (string, string, error) {
	const delimiter = "---"

	normalized := strings.ReplaceAll(text, "\r\n", "\n")
	if !strings.HasPrefix(normalized, delimiter) {
		return "", "", errors.New("frontmatter: file does not start with ---")
	}
	rest := normalized[len(delimiter):]
	end := strings.Index(rest, "\n"+delimiter)
	if end < 0 {
		return "", "", errors.New("frontmatter: closing --- not found")
	}
	front := rest[:end]
	body := rest[end+len("\n"+delimiter):]
	return front, strings.TrimPrefix(body, "\n"), nil
}

// frontmatterAndBody splits a rendered agent file into its merged document and
// its markdown body. Only generated files are re-read this way, and only when
// upserting, so the reparse cost is never on the read path.
func frontmatterAndBody(raw []byte) (map[string]any, string, error) {
	front, body, err := splitFrontmatter(string(raw))
	if err != nil {
		return nil, "", err
	}
	decoded := map[string]any{}
	if err := yaml.Unmarshal([]byte(front), &decoded); err != nil {
		return nil, "", fmt.Errorf("decode frontmatter: %w", err)
	}
	return decoded, body, nil
}

// marshalYAML encodes with a two-space indent, matching the hand-written
// examples so a generated file does not look foreign in a diff.
func marshalYAML(value any) ([]byte, error) {
	var builder strings.Builder
	encoder := yaml.NewEncoder(&builder)
	encoder.SetIndent(2)
	if err := encoder.Encode(value); err != nil {
		_ = encoder.Close()
		return nil, err
	}
	if err := encoder.Close(); err != nil {
		return nil, err
	}
	return []byte(builder.String()), nil
}

// teamFileFor resolves where a team definition actually lives in the tree.
//
// A team's identity comes from frontmatter `id`, falling back to the file stem,
// so the two can legitimately disagree — teams/qa.yml may declare id: qa_team.
// An upsert addresses the team by ID but must write back to the file that
// actually holds it; deriving the path from the ID would create a SECOND file
// for the same team, and the original — the one every flow reference resolves
// to — would freeze at its current content.
//
// Returns "" when no file claims the ID, in which case the caller falls back to
// the canonical <id>.yml. It reads the files directly rather than consulting the
// snapshot because the snapshot is keyed by ID and has already lost the
// filename.
func teamFileFor(root, teamID string) string {
	dir := filepath.Join(root, "teams")
	entries, err := os.ReadDir(dir)
	if err != nil {
		return ""
	}
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		switch filepath.Ext(entry.Name()) {
		case ".yml", ".yaml":
		default:
			continue
		}

		data, err := os.ReadFile(filepath.Join(dir, entry.Name()))
		if err != nil {
			continue
		}
		var probe types.Team
		if err := yaml.Unmarshal(data, &probe); err != nil {
			continue
		}
		declared := strings.TrimSpace(probe.ID)
		stem := strings.TrimSuffix(entry.Name(), filepath.Ext(entry.Name()))
		if declared == teamID || (declared == "" && stem == teamID) {
			return filepath.Join("teams", entry.Name())
		}
	}
	return ""
}
