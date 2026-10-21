package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// installFixture installs the shared test package under scope and returns the
// project root. Going through the real installer keeps the fixture and the
// ledger shape in sync with what remove will actually be handed.
func installFixture(t *testing.T, scope string) string {
	t.Helper()
	root := t.TempDir()
	var out, errOut bytes.Buffer
	if _, err := installTeamPackage(root, writeTestPackage(t), installOptions{Scope: scope, Yes: true}, &out, &errOut, nil); err != nil {
		t.Fatalf("install: %v\n%s", err, errOut.String())
	}
	return root
}

func readIndexForTest(t *testing.T, root string) teamsIndex {
	t.Helper()
	idx, err := readTeamsIndex(root)
	if err != nil {
		t.Fatalf("read index: %v", err)
	}
	return *idx
}

func TestRemoveDeletesPackageAndIndexEntry(t *testing.T) {
	root := installFixture(t, "review-team")
	other := installFixture(t, "other-team")
	// Two scopes in one project is the interesting case for "other scopes are
	// untouched", so copy the second install into the first root.
	if err := os.Rename(filepath.Join(other, ".agents", "_teams", "other-team"), filepath.Join(root, ".agents", "_teams", "other-team")); err != nil {
		t.Fatalf("setup second scope: %v", err)
	}
	idx := readIndexForTest(t, root)
	idx.Installed["other-team"] = installedTeam{Version: "1.2.0", Source: "./pkg", InstalledAt: "2026-01-01T00:00:00Z"}
	if err := writeTeamsIndex(root, &idx); err != nil {
		t.Fatalf("write index: %v", err)
	}

	var out bytes.Buffer
	if err := runTeamsRemove(root, []string{"@review-team"}, &out); err != nil {
		t.Fatalf("remove: %v", err)
	}

	if _, err := os.Stat(filepath.Join(root, ".agents", "_teams", "review-team")); !os.IsNotExist(err) {
		t.Fatalf("package directory still present: %v", err)
	}
	got := readIndexForTest(t, root)
	if _, ok := got.Installed["review-team"]; ok {
		t.Fatal("index entry was not dropped")
	}
	if _, ok := got.Installed["other-team"]; !ok {
		t.Fatal("unrelated index entry was dropped")
	}
	if _, err := os.Stat(filepath.Join(root, ".agents", "_teams", "other-team")); err != nil {
		t.Fatalf("unrelated scope directory was touched: %v", err)
	}
	want := "Removed team package: @review-team\n  deleted: .agents/_teams/review-team/\n"
	if out.String() != want {
		t.Fatalf("output mismatch:\nwant: %q\ngot:  %q", want, out.String())
	}
}

func TestRemoveAcceptsScopeWithoutAtSign(t *testing.T) {
	root := installFixture(t, "review-team")
	var out bytes.Buffer
	if err := runTeamsRemove(root, []string{"review-team"}, &out); err != nil {
		t.Fatalf("remove: %v", err)
	}
	if !strings.Contains(out.String(), "Removed team package: @review-team") {
		t.Fatalf("unexpected output: %q", out.String())
	}
}

func TestRemoveKeepAgentsPreservesAssets(t *testing.T) {
	root := installFixture(t, "review-team")
	var out bytes.Buffer
	if err := runTeamsRemove(root, []string{"@review-team", "--keep-agents"}, &out); err != nil {
		t.Fatalf("remove --keep-agents: %v", err)
	}

	dir := filepath.Join(root, ".agents", "_teams", "review-team")
	for _, gone := range []string{"teams", teamManifestFile} {
		if _, err := os.Stat(filepath.Join(dir, gone)); !os.IsNotExist(err) {
			t.Fatalf("%s should be removed, stat err = %v", gone, err)
		}
	}
	for _, kept := range []string{"agents/security-reviewer/AGENT.md", "skills/owasp-check/SKILL.md", "rules/review-standards.md"} {
		if _, err := os.Stat(filepath.Join(dir, kept)); err != nil {
			t.Fatalf("%s should be kept: %v", kept, err)
		}
	}

	entry, ok := readIndexForTest(t, root).Installed["review-team"]
	if !ok {
		t.Fatal("keep-agents must keep the index entry so the scope stays visible")
	}
	if !entry.Partial || !entry.KeptAgents {
		t.Fatalf("entry not marked partial: %+v", entry)
	}
	if !strings.Contains(out.String(), "kept: agents, skills, rules") {
		t.Fatalf("unexpected output: %q", out.String())
	}
}

func TestRemoveMissingPackage(t *testing.T) {
	root := t.TempDir()
	var out bytes.Buffer
	err := runTeamsRemove(root, []string{"@nope"}, &out)
	if err == nil || !strings.Contains(err.Error(), "no installed team package @nope") {
		t.Fatalf("expected missing package error, got %v", err)
	}
	if out.Len() != 0 {
		t.Fatalf("failed remove must not print a summary: %q", out.String())
	}
}

func TestRemoveRejectsPathTraversal(t *testing.T) {
	root := t.TempDir()
	outside := filepath.Join(root, "victim")
	if err := os.MkdirAll(outside, 0o755); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	err := runTeamsRemove(root, []string{"@../victim"}, &out)
	if err == nil || !strings.Contains(err.Error(), "invalid team package name") {
		t.Fatalf("expected invalid name error, got %v", err)
	}
	if _, statErr := os.Stat(outside); statErr != nil {
		t.Fatalf("path outside _teams was touched: %v", statErr)
	}
}

func TestRemoveWithoutIndexFile(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, ".agents", "_teams", "manual")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := runTeamsRemove(root, []string{"@manual"}, &out); err != nil {
		t.Fatalf("remove without index: %v", err)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("directory should be gone: %v", err)
	}
	if _, err := os.Stat(teamsIndexPath(root)); !os.IsNotExist(err) {
		t.Fatalf("a missing index must not be created: %v", err)
	}
}

func TestRemoveMissingName(t *testing.T) {
	var out bytes.Buffer
	if err := runTeamsRemove(t.TempDir(), nil, &out); err == nil || !strings.Contains(err.Error(), "missing <name>") {
		t.Fatalf("expected missing name error, got %v", err)
	}
}

func TestRemoveUnknownFlag(t *testing.T) {
	var out bytes.Buffer
	if err := runTeamsRemove(t.TempDir(), []string{"@x", "--force"}, &out); err == nil || !strings.Contains(err.Error(), "unknown flag") {
		t.Fatalf("expected unknown flag error, got %v", err)
	}
}

// TestRemoveUpdatesListStatus ties remove to the listing: the partial entry has
// to be discoverable, otherwise a user who kept agents cannot find it again.
func TestRemoveUpdatesListStatus(t *testing.T) {
	root := installFixture(t, "review-team")
	var out bytes.Buffer
	if err := runTeamsRemove(root, []string{"@review-team", "--keep-agents"}, &out); err != nil {
		t.Fatalf("remove: %v", err)
	}
	out.Reset()
	if err := listTeams(root, &out); err != nil {
		t.Fatalf("list: %v", err)
	}
	lines := strings.Split(strings.TrimRight(out.String(), "\n"), "\n")
	if len(lines) != 2 {
		t.Fatalf("expected header + 1 row, got %q", out.String())
	}
	if !strings.HasPrefix(lines[1], "@review-team") || !strings.HasSuffix(lines[1], "partial") {
		t.Fatalf("expected partial row, got %q", lines[1])
	}
}

func TestRemoveDropsPartiallyIndexedEntry(t *testing.T) {
	// A plain remove after --keep-agents must clear the leftover entry.
	root := installFixture(t, "review-team")
	var out bytes.Buffer
	if err := runTeamsRemove(root, []string{"@review-team", "--keep-agents"}, &out); err != nil {
		t.Fatalf("keep-agents: %v", err)
	}
	out.Reset()
	if err := runTeamsRemove(root, []string{"@review-team"}, &out); err != nil {
		t.Fatalf("remove: %v", err)
	}
	if _, ok := readIndexForTest(t, root).Installed["review-team"]; ok {
		t.Fatal("index entry survived a full remove")
	}
	// The ledger itself stays valid JSON with an empty installed map.
	data, err := os.ReadFile(teamsIndexPath(root))
	if err != nil {
		t.Fatalf("ledger missing: %v", err)
	}
	var idx teamsIndex
	if err := json.Unmarshal(data, &idx); err != nil {
		t.Fatalf("ledger is not valid JSON: %v", err)
	}
	if len(idx.Installed) != 0 {
		t.Fatalf("expected empty ledger, got %+v", idx.Installed)
	}
}
