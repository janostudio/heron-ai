package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/heron-ai/heron-engine/internal/teamspkg"
)

// removeResult is what the command reports. Deleted is the package directory,
// not the scope root, because that is what the user must look at to verify the
// removal.
type removeResult struct {
	Scope     string
	Kept      bool
	Deleted   string
	Preserved []string
}

// removeTeamPackage deletes an installed package.
//
// Removal is keyed by scope, not by manifest name: scope is what the Flow
// references and what the directories are named after, and one package can be
// installed under several scopes via --as.
func removeTeamPackage(root, name string, keepAgents bool, out io.Writer) (*removeResult, error) {
	scope := strings.TrimPrefix(strings.TrimSpace(name), "@")
	if scope == "" {
		return nil, errors.New("teams remove: missing <name>")
	}
	// Validate before touching the disk: the scope becomes a path component,
	// so anything else (a slash, a dot-dot) would delete outside _teams.
	if !teamspkg.ValidName(scope) {
		return nil, fmt.Errorf("invalid team package name %q: must start with a letter or digit and contain only [a-z0-9-]", name)
	}

	targetDir := filepath.Join(root, ".agents", "_teams", scope)
	if fi, err := os.Stat(targetDir); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("no installed team package @%s", scope)
		}
		return nil, err
	} else if !fi.IsDir() {
		return nil, fmt.Errorf("no installed team package @%s", scope)
	}

	res := &removeResult{
		Scope:   scope,
		Kept:    keepAgents,
		Deleted: targetDir,
	}

	if !keepAgents {
		if err := os.RemoveAll(targetDir); err != nil {
			return nil, fmt.Errorf("remove %s: %w", targetDir, err)
		}
		if err := dropTeamEntry(root, scope); err != nil {
			return nil, err
		}
		writeRemoveSummary(out, res, root)
		return res, nil
	}

	// --keep-agents deletes only what makes the directory a package: the Team
	// definitions and the manifest. Without them nothing binds the Agents into
	// a callable unit, but the Agents, Skills and Rules stay for the user's own
	// Teams to reference.
	for _, rel := range []string{"teams", teamManifestFile} {
		victim := filepath.Join(targetDir, rel)
		if _, err := os.Stat(victim); errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err := os.RemoveAll(victim); err != nil {
			return nil, fmt.Errorf("remove %s: %w", victim, err)
		}
	}
	for _, rel := range []string{"agents", "skills", "rules"} {
		if _, err := os.Stat(filepath.Join(targetDir, rel)); err == nil {
			res.Preserved = append(res.Preserved, rel)
		}
	}
	if err := markTeamEntryKept(root, scope); err != nil {
		return nil, err
	}
	writeRemoveSummary(out, res, root)
	return res, nil
}

// markTeamEntryKept turns the ledger entry into a partial one.
//
// The entry is kept rather than dropped because the surviving Agents are still
// visible to `heron teams`, and a package missing from the ledger while its
// directory exists would look like a broken install on the next add.
func markTeamEntryKept(root, scope string) error {
	idx, err := readTeamsIndex(root)
	if err != nil {
		return err
	}
	entry, ok := idx.Installed[scope]
	if !ok {
		// Installed without an index entry (hand-copied directory): there is
		// nothing to mark, and inventing an entry would claim a provenance
		// nobody recorded.
		return nil
	}
	entry.KeptAgents = true
	entry.Partial = true
	entry.InstalledAt = time.Now().UTC().Format(time.RFC3339)
	idx.Installed[scope] = entry
	return writeTeamsIndex(root, idx)
}

func writeRemoveSummary(out io.Writer, res *removeResult, root string) {
	rel := slashedRelPath(root, res.Deleted)
	if !res.Kept {
		_, _ = fmt.Fprintf(out, "Removed team package: @%s\n", res.Scope)
		_, _ = fmt.Fprintf(out, "  deleted: %s\n", rel)
		return
	}
	_, _ = fmt.Fprintf(out, "Removed team package: @%s (kept agents)\n", res.Scope)
	_, _ = fmt.Fprintf(out, "  deleted: %s (teams/, %s)\n", rel, teamManifestFile)
	if len(res.Preserved) > 0 {
		_, _ = fmt.Fprintf(out, "  kept: %s\n", strings.Join(res.Preserved, ", "))
	}
}

// slashedRelPath renders a path relative to root with a trailing slash, so the
// summary reads the same in tests (temp dirs) and in real use (cwd).
func slashedRelPath(root, path string) string {
	rel, err := filepath.Rel(root, path)
	if err != nil {
		rel = path
	}
	return filepath.ToSlash(rel) + "/"
}

// runTeamsRemove is the CLI entry point for the remove subcommand.
func runTeamsRemove(root string, args []string, stdout io.Writer) error {
	var name string
	keepAgents := false
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--keep-agents":
			keepAgents = true
		case a == "--":
			if i+1 < len(args) {
				name = args[i+1]
			}
		case strings.HasPrefix(a, "-") && a != "-":
			return fmt.Errorf("teams remove: unknown flag %s", a)
		default:
			if name != "" {
				return fmt.Errorf("teams remove: expected exactly one <name>, got %q and %q", name, a)
			}
			name = a
		}
	}
	if name == "" {
		return errors.New("teams remove: missing <name>")
	}
	_, err := removeTeamPackage(root, name, keepAgents, stdout)
	return err
}
