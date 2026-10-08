package config

import (
	"strings"

	"github.com/adrg/frontmatter"

	"github.com/heron-ai/heron-engine/internal/storage"
	"github.com/heron-ai/heron-engine/pkg/types"
)

// parseRule 是规则文件解析的唯一实现：读取文件、剥离 frontmatter，
// 返回元数据与正文。启动时的元数据索引（loadRuleMeta）与渲染时的正文
// 按需加载（LoadRuleBody）都走这里，避免同一份解析在两处各写一遍。
func parseRule(fs storage.FileStore, path string) (types.RuleItem, string, error) {
	data, err := fs.Read(path)
	if err != nil {
		return types.RuleItem{}, "", err
	}

	var rule types.RuleItem
	body, err := frontmatter.Parse(strings.NewReader(string(data)), &rule)
	if err != nil {
		return types.RuleItem{}, "", err
	}

	return rule, string(body), nil
}

// loadRuleMeta 只解析 rule 的 frontmatter 元数据（id/type/scope/priority/paths），
// 不读取正文（Content 留空），正文延迟到渲染时按需加载。
func (l *ConfigLoader) loadRuleMeta(path string) (*types.RuleItem, error) {
	rule, _, err := parseRule(l.fileStore, path)
	if err != nil {
		return nil, err
	}
	rule.Path = path

	return &rule, nil
}

// LoadRuleBody 读取规则文件并返回剥离 frontmatter 后的正文。
// 它是规则正文解析的对外入口：调用方（app 侧的 rule loader）只负责在渲染时
// 决定读哪个文件，解析本身不在这里重复实现。
func LoadRuleBody(fs storage.FileStore, path string) (string, error) {
	_, body, err := parseRule(fs, path)
	return body, err
}
