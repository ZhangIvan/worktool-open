#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""取证卫生工具（test/ 车道，W-M4）：把 AUD-06 C×3 证据卫生问题流程化。

背景（docs/04 §6 AUD-06，2026-11-03 W-M3R 审计发现）：
  F2 取证 SQL 列名笔误 —— phaseC 取证脚本把 command_events 列写成 `edge`，
     真实 schema 为 `edge_id`（server/internal/store/sqlite/sqlite.go），导致该批
     command_events 明细未采到。本工具按权威 schema 列名重采。
  F3 截图实为 JPEG —— RIG 截图以 .png 命名落盘但 magic bytes 为 JPEG。
     本工具按 magic bytes 核对/（--apply）校正扩展名。
  F4 送验 APK 未归档 —— 装到测试设备的 APK 没有进证据目录，事后无法对应
     "当时设备上跑的是什么构建"。本工具把送验 APK 连 sha256 归档进 local-private。

红线：本工具**只写 local-private/**（证据区，永不入库，G13）；对源文件（除 F3
显式 --apply 改名外）一律只读。列名等 schema 口径以 server/ 权威代码为准，
不在本文件复述 DDL（防漂移，只引用列名常量）。

用法（cwd = 仓库根；python3，仅标准库）：
  python test/collect_evidence.py command-events --db PATH [--command-id ID] [--out F]
  python test/collect_evidence.py command-events --ssh HOST --remote-db PATH [--command-id ID] [--out F]
  python test/collect_evidence.py fix-image-ext [--root DIR] [--apply]
  python test/collect_evidence.py archive-apk APK_PATH [--date YYYY-MM-DD]

退出码：0 = 完成（F2 查询行数可为 0）；1 = 参数/环境/IO 错误。
"""
import argparse
import hashlib
import json
import shutil
import sqlite3
import subprocess
import sys
import time
from pathlib import Path

REPO_ROOT = Path(__file__).resolve().parents[1]
EVIDENCE_ROOT = REPO_ROOT / "local-private" / "evidence"

# F2：command_events 权威列（sqlite.go CREATE TABLE command_events；AUD-06 F2 修正：
# 旧取证脚本误用 `edge`）。复述列名仅为查询投影，schema 本体以 server 代码为准。
CE_COLUMNS = ("command_id", "edge_id", "from_state", "to_state", "event", "detail", "at")


def _today() -> str:
    return time.strftime("%Y-%m-%d")


def _sha256(path: Path) -> str:
    h = hashlib.sha256()
    with open(path, "rb") as f:
        for chunk in iter(lambda: f.read(1 << 20), b""):
            h.update(chunk)
    return h.hexdigest()


def _write_json(path: Path, obj) -> None:
    path.parent.mkdir(parents=True, exist_ok=True)
    with open(path, "w", encoding="utf-8") as f:
        json.dump(obj, f, ensure_ascii=False, indent=1)
    print(f"[collect] 写出 {path}（{path.stat().st_size} B）")


# ---------------------------------------------------------------- F2 --------
def cmd_command_events(args) -> int:
    cols = ", ".join(CE_COLUMNS)
    where = " WHERE command_id=?" if args.command_id else ""
    params = (args.command_id,) if args.command_id else ()
    sql = f"SELECT {cols} FROM command_events{where} ORDER BY id"

    if args.ssh:
        # 远端只读重采（等价 W-M3R dutils.ssh_db 通道；只 SELECT，不开写句柄）
        py = (
            "import sqlite3,json\n"
            f"c=sqlite3.connect('file:{args.remote_db}?mode=ro',uri=True)\n"
            "c.row_factory=sqlite3.Row\n"
            f"rows=c.execute({sql!r},{list(params)!r}).fetchall()\n"
            "print(json.dumps([dict(r) for r in rows],ensure_ascii=False,default=str))\n"
        )
        # encoding 必须显式 UTF-8：Windows 默认 GBK 会话解码远端中文 detail 时崩读线程
        # （stdout 变 None）——W-M3R phaseD 已见同源 GBK 问题（driver INDEX 运维注意③）
        r = subprocess.run(["ssh", args.ssh, "python3", "-"],
                           input=py, capture_output=True, text=True,
                           encoding="utf-8", errors="replace", timeout=60)
        if r.returncode != 0:
            print(f"[F2] ssh 查询失败: {(r.stderr or r.stdout).strip()[:400]}", file=sys.stderr)
            return 1
        try:
            rows = json.loads(r.stdout.strip().splitlines()[-1])
        except (json.JSONDecodeError, IndexError):
            print(f"[F2] 远端输出不可解析: {r.stdout[:300]}", file=sys.stderr)
            return 1
        db_desc = f"{args.ssh}:{args.remote_db}"
    else:
        db = Path(args.db) if args.db else None
        if not db or not db.is_file():
            print(f"[F2] 库文件不存在: {db}", file=sys.stderr)
            return 1
        con = sqlite3.connect(f"file:{db}?mode=ro", uri=True)  # 只读连接
        con.row_factory = sqlite3.Row
        rows = [dict(r) for r in con.execute(sql, params).fetchall()]
        con.close()
        db_desc = str(db)

    out = Path(args.out) if args.out else (
        EVIDENCE_ROOT / _today() / "f2_command_events"
        / (f"{args.command_id or 'all'}.json")
    )
    _write_json(out, {
        "tool": "test/collect_evidence.py command-events",
        "purpose": "AUD-06 F2 重采：按真实列名（edge_id/from_state/to_state/event/detail/at）",
        "db": db_desc,
        "command_id_filter": args.command_id,
        "row_count": len(rows),
        "rows": rows,
    })
    print(f"[F2] 重采 {len(rows)} 行（来源 {db_desc}）")
    return 0


# ---------------------------------------------------------------- F3 --------
MAGIC = [
    (b"\x89PNG\r\n\x1a\n", ".png", "PNG"),
    (b"\xff\xd8\xff", ".jpg", "JPEG"),
    (b"GIF87a", ".gif", "GIF"),
    (b"GIF89a", ".gif", "GIF"),
]


def _sniff(path: Path):
    with open(path, "rb") as f:
        head = f.read(16)
    for sig, ext, name in MAGIC:
        if head.startswith(sig):
            return ext, name
    if head[:4] == b"RIFF" and head[8:12] == b"WEBP":
        return ".webp", "WebP"
    return None, None


def cmd_fix_image_ext(args) -> int:
    root = Path(args.root) if args.root else EVIDENCE_ROOT
    if not root.is_dir():
        print(f"[F3] 目录不存在: {root}", file=sys.stderr)
        return 1
    mismatches = []
    checked = 0
    for p in sorted(root.rglob("*")):
        if not p.is_file() or p.suffix.lower() not in {".png", ".jpg", ".jpeg", ".gif", ".webp"}:
            continue
        checked += 1
        real_ext, fmt = _sniff(p)
        if real_ext is None:
            print(f"[F3] 无法识别 magic bytes（不动）: {p}")
            continue
        cur = ".jpg" if p.suffix.lower() == ".jpeg" else p.suffix.lower()
        if cur != real_ext:
            target = p.with_suffix(real_ext)
            if target.exists():
                print(f"[F3] 目标已存在，跳过（人工处理）: {target}", file=sys.stderr)
                continue
            mismatches.append({"from": str(p), "to": str(target), "format": fmt})
            if args.apply:
                p.rename(target)
                print(f"[F3] 改名 {p.name} -> {target.name}（实为 {fmt}）")
    action = "已改名" if args.apply else "检出待改名（--apply 执行）"
    print(f"[F3] 扫描 {checked} 个图片文件，{action}: {len(mismatches)} 处")
    if mismatches:
        _write_json(EVIDENCE_ROOT / _today() / "f3_image_ext_report.json",
                    {"tool": "test/collect_evidence.py fix-image-ext",
                     "root": str(root), "applied": bool(args.apply),
                     "mismatches": mismatches})
    return 0


# ---------------------------------------------------------------- F4 --------
def cmd_archive_apk(args) -> int:
    apk = Path(args.apk)
    if not apk.is_file():
        print(f"[F4] APK 不存在: {apk}", file=sys.stderr)
        return 1
    dest_dir = EVIDENCE_ROOT / (args.date or _today()) / "assemble"
    dest_dir.mkdir(parents=True, exist_ok=True)
    dest = dest_dir / apk.name
    shutil.copy2(apk, dest)
    digest = _sha256(dest)
    manifest = dest_dir / "APK-ARCHIVE.json"
    entry = {
        "file": dest.name, "size": dest.stat().st_size, "sha256": digest,
        "source": str(apk), "archived_at": time.strftime("%Y-%m-%dT%H:%M:%S"),
        "purpose": "AUD-06 F4：送验 APK 归档（设备上运行的构建可追溯）",
    }
    entries = [entry]
    if manifest.exists():
        try:
            prev = json.loads(manifest.read_text(encoding="utf-8"))
            if isinstance(prev, list):
                entries = [e for e in prev if e.get("sha256") != digest] + [entry]
        except (json.JSONDecodeError, OSError):
            pass
    _write_json(manifest, entries)
    print(f"[F4] 归档 {dest.name} sha256={digest}")
    return 0


def main(argv=None) -> int:
    ap = argparse.ArgumentParser(description="取证卫生工具（AUD-06 F2/F3/F4；只写 local-private/）")
    sub = ap.add_subparsers(dest="cmd", required=True)

    p2 = sub.add_parser("command-events", help="F2：按真实列名重采 command_events")
    g2 = p2.add_mutually_exclusive_group()
    g2.add_argument("--db", help="本地 SQLite 库路径（只读连接）")
    g2.add_argument("--ssh", help="远端主机（如 friend@192.0.2.10；配合 --remote-db）")
    p2.add_argument("--remote-db", default="/srv/worktool-open/server/worktool.db")
    p2.add_argument("--command-id", help="只采单条指令明细（缺省=全表）")
    p2.add_argument("--out", help="输出 JSON 路径（缺省 local-private/evidence/<date>/）")
    p2.set_defaults(fn=cmd_command_events)

    p3 = sub.add_parser("fix-image-ext", help="F3：按 magic bytes 校正图片扩展名")
    p3.add_argument("--root", help="扫描根（缺省 local-private/evidence/）")
    p3.add_argument("--apply", action="store_true", help="实际改名（缺省只报告）")
    p3.set_defaults(fn=cmd_fix_image_ext)

    p4 = sub.add_parser("archive-apk", help="F4：送验 APK 连 sha256 归档 local-private")
    p4.add_argument("apk", help="APK 文件路径")
    p4.add_argument("--date", help="归档日期目录（缺省=今天）")
    p4.set_defaults(fn=cmd_archive_apk)

    args = ap.parse_args(argv)
    return args.fn(args)


if __name__ == "__main__":
    if hasattr(sys.stdout, "reconfigure"):
        sys.stdout.reconfigure(encoding="utf-8", errors="replace")
    sys.exit(main())
