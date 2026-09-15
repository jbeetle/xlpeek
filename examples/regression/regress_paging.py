# Copyright (c) 2026 henryyu@163.com. All rights reserved.
#
# This file is part of xlpeek, a read-only spreadsheet reader for agents.
# See README.md for what it does and docs/AGENTS.md for how it is meant to be
# driven.

"""Regression driver: page through a whole sheet and prove no row is lost or duplicated."""
import _common  # noqa: F401  — chdir to the repo root and force UTF-8 output
import json
import subprocess
import sys
import time

EXE = _common.EXE  # 尊重 XLPEEK 覆盖，见 _common.py
FILE = r'testdata\华鼎科技_分部收入_FY23-FY25.xlsx'


def run(args):
    p = subprocess.run([EXE] + args, capture_output=True)
    if p.returncode != 0:
        sys.stderr.write('FAILED: %s\n%s\n' % (args, p.stdout.decode('utf-8', 'replace')))
        sys.exit(1)
    return json.loads(p.stdout.decode('utf-8'))


def page_all(sheet, page_size, extra=None):
    """Walk the sheet via has_more/next_offset, returning every row plus stats."""
    extra = extra or []
    rows, offset, pages = [], 0, 0
    started = time.time()
    while True:
        data = run(['read', FILE, '-s', sheet, '--header', '--skip-empty',
                    '-o', str(offset), '-l', str(page_size)] + extra)['data']
        pages += 1
        rows.extend(data['rows'])
        if not data['has_more']:
            break
        nxt = data['next_offset']
        if nxt is None or nxt <= offset:
            sys.stderr.write('next_offset did not advance: %r -> %r\n' % (offset, nxt))
            sys.exit(1)
        offset = nxt
        if pages > 200:
            sys.stderr.write('safety stop\n')
            sys.exit(1)
    return rows, pages, time.time() - started


def report(sheet, page_size, key):
    rows, pages, elapsed = page_all(sheet, page_size)
    ids = [r[key] for r in rows]
    uniq = set(ids)
    seq = sorted(int(i.split('-')[1]) for i in ids)
    missing = sorted(set(range(seq[0], seq[-1] + 1)) - set(seq))
    print('%-16s page=%-5d pages=%-3d rows=%-5d unique=%-5d dupes=%-3d gaps=%-3d %6.2fs'
          % (sheet, page_size, pages, len(ids), len(uniq),
             len(ids) - len(uniq), len(missing), elapsed))
    return ids, missing


print('=== 分页遍历完整性（不同页大小必须得到同一批数据）===')
prev = None
for size in (100, 500, 1000, 2600):
    ids, missing = report('FY23_收入明细', size, '凭证号')
    if prev is not None and ids != prev:
        print('  !! MISMATCH: page size %d produced a different row set' % size)
        sys.exit(1)
    prev = ids
    if missing:
        print('  !! MISSING ids:', missing[:10])
        sys.exit(1)

print()
print('=== 每页边界连续性（首行/末行必须无缝衔接）===')
offset, expect_row = 0, 2
while True:
    d = run(['read', FILE, '-s', 'FY24_收入明细', '--header', '--skip-empty',
             '-o', str(offset), '-l', '700'])['data']
    if d['first_row'] != expect_row:
        print('  !! first_row=%d expected %d at offset %d' % (d['first_row'], expect_row, offset))
        sys.exit(1)
    if d['rows_returned'] != (d['last_row'] - d['first_row'] + 1):
        print('  !! row span does not match rows_returned at offset %d' % offset)
        sys.exit(1)
    expect_row = d['last_row'] + 1
    if not d['has_more']:
        break
    offset = d['next_offset']
print('  FY24_收入明细: pages are contiguous, no gap or overlap, ends at row %d' % (expect_row - 1))

print()
print('=== 跨表一致性（三张表结构应一致）===')
for sheet in ('FY23_收入明细', 'FY24_收入明细', 'FY25_收入明细'):
    d = run(['read', FILE, '-s', sheet, '--header', '-l', '1'])['data']
    print('  %-16s columns=%s header=%s' % (sheet, len(d['columns']), d['header'][:4]))

_common.finish([], '分页与跨表一致性')
