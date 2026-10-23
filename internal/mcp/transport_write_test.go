package mcp

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// blockingWriter never accepts a byte: it models a child process that stopped
// reading its stdin (npx still installing, a wedged server).
type blockingWriter struct {
	release chan struct{}
}

func (b *blockingWriter) Write(p []byte) (int, error) {
	<-b.release
	return len(p), nil
}

// TestWriteWithContextHonoursDeadline covers the unbounded side of the stdio
// request path: the read loop was bounded by ctx, the write was not, so a pipe
// nobody drained hung the whole AgentTurn behind it.
func TestWriteWithContextHonoursDeadline(t *testing.T) {
	transport := &StdioTransport{}
	writer := &blockingWriter{release: make(chan struct{})}
	defer close(writer.release)

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	start := time.Now()
	err := transport.writeWithContext(ctx, writer, []byte("{}\n"))
	require.Error(t, err)
	require.True(t, errors.Is(err, context.DeadlineExceeded), "got %v", err)
	require.Less(t, time.Since(start), 2*time.Second)
}

// TestWriteWithContextReturnsWriteError keeps the normal path intact.
func TestWriteWithContextReturnsWriteError(t *testing.T) {
	transport := &StdioTransport{}
	writer := &blockingWriter{release: make(chan struct{})}
	close(writer.release)

	require.NoError(t, transport.writeWithContext(context.Background(), writer, []byte("{}\n")))
}
