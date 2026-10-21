package teamspkg

import (
	"strings"
	"testing"

	"github.com/heron-ai/heron-engine/internal/storage"
	"github.com/heron-ai/heron-engine/pkg/types"
)

const checkManifest = `
apiVersion: heron/v1
kind: team
name: review-team
version: 1.0.0
provides:
  teams: [review_team]
  agents: [reviewer]
  skills: [owasp-check]
  rules: [review-standards]
sandbox:
  tools: [Read, Grep]
`

func checkFixture(t *testing.T) (*Manifest, *Tree) {
	t.Helper()
	root := writeTree(t, fixtureFiles())
	tree, err := Collect(storage.NewFileStore(""), root)
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	return nil, tree
}

func mustManifest(t *testing.T, data string) *Manifest {
	t.Helper()
	m, err := LoadManifest([]byte(data))
	if err != nil {
		t.Fatalf("LoadManifest: %v", err)
	}
	return m
}

func messages(result CheckResult) string {
	var b strings.Builder
	for _, p := range result.Problems {
		b.WriteString(p.Severity)
		b.WriteString(": ")
		b.WriteString(p.Message)
		b.WriteString("\n")
	}
	return b.String()
}

func TestCheckCleanPackage(t *testing.T) {
	_, tree := checkFixture(t)
	// The fixture's reviewer declares Bash, which the sandbox excludes, so a
	// clean run needs a tree whose agents stay inside the sandbox.
	closed, err := tree.Closure("review_team")
	if err != nil {
		t.Fatalf("Closure: %v", err)
	}
	agent := closed.Agents["reviewer"]
	agent.Tools.Builtin = []string{"Read"}
	closed.Agents["reviewer"] = agent

	result := Check(mustManifest(t, checkManifest), closed)
	if len(result.Problems) != 0 {
		t.Fatalf("unexpected problems:\n%s", messages(result))
	}
	if got := strings.Join(result.Models, ","); got != "model-a" {
		t.Fatalf("models = %v", result.Models)
	}
}

func TestCheckMissingAgentReference(t *testing.T) {
	_, tree := checkFixture(t)
	closed, _ := tree.Closure("review_team")
	delete(closed.Agents, "reviewer")

	result := Check(mustManifest(t, checkManifest), closed)
	want := `team "review_team" call "review" references missing agent "reviewer"`
	if !strings.Contains(messages(result), want) {
		t.Fatalf("problems missing %q:\n%s", want, messages(result))
	}
	if !result.HasErrors() {
		t.Fatal("expected errors")
	}
}

func TestCheckMissingSkillReference(t *testing.T) {
	_, tree := checkFixture(t)
	closed, _ := tree.Closure("review_team")
	agent := closed.Agents["reviewer"]
	agent.Skills = []string{"owasp-check", "ghost-skill"}
	closed.Agents["reviewer"] = agent

	result := Check(mustManifest(t, checkManifest), closed)
	want := `agent "reviewer" references missing skill "ghost-skill"`
	if !strings.Contains(messages(result), want) {
		t.Fatalf("problems missing %q:\n%s", want, messages(result))
	}
}

func TestCheckMissingRuleReference(t *testing.T) {
	_, tree := checkFixture(t)
	closed, _ := tree.Closure("review_team")
	agent := closed.Agents["reviewer"]
	agent.Rules = []string{"ghost-rule"}
	closed.Agents["reviewer"] = agent

	result := Check(mustManifest(t, checkManifest), closed)
	want := `agent "reviewer" references missing rule "ghost-rule"`
	if !strings.Contains(messages(result), want) {
		t.Fatalf("problems missing %q:\n%s", want, messages(result))
	}
}

func TestCheckProvidesMissingAsset(t *testing.T) {
	_, tree := checkFixture(t)
	closed, _ := tree.Closure("review_team")
	m := mustManifest(t, strings.Replace(checkManifest, "  rules: [review-standards]", "  rules: [review-standards, ghost-rule]", 1))

	result := Check(m, closed)
	want := `manifest "review-team" provides missing rule "ghost-rule"`
	if !strings.Contains(messages(result), want) {
		t.Fatalf("problems missing %q:\n%s", want, messages(result))
	}
}

func TestCheckEmptyProvidesTeams(t *testing.T) {
	m := mustManifest(t, `
apiVersion: heron/v1
kind: team
name: empty
version: 1.0.0
provides:
  teams: []
`)
	result := Check(m, &Tree{Teams: map[string]types.Team{}, Agents: map[string]types.AgentConfig{}, Skills: map[string]types.Skill{}})
	if !strings.Contains(messages(result), "provides.teams is empty") {
		t.Fatalf("problems:\n%s", messages(result))
	}
}

func TestCheckToolTrimWarning(t *testing.T) {
	_, tree := checkFixture(t)
	closed, _ := tree.Closure("review_team")

	result := Check(mustManifest(t, checkManifest), closed)
	msg := messages(result)
	if !strings.Contains(msg, "warning:") || !strings.Contains(msg, "Bash") {
		t.Fatalf("expected tool trim warning mentioning Bash:\n%s", msg)
	}
	if result.HasErrors() {
		t.Fatalf("tool trimming must not be an error:\n%s", msg)
	}
}

func TestCheckScriptsDeny(t *testing.T) {
	_, tree := checkFixture(t)
	closed, _ := tree.Closure("review_team")
	m := mustManifest(t, strings.Replace(checkManifest, "tools: [Read, Grep]", "tools: [Read, Grep]\n  scripts: deny", 1))

	result := Check(m, closed)
	msg := messages(result)
	if !strings.Contains(msg, "error:") || !strings.Contains(msg, "sandbox.scripts is deny") {
		t.Fatalf("expected scripts deny error:\n%s", msg)
	}
}

func TestCheckModelDedup(t *testing.T) {
	_, tree := checkFixture(t)
	closed, _ := tree.Closure("review_team")
	// A second agent on the same model must not duplicate the entry.
	extra := closed.Agents["reviewer"]
	extra.Name = "second"
	extra.Model.Model = "model-a"
	closed.Agents["second"] = extra
	third := closed.Agents["reviewer"]
	third.Name = "third"
	third.Model.Model = "model-c"
	third.Skills = nil
	third.Rules = nil
	closed.Agents["third"] = third

	result := Check(mustManifest(t, checkManifest), closed)
	if got := strings.Join(result.Models, ","); got != "model-a,model-c" {
		t.Fatalf("models = %v", result.Models)
	}
}

func TestCheckNilInputs(t *testing.T) {
	result := Check(nil, nil)
	if !result.HasErrors() {
		t.Fatal("nil manifest must report an error")
	}
}
