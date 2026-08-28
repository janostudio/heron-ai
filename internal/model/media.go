package model

import (
	"context"
	"encoding/base64"
	"fmt"
	"net/url"
	"strings"

	"github.com/heron-ai/heron-engine/pkg/types"
)

type mediaPayload struct {
	Data []byte
	URL  string
}

func resolveMediaPayload(ctx context.Context, resolver types.MediaResolver, attachment types.MediaAttachment) (mediaPayload, error) {
	if strings.EqualFold(strings.TrimSpace(attachment.SourceType), "url") &&
		strings.TrimSpace(attachment.URL) != "" {
		parsed, err := url.ParseRequestURI(attachment.URL)
		if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") {
			return mediaPayload{}, fmt.Errorf("media URL must use http or https")
		}
		return mediaPayload{URL: attachment.URL}, nil
	}
	if resolver == nil {
		if strings.TrimSpace(attachment.DataBase64) == "" {
			return mediaPayload{}, fmt.Errorf("media resolver is not configured for %q", mediaName(attachment))
		}
		data, err := decodeMediaBase64(attachment.DataBase64)
		if err != nil {
			return mediaPayload{}, err
		}
		return mediaPayload{Data: data}, nil
	}
	data, err := resolver.ResolveMedia(ctx, attachment)
	if err != nil {
		return mediaPayload{}, err
	}
	return mediaPayload{Data: data}, nil
}

func decodeMediaBase64(value string) ([]byte, error) {
	value = strings.TrimSpace(value)
	if strings.HasPrefix(value, "data:") {
		if comma := strings.IndexByte(value, ','); comma >= 0 {
			value = value[comma+1:]
		}
	}
	data, err := base64.StdEncoding.DecodeString(value)
	if err != nil {
		return nil, fmt.Errorf("decode media base64: %w", err)
	}
	return data, nil
}

func dataURL(mimeType string, data []byte) string {
	if strings.TrimSpace(mimeType) == "" {
		mimeType = "application/octet-stream"
	}
	return "data:" + mimeType + ";base64," + base64.StdEncoding.EncodeToString(data)
}

func mediaName(attachment types.MediaAttachment) string {
	if attachment.Name != "" {
		return attachment.Name
	}
	if attachment.StorageRef != "" {
		return attachment.StorageRef
	}
	return attachment.Kind
}

func mediaKind(part types.ContentPart) string {
	if part.Media != nil && part.Media.Kind != "" {
		return strings.ToLower(strings.TrimSpace(part.Media.Kind))
	}
	return strings.ToLower(strings.TrimSpace(part.Type))
}

func capabilityEnabled(capability *bool, nativeDefault bool) bool {
	if capability == nil {
		return nativeDefault
	}
	return *capability
}

func unsupportedMedia(kind string) error {
	return fmt.Errorf("model provider does not support %s content", kind)
}
