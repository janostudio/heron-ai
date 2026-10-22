package main

import (
	"io"
	"strings"
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

func formatNames(names []string) string {
	if len(names) == 0 {
		return ""
	}
	return " (" + strings.Join(names, ", ") + ")"
}
