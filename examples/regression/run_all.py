# Copyright (c) 2026 henryyu@163.com. All rights reserved.
#
# This file is part of xlpeek, a read-only spreadsheet reader for agents.
# See README.md for what it does and docs/AGENTS.md for how it is meant to be
# driven.

"""Run every regression suite and report one verdict.

    python run_all.py            # everything
    python run_all.py --verbose  # show each suite's full output

Suites run in their own process so that one crashing cannot take the rest with
it, and a suite's output is only echoed when it fails — the point of a runner is
the verdict, not the transcript.
"""

import subprocess
import sys
import time
from pathlib import Path

import _common  # noqa: F401  — chdir to the repo root and force UTF-8 output

HERE = Path(__file__).resolve().parent

# Ordered cheapest-first so that a broken build is obvious within seconds.
SUITES = [
    ("regress_new.py", "合并单元格 / 表头行 / profile / TSV"),
    ("regress_agg.py", "聚合与独立解析器交叉验证"),
    ("regress_filter.py", "过滤器正确性"),
    ("regress_paging.py", "分页完整性与跨表一致性"),
    ("regress_precision.py", "15 位有效数字规范化"),
    ("regress_crosscheck.py", "CLI 与独立 XML 解析器逐格比对"),
    ("verify_docs.py", "文档中的命令可执行"),
    ("regress_shapes.py", "真实表格形状（格式/公式/合并/括号列名/文本百分比）"),
    ("regress_round2.py", "第二轮外部复检 15 条（静默错值/分页边界/日期/接口一致性）"),
    ("regress_round3.py", "第三轮能力（--col 派生列 / --agg 公式聚合 / --share 占比与边界）"),
    ("regress_office.py", "办公室场景（隐藏行 / 小计行 / 两级表头 / 全角数字 / 文件指纹）"),
    ("regress_small.py", "小文件鲁棒性（含加密与损坏文件）"),
    ("serve_mem.py", "serve 会话内存有界"),
]


def node_available() -> bool:
    try:
        subprocess.run(["node", "--version"], capture_output=True, timeout=30, check=True)
        return True
    except (OSError, subprocess.SubprocessError):
        return False


def main() -> int:
    verbose = "--verbose" in sys.argv
    print("xlpeek 回归套件")
    print("  二进制: %s" % _common.EXE)
    print("  Python: %s" % sys.version.split()[0])
    print("=" * 66)

    results = []
    started = time.time()
    for script, title in SUITES:
        t0 = time.time()
        proc = subprocess.run([sys.executable, str(HERE / script)], capture_output=True)
        elapsed = time.time() - t0
        ok = proc.returncode == 0
        results.append((ok, title, elapsed, proc))
        print("  %s  %-34s %6.1fs" % ("✓" if ok else "✗", title, elapsed))
        if not ok or verbose:
            for line in proc.stdout.decode("utf-8", "replace").splitlines()[-12:]:
                print("       " + line)

    # The Node wrapper is a separate language but the same suite of claims.
    title = "Node.js 封装与 maxBuffer 行为"
    if node_available():
        t0 = time.time()
        node_dir = HERE.parent / "nodejs"
        proc = subprocess.run(["node", "test.js"], cwd=str(node_dir), capture_output=True)
        elapsed = time.time() - t0
        ok = proc.returncode == 0
        results.append((ok, title, elapsed, proc))
        print("  %s  %-34s %6.1fs" % ("✓" if ok else "✗", title, elapsed))
        if not ok or verbose:
            for line in proc.stdout.decode("utf-8", "replace").splitlines()[-12:]:
                print("       " + line)
    else:
        print("  ·  %-34s %s" % (title, "跳过: 未找到 node"))

    failed = [r for r in results if not r[0]]
    print("=" * 66)
    print("%d 通过, %d 失败, 总耗时 %.1fs"
          % (len(results) - len(failed), len(failed), time.time() - started))
    for _, title, _, proc in failed:
        err = proc.stderr.decode("utf-8", "replace").strip().splitlines()
        print("  ✗ %s%s" % (title, ("  — " + err[-1][:120]) if err else ""))
    return 1 if failed else 0


if __name__ == "__main__":
    sys.exit(main())
