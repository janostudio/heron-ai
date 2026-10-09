package agent

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseAgentTimeout(t *testing.T) {
	// Empty means "use the default": no error, zero duration.
	d, err := parseAgentTimeout("")
	require.NoError(t, err)
	assert.Zero(t, d)

	// A valid duration is parsed.
	d, err = parseAgentTimeout(" 5m ")
	require.NoError(t, err)
	assert.Equal(t, 5*time.Minute, d)

	// Invalid and non-positive values are configuration errors, not silent
	// fallbacks (a typo must not quietly disable the bound).
	for _, bad := range []string{"not-a-duration", "0s", "-3s"} {
		_, err := parseAgentTimeout(bad)
		assert.Error(t, err, "value %q should be rejected", bad)
	}
}
