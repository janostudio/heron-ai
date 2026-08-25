package member

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
