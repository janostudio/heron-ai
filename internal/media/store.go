package media

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"strings"

	"github.com/heron-ai/heron-engine/internal/storage"
	"github.com/heron-ai/heron-engine/pkg/types"
)

// Limits intentionally bound inbound payloads before they can reach a model
// provider. These are storage limits, not model-token limits.
type Limits struct {
	MaxBytes    int64
	AllowedMIME map[string]bool
	AllowURL    bool
	HTTPClient  *http.Client
}

func (l Limits) withDefaults() Limits {
	if l.MaxBytes <= 0 {
		l.MaxBytes = 32 * 1024 * 1024
	}
	if l.AllowedMIME == nil {
		l.AllowedMIME = map[string]bool{
			"application/pdf":    true,
			"application/json":   true,
			"application/rtf":    true,
			"application/msword": true,
			"application/vnd.openxmlformats-officedocument.wordprocessingml.document": true,
			"application/vnd.ms-excel": true,
			"application/vnd.openxmlformats-officedocument.spreadsheetml.sheet":         true,
			"application/vnd.ms-powerpoint":                                             true,
			"application/vnd.openxmlformats-officedocument.presentationml.presentation": true,
			"text/plain":    true,
			"text/markdown": true,
			"text/csv":      true,
			"image/jpeg":    true,
			"image/png":     true,
			"image/gif":     true,
			"image/webp":    true,
			"image/bmp":     true,
			"audio/mpeg":    true,
			"audio/wav":     true,
			"audio/ogg":     true,
			"video/mp4":     true,
			"video/webm":    true,
		}
	}
	if l.HTTPClient == nil {
		l.HTTPClient = http.DefaultClient
	}
	return l
}

// FileStore persists content below the workspace. The media package only
// writes content-addressed paths and never trusts an inbound filename.
type FileStore struct {
	files  storage.FileStore
	limits Limits
}

func NewFileStore(files storage.FileStore, limits Limits) *FileStore {
	return &FileStore{files: files, limits: limits.withDefaults()}
}

func (s *FileStore) Store(ctx context.Context, attachment types.MediaAttachment) (types.MediaAttachment, error) {
	if s == nil || s.files == nil {
		return types.MediaAttachment{}, errors.New("media store is not configured")
	}
	if err := contextErr(ctx); err != nil {
		return types.MediaAttachment{}, err
	}

	data, mimeType, err := s.readInput(ctx, attachment)
	if err != nil {
		return types.MediaAttachment{}, err
	}
	if int64(len(data)) > s.limits.MaxBytes {
		return types.MediaAttachment{}, fmt.Errorf("media exceeds maximum size of %d bytes", s.limits.MaxBytes)
	}
	if mimeType == "" && attachment.MIMEType != "" {
		mimeType = attachment.MIMEType
	}
	if mimeType == "" {
		mimeType = detectMIME(data)
	}
	mimeType = normalizeMIME(mimeType)
	if !s.allowedMIME(mimeType) {
		return types.MediaAttachment{}, fmt.Errorf("media MIME type %q is not allowed", mimeType)
	}
	if err := validateDeclaredKind(attachment.Kind, mimeType); err != nil {
		return types.MediaAttachment{}, err
	}

	sum := sha256.Sum256(data)
	hash := hex.EncodeToString(sum[:])
	ref := filepath.ToSlash(filepath.Join(".agents", "data", "uploads", hash))
	if err := s.files.Write(ref, data); err != nil {
		return types.MediaAttachment{}, fmt.Errorf("store media: %w", err)
	}

	result := attachment
	result.ID = "media-" + hash[:16]
	result.MIMEType = mimeType
	result.Kind = kindForMIME(mimeType)
	result.SizeBytes = int64(len(data))
	result.SHA256 = hash
	result.SourceType = "stored"
	result.StorageRef = ref
	result.Path = ""
	result.URL = ""
	result.DataBase64 = ""
	if result.Name == "" {
		result.Name = result.ID
	}
	return result, nil
}

func (s *FileStore) ResolveMedia(ctx context.Context, attachment types.MediaAttachment) ([]byte, error) {
	if s == nil || s.files == nil {
		return nil, errors.New("media store is not configured")
	}
	if err := contextErr(ctx); err != nil {
		return nil, err
	}
	switch strings.ToLower(strings.TrimSpace(attachment.SourceType)) {
	case "", "stored":
		if attachment.StorageRef == "" {
			if attachment.DataBase64 != "" {
				return decodeBase64(attachment.DataBase64)
			}
			return nil, errors.New("media storage_ref is required")
		}
		if !safeStorageRef(attachment.StorageRef) {
			return nil, fmt.Errorf("unsafe media storage_ref %q", attachment.StorageRef)
		}
		data, err := s.files.Read(attachment.StorageRef)
		if err != nil {
			return nil, fmt.Errorf("read media %q: %w", attachment.StorageRef, err)
		}
		if int64(len(data)) > s.limits.MaxBytes {
			return nil, fmt.Errorf("stored media exceeds maximum size of %d bytes", s.limits.MaxBytes)
		}
		return data, nil
	case "base64":
		data, err := decodeBase64(attachment.DataBase64)
		if err != nil {
			return nil, err
		}
		if int64(len(data)) > s.limits.MaxBytes {
			return nil, fmt.Errorf("media exceeds maximum size of %d bytes", s.limits.MaxBytes)
		}
		return data, nil
	default:
		return nil, fmt.Errorf("media source %q must be stored before provider use", attachment.SourceType)
	}
}

func (s *FileStore) readInput(ctx context.Context, attachment types.MediaAttachment) ([]byte, string, error) {
	source := strings.ToLower(strings.TrimSpace(attachment.SourceType))
	switch source {
	case "", "base64":
		if attachment.DataBase64 == "" && attachment.StorageRef != "" {
			data, err := s.ResolveMedia(ctx, attachment)
			return data, attachment.MIMEType, err
		}
		data, err := decodeBase64(attachment.DataBase64)
		return data, attachment.MIMEType, err
	case "path":
		if strings.TrimSpace(attachment.Path) == "" {
			return nil, "", errors.New("media path is required")
		}
		path := filepath.Clean(filepath.FromSlash(attachment.Path))
		if filepath.IsAbs(path) || path == "." || path == ".." ||
			strings.HasPrefix(path, ".."+string(filepath.Separator)) {
			return nil, "", fmt.Errorf("unsafe media path %q", attachment.Path)
		}
		data, err := s.files.Read(filepath.ToSlash(path))
		return data, attachment.MIMEType, err
	case "stored":
		data, err := s.ResolveMedia(ctx, attachment)
		return data, attachment.MIMEType, err
	case "url":
		if !s.limits.AllowURL {
			return nil, "", errors.New("URL media sources are disabled")
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, attachment.URL, nil)
		if err != nil {
			return nil, "", err
		}
		resp, err := s.limits.HTTPClient.Do(req)
		if err != nil {
			return nil, "", err
		}
		defer func() { _ = resp.Body.Close() }()
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			return nil, "", fmt.Errorf("download media: http %s", resp.Status)
		}
		data, err := io.ReadAll(io.LimitReader(resp.Body, s.limits.MaxBytes+1))
		return data, firstNonEmpty(resp.Header.Get("Content-Type"), attachment.MIMEType), err
	default:
		return nil, "", fmt.Errorf("unsupported media source type %q", attachment.SourceType)
	}
}

func (s *FileStore) allowedMIME(mimeType string) bool {
	if s.limits.AllowedMIME[mimeType] {
		return true
	}
	for allowed := range s.limits.AllowedMIME {
		if strings.HasSuffix(allowed, "/*") && strings.HasPrefix(mimeType, strings.TrimSuffix(allowed, "*")) {
			return true
		}
	}
	return false
}

func decodeBase64(value string) ([]byte, error) {
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

func detectMIME(data []byte) string {
	if len(data) == 0 {
		return "application/octet-stream"
	}
	return http.DetectContentType(data)
}

func normalizeMIME(value string) string {
	value = strings.ToLower(strings.TrimSpace(strings.Split(value, ";")[0]))
	if value == "image/jpg" {
		return "image/jpeg"
	}
	return value
}

func kindForMIME(mimeType string) string {
	switch {
	case strings.HasPrefix(mimeType, "image/"):
		return "image"
	case strings.HasPrefix(mimeType, "audio/"):
		return "audio"
	case strings.HasPrefix(mimeType, "video/"):
		return "video"
	case mimeType == "application/pdf" || strings.HasPrefix(mimeType, "text/") ||
		strings.Contains(mimeType, "word") || strings.Contains(mimeType, "sheet") ||
		strings.Contains(mimeType, "presentation") || mimeType == "application/json":
		return "document"
	default:
		return "file"
	}
}

func validateDeclaredKind(kind, mimeType string) error {
	kind = strings.ToLower(strings.TrimSpace(kind))
	if kind == "" || kind == "file" {
		return nil
	}
	actual := kindForMIME(mimeType)
	if kind != actual {
		return fmt.Errorf("media kind %q does not match MIME type %q", kind, mimeType)
	}
	return nil
}

func safeStorageRef(ref string) bool {
	if filepath.IsAbs(ref) {
		return false
	}
	clean := filepath.Clean(ref)
	return clean == ref && clean != "." && !strings.HasPrefix(clean, "..") &&
		!strings.Contains(filepath.ToSlash(clean), "/../")
}

func contextErr(ctx context.Context) error {
	if ctx == nil {
		return nil
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
		return nil
	}
}
