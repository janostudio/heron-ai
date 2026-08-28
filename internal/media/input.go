package media

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/heron-ai/heron-engine/pkg/types"
)

// ParseMessageContent accepts the common CLI message shapes:
// string content, {content: ...}, and arrays of text/image/document/file
// content blocks. It deliberately preserves media as structured parts.
func ParseMessageContent(raw json.RawMessage) (string, []types.ContentPart, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return "", nil, nil
	}
	var text string
	if err := json.Unmarshal(raw, &text); err == nil {
		return text, nil, nil
	}
	var envelope struct {
		Content json.RawMessage `json:"content"`
	}
	if err := json.Unmarshal(raw, &envelope); err == nil && len(envelope.Content) > 0 {
		return parseContentValue(envelope.Content)
	}
	return parseContentValue(raw)
}

func parseContentValue(raw json.RawMessage) (string, []types.ContentPart, error) {
	var text string
	if err := json.Unmarshal(raw, &text); err == nil {
		return text, nil, nil
	}
	var blocks []json.RawMessage
	if err := json.Unmarshal(raw, &blocks); err != nil {
		return "", nil, fmt.Errorf("message content must be text or content blocks")
	}
	var textParts []string
	var parts []types.ContentPart
	for _, rawBlock := range blocks {
		part, blockText, err := parseBlock(rawBlock)
		if err != nil {
			return "", nil, err
		}
		if blockText != "" {
			textParts = append(textParts, blockText)
		}
		if part.Type != "" {
			parts = append(parts, part)
		}
	}
	return strings.Join(textParts, ""), parts, nil
}

func parseBlock(raw json.RawMessage) (types.ContentPart, string, error) {
	var block struct {
		Type       string          `json:"type"`
		Text       string          `json:"text"`
		Source     json.RawMessage `json:"source"`
		ImageURL   json.RawMessage `json:"image_url"`
		File       json.RawMessage `json:"file"`
		Media      json.RawMessage `json:"media"`
		Path       string          `json:"path"`
		MIMEType   string          `json:"mime_type"`
		Name       string          `json:"name"`
		URL        string          `json:"url"`
		Data       string          `json:"data"`
		DataBase64 string          `json:"data_base64"`
	}
	if err := json.Unmarshal(raw, &block); err != nil {
		return types.ContentPart{}, "", fmt.Errorf("invalid content block: %w", err)
	}
	kind := strings.ToLower(strings.TrimSpace(block.Type))
	switch kind {
	case "", "text":
		return types.ContentPart{}, block.Text, nil
	case "image", "audio", "video", "document", "file":
		source := block.Source
		if len(source) == 0 {
			source = block.Media
		}
		if kind == "file" && len(source) == 0 {
			source = block.File
		}
		if kind == "file" && block.DataBase64 == "" && len(block.File) > 0 {
			var file struct {
				FileData string `json:"file_data"`
			}
			_ = json.Unmarshal(block.File, &file)
			block.DataBase64 = file.FileData
		}
		attachment, err := parseAttachment(kind, block.MIMEType, block.Name, block.Path, block.URL, block.DataBase64, block.Data, source)
		if err != nil {
			return types.ContentPart{}, "", err
		}
		return types.ContentPart{Type: kind, Media: &attachment}, "", nil
	case "image_url":
		attachment, err := parseURLObject("image", block.ImageURL)
		if err != nil {
			return types.ContentPart{}, "", err
		}
		return types.ContentPart{Type: "image", Media: &attachment}, "", nil
	case "input_text":
		return types.ContentPart{}, block.Text, nil
	case "input_image", "input_file":
		attachment, err := parseURLObject(strings.TrimPrefix(kind, "input_"), block.Media)
		if err != nil {
			return types.ContentPart{}, "", err
		}
		return types.ContentPart{Type: attachment.Kind, Media: &attachment}, "", nil
	default:
		return types.ContentPart{}, "", fmt.Errorf("unsupported content block type %q", block.Type)
	}
}

func parseAttachment(kind, mimeType, name, path, url, encoded, data string, sourceRaw json.RawMessage) (types.MediaAttachment, error) {
	var source struct {
		Type      string `json:"type"`
		MediaType string `json:"media_type"`
		MIMEType  string `json:"mime_type"`
		Data      string `json:"data"`
		FileData  string `json:"file_data"`
		URL       string `json:"url"`
		Path      string `json:"path"`
	}
	if len(sourceRaw) > 0 {
		_ = json.Unmarshal(sourceRaw, &source)
	}
	if mimeType == "" {
		mimeType = firstNonEmpty(source.MediaType, source.MIMEType)
	}
	if encoded == "" {
		encoded = firstNonEmpty(source.Data, source.FileData)
	}
	if url == "" {
		url = source.URL
	}
	if path == "" {
		path = source.Path
	}
	sourceType := strings.ToLower(strings.TrimSpace(source.Type))
	if sourceType == "" {
		switch {
		case encoded != "":
			sourceType = "base64"
		case path != "":
			sourceType = "path"
		case url != "":
			sourceType = "url"
		}
	}
	if sourceType == "base64" && encoded == "" {
		return types.MediaAttachment{}, fmt.Errorf("%s content block has no base64 data", kind)
	}
	return types.MediaAttachment{
		Name:       name,
		MIMEType:   mimeType,
		Kind:       kind,
		SourceType: sourceType,
		Path:       path,
		URL:        url,
		DataBase64: encoded,
	}, nil
}

func parseURLObject(kind string, raw json.RawMessage) (types.MediaAttachment, error) {
	if len(raw) == 0 {
		return types.MediaAttachment{}, fmt.Errorf("%s content block has no source", kind)
	}
	var value string
	if json.Unmarshal(raw, &value) == nil {
		return parseAttachment(kind, "", "", "", value, "", "", nil)
	}
	var object struct {
		URL        string `json:"url"`
		FileURL    string `json:"file_url"`
		FileData   string `json:"file_data"`
		Data       string `json:"data"`
		DataBase64 string `json:"data_base64"`
		FileID     string `json:"file_id"`
		MIMEType   string `json:"mime_type"`
		Name       string `json:"filename"`
		Path       string `json:"path"`
	}
	if err := json.Unmarshal(raw, &object); err != nil {
		return types.MediaAttachment{}, fmt.Errorf("invalid %s source: %w", kind, err)
	}
	if object.URL == "" {
		object.URL = object.FileURL
	}
	encoded := firstNonEmpty(object.DataBase64, object.FileData, object.Data)
	if strings.HasPrefix(encoded, "data:") {
		if comma := strings.IndexByte(encoded, ','); comma >= 0 {
			encoded = encoded[comma+1:]
		}
	}
	if encoded != "" {
		if _, err := base64.StdEncoding.DecodeString(encoded); err != nil {
			return types.MediaAttachment{}, fmt.Errorf("invalid %s base64 data: %w", kind, err)
		}
		return parseAttachment(kind, object.MIMEType, object.Name, object.Path, object.URL, encoded, "", nil)
	}
	if object.FileID != "" {
		return types.MediaAttachment{}, fmt.Errorf("%s file_id references are not supported without a file resolver", kind)
	}
	return parseAttachment(kind, object.MIMEType, object.Name, object.Path, object.URL, "", "", nil)
}

func AttachmentsToParts(attachments []types.MediaAttachment) []types.ContentPart {
	parts := make([]types.ContentPart, 0, len(attachments))
	for i := range attachments {
		attachment := attachments[i]
		kind := attachment.Kind
		if kind == "" {
			kind = kindForMIME(normalizeMIME(attachment.MIMEType))
		}
		parts = append(parts, types.ContentPart{Type: kind, Media: &attachment})
	}
	return parts
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}
