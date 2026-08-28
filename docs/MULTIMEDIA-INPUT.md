# Multimedia Input

Heron accepts structured multimedia content while keeping the execution
architecture unchanged:

```text
Flow → Team → Agent
```

Multimedia is a property of an Agent input message, not a new orchestration
layer.

## Supported transports

### HTTP

`POST /api/run`, `/api/sessions/turn`, and `/api/resume` accept either
provider-neutral content blocks:

```json
{
  "input": "请分析图片",
  "content": [
    {
      "type": "image",
      "source": {
        "type": "base64",
        "media_type": "image/png",
        "data": "..."
      }
    }
  ]
}
```

or a separate attachment list:

```json
{
  "input": "请分析附件",
  "attachments": [
    {
      "name": "diagram.png",
      "kind": "image",
      "mime_type": "image/png",
      "source_type": "base64",
      "data_base64": "..."
    }
  ]
}
```

### `stream-json`

The CLI accepts the common user message shape:

```json
{
  "type": "user",
  "message": {
    "role": "user",
    "content": [
      {"type": "text", "text": "请分析图片"},
      {
        "type": "image",
        "source": {
          "type": "base64",
          "media_type": "image/png",
          "data": "..."
        }
      }
    ]
  }
}
```

The parser also accepts OpenAI-style `image_url` blocks and file/path blocks.
The CLI keeps the existing `stream-json` result envelope.

## Durable storage and safety

Incoming base64 data is validated and stored below:

```text
.agents/data/uploads/<sha256>
```

Session events, Flow turns, and Agent checkpoints retain only media metadata
and `storage_ref`; they do not retain the large base64 body.

The default store:

- limits an attachment to 32 MiB;
- validates the MIME allowlist;
- calculates SHA-256 for deduplication and audit;
- rejects unsafe relative paths and absolute paths;
- disables remote URL downloads by default;
- resolves bytes only immediately before a Provider request.

The workspace-relative `path` source is allowed only inside the configured
workspace. Remote URLs are parsed for compatibility but require an explicitly
configured URL-enabled MediaStore.

## Provider mapping

The core uses `types.ContentPart` and `types.MediaAttachment`, so provider
wire formats do not leak into Flow/Team/Agent:

- OpenAI-compatible Chat Completions: text plus `image_url`; document/file
  parts use an inline file data URL when the model profile advertises
  document support.
- Anthropic Messages: text plus native `image` blocks; PDF documents use
  native `document` blocks.

Unsupported media is returned as an explicit error. It is never silently
dropped.

Model profiles can advertise:

```json
{
  "supportsImages": true,
  "supportsAudio": false,
  "supportsVideo": false,
  "supportsDocuments": true
}
```

## Context and compaction

Media is not inserted into ordinary prompt text and base64 bytes are not
counted as normal context characters. Context compaction preserves the
structured media reference. The Provider resolves the stored bytes only at
request construction time.

`ModelRequestStats.media_part_count` records whether a request contained
structured media parts without persisting the media body.

## Tests

From `heron-ai`:

```bash
GOCACHE=/tmp/heron-ai-go-cache go test ./...
```

Covered cases include:

- stream-json content block parsing;
- HTTP content block propagation;
- base64 persistence and SHA-256 references;
- MIME, size, and path safety checks;
- OpenAI-compatible image wire JSON;
- Anthropic image wire JSON;
- checkpoint/session-safe media metadata.
