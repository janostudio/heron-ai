#!/usr/bin/env python3
"""Minimal local Skill evolution helper.

The production auto_bugfix version clusters fix-history records and generates
skills. This fixture keeps the same extension point with a safe report-only
implementation: it reports which business skills are used and never rewrites
configuration automatically.
"""

from __future__ import annotations

import json
from pathlib import Path


def main() -> int:
    root = Path(__file__).resolve().parents[2]
    skills = []
    for path in sorted((root / ".agents/skills").glob("*/SKILL.md")):
        text = path.read_text(encoding="utf-8")
        name = next(
            (line.split(":", 1)[1].strip() for line in text.splitlines() if line.startswith("name:")),
            path.parent.name,
        )
        skills.append(name)
    print(json.dumps({"status": "report_only", "skills": skills}, ensure_ascii=False))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
