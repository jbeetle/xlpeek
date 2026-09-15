# Copyright (c) 2026 henryyu@163.com. All rights reserved.
#
# This file is part of xlpeek, a read-only spreadsheet reader for agents.
# See README.md for what it does and docs/AGENTS.md for how it is meant to be
# driven.

"""Determine whether every cell mismatch is explained by 15-significant-digit rounding."""
import _common  # noqa: F401  — chdir to the repo root and force UTF-8 output
import json
import subprocess
import xml.etree.ElementTree as ET
import zipfile

EXE = _common.EXE  # 尊重 XLPEEK 覆盖，见 _common.py
FILE = r'testdata\华鼎科技_分部收入_FY23-FY25.xlsx'
NS = '{http://schemas.openxmlformats.org/spreadsheetml/2006/main}'


def cli_rows(sheet, extra=None):
    rows, offset = [], 0
    while True:
        d = json.loads(subprocess.run(
            [EXE, 'read', FILE, '-s', sheet, '--header', '--skip-empty',
             '-o', str(offset), '-l', '1000'] + (extra or []),
            capture_output=True).stdout.decode('utf-8'))['data']
        rows.extend(d['rows'])
        if not d['has_more']:
            return rows
        offset = d['next_offset']


def xml_numeric(sheet_name):
    """Return {'K9': '677675.5699999999', ...} straight from the stored XML."""
    with zipfile.ZipFile(FILE) as z:
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
    out = {}
    for row in root.iter(NS + 'row'):
        for c in row.findall(NS + 'c'):
            if c.get('t') in (None, 'n'):
                v = c.find(NS + 'v')
                if v is not None and v.text:
                    out[c.get('r')] = v.text
    return out


def sig15(x):
    """Round a float to 15 significant digits, the way Excel and excelize do."""
    if x == 0:
        return 0.0
    return float('%.15g' % x)


SHEET = 'FY24_收入明细'
COLS = {'J': '收入金额', 'K': '成本金额'}

cli = cli_rows(SHEET)
xraw = xml_numeric(SHEET)

total = exact = normalised = unexplained = 0
samples = []
for i, crow in enumerate(cli):
    rownum = i + 2                      # worksheet row: header is row 1
    for letter, name in COLS.items():
        xv = xraw.get('%s%d' % (letter, rownum))
        if xv is None:
            continue
        cv = crow[name]
        total += 1
        if cv == xv:
            exact += 1
            continue
        try:
            same = sig15(float(cv)) == sig15(float(xv))
        except ValueError:
            same = False
        if same:
            normalised += 1
            if len(samples) < 4:
                samples.append(('%s%d' % (letter, rownum), cv, xv, '%.15g' % float(xv)))
        else:
            unexplained += 1
            if unexplained <= 5:
                print('  UNEXPLAINED %s%d: cli=%r xml=%r' % (letter, rownum, cv, xv))

print('=== 数值单元格比对：CLI vs 原始 XML 文本（FY24 全表）===')
print('  比对单元格数            : %d' % total)
print('  与XML文本完全一致       : %d' % exact)
print('  差异                    : %d' % (normalised + unexplained))
print('    其中=15位有效数字规范化: %d' % normalised)
print('    无法解释的差异        : %d' % unexplained)
print()
if samples:
    print('  差异样例（CLI 值 == XML 值取 15 位有效数字）:')
    for cell, cv, xv, s15 in samples:
        print('    %-6s cli=%-14s xml=%-22s xml取15位=%s' % (cell, cv, xv, s15))
print()
print('  结论:', 'ALL EXPLAINED — 数据无误，仅精度规范化'
      if unexplained == 0 else 'REAL DISCREPANCIES PRESENT')

print()
print('=== --raw 是否绕过该规范化？（工作簿第 9 行 K 列）===')
print('  XML 原始存储值 : %s' % xraw.get('K9'))
for extra, label in (([], '默认  '), (['--raw'], '--raw ')):
    d = cli_rows(SHEET, extra)
    print('  %s         : %s' % (label, d[7]['成本金额']))

# 每一处差异都必须能被「15 位有效数字规范化」解释；有解释不了的
# 就说明读取本身有问题，而不是精度显示问题。
_common.finish(
    [] if unexplained == 0 else ['%d 处差异无法用规范化解释' % unexplained],
    '精度规范化')
