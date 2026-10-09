# 发现：Skill 目录不支持符号链接

> 状态：**问题 1 已修复（2026-10-09）**；问题 2 保持现状（有意设计，不可放宽）
> 影响面：任何用软链复用 skill 目录的部署方式
> 关联：`internal/skill/loader.go`、`internal/config/definitions.go`、`internal/workspace/workspace.go`

## 修复记录（2026-10-09）

**重要更正**：本 issue 最初把"问题 1"定位到 `internal/skill/loader.go` 的 `LoadAll`，
但实测发现**该文件是死代码**——`SkillLoader` / `LoadAll` 在生产代码中从未被构造
（`grep NewSkillLoader` 只命中它自己的测试）。运行时真正的 skill 加载路径是
`internal/config/definitions.go` 的 `loadSkillDefinitions`，它经由
`fileStore.List`（`os.ReadDir` 取名字）+ `fileStore.Exists`（`os.Stat` 跟随软链）
加载，**本来就能加载软链 skill**（已写探针测试验证：软链 skill 被正常加载）。

所以对**当前运行时**而言，问题 1 实际不存在。为了消除"死代码里埋着同款坑"的隐患
（一旦未来把 `SkillLoader` 接进生产路径就会复现静默跳过），已顺手修复
`internal/skill/loader.go`：`LoadAll` 改用 `os.Stat` 跟随软链判断目录，
并新增 `TestSkillLoader_LoadAllFollowsSymlinkedSkillDir`。

**结论不变**：即便加载阶段放行，**问题 2**（工具访问越界）仍使软链 skill 在实际
使用中不可用，而问题 2 是有意设计。因此运行时的正确做法仍是"用工作区内的真实目录"。

---

## 现象

在 `auto_bugfix` 的 heron 工作区里，`.agents/skills/heron-ai-config` 是指向引擎
仓库的一个软链：

```bash
readlink auto_bugfix/heron-ai/.agents/skills/heron-ai-config
# /Users/jahweijiang/Documents/github/multirole-agent/heron-ai/builtin/skills/heron-ai-config
```

期望是"skill 只维护一份、两个仓库共用"。实际这个 skill **在运行时完全不可用**，
而且没有任何报错——加载阶段被静默跳过。

## 两层问题（各自独立）

### 问题 1：加载阶段静默跳过软链

`internal/skill/loader.go:39-55` 的 `LoadAll`：

```go
entries, err := os.ReadDir(skillsDir)
for _, entry := range entries {
    if !entry.IsDir() {      // ← 软链在这里被判为"非目录"
        continue
    }
    skill, err := l.Load(filepath.Join(skillsDir, entry.Name()))
    if err != nil {
        continue             // ← 加载失败也静默跳过
    }
    ...
}
```

`os.ReadDir` 返回的 `DirEntry` **不跟随软链**。实测（Go 直接跑）：

```
os.ReadDir + entry.IsDir()（引擎 LoadAll 的做法）:
  cnb-ops            type=d---------  entry.IsDir()=true   → 会被加载
  heron-ai-config    type=L---------  entry.IsDir()=false  → 被 continue 跳过

os.Stat（跟随软链）对照:
  heron-ai-config    IsDir()=true
  cnb-ops            IsDir()=true
```

`type` 的 `L` 是 `ModeSymlink`。所以软链形态的 skill 永远进不了 skills 列表，
且因为 `continue` 无日志，用户不会知道。

**更隐蔽的一点**：即使修好这行（改用 `os.Stat`），`l.Load` 之后的路径
仍是软链路径，问题 2 依然存在。

### 问题 2：工具读取时被判"越界"

即使 skill 被加载，agent 用 `Read`/`Grep`/`Glob` 访问软链目录仍会失败。
这有**真实运行记录**（`auto_bugfix/heron-ai/.agents/data/sessions/` 下的历史会话）：

```
tool_call.failed  Read  .agents/skills/heron-ai-config
  error: "path escapes workspace: .../.agents/skills/heron-ai-config"

tool_call.failed  Grep  .agents/skills/heron-ai-config
  error: "read .../heron-ai-config: is a directory"
```

根因在 `internal/workspace/workspace.go` 的 `ensureResolvedWithinWorkspace`：

```go
root, err := filepath.EvalSymlinks(s.root)
...
resolved, err := filepath.EvalSymlinks(candidate)
relative, err := filepath.Rel(root, filepath.Clean(resolved))
if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+sep) {
    return fmt.Errorf("%w: %s", ErrPathOutsideWorkspace, path)
}
```

它先用 `EvalSymlinks` 把两侧都解析成**真实路径**，再判断是否在工作区内。
软链指向引擎仓库（工作区之外），解析后自然落在 root 之外，于是拒绝。

**这是有意设计，不是缺陷**：`workspace_test.go` 里有对应测试
`TestServiceRejectsSymlinkOutsideWorkspace`，用软链试图读取工作区外的
`secret.txt`，断言必须报 `ErrPathOutsideWorkspace`。工作区隔离靠的就是
"软链不能逃逸"这条规则——它保护的是 agent 不能通过软链读到工作区外的文件。

所以问题 2 **不能**通过"让 workspace 跟随软链"来修，那会直接破坏隔离。

## 结论

**skill 目录在 heron 里必须是工作区内的真实目录。**

- 软链形式：加载阶段静默跳过（问题 1）
- 即便加载成功，工具访问阶段越界（问题 2，且此项不可放宽）

"一份维护、多仓共用"的诉求，用软链无法实现。

## 当前处置

保持现状（用户决定 A：软链不动），`heron-ai-config` 在 `auto_bugfix` 工作区
中**实际不可用**——这不妨碍其他真实目录的 skill 正常工作。

## 可选方向（均未实施，供后续决策）

| 方向 | 做法 | 代价 |
|---|---|---|
| 修问题 1 | `LoadAll` 改用 `os.Stat(entry)` 以跟随软链 | 只解决加载；问题 2 仍在，skill 内容加载后虽注入 prompt，但 agent 主动读 `references/` 仍会越界 |
| 复制 + 同步脚本 | 在 auto_bugfix 侧放真实目录，用脚本从引擎仓库单向同步 | 变成两份物理副本；可移植性最好 |
| 反转源 | 实体放 auto_bugfix（在工作区内），引擎仓库那份改为软链 | 工作区内为真实目录，避开两层问题；引擎仓库侧开发时软链可能不可用 |
| 引擎内建 | `builtin/skills` 随二进制嵌入（类似 `go:embed`），工作区无需放置 | 需引擎改动；skill 不再可热改 |

## 复现方式

```bash
# 1. 确认软链存在
ls -la auto_bugfix/heron-ai/.agents/skills/ | grep '^l'

# 2. 观察加载阶段跳过（Go 复现，见上文实测输出）
#    cnb-ops(真实目录) IsDir()=true，heron-ai-config(软链) IsDir()=false

# 3. 观察工具访问越界
grep -h 'heron-ai-config' auto_bugfix/heron-ai/.agents/data/sessions/*/agent.jsonl \
  | grep 'tool_call.failed'

# 4. 隔离设计有测试兜底（不要试图放宽）
#    internal/workspace/workspace_test.go: TestServiceRejectsSymlinkOutsideWorkspace
```

## 一句话

`os.ReadDir` 不跟随软链导致 skill 被静默跳过；工作区隔离有意拒绝软链指向外部
——两者叠加使"软链 skill"在 heron 中完全不可用，且必须用真实目录替代。
