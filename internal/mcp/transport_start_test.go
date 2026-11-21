package mcp

import (
	"context"
	"errors"
	"testing"
)

// A caller that already gave up must not spawn a server it will never close.
func TestStdioStartRejectsCancelledContext(t *testing.T) {
	tr := NewStdioTransport("definitely-not-a-real-binary", nil, nil, nil)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := tr.Start(ctx)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Start with a cancelled context = %v, want context.Canceled", err)
	}
	if tr.cmd != nil {
		t.Fatal("Start launched a process despite the cancelled context")
	}
}
