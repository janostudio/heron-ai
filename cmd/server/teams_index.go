package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// teamsIndexFile is the install ledger. It lives beside .agents/flows and is
// named so LoadDefinitions never sees it: the loader only scans *.yml, and this
// must stay out of the definition tree.
const teamsIndexFile = ".teams.json"

// teamsIndexVersion is bumped only when the on-disk shape changes in a way an
// older binary must not silently misread.
const teamsIndexVersion = 1

// installedTeam is one entry of the ledger. Files and Scripts carry content
// hashes so a later remove can detect a hand-edited package instead of deleting
// changes it knows nothing about.
type installedTeam struct {
	Version      string            `json:"version"`
	Source       string            `json:"source"`
	InstalledAt  string            `json:"installed_at"`
	ManifestHash string            `json:"manifest_hash,omitempty"`
	Files        map[string]string `json:"files,omitempty"`
	Scripts      map[string]string `json:"scripts,omitempty"`
	// KeptAgents records a remove --keep-agents. The package is no longer
	// complete, but its Agents are still on disk and still usable, so the entry
	// stays in the ledger and lists as partial instead of disappearing.
	KeptAgents bool `json:"kept_agents,omitempty"`
	Partial    bool `json:"partial,omitempty"`
	// Remote records the URL a package was fetched from, which is the only
	// place the full address survives: the manifest's source field is the
	// author's, not the address the user installed from.
	Remote string `json:"remote,omitempty"`
}

type teamsIndex struct {
	Version   int                      `json:"version"`
	Installed map[string]installedTeam `json:"installed"`
}

func teamsIndexPath(root string) string {
	return filepath.Join(root, ".agents", teamsIndexFile)
}

// readTeamsIndex loads the ledger, treating a missing file as empty.
//
// A malformed file is an error rather than a reset: overwriting it would erase
// the record of every other installed package, and the user can fix or delete
// the file themselves.
func readTeamsIndex(root string) (*teamsIndex, error) {
	path := teamsIndexPath(root)
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return &teamsIndex{Version: teamsIndexVersion, Installed: map[string]installedTeam{}}, nil
		}
		return nil, err
	}
	var idx teamsIndex
	if err := json.Unmarshal(data, &idx); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	if idx.Version > teamsIndexVersion {
		return nil, fmt.Errorf("%s was written by a newer heron (version %d)", path, idx.Version)
	}
	if idx.Installed == nil {
		idx.Installed = map[string]installedTeam{}
	}
	return &idx, nil
}

// updateTeamsIndex records one scope, preserving every other entry.
func updateTeamsIndex(root, scope string, entry installedTeam) error {
	idx, err := readTeamsIndex(root)
	if err != nil {
		return err
	}
	idx.Version = teamsIndexVersion
	idx.Installed[scope] = entry
	return writeTeamsIndex(root, idx)
}

// updateTeamEntry rewrites one existing scope in place through a read-modify
// function, so a remove only has to name the fields it changes.
func updateTeamEntry(root, scope string, edit func(*installedTeam)) error {
	idx, err := readTeamsIndex(root)
	if err != nil {
		return err
	}
	entry, ok := idx.Installed[scope]
	if !ok {
		return nil
	}
	edit(&entry)
	idx.Installed[scope] = entry
	return writeTeamsIndex(root, idx)
}

// dropTeamEntry removes one scope. A missing ledger is not an error: the user
// may have deleted .teams.json by hand, and the package directory is still
// removable, so remove must not fail on bookkeeping it can simply skip.
func dropTeamEntry(root, scope string) error {
	path := teamsIndexPath(root)
	if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
		return nil
	}
	idx, err := readTeamsIndex(root)
	if err != nil {
		return err
	}
	if _, ok := idx.Installed[scope]; !ok {
		return nil
	}
	delete(idx.Installed, scope)
	return writeTeamsIndex(root, idx)
}

func writeTeamsIndex(root string, idx *teamsIndex) error {
	idx.Version = teamsIndexVersion
	if idx.Installed == nil {
		idx.Installed = map[string]installedTeam{}
	}

	path := teamsIndexPath(root)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(idx, "", "  ")
	if err != nil {
		return err
	}
	// Write through a temp file so a crash cannot leave a truncated ledger that
	// makes every other package look uninstalled.
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, append(data, '\n'), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
