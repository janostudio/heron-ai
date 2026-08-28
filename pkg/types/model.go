package types

import (
	"context"
	"encoding/json"
)

// ModelProvider interface defines the LLM provider contract
type ModelProvider interface {
	Chat(ctx context.Context, messages []Message, tools []JSONSchema, config ModelConfig) (*ChatResponse, error)
	ChatStream(ctx context.Context, messages []Message, tools []JSONSchema, config ModelConfig) (<-chan ChatChunk, error)
}

// MediaResolver loads a durable media reference when a provider is about to
// construct its wire request. Runtime state keeps metadata and a storage
// reference; providers resolve bytes at the last possible moment.
type MediaResolver interface {
	ResolveMedia(ctx context.Context, attachment MediaAttachment) ([]byte, error)
}

// MediaStore persists inbound attachments and resolves durable references.
// Large inline payloads must not be written to session events/checkpoints.
type MediaStore interface {
	Store(ctx context.Context, attachment MediaAttachment) (MediaAttachment, error)
	MediaResolver
}

// MediaResolverSetter is implemented by providers (or a provider router)
// that can resolve durable media references while constructing a request.
type MediaResolverSetter interface {
	SetMediaResolver(resolver MediaResolver)
}

// ContentPart is the provider-neutral representation of one message part.
// Text remains a fast path; non-text content is represented by Media.
type ContentPart struct {
	Type  string           `json:"type" yaml:"type"`
	Text  string           `json:"text,omitempty" yaml:"text,omitempty"`
	Media *MediaAttachment `json:"media,omitempty" yaml:"media,omitempty"`
}

// MarshalJSON emits common provider-neutral content blocks in a CLI-friendly
// shape. Incoming wire formats remain accepted by internal/media.
func (p ContentPart) MarshalJSON() ([]byte, error) {
	type alias ContentPart
	if p.Media == nil {
		return json.Marshal(alias(p))
	}
	out := map[string]any{
		"type": p.Type,
	}
	media := *p.Media
	switch p.Type {
	case "image", "audio", "video":
		source := map[string]any{"type": media.SourceType}
		if media.ID != "" {
			source["id"] = media.ID
		}
		if media.Name != "" {
			source["name"] = media.Name
		}
		if media.MIMEType != "" {
			source["media_type"] = media.MIMEType
		}
		if media.SizeBytes > 0 {
			source["size_bytes"] = media.SizeBytes
		}
		if media.DataBase64 != "" {
			source["data"] = media.DataBase64
		}
		if media.URL != "" {
			source["url"] = media.URL
		}
		if media.Path != "" {
			source["path"] = media.Path
		}
		if media.StorageRef != "" {
			source["storage_ref"] = media.StorageRef
		}
		if media.SHA256 != "" {
			source["sha256"] = media.SHA256
		}
		out["source"] = source
	case "document", "file":
		file := map[string]any{}
		if media.ID != "" {
			file["id"] = media.ID
		}
		if media.Name != "" {
			file["filename"] = media.Name
		}
		if media.SizeBytes > 0 {
			file["size_bytes"] = media.SizeBytes
		}
		if media.DataBase64 != "" {
			file["file_data"] = media.DataBase64
		}
		if media.Path != "" {
			file["path"] = media.Path
		}
		if media.URL != "" {
			file["url"] = media.URL
		}
		if media.MIMEType != "" {
			file["mime_type"] = media.MIMEType
		}
		if media.SourceType != "" {
			file["source_type"] = media.SourceType
		}
		if media.StorageRef != "" {
			file["storage_ref"] = media.StorageRef
		}
		if media.SHA256 != "" {
			file["sha256"] = media.SHA256
		}
		out["file"] = file
	default:
		out["media"] = media
	}
	return json.Marshal(out)
}

// UnmarshalJSON accepts the same provider-neutral shape emitted by
// MarshalJSON as well as the internal {"media": ...} representation. This is
// required for Flow/session/checkpoint replay to retain durable media refs.
func (p *ContentPart) UnmarshalJSON(data []byte) error {
	var raw struct {
		Type   string           `json:"type"`
		Text   string           `json:"text"`
		Media  *MediaAttachment `json:"media"`
		Source json.RawMessage  `json:"source"`
		File   json.RawMessage  `json:"file"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}

	result := ContentPart{Type: raw.Type, Text: raw.Text, Media: raw.Media}
	if result.Media == nil && len(raw.Source) > 0 && string(raw.Source) != "null" {
		var source struct {
			Type       string `json:"type"`
			ID         string `json:"id"`
			Name       string `json:"name"`
			MediaType  string `json:"media_type"`
			MIMEType   string `json:"mime_type"`
			SizeBytes  int64  `json:"size_bytes"`
			Data       string `json:"data"`
			URL        string `json:"url"`
			Path       string `json:"path"`
			StorageRef string `json:"storage_ref"`
			SHA256     string `json:"sha256"`
		}
		if err := json.Unmarshal(raw.Source, &source); err != nil {
			return err
		}
		result.Media = &MediaAttachment{
			ID:         source.ID,
			Name:       source.Name,
			MIMEType:   firstNonEmpty(source.MediaType, source.MIMEType),
			Kind:       raw.Type,
			SizeBytes:  source.SizeBytes,
			SHA256:     source.SHA256,
			SourceType: source.Type,
			URL:        source.URL,
			Path:       source.Path,
			StorageRef: source.StorageRef,
			DataBase64: source.Data,
		}
	}
	if result.Media == nil && len(raw.File) > 0 && string(raw.File) != "null" {
		var file struct {
			Type       string `json:"type"`
			ID         string `json:"id"`
			Filename   string `json:"filename"`
			Name       string `json:"name"`
			MIMEType   string `json:"mime_type"`
			SizeBytes  int64  `json:"size_bytes"`
			FileData   string `json:"file_data"`
			URL        string `json:"url"`
			Path       string `json:"path"`
			SourceType string `json:"source_type"`
			StorageRef string `json:"storage_ref"`
			SHA256     string `json:"sha256"`
		}
		if err := json.Unmarshal(raw.File, &file); err != nil {
			return err
		}
		result.Media = &MediaAttachment{
			ID:         file.ID,
			Name:       firstNonEmpty(file.Filename, file.Name),
			MIMEType:   file.MIMEType,
			Kind:       raw.Type,
			SizeBytes:  file.SizeBytes,
			SHA256:     file.SHA256,
			SourceType: file.SourceType,
			URL:        file.URL,
			Path:       file.Path,
			StorageRef: file.StorageRef,
			DataBase64: file.FileData,
		}
	}
	if result.Media != nil && result.Media.Kind == "" {
		result.Media.Kind = result.Type
	}
	*p = result
	return nil
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

// MediaAttachment is durable metadata for an uploaded or referenced object.
// DataBase64 is accepted at API boundaries and cleared after persistence.
type MediaAttachment struct {
	ID         string `json:"id,omitempty" yaml:"id,omitempty"`
	Name       string `json:"name,omitempty" yaml:"name,omitempty"`
	MIMEType   string `json:"mime_type,omitempty" yaml:"mime_type,omitempty"`
	Kind       string `json:"kind,omitempty" yaml:"kind,omitempty"`
	SizeBytes  int64  `json:"size_bytes,omitempty" yaml:"size_bytes,omitempty"`
	SHA256     string `json:"sha256,omitempty" yaml:"sha256,omitempty"`
	SourceType string `json:"source_type,omitempty" yaml:"source_type,omitempty"` // path | url | base64 | stored
	Path       string `json:"path,omitempty" yaml:"path,omitempty"`
	URL        string `json:"url,omitempty" yaml:"url,omitempty"`
	StorageRef string `json:"storage_ref,omitempty" yaml:"storage_ref,omitempty"`
	DataBase64 string `json:"data_base64,omitempty" yaml:"data_base64,omitempty"`
}

// ModelProfile describes the capabilities and defaults advertised for one
// model by the global .agents/models.json registry. Optional generation
// values are pointers so "not declared" is different from an explicit zero.
type ModelProfile struct {
	ID        string `json:"id,omitempty"`
	Name      string `json:"name,omitempty"`
	Provider  string `json:"provider,omitempty"`
	Protocol  string `json:"protocol,omitempty"`
	BaseURL   string `json:"base_url,omitempty"`
	ModelsURL string `json:"models_url,omitempty"`
	APIKey    string `json:"api_key,omitempty"`

	MaxAllowedSize  int `json:"maxAllowedSize,omitempty"`
	MaxInputTokens  int `json:"maxInputTokens,omitempty"`
	MaxOutputTokens int `json:"maxOutputTokens,omitempty"`
	MaxTokens       int `json:"max_tokens,omitempty"` // Deprecated registry alias.

	Temperature       *float64         `json:"temperature,omitempty"`
	TopP              *float64         `json:"top_p,omitempty"`
	TopK              *int             `json:"top_k,omitempty"`
	RepetitionPenalty *float64         `json:"repetition_penalty,omitempty"`
	OnlyReasoning     bool             `json:"onlyReasoning,omitempty"`
	Reasoning         *ReasoningConfig `json:"reasoning,omitempty"`

	SupportsImages            *bool `json:"supportsImages,omitempty"`
	SupportsAudio             *bool `json:"supportsAudio,omitempty"`
	SupportsVideo             *bool `json:"supportsVideo,omitempty"`
	SupportsDocuments         *bool `json:"supportsDocuments,omitempty"`
	SupportsReasoning         *bool `json:"supportsReasoning,omitempty"`
	SupportsToolCall          *bool `json:"supportsToolCall,omitempty"`
	SupportsTemperature       *bool `json:"supportsTemperature,omitempty"`
	SupportsTopP              *bool `json:"supportsTopP,omitempty"`
	SupportsTopK              *bool `json:"supportsTopK,omitempty"`
	SupportsRepetitionPenalty *bool `json:"supportsRepetitionPenalty,omitempty"`
	SupportsStructuredOutput  *bool `json:"supportsStructuredOutput,omitempty"`
}

// Message represents a chat message
type Message struct {
	ID         string        `json:"id,omitempty"`
	RoundNum   int           `json:"round_num,omitempty"`
	CallID     string        `json:"call_id,omitempty"`
	TeamID     string        `json:"team_id,omitempty"`
	Role       string        `json:"role"`
	Content    string        `json:"content"`
	Parts      []ContentPart `json:"parts,omitempty"`
	ToolCalls  []ToolCall    `json:"tool_calls,omitempty"`
	ToolCallID string        `json:"tool_call_id,omitempty"`
	ToolName   string        `json:"tool_name,omitempty"`
	CreatedAt  string        `json:"created_at,omitempty"`
}

// ToolCall represents an LLM tool call request
type ToolCall struct {
	ID        string         `json:"id"`
	Name      string         `json:"name"`
	Arguments map[string]any `json:"arguments"`
}

// ChatResponse represents an LLM chat response
type ChatResponse struct {
	Text      string     `json:"text"`
	Reasoning string     `json:"reasoning,omitempty"`
	ToolCalls []ToolCall `json:"tool_calls,omitempty"`
	Usage     TokenUsage `json:"usage"`
}

// ChatChunk represents a streaming chat response chunk
type ChatChunk struct {
	Text      string `json:"text"`
	Reasoning string `json:"reasoning,omitempty"`
	Finished  bool   `json:"finished"`
}

// TokenUsage tracks token consumption
type TokenUsage struct {
	PromptTokens             int `json:"prompt_tokens"`
	CompletionTokens         int `json:"completion_tokens"`
	ReasoningTokens          int `json:"reasoning_tokens,omitempty"`
	TotalTokens              int `json:"total_tokens"`
	PromptCacheHitTokens     int `json:"prompt_cache_hit_tokens,omitempty"`
	PromptCacheMissTokens    int `json:"prompt_cache_miss_tokens,omitempty"`
	CacheReadInputTokens     int `json:"cache_read_input_tokens,omitempty"`
	CacheCreationInputTokens int `json:"cache_creation_input_tokens,omitempty"`
}
