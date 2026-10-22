package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/heron-ai/heron-engine/internal/storage"
	"github.com/heron-ai/heron-engine/internal/teamspkg"
)

// Package formats accepted by teams export.
const (
	formatTarGz = "tar.gz"
	formatDir   = "dir"
)

const defaultTeamVersion = "0.1.0"

// exportConfig is the resolved form of `teams export <team> [flags]`.
type exportConfig struct {
	TeamID  string
	OutDir  string // destination directory, or an explicit *.tar.gz path
	Format  string
	Version string
	Root    string // configuration root; the asset tree is <Root>/.agents
	Stdout  io.Writer
}

// exportReport is what the command tells the user afterwards. It is rendered
// from data rather than assembled inline so the numbers cannot drift between
// the tarball and directory paths.
type exportReport struct {
	Name       string
	Version    string
	Output     string
	Teams      []string
	Agents     []string
	Skills     []string
	RuleCount  int
	Knowledge  int // knowledge directories left out
	ModelsFile bool
}

// exportTeam packs the closure of TeamID from <Root>/.agents into a shareable
// package. Nothing is written until the key scan passes, so a rejected export
// never leaves a half-written artifact behind.
func exportTeam(cfg exportConfig) error {
	if strings.TrimSpace(cfg.TeamID) == "" {
		return errors.New("teams export: missing <team>")
	}
	switch cfg.Format {
	case "", formatTarGz:
		cfg.Format = formatTarGz
	case formatDir:
	default:
		return fmt.Errorf("teams export: unsupported format %q (tar.gz|dir)", cfg.Format)
	}
	if cfg.Version == "" {
		cfg.Version = defaultTeamVersion
	}
	if cfg.OutDir == "" {
		cfg.OutDir = "."
	}
	if cfg.Stdout == nil {
		cfg.Stdout = io.Discard
	}

	agentsRoot := filepath.Join(cfg.Root, ".agents")
	// Paths handed to teamspkg use forward slashes, because Tree keys are
	// package-relative ("teams/x.yml") on every platform.
	tree, err := teamspkg.Collect(storage.NewFileStore(agentsRoot), ".")
	if err != nil {
		return fmt.Errorf("teams export: read %s: %w", agentsRoot, err)
	}
	closure, err := tree.Closure(cfg.TeamID)
	if err != nil {
		return fmt.Errorf("teams export: %w", err)
	}

	name := sanitizeTeamName(cfg.TeamID)
	files, report, err := collectExportFiles(agentsRoot, cfg.TeamID, closure)
	if err != nil {
		return err
	}

	manifest := teamspkg.Manifest{
		APIVersion: teamspkg.APIVersionV1,
		Kind:       teamspkg.KindTeam,
		Name:       name,
		Version:    cfg.Version,
		Provides: teamspkg.Provides{
			Teams:  []string{cfg.TeamID},
			Agents: report.Agents,
			Skills: report.Skills,
			Rules:  sortedRuleNames(closure.Rules),
		},
	}
	manifestData, err := yaml.Marshal(manifest)
	if err != nil {
		return fmt.Errorf("teams export: encode manifest: %w", err)
	}
	files[teamManifestFile] = manifestData

	if err := scanForSecrets(files); err != nil {
		return err
	}

	report.Name = name
	report.Version = cfg.Version

	target := cfg.OutDir
	if cfg.Format == formatTarGz {
		if !strings.HasSuffix(target, ".tar.gz") {
			target = filepath.Join(target, name+".tar.gz")
		}
		if err := writePackageTarball(target, name, files); err != nil {
			return err
		}
	} else {
		target = filepath.Join(target, name)
		if err := writePackageDir(target, files); err != nil {
			return err
		}
	}
	report.Output = target

	writeExportReport(cfg.Stdout, report)
	return nil
}

// collectExportFiles maps every file that belongs in the package to its
// content, keyed by package-relative path. Assets are copied whole (skills
// include their scripts) because a partially copied skill cannot run.
func collectExportFiles(agentsRoot, teamID string, closure *teamspkg.Tree) (map[string][]byte, exportReport, error) {
	files := map[string][]byte{}
	var report exportReport

	addTree := func(absDir, relDir string) error {
		return filepath.WalkDir(absDir, func(p string, d os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			rel, relErr := filepath.Rel(absDir, p)
			if relErr != nil {
				return relErr
			}
			if d.IsDir() {
				// Knowledge never ships, wherever it sits (design 9.2).
				if p != absDir && d.Name() == "knowledge" {
					report.Knowledge++
					return filepath.SkipDir
				}
				return nil
			}
			data, readErr := os.ReadFile(p)
			if readErr != nil {
				return readErr
			}
			files[path.Join(relDir, filepath.ToSlash(rel))] = data
			return nil
		})
	}

	report.Teams = []string{teamID}
	teamFile, err := teamDefinitionFile(agentsRoot, teamID)
	if err != nil {
		return nil, report, err
	}
	teamData, err := os.ReadFile(filepath.Join(agentsRoot, "teams", teamFile))
	if err != nil {
		return nil, report, fmt.Errorf("teams export: read team %q: %w", teamID, err)
	}
	files[path.Join("teams", teamFile)] = teamData

	report.Agents = make([]string, 0, len(closure.Agents))
	for agentID := range closure.Agents {
		report.Agents = append(report.Agents, agentID)
	}
	sort.Strings(report.Agents)
	for _, agentID := range report.Agents {
		agentFile, fileErr := agentDefinitionFile(agentsRoot, agentID)
		if fileErr != nil {
			return nil, report, fileErr
		}
		data, readErr := os.ReadFile(filepath.Join(agentsRoot, "agents", agentFile))
		if readErr != nil {
			return nil, report, fmt.Errorf("teams export: read agent %q: %w", agentID, readErr)
		}
		files[path.Join("agents", agentID, "AGENT.md")] = data

		// Agent-private rules live next to the definition and travel with it;
		// private knowledge next to it does not (the walk skips it).
		privateRules := filepath.Join(agentsRoot, "agents", agentID, "rules")
		if _, statErr := os.Stat(privateRules); statErr == nil {
			if walkErr := addTree(privateRules, path.Join("agents", agentID, "rules")); walkErr != nil {
				return nil, report, fmt.Errorf("teams export: read rules of agent %q: %w", agentID, walkErr)
			}
		}
	}
	// Global knowledge is excluded by never walking it; it is still counted so
	// the report can tell the user what was left behind.
	if _, statErr := os.Stat(filepath.Join(agentsRoot, "knowledge")); statErr == nil {
		report.Knowledge++
	}

	report.Skills = make([]string, 0, len(closure.Skills))
	for skillName := range closure.Skills {
		report.Skills = append(report.Skills, skillName)
	}
	sort.Strings(report.Skills)
	for _, skillName := range report.Skills {
		absDir := filepath.Join(agentsRoot, "skills", skillName)
		if err := addTree(absDir, path.Join("skills", skillName)); err != nil {
			return nil, report, fmt.Errorf("teams export: read skill %q: %w", skillName, err)
		}
	}

	for _, ruleName := range closure.Rules {
		ruleFile, fileErr := ruleDefinitionFile(agentsRoot, ruleName)
		if fileErr != nil {
			return nil, report, fileErr
		}
		data, readErr := os.ReadFile(filepath.Join(agentsRoot, "rules", ruleFile))
		if readErr != nil {
			return nil, report, fmt.Errorf("teams export: read rule %q: %w", ruleName, readErr)
		}
		files[path.Join("rules", ruleFile)] = data
	}
	report.RuleCount = len(closure.Rules)

	if _, statErr := os.Stat(filepath.Join(agentsRoot, "models.json")); statErr == nil {
		report.ModelsFile = true
	}
	return files, report, nil
}

// teamDefinitionFile finds teams/<id>.yml regardless of extension, because the
// loader accepts both and the package must keep the author spelling.
func teamDefinitionFile(agentsRoot, teamID string) (string, error) {
	for _, ext := range []string{".yml", ".yaml"} {
		if _, err := os.Stat(filepath.Join(agentsRoot, "teams", teamID+ext)); err == nil {
			return teamID + ext, nil
		}
	}
	return "", fmt.Errorf("teams export: team %q has no definition in %s", teamID, filepath.Join(agentsRoot, "teams"))
}

func agentDefinitionFile(agentsRoot, agentID string) (string, error) {
	if _, err := os.Stat(filepath.Join(agentsRoot, "agents", agentID, "AGENT.md")); err == nil {
		return filepath.Join(agentID, "AGENT.md"), nil
	}
	// Legacy flat layout; Collect accepts it, so exports must too.
	if _, err := os.Stat(filepath.Join(agentsRoot, "agents", agentID+".md")); err == nil {
		return agentID + ".md", nil
	}
	return "", fmt.Errorf("teams export: agent %q has no definition in %s", agentID, filepath.Join(agentsRoot, "agents"))
}

func ruleDefinitionFile(agentsRoot, ruleID string) (string, error) {
	entries, err := os.ReadDir(filepath.Join(agentsRoot, "rules"))
	if err != nil {
		return "", fmt.Errorf("teams export: read rules: %w", err)
	}
	for _, e := range entries {
		if e.IsDir() || !strings.EqualFold(filepath.Ext(e.Name()), ".md") {
			continue
		}
		if strings.TrimSuffix(e.Name(), filepath.Ext(e.Name())) == ruleID {
			return e.Name(), nil
		}
	}
	return "", fmt.Errorf("teams export: rule %q has no definition in %s", ruleID, filepath.Join(agentsRoot, "rules"))
}

// secretPatterns are the shapes an accidentally committed credential takes.
// They match file text rather than parsed config, because keys also hide in
// prose and in skill scripts.
var secretPatterns = []*regexp.Regexp{
	regexp.MustCompile(`api_key\s*[:=]`),
	regexp.MustCompile(`sk-[A-Za-z0-9]{8,}`),
	regexp.MustCompile(`Bearer\s+[A-Za-z0-9]`),
	regexp.MustCompile(`ck_[A-Za-z0-9]`),
}

var scannableExt = map[string]bool{
	".md": true, ".yml": true, ".yaml": true, ".sh": true, ".py": true, ".json": true,
}

// scanForSecrets fails the whole export and lists every offending location,
// since the author has to fix all of them rather than the first one seen.
func scanForSecrets(files map[string][]byte) error {
	paths := make([]string, 0, len(files))
	for p := range files {
		paths = append(paths, p)
	}
	sort.Strings(paths)

	var hits []string
	for _, p := range paths {
		if !scannableExt[strings.ToLower(path.Ext(p))] {
			continue
		}
		for i, line := range strings.Split(string(files[p]), "\n") {
			for _, re := range secretPatterns {
				loc := re.FindStringIndex(line)
				if loc == nil {
					continue
				}
				hits = append(hits, fmt.Sprintf("  %s:%d: %s", p, i+1, redact(line[loc[0]:loc[1]])))
				break
			}
		}
	}
	if len(hits) == 0 {
		return nil
	}
	// The snippet is truncated: printing the full match would write the key it
	// is meant to protect into logs and terminals.
	return fmt.Errorf("teams export: refusing to export, possible secret(s) found:\n%s", strings.Join(hits, "\n"))
}

func redact(s string) string {
	s = strings.TrimSpace(s)
	runes := []rune(s)
	if len(runes) <= 8 {
		return s
	}
	return string(runes[:8]) + "..."
}

// writePackageTarball builds the archive in memory first: a failed export must
// not leave a truncated archive the user might later share.
func writePackageTarball(target, topDir string, files map[string][]byte) error {
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return fmt.Errorf("teams export: %w", err)
	}
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)

	paths := make([]string, 0, len(files))
	for p := range files {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	for _, p := range paths {
		data := files[p]
		hdr := &tar.Header{Name: path.Join(topDir, p), Mode: 0o644, Size: int64(len(data))}
		// Scripts must stay executable after extraction, otherwise an
		// installed skill silently fails to run its helpers.
		if isScriptPath(p) {
			hdr.Mode = 0o755
		}
		if err := tw.WriteHeader(hdr); err != nil {
			return fmt.Errorf("teams export: write tar header %s: %w", p, err)
		}
		if _, err := tw.Write(data); err != nil {
			return fmt.Errorf("teams export: write %s: %w", p, err)
		}
	}
	if err := tw.Close(); err != nil {
		return fmt.Errorf("teams export: %w", err)
	}
	if err := gz.Close(); err != nil {
		return fmt.Errorf("teams export: %w", err)
	}
	if err := os.WriteFile(target, buf.Bytes(), 0o644); err != nil {
		return fmt.Errorf("teams export: write %s: %w", target, err)
	}
	return nil
}

func isScriptPath(p string) bool {
	return strings.HasPrefix(p, "skills/") && strings.Contains(p, "/scripts/")
}

func writePackageDir(target string, files map[string][]byte) error {
	// A previous export would otherwise leak removed files into the new one.
	if err := os.RemoveAll(target); err != nil {
		return fmt.Errorf("teams export: %w", err)
	}
	for p, data := range files {
		full := filepath.Join(target, filepath.FromSlash(p))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			return fmt.Errorf("teams export: %w", err)
		}
		mode := os.FileMode(0o644)
		if isScriptPath(p) {
			mode = 0o755
		}
		if err := os.WriteFile(full, data, mode); err != nil {
			return fmt.Errorf("teams export: write %s: %w", full, err)
		}
	}
	return nil
}

// sanitizeTeamName turns a team id into a valid manifest name: the name becomes
// a directory under _teams/, so anything a filesystem could reinterpret is out.
func sanitizeTeamName(id string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(id) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
			continue
		}
		b.WriteByte('-')
	}
	name := strings.Trim(b.String(), "-")
	// Collapse runs so "review__team" and "review-team" do not produce
	// different names for the same intent.
	for strings.Contains(name, "--") {
		name = strings.ReplaceAll(name, "--", "-")
	}
	if name == "" {
		return "team"
	}
	if name[0] < 'a' || name[0] > 'z' {
		name = "team-" + name
	}
	return name
}

func sortedRuleNames(rules []string) []string {
	out := append([]string(nil), rules...)
	sort.Strings(out)
	return out
}

func writeExportReport(w io.Writer, r exportReport) {
	_, _ = fmt.Fprintf(w, "Exported team package: %s %s\n", r.Name, r.Version)
	_, _ = fmt.Fprintf(w, "  output:  %s\n", displayPath(r.Output))
	_, _ = fmt.Fprintf(w, "  teams:   %d%s\n", len(r.Teams), formatNames(r.Teams))
	_, _ = fmt.Fprintf(w, "  agents:  %d%s\n", len(r.Agents), formatNames(r.Agents))
	_, _ = fmt.Fprintf(w, "  skills:  %d%s\n", len(r.Skills), formatNames(r.Skills))
	_, _ = fmt.Fprintf(w, "  rules:   %d\n", r.RuleCount)

	var excluded []string
	if r.Knowledge > 0 {
		excluded = append(excluded, plural(r.Knowledge, "knowledge directory", "knowledge directories"))
	}
	if r.ModelsFile {
		excluded = append(excluded, "1 models.json")
	}
	if len(excluded) > 0 {
		_, _ = fmt.Fprintf(w, "  excluded: %s\n", strings.Join(excluded, ", "))
	}
}

func plural(n int, one, many string) string {
	if n == 1 {
		return fmt.Sprintf("%d %s", n, one)
	}
	return fmt.Sprintf("%d %s", n, many)
}

func displayPath(p string) string {
	rel, err := filepath.Rel(".", p)
	if err != nil || strings.HasPrefix(rel, "..") {
		return p
	}
	return "./" + rel
}

// runTeamsExport parses export flags. Positional and flag arguments may be
// interleaved for the same reason as teams add.
func runTeamsExport(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("teams export", flag.ContinueOnError)
	fs.SetOutput(stderr)
	out := fs.String("o", ".", "Output directory, or a full path ending in .tar.gz")
	format := fs.String("format", formatTarGz, "Package format: tar.gz|dir")
	version := fs.String("version", defaultTeamVersion, "Package version written to heron-team.yml")
	fs.Usage = func() { teamsUsage(stderr) }

	positional, flags := []string{}, []string{}
	valueFlags := map[string]bool{
		"-o": true, "--o": true,
		"-format": true, "--format": true,
		"-version": true, "--version": true,
	}
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--" {
			positional = append(positional, args[i+1:]...)
			break
		}
		if !strings.HasPrefix(a, "-") || a == "-" {
			positional = append(positional, a)
			continue
		}
		if valueFlags[a] && i+1 < len(args) {
			flags = append(flags, a, args[i+1])
			i++
			continue
		}
		flags = append(flags, a)
	}
	if err := fs.Parse(flags); err != nil {
		return err
	}
	if len(positional) == 0 {
		return errors.New("teams export: missing <team>")
	}
	if len(positional) > 1 {
		return fmt.Errorf("teams export: expected exactly one <team>, got %d", len(positional))
	}
	return exportTeam(exportConfig{
		TeamID:  positional[0],
		OutDir:  *out,
		Format:  *format,
		Version: *version,
		Root:    ".",
		Stdout:  stdout,
	})
}
