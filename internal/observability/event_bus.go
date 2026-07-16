package observability

import (
	"sync"
	"time"
)

// Event is the interface for all events
type Event interface {
	Type() string
	Timestamp() time.Time
}

// EventBus is a publish-subscribe event bus
type EventBus struct {
	mu           sync.RWMutex
	subscribers  map[string][]chan Event
	bufferSize   int
	droppedCount map[string]int64
}

func NewEventBus(bufferSize int) *EventBus {
	if bufferSize <= 0 {
		bufferSize = 256
	}
	return &EventBus{
		subscribers:  make(map[string][]chan Event),
		bufferSize:   bufferSize,
		droppedCount: make(map[string]int64),
	}
}

func (b *EventBus) Subscribe(eventType string, ch chan Event, bufferSize int) {
	b.mu.Lock()
	defer b.mu.Unlock()

	if bufferSize <= 0 {
		bufferSize = b.bufferSize
	}

	// Use the provided channel directly
	b.subscribers[eventType] = append(b.subscribers[eventType], ch)
}

func (b *EventBus) Publish(event Event) {
	b.mu.RLock()
	defer b.mu.RUnlock()

	eventType := event.Type()

	// Send to specific subscribers
	subscribers := b.subscribers[eventType]
	// Also send to wildcard subscribers
	subscribers = append(subscribers, b.subscribers["*"]...)

	for _, ch := range subscribers {
		select {
		case ch <- event:
			// sent successfully
		default:
			// channel full, drop
			b.droppedCount[eventType]++
		}
	}
}

func (b *EventBus) GetDroppedCount(eventType string) int64 {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return b.droppedCount[eventType]
}

// Base event struct for embedding
type BaseEvent struct {
	eventType string
	timestamp time.Time
}

func (e BaseEvent) Type() string        { return e.eventType }
func (e BaseEvent) Timestamp() time.Time { return e.timestamp }

func NewBaseEvent(eventType string) BaseEvent {
	return BaseEvent{
		eventType: eventType,
		timestamp: time.Now(),
	}
}

// Common event types
type RunStartedEvent struct {
	BaseEvent
	RunID    string
	FlowName string
}

type AgentStartedEvent struct {
	BaseEvent
	RunID     string
	RoundNum  int
	TeamName  string
	AgentName string
}

type LLMCallCompletedEvent struct {
	BaseEvent
	RunID             string
	TeamName          string
	AgentName         string
	Model             string
	PromptTokens      int
	CompletionTokens  int
	Duration          time.Duration
}

type ErrorOccurredEvent struct {
	BaseEvent
	RunID      string
	RoundNum   int
	TeamName   string
	AgentName  string
	Layer      string
	Module     string
	ToolName   string
	ErrorType  string
	Error      string
	StackTrace string
	Context    string
}

type ContextCompressedEvent struct {
	BaseEvent
	RunID        string
	AgentName    string
	Layer        string
	BeforeTokens int
	AfterTokens  int
}
