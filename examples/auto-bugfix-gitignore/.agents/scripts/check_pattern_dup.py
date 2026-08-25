#!/usr/bin/env python3
"""Small deterministic duplicate check for the example Knowledge file.

The full auto_bugfix implementation uses FTS5. This fixture keeps the same
behavioral contract—soft duplicate warning—without introducing a database.
"""

from __future__ import annotations

import argparse
import re
from pathlib import Path


def words(text: str) -> set[str]:
    return {
        word.lower()
        for word in re.findall(r"[A-Za-z0-9_.-]+|[\u4e00-\u9fff]", text)
        if len(word) > 1
    }


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--text", required=True)
    args = parser.parse_args()
    root = Path(__file__).resolve().parents[2]
    knowledge = root / ".agents/knowledge/gitignore.md"
    existing = words(knowledge.read_text(encoding="utf-8")) if knowledge.exists() else set()
    incoming = words(args.text)
    overlap = len(existing & incoming) / max(len(incoming), 1)
    if overlap >= 0.35:
        print(f"WARNING possible duplicate: overlap={overlap:.2f}")
    else:
        print(f"OK no high-similarity entry: overlap={overlap:.2f}")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
