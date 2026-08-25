#!/usr/bin/env python3
"""Read-only summary of one Heron FlowSession."""

from __future__ import annotations

import argparse
import json
import os
from collections import Counter
from pathlib import Path


def read_jsonl(path: Path) -> list[dict]:
    if not path.exists():
        return []
    values: list[dict] = []
    for line in path.read_text(encoding="utf-8").splitlines():
        if not line.strip():
            continue
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
    session_dir = root / ".agents" / "data" / "sessions" / args.session
    events = read_jsonl(session_dir / "session.jsonl")
    evidence = read_jsonl(session_dir / "evidence.jsonl")

    event_counts = Counter(event.get("type", "") for event in events)
    teams = sorted({event.get("team_id", "") for event in events if event.get("team_id")})
    member_types = sorted(
        {event.get("member_type", "") for event in events if event.get("member_type")}
    )

    records = []
    for item in evidence:
        record = item.get("record", item)
        if isinstance(record, dict):
            records.append(
                {
                    "name": record.get("name", ""),
                    "kind": record.get("kind", ""),
                    "status": record.get("status", ""),
                }
            )

    result = {
        "session_id": args.session,
        "session_file": str(session_dir / "session.jsonl"),
        "evidence_file": str(session_dir / "evidence.jsonl"),
        "event_count": len(events),
        "evidence_count": len(evidence),
        "event_counts": dict(sorted(event_counts.items())),
        "teams_seen": teams,
        "member_types_seen": member_types,
        "records": records,
        "recovery_events": event_counts.get("recovery.requested", 0)
        + event_counts.get("recovery.completed", 0),
    }
    print(json.dumps(result, ensure_ascii=False, sort_keys=True))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
