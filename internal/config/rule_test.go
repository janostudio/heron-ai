package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/heron-ai/heron-engine/internal/storage"
)

// writeRule 在临时目录里写一个规则文件，返回可供 LoadRuleBody 使用的 FileStore 与相对路径。
func writeRule(t *testing.T, name, content string) (storage.FileStore, string) {
	t.Helper()
	root := t.TempDir()
	path := filepath.Join(".agents", "rules", name)
	full := filepath.Join(root, path)
	require.NoError(t, os.MkdirAll(filepath.Dir(full), 0755))
	require.NoError(t, os.WriteFile(full, []byte(content), 0644))
	return storage.NewFileStore(root), path
}

func TestLoadRuleBody(t *testing.T) {
	fs, path := writeRule(t, "with-fm.md", `---
id: r1
type: soft
scope:
  type: flow
priority: 10
paths: ["src/**"]
---
# Rule body

second line
`)
	body, err := LoadRuleBody(fs, path)
	require.NoError(t, err)
	require.Equal(t, "# Rule body\n\nsecond line\n", body)
	require.NotContains(t, body, "---")
	require.NotContains(t, body, "id: r1")
}

func TestLoadRuleBodyWithoutFrontmatter(t *testing.T) {
	// 没有 frontmatter 时 frontmatter.Parse 把整个文件当正文返回，
	// 这是既有行为（规则文件允许不带 frontmatter）。
	fs, path := writeRule(t, "plain.md", "# Just a body\n")
	body, err := LoadRuleBody(fs, path)
	require.NoError(t, err)
	require.Equal(t, "# Just a body\n", body)
}

func TestLoadRuleBodyEmptyFile(t *testing.T) {
	fs, path := writeRule(t, "empty.md", "")
	body, err := LoadRuleBody(fs, path)
	require.NoError(t, err)
	require.Equal(t, "", body)
}

func TestLoadRuleBodyEmptyFrontmatterOnly(t *testing.T) {
	fs, path := writeRule(t, "fm-only.md", "---\nid: r2\n---\n")
	body, err := LoadRuleBody(fs, path)
	require.NoError(t, err)
	require.Equal(t, "", body)
}

func TestLoadRuleBodyInvalidFrontmatter(t *testing.T) {
	fs, path := writeRule(t, "bad.md", "---\nid: [unclosed\n---\nbody\n")
	body, err := LoadRuleBody(fs, path)
	require.Error(t, err)
	require.Equal(t, "", body)
}

func TestLoadRuleBodyMissingFile(t *testing.T) {
	fs := storage.NewFileStore(t.TempDir())
	body, err := LoadRuleBody(fs, filepath.Join(".agents", "rules", "nope.md"))
	require.Error(t, err)
	require.Equal(t, "", body)
}

// TestLoadRuleMetaStillParses 锁定把两处解析收敛到 parseRule 之后，
// loadRuleMeta 的元数据行为不变（Path 被回填，Content 仍留空）。
func TestLoadRuleMetaStillParses(t *testing.T) {
	root := t.TempDir()
	rel := filepath.Join(".agents", "rules", "r.md")
	full := filepath.Join(root, rel)
	require.NoError(t, os.MkdirAll(filepath.Dir(full), 0755))
	require.NoError(t, os.WriteFile(full, []byte("---\nid: r3\npriority: 7\n---\ncontent here\n"), 0644))

	loader := NewConfigLoader(root)
	rule, err := loader.loadRuleMeta(rel)
	require.NoError(t, err)
	require.Equal(t, "r3", rule.ID)
	require.Equal(t, 7, rule.Priority)
	require.Equal(t, rel, rule.Path)
	require.Equal(t, "", rule.Content)
}
