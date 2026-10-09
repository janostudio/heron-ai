# Workspace Configuration

Workspace 决定 agent 操作"项目文件/命令"的执行后端。默认本地（local），可配置远程 SSH 执行。

核心边界（见 `docs/generic-engine/28-remote-workspace.md`）：

- **仅"执行"远程化**：Read/Write/Bash/Grep/Glob 等 workspace 工具操作远程；引擎自身状态（`.agents/data/sessions/<id>/{flow,team,agent}.jsonl`、evidence、knowledge、state、logging）始终在本地。
- **工具分两类**：workspace 工具（操作项目，可远程）vs 引擎工具（Todo/State/Spawn/Ask/Web，操作 agent 自身，始终本地）。

## 三级继承

workspace 配置可挂在 **flow / team / agent** 任意一级，继承优先级 **agent > team > flow**，都不配置则默认 `local`。

## 配置格式

```yaml
workspace:
  type: local            # local | ssh（docker 等后续扩展）
  # local 无额外参数

  # type: ssh 时：
  ssh:
    host: "192.168.1.10"
    port: 22
    user: "deploy"
    key_path: "~/.ssh/id_ed25519"   # 或 password；都不给则尝试 ssh-agent
    root: "/home/deploy/project"     # 远程 workspace 根，缺省 /root/workspace
    insecure: false                  # 默认 false 走 known_hosts 校验；true 跳过校验（仅内网/测试）
```

## 字段说明

| 字段 | 类型 | 说明 |
|------|------|------|
| `type` | string | `local`（默认）/ `ssh` |
| `ssh.host` | string | 远程主机（必填） |
| `ssh.port` | int | 端口，缺省 22 |
| `ssh.user` | string | 登录用户，缺省 root |
| `ssh.key_path` | string | 私钥路径（`~` 会展开）；优先于 password |
| `ssh.password` | string | 密码登录 |
| `ssh.root` | string | 远程 workspace 根，缺省 `/root/workspace` |
| `ssh.insecure` | bool | 跳过 host key 校验，默认 false（走 `~/.ssh/known_hosts`） |

## 远程场景的行为差异

| 能力 | local | ssh |
|------|-------|-----|
| Read / Write / Glob | 本地文件 | SFTP |
| Bash | 本地 `exec` | 远程 `ssh exec`，cwd=远程 root，shell `/bin/sh` |
| Grep | 本地 WalkDir | 远程 `grep` 命令 |
| CodeNav | 本地 codels 索引 | **不可用**，报错引导改用 Grep/Glob |
| Todo / State / Spawn / Ask / Web | 本地 | 本地（引擎工具不受影响） |

## 示例

### 本地（默认）

```yaml
workspace:
  type: local
```

或不写 workspace 字段（等价）。

### 远程 SSH

```yaml
# flow 级配置（所有 team/agent 共享同一远程 workspace）
workspace:
  type: ssh
  ssh:
    host: "build-server.internal"
    user: "deploy"
    key_path: "~/.ssh/id_ed25519"
    root: "/workspace/app"
```

## 安全

- 默认走 `~/.ssh/known_hosts` 校验 host key，防止中间人攻击。
- 文件不存在或无法解析时会报错，提示配置 known_hosts 或显式 `insecure: true`。
- `insecure: true` 仅建议内网/测试环境使用。
