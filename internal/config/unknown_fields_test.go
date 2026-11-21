package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCheckSettingsFieldsAcceptsKnownKeys(t *testing.T) {
	data := []byte(`{
		"runtime": {"max_team_turns": 5},
		"logging": {"level": "debug"},
		"web": {"provider": "duckduckgo"},
		"tools": {"restricted_bash_phrases": ["rm -rf"]},
		"env": {"ANY_VARIABLE_NAME": "/tmp"},
		"mcp": [{"name": "srv", "transport": "stdio", "command": "srv", "args": ["--x"]}]
	}`)
	if got := CheckSettingsFields(data); len(got) != 0 {
		t.Fatalf("known keys reported as unknown: %v", got)
	}
}

func TestCheckSettingsFieldsSuggestsTheClosestKey(t *testing.T) {
	cases := []struct {
		name       string
		data       string
		wantPath   string
		wantSugges string
	}{
		{
			name:       "misspelled section",
			data:       `{"runtme": {"max_team_turns": 5}}`,
			wantPath:   "runtme",
			wantSugges: "runtime",
		},
		{
			name:       "misspelled field",
			data:       `{"runtime": {"max_team_turn": 5}}`,
			wantPath:   "runtime.max_team_turn",
			wantSugges: "max_team_turns",
		},
		{
			name:       "misspelled field inside an array",
			data:       `{"mcp": [{"name": "srv", "arg": ["--x"]}]}`,
			wantPath:   "mcp[0].arg",
			wantSugges: "args",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := CheckSettingsFields([]byte(tc.data))
			if len(got) != 1 {
				t.Fatalf("got %d unknown fields, want 1: %v", len(got), got)
			}
			if got[0].Path != tc.wantPath {
				t.Fatalf("path = %q, want %q", got[0].Path, tc.wantPath)
			}
			if got[0].Suggestion != tc.wantSugges {
				t.Fatalf("suggestion = %q, want %q", got[0].Suggestion, tc.wantSugges)
			}
		})
	}
}

func TestCheckSettingsFieldsDoesNotGuessForUnrelatedNames(t *testing.T) {
	got := CheckSettingsFields([]byte(`{"experimental_section": {}}`))
	if len(got) != 1 {
		t.Fatalf("got %v, want one unknown field", got)
	}
	if got[0].Suggestion != "" {
		t.Fatalf("suggestion = %q, want none for an unrelated name", got[0].Suggestion)
	}
	if want := `unknown setting "experimental_section"`; got[0].String() != want {
		t.Fatalf("String() = %q, want %q", got[0].String(), want)
	}
}

func TestCheckSettingsFieldsIgnoresCaseTheWayJSONDoes(t *testing.T) {
	// encoding/json matches names case-insensitively, so this file works and
	// reporting it would be a false positive.
	if got := CheckSettingsFields([]byte(`{"Web": {"provider": "duckduckgo"}}`)); len(got) != 0 {
		t.Fatalf("case-variant key reported as unknown: %v", got)
	}
}

func TestCheckSettingsFieldsIgnoresMalformedJSON(t *testing.T) {
	// Malformed JSON is the loader's error to report; there is no structure to
	// walk.
	if got := CheckSettingsFields([]byte(`{"runtime": `)); got != nil {
		t.Fatalf("got %v, want nothing for malformed JSON", got)
	}
	if got := CheckSettingsFields(nil); got != nil {
		t.Fatalf("got %v, want nothing for an empty document", got)
	}
}

func TestCheckFieldsAgainstUsesTheGivenSchema(t *testing.T) {
	type inner struct {
		Name string `json:"name"`
	}
	type schema struct {
		Items []inner `json:"items"`
	}
	got := CheckFieldsAgainst([]byte(`{"items": [{"name": "a", "nmae": "b"}]}`), schema{})
	if len(got) != 1 || got[0].Path != "items[0].nmae" || got[0].Suggestion != "name" {
		t.Fatalf("got %v, want one unknown field items[0].nmae suggesting name", got)
	}
}

func TestUnknownSettingsFieldsReadsThroughTheLoader(t *testing.T) {
	dir := t.TempDir()
	agents := filepath.Join(dir, ".agents")
	if err := os.MkdirAll(agents, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(agents, "settings.json"),
		[]byte(`{"runtme": {"max_team_turns": 3}}`), 0o644); err != nil {
		t.Fatalf("write settings: %v", err)
	}

	got := NewConfigLoader(dir).UnknownSettingsFields()
	if len(got) != 1 || got[0].Path != "runtme" || got[0].Suggestion != "runtime" {
		t.Fatalf("got %v, want one unknown field runtime suggesting runtime", got)
	}
}
