package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

const teamManifestFile = "heron-team.yml"

// teamManifest holds only the fields the CLI itself needs. The rest of the
// manifest (provides/requires/sandbox) describes the install graph, which the
// package layer owns; duplicating that schema here would let the two drift.
type teamManifest struct {
	Name        string `yaml:"name"`
	Version     string `yaml:"version"`
	Description string `yaml:"description"`
	Source      string `yaml:"source"`
}

// teamPackage is the dry-run view of a package directory: the manifest plus the
// asset names found on disk.
type teamPackage struct {
	Dir         string
	Manifest    teamManifest
	Teams       []string
	Agents      []string
	Skills      []string
	RuleCount   int
	ScriptCount int
}

// loadTeamPackage reads a local package directory. Assets are counted from the
// directory instead of trusting the manifest provides list: dry-run exists to
// show what would actually be installed.
func loadTeamPackage(dir string) (*teamPackage, error) {
	fi, err := os.Stat(dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("source directory not found: %s", dir)
		}
		return nil, err
	}
	if !fi.IsDir() {
		return nil, fmt.Errorf("source must be a local directory: %s", dir)
	}

	manifestPath := filepath.Join(dir, teamManifestFile)
	data, err := os.ReadFile(manifestPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("missing %s in %s", teamManifestFile, dir)
		}
		return nil, err
	}
	var m teamManifest
	if err := yaml.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("parse %s: %w", manifestPath, err)
	}
	if strings.TrimSpace(m.Name) == "" {
		return nil, fmt.Errorf("%s is missing required field: name", manifestPath)
	}

	pkg := &teamPackage{Dir: dir, Manifest: m}
	if pkg.Teams, err = listYAMLStems(filepath.Join(dir, "teams")); err != nil {
		return nil, err
	}
	if pkg.Agents, err = listAssetDirs(filepath.Join(dir, "agents"), "AGENT.md"); err != nil {
		return nil, err
	}
	if pkg.Skills, err = listAssetDirs(filepath.Join(dir, "skills"), "SKILL.md"); err != nil {
		return nil, err
	}
	if pkg.RuleCount, err = countFiles(filepath.Join(dir, "rules"), ".md"); err != nil {
		return nil, err
	}
	if pkg.ScriptCount, err = countSkillScripts(filepath.Join(dir, "skills")); err != nil {
		return nil, err
	}
	return pkg, nil
}

// listYAMLStems returns *.yml/*.yaml file names without their extension.
func listYAMLStems(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	var names []string
	for _, e := range entries {
		if e.IsDir() || !isYAMLFile(e.Name()) {
			continue
		}
		names = append(names, strings.TrimSuffix(e.Name(), filepath.Ext(e.Name())))
	}
	sort.Strings(names)
	return names, nil
}

// listAssetDirs returns the immediate subdirectories of dir that contain marker.
func listAssetDirs(dir, marker string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	var names []string
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		if _, err := os.Stat(filepath.Join(dir, e.Name(), marker)); err != nil {
			continue
		}
		names = append(names, e.Name())
	}
	sort.Strings(names)
	return names, nil
}

func countFiles(dir, ext string) (int, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return 0, nil
		}
		return 0, err
	}
	n := 0
	for _, e := range entries {
		if !e.IsDir() && strings.EqualFold(filepath.Ext(e.Name()), ext) {
			n++
		}
	}
	return n, nil
}

// countSkillScripts counts files under skills/<name>/scripts/ at any depth.
func countSkillScripts(dir string) (int, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return 0, nil
		}
		return 0, err
	}
	n := 0
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		err := filepath.WalkDir(filepath.Join(dir, e.Name(), "scripts"), func(_ string, d os.DirEntry, err error) error {
			if err != nil {
				if errors.Is(err, os.ErrNotExist) {
					return nil
				}
				return err
			}
			if !d.IsDir() {
				n++
			}
			return nil
		})
		if err != nil {
			return 0, err
		}
	}
	return n, nil
}

// runTeamsAddDryRun keeps the older, directory-only preview. It is retained as
// the entry point the dispatcher already calls; installTeamPackage handles the
// richer preview used by the install path.
func runTeamsAddDryRun(source string, out io.Writer) error {
	pkg, err := loadTeamPackage(source)
	if err != nil {
		return err
	}
	writePackagePreview(out, pkg)
	return nil
}

// teamsAddInputs is the argument bundle teams_cli.go hands to the install path,
// so the dispatcher does not need to know about installOptions.
type teamsAddInputs struct {
	Source  string
	DryRun  bool
	Yes     bool
	Scope   string
	Refresh bool
	Stdout  io.Writer
	Stderr  io.Writer
	Stdin   io.Reader
}

// runTeamsAddFromInputs is the single entry point for `teams add`. The CLI
// branch only needs to build teamsAddInputs and call this.
//
// Dry run goes through the same pipeline as a real install so the preview
// reports the actual trim list and script policy instead of a parallel,
// approximate scan; installTeamPackage is what guarantees nothing is written.
func runTeamsAddFromInputs(root string, in teamsAddInputs) error {
	opts := installOptions{DryRun: in.DryRun, Yes: in.Yes, Scope: in.Scope, Refresh: in.Refresh}

	// The summary needs the manifest, and the install path already loads it, so
	// it travels back through the result rather than being read twice.
	res, err := installTeamPackage(root, in.Source, opts, in.Stdout, in.Stderr, in.Stdin)
	if err != nil {
		return err
	}
	if in.DryRun {
		return nil
	}
	writeInstallSummary(in.Stdout, res.Manifest, res)
	return nil
}

func writePackagePreview(out io.Writer, pkg *teamPackage) {
	m := pkg.Manifest
	title := m.Name
	if v := strings.TrimSpace(m.Version); v != "" {
		title += " " + v
	}
	fmt.Fprintf(out, "Applying team package: %s\n", title)
	if d := strings.TrimSpace(m.Description); d != "" {
		fmt.Fprintf(out, "  description: %s\n", d)
	}
	fmt.Fprintf(out, "  teams:    %d%s\n", len(pkg.Teams), formatNames(pkg.Teams))
	fmt.Fprintf(out, "  agents:   %d%s\n", len(pkg.Agents), formatNames(pkg.Agents))
	fmt.Fprintf(out, "  skills:   %d%s\n", len(pkg.Skills), formatNames(pkg.Skills))
	fmt.Fprintf(out, "  rules:    %d\n", pkg.RuleCount)
	if pkg.ScriptCount > 0 {
		// Scripts are the one thing a user must judge before a real install, so
		// dry-run surfaces the count even though it cannot prompt.
		fmt.Fprintf(out, "  scripts:  %d (would be reviewed before install)\n", pkg.ScriptCount)
	}
	fmt.Fprintf(out, "Target: .agents/_teams/%s/\n", m.Name)
	fmt.Fprintln(out, "Dry run: nothing was written.")
}

func formatNames(names []string) string {
	if len(names) == 0 {
		return ""
	}
	return " (" + strings.Join(names, ", ") + ")"
}
