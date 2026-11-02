package script

import (
	"bytes"
	"time"
)

// Limits for one Script execution. They exist because model-written code is
// budgeted work, not user intent: a runaway loop must cost one timeout, not
// the whole session.
const (
	// DefaultTimeout bounds one Script call when the caller passes none.
	DefaultTimeout = 60 * time.Second
	// MaxTimeout is the ceiling a caller can request. An AgentTurn already has
	// DefaultToolTimeout (15m) as the outer backstop; this is tighter because a
	// script fans out many tool calls and one slow cell should not consume the
	// whole turn budget.
	MaxTimeout = 600 * time.Second
	// DefaultMaxSteps bounds inner tool calls per execution. This is the budget
	// that catches "call Bash 100000 times in a loop", which the wall clock may
	// not notice on a fast tool.
	DefaultMaxSteps = 200
	// MaxCodeBytes caps the submitted source. The model writes orchestration,
	// not a program with embedded data blobs.
	MaxCodeBytes = 64 * 1024
	// MaxOutputBytes caps returned value plus console output together. The full
	// text is spilled to a blob store and referenced, never silently dropped.
	MaxOutputBytes = 100 * 1024
	// watchDogInterval is how often a running cell checks whether it should be
	// interrupted. goja's Interrupt is cooperative: it lands on the next
	// interruptible point, so this value sets latency, not a hard deadline.
	watchDogInterval = 50 * time.Millisecond
)

// ClampTimeout normalises a caller-supplied timeout.
func ClampTimeout(d time.Duration) time.Duration {
	if d <= 0 {
		return DefaultTimeout
	}
	if d > MaxTimeout {
		return MaxTimeout
	}
	return d
}

// ClampMaxSteps normalises the inner-call budget.
func ClampMaxSteps(n int) int {
	if n <= 0 {
		return DefaultMaxSteps
	}
	return n
}

// limitedBuffer collects console output without letting a chatty script grow
// unbounded. It records the overflow instead of failing so the model still sees
// the beginning of its own output.
type limitedBuffer struct {
	buf      bytes.Buffer
	limit    int
	overflow bool
}

func newLimitedBuffer(limit int) *limitedBuffer {
	if limit <= 0 {
		limit = MaxOutputBytes
	}
	return &limitedBuffer{limit: limit}
}

func (b *limitedBuffer) Write(p []byte) (int, error) {
	if b.buf.Len() >= b.limit {
		b.overflow = true
		return len(p), nil
	}
	room := b.limit - b.buf.Len()
	if len(p) > room {
		b.buf.Write(p[:room])
		b.overflow = true
		return len(p), nil
	}
	return b.buf.Write(p)
}

func (b *limitedBuffer) String() string { return b.buf.String() }

func (b *limitedBuffer) Overflowed() bool { return b.overflow }

// truncateText shortens s to at most limit bytes on a rune boundary so the
// result stays valid UTF-8.
func truncateText(s string, limit int) (string, bool) {
	if limit <= 0 || len(s) <= limit {
		return s, false
	}
	cut := limit
	for cut > 0 && !utf8Start(s[cut]) {
		cut--
	}
	return s[:cut], true
}

// utf8Start reports whether b begins a UTF-8 sequence (i.e. is not a
// continuation byte).
func utf8Start(b byte) bool { return b&0xC0 != 0x80 }
