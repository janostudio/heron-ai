package main

import (
	"testing"

	"github.com/heron-ai/heron-engine/internal/storage"
	"github.com/heron-ai/heron-engine/pkg/types"
)

func TestEventLayer(t *testing.T) {
	cases := map[string]string{
		types.EventSharedRecordPublished: "flow",
		types.EventFlowTurnCompleted:     "flow",
		types.EventTeamTurnCompleted:     "team",
		types.EventAgentTurnCompleted:    "agent",
		types.EventToolCallCompleted:     "agent",
		"command_turn.completed":         "team",
		"webhook_turn.started":           "team",
		"approval.requested":             "team",
	}
	for eventType, want := range cases {
		if got := eventLayer(eventType); got != want {
			t.Fatalf("eventLayer(%q) = %q, want %q", eventType, got, want)
		}
	}
}

func TestEventsToLayeredSources(t *testing.T) {
	events := []storage.SessionEvent{
		{
			EventHeader: types.EventHeader{Type: types.EventSharedRecordPublished},
			Payload: map[string]any{
				"record": map[string]any{"name": "Rule A", "summary": "Use X"},
			},
		},
		{
			EventHeader: types.EventHeader{Type: types.EventAgentTurnCompleted},
			Payload:     map[string]any{"reply": "did Y"},
		},
	}

	sources := eventsToLayeredSources(events)
	if len(sources) != 2 {
		t.Fatalf("expected 2 sources, got %d", len(sources))
	}
	if sources[0].Layer != "flow" {
		t.Fatalf("expected flow layer, got %q", sources[0].Layer)
	}
	if sources[1].Layer != "agent" {
		t.Fatalf("expected agent layer, got %q", sources[1].Layer)
	}
}

func TestNormalizeScope(t *testing.T) {
	cases := map[string]string{
		"flow":    "flow",
		"team":    "team",
		"agent":   "agent",
		"FLOW":    "flow",
		"all":     "flow",
		"agents":  "agent",
		"":        "flow",
		"bogus":   "flow",
	}
	for in, want := range cases {
		if got := normalizeScope(in); got != want {
			t.Fatalf("normalizeScope(%q) = %q, want %q", in, got, want)
		}
	}
}
