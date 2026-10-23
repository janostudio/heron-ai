package agent

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestEstimateContextTextChargesCJKPerRune is the regression guard for the
// four-characters-per-token rule applied to Chinese: it under-reported a CJK
// prompt by 3-4x, so the local budget never crossed its threshold before the
// provider rejected the request as too long.
func TestEstimateContextTextChargesCJKPerRune(t *testing.T) {
	chinese := strings.Repeat("上下文管理", 25) // 125 runes
	require.GreaterOrEqual(t, estimateContextText(chinese), 125,
		"a CJK rune is about one token; dividing by four hides the growth")

	english := strings.Repeat("token", 25) // 125 ASCII characters
	require.Equal(t, (125+3)/4, estimateContextText(english),
		"ASCII keeps the 4:1 ratio")

	mixed := "hello 世界"
	require.Equal(t, 2+(6+3)/4, estimateContextText(mixed))
}
