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
//
// Required capabilities must be present (or be one of the core capabilities
// "workspace"/"records", which the caller supplies without registering them as
// third-party extensions); a missing required capability yields an error.
//
// Optional capabilities are not required for startup: a missing optional
// capability does not produce an error. Instead it is reported in the returned
// missing slice so the caller can degrade gracefully and record a
// "capability unavailable" notice.
func (r *ExtensionRegistry) CheckCapabilities(required []string, optional []string) (missing []string, err error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	for _, capability := range required {
		if !r.hasCapabilityLocked(capability) && capability != "workspace" && capability != "records" {
			return nil, fmt.Errorf("required capability %q is unavailable", capability)
		}
	}

	for _, capability := range optional {
		if !r.hasCapabilityLocked(capability) {
			missing = append(missing, capability)
		}
	}

	return missing, nil
}

func (r *ExtensionRegistry) HasCapability(name string) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.hasCapabilityLocked(name)
}

// hasCapabilityLocked reports whether a capability with the given name is
// registered and enabled. Capabilities are keyed by ExtensionInfo.Name;
// ExtensionInfo.Type ("lua" | "wasm") describes the extension's runtime format
// and is unrelated to capability lookup.
func (r *ExtensionRegistry) hasCapabilityLocked(name string) bool {
	info, ok := r.extensions[name]
	return ok && info.Enabled
}
