#!/usr/bin/env python3
"""Fail when a Markdown file links to a relative path that does not exist.

Docs rot silently: a renamed file leaves a 404 that nobody clicks until a new
reader does. This check makes that a build failure instead.

Scope: relative links between repository files. It deliberately ignores

  - absolute URLs (http/https/mailto) — those rot for reasons a repo cannot see;
  - pure fragments (#section) — GitHub renders them, nothing to stat here;
  - links inside fenced code blocks, which are usually examples.

Usage:
    python3 scripts/check-docs-links.py          # from the repo root
    python3 scripts/check-docs-links.py --verbose
"""

from __future__ import annotations

import os
import re
import sys

SKIP_DIRS = {".git", "node_modules", "vendor"}
LINK = re.compile(r"\[[^\]]*\]\(([^)\s]+)\)")
FENCE = re.compile(r"```.*?```", re.S)


def iter_markdown(root: str):
    for dirpath, dirnames, filenames in os.walk(root):
        dirnames[:] = [d for d in dirnames if d not in SKIP_DIRS]
        for name in filenames:
            if name.endswith(".md"):
                yield os.path.join(dirpath, name)


def check(root: str) -> tuple[int, list[tuple[str, int, str]]]:
    """Return (links_checked, broken) where broken carries line numbers."""
    checked = 0
    broken: list[tuple[str, int, str]] = []
    for path in iter_markdown(root):
        with open(path, encoding="utf-8") as handle:
            text = handle.read()
        # Blank out fenced blocks without changing line numbering.
        text = FENCE.sub(lambda m: "\n" * m.group(0).count("\n"), text)
        for lineno, line in enumerate(text.splitlines(), start=1):
            for match in LINK.finditer(line):
                target = match.group(1)
                if target.startswith(("http://", "https://", "mailto:", "#", "/")):
                    continue
                path_only = target.split("#", 1)[0]
                if not path_only:
                    continue
                checked += 1
                resolved = os.path.normpath(os.path.join(os.path.dirname(path), path_only))
                if not os.path.exists(resolved):
                    broken.append((os.path.relpath(path, root), lineno, target))
    return checked, broken


def main() -> int:
    root = os.path.abspath(sys.argv[1] if len(sys.argv) > 1 and not sys.argv[1].startswith("-") else ".")
    checked, broken = check(root)
    verbose = "--verbose" in sys.argv

    if verbose:
        print(f"checked {checked} relative links under {root}")
    if broken:
        print(f"broken relative links: {len(broken)}", file=sys.stderr)
        for path, lineno, target in broken:
            print(f"  {path}:{lineno} -> {target}", file=sys.stderr)
        print(
            "\nFix the path, or remove the link. See docs/INDEX.md for the current doc map.",
            file=sys.stderr,
        )
        return 1
    if verbose:
        print("all relative links resolve")
    return 0


if __name__ == "__main__":
    sys.exit(main())
