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
		name         string
		callTimeout  string
		specTimeout  string
		wantErr      bool
		wantTimeout  time.Duration
		wantNoCancel bool // true when no timeout is applied (cancel is no-op)
	}{
		{"call timeout wins", "2s", "10s", false, 2 * time.Second, false},
		{"fallback to spec", "", "5s", false, 5 * time.Second, false},
		{"whitespace call timeout", "  3s  ", "10s", false, 3 * time.Second, false},
		{"both empty returns ctx", "", "", false, 0, true},
		{"invalid duration", "not-a-duration", "", true, 0, false},
		{"zero duration", "0s", "", true, 0, false},
		{"negative duration", "-1s", "", true, 0, false},
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

			if tt.wantNoCancel {
				// Same ctx returned, no deadline set.
				assert.Equal(t, ctx, gotCtx)
				_, ok := gotCtx.Deadline()
				assert.False(t, ok)
				return
			}

			deadline, ok := gotCtx.Deadline()
			assert.True(t, ok, "expected a deadline")
			assert.InDelta(t, tt.wantTimeout, time.Until(deadline), float64(500*time.Millisecond))
		})
	}
}
