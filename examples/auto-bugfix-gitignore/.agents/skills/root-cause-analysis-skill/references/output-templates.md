# 输出格式模板

## root_cause JSON Schema（fix 模式）

```json
{
  "description": "Bug 根因，一句话",
  "confidence": "高/中/低",
  "action_type": "fix/investigate",
  "fix_direction": "最小修复方向（改哪个文件的哪个函数，一句话）",
  "affected_files": ["涉及的文件路径列表"],
  "acceptance_criteria": [
    {
      "id": "AC-B1",
      "type": "bug_behavior",
      "given": "前置条件",
      "when": "触发动作",
      "then": "当前错误结果（可观测）"
    },
    {
      "id": "AC-E1",
      "type": "expected_behavior",
      "given": "修复后的前置条件",
      "when": "修复后的触发动作",
      "then": "修复后应达到的正确结果（可观测）"
    },
    {
      "id": "AC-R1",
      "type": "regression_safety",
      "given": "不受影响的前置条件",
      "when": "不受影响的操作",
      "then": "SHALL CONTINUE TO: 现有正确行为必须保持"
    }
  ],
  "system_config_excluded": "是/否/不适用"
}
```

## root_cause JSON Schema（investigate 模式）

```json
{
  "description": "根因/需求描述（一段话）",
  "confidence": "低",
  "action_type": "investigate",
  "fix_direction": "",
  "affected_files": [],
  "investigation": {
    "local_findings": ["从代码中确认的事实"],
    "external_references": [],
    "root_cause_category": "backend_only|cross_repo|insufficient_info|needs_design|external_blocker|unclear_requirements",
    "blockers": [{"type": "...", "detail": "...", "owner": "..."}],
    "recommended_actions": [{"type": "...", "detail": "..."}]
  }
}
```

## investigation_log 写入模板

analyze 节点需要在 `complete` 之前写入 investigation_log：

```bash
: "${DAG_SESSION_ID:?调用方必须显式提供父 DAG_SESSION_ID}"
export DAG_SESSION_ID
DAG_SCHEDULER=(python3 .codebuddy/scripts/dag_scheduler.py --session-id "$DAG_SESSION_ID")
if [[ -n "${DAG_CONTEXT_SCOPE:-}" ]]; then
  DAG_SCHEDULER+=(--context-scope "$DAG_CONTEXT_SCOPE")
fi

EXISTING=$("${DAG_SCHEDULER[@]}" get investigation_log --json 2>/dev/null || echo "{}")
ROUND=$(echo "$EXISTING" | python3 -c "import sys,json; d=json.load(sys.stdin); print(len(d.get('hypotheses',[])) + 1)")

"${DAG_SCHEDULER[@]}" set investigation_log '{
  "hypotheses": [
    {
      "round": '$ROUND',
      "hypothesis": "<根因假设>",
      "evidence_for": ["<证据1>", "<证据2>"],
      "evidence_against": [],
      "challenge_verdict": "pending",
      "challenge_notes": "",
      "source": "analyze(round='$ROUND')"
    }
  ],
  "final_root_cause": {
    "description": "<根因描述>",
    "confidence": "<置信度>",
    "decision": {"options": ["<A>", "<B>"], "chosen": "<A>", "reason": "<原因>"},
    "fix_direction": "<修复方向>",
    "affected_files": ["<文件列表>"],
    "source": "analyze"
  }
}' --by root-cause-analyst
```
