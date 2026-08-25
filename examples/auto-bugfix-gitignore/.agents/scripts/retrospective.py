#!/usr/bin/env python3
"""Read-only Heron session retrospective.

It mirrors the useful part of auto_bugfix's retrospective.py: deterministic
counts first, model interpretation later. No file is modified.
"""

from __future__ import annotations

import argparse
import json
import os
from collections import Counter
from pathlib import Path


def read_jsonl(path: Path) -> list[dict]:
    if not path.exists():
        return []
    values = []
    for line in path.read_text(encoding="utf-8").splitlines():
        try:
            value = json.loads(line)
        except json.JSONDecodeError:
            continue
        if isinstance(value, dict):
            values.append(value)
    return values


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--session", default=os.environ.get("HERON_FLOW_SESSION_ID", ""))
    args = parser.parse_args()
    if not args.session:
        parser.error("--session or HERON_FLOW_SESSION_ID is required")

    root = Path(__file__).resolve().parents[2]
    path = root / ".agents/data/sessions" / args.session / "session.jsonl"
    events = read_jsonl(path)
    counts = Counter(event.get("type", "") for event in events)
    tool_events = [
        event for event in events if event.get("type", "").startswith("tool_call.")
    ]
    workspace_reads = [
        event
        for event in events
        if event.get("type") == "workspace.operation"
        and (event.get("payload") or {}).get("operation", {}).get("kind") == "read"
    ]
    result = {
        "session_id": args.session,
        "event_count": len(events),
        "event_counts": dict(sorted(counts.items())),
        "tool_event_count": len(tool_events),
        "workspace_read_count": len(workspace_reads),
        "recovery_count": counts.get("recovery.requested", 0)
        + counts.get("recovery.completed", 0),
        "failed_turns": sum(
            1
            for event in events
            if event.get("type", "").endswith(".completed")
            and (event.get("payload") or {}).get("status") == "failed"
        ),
    }
    print(json.dumps(result, ensure_ascii=False, sort_keys=True))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
