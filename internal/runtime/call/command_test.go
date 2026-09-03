package call

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCommandPassedRecognizesPublishedBusinessFailure(t *testing.T) {
	require.True(t, commandPassed(nil, 0, "RESULT passed\n", ""))
	require.False(t, commandPassed(nil, 0, "RESULT failed\n", ""))
	require.False(t, commandPassed(errors.New("exit"), 1, "", ""))
}

func TestCommandPassedTableDriven(t *testing.T) {
	tests := []struct {
		name     string
		err      error
		exitCode int
		stdout   string
		stderr   string
		want     bool
	}{
		{"clean exit", nil, 0, "ok\n", "", true},
		{"run error", errors.New("boom"), 0, "", "", false},
		{"non-zero exit", nil, 1, "out", "err", false},
		{"negative exit", nil, -1, "", "", false},
		{"result failed in stdout", nil, 0, "RESULT FAILED\n", "", false},
		{"result failed in stderr", nil, 0, "", "result failed", false},
		{"status=failed", nil, 0, "", "status=failed", false},
		{"case insensitive result failed", nil, 0, "", "Result Failed", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, commandPassed(tt.err, tt.exitCode, tt.stdout, tt.stderr))
		})
	}
}
