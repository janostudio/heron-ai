package teamspkg

import (
	"strings"
	"testing"
)

const validManifest = `
apiVersion: heron/v1
kind: team
name: review-team
version: 1.2.0
provides:
  teams: [review_team]
sandbox:
  tools: [Read, Grep]
`

func TestLoadManifestValid(t *testing.T) {
	m, err := LoadManifest([]byte(validManifest))
	if err != nil {
		t.Fatalf("LoadManifest: %v", err)
	}
	if m.Name != "review-team" || m.Version != "1.2.0" {
		t.Fatalf("unexpected manifest: %+v", m)
	}
	if len(m.Provides.Teams) != 1 || m.Provides.Teams[0] != "review_team" {
		t.Fatalf("provides.teams = %v", m.Provides.Teams)
	}
	if m.Sandbox.Scripts != ScriptsReview {
		t.Fatalf("sandbox.scripts default = %q, want review", m.Sandbox.Scripts)
	}
}

func TestLoadManifestExplicitScripts(t *testing.T) {
	m, err := LoadManifest([]byte(strings.Replace(validManifest, "tools: [Read, Grep]", "scripts: deny", 1)))
	if err != nil {
		t.Fatalf("LoadManifest: %v", err)
	}
	if m.Sandbox.Scripts != ScriptsDeny {
		t.Fatalf("sandbox.scripts = %q, want deny", m.Sandbox.Scripts)
	}
}

func TestLoadManifestInvalid(t *testing.T) {
	tests := []struct {
		name string
		data string
		want string
	}{
		{
			name: "wrong apiVersion",
			data: strings.Replace(validManifest, "apiVersion: heron/v1", "apiVersion: heron/v2", 1),
			want: "unsupported apiVersion",
		},
		{
			name: "wrong kind",
			data: strings.Replace(validManifest, "kind: team", "kind: agent", 1),
			want: "unsupported kind",
		},
		{
			name: "missing name",
			data: strings.Replace(validManifest, "name: review-team", "name: \"\"", 1),
			want: "name is required",
		},
		{
			name: "bad name",
			data: strings.Replace(validManifest, "name: review-team", "name: Review_Team", 1),
			want: "must match",
		},
		{
			name: "missing version",
			data: strings.Replace(validManifest, "version: 1.2.0\n", "", 1),
			want: "version is required",
		},
		{
			name: "bad scripts",
			data: strings.Replace(validManifest, "tools: [Read, Grep]", "scripts: sometimes", 1),
			want: "sandbox.scripts",
		},
		{
			name: "not yaml",
			data: "apiVersion: [",
			want: "parse manifest",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := LoadManifest([]byte(tc.data))
			if err == nil {
				t.Fatalf("expected error containing %q, got nil", tc.want)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error %q does not contain %q", err, tc.want)
			}
		})
	}
}
