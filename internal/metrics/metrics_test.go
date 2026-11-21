package metrics

import (
	"strings"
	"testing"
)

func TestCountersAccumulate(t *testing.T) {
	Reset()
	Inc("turns_started_total")
	Inc("turns_started_total")
	Add("turns_started_total", 3)

	if got := Value("turns_started_total"); got != 5 {
		t.Fatalf("Value = %d, want 5", got)
	}
	if got := Value("never_written_total"); got != 0 {
		t.Fatalf("missing counter = %d, want 0", got)
	}
}

func TestSetReplacesTheValue(t *testing.T) {
	Reset()
	Add("wakeups_pending_current", 10)
	Set("wakeups_pending_current", 3)
	if got := Value("wakeups_pending_current"); got != 3 {
		t.Fatalf("gauge = %d, want 3", got)
	}
}

func TestRenderUsesTheExpositionFormat(t *testing.T) {
	Reset()
	Inc("turns_started_total")
	Set("turns_inflight_current", 2)

	out := Render()

	for _, want := range []string{
		"# TYPE heron_turns_started_total counter\n",
		"heron_turns_started_total 1\n",
		// A name ending in _current reads as a gauge, which is what an
		// operator expects to see in a dashboard.
		"# TYPE heron_turns_inflight_current gauge\n",
		"heron_turns_inflight_current 2\n",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("Render() is missing %q:\n%s", want, out)
		}
	}
}

func TestRenderIsSortedAndStable(t *testing.T) {
	Reset()
	Inc("b_total")
	Inc("a_total")
	first := Render()
	second := Render()
	if first != second {
		t.Fatalf("two scrapes of an unchanged process differ:\n%s\n%s", first, second)
	}
	if strings.Index(first, "heron_a_total") > strings.Index(first, "heron_b_total") {
		t.Fatalf("names are not sorted:\n%s", first)
	}
}

func TestRenderSanitizesInvalidNameCharacters(t *testing.T) {
	Reset()
	Inc("turn.count-with-dash")
	out := Render()
	if !strings.Contains(out, "heron_turn_count_with_dash 1") {
		t.Fatalf("invalid characters survived into the exposition:\n%s", out)
	}
}

func TestResetClearsEverything(t *testing.T) {
	Reset()
	Inc("x_total")
	Reset()
	if len(Snapshot()) != 0 {
		t.Fatalf("Snapshot after Reset = %v, want empty", Snapshot())
	}
}
