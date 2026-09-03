package observability

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestHistogram_ObserveBucketBoundaries(t *testing.T) {
	buckets := []float64{1.0, 2.0, 3.0}
	h := NewHistogram(buckets)

	// Exactly on a boundary should fall into that bucket (value <= boundary).
	h.Observe(1.0)
	// Below the first boundary falls into the first bucket.
	h.Observe(0.5)
	// Middle bucket.
	h.Observe(2.0)
	// Above all buckets falls into the last bucket.
	h.Observe(10.0)

	require.Len(t, h.Counts, 3)
	assert.Equal(t, int64(2), h.Counts[0]) // 1.0 and 0.5
	assert.Equal(t, int64(1), h.Counts[1]) // 2.0
	assert.Equal(t, int64(1), h.Counts[2]) // 10.0 (overflow)

	assert.Equal(t, int64(4), h.Count)
	assert.InDelta(t, 13.5, h.Sum, 1e-9)
}

func TestHistogram_EmptyReturnsZero(t *testing.T) {
	h := NewHistogram([]float64{1.0, 2.0})
	assert.Equal(t, float64(0), h.Percentile(50))
	assert.Equal(t, float64(0), h.Percentile(95))
	assert.Equal(t, int64(0), h.Count)
	assert.Equal(t, float64(0), h.Sum)
}

func TestHistogram_Percentile(t *testing.T) {
	h := NewHistogram([]float64{1.0, 2.0, 3.0, 4.0})
	// One observation per bucket.
	for _, v := range []float64{1.0, 2.0, 3.0, 4.0} {
		h.Observe(v)
	}

	// Count = 4. target = int(4 * p / 100).
	// p50 -> target 2 -> cumulative >= 3 -> bucket index 2 -> 3.0
	assert.Equal(t, 3.0, h.Percentile(50))
	// p95 -> target 3 -> cumulative >= 4 -> bucket index 3 -> 4.0
	assert.Equal(t, 4.0, h.Percentile(95))
}

func TestHistogram_ObserveUsesSeconds(t *testing.T) {
	// Metrics.Observe converts time.Duration to seconds via duration.Seconds().
	m := NewMetrics()
	m.Observe("latency", 1500*time.Millisecond) // 1.5 seconds

	h := m.histograms["latency"]
	require.NotNil(t, h)
	assert.Equal(t, int64(1), h.Count)
	assert.InDelta(t, 1.5, h.Sum, 1e-9)
}

func TestMetrics_IncAdd(t *testing.T) {
	m := NewMetrics()
	m.Inc("requests")
	m.Inc("requests")
	m.Add("requests", 5)
	m.Add("errors", 2)

	assert.Equal(t, float64(7), m.Get("requests"))
	assert.Equal(t, float64(2), m.Get("errors"))
}

func TestMetrics_Set(t *testing.T) {
	m := NewMetrics()
	m.Set("temperature", 0.75)
	m.Set("temperature", 0.9)

	assert.Equal(t, 0.9, m.Get("temperature"))
}

func TestMetrics_GetPriority(t *testing.T) {
	m := NewMetrics()

	// Counter takes priority over gauge when both present.
	m.Add("metric", 10)
	m.Set("metric", 3.14)
	assert.Equal(t, float64(10), m.Get("metric"))

	// Gauge only.
	m.Set("gauge_only", 2.5)
	assert.Equal(t, 2.5, m.Get("gauge_only"))
}

func TestMetrics_GetMissing(t *testing.T) {
	m := NewMetrics()
	assert.Equal(t, float64(0), m.Get("nonexistent"))
}

func TestMetrics_ObserveCreatesHistogramOnce(t *testing.T) {
	m := NewMetrics()
	m.Observe("latency", time.Second)
	first := m.histograms["latency"]
	require.NotNil(t, first)

	m.Observe("latency", 2*time.Second)
	second := m.histograms["latency"]

	assert.Same(t, first, second)
	assert.Equal(t, int64(2), second.Count)
	assert.InDelta(t, 3.0, second.Sum, 1e-9)
}

func TestMetrics_Snapshot(t *testing.T) {
	m := NewMetrics()
	m.Inc("requests")
	m.Add("errors", 3)
	m.Set("gauge1", 1.5)
	m.Observe("latency", 100*time.Millisecond)
	m.Observe("latency", 300*time.Millisecond)

	snap := m.Snapshot()

	counters, ok := snap["counters"].(map[string]int64)
	require.True(t, ok)
	assert.Equal(t, int64(1), counters["requests"])
	assert.Equal(t, int64(3), counters["errors"])

	gauges, ok := snap["gauges"].(map[string]float64)
	require.True(t, ok)
	assert.Equal(t, 1.5, gauges["gauge1"])

	histograms, ok := snap["histograms"].(map[string]map[string]any)
	require.True(t, ok)
	latency, ok := histograms["latency"]
	require.True(t, ok)
	assert.Equal(t, int64(2), latency["count"])
	assert.InDelta(t, 0.4, latency["sum"].(float64), 1e-9)
}

func TestMetrics_SnapshotEmpty(t *testing.T) {
	m := NewMetrics()
	snap := m.Snapshot()

	counters := snap["counters"].(map[string]int64)
	gauges := snap["gauges"].(map[string]float64)
	histograms := snap["histograms"].(map[string]map[string]any)

	assert.Empty(t, counters)
	assert.Empty(t, gauges)
	assert.Empty(t, histograms)
}
