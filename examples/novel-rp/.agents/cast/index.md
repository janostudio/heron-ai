# 角色卡索引

角色 = 数据，不是 agent。角色卡存放在 `.agents/cast/*.md`，每张卡是一个角色的完整档案；
agent 定义只有 director / role-actor / narrator 三个，100 个角色也只是 100 张卡。

导演（director）用 Glob 列出卡片、Read 读取内容，按本轮剧情需要挑选上场角色并组装进
ScenePlan.cast；剧情需要新角色时，导演现场起草新卡，用 Write 写入 `<id>.md`。

## 卡片格式

每张卡的正文就是角色档案，按以下字段组织：

| 字段 | 含义 |
|---|---|
| name | 角色名（可含外文拼写） |
| role | 定位（主角 / 反派 / 导师 / 同伴等） |
| goal | 当前目标或动机 |
| backstory | 背景经历 |
| speech_style | 说话方式与语言习惯 |
| current_state | 当前状态（位置、伤势、情绪、持有物等，随剧情更新） |
| relationships | 与其他角色的关系 |

## 角色列表

| 文件 | name | role |
|---|---|---|
| alin.md | 阿林 | 年轻冒险者（主角） |
| morwen.md | 莫尔文 | 堕落法师（反派） |
| selene.md | 赛琳 | 精灵导师 |
| finn.md | 芬恩 | 盗贼同伴 |
