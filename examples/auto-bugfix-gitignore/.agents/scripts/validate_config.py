#!/usr/bin/env python3
"""Validate the example's visible configuration contract.

The Go loader is authoritative for YAML/frontmatter parsing. This script is a
small human-facing preflight that checks the files a real auto-bugfix package
would expect before starting a Flow.
"""

from __future__ import annotations

import argparse
import json
from pathlib import Path


REQUIRED = [
    ".agents/flows/auto_bugfix.yml",
    ".agents/settings.json",
    ".agents/models.json",
    ".agents/teams/default_team.yml",
    ".agents/teams/diagnose_team.yml",
    ".agents/teams/challenge_team.yml",
    ".agents/teams/fix_team.yml",
    ".agents/teams/test_team.yml",
    ".agents/teams/review_team.yml",
    ".agents/teams/knowledge_team.yml",
    ".agents/teams/audit_team.yml",
]


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--root", default=".")
    args = parser.parse_args()
    root = Path(args.root).resolve()

    missing = [path for path in REQUIRED if not (root / path).exists()]
    if missing:
        print(json.dumps({"status": "failed", "missing": missing}, ensure_ascii=False))
        return 1

    models = json.loads((root / ".agents/models.json").read_text(encoding="utf-8"))
    if not models.get("models"):
        print(json.dumps({"status": "failed", "error": "models.json has no models"}))
        return 1

    agents = sorted((root / ".agents/agents").glob("*.md"))
    skills = sorted((root / ".agents/skills").glob("*/SKILL.md"))
    scripts = sorted((root / ".agents/scripts").glob("*"))
    result = {
        "status": "passed",
        "agents": len(agents),
        "skills": len(skills),
        "scripts": len([path for path in scripts if path.is_file()]),
        "teams": 8,
        "flow": ".agents/flows/auto_bugfix.yml",
    }
    print(json.dumps(result, ensure_ascii=False, sort_keys=True))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
