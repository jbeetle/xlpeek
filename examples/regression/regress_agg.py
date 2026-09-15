# Copyright (c) 2026 henryyu@163.com. All rights reserved.
#
# This file is part of xlpeek, a read-only spreadsheet reader for agents.
# See README.md for what it does and docs/AGENTS.md for how it is meant to be
# driven.

"""Verify agg against an independent XML-parse aggregation, plus expressions, filters and paging."""
import _common  # noqa: F401  — chdir to the repo root and force UTF-8 output
import json
import subprocess
import sys
import xml.etree.ElementTree as ET
import zipfile

EXE = _common.EXE  # 尊重 XLPEEK 覆盖，见 _common.py
FILE = r'testdata\华鼎科技_分部收入_FY23-FY25.xlsx'
NS = '{http://schemas.openxmlformats.org/spreadsheetml/2006/main}'
FAILURES = []


def run(args, expect_ok=True):
    p = subprocess.run([EXE] + args, capture_output=True)
    raw = p.stdout.decode('utf-8')
    try:
        env = json.loads(raw)
    except json.JSONDecodeError:
        print('NOT JSON:', raw[:200])
        sys.exit(1)
    if expect_ok and not env['ok']:
        print('UNEXPECTED FAILURE:', env['error'])
        sys.exit(1)
    return env, p.returncode


def check(name, got, want, tol=0.0):
    """tol is an absolute tolerance; money comparisons use half a cent."""
    if isinstance(want, float) and isinstance(got, (int, float)):
        ok = abs(got - want) <= tol
    else:
        ok = got == want
    shown = ('%r' % want) if not isinstance(want, float) else ('%.12g' % want)
    gshown = ('%r' % got) if not isinstance(got, float) else ('%.12g' % got)
    print('  %-42s got=%-24s want=%-24s %s'
          % (name, gshown, shown, 'ok' if ok else 'FAIL'))
    if not ok:
        FAILURES.append(name)


def xlsx_grid(sheet_name):
    """Independent reader: zip + XML, no excelize."""
    with zipfile.ZipFile(FILE) as z:
        sst = []
        if 'xl/sharedStrings.xml' in z.namelist():
            root = ET.fromstring(z.read('xl/sharedStrings.xml'))
            for si in root.findall(NS + 'si'):
                sst.append(''.join(t.text or '' for t in si.iter(NS + 't')))
        wb = ET.fromstring(z.read('xl/workbook.xml'))
        rels = ET.fromstring(z.read('xl/_rels/workbook.xml.rels'))
        targets = {r.get('Id'): r.get('Target') for r in rels}
        t = None
        for sh in wb.find(NS + 'sheets'):
            if sh.get('name') == sheet_name:
                t = targets[sh.get(
                    '{http://schemas.openxmlformats.org/officeDocument/2006/relationships}id')]
        t = t.lstrip('/')
        if not t.startswith('xl/'):
            t = 'xl/' + t
        root = ET.fromstring(z.read(t))
    grid = []
    for row in root.iter(NS + 'row'):
        cells = {}
        for c in row.findall(NS + 'c'):
            col = ''.join(ch for ch in c.get('r') if ch.isalpha())
            v = c.find(NS + 'v')
            isv = c.find(NS + 'is')
            if c.get('t') == 's' and v is not None:
                val = sst[int(v.text)]
            elif isv is not None:
                val = ''.join(x.text or '' for x in isv.iter(NS + 't'))
            elif v is not None:
                val = v.text
            else:
                val = ''
            cells[col] = val
        grid.append(cells)
    return grid


SHEET = 'FY24_收入明细'
grid = xlsx_grid(SHEET)
data = [r for r in grid[1:] if any(v != '' for v in r.values())]

print('=== A. agg 与独立 XML 解析逐区域比对（收入）===')
env, _ = run(['agg', FILE, '-s', SHEET, '--header', '--group-by', '销售区域',
              '--sum', '收入金额', '--sum', '成本金额', '--count'])
rows = env['data']['rows']
agg = {r['销售区域']: r for r in rows}

expect = {}
for r in data:
    region = r.get('G', '')
    if not region:
        continue
    e = expect.setdefault(region, {'rev': 0.0, 'cost': 0.0, 'n': 0})
    if r.get('J'):
        e['rev'] += float(r['J'])
    if r.get('K'):
        e['cost'] += float(r['K'])
    e['n'] += 1

print('  区域数: agg=%d 独立=%d' % (len(agg), len(expect)))
check('group count', len(agg), len(expect))
for region in sorted(expect):
    a, e = agg.get(region), expect[region]
    check('  %s 收入' % region, a['sum_收入金额'], round(float('%.15g' % e['rev']), 12))
    check('  %s 成本' % region, a['sum_成本金额'], round(float('%.15g' % e['cost']), 12))
    check('  %s 行数' % region, a['count'], e['n'])

print()
print('=== B. 总计（不带 --group-by）===')
env, _ = run(['agg', FILE, '-s', SHEET, '--header', '--sum', '收入金额', '--count'])
total_agg = env['data']['rows'][0]
total_xml = sum(float(r['J']) for r in data if r.get('J'))
check('grand total revenue', total_agg['sum_收入金额'], total_xml, tol=0.005)
check('grand total rows', total_agg['count'], len(data))

print()
print('=== C. 表达式聚合 --sum "净利=收入金额-成本金额" ===')
env, _ = run(['agg', FILE, '-s', SHEET, '--header', '--sum', '净利=收入金额-成本金额'])
net = env['data']['rows'][0]['净利']
want = sum((float(r['J']) - float(r['K'])) for r in data if r.get('J') and r.get('K'))
check('net profit', net, want, tol=0.005)

print()
print('=== D. 过滤 + 聚合（只算华北）===')
env, _ = run(['agg', FILE, '-s', SHEET, '--header', '--where', '销售区域=华北', '--sum', '收入金额', '--count'])
got = env['data']['rows'][0]
want_rev = sum(float(r['J']) for r in data if r.get('G') == '华北' and r.get('J'))
want_n = sum(1 for r in data if r.get('G') == '华北')
check('华北 revenue', got['sum_收入金额'], want_rev, tol=0.005)
check('华北 rows', got['count'], want_n)
check('rows_matched', env['data']['rows_matched'], want_n)

print()
print('=== E. --sort-by + --limit = top N ===')
env, _ = run(['agg', FILE, '-s', SHEET, '--header', '--group-by', '销售区域',
              '--sum', '收入金额', '--sort-by', 'sum_收入金额', '--limit', '3'])
top = [r['销售区域'] for r in env['data']['rows']]
want_top = sorted(expect, key=lambda k: -expect[k]['rev'])[:3]
check('top 3 by revenue', top, want_top)
check('has_more', env['data']['has_more'], True)
check('next_offset', env['data']['next_offset'], 3)

print()
print('=== F. 派生指标 ===')
env, _ = run(['agg', FILE, '-s', SHEET, '--header', '--group-by', '销售区域',
              '--sum', '收入金额', '--sum', '成本金额',
              '--derive', '毛利率=(sum_收入金额-sum_成本金额)/sum_收入金额'])
margins = {k: (v['rev'] - v['cost']) / v['rev'] for k, v in expect.items()}
best = max(env['data']['rows'], key=lambda r: r['毛利率'])
worst = min(env['data']['rows'], key=lambda r: r['毛利率'])
want_best = max(margins, key=lambda k: margins[k])
want_worst = min(margins, key=lambda k: margins[k])
check('best margin region', best['销售区域'], want_best)
check('  its margin', best['毛利率'], margins[want_best], tol=1e-9)
check('worst margin region', worst['销售区域'], want_worst)
check('  its margin', worst['毛利率'], margins[want_worst], tol=1e-9)

print()
print('=== G. 多列分组 ===')
env, _ = run(['agg', FILE, '-s', SHEET, '--header', '--group-by', '销售区域,产品线',
              '--sum', '收入金额', '--count'])
check('multi-group count', env['data']['group_count'], len(env['data']['rows']))
check('group_by field', env['data']['group_by'], ['销售区域', '产品线'])

print()
print('=== H. 错误路径 ===')
for args, want_code in (
    (['--group-by', '不存在列', '--sum', '收入金额'], 'COLUMN_NOT_FOUND'),
    (['--sum', '不存在列'], 'COLUMN_NOT_FOUND'),
    (['--sum', '收入金额', '--derive', 'x=sum_不存在'], 'USAGE'),
    (['--sum', '收入金额+'], 'USAGE'),
    (['--sum', '收入金额', '--sort-by', '无此字段'], 'USAGE'),
    ([], 'USAGE'),
):
    env, code = run(['agg', FILE, '-s', SHEET, '--header'] + args, expect_ok=False)
    got = env['error']['code']
    print('  %-46s -> %-22s %s' % (' '.join(args)[:46], got, 'ok' if got == want_code else 'FAIL'))
    if got != want_code:
        FAILURES.append('error path: ' + ' '.join(args))

_common.finish(FAILURES, '聚合交叉验证')
