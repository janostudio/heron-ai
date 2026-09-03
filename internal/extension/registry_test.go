package extension

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newRegistryWith(t *testing.T, infos ...ExtensionInfo) *ExtensionRegistry {
	t.Helper()
	reg := NewExtensionRegistry()
	for _, info := range infos {
		require.NoError(t, reg.Register(info))
	}
	return reg
}

func TestCheckCapabilities_AllRequiredSatisfied(t *testing.T) {
	reg := newRegistryWith(t,
		ExtensionInfo{Name: "memory", Type: "lua", Enabled: true},
		ExtensionInfo{Name: "knowledge", Type: "lua", Enabled: true},
	)

	missing, err := reg.CheckCapabilities([]string{"memory", "knowledge"}, nil)
	require.NoError(t, err)
	assert.Empty(t, missing)
}

func TestCheckCapabilities_MissingRequiredReturnsError(t *testing.T) {
	reg := newRegistryWith(t, ExtensionInfo{Name: "memory", Type: "lua", Enabled: true})

	missing, err := reg.CheckCapabilities([]string{"memory", "guardrail"}, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "guardrail")
	assert.Nil(t, missing)
}

func TestCheckCapabilities_RequiredDisabledReturnsError(t *testing.T) {
	reg := newRegistryWith(t, ExtensionInfo{Name: "memory", Type: "lua", Enabled: false})

	_, err := reg.CheckCapabilities([]string{"memory"}, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "memory")
}

func TestCheckCapabilities_WorkspaceAndRecordsExempt(t *testing.T) {
	reg := NewExtensionRegistry()

	missing, err := reg.CheckCapabilities([]string{"workspace", "records"}, nil)
	require.NoError(t, err)
	assert.Empty(t, missing)
}

func TestCheckCapabilities_OptionalMissingNotErrorButReported(t *testing.T) {
	reg := newRegistryWith(t, ExtensionInfo{Name: "memory", Type: "lua", Enabled: true})

	missing, err := reg.CheckCapabilities(
		[]string{"memory"},
		[]string{"knowledge", "observability"},
	)
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{"knowledge", "observability"}, missing)
}

func TestCheckCapabilities_OptionalSatisfiedNotReported(t *testing.T) {
	reg := newRegistryWith(t,
		ExtensionInfo{Name: "memory", Type: "lua", Enabled: true},
		ExtensionInfo{Name: "knowledge", Type: "lua", Enabled: true},
	)

	missing, err := reg.CheckCapabilities(nil, []string{"knowledge"})
	require.NoError(t, err)
	assert.Empty(t, missing)
}

func TestHasCapability_RegisteredEnabled(t *testing.T) {
	reg := newRegistryWith(t, ExtensionInfo{Name: "memory", Type: "lua", Enabled: true})
	assert.True(t, reg.HasCapability("memory"))
}

func TestHasCapability_RegisteredDisabled(t *testing.T) {
	reg := newRegistryWith(t, ExtensionInfo{Name: "memory", Type: "lua", Enabled: false})
	assert.False(t, reg.HasCapability("memory"))
}

func TestHasCapability_NotRegistered(t *testing.T) {
	reg := NewExtensionRegistry()
	assert.False(t, reg.HasCapability("nonexistent"))
}

func TestHasCapability_DoesNotMatchByType(t *testing.T) {
	// A capability must be registered under its Name; the Type field
	// ("lua"/"wasm") is the runtime format, not a capability key.
	reg := newRegistryWith(t, ExtensionInfo{Name: "ext1", Type: "memory", Enabled: true})
	assert.False(t, reg.HasCapability("memory"))
	assert.True(t, reg.HasCapability("ext1"))
}

func TestCheckCapabilities_EmptyInput(t *testing.T) {
	reg := NewExtensionRegistry()
	missing, err := reg.CheckCapabilities(nil, nil)
	require.NoError(t, err)
	assert.Empty(t, missing)
}
