# Copyright (c) 2026 henryyu@163.com. All rights reserved.
#
# This file is part of xlpeek, a read-only spreadsheet reader for
# agents. See README.md for what it does and docs/AGENTS.md for how it is
# meant to be driven.

"""The shapes a real report produces, and that 1.0.1 answered wrongly.

Every other suite reads one workbook whose columns are plain and unformatted.
The shapes below are what a finance report actually looks like, and each one
made the tool either filter wrongly (silently, with ok:true) or hide data:
a money column carrying ¥#,##0.00, a ratio column carrying 0.00%, a formula
column with no cached value, a label merged across rows, column names holding
parentheses, and a column of percentage *text*.

They live in testdata/shapes.xlsx, one sheet each, with expectations written by
hand here — so the suite stays stdlib-only like its neighbours, and a regression
in any of these behaviours fails the build rather than a user's analysis. The
pre-fix behaviour is noted per section, because a test that cannot fail is not
a test.
"""
import _common  # noqa: F401  — chdir to the repo root and force UTF-8 output
import json
import subprocess
import sys

EXE = _common.EXE  # 尊重 XLPEEK 覆盖，见 _common.py
FILE = 'testdata/shapes.xlsx'
FAILURES = []


def run(*args):
    p = subprocess.run([EXE, *args], capture_output=True, timeout=120)
    out = p.stdout.decode('utf-8', 'replace').strip()
    try:
        return json.loads(out.splitlines()[0])
    except Exception:
        return {'_raw': out[:200]}


def check(name, got, want):
    ok = got == want
    print('  %-46s got=%-28s want=%-28s %s'
          % (name, _show(got), _show(want), 'ok' if ok else 'FAIL'))
    if not ok:
        FAILURES.append(name)


def _show(value):
    text = repr(value)
    return text if len(text) <= 28 else text[:25] + '...'


class _Missing(dict):
    """占位：字段或整列缺失时返回 None，让断言记 FAIL 而不是抛异常。"""

    def __missing__(self, key):
        return None


MISSING = _Missing()


def rows(env, key):
    """read/agg 的行里取某一列，失败时返回错误码，便于断言直接比较。"""
    if not env.get('ok'):
        return env.get('error', {}).get('code', 'NO_ENVELOPE')
    return [r[key] for r in env['data']['rows']]


def group_tuples(env, keys):
    """agg 分组行的指定字段；命令失败时返回错误码，比较即 FAIL 而不会抛异常。"""
    if not env.get('ok'):
        return env.get('error', {}).get('code', 'NO_ENVELOPE')
    return [tuple(r[k] for k in keys) for r in env['data']['rows']]


def total_of(env, key):
    if not env.get('ok'):
        return env.get('error', {}).get('code', 'NO_ENVELOPE')
    return sum(r[key] for r in env['data']['rows'])


def profile(env, column):
    """某一列的画像；列不存在或命令失败时返回占位，断言据此记 FAIL。"""
    if not env.get('ok'):
        return MISSING
    for c in env['data']['columns']:
        if c['name'] == column:
            return c
    return MISSING


# ── 一、数字格式：¥#,##0.00 与 0.00% ────────────────────────────
# 1.0.1 读到的显示串无法当数字解析，于是 --where "比率>20%" 退化成字典序比较，
# 5.00% 会命中 >20%；profile 把两列都判成 text。
print('=== 格式：金额列 ¥#,##0.00 / 比率列 0.00% ===')
check('read 显示值即格式串',
      rows(run('read', FILE, '-s', '格式', '--header', '-l', '9'), '金额'),
      ['¥1,234.50', '¥2,345.25', '¥999.99', '¥5,678.00'])
check('比率>20% 只命中 25.67% 与 50.00%',
      rows(run('read', FILE, '-s', '格式', '--header', '--where', '比率>20%', '-l', '9'), '名称'),
      ['C', 'D'])
check('比率<20% 命中 A(5.00%) 与 B(12.34%)',
      rows(run('read', FILE, '-s', '格式', '--header', '--where', '比率<20%', '-l', '9'), '名称'),
      ['A', 'B'])
check('阈值收紧到 2% 是全量',
      rows(run('read', FILE, '-s', '格式', '--header', '--where', '比率>2%', '-l', '9'), '名称'),
      ['A', 'B', 'C', 'D'])
check('比率>0.2 与 比率>20% 同语义',
      rows(run('read', FILE, '-s', '格式', '--header', '--where', '比率>0.2', '-l', '9'), '名称'),
      ['C', 'D'])
check('--raw 下结果不变',
      rows(run('read', FILE, '-s', '格式', '--header', '--where', '比率>20%', '-l', '9',
               '--raw'), '名称'),
      ['C', 'D'])
check('过滤值带 ¥ 与不带等价',
      rows(run('read', FILE, '-s', '格式', '--header', '--where', '金额>¥2000', '-l', '9'), '名称'),
      ['B', 'D'])
prof = run('profile', FILE, '-s', '格式', '--header')
check('profile 金额列是 number', profile(prof, '金额').get('type'), 'number')
check('profile 比率列是 number', profile(prof, '比率').get('type'), 'number')
check('profile 金额列统计量',
      [profile(prof, '金额').get(k) for k in ('min', 'max', 'mean')],
      [999.99, 5678, 2564.435])
check('profile 比率列统计量',
      [profile(prof, '比率').get(k) for k in ('min', 'max', 'mean')],
      [0.05, 0.5, 0.232525])
check('agg 默认求和',
      rows(run('agg', FILE, '-s', '格式', '--header', '--sum', '金额', '--count'), 'sum_金额'),
      [10257.74])
check('agg --raw 求和与默认一致',
      rows(run('agg', FILE, '-s', '格式', '--header', '--sum', '金额', '--raw'), 'sum_金额'),
      [10257.74])

# ── 二、没有缓存值的公式列 ──────────────────────────────────────
# 程序生成的 xlsx（openpyxl / pandas / xlsxwriter）不写公式结果。1.0.1 静默读成空：
# agg 求和为 null 且 warning_count=0，调用方没有任何线索。--calc 当时也只 read 支持。
print('=== 公式：openpyxl 写出的无缓存公式列 ===')
check('read 默认读成空串',
      rows(run('read', FILE, '-s', '公式', '--header', '-l', '9'), '金额'),
      ['', '', ''])
check('read --calc 求出 21/12/11',
      rows(run('read', FILE, '-s', '公式', '--header', '-l', '9', '--calc'), '金额'),
      ['21', '12', '11'])
env = run('agg', FILE, '-s', '公式', '--header', '--sum', '金额')
check('agg 默认仍是 null（不假装有值）', rows(env, 'sum_金额'), [None])
check('agg 默认给出告警', env['data']['warning_count'] > 0, True)
check('告警点名该列并提示 --calc',
      any('金额' in w and '--calc' in w for w in env['data'].get('warnings', [])), True)
check('agg --calc 求和为 44',
      rows(run('agg', FILE, '-s', '公式', '--header', '--sum', '金额', '--calc'), 'sum_金额'),
      [44])
check('profile --calc 判为 number',
      profile(run('profile', FILE, '-s', '公式', '--header', '--columns', '金额', '--calc'),
              '金额').get('type'),
      'number')

# ── 三、跨行合并的标签列 ────────────────────────────────────────
# 合并区只在左上角存值。1.0.1 的 agg 没有 --fill-merged，子行静默落进空分组。
print('=== 合并：A2:A3 的「华北」跨两行 ===')
check('read --fill-merged 填满合并区',
      [rows(run('read', FILE, '-s', '合并', '--header', '--fill-merged', '-l', '9'), '区域'),
       rows(run('read', FILE, '-s', '合并', '--header', '--fill-merged', '-l', '9'), '金额')],
      [['华北', '华北', '华南'], ['100', '200', '300']])
env = run('agg', FILE, '-s', '合并', '--header', '--group-by', '区域', '--sum', '金额')
check('agg 默认告警空分组并提示 --fill-merged',
      any('--fill-merged' in w for w in env['data'].get('warnings', [])), True)
env = run('agg', FILE, '-s', '合并', '--header', '--group-by', '区域',
          '--sum', '金额', '--count', '--fill-merged')
check('agg --fill-merged 后华北=300/2 行',
      group_tuples(env, ('区域', 'sum_金额', 'count')),
      [('华北', 300, 2), ('华南', 300, 1)])
check('填值不改变总额', total_of(env, 'sum_金额'), 600)

# ── 四、列名本身含括号 ──────────────────────────────────────────
# 聚合表达式里「金额(万元)」的括号会被当成算术，必须写成 [金额(万元)]；
# 报错必须给出这个写法，否则调用方（尤其 agent）无从得知。
print('=== 括号列名：金额(万元) ===')
err = run('agg', FILE, '-s', '括号列名', '--header', '--sum', '金额(万元)')
check('裸列名报 USAGE', err.get('error', {}).get('code'), 'USAGE')
check('报错给出方括号写法',
      '[金额(万元)]' in err.get('error', {}).get('message', ''), True)
check('方括号写法可求和',
      rows(run('agg', FILE, '-s', '括号列名', '--header', '--sum', '[金额(万元)]'),
           'sum_[金额(万元)]'),
      [350.75])
check('--where 的括号列名无需方括号',
      rows(run('read', FILE, '-s', '括号列名', '--header',
               '--where', '金额(万元)>150', '-l', '9'), '项目'),
      ['Y'])

# ── 五、文本百分比列 ────────────────────────────────────────────
# 值是字符串 "26.3%"，不是数字格式。1.0.1 既不会按数值比较，profile 也判成 text。
print('=== 文本百分比：值为 "26.3%" 的字符串列 ===')
check('read 原样返回字符串',
      rows(run('read', FILE, '-s', '文本百分比', '--header', '-l', '9'), '占比'),
      ['26.3%', '12.5%', '40.0%', '5.0%'])
check('占比>20% 命中 P 与 R',
      rows(run('read', FILE, '-s', '文本百分比', '--header',
               '--where', '占比>20%', '-l', '9'), '名称'),
      ['P', 'R'])
check('占比<20% 命中 5.0%（D1 的原始症状）',
      rows(run('read', FILE, '-s', '文本百分比', '--header',
               '--where', '占比<20%', '-l', '9'), '名称'),
      ['Q', 'S'])
check('agg 按数值求和 0.838',
      rows(run('agg', FILE, '-s', '文本百分比', '--header', '--sum', '占比'), 'sum_占比'),
      [0.838])
check('profile 判为 number 并给均值',
      [profile(run('profile', FILE, '-s', '文本百分比', '--header', '--columns', '占比'),
               '占比').get(k) for k in ('type', 'mean')],
      ['number', 0.2095])

# ── 六、每张表都守信封契约 ──────────────────────────────────────
print('=== 每张表的信封契约 ===')
for sheet in ('格式', '公式', '合并', '括号列名', '文本百分比'):
    for cmd in (['read', FILE, '-s', sheet, '-l', '5'],
                ['agg', FILE, '-s', sheet, '--count'],
                ['profile', FILE, '-s', sheet, '--max-values', '3'],
                ['find', FILE, '-s', sheet, '-v', '1', '-l', '3']):
        env = run(*cmd)
        ok = (env.get('ok') is True and 'data' in env
              and not [k for k, v in env['data'].items() if v is None])
        check('%s -s %s 信封合法且无 null 数组' % (cmd[0], sheet), ok, True)

print()
_common.finish(FAILURES, label='SHAPES')
