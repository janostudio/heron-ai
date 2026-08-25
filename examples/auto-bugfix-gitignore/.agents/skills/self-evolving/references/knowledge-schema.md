# 知识文件格式规范

## fix-history.json 结构

```json
{
  "version": 1,
  "last_updated": "2026-04-13 12:00",
  "fixes": [
    {
      "issue_number": 2214,
      "title": "chat流loading中ppt提前生成",
      "priority": "P0",
      "root_cause_category": "chat-flow",
      "category_label": "聊天流/消息状态",
      "affected_modules": ["chat/hooks", "chat/adapter"],
      "modified_files": ["hooks/useAgentChat.ts"],
      "fix_summary": "添加状态检查条件",
      "fix_date": "2026-04-13"
    }
  ]
}
```

## architecture.md 更新规则

- 只追加新发现的架构信息，不修改已有内容
- 新增的技术栈/工具在表格末尾追加
- 新增的目录在结构图对应位置追加

## modules.md 更新规则

- 按模块组织，每个模块一个二级标题
- 文件列表使用表格格式：| 文件 | 大小 | 职责 |
- 新发现的文件追加到对应模块表格末尾
- "常见改动热点" 部分根据 fix-history 自动更新

## patterns.md 更新规则

- 按根因分类组织（与 learn.py 的 VALID_CATEGORIES 对应）
- 每个模式包含：触发条件、根因、修复手法、涉及文件、关联 Issue、出现次数
- 出现次数 >= 3 时标记为 [PROMOTE_CANDIDATE]
- 已晋升的标记为 [PROMOTED]，保留原文不删除

## 根因分类标准

| 分类 ID | 中文名 | 判定标准 |
|---------|--------|---------|
| chat-flow | 聊天流/消息状态 | 消息收发、流式渲染、聊天状态切换相关 |
| agent-comm | Agent 通信/Session | Session 生命周期、ACP 协议、SSE 连接相关 |
| ui-render | UI 渲染/组件交互 | 组件渲染错误、事件处理、DOM 操作相关 |
| file-upload | 文件/图片上传 | 文件选择、压缩、上传、预览相关 |
| websocket | WebSocket/实时通信 | WS 连接、消息推送、重连相关 |
| store-sync | 状态管理/Store 同步 | Zustand store 更新、组件同步、数据一致性 |
| api-request | API/网络请求 | HTTP 请求、错误处理、数据解析相关 |
| style-layout | 样式/布局 | CSS、Tailwind、响应式、动画相关 |
| other | 其他 | 不属于以上分类 |
