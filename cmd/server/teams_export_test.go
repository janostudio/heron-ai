package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// writeExportFixture builds a full .agents tree: reachable assets, assets the
// team never references, and the four categories that must never ship.
func writeExportFixture(t *testing.T, dir string) {
	t.Helper()
	r := filepath.Join(dir, ".agents")
	mustWrite(t, filepath.Join(r, "teams", "review_team.yml"), strings.Join([]string{
		"id: review_team",
		"calls:",
		"  lead:",
		"    type: agent",
		"    agent: lead-reviewer",
		"  sec:",
		"    type: agent",
		"    agent: security-reviewer",
		"",
	}, "\n"))
	mustWrite(t, filepath.Join(r, "agents", "lead-reviewer", "AGENT.md"), strings.Join([]string{
		"---",
		"name: lead-reviewer",
		"model:",
		"  model: test-model",
		"skills: [owasp-check]",
		"rules: [review-standards]",
		"---",
		"Lead the review.",
		"",
	}, "\n"))
	mustWrite(t, filepath.Join(r, "agents", "security-reviewer", "AGENT.md"), strings.Join([]string{
		"---",
		"name: security-reviewer",
		"model:",
		"  model: test-model",
		"---",
		"Check security.",
		"",
	}, "\n"))
	mustWrite(t, filepath.Join(r, "agents", "security-reviewer", "rules", "private.md"), "---\nid: private-rule\n---\nPrivate rule.\n")
	// Assets the team never reaches: exporting them would leak unrelated work.
	mustWrite(t, filepath.Join(r, "agents", "unused-agent", "AGENT.md"), "---\nname: unused-agent\n---\nUnused.\n")
	mustWrite(t, filepath.Join(r, "skills", "owasp-check", "SKILL.md"), "---\nname: owasp-check\nscripts: [scripts/scan.sh]\n---\nScan.\n")
	mustWrite(t, filepath.Join(r, "skills", "owasp-check", "scripts", "scan.sh"), "#!/bin/sh\necho scan\n")
	mustWrite(t, filepath.Join(r, "skills", "unused-skill", "SKILL.md"), "---\nname: unused-skill\n---\nUnused.\n")
	mustWrite(t, filepath.Join(r, "rules", "review-standards.md"), "---\nid: review-standards\n---\nStandards.\n")

	// Never exported. Each one carries a unique sentinel so a test can prove
	// it did not travel by grepping the whole package.
	mustWrite(t, filepath.Join(r, "knowledge", "global.md"), "GLOBAL-KNOWLEDGE\n")
	mustWrite(t, filepath.Join(r, "agents", "lead-reviewer", "knowledge", "note.md"), "AGENT-KNOWLEDGE\n")
	mustWrite(t, filepath.Join(r, "teams", "review_team", "knowledge", "team.md"), "TEAM-KNOWLEDGE\n")
	mustWrite(t, filepath.Join(r, "models.json"), `{"models": {"test-model": {"api_key": "sk-abcdefgh12345678"}}}`)
	mustWrite(t, filepath.Join(r, "flows", "default.yml"), "teams: {}\n")
	mustWrite(t, filepath.Join(r, "settings.json"), "{}\n")
	mustWrite(t, filepath.Join(r, "data", "session.jsonl"), "{}\n")
	mustWrite(t, filepath.Join(dir, ".agents", ".teams.json"), "{}\n")
}

// readTarball returns package-relative path -> content for a Heron team archive.
func readTarball(t *testing.T, file string) map[string]string {
	t.Helper()
	data, err := os.ReadFile(file)
	if err != nil {
		t.Fatalf("read %s: %v", file, err)
	}
	gz, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("gzip: %v", err)
	}
	tr := tar.NewReader(gz)
	out := map[string]string{}
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("tar next: %v", err)
		}
		body, err := io.ReadAll(tr)
		if err != nil {
			t.Fatalf("tar read %s: %v", hdr.Name, err)
		}
		out[hdr.Name] = string(body)
	}
	return out
}

func TestExportTeamTarballStructure(t *testing.T) {
	dir := t.TempDir()
	writeExportFixture(t, dir)
	var out bytes.Buffer
	err := exportTeam(exportConfig{TeamID: "review_team", OutDir: dir, Root: dir, Stdout: &out})
	if err != nil {
		t.Fatalf("exportTeam: %v", err)
	}

	archive := filepath.Join(dir, "review-team.tar.gz")
	if _, err := os.Stat(archive); err != nil {
		t.Fatalf("expected %s: %v", archive, err)
	}
	files := readTarball(t, archive)
	for _, want := range []string{
		"review-team/heron-team.yml",
		"review-team/teams/review_team.yml",
		"review-team/agents/lead-reviewer/AGENT.md",
		"review-team/agents/security-reviewer/AGENT.md",
		"review-team/agents/security-reviewer/rules/private.md",
		"review-team/skills/owasp-check/SKILL.md",
		"review-team/skills/owasp-check/scripts/scan.sh",
		"review-team/rules/review-standards.md",
	} {
		if _, ok := files[want]; !ok {
			t.Errorf("missing %s in package", want)
		}
	}
	for p := range files {
		if !strings.HasPrefix(p, "review-team/") {
			t.Errorf("entry %s is outside the package top directory", p)
		}
		if strings.Contains(p, "unused-agent") || strings.Contains(p, "unused-skill") {
			t.Errorf("closure leaked unrelated asset %s", p)
		}
	}

	if !strings.Contains(out.String(), "Exported team package: review-team 0.1.0") ||
		!strings.Contains(out.String(), "agents:  2 (lead-reviewer, security-reviewer)") {
		t.Fatalf("unexpected report:\n%s", out.String())
	}
}

func TestExportTeamExcludesKnowledgeAndLocalEnvironment(t *testing.T) {
	dir := t.TempDir()
	writeExportFixture(t, dir)
	if err := exportTeam(exportConfig{TeamID: "review_team", OutDir: dir, Format: formatDir, Root: dir}); err != nil {
		t.Fatalf("exportTeam: %v", err)
	}

	pkg := filepath.Join(dir, "review-team")
	var sentinels []string
	err := filepath.WalkDir(pkg, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() && d.Name() == "knowledge" {
			t.Errorf("knowledge directory exported: %s", p)
		}
		if d.IsDir() {
			return nil
		}
		data, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		sentinels = append(sentinels, string(data))
		return nil
	})
	if err != nil {
		t.Fatalf("walk: %v", err)
	}
	joined := strings.Join(sentinels, "\n")
	for _, leaked := range []string{"GLOBAL-KNOWLEDGE", "AGENT-KNOWLEDGE", "TEAM-KNOWLEDGE", "sk-abcdefgh12345678"} {
		if strings.Contains(joined, leaked) {
			t.Errorf("package contains %q", leaked)
		}
	}
	for _, absent := range []string{"models.json", "flows", "settings.json", "data"} {
		if _, err := os.Stat(filepath.Join(pkg, absent)); err == nil {
			t.Errorf("%s was exported", absent)
		}
	}
}

func TestExportTeamManifestProvidesMatchesClosure(t *testing.T) {
	dir := t.TempDir()
	writeExportFixture(t, dir)
	if err := exportTeam(exportConfig{TeamID: "review_team", OutDir: dir, Format: formatDir, Version: "1.2.3", Root: dir}); err != nil {
		t.Fatalf("exportTeam: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(dir, "review-team", "heron-team.yml"))
	if err != nil {
		t.Fatalf("read manifest: %v", err)
	}
	var m struct {
		APIVersion string `yaml:"apiVersion"`
		Kind       string `yaml:"kind"`
		Name       string `yaml:"name"`
		Version    string `yaml:"version"`
		Provides   struct {
			Teams  []string `yaml:"teams"`
			Agents []string `yaml:"agents"`
			Skills []string `yaml:"skills"`
			Rules  []string `yaml:"rules"`
		} `yaml:"provides"`
	}
	if err := yaml.Unmarshal(data, &m); err != nil {
		t.Fatalf("parse manifest: %v", err)
	}
	if m.APIVersion != "heron/v1" || m.Kind != "team" || m.Name != "review-team" || m.Version != "1.2.3" {
		t.Fatalf("unexpected manifest header: %+v", m)
	}
	if got := strings.Join(m.Provides.Teams, ","); got != "review_team" {
		t.Errorf("teams = %q", got)
	}
	if got := strings.Join(m.Provides.Agents, ","); got != "lead-reviewer,security-reviewer" {
		t.Errorf("agents = %q", got)
	}
	if got := strings.Join(m.Provides.Skills, ","); got != "owasp-check" {
		t.Errorf("skills = %q", got)
	}
	if got := strings.Join(m.Provides.Rules, ","); got != "review-standards" {
		t.Errorf("rules = %q", got)
	}
}

func TestExportTeamRejectsSecrets(t *testing.T) {
	dir := t.TempDir()
	writeExportFixture(t, dir)
	mustWrite(t, filepath.Join(dir, ".agents", "rules", "review-standards.md"),
		"---\nid: review-standards\n---\nok\napi_key: sk-live-0000000000\n")

	err := exportTeam(exportConfig{TeamID: "review_team", OutDir: dir, Root: dir})
	if err == nil {
		t.Fatal("expected export to fail on a secret")
	}
	msg := err.Error()
	if !strings.Contains(msg, "rules/review-standards.md:5") {
		t.Fatalf("error should name file and line, got: %s", msg)
	}
	if strings.Contains(msg, "sk-live-0000000000") {
		t.Fatalf("error leaks the secret: %s", msg)
	}
	if _, statErr := os.Stat(filepath.Join(dir, "review-team.tar.gz")); statErr == nil {
		t.Fatal("failed export must not leave an archive behind")
	}
}

func TestExportTeamDirFormat(t *testing.T) {
	dir := t.TempDir()
	writeExportFixture(t, dir)
	outDir := filepath.Join(dir, "dist")
	if err := exportTeam(exportConfig{TeamID: "review_team", OutDir: outDir, Format: formatDir, Root: dir}); err != nil {
		t.Fatalf("exportTeam: %v", err)
	}
	for _, rel := range []string{"heron-team.yml", "teams/review_team.yml", "skills/owasp-check/scripts/scan.sh"} {
		if _, err := os.Stat(filepath.Join(outDir, "review-team", filepath.FromSlash(rel))); err != nil {
			t.Errorf("missing %s: %v", rel, err)
		}
	}
	info, err := os.Stat(filepath.Join(outDir, "review-team", "skills", "owasp-check", "scripts", "scan.sh"))
	if err != nil {
		t.Fatalf("stat script: %v", err)
	}
	if info.Mode()&0o100 == 0 {
		t.Errorf("skill script lost its executable bit: %v", info.Mode())
	}
}

func TestExportTeamExplicitTarGzPath(t *testing.T) {
	dir := t.TempDir()
	writeExportFixture(t, dir)
	target := filepath.Join(dir, "custom", "out.tar.gz")
	if err := exportTeam(exportConfig{TeamID: "review_team", OutDir: target, Root: dir}); err != nil {
		t.Fatalf("exportTeam: %v", err)
	}
	if _, err := os.Stat(target); err != nil {
		t.Fatalf("expected exact path %s: %v", target, err)
	}
	if _, err := os.Stat(filepath.Join(dir, "custom", "review-team.tar.gz")); err == nil {
		t.Fatal("an explicit .tar.gz path must not also produce the default name")
	}
}

func TestExportTeamFlagsAfterTeam(t *testing.T) {
	dir := t.TempDir()
	writeExportFixture(t, dir)
	t.Chdir(dir)
	var out, errOut bytes.Buffer
	if err := runTeams([]string{"export", "review_team", "-o", filepath.Join(dir, "out"), "--format", "dir", "--version", "9.9.9"}, &out, &errOut); err != nil {
		t.Fatalf("runTeams: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "out", "review-team", "heron-team.yml")); err != nil {
		t.Fatalf("expected exported dir: %v", err)
	}
	if !strings.Contains(out.String(), "review-team 9.9.9") {
		t.Fatalf("unexpected report: %s", out.String())
	}
}

func TestExportTeamUnknownTeam(t *testing.T) {
	dir := t.TempDir()
	writeExportFixture(t, dir)
	err := exportTeam(exportConfig{TeamID: "nope", OutDir: dir, Root: dir})
	if err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("expected not-found error, got %v", err)
	}
}
