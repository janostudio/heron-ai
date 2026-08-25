---
name: gitignore-diagnostics
description: "检查 Git 忽略规则、生成物和误忽略风险"
tools:
  - Read
  - Write
  - Glob
  - Grep
knowledge:
  - gitignore-basics
---
# Gitignore 诊断规范

固定目标：

```text
project/.env
project/.env.local
project/.pytest_cache/
project/__pycache__/
project/dist/
project/.idea/
```

必须保留：

```text
project/README.md
project/app.py
project/requirements.txt
project/.gitignore
```

规则只能作用于 `project/.gitignore`。测试必须使用嵌套项目 Git：

```bash
git -C project check-ignore -v ...
git -C project status --short --ignored
```
