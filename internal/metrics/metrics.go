// Package metrics keeps the process-wide counters an operator needs to answer
// "is it working, and where is it stuck" without reading session files.
//
// It is deliberately tiny: a name-to-integer map exported as Prometheus text.
// A metrics SDK would be a large dependency for a CLI that people also run as a
// server, and the numbers below are the ones this engine can actually act on.
// Everything here is process-local: a restart resets the counters, which is
// what "since this process started" means and avoids persisting metrics into
// the session files that are the product's source of truth.
package metrics

import (
	"fmt"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
)

// counter is one integer. Counters and gauges share the type: the difference
// is only how the operator reads them, and a gauge that is written with Store
// is still just a number.
type counter struct {
	value atomic.Int64
}

var registry sync.Map // name -> *counter

// Inc adds one to name.
func Inc(name string) { Add(name, 1) }

// Add adds delta to name, creating it at zero first.
func Add(name string, delta int64) {
	load(name).value.Add(delta)
}

// Set replaces name's value. Used for gauges, where the current value is the
// whole story (how many turns are running right now).
func Set(name string, value int64) {
	load(name).value.Store(value)
}

// Value reads one counter. Missing names read as zero.
func Value(name string) int64 {
	if entry, ok := registry.Load(name); ok {
		return entry.(*counter).value.Load()
	}
	return 0
}

// Snapshot copies every counter. Tests use it; Render is what the endpoint uses.
func Snapshot() map[string]int64 {
	out := make(map[string]int64)
	registry.Range(func(key, value any) bool {
		out[key.(string)] = value.(*counter).value.Load()
		return true
	})
	return out
}

// Reset clears every counter. It exists for tests: a process-wide registry
// would otherwise leak state between them.
func Reset() {
	registry.Range(func(key, _ any) bool {
		registry.Delete(key)
		return true
	})
}

// Render writes the registry in Prometheus text exposition format.
//
// The names are emitted as-is under a heron_ prefix, in sorted order, so two
// scrapes of an unchanged process produce identical bytes and a diff of the
// output is meaningful.
func Render() string {
	snapshot := Snapshot()
	names := make([]string, 0, len(snapshot))
	for name := range snapshot {
		names = append(names, name)
	}
	sort.Strings(names)

	var builder strings.Builder
	for _, name := range names {
		full := "heron_" + sanitize(name)
		kind := "counter"
		if strings.HasSuffix(name, "_current") {
			kind = "gauge"
		}
		fmt.Fprintf(&builder, "# TYPE %s %s\n%s %d\n", full, kind, full, snapshot[name])
	}
	return builder.String()
}

// sanitize keeps a metric name inside the exposition character set: a name
// with a dash or a dot would otherwise produce a line no scraper accepts.
func sanitize(name string) string {
	var builder strings.Builder
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '_', r == ':':
			builder.WriteRune(r)
		default:
			builder.WriteByte('_')
		}
	}
	return builder.String()
}

func load(name string) *counter {
	if entry, ok := registry.Load(name); ok {
		return entry.(*counter)
	}
	created := &counter{}
	actual, _ := registry.LoadOrStore(name, created)
	return actual.(*counter)
}
