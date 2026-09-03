package observability

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestEventBus_PublishWildcard(t *testing.T) {
	bus := NewEventBus(10)
	ch := make(chan Event, 10)
	bus.Subscribe("*", ch, 0)

	bus.Publish(NewBaseEvent("some.event"))

	select {
	case received := <-ch:
		assert.Equal(t, "some.event", received.Type())
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for wildcard event")
	}
}

func TestEventBus_MultipleSubscribers(t *testing.T) {
	bus := NewEventBus(10)
	ch1 := make(chan Event, 10)
	ch2 := make(chan Event, 10)
	bus.Subscribe("multi.event", ch1, 0)
	bus.Subscribe("multi.event", ch2, 0)

	event := NewBaseEvent("multi.event")
	bus.Publish(event)

	for i, ch := range []chan Event{ch1, ch2} {
		select {
		case received := <-ch:
			assert.Equal(t, "multi.event", received.Type(), "subscriber %d", i)
		case <-time.After(time.Second):
			t.Fatalf("timed out waiting for subscriber %d", i)
		}
	}
}

func TestEventBus_SpecificAndWildcardBothReceive(t *testing.T) {
	bus := NewEventBus(10)
	specific := make(chan Event, 10)
	wildcard := make(chan Event, 10)
	bus.Subscribe("both.event", specific, 0)
	bus.Subscribe("*", wildcard, 0)

	bus.Publish(NewBaseEvent("both.event"))

	select {
	case received := <-specific:
		assert.Equal(t, "both.event", received.Type())
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for specific subscriber")
	}

	select {
	case received := <-wildcard:
		assert.Equal(t, "both.event", received.Type())
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for wildcard subscriber")
	}
}
