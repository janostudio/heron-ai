package mcp

import (
	"context"
	"errors"
	"strings"
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

// TestReadSSEMessageWaitsForMatchingID covers the stream carrying a
// server-initiated notification before the reply: taking the first parseable
// message returned a Response with no Result, and the call failed while decoding
// a payload that was never an answer.
func TestReadSSEMessageWaitsForMatchingID(t *testing.T) {
	body := strings.Join([]string{
		`data: {"jsonrpc":"2.0","method":"notifications/message","params":{"level":"info"}}`,
		"",
		`data: {"jsonrpc":"2.0","id":7,"result":{"ok":true}}`,
		"",
	}, "\n")

	msg, err := readSSEMessage(strings.NewReader(body), 7)
	require.NoError(t, err)
	require.Equal(t, int64(7), msg.ID)
	require.JSONEq(t, `{"ok":true}`, string(msg.Result))

	_, err = readSSEMessage(strings.NewReader(body), 8)
	require.Error(t, err, "a reply for another id must not be returned")
	require.Contains(t, err.Error(), "request 8")
}
