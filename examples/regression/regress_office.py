# Copyright (c) 2026 henryyu@163.com. All rights reserved.
#
# This file is part of xlpeek, a read-only spreadsheet reader for
# agents. See README.md for what it does and docs/AGENTS.md for how it is
# meant to be driven.

"""The office habits that used to change an answer without saying so.

Every check here is stated the way the person looking at the sheet would state
it: the total they can see in Excel, the column name they read off the report,
the number they would have typed. The four sheets in `testdata/office.xlsx` are
shapes rather than datasets — a view saved from a filter, a report holding its
own subtotals, a title row merged over the columns it groups, and figures pasted
in from a word processor — and each one used to produce a confident wrong
number:

    hidden rows      the file totalled 2100 where the screen says 1600
    subtotal rows    1700 where the report itself says 700
    full-width       500 where the column holds 2968
    two-level header the columns could not be named at all

This suite is the tracked counterpart to the internal review in
`bugs/roadmap-office-gaps.md`; regenerate the fixture with
`python examples/regression/make_fixtures.py`.
"""
import _common  # noqa: F401  — chdir to the repo root and force UTF-8 output
import hashlib
import json
import os
import subprocess
import sys

EXE = _common.EXE
FILE = 'testdata/office.xlsx'
FAILURES = []


def run(*args):
    """Return (exit code, envelope). Envelope parsing is the suite's own."""
    p = subprocess.run([EXE, *args], capture_output=True, timeout=300)
    out = p.stdout.decode('utf-8', 'replace').strip()
    try:
        env = json.loads(out.splitlines()[0])
    except Exception:
        env = {'_raw': out[:200]}
    return p.returncode, env


def check(name, got, want):
    ok = got == want
    print('  %-52s got=%-30s %s'
          % (name, _show(got), 'ok' if ok else 'FAIL want=%s' % _show(want)))
    if not ok:
        FAILURES.append('%s: got %s want %s' % (name, _show(got), _show(want)))


def _show(value):
    text = repr(value)
    return text if len(text) <= 30 else text[:27] + '...'


def data(env):
    payload = env.get('data')
    return payload if isinstance(payload, dict) else {}


def rows(env):
    return data(env).get('rows') or []


def value(env, field):
    """The named field of the single row a grand total produces."""
    result = rows(env)
    return result[0].get(field) if result else None


def warnings(env):
    return data(env).get('warnings') or []


def mentions(env, *words):
    """Whether some warning holds all of these words."""
    return any(all(word in w for word in words) for w in warnings(env))


def failed(rc, env):
    return rc, (env.get('error') or {}).get('code')


# ── 隐藏行：文件里有、屏幕上没有 ────────────────────────────────
print('=== 隐藏行：Excel 里看到的是筛选/折叠后的视图 ===')
rc, env = run('agg', FILE, '-s', '隐藏行', '--header', '--sum', '金额', '--count')
check('默认按全表算（文件里 6 行）', (value(env, 'sum_金额'), value(env, 'count')), (2100, 6))
check('并说明有 2 行是隐藏的',
      mentions(env, '2 of the 6 rows', 'hidden'), True)
check('说明里给了 --visible-only', mentions(env, '--visible-only'), True)

rc, env = run('agg', FILE, '-s', '隐藏行', '--header', '--sum', '金额', '--count',
              '--visible-only')
check('--visible-only 给屏幕上那个数', (value(env, 'sum_金额'), value(env, 'count')), (1600, 4))
check('并说明跳过了几行', mentions(env, 'skipped 2 hidden row'), True)

rc, env = run('read', FILE, '-s', '隐藏行', '--header', '--visible-only')
check('read 只返回可见行', [r.get('项目') for r in rows(env)], ['A', 'D', 'E', 'F'])
rc, env = run('read', FILE, '-s', '隐藏行', '--header', '-l', '10')
check('read 默认仍是全表', len(rows(env)), 6)

rc, env = run('info', FILE, '-s', '隐藏行', '--deep')
sheet = (data(env).get('sheets') or [{}])[0]
check('info --deep 报出隐藏行数', sheet.get('hidden_rows'), 2)

# ── 小计行：报表自己的合计被当明细加了一遍 ──────────────────────
print('=== 小计行：明细里写着 小计/合计 ===')
rc, env = run('agg', FILE, '-s', '小计行', '--header', '--sum', '金额', '--count')
check('默认把小计也加进去', (value(env, 'sum_金额'), value(env, 'count')), (1700, 5))
check('并点名这是小计行', mentions(env, 'subtotal or total rows', '"小计"'), True)

rc, env = run('agg', FILE, '-s', '小计行', '--header', '--sum', '金额', '--count',
              '--exclude-totals')
check('--exclude-totals 给出报表自己的数', (value(env, 'sum_金额'), value(env, 'count')), (700, 3))
check('并说明丢掉了 2 行', mentions(env, 'dropped 2 rows'), True)

rc, env = run('read', FILE, '-s', '小计行', '--header', '--exclude-totals')
check('read 也能排除', [r.get('科目') for r in rows(env)], ['差旅费', '办公费', '会议费'])

# ── 两级表头：合并的标题行 + 真正的列名 ──────────────────────────
print('=== 两级表头：标题行跨在列名之上 ===')
rc, env = run('agg', FILE, '-s', '两级表头', '--header', '--header-rows', '2',
              '--sum', '2024年金额', '--sum', '数量', '--count')
check('两行合起来就是列名', (value(env, 'sum_2024年金额'), value(env, 'sum_数量')), (300, 3))

rc, env = run('agg', FILE, '-s', '两级表头', '--header', '--header-rows', '2',
              '--fill-merged', '--sum', '2024年金额', '--sum', '2024年数量', '--count')
check('--fill-merged 让合并区第二列也有名字',
      (value(env, 'sum_2024年金额'), value(env, 'sum_2024年数量')), (300, 3))

rc, env = run('read', FILE, '-s', '两级表头', '--header', '--header-rows', '2',
              '--fill-merged', '--columns', '部门,2024年数量')
check('read 的投影也用同一个名字',
      [(r.get('部门'), r.get('2024年数量')) for r in rows(env)], [('甲', '1'), ('乙', '2')])

check('没有表头行时 --header-rows 报错',
      failed(*run('agg', FILE, '-s', '两级表头', '--header-rows', '2', '--sum', '金额')),
      (2, 'USAGE'))

# ── 全角数字：从 Word / 微信 粘进来的数 ─────────────────────────
print('=== 全角数字：１２３４ 也是数字 ===')
rc, env = run('agg', FILE, '-s', '全角数字', '--header', '--sum', '金额', '--count')
check('全角与半角一起加总', value(env, 'sum_金额'), 2968)
check('不再有“读不出数字”的告警', data(env).get('warning_count'), 0)

rc, env = run('profile', FILE, '-s', '全角数字', '--header')
types = {c['name']: c['type'] for c in data(env).get('columns') or []}
check('profile 也认它是数字', types.get('金额'), 'number')

rc, env = run('read', FILE, '-s', '全角数字', '--header', '--where', '金额>1000')
check('过滤按数值比较', len(rows(env)), 2)

# ── 文件指纹：这个数是哪一版文件算的 ───────────────────────────
print('=== 文件指纹：数字可以追到文件的那一版 ===')
with open(FILE, 'rb') as handle:
    digest = hashlib.sha256(handle.read()).hexdigest()

rc, env = run('agg', FILE, '-s', '隐藏行', '--header', '--sum', '金额', '--fingerprint')
check('--fingerprint 给出 sha256', data(env).get('file_sha256'), digest)
check('大小与文件一致', data(env).get('file_size'), os.path.getsize(FILE))
check('时间戳存在', bool(data(env).get('file_mtime')), True)

rc, env = run('agg', FILE, '-s', '隐藏行', '--header', '--sum', '金额')
check('默认不带 sha256（省一次全文件读取）', 'file_sha256' in data(env), False)
check('但大小与时间始终在',
      (data(env).get('file_size'), bool(data(env).get('file_mtime'))),
      (os.path.getsize(FILE), True))

# ── 不误报：干净的表格不该有这些告警 ────────────────────────────
print('=== 干净的表不该被误报 ===')
rc, env = run('agg', FILE, '-s', '全角数字', '--header', '--sum', '金额', '--count')
check('无隐藏行、无小计的表格零告警', data(env).get('warning_count'), 0)
rc, env = run('read', FILE, '-s', '全角数字', '--header', '-l', '5')
check('read 同样不报', data(env).get('warning_count'), 0)

_common.finish(FAILURES, '办公室场景')
