package media

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/heron-ai/heron-engine/pkg/types"
)

func TestAttachmentsToParts(t *testing.T) {
	tests := []struct {
		name        string
		attachments []types.MediaAttachment
		wantKinds   []string
	}{
		{
			name:        "empty",
			attachments: nil,
			wantKinds:   []string{},
		},
		{
			name: "explicit kind preserved",
			attachments: []types.MediaAttachment{
				{Kind: "image", MIMEType: "image/png", Name: "a.png"},
				{Kind: "document", MIMEType: "application/pdf", Name: "b.pdf"},
			},
			wantKinds: []string{"image", "document"},
		},
		{
			name: "kind inferred from mime",
			attachments: []types.MediaAttachment{
				{MIMEType: "image/jpeg"},
				{MIMEType: "audio/mpeg"},
				{MIMEType: "video/mp4"},
				{MIMEType: "text/plain"},
				{MIMEType: "application/x-unknown"},
			},
			wantKinds: []string{"image", "audio", "video", "document", "file"},
		},
		{
			name: "empty kind and mime falls back to file",
			attachments: []types.MediaAttachment{
				{},
			},
			wantKinds: []string{"file"},
		},
		{
			name: "image/jpg alias normalized",
			attachments: []types.MediaAttachment{
				{MIMEType: "image/jpg"},
			},
			wantKinds: []string{"image"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			parts := AttachmentsToParts(tt.attachments)
			require.Len(t, parts, len(tt.wantKinds))
			for i, want := range tt.wantKinds {
				require.Equal(t, want, parts[i].Type)
				require.NotNil(t, parts[i].Media)
			}
		})
	}
}

func TestNormalizeMIME(t *testing.T) {
	tests := []struct {
		in   string
		want string
	}{
		{"IMAGE/PNG", "image/png"},
		{"image/jpg", "image/jpeg"},
		{"image/jpeg", "image/jpeg"},
		{"  text/plain  ", "text/plain"},
		{"application/pdf; charset=utf-8", "application/pdf"},
		{"", ""},
	}
	for _, tt := range tests {
		require.Equal(t, tt.want, normalizeMIME(tt.in), "input %q", tt.in)
	}
}

func TestKindForMIME(t *testing.T) {
	tests := []struct {
		mime string
		want string
	}{
		{"image/png", "image"},
		{"image/jpeg", "image"},
		{"audio/mpeg", "audio"},
		{"audio/wav", "audio"},
		{"video/mp4", "video"},
		{"application/pdf", "document"},
		{"text/plain", "document"},
		{"text/markdown", "document"},
		{"application/msword", "document"},
		{"application/vnd.openxmlformats-officedocument.spreadsheetml.sheet", "document"},
		{"application/vnd.openxmlformats-officedocument.presentationml.presentation", "document"},
		{"application/json", "document"},
		{"application/octet-stream", "file"},
		{"application/zip", "file"},
	}
	for _, tt := range tests {
		require.Equal(t, tt.want, kindForMIME(tt.mime), "mime %q", tt.mime)
	}
}

func TestValidateDeclaredKind(t *testing.T) {
	tests := []struct {
		name     string
		kind     string
		mime     string
		wantErr  bool
	}{
		{"empty kind ok", "", "image/png", false},
		{"file kind always ok", "file", "image/png", false},
		{"matching image", "image", "image/png", false},
		{"matching document", "document", "application/pdf", false},
		{"mismatch image vs document", "image", "application/pdf", true},
		{"mismatch audio vs video", "audio", "video/mp4", true},
		{"case-insensitive kind", "IMAGE", "image/png", false},
		// Note: MIME is expected to be pre-normalized by Store; uppercase MIME
		// is not lowercased inside validateDeclaredKind and thus mismatches.
		{"uppercase mime not normalized", "image", "IMAGE/PNG", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateDeclaredKind(tt.kind, tt.mime)
			if tt.wantErr {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
		})
	}
}

func TestDecodeBase64(t *testing.T) {
	tests := []struct {
		name    string
		in      string
		want    string
		wantErr bool
	}{
		{"plain base64", "aGVsbG8=", "hello", false},
		{"data url prefix", "data:image/png;base64,aGVsbG8=", "hello", false},
		{"data url no params", "data:,aGVsbG8=", "hello", false},
		{"surrounding whitespace", "  aGVsbG8=  ", "hello", false},
		{"invalid base64", "!!!not-base64!!!", "", true},
		{"empty", "", "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := decodeBase64(tt.in)
			if tt.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tt.want, string(got))
		})
	}
}

func TestSafeStorageRef(t *testing.T) {
	tests := []struct {
		ref  string
		want bool
	}{
		{".agents/data/uploads/abc", true},
		{"abc", true},
		{"/abs/path", false},
		{"../outside", false},
		{"a/../../outside", false},
		{"a/../b", false},
		{".", false},
		{"a//b", false}, // not clean
	}
	for _, tt := range tests {
		require.Equal(t, tt.want, safeStorageRef(tt.ref), "ref %q", tt.ref)
	}
}

func TestAllowedMIME(t *testing.T) {
	defaults := (Limits{}).withDefaults()
	store := &FileStore{limits: defaults}

	tests := []struct {
		mime string
		want bool
	}{
		{"image/png", true},
		{"application/pdf", true},
		{"text/plain", true},
		{"video/mp4", true},
		{"application/x-unknown", false},
		{"text/x-unknown", false},
	}
	for _, tt := range tests {
		require.Equal(t, tt.want, store.allowedMIME(tt.mime), "mime %q", tt.mime)
	}

	// Wildcard rule: audio/* matches any audio subtype.
	wild := store.allowedMIME("audio/flac")
	require.False(t, wild, "defaults do not include audio/*")

	store.limits.AllowedMIME = map[string]bool{"audio/*": true}
	require.True(t, store.allowedMIME("audio/flac"))
	require.False(t, store.allowedMIME("video/mp4"))
}

func TestParseMessageContentBranches(t *testing.T) {
	t.Run("empty raw", func(t *testing.T) {
		text, parts, err := ParseMessageContent(nil)
		require.NoError(t, err)
		require.Empty(t, text)
		require.Nil(t, parts)
	})
	t.Run("null raw", func(t *testing.T) {
		text, parts, err := ParseMessageContent(json.RawMessage("null"))
		require.NoError(t, err)
		require.Empty(t, text)
		require.Nil(t, parts)
	})
	t.Run("plain string", func(t *testing.T) {
		text, parts, err := ParseMessageContent(json.RawMessage(`"hello"`))
		require.NoError(t, err)
		require.Equal(t, "hello", text)
		require.Nil(t, parts)
	})
	t.Run("envelope object", func(t *testing.T) {
		text, parts, err := ParseMessageContent(json.RawMessage(`{"content":"wrapped"}`))
		require.NoError(t, err)
		require.Equal(t, "wrapped", text)
		require.Nil(t, parts)
	})
	t.Run("envelope with blocks", func(t *testing.T) {
		text, parts, err := ParseMessageContent(json.RawMessage(`{"content":[{"type":"text","text":"hi"}]}`))
		require.NoError(t, err)
		require.Equal(t, "hi", text)
		require.Empty(t, parts)
	})
	t.Run("invalid content", func(t *testing.T) {
		_, _, err := ParseMessageContent(json.RawMessage(`{"foo": 123}`))
		require.Error(t, err)
	})
	t.Run("mixed text and image blocks", func(t *testing.T) {
		text, parts, err := ParseMessageContent(json.RawMessage(`[
			{"type":"text","text":"before "},
			{"type":"image","source":{"type":"url","url":"https://example.test/a.png"}},
			{"type":"text","text":"after"}
		]`))
		require.NoError(t, err)
		require.Equal(t, "before after", text)
		require.Len(t, parts, 1)
		require.Equal(t, "image", parts[0].Type)
		require.Equal(t, "url", parts[0].Media.SourceType)
	})
}

func TestParseBlockBranches(t *testing.T) {
	t.Run("empty block is text", func(t *testing.T) {
		part, text, err := parseBlock(json.RawMessage(`{"text":"hi"}`))
		require.NoError(t, err)
		require.Equal(t, "hi", text)
		require.Empty(t, part.Type)
	})
	t.Run("text type", func(t *testing.T) {
		part, text, err := parseBlock(json.RawMessage(`{"type":"text","text":"hi"}`))
		require.NoError(t, err)
		require.Equal(t, "hi", text)
		require.Empty(t, part.Type)
	})
	t.Run("input_text type", func(t *testing.T) {
		_, text, err := parseBlock(json.RawMessage(`{"type":"input_text","text":"hi"}`))
		require.NoError(t, err)
		require.Equal(t, "hi", text)
	})
	t.Run("file type with file_data", func(t *testing.T) {
		part, _, err := parseBlock(json.RawMessage(`{"type":"file","file":{"file_data":"aGVsbG8="}}`))
		require.NoError(t, err)
		require.Equal(t, "file", part.Type)
		require.Equal(t, "aGVsbG8=", part.Media.DataBase64)
		require.Equal(t, "base64", part.Media.SourceType)
	})
	t.Run("file type with path", func(t *testing.T) {
		part, _, err := parseBlock(json.RawMessage(`{"type":"file","file":{"path":"x/y.pdf"}}`))
		require.NoError(t, err)
		require.Equal(t, "path", part.Media.SourceType)
		require.Equal(t, "x/y.pdf", part.Media.Path)
	})
	t.Run("input_image via media", func(t *testing.T) {
		part, _, err := parseBlock(json.RawMessage(`{"type":"input_image","media":{"url":"https://example.test/i.png"}}`))
		require.NoError(t, err)
		require.Equal(t, "image", part.Type)
		require.Equal(t, "url", part.Media.SourceType)
	})
	t.Run("input_file via media", func(t *testing.T) {
		part, _, err := parseBlock(json.RawMessage(`{"type":"input_file","media":{"path":"doc.txt"}}`))
		require.NoError(t, err)
		require.Equal(t, "file", part.Type)
		require.Equal(t, "path", part.Media.SourceType)
	})
	t.Run("unsupported type errors", func(t *testing.T) {
		_, _, err := parseBlock(json.RawMessage(`{"type":"weird"}`))
		require.Error(t, err)
	})
	t.Run("invalid json errors", func(t *testing.T) {
		_, _, err := parseBlock(json.RawMessage(`{`))
		require.Error(t, err)
	})
	t.Run("base64 source with no data errors", func(t *testing.T) {
		_, _, err := parseBlock(json.RawMessage(`{"type":"image","source":{"type":"base64"}}`))
		require.Error(t, err)
	})
}

func TestParseAttachmentSourceTypeInference(t *testing.T) {
	t.Run("base64 inferred", func(t *testing.T) {
		att, err := parseAttachment("image", "", "", "", "", "aGk=", "", nil)
		require.NoError(t, err)
		require.Equal(t, "base64", att.SourceType)
	})
	t.Run("path inferred", func(t *testing.T) {
		att, err := parseAttachment("image", "", "", "a/b.png", "", "", "", nil)
		require.NoError(t, err)
		require.Equal(t, "path", att.SourceType)
	})
	t.Run("url inferred", func(t *testing.T) {
		att, err := parseAttachment("image", "", "", "", "https://e.test/a.png", "", "", nil)
		require.NoError(t, err)
		require.Equal(t, "url", att.SourceType)
	})
	t.Run("source fields merged", func(t *testing.T) {
		src := json.RawMessage(`{"media_type":"image/png","data":"aGk="}`)
		att, err := parseAttachment("image", "", "", "", "", "", "", src)
		require.NoError(t, err)
		require.Equal(t, "image/png", att.MIMEType)
		require.Equal(t, "aGk=", att.DataBase64)
		require.Equal(t, "base64", att.SourceType)
	})
}

func TestParseURLObjectBranches(t *testing.T) {
	t.Run("empty raw errors", func(t *testing.T) {
		_, err := parseURLObject("image", nil)
		require.Error(t, err)
	})
	t.Run("string url", func(t *testing.T) {
		att, err := parseURLObject("image", json.RawMessage(`"https://e.test/a.png"`))
		require.NoError(t, err)
		require.Equal(t, "url", att.SourceType)
		require.Equal(t, "https://e.test/a.png", att.URL)
	})
	t.Run("object with data url", func(t *testing.T) {
		att, err := parseURLObject("image", json.RawMessage(`{"data":"data:image/png;base64,aGVsbG8="}`))
		require.NoError(t, err)
		require.Equal(t, "base64", att.SourceType)
		require.Equal(t, "aGVsbG8=", att.DataBase64)
	})
	t.Run("invalid base64 errors", func(t *testing.T) {
		_, err := parseURLObject("image", json.RawMessage(`{"data":"!!!"}`))
		require.Error(t, err)
	})
	t.Run("file_id unsupported", func(t *testing.T) {
		_, err := parseURLObject("image", json.RawMessage(`{"file_id":"abc"}`))
		require.Error(t, err)
	})
	t.Run("object with file_url fallback", func(t *testing.T) {
		att, err := parseURLObject("image", json.RawMessage(`{"file_url":"https://e.test/f.png"}`))
		require.NoError(t, err)
		require.Equal(t, "url", att.SourceType)
		require.Equal(t, "https://e.test/f.png", att.URL)
	})
}
