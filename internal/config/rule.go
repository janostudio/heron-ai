package config

import (
	"fmt"
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

// RulePathConfigWarning reports the paths declarations that will never match,
// or will match something the author did not mean.
//
// The patterns are compared against workspace-relative paths, so an absolute one
// can never hit, ".." climbs out of the place the comparison happens in, and a
// "**" in the middle reads as a glob that Go's matcher cannot express. Each is a
// rule that silently never applies — the failure mode this feature has to avoid
// most.
func RulePathConfigWarning(rule types.RuleItem) string {
	for _, pattern := range rule.Paths {
		trimmed := strings.TrimSpace(pattern)
		switch {
		case trimmed == "":
			return fmt.Sprintf("rule %q declares an empty path pattern", rule.ID)
		case strings.HasPrefix(trimmed, "/"):
			return fmt.Sprintf("rule %q: pattern %q is absolute; patterns are workspace-relative", rule.ID, pattern)
		case strings.Contains(strings.TrimPrefix(strings.TrimPrefix(trimmed, "../"), "../"), ".."):
			// A leading ../ is how a configuration names a directory that lives
			// next to the workspace (docs/, knowledge/ in the repository around
			// it). A ".." anywhere else climbs out mid-path, which no touched
			// path will ever equal.
			return fmt.Sprintf("rule %q: pattern %q climbs out of the workspace", rule.ID, pattern)
		case strings.Contains(trimmed, "**") && !strings.HasSuffix(trimmed, "/**"):
			return fmt.Sprintf("rule %q: pattern %q uses ** somewhere other than the end; only a trailing /** is supported", rule.ID, pattern)
		}
	}
	return ""
}

// LoadRuleBody 读取规则文件并返回剥离 frontmatter 后的正文。
// 它是规则正文解析的对外入口：调用方（app 侧的 rule loader）只负责在渲染时
// 决定读哪个文件，解析本身不在这里重复实现。
func LoadRuleBody(fs storage.FileStore, path string) (string, error) {
	_, body, err := parseRule(fs, path)
	return body, err
}
