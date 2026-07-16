package agent

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/heron-ai/heron-engine/pkg/types"
)

type HITLGate struct {
	mu      sync.Mutex
	pending map[string]chan types.HITLResponse
	timeout time.Duration
}

func NewHITLGate(timeout time.Duration) *HITLGate {
	if timeout <= 0 {
		timeout = 5 * time.Minute
	}
	return &HITLGate{
		pending: make(map[string]chan types.HITLResponse),
		timeout: timeout,
	}
}

func (g *HITLGate) RequestApproval(ctx context.Context, req types.HITLRequest) (types.HITLResponse, error) {
	ch := make(chan types.HITLResponse, 1)

	g.mu.Lock()
	g.pending[req.RequestID] = ch
	g.mu.Unlock()

	select {
	case resp := <-ch:
		return resp, nil
	case <-time.After(g.timeout):
		g.mu.Lock()
		delete(g.pending, req.RequestID)
		g.mu.Unlock()
		return types.HITLResponse{
			RequestID: req.RequestID,
			Approved:  false,
			Reason:    "approval timeout",
		}, nil
	case <-ctx.Done():
		g.mu.Lock()
		delete(g.pending, req.RequestID)
		g.mu.Unlock()
		return types.HITLResponse{}, ctx.Err()
	}
}

func (g *HITLGate) SubmitResponse(resp types.HITLResponse) error {
	g.mu.Lock()
	ch, ok := g.pending[resp.RequestID]
	delete(g.pending, resp.RequestID)
	g.mu.Unlock()

	if !ok {
		return fmt.Errorf("no pending request with ID %q", resp.RequestID)
	}

	ch <- resp
	return nil
}

func (g *HITLGate) PendingCount() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return len(g.pending)
}
