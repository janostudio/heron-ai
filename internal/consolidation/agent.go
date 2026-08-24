// Package consolidation contains optional helpers for combining explicit
// SharedRecord values. Core TeamRuntime already performs configured output
// selection and does not require a special Aggregator concept.
package consolidation

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/heron-ai/heron-engine/pkg/types"
)

type RecordConsolidator struct{}

func NewRecordConsolidator() *RecordConsolidator {
	return &RecordConsolidator{}
}

func (c *RecordConsolidator) Consolidate(_ context.Context, records []types.SharedRecord) string {
	if len(records) == 0 {
		return ""
	}
	if len(records) == 1 {
		return records[0].Summary
	}

	ordered := append([]types.SharedRecord(nil), records...)
	sort.SliceStable(ordered, func(i, j int) bool {
		return ordered[i].Name < ordered[j].Name
	})

	var parts []string
	parts = append(parts, "## Shared Records")
	for i, record := range ordered {
		name := record.Name
		if name == "" {
			name = fmt.Sprintf("record-%d", i+1)
		}
		parts = append(parts, fmt.Sprintf("### %s\n%s", name, record.Summary))
	}
	return strings.Join(parts, "\n\n")
}
