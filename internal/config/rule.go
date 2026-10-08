package config

import (
	"strings"

	"github.com/adrg/frontmatter"

	"github.com/heron-ai/heron-engine/pkg/types"
)

// loadRuleMeta 只解析 rule 的 frontmatter 元数据（id/type/scope/priority/paths），
// 不读取正文（Content 留空），正文延迟到渲染时按需加载。
func (l *ConfigLoader) loadRuleMeta(path string) (*types.RuleItem, error) {
	data, err := l.fileStore.Read(path)
	if err != nil {
		return nil, err
	}

	var rule types.RuleItem
	if _, err := frontmatter.Parse(strings.NewReader(string(data)), &rule); err != nil {
		return nil, err
	}
	rule.Path = path

	return &rule, nil
}
