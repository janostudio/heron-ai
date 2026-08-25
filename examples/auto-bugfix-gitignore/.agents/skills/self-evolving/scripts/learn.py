#!/usr/bin/env python3
"""
知识提取脚本 - 自我进化 Skill

将每次 Bug 修复的信息记录到 knowledge/fix-history.json，
并检查是否有重复模式需要晋升为规则。
支持基于 status 的归档（思路 B：14 天 + 终态双条件）和知识更新（supersede）。

Status 字段（v3 schema）：
    active        默认；需要继续跟踪/未修复
    fixed         已修复（有 PR / 真实代码改动）
    investigate   仅调研，不会自行修复
    superseded    被新方案替代
    archived      手动归档

归档策略：年龄 ≥ARCHIVE_DAYS 天 *且* status ∈ {fixed, superseded, archived}
        active / investigate 永不自动归档（防丢失活跃信息）

用法:
    # 新增记录
    python3 learn.py \
        --issue 2214 \
        --title "chat流loading中ppt提前生成" \
        --priority "P0" \
        --category "chat-flow" \
        --modules "chat/hooks,chat/adapter" \
        --files "hooks/useAgentChat.ts,adapter/agent-adapter.ts" \
        --summary "添加状态检查条件避免在loading状态下触发ppt生成" \
        --status fixed

    # 更新已有记录的 status（Issue 状态从 investigate 转为 fixed）
    python3 learn.py --issue 2613 --set-status fixed

    # 更新已有记录的方案（A→B 迭代）
    python3 learn.py \
        --issue 2079 \
        --supersede \
        --summary "改用 Readiness Probe 方案替代 onEndTurn 无条件发送"

    # 手动触发老化归档
    python3 learn.py --archive-only

    # 归档超过 30 天且对应 Issue 已关闭的 investigate 记录（需要 CNB_TOKEN 环境变量）
    python3 learn.py --archive-old-investigate
    # 仅预览（不实际归档）
    python3 learn.py --archive-old-investigate --dry-run
"""

import json
import os
import sys
import argparse
from datetime import datetime, timedelta
from pathlib import Path

# 项目根目录
ROOT_DIR = Path(__file__).resolve().parent.parent.parent.parent.parent
KNOWLEDGE_DIR = ROOT_DIR / "knowledge"
FIX_HISTORY_FILE = KNOWLEDGE_DIR / "fix-history.json"
FIX_HISTORY_ARCHIVE = KNOWLEDGE_DIR / "fix-history-archive.json"
# patterns.md 优先使用仓库级路径，无 repo 时回退到 _shared/
PATTERNS_FILE = KNOWLEDGE_DIR / "_shared" / "patterns.md"

ARCHIVE_DAYS = 14  # 超过此天数 + 终态 status 的记录自动归档
ARCHIVE_TRIGGER_COUNT = 80  # 主文件 fixes 超过此数才触发归档（防假期批量断崖）
ARCHIVE_BATCH_MAX = 15  # 单次归档最多移动条数（削峰）
INVESTIGATE_ARCHIVE_DAYS = 30  # investigate 记录在此天数后可被 --archive-old-investigate 清理
ARCHIVABLE_STATUS = {"fixed", "superseded", "archived"}
VALID_STATUS = {"active", "fixed", "investigate", "superseded", "archived"}
# learn_status: 深度学习（pattern 提取）状态
VALID_LEARN_STATUS = {"pending", "complete"}

VALID_CATEGORIES = [
    "chat-flow", "agent-comm", "ui-render", "file-upload",
    "websocket", "store-sync", "api-request", "style-layout", "other"
]

CATEGORY_LABELS = {
    "chat-flow": "聊天流/消息状态",
    "agent-comm": "Agent 通信/Session 管理",
    "ui-render": "UI 渲染/组件交互",
    "file-upload": "文件/图片上传",
    "websocket": "WebSocket/实时通信",
    "store-sync": "状态管理/Store 同步",
    "api-request": "API/网络请求",
    "style-layout": "样式/布局",
    "other": "其他",
}

# 知识文件与 Issue 分类的映射关系（用于按需加载）
# 支持仓库级路径：knowledge/{repo}/filename.md
# 无 repo 时回退到 knowledge/filename.md（向后兼容）
CATEGORY_KNOWLEDGE_MAP = {
    "chat-flow": [
        "architecture.md", "modules.md", "patterns.md", "fix-history.json",
        "state-lifecycle.md", "page-components.md", "key-functions.md",
    ],
    "agent-comm": [
        "architecture.md", "modules.md", "patterns.md", "fix-history.json",
        "api-contracts.md", "error-handling.md", "key-functions.md",
    ],
    "ui-render": [
        "modules.md", "patterns.md", "fix-history.json",
        "page-components.md", "state-lifecycle.md",
    ],
    "file-upload": [
        "modules.md", "patterns.md", "fix-history.json",
        "api-contracts.md", "tech-constraints.md",
    ],
    "websocket": [
        "architecture.md", "modules.md", "patterns.md", "fix-history.json",
        "api-contracts.md", "error-handling.md",
    ],
    "store-sync": [
        "modules.md", "patterns.md", "fix-history.json",
        "state-lifecycle.md", "dependencies.md",
    ],
    "api-request": [
        "architecture.md", "modules.md", "patterns.md", "fix-history.json",
        "api-contracts.md", "error-handling.md",
    ],
    "style-layout": [
        "modules.md", "patterns.md", "fix-history.json",
        "page-components.md",
    ],
    "other": [
        "architecture.md", "modules.md", "patterns.md", "fix-history.json",
    ],
}

# 全局文件：fix-history.json / api-contracts.md 在所有 repo 中共享，位于根目录
GLOBAL_FILES = {"fix-history.json", "api-contracts.md"}

# 所有分类都需要的基础知识文件
BASE_KNOWLEDGE_FILES = ["architecture.md", "modules.md", "patterns.md", "fix-history.json"]


def load_fix_history():
    """加载修复历史"""
    if FIX_HISTORY_FILE.exists():
        with open(FIX_HISTORY_FILE, "r", encoding="utf-8") as f:
            return json.load(f)
    return {"version": 2, "description": "Bug 修复历史记录（近 30 天活跃知识）", "last_updated": "", "fixes": []}


def save_fix_history(data):
    """保存修复历史"""
    data["last_updated"] = datetime.now().strftime("%Y-%m-%d %H:%M")
    with open(FIX_HISTORY_FILE, "w", encoding="utf-8") as f:
        json.dump(data, f, ensure_ascii=False, indent=2)


def load_archive():
    """加载归档历史"""
    if FIX_HISTORY_ARCHIVE.exists():
        with open(FIX_HISTORY_ARCHIVE, "r", encoding="utf-8") as f:
            return json.load(f)
    return {"version": 2, "description": "Bug 修复历史归档（30 天前的记录，不主动加载）", "last_updated": "", "fixes": []}


def save_archive(data):
    """保存归档历史"""
    data["last_updated"] = datetime.now().strftime("%Y-%m-%d %H:%M")
    with open(FIX_HISTORY_ARCHIVE, "w", encoding="utf-8") as f:
        json.dump(data, f, ensure_ascii=False, indent=2)


def archive_old_records():
    """将「年龄 ≥ARCHIVE_DAYS 天 + status 终态」的记录移入归档文件。

    削峰策略（防假期批量断崖）：
      - 仅当主文件 fixes 数 > ARCHIVE_TRIGGER_COUNT 才触发归档
      - 单次最多移 ARCHIVE_BATCH_MAX 条（最老优先）
      - 归档文件已纳入 FTS5 索引，归档 ≠ 失联，仍可搜到
    active / investigate 永远保留在主文件。
    """
    data = load_fix_history()
    total = len(data["fixes"])

    # 阈值触发：主文件不够大时不归档，避免"假期结束一次性移走几十条"的断崖
    if total <= ARCHIVE_TRIGGER_COUNT:
        return 0

    archive = load_archive()
    cutoff = (datetime.now() - timedelta(days=ARCHIVE_DAYS)).strftime("%Y-%m-%d")

    # 找出所有可归档候选（老 + 终态），按日期升序（最老优先）
    candidates = []
    for i, fix in enumerate(data["fixes"]):
        fix_date = fix.get("fix_date") or fix.get("date", "")
        status = fix.get("status", "active")
        if fix_date and fix_date < cutoff and status in ARCHIVABLE_STATUS:
            candidates.append((fix_date, i, fix))
    candidates.sort(key=lambda x: x[0])  # 最老优先

    # 削峰：单次最多移 ARCHIVE_BATCH_MAX 条
    to_move_idx = {i for _, i, _ in candidates[:ARCHIVE_BATCH_MAX]}
    if not to_move_idx:
        return 0

    active = []
    moved = 0
    for i, fix in enumerate(data["fixes"]):
        if i in to_move_idx:
            archive["fixes"].append(fix)
            moved += 1
        else:
            active.append(fix)

    if moved > 0:
        data["fixes"] = active
        save_fix_history(data)
        save_archive(archive)
        print(f"📦 已归档 {moved} 条（削峰：单次上限 {ARCHIVE_BATCH_MAX}，剩余 {len(active)} 条）到 fix-history-archive.json")
        remaining = len(candidates) - moved
        if remaining > 0:
            print(f"   另有 {remaining} 条可归档候选，下次运行继续削峰（避免批量断崖）")

    return moved


def archive_old_investigate(dry_run: bool = False):
    """归档超过 INVESTIGATE_ARCHIVE_DAYS 天的 investigate 记录。

    investigate 记录默认永不自动归档，但随时间积累会导致 fix-history.json 膨胀。
    此函数提供手动触发的归档，条件：
      1. status == 'investigate'
      2. fix_date 超过 INVESTIGATE_ARCHIVE_DAYS 天
      3. （可选）对应 Issue 已关闭（通过 CNB_TOKEN 检查，无 token 时跳过此检查）

    用法：
        python3 learn.py --archive-old-investigate            # 实际归档
        python3 learn.py --archive-old-investigate --dry-run  # 预览（不修改文件）
    """
    data = load_fix_history()
    archive = load_archive()
    cutoff = (datetime.now() - timedelta(days=INVESTIGATE_ARCHIVE_DAYS)).strftime("%Y-%m-%d")

    # 尝试加载 CNB_TOKEN，用于检查 Issue 是否已关闭（可选）
    cnb_token = os.environ.get("CNB_TOKEN", "")
    credentials_file = Path(__file__).resolve().parents[5] / ".credentials"
    if not cnb_token and credentials_file.exists():
        for line in credentials_file.read_text().splitlines():
            line = line.strip()
            if line.startswith("CNB_TOKEN="):
                cnb_token = line.split("=", 1)[1].strip().strip('"').strip("'")
                break

    candidates = []
    keep = []
    for fix in data["fixes"]:
        fix_date = fix.get("fix_date") or fix.get("date", "")
        status = fix.get("status", "active")
        is_old = fix_date and fix_date < cutoff
        is_investigate = status == "investigate"
        if is_old and is_investigate:
            candidates.append(fix)
        else:
            keep.append(fix)

    if not candidates:
        print(f"✅ 无需归档（无超过 {INVESTIGATE_ARCHIVE_DAYS} 天的 investigate 记录）")
        return 0

    # 尝试检查 Issue 是否已关闭
    to_archive = []
    to_keep_open = []
    for fix in candidates:
        issue_number = fix.get("issue_number")
        issue_closed = False
        if cnb_token and issue_number:
            try:
                import subprocess
                cnb_sh = Path(__file__).resolve().parents[5] / ".codebuddy" / "skills" / "cnb-api" / "scripts" / "cnb.sh"
                # 动态取所有 CNB 仓库（新增仓库自动适配）
                try:
                    import sys as _sys2
                    _sys2.path.insert(0, str(ROOT_DIR / "scripts" / "knowledge"))
                    from repos_config import cnb_repos as _cr
                    _repos = _cr()
                except Exception:
                    _repos = ["genie/genie", "genie-agent/genie-agent"]
                for repo in _repos:
                    result = subprocess.run(
                        [str(cnb_sh), "issues", "get-issue", "--repo", repo, "--number", str(issue_number)],
                        capture_output=True, text=True, timeout=10
                    )
                    if result.returncode == 0 and '"state"' in result.stdout:
                        import json as _json
                        try:
                            issue_data = _json.loads(result.stdout)
                            state = issue_data.get("data", {}).get("state", "open")
                            if state in ("closed", "done", "resolved"):
                                issue_closed = True
                                break
                        except Exception:
                            pass
            except Exception:
                pass

        if not cnb_token:
            # 无 token，不检查 Issue 状态，全部候选归档
            to_archive.append(fix)
        elif issue_closed:
            to_archive.append(fix)
        else:
            to_keep_open.append(fix)

    if not to_archive:
        print(f"ℹ️  找到 {len(candidates)} 条超龄 investigate 记录，但对应 Issue 均未关闭，保留。")
        if to_keep_open:
            for fix in to_keep_open:
                print(f"   保留: #{fix.get('issue_number')} {fix.get('title', '')[:40]}")
        return 0

    print(f"{'[DRY RUN] ' if dry_run else ''}准备归档 {len(to_archive)} 条超龄 investigate 记录：")
    for fix in to_archive:
        print(f"   #{fix.get('issue_number')} ({fix.get('fix_date')}) {fix.get('title', '')[:50]}")

    if to_keep_open:
        print(f"   保留 {len(to_keep_open)} 条（Issue 仍 open）")

    if not dry_run:
        data["fixes"] = keep + to_keep_open
        for fix in to_archive:
            fix["archived_at"] = datetime.now().strftime("%Y-%m-%d")
            fix["archive_reason"] = f"investigate 记录超过 {INVESTIGATE_ARCHIVE_DAYS} 天且 Issue 已关闭"
            archive["fixes"].append(fix)
        save_fix_history(data)
        save_archive(archive)
        print(f"📦 已归档 {len(to_archive)} 条超龄 investigate 记录到 fix-history-archive.json")

    return len(to_archive)


def set_status(issue_number, new_status):
    """更新已有记录的 status 字段（active/fixed/investigate/superseded/archived）。"""
    if new_status not in VALID_STATUS:
        print(f"⚠️  非法 status: {new_status}，必须是 {sorted(VALID_STATUS)}", file=sys.stderr)
        return False

    data = load_fix_history()
    for fix in data["fixes"]:
        if fix["issue_number"] == issue_number:
            old = fix.get("status", "active")
            fix["status"] = new_status
            fix["status_updated_at"] = datetime.now().strftime("%Y-%m-%d %H:%M")
            save_fix_history(data)
            print(f"✅ Issue #{issue_number} status: {old} → {new_status}")
            return True

    archive = load_archive()
    for fix in archive["fixes"]:
        if fix["issue_number"] == issue_number:
            old = fix.get("status", "active")
            fix["status"] = new_status
            fix["status_updated_at"] = datetime.now().strftime("%Y-%m-%d %H:%M")
            save_archive(archive)
            print(f"✅ Issue #{issue_number}（归档中）status: {old} → {new_status}")
            return True

    print(f"⚠️  Issue #{issue_number} 不存在", file=sys.stderr)
    return False


def set_learn_status(issue_number, new_learn_status):
    """更新已有记录的 learn_status 字段（pending/complete）。

    供 tidy-knowledge 深度补偿后标记 complete，或 knowledge-learner 完成后标记。
    同时查主文件和归档文件。
    """
    if new_learn_status not in VALID_LEARN_STATUS:
        print(f"⚠️  非法 learn_status: {new_learn_status}，必须是 {sorted(VALID_LEARN_STATUS)}", file=sys.stderr)
        return False

    for loader, saver, tag in ((load_fix_history, save_fix_history, ""),
                                (load_archive, save_archive, "（归档中）")):
        data = loader()
        for fix in data["fixes"]:
            if fix["issue_number"] == issue_number:
                old = fix.get("learn_status", "pending")
                fix["learn_status"] = new_learn_status
                fix["learn_status_updated_at"] = datetime.now().strftime("%Y-%m-%d %H:%M")
                saver(data)
                print(f"✅ Issue #{issue_number}{tag} learn_status: {old} → {new_learn_status}")
                return True

    print(f"⚠️  Issue #{issue_number} 不存在", file=sys.stderr)
    return False


def supersede_record(issue_number, new_summary):
    """更新已有记录的修复方案（A→B 迭代）"""
    data = load_fix_history()
    found = False

    for fix in data["fixes"]:
        if fix["issue_number"] == issue_number:
            old_summary = fix.get("fix_summary", "")
            # 保留历史方案
            superseded = fix.setdefault("_superseded", [])
            superseded.append({
                "summary": old_summary,
                "superseded_at": datetime.now().strftime("%Y-%m-%d %H:%M"),
            })
            # 更新为新方案
            fix["fix_summary"] = new_summary
            fix["fix_date"] = datetime.now().strftime("%Y-%m-%d")
            found = True
            break

    if not found:
        # 也检查归档
        archive = load_archive()
        for fix in archive["fixes"]:
            if fix["issue_number"] == issue_number:
                old_summary = fix.get("fix_summary", "")
                superseded = fix.setdefault("_superseded", [])
                superseded.append({
                    "summary": old_summary,
                    "superseded_at": datetime.now().strftime("%Y-%m-%d %H:%M"),
                })
                fix["fix_summary"] = new_summary
                fix["fix_date"] = datetime.now().strftime("%Y-%m-%d")
                save_archive(archive)
                print(f"🔄 已更新归档中 Issue #{issue_number} 的修复方案（旧方案已保留在 _superseded）")
                return True
        print(f"⚠️  Issue #{issue_number} 不存在修复记录，无法更新")
        return False

    save_fix_history(data)
    print(f"🔄 已更新 Issue #{issue_number} 的修复方案（旧方案已保留在 _superseded）")
    return True


def count_pattern_occurrences(category):
    """统计某个分类在修复历史中的出现次数（含活跃+归档）"""
    data = load_fix_history()
    count = sum(1 for fix in data["fixes"] if fix.get("root_cause_category") == category)
    archive = load_archive()
    count += sum(1 for fix in archive["fixes"] if fix.get("root_cause_category") == category)
    return count


def get_knowledge_files_for_category(category):
    """根据 Issue 分类返回应该加载的知识文件列表"""
    files = CATEGORY_KNOWLEDGE_MAP.get(category, BASE_KNOWLEDGE_FILES)
    # 去重并保持顺序
    seen = set()
    result = []
    for f in files:
        if f not in seen:
            seen.add(f)
            result.append(f)
    return result


def print_knowledge_load_guide(category, repo=""):
    """输出按分类加载知识的指引。支持仓库级路径查找，自动 fallback 到 _shared/。"""
    files = get_knowledge_files_for_category(category)
    label = CATEGORY_LABELS.get(category, category)
    repo_tag = f" [{repo}]" if repo else ""
    print(f"\n📚 分类「{label}」建议加载的知识文件{repo_tag}：")
    for f in files:
        if f in GLOBAL_FILES:
            filepath = KNOWLEDGE_DIR / f
            display = f"knowledge/{f}"
        elif repo:
            # 优先仓库级路径，回退到 _shared/
            repo_path = KNOWLEDGE_DIR / repo / f
            shared_path = KNOWLEDGE_DIR / "_shared" / f
            if repo_path.exists():
                filepath = repo_path
                display = f"knowledge/{repo}/{f}"
            elif shared_path.exists():
                filepath = shared_path
                display = f"knowledge/_shared/{f}"
            else:
                filepath = repo_path  # 不存在，展示预期路径
                display = f"knowledge/{repo}/{f}"
        else:
            # 无 repo 时回退到根目录或 _shared/
            root_path = KNOWLEDGE_DIR / f
            shared_path = KNOWLEDGE_DIR / "_shared" / f
            if root_path.exists():
                filepath = root_path
                display = f"knowledge/{f}"
            elif shared_path.exists():
                filepath = shared_path
                display = f"knowledge/_shared/{f}"
            else:
                filepath = root_path
                display = f"knowledge/{f}"
        if filepath.exists():
            size = filepath.stat().st_size
            print(f"   ✅ {display} ({size // 1024}KB)")
        else:
            print(f"   ⚠️  {display} (不存在)")


def _infer_repo_from_files(files_str: str) -> str:
    """从 modified_files 路径前缀推断仓库 slug。

    规则：路径以 repos/{slug}/ 或 repos/.pool/{slug}-N/ 开头 → 提取 slug。
    跨仓库或多仓库匹配不到时返回空字符串。
    """
    if not files_str:
        return ""
    # 动态仓库列表（从 repos-config.json，新增仓库自动适配）
    try:
        import sys as _sys
        _sys.path.insert(0, str(ROOT_DIR / "scripts" / "knowledge"))
        from repos_config import repo_slugs as _rs
        known = _rs()
    except Exception:
        known = ["genie", "genie-agent", "marketplace"]  # 回退
    import re
    slugs = set()
    for f in files_str.split(","):
        f = f.strip()
        # 标准 worktree 路径：repos/{slug}/...
        for slug in known:
            if f.startswith(f"repos/{slug}/"):
                slugs.add(slug)
                break
        # pool 槽位路径：repos/.pool/{slug}-N/
        else:
            if f.startswith("repos/.pool/"):
                m = re.match(r"repos/\.pool/([a-z-]+)-\d+/", f)
                if m and m.group(1) in known:
                    slugs.add(m.group(1))
    if len(slugs) == 1:
        return slugs.pop()
    # 跨仓库或无匹配
    return ""


def main():
    parser = argparse.ArgumentParser(description="记录 Bug 修复知识")
    parser.add_argument("--issue", type=int, help="Issue 编号")
    parser.add_argument("--title", help="Issue 标题")
    parser.add_argument("--priority", default="P2", help="优先级（-1P/P0/P1/P2）")
    parser.add_argument("--category", choices=VALID_CATEGORIES, help="根因分类")
    parser.add_argument("--modules", help="涉及模块（逗号分隔）")
    parser.add_argument("--files", help="修改文件（逗号分隔）")
    parser.add_argument("--summary", help="修复方案摘要")
    parser.add_argument("--supersede", action="store_true", help="更新已有记录的方案（需配合 --issue 和 --summary）")
    parser.add_argument("--archive-only", action="store_true", help="仅执行老化归档，不新增记录")
    parser.add_argument("--archive-old-investigate", action="store_true",
                        help="归档超过 30 天且对应 Issue 已关闭的 investigate 记录（需 CNB_TOKEN）")
    parser.add_argument("--dry-run", action="store_true", help="配合 --archive-old-investigate：仅预览，不实际归档")
    parser.add_argument("--suggest-files", help="根据分类输出建议加载的知识文件列表")
    parser.add_argument("--status", choices=sorted(VALID_STATUS), default="fixed",
                        help="新增记录的 status，默认 fixed（无 PR 调研用 investigate）")
    parser.add_argument("--set-status", choices=sorted(VALID_STATUS),
                        help="只更新已有记录的 status，需配合 --issue")
    parser.add_argument("--learn-status", choices=sorted(VALID_LEARN_STATUS), default="pending",
                        help="新增记录的 learn_status，默认 pending（pattern 待补偿）。learn 节点完整执行传 complete")
    parser.add_argument("--set-learn-status", choices=sorted(VALID_LEARN_STATUS),
                        help="只更新已有记录的 learn_status（pending/complete），需配合 --issue")
    parser.add_argument("--repo", help="所属仓库 slug（如 genie / genie-agent / marketplace）")

    args = parser.parse_args()

    # 每次运行都先执行老化归档
    archive_old_records()

    # 仅归档模式
    if args.archive_only:
        return

    # 归档老化 investigate 记录
    if args.archive_old_investigate:
        archive_old_investigate(dry_run=getattr(args, "dry_run", False))
        return

    # 输出知识加载建议
    if args.suggest_files:
        print_knowledge_load_guide(args.suggest_files)
        return

    # 仅更新 status
    if args.set_status:
        if not args.issue:
            print("⚠️  --set-status 需要同时提供 --issue", file=sys.stderr)
            sys.exit(1)
        set_status(args.issue, args.set_status)
        return

    # 仅更新 learn_status（tidy 深度补偿后标记 complete）
    if args.set_learn_status:
        if not args.issue:
            print("⚠️  --set-learn-status 需要同时提供 --issue", file=sys.stderr)
            sys.exit(1)
        set_learn_status(args.issue, args.set_learn_status)
        return

    # 更新模式
    if args.supersede:
        if not args.issue or not args.summary:
            print("⚠️  --supersede 需要同时提供 --issue 和 --summary", file=sys.stderr)
            sys.exit(1)
        supersede_record(args.issue, args.summary)
        # supersede 同时把 status 标记为 superseded
        set_status(args.issue, "superseded")
        return

    # 新增模式：验证必填参数
    required = {
        "--issue": args.issue,
        "--title": args.title,
        "--category": args.category,
        "--modules": args.modules,
        "--files": args.files,
        "--summary": args.summary,
    }
    missing = [k for k, v in required.items() if not v]
    if missing:
        print(f"⚠️  新增记录缺少参数: {', '.join(missing)}", file=sys.stderr)
        print("", file=sys.stderr)
        print("完整示例:", file=sys.stderr)
        print('  python3 learn.py \\', file=sys.stderr)
        print('    --issue 2175 \\', file=sys.stderr)
        print('    --title "stop 后重置工具状态" \\', file=sys.stderr)
        print('    --category chat-flow \\', file=sys.stderr)
        print('    --modules useAgentChat \\', file=sys.stderr)
        print('    --files "path/to/file1.ts,path/to/file2.ts" \\', file=sys.stderr)
        print('    --summary "一句话修复摘要" \\', file=sys.stderr)
        print('    --pr https://cnb.woa.com/.../pulls/xxx', file=sys.stderr)
        print("", file=sys.stderr)
        print("可选分类 (category): chat-flow / agent-comm / ui-render / file-upload /", file=sys.stderr)
        print("                    websocket / store-sync / api-request / style-layout / other", file=sys.stderr)
        sys.exit(1)

    # 加载并追加修复记录
    data = load_fix_history()

    # 检查是否已记录
    existing = [f for f in data["fixes"] if f["issue_number"] == args.issue]
    if existing:
        print(f"⚠️  Issue #{args.issue} 已存在修复记录，跳过重复记录")
        print(f"   如需更新方案，请使用: --supersede --issue {args.issue} --summary \"新方案\"")
        return

    fix_record = {
        "issue_number": args.issue,
        "title": args.title,
        "priority": args.priority,
        "repo": args.repo or _infer_repo_from_files(args.files),
        "root_cause_category": args.category,
        "category_label": CATEGORY_LABELS.get(args.category, args.category),
        "affected_modules": [m.strip() for m in args.modules.split(",")],
        "modified_files": [f.strip() for f in args.files.split(",")],
        "fix_summary": args.summary,
        "fix_date": datetime.now().strftime("%Y-%m-%d"),
        "status": args.status,
        # learn_status: 深度学习状态。complete=已提取 pattern 写入 patterns.md；
        # pending=仅记录 fix-history，pattern 待 tidy-knowledge 从 jsonl 补偿提取
        "learn_status": args.learn_status,
    }

    data["fixes"].append(fix_record)
    save_fix_history(data)

    print(f"✅ 已记录 Issue #{args.issue} 的修复知识")
    print(f"   分类: {CATEGORY_LABELS.get(args.category)}")
    print(f"   模块: {', '.join(fix_record['affected_modules'])}")
    print(f"   文件: {', '.join(fix_record['modified_files'])}")

    # 输出该分类的知识加载建议
    print_knowledge_load_guide(args.category, args.repo or "")

    # 检查晋升条件
    count = count_pattern_occurrences(args.category)
    if count >= 3:
        label = CATEGORY_LABELS.get(args.category, args.category)
        print(f"\n⚡ 知识晋升建议：模式「{label}」已出现 {count} 次，建议晋升为 .codebuddy/rules/ 下的强制规则。")

    # 触发技能演化
    evolve_script = ROOT_DIR / ".codebuddy" / "scripts" / "skill_evolve.py"
    if evolve_script.exists():
        print(f"\n🧬 触发技能演化检查...")
        import subprocess
        result = subprocess.run(
            ["python3", str(evolve_script), "learn",
             "--issue", str(args.issue),
             "--category", args.category],
            capture_output=True, text=True, timeout=30
        )
        if result.stdout.strip():
            print(result.stdout.strip())
        if result.returncode != 0 and result.stderr.strip():
            print(f"   ⚠️ 演化检查异常: {result.stderr.strip()[:100]}")


if __name__ == "__main__":
    main()
