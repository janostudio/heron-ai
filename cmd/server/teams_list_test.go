package main

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"
)

func TestListTeamsPrefersIndexSource(t *testing.T) {
	root := t.TempDir()
	mustWrite(t, filepath.Join(root, ".agents", "_teams", "review-team", teamManifestFile), "version: 1.2.0\nsource: acme/review-team\n")
	// The manifest names the author's repo; the ledger records where this user
	// installed from, which is what a re-install would use.
	if err := updateTeamsIndex(root, "review-team", installedTeam{
		Version: "1.2.0",
		Source:  "https://files.example.com/review.tar.gz",
	}); err != nil {
		t.Fatalf("index: %v", err)
	}

	var out bytes.Buffer
	if err := listTeams(root, &out); err != nil {
		t.Fatalf("list: %v", err)
	}
	row := tableRow(t, out.String(), 1)
	if !strings.Contains(row, "https://files.example.com/review.tar.gz") {
		t.Fatalf("expected the index source, got %q", row)
	}
	if strings.Contains(row, "acme/review-team") {
		t.Fatalf("manifest source should have lost to the index: %q", row)
	}
}

func TestListTeamsFallsBackToManifestSource(t *testing.T) {
	root := t.TempDir()
	mustWrite(t, filepath.Join(root, ".agents", "_teams", "review-team", teamManifestFile), "version: 1.2.0\nsource: acme/review-team\n")

	var out bytes.Buffer
	if err := listTeams(root, &out); err != nil {
		t.Fatalf("list: %v", err)
	}
	if row := tableRow(t, out.String(), 1); !strings.Contains(row, "acme/review-team") {
		t.Fatalf("expected manifest source, got %q", row)
	}
}

func TestListTeamsShowsDashWithoutAnySource(t *testing.T) {
	root := t.TempDir()
	mustWrite(t, filepath.Join(root, ".agents", "_teams", "review-team", teamManifestFile), "version: 1.2.0\n")

	var out bytes.Buffer
	if err := listTeams(root, &out); err != nil {
		t.Fatalf("list: %v", err)
	}
	if row := tableRow(t, out.String(), 1); !strings.Contains(row, "  -  ") {
		t.Fatalf("expected a dash source, got %q", row)
	}
}

func TestListTeamsMarksPartialFromIndex(t *testing.T) {
	root := t.TempDir()
	mustWrite(t, filepath.Join(root, ".agents", "_teams", "review-team", teamManifestFile), "version: 1.2.0\nsource: x\n")
	if err := updateTeamsIndex(root, "review-team", installedTeam{Version: "1.2.0", Source: "x", Partial: true, KeptAgents: true}); err != nil {
		t.Fatalf("index: %v", err)
	}

	var out bytes.Buffer
	if err := listTeams(root, &out); err != nil {
		t.Fatalf("list: %v", err)
	}
	row := tableRow(t, out.String(), 1)
	if !strings.HasSuffix(row, "partial") {
		t.Fatalf("expected partial status, got %q", row)
	}
}

func TestListTeamsSurvivesBrokenIndex(t *testing.T) {
	root := t.TempDir()
	mustWrite(t, filepath.Join(root, ".agents", teamsIndexFile), "{not json")
	mustWrite(t, filepath.Join(root, ".agents", "_teams", "review-team", teamManifestFile), "version: 1.2.0\nsource: acme\n")

	var out bytes.Buffer
	if err := listTeams(root, &out); err != nil {
		t.Fatalf("a broken ledger must not fail the listing: %v", err)
	}
	if row := tableRow(t, out.String(), 1); !strings.Contains(row, "acme") {
		t.Fatalf("expected the manifest row, got %q", row)
	}
}

// tableRow returns the nth data row (1-based, header excluded).
func tableRow(t *testing.T, output string, n int) string {
	t.Helper()
	lines := strings.Split(strings.TrimRight(output, "\n"), "\n")
	if len(lines) <= n {
		t.Fatalf("expected at least %d rows, got %q", n, output)
	}
	return lines[n]
}
