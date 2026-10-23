package main

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestStreamJSONApprovalKeepsMissingDecisionDistinct covers why the field is a
// pointer: with a plain bool, a caller that omitted `approved` sent "false" to
// the server and silently denied a pending approval.
func TestStreamJSONApprovalKeepsMissingDecisionDistinct(t *testing.T) {
	var missing streamJSONInput
	require.NoError(t, json.Unmarshal([]byte(`{"type":"approval","approval_id":"a1"}`), &missing))
	require.Nil(t, missing.Approved, "an absent decision must stay absent")

	var denied streamJSONInput
	require.NoError(t, json.Unmarshal([]byte(`{"type":"approval","approval_id":"a1","approved":false}`), &denied))
	require.NotNil(t, denied.Approved)
	require.False(t, *denied.Approved)

	var approved streamJSONInput
	require.NoError(t, json.Unmarshal([]byte(`{"type":"approval","approval_id":"a1","approved":true}`), &approved))
	require.NotNil(t, approved.Approved)
	require.True(t, *approved.Approved)
}
