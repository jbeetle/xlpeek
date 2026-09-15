# Copyright (c) 2026 henryyu@163.com. All rights reserved.
#
# This file is part of xlpeek, a read-only spreadsheet reader for agents.
# See README.md for what it does and docs/AGENTS.md for how it is meant to be
# driven.

"""Regression over every small file in test/.

The contract under test is not "these files parse" — it is "no input makes the
tool crash, and every invocation yields exactly one parseable envelope with a
known error code". A Go panic would print a stack trace to stderr and emit
nothing on stdout, which is the failure this is looking for.
"""
import _common  # noqa: F401  — chdir to the repo root and force UTF-8 output
import json
import os
import subprocess
import sys

EXE = _common.EXE  # 尊重 XLPEEK 覆盖，见 _common.py
TEST = r'testdata'
FAILURES = []
KNOWN_CODES = {'USAGE', 'FILE_NOT_FOUND', 'SHEET_NOT_FOUND', 'INVALID_PASSWORD',
               'PASSWORD_REQUIRED', 'UNSUPPORTED_FORMAT', 'COLUMN_NOT_FOUND', 'READ_ERROR'}

SMALL_FILES = ['BadWorkbook.xlsx', 'Book1.xlsx', 'CalcChain.xlsx', 'MergeCell.xlsx',
               'OverflowNumericCell.xlsx', 'SharedStrings.xlsx',
               'encryptAES.xlsx', 'encryptSHA1.xlsx', 'encryptSHA512.xlsx',
               'vbaProject.bin']
# The large file is covered by its own regression suites.
PASSWORDS = {'encryptAES.xlsx': 'password', 'encryptSHA1.xlsx': 'password',
             'encryptSHA512.xlsx': 'password'}


def run(args, timeout=120):
    p = subprocess.run([EXE] + args, capture_output=True, timeout=timeout)
    out = p.stdout.decode('utf-8', 'replace')
    err = p.stderr.decode('utf-8', 'replace')
    return p.returncode, out, err


def fail(msg):
    print('    FAIL:', msg)
    FAILURES.append(msg)


def check_envelope(label, args, expect_ok=None, expect_code=None):
    """Run one command and assert the envelope contract holds."""
    try:
        code, out, err = run(args)
    except subprocess.TimeoutExpired:
        fail('%s: TIMED OUT' % label)
        return None

    if 'panic:' in err or 'goroutine ' in err:
        fail('%s: PANICKED\n%s' % (label, err[:400]))
        return None
    lines = [l for l in out.split('\n') if l.strip()]
    if not lines:
        fail('%s: no output on stdout (exit %d, stderr %r)' % (label, code, err[:120]))
        return None
    try:
        env = json.loads(lines[0])
    except json.JSONDecodeError as exc:
        fail('%s: first stdout line is not JSON (%s): %r' % (label, exc, lines[0][:120]))
        return None

    if 'ok' not in env:
        fail('%s: envelope has no ok field' % label)
        return None
    if env['ok']:
        if expect_ok is False:
            fail('%s: expected failure, got ok' % label)
        if code != 0:
            fail('%s: ok envelope but exit code %d' % (label, code))
    else:
        if expect_code and env.get('error', {}).get('code') != expect_code:
            fail('%s: expected %s, got %s' % (label, expect_code, env['error'].get('code')))
        got = env.get('error', {}).get('code')
        if got not in KNOWN_CODES:
            fail('%s: unknown error code %r' % (label, got))
        if code == 0:
            fail('%s: failure envelope but exit code 0' % label)
    return env


print('=' * 72)
print('A. 每个文件：所有命令都不得崩溃，且必须给出合法信封')
print('=' * 72)
for name in SMALL_FILES:
    path = os.path.join(TEST, name)
    if not os.path.exists(path):
        fail('%s: missing' % name)
        continue
    pw = PASSWORDS.get(name)
    suffix = ['--password', pw] if pw else []
    results = []
    for label, args in (
        ('info',   ['info', path] + suffix),
        ('deep',   ['info', path, '--deep'] + suffix),
        ('read',   ['read', path] + suffix),
        ('agent',  ['read', path, '--header', '-l', '5'] + suffix),
        ('agg',    ['agg', path, '--sum', 'A'] + suffix),
        ('profile',['profile', path] + suffix),
        ('find',   ['find', path, '-v', 'a'] + suffix),
    ):
        env = check_envelope('%s/%s' % (name, label), args)
        results.append('ok' if env and env['ok'] else
                       ('err:' + env['error']['code'] if env else 'CRASH'))
    print('  %-26s %s' % (name, '  '.join(results)))

print()
print('=' * 72)
print('B. 加密工作簿（此前未覆盖）')
print('=' * 72)
for name in ('encryptAES.xlsx', 'encryptSHA1.xlsx', 'encryptSHA512.xlsx'):
    path = os.path.join(TEST, name)
    print('  --- %s' % name)
    env = check_envelope('%s no password' % name, ['info', path])
    print('      无密码      -> %s' % (env['error']['code'] if env and not env['ok'] else 'ok'))
    env = check_envelope('%s wrong password' % name, ['info', path, '--password', 'passwd'])
    print('      错误密码    -> %s' % (env['error']['code'] if env and not env['ok'] else 'ok'))
    env = check_envelope('%s right password' % name, ['info', path, '--password', 'password'])
    if env and env['ok']:
        sheets = [s['name'] for s in env['data']['sheets']]
        print('      正确密码    -> ok, sheets=%s' % sheets)
        # 解密后必须能真正读出数据
        env2 = check_envelope('%s read' % name,
                              ['read', path, '--password', 'password', '-l', '3'])
        if env2 and env2['ok']:
            print('      读取数据    -> %d 行, 首行=%s'
                  % (env2['data']['rows_returned'],
                     (env2['data']['rows'] or [['(空)']])[0]))

print()
print('=' * 72)
print('C. 损坏文件与非法输入')
print('=' * 72)
env = check_envelope('BadWorkbook info', ['info', os.path.join(TEST, 'BadWorkbook.xlsx')])
print('  BadWorkbook.xlsx        -> %s' % (env['error']['code'] if env and not env['ok'] else 'ok'))
env = check_envelope('vbaProject.bin', ['info', os.path.join(TEST, 'vbaProject.bin')])
print('  vbaProject.bin(非表格)  -> %s' % (env['error']['code'] if env and not env['ok'] else 'ok'))
env = check_envelope('empty file', ['info', os.path.join(TEST, 'images', 'excel.png')])
print('  excel.png(非表格)       -> %s' % (env['error']['code'] if env and not env['ok'] else 'ok'))
env = check_envelope('missing', ['info', os.path.join(TEST, 'no-such.xlsx')],
                     expect_code='FILE_NOT_FOUND')
print('  不存在的文件            -> %s' % (env['error']['code'] if env and not env['ok'] else 'ok'))

print()
print('=' * 72)
print('D. 共享字符串 / 溢出数值 / 计算链 的真实内容')
print('=' * 72)
for name, sheet_hint in (('SharedStrings.xlsx', None), ('CalcChain.xlsx', None),
                         ('OverflowNumericCell.xlsx', None), ('MergeCell.xlsx', None)):
    path = os.path.join(TEST, name)
    env = check_envelope('%s content' % name, ['info', path])
    if not (env and env['ok']):
        continue
    s = env['data']['sheets'][0]
    print('  %-26s sheet=%s %sx%s' % (name, s['name'], s.get('max_row'), s.get('max_column')))
    print('      header: %s' % (s.get('header') or '(无)'))
    env2 = check_envelope('%s read' % name,
                          ['read', path, '-s', s['name'], '-l', '3', '--skip-empty'])
    if env2 and env2['ok']:
        for row in env2['data']['rows'][:3]:
            print('      %s' % row)

print()
print('=' * 72)
print('E. 开关组合作用于小文件（--calc/--fill-merged/--raw/--format tsv/--header-row）')
print('=' * 72)
for name in ('Book1.xlsx', 'MergeCell.xlsx', 'SharedStrings.xlsx', 'CalcChain.xlsx'):
    path = os.path.join(TEST, name)
    got = []
    for label, extra in (('calc', ['--calc']), ('fill', ['--fill-merged']),
                         ('raw', ['--raw']), ('tsv', ['--format', 'tsv']),
                         ('hdr0', ['--header-row', '0']), ('hdr2', ['--header-row', '2'])):
        env = check_envelope('%s %s' % (name, label), ['read', path, '-l', '3'] + extra)
        got.append('%s=%s' % (label, 'ok' if env and env['ok'] else (env['error']['code'] if env else 'CRASH')))
    print('  %-24s %s' % (name, ' '.join(got)))

print()
print('=' * 72)
print('F. 数组字段不得为 null（agent 会直接遍历它们）')
print('=' * 72)
# A field that is sometimes an array and sometimes null forces every caller to
# guard before iterating. Absent is fine; null is not.
ARRAY_FIELDS = {'rows', 'matches', 'columns', 'sheets', 'sample_rows', 'top_values',
                'unaccounted_columns', 'merged_sample', 'warnings', 'column_names',
                'header', 'header_keys', 'group_by', 'fields'}


def null_arrays(node, path='data'):
    found = []
    if isinstance(node, dict):
        for key, value in node.items():
            here = '%s.%s' % (path, key)
            if value is None and key in ARRAY_FIELDS:
                found.append(here)
            found += null_arrays(value, here)
    elif isinstance(node, list):
        for i, value in enumerate(node):
            found += null_arrays(value, '%s[%d]' % (path, i))
    return found


nulls = set()
for name in SMALL_FILES:
    path = os.path.join(TEST, name)
    pw = PASSWORDS.get(name)
    suffix = ['--password', pw] if pw else []
    for label, args in (
        ('info', ['info', path] + suffix),
        ('read', ['read', path, '--header', '-l', '3'] + suffix),
        ('agg', ['agg', path, '--sum', 'A'] + suffix),
        ('profile', ['profile', path] + suffix),
        ('find', ['find', path, '-v', 'a'] + suffix),
    ):
        env = check_envelope('%s/%s null-check' % (name, label), args)
        if env and env['ok']:
            for hit in null_arrays(env.get('data')):
                nulls.add(hit)
if nulls:
    for hit in sorted(nulls):
        fail('null array field: %s' % hit)
else:
    print('  所有命令、所有文件：没有数组字段为 null')

print()
print('=' * 72)
print('G. serve 模式对每个小文件的鲁棒性（坏文件不得中断会话）')
print('=' * 72)
reqs = []
for name in SMALL_FILES[:6]:
    reqs.append(json.dumps({'command': 'info', 'args': [os.path.join(TEST, name)]}))
reqs.append(json.dumps({'command': 'quit'}))
p = subprocess.run([EXE, 'serve'], input=('\n'.join(reqs) + '\n').encode('utf-8'),
                   capture_output=True, timeout=180)
out_lines = [l for l in p.stdout.decode('utf-8').split('\n') if l.strip()]
# Every request line gets a response line, quit included — it acknowledges
# before closing so that a host waiting on its own shutdown is not left
# blocking until the pipe ends.
print('  发送 %d 行（含 quit），收到 %d 行响应' % (len(reqs), len(out_lines)))
if len(out_lines) != len(reqs):
    fail('serve: response count %d != request count %d' % (len(out_lines), len(reqs)))
if json.loads(out_lines[-1])['command'] != 'serve':
    fail('serve: the last response should be the shutdown acknowledgement')
ok_count = sum(1 for l in out_lines if json.loads(l)['ok'])
print('  其中成功 %d，失败 %d —— 会话未中断' % (ok_count, len(out_lines) - ok_count))

_common.finish(FAILURES, '小文件鲁棒性')
