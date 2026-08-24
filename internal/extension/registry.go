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

type Capability string

const (
	CapabilityMemory        Capability = "memory"
	CapabilityKnowledge     Capability = "knowledge"
	CapabilityObservability Capability = "observability"
	CapabilityLearning      Capability = "learning"
	CapabilityTool          Capability = "tool"
)

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

// CheckCapabilities validates required and optional capabilities separately.
// Core capabilities such as workspace and records can be supplied by the
// caller without registering them as third-party extensions.
func (r *ExtensionRegistry) CheckCapabilities(required []string, optional []string) error {
	r.mu.RLock()
	defer r.mu.RUnlock()

	for _, capability := range required {
		if !r.hasCapabilityLocked(capability) && capability != "workspace" && capability != "records" {
			return fmt.Errorf("required capability %q is unavailable", capability)
		}
	}
	return nil
}

func (r *ExtensionRegistry) HasCapability(name string) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.hasCapabilityLocked(name)
}

func (r *ExtensionRegistry) hasCapabilityLocked(name string) bool {
	if info, ok := r.extensions[name]; ok {
		return info.Enabled
	}
	for _, info := range r.extensions {
		if info.Type == name && info.Enabled {
			return true
		}
	}
	return false
}
