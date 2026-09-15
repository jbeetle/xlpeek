# Copyright (c) 2026 henryyu@163.com. All rights reserved.
#
# This file is part of xlpeek, a read-only spreadsheet reader for agents.
# See README.md for what it does and docs/AGENTS.md for how it is meant to be
# driven.

"""Cross-check the new commands: profile statistics, merged cells, header-row, TSV."""
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
    """Parse line 1 as the envelope; the rest of stdout may be a TSV body."""
    p = subprocess.run([EXE] + args, capture_output=True)
    raw = p.stdout.decode('utf-8')
    try:
        env = json.loads(raw.split('\n', 1)[0])
    except json.JSONDecodeError:
        print('NOT JSON:', raw[:200]); sys.exit(1)
    if expect_ok and not env['ok']:
        print('UNEXPECTED FAILURE:', env['error']); sys.exit(1)
    return env, raw, p.returncode


def check(name, got, want):
    if isinstance(want, float) and isinstance(got, (int, float)):
        # Means are reported to 15 significant digits, so a fixed absolute
        # tolerance is meaningless at this magnitude; compare relatively.
        scale = max(abs(want), 1e-12)
        ok = abs(got - want) / scale < 1e-12
    else:
        ok = got == want
    print('  %-40s got=%-26s want=%-26s %s' % (name, got, want, 'ok' if ok else 'FAIL'))
    if not ok:
        FAILURES.append(name)


def xlsx_grid(sheet_name):
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
LETTERS = 'ABCDEFGHIJKLM'

print('=== A. profile 统计量 与 独立 XML 解析比对 ===')
env, _, _ = run(['profile', FILE, '-s', SHEET, '--header', '--max-values', '0'])
cols = {c['column']: c for c in env['data']['columns']}
print('  列数: profile=%d 独立=%d' % (len(cols), len(LETTERS)))
for letter in LETTERS:
    values = [r.get(letter, '') for r in data]
    nonempty = [v for v in values if v != '']
    c = cols[letter]
    check('  %s %s 去重数' % (letter, c.get('name', '')), c['distinct'], len(set(nonempty)))
    check('  %s 非空数' % letter, c['non_empty'], len(nonempty))
    check('  %s 空值数' % letter, c['empty'], len(values) - len(nonempty))
    nums = []
    for v in nonempty:
        try:
            nums.append(float(v.replace(',', '')))
        except ValueError:
            pass
    if nums and c['type'] == 'number':
        check('  %s 最小值' % letter, c['min'], min(nums))
        check('  %s 最大值' % letter, c['max'], max(nums))
        check('  %s 均值' % letter, c['mean'], sum(nums) / len(nums))
    elif c['type'] == 'number':
        FAILURES.append('%s typed number but no numeric values' % letter)

print()
print('=== B. profile 取值枚举（--max-values 生效）===')
env, _, _ = run(['profile', FILE, '-s', SHEET, '--header', '--max-values', '20'])
region = [c for c in env['data']['columns'] if c['column'] == 'G'][0]
enumerated = {v['value']: v['count'] for v in region['top_values']}
expect_counts = {}
for r in data:
    g = r.get('G', '')
    if g:
        expect_counts[g] = expect_counts.get(g, 0) + 1
check('销售区域 枚举数', len(enumerated), len(expect_counts))
for region_name, count in expect_counts.items():
    check('  %s 计数' % region_name, enumerated.get(region_name), count)
currency = [c for c in env['data']['columns'] if c['column'] == 'H'][0]
check('币种 枚举数', len(currency['top_values']), 3)
remarks = [c for c in env['data']['columns'] if c['column'] == 'M'][0]
check('备注 枚举数', len(remarks['top_values']), 1)
check('备注 填充率', round(remarks['fill_rate'], 4), round(86 / len(data), 4))

print()
print('=== C. profile --where 与 --columns ===')
env, _, _ = run(['profile', FILE, '-s', SHEET, '--header',
                 '--where', '销售区域=华北', '--columns', '凭证号,收入金额'])
check('过滤后行数', env['data']['rows_matched'], 378)
check('只剖析请求的列', [c['column'] for c in env['data']['columns']], ['A', 'J'])

print()
print('=== D. 合并单元格 --fill-merged ===')
MERGED = r'testdata\MergeCell.xlsx'
env, _, _ = run(['read', MERGED, '-s', 'Sheet1', '-l', '4'])
plain = env['data']['rows']
env2, _, _ = run(['read', MERGED, '-s', 'Sheet1', '--fill-merged', '-l', '4'])
filled = env2['data']['rows']
check('未填充时 B1 为空', plain[0][1], '')
check('填充后 B1 = A1', filled[0][1], plain[0][0])
check('未填充时 A3 为空', plain[2][0], '')
check('填充后 A3 = A2 (纵向合并)', filled[2][0], plain[1][0])
check('已有值不被覆盖 B2', filled[1][1], plain[1][1])
env3, _, _ = run(['info', MERGED, '--deep'])
check('info --deep 报告合并数', env3['data']['sheets'][0]['merged_ranges'], 4)

print()
print('=== E. --header-row ===')
# Checked in next to this suite — a report whose table header sits below a
# title block, which is what --header-row exists for. Rows 1-2 are the title
# and the unit line; row 3 is the header; rows 4-6 are data.
HDR = 'examples/regression/fixtures/header_row.xlsx'
env, _, _ = run(['read', HDR, '-s', 'Sheet1', '--header-row', '3'])
check('表头行', env['data']['header'], ['区域', '收入', '成本'])
check('首数据行', env['data']['first_row'], 4)
check('数据行数', env['data']['rows_returned'], 3)
env, _, _ = run(['agg', HDR, '-s', 'Sheet1', '--header-row', '3', '--sum', '收入', '--count'])
check('agg 汇总收入', env['data']['rows'][0]['sum_收入'], 3600)
check('agg 计数', env['data']['rows'][0]['count'], 3)
env, _, _ = run(['profile', HDR, '-s', 'Sheet1', '--header-row', '3'])
check('profile 列名', [c.get('name') for c in env['data']['columns']], ['区域', '收入', '成本'])
env, _, _ = run(['find', HDR, '-s', 'Sheet1', '--header-row', '3', '-v', '华东'])
check('find 列名标注', env['data']['matches'][0]['column_name'], '区域')

print()
print('=== F. TSV 输出版式 ===')
env, raw, _ = run(['read', FILE, '-s', SHEET, '--header',
                   '--columns', '凭证号,收入金额', '-l', '3', '--format', 'tsv'])
lines = raw.split('\n')
envelope = json.loads(lines[0])
check('首行是 JSON 信封', envelope['ok'], True)
check('信封 format 字段', envelope['data']['format'], 'tsv')
check('信封中 rows 为 null', envelope['data']['rows'], None)
check('分页元数据仍在信封中', envelope['data']['has_more'], True)
check('第二行是表头', lines[1], '凭证号\t收入金额')
check('第三行是数据', lines[2].split('\t')[0], 'FI-24000001')
check('行数 = 1 信头 + 1 表头 + 3 数据 + 末尾空行', len(lines), 6)

# 含制表符/换行的字段必须被引号包裹，否则表格会错位
env, raw, _ = run(['read', HDR, '-s', 'Sheet1', '--header-row', '3', '--format', 'tsv'])
first_body = raw.split('\n')[1]
check('TSV 表头正确', first_body, '区域\t收入\t成本')

_common.finish(FAILURES, '新功能交叉验证')
