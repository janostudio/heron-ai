package main

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func mustWrite(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func TestTeamsNoSubcommandInEmptyDir(t *testing.T) {
	dir := t.TempDir()
	var out bytes.Buffer
	if err := listTeams(dir, &out); err != nil {
		t.Fatalf("listTeams: %v", err)
	}
	if got := strings.TrimSpace(out.String()); got != "no teams" {
		t.Fatalf("expected no teams, got %q", got)
	}
}

func TestTeamsNoSubcommandViaDispatcher(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	var out, errOut bytes.Buffer
	if err := runTeams(nil, &out, &errOut); err != nil {
		t.Fatalf("runTeams: %v", err)
	}
	if !strings.Contains(out.String(), "no teams") {
		t.Fatalf("expected no teams, got %q", out.String())
	}
}

func TestTeamsListIsUnknownSubcommand(t *testing.T) {
	var out, errOut bytes.Buffer
	err := runTeams([]string{"list"}, &out, &errOut)
	if err == nil {
		t.Fatal("expected error for teams list")
	}
	if !errors.Is(err, errUnknownTeamsSubcommand) {
		t.Fatalf("expected errUnknownTeamsSubcommand, got %v", err)
	}
}

func TestTeamsUnknownSubcommand(t *testing.T) {
	for _, sub := range []string{"ls", "show", "bogus"} {
		var out, errOut bytes.Buffer
		if err := runTeams([]string{sub}, &out, &errOut); !errors.Is(err, errUnknownTeamsSubcommand) {
			t.Fatalf("%s: expected unknown subcommand error, got %v", sub, err)
		}
	}
}

// The dispatcher must reach the real remove path; the behaviour itself is
// covered by the teams_remove tests, so this only proves the routing.
func TestTeamsRemoveMissingPackage(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	var out, errOut bytes.Buffer
	err := runTeams([]string{"remove", "x"}, &out, &errOut)
	if err == nil || !strings.Contains(err.Error(), "no installed team package @x") {
		t.Fatalf("remove: expected missing package error, got %v", err)
	}
}

func TestListTeamsMixedSources(t *testing.T) {
	dir := t.TempDir()
	mustWrite(t, filepath.Join(dir, ".agents", "teams", "review_team.yml"), "id: review_team\n")
	mustWrite(t, filepath.Join(dir, ".agents", "_teams", "review-team", "heron-team.yml"), "version: 1.2.0\nsource: github.com/x/y#v1.2\n")
	var out bytes.Buffer
	if err := listTeams(dir, &out); err != nil {
		t.Fatalf("listTeams: %v", err)
	}
	lines := strings.Split(strings.TrimRight(out.String(), "\n"), "\n")
	if len(lines) != 3 {
		t.Fatalf("expected header + 2 rows, got %d: %q", len(lines), out.String())
	}
	if !strings.HasPrefix(lines[0], "TEAM") || !strings.Contains(lines[0], "SOURCE") || !strings.Contains(lines[0], "STATUS") {
		t.Fatalf("unexpected header: %q", lines[0])
	}
	// Names sort as @review-team < review_team.
	if !strings.HasPrefix(lines[1], "@review-team") {
		t.Fatalf("expected @review-team first, got %q", lines[1])
	}
	if !strings.Contains(lines[1], "github.com/x/y#v1.2") || !strings.Contains(lines[1], "1.2.0") || !strings.HasSuffix(lines[1], "ok") {
		t.Fatalf("unexpected installed row: %q", lines[1])
	}
	if !strings.HasPrefix(lines[2], "review_team") || !strings.Contains(lines[2], "local") || !strings.HasSuffix(lines[2], "ok") {
		t.Fatalf("unexpected local row: %q", lines[2])
	}
	if strings.Contains(lines[2], "1.2.0") {
		t.Fatalf("local row should not carry a version: %q", lines[2])
	}
}

func TestListTeamsIgnoresIncompletePackageDir(t *testing.T) {
	dir := t.TempDir()
	mustWrite(t, filepath.Join(dir, ".agents", "_teams", "broken", "README.md"), "x")
	var out bytes.Buffer
	if err := listTeams(dir, &out); err != nil {
		t.Fatalf("listTeams: %v", err)
	}
	if !strings.Contains(out.String(), "no teams") {
		t.Fatalf("expected no teams, got %q", out.String())
	}
}

func buildTeamPackage(t *testing.T, root string) string {
	t.Helper()
	pkg := filepath.Join(root, "review-team")
	mustWrite(t, filepath.Join(pkg, "heron-team.yml"), "apiVersion: heron/v1\nkind: team\nname: review-team\nversion: 1.2.0\ndescription: 安全与性能并行审查\nprovides:\n  teams: [review_team]\n  agents: [security-reviewer, lead-reviewer]\n  skills: [owasp-check]\n  rules: [review-standards]\n")
	mustWrite(t, filepath.Join(pkg, "teams", "review_team.yml"), "id: review_team\n")
	mustWrite(t, filepath.Join(pkg, "agents", "security-reviewer", "AGENT.md"), "# a\n")
	mustWrite(t, filepath.Join(pkg, "agents", "lead-reviewer", "AGENT.md"), "# b\n")
	mustWrite(t, filepath.Join(pkg, "skills", "owasp-check", "SKILL.md"), "# s\n")
	mustWrite(t, filepath.Join(pkg, "skills", "owasp-check", "scripts", "scan.sh"), "echo\n")
	mustWrite(t, filepath.Join(pkg, "rules", "review-standards.md"), "# r\n")
	return pkg
}

func snapshotTree(t *testing.T, root string) []string {
	t.Helper()
	var paths []string
	err := filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		paths = append(paths, p+"|"+info.ModTime().String()+"|"+strconv.FormatInt(info.Size(), 10))
		return nil
	})
	if err != nil {
		t.Fatalf("walk: %v", err)
	}
	return paths
}

func TestTeamsAddDryRun(t *testing.T) {
	root := t.TempDir()
	pkg := buildTeamPackage(t, root)
	before := snapshotTree(t, root)

	work := t.TempDir()
	t.Chdir(work)

	var out, errOut bytes.Buffer
	if err := runTeams([]string{"add", pkg, "--dry-run"}, &out, &errOut); err != nil {
		t.Fatalf("runTeams add --dry-run: %v", err)
	}

	s := out.String()
	// Only the invariants of the pipeline are asserted here: the exact counts
	// depend on what Collect can parse out of the fixture, and the richer
	// assertions live in the installer's own tests.
	for _, want := range []string{"Applying team package: review-team 1.2.0", "description: 安全与性能并行审查", "teams:    1 (review_team)", "Target: .agents/_teams/review-team/", "Dry run: nothing was written."} {
		if !strings.Contains(s, want) {
			t.Fatalf("preview missing %q:\n%s", want, s)
		}
	}

	after := snapshotTree(t, root)
	if strings.Join(before, "|") != strings.Join(after, "|") {
		t.Fatal("dry-run modified the package directory")
	}
	entries, err := os.ReadDir(work)
	if err != nil {
		t.Fatalf("readdir: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("dry-run wrote into cwd: %v", entries)
	}
}

func TestTeamsAddDryRunAcceptsIgnoredFlags(t *testing.T) {
	root := t.TempDir()
	pkg := buildTeamPackage(t, root)
	var out, errOut bytes.Buffer
	if err := runTeams([]string{"add", pkg, "--dry-run", "--yes", "--as", "other", "--refresh"}, &out, &errOut); err != nil {
		t.Fatalf("runTeams: %v", err)
	}
	if !strings.Contains(out.String(), "Dry run") {
		t.Fatalf("expected preview, got %q", out.String())
	}
}

func TestTeamsAddMissingManifest(t *testing.T) {
	pkg := filepath.Join(t.TempDir(), "nomanifest")
	if err := os.MkdirAll(pkg, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	var out, errOut bytes.Buffer
	err := runTeams([]string{"add", pkg, "--dry-run"}, &out, &errOut)
	if err == nil || !strings.Contains(err.Error(), "missing heron-team.yml") {
		t.Fatalf("expected missing manifest error, got %v", err)
	}
}

func TestTeamsAddMissingSourceDir(t *testing.T) {
	var out, errOut bytes.Buffer
	err := runTeams([]string{"add", filepath.Join(t.TempDir(), "nope"), "--dry-run"}, &out, &errOut)
	if err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("expected not-found error, got %v", err)
	}
}

// TestTeamsAddWithoutDryRunRunsTheInstaller is the routing test: a real install
// must reach the installer rather than the old stub. The installer itself is
// covered by the teams_install tests; here the package is deliberately
// malformed so the assertion only needs the error to come from manifest
// validation, proving the call left the CLI layer.
func TestTeamsAddWithoutDryRunRunsTheInstaller(t *testing.T) {
	pkg := t.TempDir()
	if err := os.WriteFile(filepath.Join(pkg, "heron-team.yml"), []byte("kind: team\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	var out, errOut bytes.Buffer
	err := runTeams([]string{"add", pkg}, &out, &errOut)
	if err == nil || !strings.Contains(err.Error(), "apiVersion") {
		t.Fatalf("expected manifest validation to run, got %v", err)
	}
}

func TestTeamsAddMissingSource(t *testing.T) {
	var out, errOut bytes.Buffer
	err := runTeams([]string{"add", "--dry-run"}, &out, &errOut)
	if err == nil || !strings.Contains(err.Error(), "missing <source>") {
		t.Fatalf("expected missing source error, got %v", err)
	}
}
