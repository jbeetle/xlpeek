# Copyright (c) 2026 henryyu@163.com. All rights reserved.
#
# This file is part of xlpeek, a read-only spreadsheet reader for agents.
# See README.md for what it does and docs/AGENTS.md for how it is meant to be
# driven.

"""One case per finding of the second external review, ported to a fixture.

The review that produced these findings ran against the shipped 1.0.2 binary
from outside the repository, and its central point was that a textual assurance
is not evidence: each of its findings is restated here as a command with an
expected answer, so that a regression fails the build instead of a user's
analysis. Three of them returned ok:true with a plausible wrong result, which
is why several checks below assert a *failure* — the answer is the error.

They read testdata/round2.xlsx, whose sheets are shapes rather than data: a
percentage column to mistype a filter value against, two hundred formatted empty
rows, dates written three ways, a bracketed column name, and a column unique
enough that grouping by it stops summarising. Regenerate it with
`python examples/regression/make_fixtures.py`.
"""
import _common  # noqa: F401  — chdir to the repo root and force UTF-8 output
import json
import subprocess
import sys

EXE = _common.EXE
FILE = 'testdata/round2.xlsx'
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
        FAILURES.append(name)


def _show(value):
    text = repr(value)
    return text if len(text) <= 30 else text[:27] + '...'


def data(env):
    """The envelope's data, or a stand-in, so a failed call cannot raise."""
    payload = env.get('data')
    if not isinstance(payload, dict):
        return {}
    return payload


def values(env, key):
    return [row.get(key) for row in data(env).get('rows') or []]


def failed(rc, env):
    """(exit code, error code) of a call that was expected to be rejected."""
    return rc, (env.get('error') or {}).get('code')


# ── P1-1 一个字符写错，数值比较静默退化成字典序 ──────────────────
# 过滤值不是数字、单元格却是数字时，比较按文本进行——结果集完全不同，
# 而且看起来很正常。现在至少必须出声：warning_count > 0 且点名原因。
print('=== P1-1 过滤值不是数字，但单元格是 ===')
rc, env = run('read', FILE, '-s', '筛选', '--header', '--where', '金额>1500', '-l', '9')
check('数值过滤命中 C', values(env, '名称'), ['C'])
check('数值过滤无告警', data(env).get('warning_count'), 0)

rc, env = run('read', FILE, '-s', '筛选', '--header', '--where', '金额>1OO', '-l', '9')
warnings = data(env).get('warnings') or []
check('1OO 仍然返回结果（字典序语义不变）', rc, 0)
check('1OO 现在告警', data(env).get('warning_count', 0) > 0, True)
check('告警点名过滤值与列',
      any('1OO' in w and '金额' in w for w in warnings), True)

rc, env = run('agg', FILE, '-s', '筛选', '--header', '--sum', '金额',
              '--where', '金额>1OO')
check('agg 同样告警', data(env).get('warning_count', 0) > 0, True)

rc, env = run('profile', FILE, '-s', '筛选', '--header', '--where', '金额>1OO')
check('profile 同样告警', data(env).get('warning_count', 0) > 0, True)

# 文本过滤不受影响：这两条本来就该按文本比较。
rc, env = run('read', FILE, '-s', '筛选', '--header', '--where', '状态=完成', '-l', '9')
check('文本等值过滤不误报', data(env).get('warning_count'), 0)
check('文本等值过滤命中 A 与 C', values(env, '名称'), ['A', 'C'])
rc, env = run('read', FILE, '-s', '筛选', '--header', '--where', '名称~B', '-l', '9')
check('子串过滤不误报', data(env).get('warning_count'), 0)

# ── P1-2 畸形过滤表达式静默返回结果 ─────────────────────────────
# 每一条都曾经 ok:true 且给出行数：">>" 命中 0 条，">" 命中全表，
# ">100>200" 命中 1 条。行数看起来都正常，所以没有任何线索。
print('=== P1-2 畸形过滤表达式 ===')
for expr in ('金额>>100', '金额>', '金额>100>200', '金额>=>100',
             '金额>>=100', '金额==>100'):
    rc, env = run('read', FILE, '-s', '筛选', '--header', '--where', expr, '-l', '9')
    check('拒绝 %s' % expr, failed(rc, env), (2, 'USAGE'))

# 值里真的含运算符时，加引号仍可表达——严格不等于无法表达。
rc, env = run('read', FILE, '-s', '筛选', '--header', '--where', "状态='完成'", '-l', '9')
check('引号包住普通值仍可用', (rc, values(env, '名称')), (0, ['A', 'C']))
rc, env = run('read', FILE, '-s', '筛选', '--header', '--where', "名称~'A>B'", '-l', '9')
check('引号内的运算符不被当成第二个', (rc, data(env).get('rows_returned')), (0, 0))

# ── P1-3 / P2-4 尾部带格式空行 ──────────────────────────────────
# 该表 dimension 到第 200 行，但内容止于第 3 行。以前 complete 只按位置算，
# 于是翻出 99 个全空页。
print('=== P1-3 / P2-4 尾部空行与数据边界 ===')
rc, env = run('info', FILE, '-s', '尾部空行', '--deep')
sheet = (data(env).get('sheets') or [{}])[0]
check('max_row 仍是文件的说法', sheet.get('max_row'), 200)
check('last_populated_row 给出真实边界', sheet.get('last_populated_row'), 3)
check('populated_rows 是计数', sheet.get('populated_rows'), 3)

rc, env = run('read', FILE, '-s', '尾部空行', '--offset', '0', '-l', '2')
check('第 1 页 has_more', data(env).get('has_more'), True)
rc, env = run('read', FILE, '-s', '尾部空行', '--offset', '2', '-l', '2')
check('第 2 页 complete', data(env).get('complete'), True)
check('第 2 页 has_more 为假', data(env).get('has_more'), False)
check('第 2 页带回最后一行数据',
      [row[0] for row in data(env).get('rows') or []], ['B', ''])

# 中间的空洞不算结束：第 5 行起是空行，但 --skip-empty 后仍能取到全部数据。
rc, env = run('read', FILE, '-s', '尾部空行', '--header', '--skip-empty', '-l', '50')
check('--skip-empty 取到两行数据', values(env, '名称'), ['A', 'B'])
check('--skip-empty 时 complete', data(env).get('complete'), True)

# ── P2-1 方括号列名参与 --derive ────────────────────────────────
print('=== P2-1 括号列名的聚合字段 ===')
base = ['agg', FILE, '-s', '括号列名', '--header',
        '--sum', '[金额(万元)]', '--sum', '[成本(万元)]']
rc, env = run(*base)
check('聚合字段名就是方括号写法',
      data(env).get('fields'), ['sum_[金额(万元)]', 'sum_[成本(万元)]'])
rc, env = run(*base, '--derive',
              '毛利率=(sum_[金额(万元)]-sum_[成本(万元)])/sum_[金额(万元)]')
check('derive 引用方括号字段', round(values(env, '毛利率')[0], 6), round(4 / 9, 6))
rc, env = run(*base, '--derive',
              '毛利率=([sum_[金额(万元)]]-[sum_[成本(万元)]])/[sum_[金额(万元)]]')
check('整体加方括号也能解析', round(values(env, '毛利率')[0], 6), round(4 / 9, 6))

# ── P2-2 日期形态 ───────────────────────────────────────────────
# 同一个日期随文件的数字格式变成 "2026-01-01 0:00:00" / "01-01-26" / 序列号。
print('=== P2-2 --dates ===')
rc, env = run('read', FILE, '-s', '日期', '--header', '-l', '3')
first = (data(env).get('rows') or [{}])[0]
check('默认仍是显示值', first.get('短日期'), '01-01-26')

rc, env = run('read', FILE, '-s', '日期', '--header', '--dates', 'iso', '-l', '3')
first = (data(env).get('rows') or [{}])[0]
check('iso 抹平短日期', first.get('短日期'), '2026-01-01')
check('iso 保留时间', first.get('时间'), '2026-01-01T09:30:00')
check('iso 不动非日期列', first.get('金额'), '¥1,234.50')
check('iso 不动文本日期列', first.get('文本日期'), '2026-01-01')
check('信封报告 dates', data(env).get('dates'), 'iso')

rc, env = run('read', FILE, '-s', '日期', '--header', '--dates', 'serial', '-l', '1')
first = (data(env).get('rows') or [{}])[0]
check('serial 给出存储值', first.get('短日期'), '46023')

rc, env = run('read', FILE, '-s', '日期', '--header', '--dates', 'nope', '-l', '1')
check('非法 --dates 报 USAGE', failed(rc, env), (2, 'USAGE'))

# ── P2-3 find 续页 ──────────────────────────────────────────────
print('=== P2-3 find --offset ===')
rc, env = run('find', FILE, '-s', '筛选', '--header', '-v', '完成', '-l', '1')
check('第一页截断且有游标', (data(env).get('truncated'), data(env).get('next_offset')),
      (True, 1))
rc, env = run('find', FILE, '-s', '筛选', '--header', '-v', '完成', '-l', '1', '--offset', '1')
check('续页取到第二条', [m.get('cell') for m in data(env).get('matches') or []], ['D4'])
check('续页后无更多', (data(env).get('truncated'), data(env).get('complete')), (False, True))

# ── P2-5 agg 默认分组上限 ───────────────────────────────────────
print('=== P2-5 agg --limit 默认 ===')
rc, env = run('agg', FILE, '-s', '高基数', '--header', '--group-by', '凭证号', '--sum', '金额')
check('默认只回 1000 组', len(data(env).get('rows') or []), 1000)
check('总数如实', data(env).get('group_count'), 1200)
check('默认上限也算截断', data(env).get('has_more'), True)
check('给出下一页游标', data(env).get('next_offset'), 1000)
check('默认截断要解释',
      any('--limit' in w for w in data(env).get('warnings') or []), True)

rc, env = run('agg', FILE, '-s', '高基数', '--header', '--group-by', '凭证号',
              '--sum', '金额', '--limit', '0')
check('--limit 0 仍是不限', len(data(env).get('rows') or []), 1200)
rc, env = run('agg', FILE, '-s', '高基数', '--header', '--group-by', '凭证号',
              '--sum', '金额', '--limit', '5')
check('显式 --limit 不再告警', data(env).get('warning_count'), 0)

# ── P3-1 布尔 flag 的写法 ───────────────────────────────────────
print('=== P3-1 布尔 flag ===')
rc, env = run('find', FILE, '-s', '筛选', '--header', '-v', 'b', '--ignore-case', 'false')
check('--flag false 生效', (rc, data(env).get('match_count')), (0, 0))
rc, env = run('find', FILE, '-s', '筛选', '--header', '-v', 'b', '--ignore-case', 'true')
check('--flag true 生效', (rc, data(env).get('match_count')), (0, 1))
rc, env = run('find', FILE, '-s', '筛选', '--header', '-v', 'b', '--ignore-case=true')
check('--flag=true 仍然生效', (rc, data(env).get('match_count')), (0, 1))
rc, env = run('find', FILE, '-s', '筛选', '--header', '-v', 'b', '--ignore-case', 'yes')
check('真正的多余参数仍报错', failed(rc, env), (2, 'USAGE'))
check('报错点名了那个参数',
      'yes' in (env.get('error') or {}).get('message', ''), True)

# ── P3-2 参数能力缺口 ───────────────────────────────────────────
print('=== P3-2 索引 / 区间 / csv ===')
rc, env = run('read', FILE, '-s', '0', '-l', '1')
check('-s 索引选中第一张表', (rc, data(env).get('sheet')), (0, '筛选'))
rc, env = run('read', FILE, '-s', '1', '-l', '1')
check('-s 索引与 info 的 index 一致', data(env).get('sheet'), '日期')
rc, env = run('read', FILE, '-s', '筛选', '--header', '--columns', 'A:C', '-l', '1')
check('--columns 区间展开', data(env).get('columns'), ['A', 'B', 'C'])
rc, env = run('read', FILE, '-s', '筛选', '--header', '--columns', 'C:A', '-l', '1')
check('倒序区间报 USAGE', failed(rc, env), (2, 'COLUMN_NOT_FOUND'))
rc, env = run('read', FILE, '-s', '筛选', '--header', '--format', 'csv', '-l', '1')
check('--format csv 接受', (rc, data(env).get('format')), (0, 'csv'))
p = subprocess.run([EXE, 'read', FILE, '-s', '筛选', '--header', '--format', 'csv', '-l', '1'],
                   capture_output=True)
body = p.stdout.decode('utf-8').splitlines()[1:]
check('csv 首行是表头', body[0], '名称,金额,比率,状态')
p = subprocess.run([EXE, 'read', FILE, '-s', '筛选', '--header', '--format', 'markdown', '-l', '1'],
                   capture_output=True)
body = p.stdout.decode('utf-8').splitlines()[1:]
check('markdown 有分隔行', body[1], '| --- | --- | --- | --- |')

# ── P3-3 --raw 下的百分比等值比较 ───────────────────────────────
print('=== P3-3 百分比等值比较 ===')
for extra in ([], ['--raw']):
    rc, env = run('read', FILE, '-s', '筛选', '--header', '--where', '比率=25.67%',
                  '-l', '9', *extra)
    check('比率=25.67%% %s' % ('--raw' if extra else '默认  '),
          values(env, '名称'), ['C'])
rc, env = run('read', FILE, '-s', '筛选', '--header', '--where', '比率!=25.67%',
              '-l', '9', '--raw')
check('不等于仍然排除 C', values(env, '名称'), ['A', 'B'])

# ── P3-4 退出码分类 ─────────────────────────────────────────────
# 参数内容错误（改参数就能修）与运行期故障（改参数没用）必须能分开。
print('=== P3-4 退出码 ===')
cases = [
    (('read', FILE, '-s', '筛选', '--header', '--where', '不存在>100'), (2, 'COLUMN_NOT_FOUND')),
    (('read', FILE, '-s', '筛选', '--header', '--where', '金额>'), (2, 'USAGE')),
    (('read', FILE, '-s', '筛选', '--columns', '不存在', '-l', '1'), (2, 'COLUMN_NOT_FOUND')),
    (('read', FILE, '-s', '不存在', '-l', '1'), (2, 'SHEET_NOT_FOUND')),
    (('read', FILE, '-s', '筛选', '-l', '99999'), (2, 'USAGE')),
    # 运行期故障：文件或环境的问题，改参数没用，退出码仍是 1。
    (('read', 'testdata/does-not-exist.xlsx', '-l', '1'), (1, 'FILE_NOT_FOUND')),
    (('read', 'testdata/images/excel.png', '-l', '1'), (1, 'UNSUPPORTED_FORMAT')),
]
for args, want in cases:
    rc, env = run(*args)
    # 标签带上出错的那几个参数，比命令名更能定位是哪一条。
    check('%s -> %s' % (' '.join(args[2:])[:40], want[1]), failed(rc, env), want)

# ── P3-5 聚合字段命名规则 ───────────────────────────────────────
print('=== P3-5 字段命名 ===')
rc, env = run('agg', FILE, '-s', '括号列名', '--header',
              '--sum', '收入=[金额(万元)]', '--sum', '成本=[成本(万元)]')
check('显式命名不加前缀', data(env).get('fields'), ['收入', '成本'])
rc, env = run('agg', FILE, '-s', '括号列名', '--header',
              '--sum', '收入=[金额(万元)]', '--sum', '成本=[成本(万元)]',
              '--derive', 'x=(sum_收入-成本)/收入')
check('写错前缀报 USAGE', failed(rc, env), (2, 'USAGE'))
check('并给出正确字段名',
      'did you mean' in (env.get('error') or {}).get('message', ''), True)

print()
_common.finish(FAILURES, label='ROUND2')
