package extension

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestExtensionRegistry_RegisterAndGet(t *testing.T) {
	reg := NewExtensionRegistry()

	err := reg.Register(ExtensionInfo{
		Name:    "ext1",
		Type:    "lua",
		Path:    "/path/to/ext1.lua",
		Enabled: true,
	})
	require.NoError(t, err)

	info, err := reg.Get("ext1")
	require.NoError(t, err)
	assert.Equal(t, "ext1", info.Name)
	assert.Equal(t, "lua", info.Type)
	assert.Equal(t, "/path/to/ext1.lua", info.Path)
	assert.True(t, info.Enabled)
}

func TestExtensionRegistry_Register_Duplicate(t *testing.T) {
	reg := NewExtensionRegistry()
	info := ExtensionInfo{Name: "ext1", Type: "lua"}

	err := reg.Register(info)
	require.NoError(t, err)

	err = reg.Register(info)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "already registered")
}

func TestExtensionRegistry_Get_NotFound(t *testing.T) {
	reg := NewExtensionRegistry()
	_, err := reg.Get("nonexistent")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "not found")
}

func TestExtensionRegistry_List(t *testing.T) {
	reg := NewExtensionRegistry()
	reg.Register(ExtensionInfo{Name: "e1", Type: "lua"})
	reg.Register(ExtensionInfo{Name: "e2", Type: "wasm"})

	list := reg.List()
	assert.Len(t, list, 2)

	names := make([]string, len(list))
	for i, e := range list {
		names[i] = e.Name
	}
	assert.Contains(t, names, "e1")
	assert.Contains(t, names, "e2")
}

func TestExtensionRegistry_ListByType(t *testing.T) {
	reg := NewExtensionRegistry()
	reg.Register(ExtensionInfo{Name: "e1", Type: "lua"})
	reg.Register(ExtensionInfo{Name: "e2", Type: "lua"})
	reg.Register(ExtensionInfo{Name: "e3", Type: "wasm"})

	luaExts := reg.ListByType("lua")
	assert.Len(t, luaExts, 2)

	luaNames := make([]string, len(luaExts))
	for i, e := range luaExts {
		luaNames[i] = e.Name
	}
	assert.Contains(t, luaNames, "e1")
	assert.Contains(t, luaNames, "e2")

	wasmExts := reg.ListByType("wasm")
	assert.Len(t, wasmExts, 1)
	assert.Equal(t, "e3", wasmExts[0].Name)

	noExts := reg.ListByType("python")
	assert.Len(t, noExts, 0)
}
