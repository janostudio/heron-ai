package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/heron-ai/heron-engine/internal/teamspkg"
)

const testManifest = `apiVersion: heron/v1
kind: team
name: review-team
version: 1.2.0
description: security review
provides:
  teams: [review_team]
  agents: [security-reviewer]
  skills: [owasp-check]
  rules: [review-standards]
sandbox:
  tools: [Read, Grep]
  scripts: review
`

const testTeamYML = `id: review_team
calls:
  review:
    type: agent
    agent: security-reviewer
`

const testAgentMD = `---
name: security-reviewer
model:
  provider: test
  model: claude-sonnet-4.6
tools:
  builtin: [Read, Bash, Grep]
  custom: [my-tool]
skills: [owasp-check]
rules: [review-standards]
---
You review security.
`

const testSkillMD = `---
name: owasp-check
description: OWASP checks
scripts: [scripts/scan.sh]
---
Run the scanner.
`

const testRuleMD = `---
id: review-standards
type: constraint
---
Be strict.
`

func testManifestText() string { return testManifest }

func writeTestPackage(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	files := map[string]string{
		"heron-team.yml":                        testManifestText(),
		"teams/review_team.yml":                 testTeamYML,
		"agents/security-reviewer/AGENT.md":     testAgentMD,
		"skills/owasp-check/SKILL.md":           testSkillMD,
		"skills/owasp-check/scripts/scan.sh":    "#!/bin/sh\necho scan\n",
		"rules/review-standards.md":             testRuleMD,
		"agents/security-reviewer/README.extra": "keep me\n",
	}
	for name, body := range files {
		path := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	return dir
}

func installTest(t *testing.T, pkgDir string, opts installOptions, stdin string) (*installResult, string, string, error) {
	t.Helper()
	root := t.TempDir()
	var stdout, stderr bytes.Buffer
	var in *strings.Reader
	if stdin != "" {
		in = strings.NewReader(stdin)
	}
	res, err := installTeamPackage(root, pkgDir, opts, &stdout, &stderr, in)
	return res, root, stdout.String(), err
}

func readIndex(t *testing.T, root string) teamsIndex {
	t.Helper()
	idx, err := readTeamsIndex(root)
	if err != nil {
		t.Fatalf("readTeamsIndex: %v", err)
	}
	return *idx
}

func TestInstallFromDirectory(t *testing.T) {
	pkg := writeTestPackage(t)
	res, root, _, err := installTest(t, pkg, installOptions{Yes: true}, "")
	if err != nil {
		t.Fatalf("install: %v", err)
	}
	if res.Scope != "review-team" {
		t.Fatalf("scope = %q", res.Scope)
	}

	target := filepath.Join(root, ".agents", "_teams", "review-team")
	for _, rel := range []string{"heron-team.yml", "teams/review_team.yml", "agents/security-reviewer/AGENT.md", "skills/owasp-check/scripts/scan.sh", "rules/review-standards.md", "agents/security-reviewer/README.extra"} {
		if _, err := os.Stat(filepath.Join(target, rel)); err != nil {
			t.Fatalf("installed package missing %s: %v", rel, err)
		}
	}

	idx := readIndex(t, root)
	entry, ok := idx.Installed["review-team"]
	if !ok {
		t.Fatalf("index has no review-team entry: %+v", idx.Installed)
	}
	if entry.Version != "1.2.0" || entry.Source != pkg {
		t.Fatalf("entry = %+v", entry)
	}
	if entry.InstalledAt == "" {
		t.Fatal("installed_at is empty")
	}
	if _, ok := entry.Files["agents/security-reviewer/AGENT.md"]; !ok {
		t.Fatalf("files = %v", entry.Files)
	}
	if _, ok := entry.Scripts["skills/owasp-check/scripts/scan.sh"]; !ok {
		t.Fatalf("scripts = %v", entry.Scripts)
	}

	// No staging leftovers.
	entries, err := os.ReadDir(filepath.Join(root, ".agents", "_teams"))
	if err != nil {
		t.Fatalf("readdir: %v", err)
	}
	if len(entries) != 1 || entries[0].Name() != "review-team" {
		t.Fatalf("_teams entries = %v", entries)
	}
}

func TestInstallFromTarball(t *testing.T) {
	pkg := writeTestPackage(t)
	archive := filepath.Join(t.TempDir(), "review-team.tar.gz")
	tarDir(t, pkg, archive, "review-team")

	res, root, _, err := installTest(t, archive, installOptions{Yes: true}, "")
	if err != nil {
		t.Fatalf("install: %v", err)
	}
	if res.Scope != "review-team" {
		t.Fatalf("scope = %q", res.Scope)
	}
	// The archive wraps everything in review-team/, which must be unwrapped:
	// heron-team.yml has to sit at the scope root.
	manifest := filepath.Join(root, ".agents", "_teams", "review-team", "heron-team.yml")
	if _, err := os.Stat(manifest); err != nil {
		t.Fatalf("manifest not at scope root: %v", err)
	}
	if _, ok := readIndex(t, root).Installed["review-team"]; !ok {
		t.Fatal("index entry missing after tarball install")
	}
}

func TestInstallSameVersionIsIdempotent(t *testing.T) {
	pkg := writeTestPackage(t)
	root := t.TempDir()
	var out, errOut bytes.Buffer
	opts := installOptions{Yes: true}
	if _, err := installTeamPackage(root, pkg, opts, &out, &errOut, nil); err != nil {
		t.Fatalf("first install: %v", err)
	}
	if _, err := installTeamPackage(root, pkg, opts, &out, &errOut, nil); err != nil {
		t.Fatalf("second install: %v", err)
	}
	if len(readIndex(t, root).Installed) != 1 {
		t.Fatalf("index = %+v", readIndex(t, root).Installed)
	}

	// A stale file from a hand edit must be gone after the overwrite: the
	// install replaces the directory rather than merging into it.
	stale := filepath.Join(root, ".agents", "_teams", "review-team", "stale.txt")
	if err := os.WriteFile(stale, []byte("x"), 0o644); err != nil {
		t.Fatalf("write stale: %v", err)
	}
	if _, err := installTeamPackage(root, pkg, opts, &out, &errOut, nil); err != nil {
		t.Fatalf("third install: %v", err)
	}
	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Fatalf("stale file survived reinstall: %v", err)
	}
}

func TestInstallDifferentVersionFails(t *testing.T) {
	pkg := writeTestPackage(t)
	root := t.TempDir()
	var out, errOut bytes.Buffer
	opts := installOptions{Yes: true}
	if _, err := installTeamPackage(root, pkg, opts, &out, &errOut, nil); err != nil {
		t.Fatalf("first install: %v", err)
	}

	bumped := strings.Replace(testManifestText(), "version: 1.2.0", "version: 1.3.0", 1)
	if err := os.WriteFile(filepath.Join(pkg, "heron-team.yml"), []byte(bumped), 0o644); err != nil {
		t.Fatalf("rewrite manifest: %v", err)
	}
	_, err := installTeamPackage(root, pkg, opts, &out, &errOut, nil)
	if err == nil {
		t.Fatal("expected version conflict")
	}
	if !strings.Contains(err.Error(), "heron teams remove @review-team") {
		t.Fatalf("error should point at remove: %v", err)
	}
	if got := readIndex(t, root).Installed["review-team"].Version; got != "1.2.0" {
		t.Fatalf("index version changed to %q", got)
	}
}

func TestInstallTrimsTools(t *testing.T) {
	pkg := writeTestPackage(t)
	res, root, _, err := installTest(t, pkg, installOptions{Yes: true}, "")
	if err != nil {
		t.Fatalf("install: %v", err)
	}
	if len(res.Trimmed) != 1 || res.Trimmed[0].Agent != "security-reviewer" {
		t.Fatalf("trimmed = %+v", res.Trimmed)
	}
	if got := strings.Join(res.Trimmed[0].Tools, ","); got != "Bash" {
		t.Fatalf("trimmed tools = %q", got)
	}

	data, err := os.ReadFile(filepath.Join(root, ".agents", "_teams", "review-team", "agents/security-reviewer/AGENT.md"))
	if err != nil {
		t.Fatalf("read agent: %v", err)
	}
	text := string(data)
	if strings.Contains(text, "Bash") {
		t.Fatalf("Bash survived trimming:\n%s", text)
	}
	if !strings.Contains(text, "You review security.") {
		t.Fatalf("body lost:\n%s", text)
	}
	// Untouched fields have to survive: the rewrite must not rebuild the file
	// from types.AgentConfig, which would drop tools.custom.
	if !strings.Contains(text, "my-tool") || !strings.Contains(text, "claude-sonnet-4.6") {
		t.Fatalf("other fields lost:\n%s", text)
	}

	// The trimmed file must still parse as an agent with the expected tools.
	tree, err := teamspkg.Collect(newTestFileStore(), filepath.Join(root, ".agents", "_teams", "review-team"))
	if err != nil {
		t.Fatalf("collect: %v", err)
	}
	got := tree.Agents["security-reviewer"].Tools.Builtin
	if strings.Join(got, ",") != "Read,Grep" {
		t.Fatalf("builtin after trim = %v", got)
	}
}

func TestInstallScriptsDenyFails(t *testing.T) {
	pkg := writeTestPackage(t)
	manifest := strings.Replace(testManifestText(), "scripts: review", "scripts: deny", 1)
	if err := os.WriteFile(filepath.Join(pkg, "heron-team.yml"), []byte(manifest), 0o644); err != nil {
		t.Fatalf("rewrite manifest: %v", err)
	}
	_, root, _, err := installTest(t, pkg, installOptions{Yes: true}, "")
	if err == nil {
		t.Fatal("expected deny to fail")
	}
	if !strings.Contains(err.Error(), "scan.sh") {
		t.Fatalf("error should name the script: %v", err)
	}
	if _, statErr := os.Stat(filepath.Join(root, ".agents", "_teams", "review-team")); !os.IsNotExist(statErr) {
		t.Fatalf("deny still wrote the package: %v", statErr)
	}
	if _, statErr := os.Stat(teamsIndexPath(root)); !os.IsNotExist(statErr) {
		t.Fatalf("deny still wrote the index")
	}
}

func TestInstallScriptsReviewRequiresConfirmation(t *testing.T) {
	pkg := writeTestPackage(t)

	_, root, stdout, err := installTest(t, pkg, installOptions{}, "n\n")
	if err == nil {
		t.Fatal("expected refusal to cancel the install")
	}
	if !strings.Contains(stdout, "echo scan") {
		t.Fatalf("script content not shown:\n%s", stdout)
	}
	if _, statErr := os.Stat(filepath.Join(root, ".agents", "_teams", "review-team")); !os.IsNotExist(statErr) {
		t.Fatal("cancelled install wrote the package")
	}

	res, _, _, err := installTest(t, pkg, installOptions{}, "y\n")
	if err != nil {
		t.Fatalf("install with approval: %v", err)
	}
	if res.ScriptsReviewed != 1 {
		t.Fatalf("scripts reviewed = %d", res.ScriptsReviewed)
	}
}

func TestInstallScriptsAllowSkipsReview(t *testing.T) {
	pkg := writeTestPackage(t)
	manifest := strings.Replace(testManifestText(), "scripts: review", "scripts: allow", 1)
	if err := os.WriteFile(filepath.Join(pkg, "heron-team.yml"), []byte(manifest), 0o644); err != nil {
		t.Fatalf("rewrite manifest: %v", err)
	}
	// No stdin and no --yes: allow must not prompt.
	if _, _, _, err := installTest(t, pkg, installOptions{}, ""); err != nil {
		t.Fatalf("install: %v", err)
	}
}

func TestInstallRejectsTarballWithSymlink(t *testing.T) {
	pkg := writeTestPackage(t)
	archive := filepath.Join(t.TempDir(), "linked.tar.gz")
	tarWithSymlink(t, pkg, archive)

	_, root, _, err := installTest(t, archive, installOptions{Yes: true}, "")
	if err == nil {
		t.Fatal("expected symlink rejection")
	}
	if !strings.Contains(err.Error(), "link") {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, statErr := os.Stat(filepath.Join(root, ".agents", "_teams", "review-team")); !os.IsNotExist(statErr) {
		t.Fatal("rejected archive still installed")
	}
}

func TestInstallRejectsOversizedFile(t *testing.T) {
	pkg := writeTestPackage(t)
	big := filepath.Join(pkg, "agents/security-reviewer/big.bin")
	f, err := os.Create(big)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := f.Truncate(maxPackageFileSize + 1); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	f.Close()
	archive := filepath.Join(t.TempDir(), "big.tar.gz")
	tarDir(t, pkg, archive, "")

	if _, _, _, err := installTest(t, archive, installOptions{Yes: true}, ""); err == nil {
		t.Fatal("expected size rejection")
	}
}

func TestInstallKeepsOtherScopeEntries(t *testing.T) {
	root := t.TempDir()
	idx := teamsIndex{Version: teamsIndexVersion, Installed: map[string]installedTeam{
		"other": {Version: "0.1.0", Source: "./other", InstalledAt: "2026-01-01T00:00:00Z"},
	}}
	if err := updateTeamsIndex(root, "other", idx.Installed["other"]); err != nil {
		t.Fatalf("seed index: %v", err)
	}

	pkg := writeTestPackage(t)
	var out, errOut bytes.Buffer
	if _, err := installTeamPackage(root, pkg, installOptions{Yes: true}, &out, &errOut, nil); err != nil {
		t.Fatalf("install: %v", err)
	}

	got := readIndex(t, root)
	if _, ok := got.Installed["other"]; !ok {
		t.Fatalf("other scope dropped: %+v", got.Installed)
	}
	if _, ok := got.Installed["review-team"]; !ok {
		t.Fatalf("review-team missing: %+v", got.Installed)
	}

	raw, err := os.ReadFile(teamsIndexPath(root))
	if err != nil {
		t.Fatalf("read index: %v", err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("index is not JSON: %v", err)
	}
}

func TestInstallDryRunWritesNothing(t *testing.T) {
	pkg := writeTestPackage(t)
	_, root, stdout, err := installTest(t, pkg, installOptions{DryRun: true}, "")
	if err != nil {
		t.Fatalf("dry run: %v", err)
	}
	if !strings.Contains(stdout, "Dry run: nothing was written.") {
		t.Fatalf("output:\n%s", stdout)
	}
	if _, statErr := os.Stat(filepath.Join(root, ".agents")); !os.IsNotExist(statErr) {
		t.Fatal("dry run wrote .agents/")
	}
}

func TestInstallScopeOverride(t *testing.T) {
	pkg := writeTestPackage(t)
	res, root, _, err := installTest(t, pkg, installOptions{Yes: true, Scope: "alt-scope"}, "")
	if err != nil {
		t.Fatalf("install: %v", err)
	}
	if res.Scope != "alt-scope" {
		t.Fatalf("scope = %q", res.Scope)
	}
	if _, err := os.Stat(filepath.Join(root, ".agents", "_teams", "alt-scope", "heron-team.yml")); err != nil {
		t.Fatalf("package not under overridden scope: %v", err)
	}
	if _, ok := readIndex(t, root).Installed["alt-scope"]; !ok {
		t.Fatal("index keyed by manifest name instead of scope")
	}
}

func TestInstallRejectsInvalidScope(t *testing.T) {
	pkg := writeTestPackage(t)
	if _, _, _, err := installTest(t, pkg, installOptions{Yes: true, Scope: "../escape"}, ""); err == nil {
		t.Fatal("expected invalid scope rejection")
	}
}

func TestInstallSummarySuggestsFlowBinding(t *testing.T) {
	pkg := writeTestPackage(t)
	root := t.TempDir()
	var out, errOut bytes.Buffer
	manifest, err := teamspkg.LoadManifest([]byte(testManifestText()))
	if err != nil {
		t.Fatalf("manifest: %v", err)
	}
	res, err := installTeamPackage(root, pkg, installOptions{Yes: true}, &out, &errOut, nil)
	if err != nil {
		t.Fatalf("install: %v", err)
	}
	writeInstallSummary(&out, manifest, res)
	text := out.String()
	if !strings.Contains(text, "Installed team package: review-team 1.2.0 -> .agents/_teams/review-team/") {
		t.Fatalf("summary:\n%s", text)
	}
	if !strings.Contains(text, "trimmed tools: security-reviewer: Bash") {
		t.Fatalf("summary missing trimmed tools:\n%s", text)
	}
	if !strings.Contains(text, "scripts reviewed: 1") {
		t.Fatalf("summary missing script count:\n%s", text)
	}
	if !strings.Contains(text, `team: "review-team/review_team"`) {
		t.Fatalf("summary missing flow snippet:\n%s", text)
	}
	// The Flow must never be written by the installer.
	if _, statErr := os.Stat(filepath.Join(root, ".agents", "flows")); !os.IsNotExist(statErr) {
		t.Fatal("installer wrote a flow file")
	}
}

// runTeamsAddFromInputs is what teams_cli.go will call, so the wiring itself is
// covered here even while the dispatcher still routes to the dry-run branch.
func TestRunTeamsAddFromInputs(t *testing.T) {
	pkg := writeTestPackage(t)
	root := t.TempDir()
	var out, errOut bytes.Buffer
	err := runTeamsAddFromInputs(root, teamsAddInputs{
		Source: pkg,
		Yes:    true,
		Stdout: &out,
		Stderr: &errOut,
		Stdin:  strings.NewReader(""),
	})
	if err != nil {
		t.Fatalf("runTeamsAddFromInputs: %v", err)
	}
	if !strings.Contains(out.String(), "Installed team package: review-team 1.2.0") {
		t.Fatalf("no install summary:\n%s", out.String())
	}

	var dryOut bytes.Buffer
	if err := runTeamsAddFromInputs(root, teamsAddInputs{
		Source: pkg,
		DryRun: true,
		Stdout: &dryOut,
		Stderr: &errOut,
	}); err != nil {
		t.Fatalf("dry run: %v", err)
	}
	if strings.Contains(dryOut.String(), "Installed team package") {
		t.Fatalf("dry run printed an install summary:\n%s", dryOut.String())
	}
	if !strings.Contains(dryOut.String(), "Dry run: nothing was written.") {
		t.Fatalf("dry run output:\n%s", dryOut.String())
	}
}

// A tarball via the same entry point: the CLI's source argument must reach the
// archive path without the caller unpacking anything.
func TestRunTeamsAddFromInputsTarball(t *testing.T) {
	pkg := writeTestPackage(t)
	archive := filepath.Join(t.TempDir(), "review-team.tar.gz")
	tarDir(t, pkg, archive, "review-team")

	root := t.TempDir()
	var out, errOut bytes.Buffer
	err := runTeamsAddFromInputs(root, teamsAddInputs{
		Source: archive,
		Yes:    true,
		Stdout: &out,
		Stderr: &errOut,
	})
	if err != nil {
		t.Fatalf("runTeamsAddFromInputs: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, ".agents", "_teams", "review-team", "teams", "review_team.yml")); err != nil {
		t.Fatalf("tarball not installed: %v", err)
	}
	// A fresh scope has no models.json, so every declared model is reported as
	// missing: "cannot resolve" and "absent" are the same thing to the runner.
	if !strings.Contains(out.String(), "missing models: claude-sonnet-4.6") {
		t.Fatalf("expected missing model hint, got:\n%s", out.String())
	}

	// With the model registered the hint must disappear.
	writeModelsJSON(t, root, "claude-sonnet-4.6")
	root2 := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root2, ".agents"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	writeModelsJSON(t, root2, "claude-sonnet-4.6")
	var out2, errOut2 bytes.Buffer
	if err := runTeamsAddFromInputs(root2, teamsAddInputs{Source: archive, Yes: true, Stdout: &out2, Stderr: &errOut2}); err != nil {
		t.Fatalf("install: %v", err)
	}
	if strings.Contains(out2.String(), "missing models") {
		t.Fatalf("unexpected missing-model hint:\n%s", out2.String())
	}
}

func writeModelsJSON(t *testing.T, root, model string) {
	t.Helper()
	body := "{\"model\": \"" + model + "\", \"models\": [{\"name\": \"" + model + "\", \"provider\": \"test\"}]}"
	if err := os.WriteFile(filepath.Join(root, ".agents", "models.json"), []byte(body), 0o644); err != nil {
		t.Fatalf("write models.json: %v", err)
	}
}
