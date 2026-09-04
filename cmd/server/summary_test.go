package main

import (
	"testing"

	"github.com/heron-ai/heron-engine/internal/storage"
	"github.com/heron-ai/heron-engine/pkg/types"
)

func TestExtractSharedRecords(t *testing.T) {
	record := types.SharedRecord{
		RecordID: "r1",
		Name:     "research",
		Summary:  "the summary",
		Scope:    types.RecordScopeFlow,
		Status:   types.RecordActive,
	}

	replay := &storage.SessionReplay{
		SessionID: "s1",
		Events: []storage.SessionEvent{
			{
				EventHeader: types.EventHeader{
					Type:          types.EventSharedRecordPublished,
					FlowSessionID: "s1",
				},
				Payload: map[string]any{"record": record},
			},
			{
				EventHeader: types.EventHeader{
					Type:          types.EventTeamTurnCompleted,
					FlowSessionID: "s1",
				},
				Payload: map[string]any{"team_result": "ignored"},
			},
			{
				EventHeader: types.EventHeader{
					Type:          types.EventSharedRecordPublished,
					FlowSessionID: "s1",
				},
				Payload: map[string]any{}, // missing record key
			},
			{
				EventHeader: types.EventHeader{
					Type:          types.EventSharedRecordPublished,
					FlowSessionID: "s1",
				},
				Payload: map[string]any{"record": "not-a-record"}, // invalid shape
			},
		},
	}

	records := extractSharedRecords(replay)
	if len(records) != 1 {
		t.Fatalf("expected 1 record, got %d", len(records))
	}
	if records[0].RecordID != "r1" || records[0].Name != "research" || records[0].Summary != "the summary" {
		t.Fatalf("unexpected record: %+v", records[0])
	}
}

func TestExtractSharedRecordsNilReplay(t *testing.T) {
	if got := extractSharedRecords(nil); got != nil {
		t.Fatalf("expected nil for nil replay, got %v", got)
	}
}

func TestRecordsToSources(t *testing.T) {
	records := []types.SharedRecord{
		{Name: "a", Summary: "summary a"},
		{Name: "b"},
		{Summary: "summary c"},
		{},
	}

	got := recordsToSources(records)
	want := []string{"[a] summary a", "b", "summary c"}
	if len(got) != len(want) {
		t.Fatalf("expected %d sources, got %d: %v", len(want), len(got), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("source %d: expected %q, got %q", i, want[i], got[i])
		}
	}
}

func TestRecordsToSourcesEmpty(t *testing.T) {
	if got := recordsToSources(nil); len(got) != 0 {
		t.Fatalf("expected empty, got %v", got)
	}
}
