package observability

import (
	"sync"
	"time"
)

type Histogram struct {
	mu      sync.Mutex
	Buckets []float64
	Counts  []int64
	Sum     float64
	Count   int64
}

func NewHistogram(buckets []float64) *Histogram {
	return &Histogram{
		Buckets: buckets,
		Counts:  make([]int64, len(buckets)),
	}
}

func (h *Histogram) Observe(value float64) {
	h.mu.Lock()
	defer h.mu.Unlock()

	h.Sum += value
	h.Count++

	for i, boundary := range h.Buckets {
		if value <= boundary {
			h.Counts[i]++
			return
		}
	}
	// Above all buckets, add to last
	h.Counts[len(h.Counts)-1]++
}

func (h *Histogram) Percentile(p float64) float64 {
	h.mu.Lock()
	defer h.mu.Unlock()

	if h.Count == 0 {
		return 0
	}

	target := int(float64(h.Count) * p / 100.0)
	cumulative := int64(0)
	for i, count := range h.Counts {
		cumulative += count
		if cumulative >= int64(target+1) {
			return h.Buckets[i]
		}
	}
	return h.Buckets[len(h.Buckets)-1]
}

type Metrics struct {
	mu         sync.RWMutex
	counters   map[string]int64
	gauges     map[string]float64
	histograms map[string]*Histogram
}

func NewMetrics() *Metrics {
	return &Metrics{
		counters:   make(map[string]int64),
		gauges:     make(map[string]float64),
		histograms: make(map[string]*Histogram),
	}
}

func (m *Metrics) Inc(name string) {
	m.Add(name, 1)
}

func (m *Metrics) Add(name string, value int64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.counters[name] += value
}

func (m *Metrics) Set(name string, value float64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.gauges[name] = value
}

func (m *Metrics) Observe(name string, duration time.Duration) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if _, ok := m.histograms[name]; !ok {
		m.histograms[name] = NewHistogram(defaultBuckets())
	}
	m.histograms[name].Observe(duration.Seconds())
}

func (m *Metrics) Get(name string) float64 {
	m.mu.RLock()
	defer m.mu.RUnlock()

	if v, ok := m.counters[name]; ok {
		return float64(v)
	}
	if v, ok := m.gauges[name]; ok {
		return v
	}
	return 0
}

func (m *Metrics) Snapshot() map[string]any {
	m.mu.RLock()
	defer m.mu.RUnlock()

	snap := make(map[string]any)

	counters := make(map[string]int64)
	for k, v := range m.counters {
		counters[k] = v
	}
	snap["counters"] = counters

	gauges := make(map[string]float64)
	for k, v := range m.gauges {
		gauges[k] = v
	}
	snap["gauges"] = gauges

	histograms := make(map[string]map[string]any)
	for name, h := range m.histograms {
		histograms[name] = map[string]any{
			"count": h.Count,
			"sum":   h.Sum,
			"p50":   h.Percentile(50),
			"p95":   h.Percentile(95),
			"p99":   h.Percentile(99),
		}
	}
	snap["histograms"] = histograms

	return snap
}

func defaultBuckets() []float64 {
	return []float64{0.01, 0.05, 0.1, 0.5, 1.0, 5.0, 10.0, 30.0, 60.0}
}
