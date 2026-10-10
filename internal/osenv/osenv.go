// Package osenv 提供子进程环境变量的合并工具。
//
// 引擎需要在多处起子进程（command call、Bash 工具、MCP stdio server、
// hooks、codenav），它们共享同一套"全局环境变量注入"语义：由
// settings.json 的 env 段声明的键值，以低于显式配置的优先级并入进程环境。
// 把这段语义放在一个中立包里，避免每个调用点各写一份。
package osenv

import (
	"os"
	"sort"
	"strings"
)

// Merge 以 base 为底、用 overrides 覆盖同名项，返回排序后的 "KEY=VALUE" 列表。
//
// base 为 nil/空时以 os.Environ() 为底。overrides 里的同名键胜出——这是注入
// 全局环境变量的通用语义：显式配置的值优先于进程既有环境，避免"配了却不生效"。
//
// 不修改入参，返回新切片。
func Merge(base []string, overrides map[string]string) []string {
	if len(base) == 0 {
		base = os.Environ()
	}
	if len(overrides) == 0 {
		// 无覆盖时原样返回 base 的副本，保持调用方拿到的切片不被共享。
		out := make([]string, len(base))
		copy(out, base)
		return out
	}

	merged := make(map[string]string, len(base)+len(overrides))
	for _, entry := range base {
		if i := strings.IndexByte(entry, '='); i > 0 {
			merged[entry[:i]] = entry[i+1:]
		}
	}
	for name, value := range overrides {
		merged[name] = value
	}

	names := make([]string, 0, len(merged))
	for name := range merged {
		names = append(names, name)
	}
	sort.Strings(names)

	out := make([]string, 0, len(names))
	for _, name := range names {
		out = append(out, name+"="+merged[name])
	}
	return out
}
