package media

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestParseMessageContentSupportsClaudeAndOpenAIShapes(t *testing.T) {
	raw := json.RawMessage(`[
		{"type":"text","text":"分析这张图"},
		{"type":"image","source":{"type":"base64","media_type":"image/png","data":"cG5n"}},
		{"type":"image_url","image_url":{"url":"https://example.test/a.png"}}
	]`)

	text, parts, err := ParseMessageContent(raw)
	require.NoError(t, err)
	require.Equal(t, "分析这张图", text)
	require.Len(t, parts, 2)
	require.Equal(t, "base64", parts[0].Media.SourceType)
	require.Equal(t, "url", parts[1].Media.SourceType)
}

func TestParseMessageContentSupportsFilePath(t *testing.T) {
	raw := json.RawMessage(`{"role":"user","content":[
		{"type":"file","file":{"path":"project/spec.pdf","mime_type":"application/pdf"}}
	]}`)
	_, parts, err := ParseMessageContent(raw)
	require.NoError(t, err)
	require.Len(t, parts, 1)
	require.Equal(t, "path", parts[0].Media.SourceType)
	require.Equal(t, "project/spec.pdf", parts[0].Media.Path)
}
