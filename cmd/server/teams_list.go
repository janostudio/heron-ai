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

// teamRow is one line of the teams listing. It is deliberately plain data:
// the only thing the list command knows about a team is where it came from,
// which is exactly what the user needs to tell a native team from an
// installed package.
type teamRow struct {
	Name    string
	Source  string
	Version string
	Status  string
}

// listTeams scans the two roots a team can live in and writes an aligned table.
//
// It must never require a Flow config: listing is a metadata command, so it
// also has to answer in a directory that has no .agents/ at all. Both roots
// are therefore treated as optional and a missing root is not an error.
func listTeams(root string, out io.Writer) error {
	rows, err := collectTeams(root)
	if err != nil {
		return err
	}
	if len(rows) == 0 {
		_, err := fmt.Fprintln(out, "no teams")
		return err
	}
	writeTeamTable(out, rows)
	return nil
}

func collectTeams(root string) ([]teamRow, error) {
	agentsDir := filepath.Join(root, ".agents")

	native, err := collectNativeTeams(filepath.Join(agentsDir, "teams"))
	if err != nil {
		return nil, err
	}
	// A malformed ledger must not hide every installed package: the index is
	// only an annotation on top of the directories, so a read failure degrades
	// to manifest-only rows instead of failing the listing.
	index, _ := readTeamsIndex(root)
	if index == nil {
		index = &teamsIndex{Installed: map[string]installedTeam{}}
	}
	installed, err := collectInstalledTeams(filepath.Join(agentsDir, "_teams"), index)
	if err != nil {
		return nil, err
	}

	rows := append(native, installed...)
	sort.SliceStable(rows, func(i, j int) bool { return rows[i].Name < rows[j].Name })
	return rows, nil
}

// collectNativeTeams lists .agents/teams/*.yml. The file name (minus extension)
// is the team id the loader uses, so it is the name shown to the user.
func collectNativeTeams(dir string) ([]teamRow, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	var rows []teamRow
	for _, e := range entries {
		if e.IsDir() || !isYAMLFile(e.Name()) {
			continue
		}
		rows = append(rows, teamRow{
			Name:    strings.TrimSuffix(e.Name(), filepath.Ext(e.Name())),
			Source:  "local",
			Version: "-",
			// Model availability is a later phase; nothing here can fail yet.
			Status: "ok",
		})
	}
	return rows, nil
}

// collectInstalledTeams lists .agents/_teams/<scope>/ packages. A directory only
// counts as installed when it carries the manifest, because a half-copied or
// half-deleted directory cannot be described (no source, no version).
func collectInstalledTeams(dir string, index *teamsIndex) ([]teamRow, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	var rows []teamRow
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		scope := e.Name()
		entry := index.Installed[scope]
		data, err := os.ReadFile(filepath.Join(dir, scope, teamManifestFile))
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				// A remove --keep-agents leaves a directory with no manifest but
				// real Agents inside. It is still installed state worth showing,
				// and it is the only way to find it again to delete it.
				if entry.Partial || entry.KeptAgents {
					rows = append(rows, teamRow{
						Name:    "@" + scope,
						Source:  packageSource(entry, ""),
						Version: orDash(entry.Version),
						Status:  "partial",
					})
				}
				continue
			}
			return nil, err
		}
		var m teamManifest
		if err := yaml.Unmarshal(data, &m); err != nil {
			// A broken manifest still belongs in the listing: hiding it would
			// leave the user unable to find the package they need to remove.
			rows = append(rows, teamRow{Name: "@" + scope, Source: packageSource(entry, ""), Version: "-", Status: "invalid manifest"})
			continue
		}
		status := "ok"
		if entry.Partial {
			status = "partial"
		}
		rows = append(rows, teamRow{
			Name:    "@" + scope,
			Source:  packageSource(entry, m.Source),
			Version: orDash(m.Version),
			Status:  status,
		})
	}
	return rows, nil
}

// packageSource prefers the ledger's record over the manifest's.
//
// The manifest source names where the author published the package; the ledger
// records where this user actually installed it from, which is a URL when the
// package was fetched remotely and is what someone would re-install from.
func packageSource(entry installedTeam, manifestSource string) string {
	if s := strings.TrimSpace(entry.Source); s != "" {
		return s
	}
	if s := strings.TrimSpace(entry.Remote); s != "" {
		return s
	}
	return orDash(manifestSource)
}

func writeTeamTable(out io.Writer, rows []teamRow) {
	header := []string{"TEAM", "SOURCE", "VERSION", "STATUS"}
	widths := []int{len(header[0]), len(header[1]), len(header[2])}
	// STATUS is last on the line, so it needs no padding.
	for _, r := range rows {
		if n := len(r.Name); n > widths[0] {
			widths[0] = n
		}
		if n := len(r.Source); n > widths[1] {
			widths[1] = n
		}
		if n := len(r.Version); n > widths[2] {
			widths[2] = n
		}
	}
	_, _ = fmt.Fprintf(out, "%-*s  %-*s  %-*s  %s\n", widths[0], header[0], widths[1], header[1], widths[2], header[2], header[3])
	for _, r := range rows {
		_, _ = fmt.Fprintf(out, "%-*s  %-*s  %-*s  %s\n", widths[0], r.Name, widths[1], r.Source, widths[2], r.Version, r.Status)
	}
}

func isYAMLFile(name string) bool {
	switch strings.ToLower(filepath.Ext(name)) {
	case ".yml", ".yaml":
		return true
	}
	return false
}

func orDash(s string) string {
	if strings.TrimSpace(s) == "" {
		return "-"
	}
	return s
}
