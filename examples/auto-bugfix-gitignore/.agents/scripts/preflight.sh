#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
cd "$ROOT"

required=(
  ".agents/flows/auto_bugfix.yml"
  ".agents/teams/default_team.yml"
  ".agents/teams/diagnose_team.yml"
  ".agents/teams/challenge_team.yml"
  ".agents/teams/fix_team.yml"
  ".agents/teams/test_team.yml"
  ".agents/teams/review_team.yml"
  ".agents/teams/knowledge_team.yml"
  ".agents/agents/default-coordinator.md"
  ".agents/agents/audit-agent.md"
  ".agents/skills/gitignore-diagnostics/SKILL.md"
  ".agents/skills/session-observation/SKILL.md"
  ".agents/scripts/validate_config.py"
  ".agents/scripts/retrospective.py"
  ".agents/scripts/check_pattern_dup.py"
  ".agents/scripts/skill_evolve.py"
  "project/.gitignore"
)

for path in "${required[@]}"; do
  if [[ ! -e "$path" ]]; then
    echo "MISSING $path" >&2
    exit 1
  fi
done

if [[ ! -d project/.git ]]; then
  echo "project/.git is missing; run setup-fixture.sh first" >&2
  exit 1
fi

echo "preflight: ok"
echo "workspace: $ROOT"
echo "flow: .agents/flows/auto_bugfix.yml"
