package media

import (
	"context"
	"encoding/base64"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/heron-ai/heron-engine/internal/storage"
	"github.com/heron-ai/heron-engine/pkg/types"
)

func TestFileStoreStoresBase64ContentAddressedAndClearsPayload(t *testing.T) {
	files := storage.NewFileStore(t.TempDir())
	store := NewFileStore(files, Limits{MaxBytes: 1024})

	attachment, err := store.Store(context.Background(), types.MediaAttachment{
		Name:       "pixel.png",
		Kind:       "image",
		MIMEType:   "image/png",
		SourceType: "base64",
		DataBase64: base64.StdEncoding.EncodeToString([]byte("png-fixture")),
	})
	require.NoError(t, err)
	require.Equal(t, "stored", attachment.SourceType)
	require.Empty(t, attachment.DataBase64)
	require.Equal(t, int64(len("png-fixture")), attachment.SizeBytes)
	require.Equal(t, filepath.Join(".agents", "data", "uploads", attachment.SHA256), filepath.FromSlash(attachment.StorageRef))

	data, err := store.ResolveMedia(context.Background(), attachment)
	require.NoError(t, err)
	require.Equal(t, []byte("png-fixture"), data)
}

func TestFileStoreRejectsUnsafeReferenceAndOversize(t *testing.T) {
	store := NewFileStore(storage.NewFileStore(t.TempDir()), Limits{MaxBytes: 3})
	_, err := store.Store(context.Background(), types.MediaAttachment{
		MIMEType:   "text/plain",
		SourceType: "base64",
		DataBase64: base64.StdEncoding.EncodeToString([]byte("toolong")),
	})
	require.Error(t, err)

	_, err = store.ResolveMedia(context.Background(), types.MediaAttachment{
		SourceType: "stored", StorageRef: "../outside",
	})
	require.Error(t, err)
}
