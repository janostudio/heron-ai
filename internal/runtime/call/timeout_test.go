package call

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestWithTimeout(t *testing.T) {
	tests := []struct {
		name        string
		callTimeout string
		specTimeout string
		wantErr     bool
		wantTimeout time.Duration
	}{
		{"call timeout wins", "2s", "10s", false, 2 * time.Second},
		{"fallback to spec", "", "5s", false, 5 * time.Second},
		{"whitespace call timeout", "  3s  ", "10s", false, 3 * time.Second},
		// No configured timeout must still yield a deadline (the default), so a
		// command/webhook cannot run unbounded.
		{"both empty applies default", "", "", false, DefaultCallTimeout},
		{"invalid duration", "not-a-duration", "", true, 0},
		{"zero duration", "0s", "", true, 0},
		{"negative duration", "-1s", "", true, 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			gotCtx, cancel, err := withTimeout(ctx, tt.callTimeout, tt.specTimeout)

			if tt.wantErr {
				require.Error(t, err)
				assert.Nil(t, gotCtx)
				assert.Nil(t, cancel)
				return
			}

			require.NoError(t, err)
			require.NotNil(t, gotCtx)
			require.NotNil(t, cancel)
			defer cancel()

			deadline, ok := gotCtx.Deadline()
			assert.True(t, ok, "expected a deadline")
			assert.InDelta(t, tt.wantTimeout, time.Until(deadline), float64(500*time.Millisecond))
		})
	}
}

// An existing caller deadline is respected rather than replaced.
func TestWithTimeoutRespectsCallerDeadline(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	gotCtx, gotCancel, err := withTimeout(ctx, "", "")
	require.NoError(t, err)
	defer gotCancel()
	// Same context object: no extra layer added, the caller's deadline stands.
	assert.Equal(t, ctx, gotCtx)
}
