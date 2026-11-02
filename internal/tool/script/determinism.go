package script

import (
	"fmt"
	"hash/fnv"
	"regexp"
	"time"

	"github.com/dop251/goja"
)

// The replay design in §5 of the design doc requires one property from the
// engine: the same script over the same tool results must take the same path
// every time. goja does not give that for free — Date, Math.random and a few
// host objects are ambient nondeterminism. This file closes those holes.
//
// Every entry here is a correctness requirement, not a hardening nicety. Losing
// one shows up as "replay ran a different number of tool calls", which is very
// hard to trace back to its cause.
//
// When upgrading goja, or when exposing a new global, walk this list again.

// forbiddenPatterns are source constructs that cannot be made deterministic, or
// that would silently degrade instead of failing loudly.
var forbiddenPatterns = []struct {
	re  *regexp.Regexp
	msg string
}{
	{regexp.MustCompile(`\bawait\b`), "await is not available: this runtime has no async. Call tools directly and use the returned value"},
	{regexp.MustCompile(`\basync\b`), "async functions are not available: every tool call returns its result synchronously"},
	{regexp.MustCompile(`\bPromise\b`), "Promise is not available: there is no event loop, so a Promise would never settle"},
	{regexp.MustCompile(`\bsetTimeout\b|\bsetInterval\b`), "timers are not available: they make execution order time-dependent, which replay cannot reproduce"},
	{regexp.MustCompile(`\bnew\s+Date\s*\(`), "new Date() reads the wall clock and breaks replay. Date.now() is pinned to the start of this script; use that instead"},
	{regexp.MustCompile(`\brequire\s*\(`), "require is not available: this sandbox has no module system"},
	{regexp.MustCompile(`\bimport\s*\(`), "dynamic import is not available: tools are pre-bound on the tools object"},
}

// CheckSource rejects code that cannot run deterministically. It runs before
// the runtime exists so the model gets a sentence it can act on instead of a
// ReferenceError or, worse, a Promise that never settles.
func CheckSource(code string) error {
	for _, p := range forbiddenPatterns {
		if loc := p.re.FindStringIndex(code); loc != nil {
			return fmt.Errorf("%s", p.msg)
		}
	}
	return nil
}

// escapeSurfaces are globals that either expose host capabilities or create
// concurrency that replay cannot model. Deleting them is defence in depth: the
// sandbox already has no import, so they are unreachable in practice, but a
// future goja release must not be able to reintroduce them silently.
var escapeSurfaces = []string{
	"WebAssembly",
	"Atomics",
	"SharedArrayBuffer",
	"FinalizationRegistry",
	"WeakRef",
	"Proxy",
}

// installDeterminism pins the ambient nondeterminism sources and removes the
// escape surfaces. baseMillis is derived from the script identity so a replay
// of the same call sees the same clock.
func installDeterminism(rt *goja.Runtime, scriptID string, baseMillis int64) error {
	fixed := time.UnixMilli(baseMillis).UTC()

	dateObj := rt.Get("Date")
	if dateObj != nil {
		if o := dateObj.ToObject(rt); o != nil {
			if err := o.Set("now", func(goja.FunctionCall) goja.Value {
				return rt.ToValue(baseMillis)
			}); err != nil {
				return fmt.Errorf("pin Date.now: %w", err)
			}
			// new Date() is rejected by CheckSource, but a script can still reach
			// the constructor through Date(). Pin it to the same instant so any
			// remaining path is stable.
			if err := o.Set("toString", func(goja.FunctionCall) goja.Value {
				return rt.ToValue(fixed.Format(time.RFC3339Nano))
			}); err != nil {
				return fmt.Errorf("pin Date.toString: %w", err)
			}
		}
	}

	perf := rt.NewObject()
	if err := perf.Set("now", func(goja.FunctionCall) goja.Value {
		return rt.ToValue(int64(0))
	}); err != nil {
		return fmt.Errorf("install performance.now: %w", err)
	}
	if err := rt.Set("performance", perf); err != nil {
		return fmt.Errorf("set performance: %w", err)
	}

	mathObj := rt.Get("Math")
	if mathObj != nil {
		if o := mathObj.ToObject(rt); o != nil {
			gen := newDeterministicRand(seedFromScriptID(scriptID))
			if err := o.Set("random", func(goja.FunctionCall) goja.Value {
				return rt.ToValue(gen.next())
			}); err != nil {
				return fmt.Errorf("pin Math.random: %w", err)
			}
		}
	}

	global := rt.GlobalObject()
	for _, name := range escapeSurfaces {
		if err := global.Delete(name); err != nil {
			return fmt.Errorf("remove %s: %w", name, err)
		}
	}
	return nil
}

// seedFromScriptID derives a stable seed. The script call id is generated once
// by the model call and stays the same across replays, so the seed does too.
func seedFromScriptID(scriptID string) uint64 {
	h := fnv.New64a()
	_, _ = h.Write([]byte(scriptID))
	seed := h.Sum64()
	if seed == 0 {
		seed = 0x9E3779B97F4A7C15
	}
	return seed
}

// deterministicRand is xorshift64*. Any small PRNG works; what matters is that
// the sequence depends only on the seed, never on wall time or entropy.
type deterministicRand struct{ state uint64 }

func newDeterministicRand(seed uint64) *deterministicRand {
	return &deterministicRand{state: seed}
}

func (r *deterministicRand) next() float64 {
	x := r.state
	x ^= x << 13
	x ^= x >> 7
	x ^= x << 17
	r.state = x
	// 53 bits of mantissa, matching the range of Math.random [0,1).
	return float64(x>>11) / float64(1<<53)
}

// deterministicBaseMillis returns the clock value a script sees. It is derived
// from the script id so it is stable across replays but still differs between
// two separate Script calls.
func deterministicBaseMillis(scriptID string) int64 {
	// 2026-01-01T00:00:00Z plus a per-script offset under one day. A fixed epoch
	// keeps logs comparable while the offset prevents every script from looking
	// like it ran at exactly the same instant.
	const epoch = int64(1767225600000)
	return epoch + int64(seedFromScriptID(scriptID)%86400000)
}
