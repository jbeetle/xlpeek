# Copyright (c) 2026 henryyu@163.com. All rights reserved.
#
# This file is part of xlpeek, a read-only spreadsheet reader for agents.
# See README.md for what it does and docs/AGENTS.md for how it is meant to be
# driven.

"""Every runnable command line in the docs must actually work.

Documentation that has drifted from the tool is worse than none: the agent
follows it, gets an error, and starts guessing. This extracts the commands and
runs them, mapping the docs' illustrative names onto the real fixture.

Tokens are split first and substituted per token, so that a Windows path's
backslashes never pass through shlex's escape handling.
"""
import _common  # noqa: F401  — chdir to the repo root and force UTF-8 output
import json
import re
import shlex
import subprocess

EXE = _common.EXE  # 尊重 XLPEEK 覆盖，见 _common.py
REAL = 'testdata/华鼎科技_分部收入_FY23-FY25.xlsx'
FAILURES = []

# The docs use generic names in their examples; the fixture uses real ones.
SUBS = [
    (r'^xlpeek$', EXE),
    (r'^book\.xlsx$', REAL),
    (r'^report\.xlsx$', REAL),
    (r'^Sheet1$', 'FY24_收入明细'),
    (r'^明细$', 'FY24_收入明细'),
    (r'^目标表$', 'FY24_收入明细'),
    (r'^订单号(,.*)?$', lambda m: '凭证号' + (m.group(1) or '')),
    (r'^金额(>.*)?$', lambda m: '收入金额' + (m.group(1) or '')),
    (r'^状态$', '销售区域'),
    (r'^已取消$', '华北'),
    (r'^张三$', '北方智造装备集团有限公司'),
    (r'^sum_金额$', 'sum_收入金额'),
]


def extract(path):
    text = open(path, encoding='utf-8').read()
    commands = []
    for block in re.findall(r'```(?:bash|sh)\n(.*?)```', text, re.S):
        joined = block.replace('\\\n', ' ')
        for line in joined.split('\n'):
            line = line.split('#')[0].strip()
            if line.startswith('xlpeek '):
                commands.append(line)
    return commands


def runnable(cmd):
    if '{"command"' in cmd or cmd.strip() == 'xlpeek serve':
        return False
    if any(ch in cmd for ch in '<>[]'):
        return False
    return True


def substitute(token):
    # Some names appear *inside* an argument rather than as one — a sheet name
    # written in a formula, where the token is the whole formula — so they are
    # rewritten before the anchored patterns get a look at it.
    for old, new in (('目标表', 'FY24_收入明细'),):
        token = token.replace(old, new)
    # A comma-separated flag value carries several names in one token, as in
    # --columns "订单号,金额", so each part has to be mapped separately.
    if ',' in token:
        return ','.join(substitute(part) for part in token.split(','))
    for pattern, replacement in SUBS:
        m = re.match(pattern, token)
        if m:
            return replacement(m) if callable(replacement) else replacement
    return token


checked = failed = 0
for doc in ('docs/AGENTS.md', 'README.md'):
    commands = [c for c in extract(doc) if runnable(c)]
    print('=== %s: %d 条可执行命令 ===' % (doc, len(commands)))
    for raw in commands:
        try:
            args = [substitute(tok) for tok in shlex.split(raw)]
        except ValueError as exc:
            FAILURES.append('%s\n      -> 解析失败: %s' % (raw, exc))
            failed += 1
            continue
        checked += 1
        try:
            p = subprocess.run(args, capture_output=True, timeout=180)
        except Exception as exc:                      # noqa: BLE001 - report anything
            FAILURES.append('%s\n      -> 无法执行: %s' % (raw, exc))
            failed += 1
            continue
        out = p.stdout.decode('utf-8', 'replace')
        first = out.split('\n', 1)[0] if out else ''
        try:
            ok = json.loads(first).get('ok', False)
        except json.JSONDecodeError:
            ok = False
        if not ok:
            failed += 1
            FAILURES.append('%s\n      -> %s' % (raw, first[:120]))

print('实际执行 %d 条，失败 %d 条' % (checked, failed))
for f in FAILURES:
    print('  ✗', f)
_common.finish(FAILURES, '文档可执行性')
