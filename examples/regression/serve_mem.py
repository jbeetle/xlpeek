# Copyright (c) 2026 henryyu@163.com. All rights reserved.
#
# This file is part of xlpeek, a read-only spreadsheet reader for agents.
# See README.md for what it does and docs/AGENTS.md for how it is meant to be
# driven.

"""How does a serve session's memory behave as it answers more requests?

excelize caches a parsed worksheet on the File handle, and serve holds that
handle open, so the question is whether a long-lived session grows without
bound — which decides whether it can be started once and left running.
"""
import _common  # noqa: F401  — chdir to the repo root and force UTF-8 output
import json
import subprocess
import sys
import time

EXE = _common.EXE  # 尊重 XLPEEK 覆盖，见 _common.py
BIG = r'testdata\华鼎科技_分部收入_FY23-FY25.xlsx'
SHEETS = ['FY23_收入明细', 'FY24_收入明细', 'FY25_收入明细', '口径与汇率说明']


def rss_mb(pid):
    out = subprocess.run(
        ['powershell', '-NoProfile', '-Command',
         '(Get-Process -Id %d).WorkingSet64' % pid],
        capture_output=True).stdout.decode().strip()
    try:
        return int(out) / 1048576.0
    except ValueError:
        return float('nan')


def main():
    proc = subprocess.Popen([EXE, 'serve'], stdin=subprocess.PIPE,
                            stdout=subprocess.PIPE, stderr=subprocess.PIPE)
    base = None
    samples = []

    def send(payload):
        proc.stdin.write((json.dumps(payload) + '\n').encode('utf-8'))
        proc.stdin.flush()
        line = proc.stdout.readline()
        if not line:
            print('  serve 提前退出')
            sys.exit(1)
        return json.loads(line.decode('utf-8'))

    for i in range(300):
        sheet = SHEETS[i % len(SHEETS)]
        env = send({'command': 'read',
                    'args': [BIG, '-s', sheet, '--header', '-l', '200']})
        if not env['ok']:
            print('  请求失败:', env['error'])
            sys.exit(1)
        if i in (0, 9, 49, 99, 199, 299):
            mb = rss_mb(proc.pid)
            if base is None:
                base = mb
            samples.append((i + 1, mb))

    print('  %-8s %-12s %s' % ('请求数', '内存(MB)', '相对首次'))
    for count, mb in samples:
        print('  %-8d %-12.1f %+.1f MB' % (count, mb, mb - base))

    # 换到另一个文件，验证缓存是替换而不是累积
    send({'command': 'read', 'args': [r'testdata\Book1.xlsx', '-l', '5']})
    after_swap = rss_mb(proc.pid)
    for i in range(50):
        send({'command': 'read', 'args': [r'testdata\Book1.xlsx', '-l', '5']})
    after_50 = rss_mb(proc.pid)
    print()
    print('  切换到小文件后 : %.1f MB' % after_swap)
    print('  再问 50 次后   : %.1f MB  (%+.1f MB)' % (after_50, after_50 - after_swap))

    send({'command': 'quit'})
    proc.wait(timeout=30)
    print()
    print('  退出码:', proc.returncode)

    # 文档声称"内存有界，不再增长"，这里把它变成断言而不是一句观察。
    # 取第 100 次与第 300 次之间的增量：早期增长来自工作表被首次解析并缓存，
    # 那之后应当持平。
    failures = []
    settled = dict(samples)
    if 100 in settled and 300 in settled:
        drift = settled[300] - settled[100]
        print('  第 100 次 → 第 300 次 增长: %+.1f MB' % drift)
        if drift > 5:
            failures.append('内存未收敛：第 100→300 次增长 %.1f MB（阈值 5 MB）' % drift)
    else:
        failures.append('未采到第 100/300 次样本，无法判断内存是否收敛')
    if after_50 - after_swap > 2:
        failures.append('切换文件后内存反弹 %.1f MB，缓存可能在累积' % (after_50 - after_swap))
    if proc.returncode != 0:
        failures.append('serve 退出码 %s' % proc.returncode)

    _common.finish(failures, 'serve 会话内存有界')


main()
