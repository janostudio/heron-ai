package tool

import (
	"fmt"
	"sync"

	"github.com/heron-ai/heron-engine/pkg/types"
)

type ToolRegistry struct {
	mu    sync.RWMutex
	tools map[string]types.Tool
}

func NewToolRegistry() *ToolRegistry {
	return &ToolRegistry{tools: make(map[string]types.Tool)}
}

func (r *ToolRegistry) Register(tool types.Tool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.tools[tool.Name()] = tool
}

func (r *ToolRegistry) Lookup(name string) (types.Tool, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	t, ok := r.tools[name]
	if !ok {
		return nil, fmt.Errorf("tool %q not found", name)
	}
	return t, nil
}

func (r *ToolRegistry) List() []types.Tool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	result := make([]types.Tool, 0, len(r.tools))
	for _, t := range r.tools {
		result = append(result, t)
	}
	return result
}

func (r *ToolRegistry) ListNames() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	names := make([]string, 0, len(r.tools))
	for name := range r.tools {
		names = append(names, name)
	}
	return names
}
