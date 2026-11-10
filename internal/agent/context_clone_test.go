package agent

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/heron-ai/heron-engine/pkg/types"
)

// TestCloneMessageKeepsParts covers the copy every context path goes through —
// adding a message, reading the active list back, replaying a transcript.
//
// The bug this guards was invisible in text: cloneMessage replaced
// message.Parts with a fresh slice and then ranged over that same field, so it
// iterated the new all-zero slice and every part came out empty. Text still
// looked right because the words live in Content; an attachment became an empty
// part and never reached the provider.
func TestCloneMessageKeepsParts(t *testing.T) {
	original := types.Message{
		Role:    "user",
		Content: "看图",
		Parts: []types.ContentPart{
			{
				Type:  "image",
				Media: &types.MediaAttachment{SourceType: "base64", DataBase64: "aGk=", Kind: "image", MIMEType: "image/png"},
			},
			{Type: "text", Text: "附注"},
		},
	}

	cloned := cloneMessage(original)
	require.Len(t, cloned.Parts, 2)

	require.Equal(t, "image", cloned.Parts[0].Type)
	require.NotNil(t, cloned.Parts[0].Media, "附件在克隆时被清空了")
	require.Equal(t, "aGk=", cloned.Parts[0].Media.DataBase64)
	require.Equal(t, "text", cloned.Parts[1].Type)
	require.Equal(t, "附注", cloned.Parts[1].Text)

	// The copy has to be deep enough that compacting or rewriting the clone
	// cannot change the message it came from.
	cloned.Parts[0].Media.DataBase64 = "changed"
	require.Equal(t, "aGk=", original.Parts[0].Media.DataBase64, "克隆与原消息共享了附件对象")
}
