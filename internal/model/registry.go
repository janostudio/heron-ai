package model

import (
	"fmt"
	"sync"

	"github.com/heron-ai/heron-engine/pkg/types"
)

type ModelRegistry struct {
	mu              sync.RWMutex
	providers       map[string]types.ModelProvider
	defaultProvider string
}

func NewModelRegistry() *ModelRegistry {
	return &ModelRegistry{
		providers: make(map[string]types.ModelProvider),
	}
}

func (r *ModelRegistry) Register(name string, provider types.ModelProvider) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.providers[name]; exists {
		return fmt.Errorf("provider %q already registered", name)
	}
	r.providers[name] = provider
	if r.defaultProvider == "" {
		r.defaultProvider = name
	}
	return nil
}

func (r *ModelRegistry) SetDefault(name string) error {
	r.mu.RLock()
	_, exists := r.providers[name]
	r.mu.RUnlock()
	if !exists {
		return fmt.Errorf("provider %q not found", name)
	}
	r.mu.Lock()
	r.defaultProvider = name
	r.mu.Unlock()
	return nil
}

func (r *ModelRegistry) Get(name string) (types.ModelProvider, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	p, ok := r.providers[name]
	if !ok {
		return nil, fmt.Errorf("provider %q not found", name)
	}
	return p, nil
}

func (r *ModelRegistry) GetDefault() (types.ModelProvider, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if r.defaultProvider == "" {
		return nil, fmt.Errorf("no default provider configured")
	}
	return r.providers[r.defaultProvider], nil
}

func (r *ModelRegistry) List() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	names := make([]string, 0, len(r.providers))
	for name := range r.providers {
		names = append(names, name)
	}
	return names
}
