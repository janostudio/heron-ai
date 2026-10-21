package teamspkg

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/heron-ai/heron-engine/internal/storage"
	"github.com/heron-ai/heron-engine/pkg/types"
)

// writeTree lays out a package directory that mirrors the files a real export
// would produce, including assets the entry team must not reach.
func writeTree(t *testing.T, files map[string]string) string {
	t.Helper()
	root := t.TempDir()
	for name, content := range files {
		path := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	return root
}

const (
	reviewTeamYML = `
id: review_team
calls:
  review:
    type: agent
    agent: reviewer
  lint:
    type: command
    command: ./lint.sh
`

	otherTeamYML = `
id: other_team
calls:
  solo:
    type: agent
    agent: unrelated
`

	reviewerAgentMD = `---
name: reviewer
model:
  provider: test
  model: model-a
tools:
  builtin: [Read, Bash]
skills: [owasp-check]
rules: [review-standards]
---
You review code.
`

	unrelatedAgentMD = `---
name: unrelated
model:
  provider: test
  model: model-b
---
Unrelated.
`

	owaspSkillMD = `---
name: owasp-check
description: OWASP checks
tools: [Read]
scripts: [scripts/scan.sh]
---
Run the scanner.
`

	unusedSkillMD = `---
name: unused-skill
description: never referenced
---
Unused.
`

	reviewRuleMD = `---
id: review-standards
type: constraint
---
Be strict.
`

	unusedRuleMD = `---
id: unused-rule
---
Never referenced.
`
)

func fixtureFiles() map[string]string {
	return map[string]string{
		"teams/review_team.yml":               reviewTeamYML,
		"teams/other_team.yml":                otherTeamYML,
		"agents/reviewer/AGENT.md":            reviewerAgentMD,
		"agents/unrelated/AGENT.md":           unrelatedAgentMD,
		"skills/owasp-check/SKILL.md":         owaspSkillMD,
		"skills/owasp-check/scripts/scan.sh":  "#!/bin/sh\necho scan\n",
		"skills/unused-skill/SKILL.md":        unusedSkillMD,
		"rules/review-standards.md":           reviewRuleMD,
		"rules/unused-rule.md":                unusedRuleMD,
		"agents/reviewer/knowledge/secret.md": "private notes",
		"knowledge/global.md":                 "global notes",
	}
}

func TestCollect(t *testing.T) {
	t.Parallel()
	root := writeTree(t, fixtureFiles())

	tree, err := Collect(storage.NewFileStore(""), root)
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}

	if len(tree.Teams) != 2 {
		t.Fatalf("teams = %v, want 2", keys(tree.Teams))
	}
	if len(tree.Agents) != 2 {
		t.Fatalf("agents = %v, want 2", keys(tree.Agents))
	}
	if len(tree.Skills) != 2 {
		t.Fatalf("skills = %v, want 2", keys(tree.Skills))
	}
	rules := append([]string(nil), tree.Rules...)
	sort.Strings(rules)
	if strings.Join(rules, ",") != "review-standards,unused-rule" {
		t.Fatalf("rules = %v", rules)
	}

	agent := tree.Agents["reviewer"]
	if agent.Body != "You review code.\n" {
		t.Fatalf("agent body = %q", agent.Body)
	}
	if got := agent.Skills; len(got) != 1 || got[0] != "owasp-check" {
		t.Fatalf("agent skills = %v", got)
	}
	if got := tree.Scripts["owasp-check"]; len(got) != 1 || got[0] != "scripts/scan.sh" {
		t.Fatalf("skill scripts = %v", got)
	}
	if tree.Skills["owasp-check"].Body != "Run the scanner." {
		t.Fatalf("skill body = %q", tree.Skills["owasp-check"].Body)
	}
}

func TestCollectMissingDirectoriesIsNotAnError(t *testing.T) {
	root := writeTree(t, map[string]string{"teams/solo.yml": "id: solo\n"})
	tree, err := Collect(storage.NewFileStore(""), root)
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if len(tree.Teams) != 1 || len(tree.Agents) != 0 || len(tree.Skills) != 0 || len(tree.Rules) != 0 {
		t.Fatalf("unexpected tree: %+v", tree)
	}
}

func TestClosure(t *testing.T) {
	t.Parallel()
	root := writeTree(t, fixtureFiles())
	tree, err := Collect(storage.NewFileStore(""), root)
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}

	closed, err := tree.Closure("review_team")
	if err != nil {
		t.Fatalf("Closure: %v", err)
	}

	if got := keys(closed.Teams); strings.Join(got, ",") != "review_team" {
		t.Fatalf("teams = %v", got)
	}
	if got := keys(closed.Agents); strings.Join(got, ",") != "reviewer" {
		t.Fatalf("agents = %v", got)
	}
	if got := keys(closed.Skills); strings.Join(got, ",") != "owasp-check" {
		t.Fatalf("skills = %v", got)
	}
	if got := strings.Join(closed.Rules, ","); got != "review-standards" {
		t.Fatalf("rules = %v", got)
	}
	if got := closed.Scripts["owasp-check"]; len(got) != 1 {
		t.Fatalf("scripts = %v", got)
	}

	// The closure must not mutate the source tree.
	if len(tree.Agents) != 2 || len(tree.Skills) != 2 {
		t.Fatalf("Closure mutated the source tree: agents=%v skills=%v", keys(tree.Agents), keys(tree.Skills))
	}
}

func TestClosureUnknownTeam(t *testing.T) {
	tree := &Tree{Teams: map[string]types.Team{}}
	if _, err := tree.Closure("nope"); err == nil {
		t.Fatal("expected error for unknown team")
	}
}

func TestClosureCyclesTerminate(t *testing.T) {
	// Two teams referencing agents whose skills/rules point back at names
	// already visited: the walk must terminate instead of revisiting.
	files := map[string]string{
		"teams/a.yml": `
id: a
calls:
  one: {type: agent, agent: x}
  two: {type: agent, agent: y}
  again: {type: agent, agent: x}
`,
		"agents/x/AGENT.md": "---\nname: x\nskills: [s]\nrules: [r1]\n---\nx\n",
		"agents/y/AGENT.md": "---\nname: y\nskills: [s]\nrules: [r1, r2]\n---\ny\n",
		"skills/s/SKILL.md": "---\nname: s\ndescription: d\n---\ns\n",
		"rules/r1.md":       "---\nid: r1\n---\n1\n",
		"rules/r2.md":       "---\nid: r2\n---\n2\n",
	}
	root := writeTree(t, files)
	tree, err := Collect(storage.NewFileStore(""), root)
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	closed, err := tree.Closure("a")
	if err != nil {
		t.Fatalf("Closure: %v", err)
	}
	if len(closed.Agents) != 2 {
		t.Fatalf("agents = %v", keys(closed.Agents))
	}
	if got := strings.Join(closed.Rules, ","); got != "r1,r2" {
		t.Fatalf("rules = %v", closed.Rules)
	}
}

func keys[V any](m map[string]V) []string { return sortedKeys(m) }
