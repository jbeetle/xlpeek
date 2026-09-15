# Copyright (c) 2026 henryyu@163.com. All rights reserved.
#
# This file is part of xlpeek, a read-only spreadsheet reader for agents.
# See README.md for what it does and docs/AGENTS.md for how it is meant to be
# driven.

"""Prove --where is correct by independently recounting from a full scan."""
import _common  # noqa: F401  — chdir to the repo root and force UTF-8 output
import json
import subprocess
import sys

EXE = _common.EXE  # 尊重 XLPEEK 覆盖，见 _common.py
FILE = r'testdata\华鼎科技_分部收入_FY23-FY25.xlsx'
SHEET = 'FY23_收入明细'


def run(args):
    p = subprocess.run([EXE] + args, capture_output=True)
    if p.returncode != 0:
        sys.stderr.write('FAILED %s\n%s\n' % (args, p.stdout.decode('utf-8', 'replace')))
        sys.exit(1)
    return json.loads(p.stdout.decode('utf-8'))['data']


def page_filtered(where, size=700):
    rows, offset = [], 0
    while True:
        d = run(['read', FILE, '-s', SHEET, '--header', '--skip-empty',
                 '-o', str(offset), '-l', str(size)] +
                sum([['--where', w] for w in where], []))
        rows.extend(d['rows'])
        if not d['has_more']:
            return rows
        offset = d['next_offset']


print('=== A. 取全量基准（不过滤）===')
all_rows = page_filtered([])
print('  total rows: %d' % len(all_rows))
print('  列名:', list(all_rows[0].keys()))
print()


def check(name, where, predicate):
    got = page_filtered(where)
    expect = [r for r in all_rows if predicate(r)]
    ok = len(got) == len(expect)
    # 逐行比对凭证号，而不只是比数量
    if ok:
        ok = [r['凭证号'] for r in got] == [r['凭证号'] for r in expect]
    # 再验证每一条返回的行确实满足条件
    if ok:
        ok = all(predicate(r) for r in got)
    print('  %-34s got=%-5d expect=%-5d  %s' % (name, len(got), len(expect), 'PASS' if ok else 'FAIL'))
    return ok


def amount(r):
    return float(r['收入金额'])


print('=== B. 过滤正确性（与全量扫描独立复算比对）===')
results = []
results.append(check('收入金额>3000000', ['收入金额>3000000'], lambda r: amount(r) > 3000000))
results.append(check('收入金额>=1000000', ['收入金额>=1000000'], lambda r: amount(r) >= 1000000))
results.append(check('收入金额<100000', ['收入金额<100000'], lambda r: amount(r) < 100000))
results.append(check('销售区域=华北', ['销售区域=华北'], lambda r: r['销售区域'] == '华北'))
results.append(check('销售区域!=华北', ['销售区域!=华北'], lambda r: r['销售区域'] != '华北'))
results.append(check('客户名称~香港', ['客户名称~香港'], lambda r: '香港' in r['客户名称']))
results.append(check('结算方式~承兑', ['结算方式~承兑'], lambda r: '承兑' in r['结算方式']))
results.append(check('组合: 华北 AND 收入>2000000',
                     ['销售区域=华北', '收入金额>2000000'],
                     lambda r: r['销售区域'] == '华北' and amount(r) > 2000000))
results.append(check('组合: ~香港 AND 收入>4000000',
                     ['客户名称~香港', '收入金额>4000000'],
                     lambda r: '香港' in r['客户名称'] and amount(r) > 4000000))
FAILURES = []
if not all(results):
    FAILURES.append('B 段 %d 项过滤表达式与全量复算不一致' % results.count(False))
print()
print('过滤器正确性:', 'ALL PASS' if all(results) else 'FAILURES PRESENT')

print()
print('=== C. offset 在过滤下数的是“命中行”（SQL 语义）===')
d = page_filtered(['销售区域=华北'])
print('  华北 命中 %d 行' % len(d))
first = run(['read', FILE, '-s', SHEET, '--header', '--skip-empty',
             '--where', '销售区域=华北', '-o', '0', '-l', '3'])
second = run(['read', FILE, '-s', SHEET, '--header', '--skip-empty',
              '--where', '销售区域=华北', '-o', '3', '-l', '3'])
p1 = [r['凭证号'] for r in first['rows']]
p2 = [r['凭证号'] for r in second['rows']]
expect_p2 = [r['凭证号'] for r in d[3:6]]
print('  page1:', p1)
print('  page2:', p2)
offset_ok = p2 == expect_p2
if not offset_ok:
    FAILURES.append('C 段 offset=3 的命中与全量命中的第 4-6 条不符')
print('  offset=3 得到的应与全量命中列表的第 4-6 条一致:',
      'PASS' if offset_ok else 'FAIL')
print('  first_row 跳号（证明 offset 数的是命中行而非物理行）:',
      '%s -> %s' % (first['first_row'], second['first_row']))

print()
print('=== D. --columns 投影 ===')
d = run(['read', FILE, '-s', SHEET, '--header', '--columns', '凭证号,收入金额,销售区域', '-l', '2'])
print('  columns:', d['columns'])
print('  keys   :', list(d['rows'][0].keys()))
projection_ok = list(d['rows'][0].keys()) == ['凭证号', '收入金额', '销售区域']
if not projection_ok:
    FAILURES.append('D 段列投影的键或顺序不符')
print('  只返回请求的列且顺序保持:', 'PASS' if projection_ok else 'FAIL')
d = run(['read', FILE, '-s', SHEET, '--columns', 'C,J,2', '-l', '1'])
print('  按列字母/序号投影 C,J,2 ->', d['columns'], '=', d['rows'][0])

print()
print('=== E. --raw 原始值 ===')
d = run(['read', FILE, '-s', SHEET, '--header', '--columns', '过账日期', '-l', '1', '--raw'])
print('  --raw  过账日期 =', d['rows'][0])
d = run(['read', FILE, '-s', SHEET, '--header', '--columns', '过账日期', '-l', '1'])
print('  默认   过账日期 =', d['rows'][0])

_common.finish(FAILURES, '过滤器正确性')
