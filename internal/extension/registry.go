package extension

import (
	"fmt"
	"sync"
)

type ExtensionInfo struct {
	Name    string
	Type    string // lua | wasm
	Path    string
	Enabled bool
}

type ExtensionRegistry struct {
	mu         sync.RWMutex
	extensions map[string]ExtensionInfo
}

func NewExtensionRegistry() *ExtensionRegistry {
	return &ExtensionRegistry{
		extensions: make(map[string]ExtensionInfo),
	}
}

func (r *ExtensionRegistry) Register(info ExtensionInfo) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.extensions[info.Name]; exists {
		return fmt.Errorf("extension %q already registered", info.Name)
	}
	r.extensions[info.Name] = info
	return nil
}

func (r *ExtensionRegistry) Get(name string) (ExtensionInfo, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	info, ok := r.extensions[name]
	if !ok {
		return ExtensionInfo{}, fmt.Errorf("extension %q not found", name)
	}
	return info, nil
}

func (r *ExtensionRegistry) List() []ExtensionInfo {
	r.mu.RLock()
	defer r.mu.RUnlock()
	result := make([]ExtensionInfo, 0, len(r.extensions))
	for _, info := range r.extensions {
		result = append(result, info)
	}
	return result
}

func (r *ExtensionRegistry) ListByType(extType string) []ExtensionInfo {
	r.mu.RLock()
	defer r.mu.RUnlock()
	var result []ExtensionInfo
	for _, info := range r.extensions {
		if info.Type == extType {
			result = append(result, info)
		}
	}
	return result
}
