package config

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLoadGlobalEnvResolvesRelativePath(t *testing.T) {
	root := t.TempDir()
	writeSettings(t, root, `{
  "env": {"AUTO": "..", "ABS": "/already/absolute", "PLAIN": "value", "EMPTY": ""}
}`)

	env := NewConfigLoader(root).LoadGlobalEnv()
	require.Len(t, env, 4)

	// 相对路径相对 baseDir 展开为绝对路径（root/.. 的绝对形式）。
	assert.Equal(t, filepath.Dir(filepath.Clean(root)), filepath.Clean(env["AUTO"]))
	assert.True(t, filepath.IsAbs(env["AUTO"]), "AUTO should be absolute: %q", env["AUTO"])

	// 绝对路径原样保留。
	assert.Equal(t, "/already/absolute", env["ABS"])

	// 注意：PLAIN/EMPTY 这类非路径值也会按路径解析——本段定位就是"路径型
	// 工作区锚点"，因此 "value" 会被解析为 baseDir/value。这里只验证它们
	// 被处理而非丢弃，语义在 LoadGlobalEnv 的文档里写清。
	assert.Equal(t, filepath.Join(root, "value"), env["PLAIN"])
	assert.Equal(t, "", env["EMPTY"])
}

func TestLoadGlobalEnvMissingSectionReturnsNil(t *testing.T) {
	root := t.TempDir()
	writeSettings(t, root, `{"logging": {"level": "info"}}`)
	assert.Nil(t, NewConfigLoader(root).LoadGlobalEnv())
}

func TestLoadGlobalEnvMissingFileReturnsNil(t *testing.T) {
	assert.Nil(t, NewConfigLoader(t.TempDir()).LoadGlobalEnv())
}

func TestLoadGlobalEnvMalformedJSONReturnsNil(t *testing.T) {
	root := t.TempDir()
	writeSettings(t, root, `{"env": {not json`)
	assert.Nil(t, NewConfigLoader(root).LoadGlobalEnv())
}
