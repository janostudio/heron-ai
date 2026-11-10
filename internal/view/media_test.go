package view

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/heron-ai/heron-engine/internal/storage"
)

// newMediaHandler builds a handler over a temp workspace holding one real PNG.
func newMediaHandler(t *testing.T) (*Handler, string) {
	t.Helper()
	root := t.TempDir()
	uploads := filepath.Join(root, ".agents", "data", "uploads")
	if err := os.MkdirAll(uploads, 0o755); err != nil {
		t.Fatalf("mkdir uploads: %v", err)
	}
	name := strings.Repeat("a", 64)
	// Smallest valid PNG header plus payload: enough for DetectContentType.
	png := []byte{0x89, 'P', 'N', 'G', 0x0d, 0x0a, 0x1a, 0x0a, 0, 0, 0, 0, 'I', 'H', 'D', 'R'}
	if err := os.WriteFile(filepath.Join(uploads, name), png, 0o600); err != nil {
		t.Fatalf("write upload: %v", err)
	}
	text := strings.Repeat("b", 64)
	if err := os.WriteFile(filepath.Join(uploads, text), []byte("just text"), 0o600); err != nil {
		t.Fatalf("write text upload: %v", err)
	}
	handler := NewHandler()
	handler.SetFileStore(storage.NewFileStore(root))
	return handler, name
}

func TestHandleMediaServesStoredAttachment(t *testing.T) {
	handler, name := newMediaHandler(t)
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/media?ref="+mediaRefPrefix+name, nil)
	handler.HandleMedia(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body=%s)", rr.Code, strings.TrimSpace(rr.Body.String()))
	}
	if ct := rr.Header().Get("Content-Type"); !strings.HasPrefix(ct, "image/") {
		t.Fatalf("Content-Type = %q, want an image type", ct)
	}
	if rr.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Fatal("missing nosniff: an uploaded byte stream must not be sniffed into a document")
	}
}

func TestHandleMediaRejectsEverythingElse(t *testing.T) {
	handler, name := newMediaHandler(t)
	cases := []struct {
		name string
		ref  string
		want int
	}{
		{name: "missing ref", ref: "", want: http.StatusBadRequest},
		{name: "path traversal", ref: mediaRefPrefix + "../../../etc/passwd", want: http.StatusBadRequest},
		{name: "outside uploads", ref: ".agents/data/sessions/x/flow.jsonl", want: http.StatusBadRequest},
		{name: "not content addressed", ref: mediaRefPrefix + "not-a-hash", want: http.StatusBadRequest},
		{name: "unknown attachment", ref: mediaRefPrefix + strings.Repeat("c", 64), want: http.StatusNotFound},
		{name: "not media", ref: mediaRefPrefix + strings.Repeat("b", 64), want: http.StatusUnsupportedMediaType},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rr := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodGet, "/api/media?ref="+tc.ref, nil)
			handler.HandleMedia(rr, req)
			if rr.Code != tc.want {
				t.Fatalf("status = %d, want %d (body=%s)", rr.Code, tc.want, strings.TrimSpace(rr.Body.String()))
			}
		})
	}
	_ = name
}
