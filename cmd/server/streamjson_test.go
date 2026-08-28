package main

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestStreamJSONMessageContentSupportsMultimediaBlocks(t *testing.T) {
	raw := json.RawMessage(`{"role":"user","content":[
		{"type":"text","text":"请分析图片"},
		{"type":"image","source":{"type":"base64","media_type":"image/png","data":"cG5n"}}
	]}`)
	text, parts, err := parseStreamJSONContent(raw)
	require.NoError(t, err)
	require.Equal(t, "请分析图片", text)
	require.Len(t, parts, 1)
	require.Equal(t, "image", parts[0].Type)
	require.Equal(t, "base64", parts[0].Media.SourceType)
}
