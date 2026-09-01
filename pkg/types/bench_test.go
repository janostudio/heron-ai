package types

import (
	"encoding/json"
	"testing"
	"time"
)

// BenchmarkSharedRecordJSON measures SharedRecord JSON encode/decode. This is
// the wire + storage format for every cross-team shared record, so it sits on
// the evidence-store persist path and the prompt "records" rendering path.
// The large Data map sub-benchmark isolates the cost of business-defined
// payloads of varying size, which dominates marshal time.
func BenchmarkSharedRecordJSON(b *testing.B) {
	b.Run("marshal", func(b *testing.B) {
		record := makeSharedRecord(16)
		b.ReportAllocs()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			if _, err := json.Marshal(record); err != nil {
				b.Fatal(err)
			}
		}
	})

	b.Run("unmarshal", func(b *testing.B) {
		record := makeSharedRecord(16)
		data, err := json.Marshal(record)
		if err != nil {
			b.Fatal(err)
		}
		b.ReportAllocs()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			var out SharedRecord
			if err := json.Unmarshal(data, &out); err != nil {
				b.Fatal(err)
			}
		}
	})

	b.Run("roundtrip_large_data", func(b *testing.B) {
		record := makeSharedRecord(256)
		b.ReportAllocs()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			data, err := json.Marshal(record)
			if err != nil {
				b.Fatal(err)
			}
			var out SharedRecord
			if err := json.Unmarshal(data, &out); err != nil {
				b.Fatal(err)
			}
		}
	})
}

func makeSharedRecord(dataSize int) SharedRecord {
	data := make(map[string]any, dataSize)
	for i := 0; i < dataSize; i++ {
		data[keyFor(i)] = map[string]any{
			"value": i,
			"label": "some-business-payload",
			"flag":  i%2 == 0,
		}
	}
	return SharedRecord{
		RecordID: "diagnosis-1",
		Kind:     "diagnosis",
		Name:     "DiagnosisReport",
		Scope:    RecordScopeFlow,
		Producer: ProducerRef{
			FlowSessionID: "fs-1",
			TeamID:        "default",
			CallID:        "assistant",
		},
		Summary: "Root cause found",
		Data:    data,
		Basis: []BasisRef{
			{Kind: "workspace", Path: "src/main.go", Revision: "abc123"},
		},
		Status:    RecordActive,
		Revision:  2,
		Links:     []RecordLink{{Relation: "supersedes", TargetID: "diagnosis-0"}},
		CreatedAt: time.Now().UTC(),
	}
}

func keyFor(i int) string {
	// Deterministic short keys avoid a string-builder allocation in the loop.
	const alphabet = "abcdefghijklmnopqrstuvwxyz"
	return string([]byte{alphabet[i%26], alphabet[(i/26)%26], alphabet[(i/676)%26]})
}
