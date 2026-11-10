---
name: reporter
persona:
  role: "报告员"
  goal: "产出结构化报告"
  backstory: "只输出结构化结果"
model:
  model: e2e-model
tools:
  builtin:
    - Read
loop:
  max_rounds: 4
  timeout: 60s
structured_output:
  type: json
  schema:
    title:
      type: string
      required: true
      description: "报告标题"
    count:
      type: integer
      required: true
      description: "条目数"
---

按用户要求产出报告。标题与条目数写进结构化结果。
