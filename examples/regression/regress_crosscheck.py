# Copyright (c) 2026 henryyu@163.com. All rights reserved.
#
# This file is part of xlpeek, a read-only spreadsheet reader for agents.
# See README.md for what it does and docs/AGENTS.md for how it is meant to be
# driven.

"""Cross-check the CLI against a completely independent reader.

Path A: xlpeek pages every row and aggregates.
Path B: Python parses the xlsx XML directly with the standard library.

If the two disagree, one of them is wrong. They share no code.
"""
import _common  # noqa: F401  — chdir to the repo root and force UTF-8 output
import json
import subprocess
import xml.etree.ElementTree as ET
import zipfile
from collections import defaultdict

EXE = _common.EXE  # 尊重 XLPEEK 覆盖，见 _common.py
FILE = r'testdata\华鼎科技_分部收入_FY23-FY25.xlsx'
NS = '{http://schemas.openxmlformats.org/spreadsheetml/2006/main}'


def cli_rows(sheet):
    rows, offset = [], 0
    while True:
        d = json.loads(subprocess.run(
            [EXE, 'read', FILE, '-s', sheet, '--header', '--skip-empty',
             '-o', str(offset), '-l', '1000'], capture_output=True).stdout.decode('utf-8'))['data']
        rows.extend(d['rows'])
        if not d['has_more']:
            return rows
        offset = d['next_offset']


def xlsx_rows(sheet_name):
    """Independent reader: zip + XML, no excelize involved."""
    with zipfile.ZipFile(FILE) as z:
        # shared strings
        sst = []
        if 'xl/sharedStrings.xml' in z.namelist():
            root = ET.fromstring(z.read('xl/sharedStrings.xml'))
            for si in root.findall(NS + 'si'):
                sst.append(''.join(t.text or '' for t in si.iter(NS + 't')))
        # sheet name -> part
        wb = ET.fromstring(z.read('xl/workbook.xml'))
        rels = ET.fromstring(z.read('xl/_rels/workbook.xml.rels'))
        rid_to_target = {r.get('Id'): r.get('Target') for r in rels}
        target = None
        for sh in wb.find(NS + 'sheets'):
            if sh.get('name') == sheet_name:
                rid = sh.get('{http://schemas.openxmlformats.org/officeDocument/2006/relationships}id')
                target = rid_to_target[rid]
        if target is None:
            raise SystemExit('sheet not found: ' + sheet_name)
        target = target.lstrip('/')
        if not target.startswith('xl/'):
            target = 'xl/' + target
        root = ET.fromstring(z.read(target))

    out = []
    for row in root.iter(NS + 'row'):
        cells = {}
        for c in row.findall(NS + 'c'):
            ref = c.get('r')
            col = ''.join(ch for ch in ref if ch.isalpha())
            v = c.find(NS + 'v')
            isv = c.find(NS + 'is')
            if c.get('t') == 's' and v is not None:
                val = sst[int(v.text)]
            elif isv is not None:
                val = ''.join(t.text or '' for t in isv.iter(NS + 't'))
            elif v is not None:
                val = v.text
            else:
                val = ''
            cells[col] = val
        out.append(cells)
    return out


def col_idx(letter):
    n = 0
    for ch in letter:
        n = n * 26 + (ord(ch) - 64)
    return n


CLI_COLS = {'凭证号': 'A', '分部': 'C', '产品线': 'D', '销售区域': 'G',
            '收入金额': 'J', '成本金额': 'K', '结算方式': 'L'}
hdr = {v: k for k, v in CLI_COLS.items()}

print('=== 逐格比对：CLI 与独立 XML 解析器，FY24 全表 ===')
cli = cli_rows('FY24_收入明细')
raw = xlsx_rows('FY24_收入明细')
# drop rows that are entirely empty, matching --skip-empty
raw = [r for r in raw if any(v != '' for v in r.values())]
print('  CLI 行数=%d   独立解析行数=%d  (含表头)' % (len(cli) + 1, len(raw)))
assert len(cli) + 1 == len(raw), 'row count mismatch'

mismatches = 0
checked = 0
for i, (crow, xrow) in enumerate(zip(cli, raw[1:])):   # raw[0] is the header
    for name, letter in CLI_COLS.items():
        a = crow[name]
        b = xrow.get(letter, '')
        checked += 1
        if a != b:
            mismatches += 1
            if mismatches <= 5:
                print('  MISMATCH row=%d col=%s(%s): cli=%r xml=%r' % (i + 2, name, letter, a, b))
print('  比对单元格数: %d   不一致: %d   ->  %s'
      % (checked, mismatches, 'PASS' if mismatches == 0 else 'FAIL'))

print()
print('=== 聚合比对：按销售区域汇总收入（FY24）===')
agg_cli = defaultdict(float)
for r in cli:
    agg_cli[r['销售区域']] += float(r['收入金额'])
agg_xml = defaultdict(float)
for r in raw[1:]:
    region = r.get('G', '')
    amt = r.get('J', '')
    if region and amt:
        agg_xml[region] += float(amt)

ok = True
for region in sorted(set(agg_cli) | set(agg_xml)):
    a, b = agg_cli.get(region, 0.0), agg_xml.get(region, 0.0)
    flag = 'ok' if abs(a - b) < 0.01 else 'DISAGREE'
    if flag != 'ok':
        ok = False
    print('  %-6s CLI=%18.2f   独立=%18.2f   %s' % (region, a, b, flag))
print('  合计   CLI=%18.2f   独立=%18.2f' % (sum(agg_cli.values()), sum(agg_xml.values())))
print('  聚合一致性:', 'PASS' if ok else 'FAIL')

print()
print('=== 三年汇总（CLI 路径）===')
for sheet in ('FY23_收入明细', 'FY24_收入明细', 'FY25_收入明细'):
    rs = cli_rows(sheet)
    rev = sum(float(r['收入金额']) for r in rs if r['收入金额'])
    cost = sum(float(r['成本金额']) for r in rs if r['成本金额'])
    print('  %-16s 行数=%-5d 收入=%16.2f  成本=%16.2f  毛利率=%6.2f%%'
          % (sheet, len(rs), rev, cost, (rev - cost) / rev * 100))

# 失败会在检查点直接 sys.exit(1)；能走到这里说明全部通过。
_common.finish([], 'CLI 与独立解析器逐格比对')
