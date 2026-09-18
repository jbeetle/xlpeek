# Copyright (c) 2026 henryyu@163.com. All rights reserved.
#
# This file is part of xlpeek, a read-only spreadsheet reader for
# agents. See README.md for what it does and docs/AGENTS.md for how it is
# meant to be driven.

"""The three capabilities of the third review round, one case per claim.

The third round was a request rather than a defect report: expose the formula
engine that --calc already used, so that a caller can derive a column per row
(--col), aggregate a group with any of its 900-odd functions (--agg), and share
a total out (--share). What it asked for alongside them is the part worth
pinning down, and most of the checks below are about that:

  * a formula whose value changes between two runs — NOW, TODAY, RAND,
    RANDBETWEEN — must be refused rather than answered;
  * a function the engine does not implement (QUARTER) must come back named,
    not as a column of empty values;
  * a row the engine cannot compute (#DIV/0!, a lookup that finds nothing) must
    be distinguishable from a row whose value is empty, and counted;
  * the scratch worksheet the evaluation needs must not survive the command or
    appear in any sheet list.

They read testdata/round2.xlsx, whose 公式 sheet is sized by hand: 部门 A holds
100, 110, 120, 130 and 10000, so its mean (2092) is 17.4 times its median (120)
— the illustration the review led with. Regenerate the file with
`python examples/regression/make_fixtures.py`.
"""
import _common  # noqa: F401  — chdir to the repo root and force UTF-8 output
import json
import statistics
import subprocess
import sys

EXE = _common.EXE
FILE = 'testdata/round2.xlsx'
FAILURES = []

# 公式 sheet, 部门 A: 100, 110, 120, 130, 10000. 部门 B: 200, 400, 600.
A = [100, 110, 120, 130, 10000]
B = [200, 400, 600]
TOTAL = sum(A) + sum(B)          # 11660
COUNT = len(A) + len(B)          # 8


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
    print('  %-56s got=%-28s %s'
          % (name, _show(got), 'ok' if ok else 'FAIL want=%s' % _show(want)))
    if not ok:
        FAILURES.append('%s: got %s want %s' % (name, _show(got), _show(want)))


def check_near(name, got, want, places=6):
    try:
        ok = got is not None and abs(float(got) - float(want)) < 10 ** -places
    except (TypeError, ValueError):
        ok = False
    print('  %-56s got=%-28s %s'
          % (name, _show(got), 'ok' if ok else 'FAIL want≈%s' % _show(want)))
    if not ok:
        FAILURES.append('%s: got %s want %s' % (name, _show(got), _show(want)))


def _show(value):
    text = repr(value)
    return text if len(text) <= 28 else text[:25] + '...'


def data(env):
    payload = env.get('data')
    return payload if isinstance(payload, dict) else {}


def rows(env):
    return data(env).get('rows') or []


def field(env, name):
    """The named field of every row, keyed by the group key."""
    out = {}
    for row in rows(env):
        keys = list(row.items())
        out[keys[0][1]] = row.get(name)
    return out


def rejects(name, args, code='USAGE'):
    """A call that must be refused: exit 2, with the given error code."""
    rc, env = run(*args)
    got = (rc, (env.get('error') or {}).get('code'))
    check(name, got, (2, code))
    return (env.get('error') or {}).get('message', '')


# ── P0-A 公式派生列 ─────────────────────────────────────────────
print('=== P0-A --col: a formula per row ===')
rc, env = run('agg', FILE, '-s', '公式', '--header',
              '--col', '大额=IF(金额>1000,"是","否")',
              '--group-by', '大额', '--count')
check('IF 派生列分组', field(env, 'count'), {'否': 7, '是': 1})

# The same question asked through --where, which does not go through the formula
# engine at all: the two paths must agree.
rc, env2 = run('agg', FILE, '-s', '公式', '--header', '--where', '金额>1000', '--count')
check('与 --where 的答案一致', data(env2).get('rows_matched'), 1)

# 文本日期可以直接参与时间派生 —— 第三轮明确要求文档化的行为。
rc, env = run('agg', FILE, '-s', '日期', '--header',
              '--col', '月=MONTH(文本日期)', '--group-by', '月', '--count')
check('MONTH(文本日期) 可用', field(env, 'count'), {'1': 2})
rc, env = run('agg', FILE, '-s', '日期', '--header',
              '--col', '月=MONTH(日期)', '--col', '季=ROUNDUP(MONTH(日期)/3,0)',
              '--group-by', '月,季', '--count')
check('真日期的月与季', rows(env), [{'月': '1', '季': '1', 'count': 2}])

# 非日期文本得到 #VALUE!，而不是静默的 0 —— 而且这一行为必须被计数。
rc, env = run('agg', FILE, '-s', '筛选', '--header',
              '--col', '月=MONTH(名称)', '--group-by', '月', '--count')
check('非日期文本 → #VALUE!', field(env, 'count'), {'#VALUE!': 3})
check('算不出来的行被计数', data(env).get('warning_count') > 0, True)

# 文本清洗（TRIM/SUBSTITUTE），并确认清洗后的值真的参与分组。
rc, env = run('read', FILE, '-s', '公式', '--header',
              '--col', '干净=TRIM(SUBSTITUTE(编号," ",""))',
              '--columns', '干净', '-l', '8')
check('TRIM/SUBSTITUTE 清洗编号',
      [r['干净'] for r in rows(env)],
      ['A-1', 'A-2', 'A-3', 'A-4', 'A-5', 'B-1', 'B-2', 'B-3'])

# 跨表取值：查到的给值，查不到的必须与"空"区分开。
rc, env = run('agg', FILE, '-s', '公式', '--header',
              '--col', '目标=VLOOKUP(代码,字典!$A:$B,2,FALSE)',
              '--group-by', '目标', '--count', '--limit', '0')
found = {key: value for key, value in field(env, 'count').items()}
check('VLOOKUP 命中字典表', {k: v for k, v in found.items() if k in ('11', '22', '33')},
      {'11': 3, '22': 2, '33': 2})
check('VLOOKUP 无结果原样透出', 'VLOOKUP no result found' in found, True)
check('无结果被计数', data(env).get('warning_count') > 0, True)

# 除零：计算不出来的单元格是引擎的报错，不是 0。
rc, env = run('agg', FILE, '-s', '公式', '--header',
              '--col', '比率=金额/分母', '--group-by', '比率', '--count', '--limit', '0')
check('=金额/分母 的除零结果', '#DIV/0!' in field(env, 'count'), True)

# read 端的派生列：列名进 header，也能被 --columns 投影。
rc, env = run('read', FILE, '-s', '公式', '--header',
              '--col', '月=MONTH(编号)', '--columns', '部门,月', '-l', '1')
check('read --col 的 derived_columns', data(env).get('derived_columns'), ['月'])
check('read --col 的空列名在 header 末尾', data(env).get('header')[-1], '月')

# 一条需求解锁四类场景的理由：派生列可以再被派生列引用。
rc, env = run('agg', FILE, '-s', '公式', '--header',
              '--col', '倍数=金额/100', '--col', '再加倍=倍数*2',
              '--group-by', '再加倍', '--count', '--limit', '2')
check('派生列引用派生列', rows(env)[0].get('count'), 1)

# ── 边界：写错的公式必须在扫全表之前报出来 ──────────────────────
print('=== 边界：公式本身不对 ===')
for name in ('NOW', 'TODAY', 'RAND', 'RANDBETWEEN'):
    message = rejects('%s() 被拒绝' % name,
                      ['agg', FILE, '-s', '公式', '--header',
                       '--col', '带今天=IF(%s()>0,1,2)' % name, '--count'])
    check('  错误信息点名 %s' % name, name in message, True)

message = rejects('清单外的函数被拒绝',
                  ['agg', FILE, '-s', '公式', '--header',
                   '--col', '季=QUARTER(金额)', '--group-by', '季', '--count'])
check('  错误信息点名 QUARTER', 'QUARTER' in message, True)
rc, env = run('agg', FILE, '-s', '日期', '--header',
              '--col', '季=ROUNDUP(MONTH(日期)/3,0)', '--group-by', '季', '--count')
check('  等价的 ROUNDUP 写法可用', (rc, (env.get('error') or {}).get('code')), (0, None))

message = rejects('写错的列名被拒绝',
                  ['agg', FILE, '-s', '公式', '--header',
                   '--col', '金额2=金鹅', '--group-by', '金额2', '--count'])
check('  错误信息点名写错的列', '金鹅' in message, True)

message = rejects('不存在的 sheet 被拒绝',
                  ['agg', FILE, '-s', '公式', '--header',
                   '--col', '目标值=目标表!A1', '--group-by', '目标值', '--count'])
check('  错误信息列出可用 sheet', '字典' in message, True)

# 重复 JSON 键：解析器只保留最后一个，等于数据静默丢失。
rejects('分组列与输出字段同名被拒绝',
        ['agg', FILE, '-s', '公式', '--header', '--group-by', 'count', '--count'])
rejects('同一个分组列写两次被拒绝',
        ['agg', FILE, '-s', '公式', '--header', '--group-by', '部门,部门', '--count'])

# 自引用构造不出环：一个派生列只能引用在它之前定义的那个。
rejects('派生列自引用被拒绝',
        ['agg', FILE, '-s', '公式', '--header', '--col', '自己=自己+1', '--group-by', '部门', '--count'])
rejects('两个派生列同名被拒绝',
        ['agg', FILE, '-s', '公式', '--header', '--col', 'a=金额', '--col', 'a=金额*2', '--count'])

rejects('--col 没有表头行',
        ['agg', FILE, '-s', '公式', '--col', '倍数=金额*2', '--sum', '倍数'])
rejects('派生列与现有列重名',
        ['agg', FILE, '-s', '公式', '--header', '--col', '金额=金额*2', '--sum', '金额'])
rejects('派生列叫列字母',
        ['agg', FILE, '-s', '公式', '--header', '--col', 'B=金额*2', '--sum', 'B'])

# ── P1-C 公式聚合 ───────────────────────────────────────────────
print('=== P1-C --agg: a formula per group ===')
rc, env = run('agg', FILE, '-s', '公式', '--header', '--group-by', '部门',
              '--agg', '中位=MEDIAN(金额)', '--agg', '标准差=STDEV(金额)',
              '--agg', 'p50=PERCENTILE(金额,0.5)', '--count')
check('中位数', field(env, '中位'), {'A': 120, 'B': 400})
check('分位数', field(env, 'p50'), {'A': 120, 'B': 400})
check_near('标准差', field(env, '标准差')['A'], statistics.stdev(A), 6)
check_near('标准差 B', field(env, '标准差')['B'], statistics.stdev(B), 6)

# 中位数与平均值的差别，正是这一轮要求它的理由。
rc, env = run('agg', FILE, '-s', '公式', '--header', '--group-by', '部门',
              '--avg', '金额', '--agg', '中位=MEDIAN(金额)')
check('平均值被离群值抬高', field(env, 'avg_金额'), {'A': 2092, 'B': 400})
check('中位数不受影响', field(env, '中位'), {'A': 120, 'B': 400})

rc, env = run('agg', FILE, '-s', '公式', '--header', '--group-by', '部门',
              '--agg', '超标=COUNTIFS(金额,">1000",区域,"华东")',
              '--agg', '华东合计=SUMIFS(金额,区域,"华东")')
check('COUNTIFS 两个条件', field(env, '超标'), {'A': 1, 'B': 0})
check('SUMIFS 条件求和', field(env, '华东合计'), {'A': 10330, 'B': 0})

# 公式里也能用算出来的聚合值：离散系数 = 标准差/平均值。
rc, env = run('agg', FILE, '-s', '公式', '--header', '--group-by', '部门',
              '--agg', '离散=STDEV(金额)/AVERAGE(金额)')
check_near('公式内混合聚合', field(env, '离散')['A'],
           statistics.stdev(A) / statistics.mean(A), 6)

# 一组里全是文本 → 引擎报错 → null，并计一次警告。
rc, env = run('agg', FILE, '-s', '公式', '--header', '--group-by', '部门',
              '--agg', '文本中位=MEDIAN(编号)')
check('文本列的中位数是 null', field(env, '文本中位'), {'A': None, 'B': None})
check('并给出一次说明', data(env).get('warning_count') > 0, True)

# 程序生成的表里公式列没有缓存值：引擎对"空的 SUM"给 0，那是错数字，
# 所以必须说明；加了 --calc 才能算出真实合计。
SHAPES = 'testdata/shapes.xlsx'
rc, env = run('agg', SHAPES, '-s', '公式', '--header', '--agg', '合计=SUM(金额)', '--count')
check('空列的 SUM 被说明', [w for w in (data(env).get('warnings') or [])
                            if 'held nothing' in w] and True, True)
check('空列的 SUM 仍是 0', [r.get('合计') for r in rows(env)], [0])
rc, env = run('agg', SHAPES, '-s', '公式', '--header', '--agg', '合计=SUM(金额)', '--count', '--calc')
check('加 --calc 后算出真值', [r.get('合计') for r in rows(env)], [44])
# --col 不需要 --calc：它引用的是单元格，引擎自己会求值。
rc, env = run('agg', SHAPES, '-s', '公式', '--header', '--col', '含税=金额*1.13', '--sum', '含税', '--count')
check('--col 在无缓存公式列上可用', [r.get('sum_含税') for r in rows(env)], [49.72])

rejects('--agg 要求 name=formula',
        ['agg', FILE, '-s', '公式', '--header', '--agg', 'MEDIAN(金额)'])

# 整列引用是对的，但代价是"被求值的行数 × 范围行数"——2700 行的表上实测 44 秒。
# 工具不禁止它，但必须出声：不告警的 44 秒看起来就是卡住了。
rc, env = run('agg', FILE, '-s', '公式', '--header', '--group-by', '部门',
              '--col', '目标=VLOOKUP(代码,字典!$A:$B,2,FALSE)',
              '--agg', '合计=SUM(字典!$A:$A)', '--count')
check('整列引用被点名为告警',
      len([w for w in (data(env).get('warnings') or []) if 'whole columns' in w]), 2)
rc, env = run('agg', FILE, '-s', '公式', '--header', '--group-by', '部门',
              '--col', '目标=VLOOKUP(代码,字典!$A$1:$B$100,2,FALSE)', '--count')
check('限定范围的写法不报整列告警',
      [w for w in (data(env).get('warnings') or []) if 'whole columns' in w], [])

# 两个接口能合起来用：--agg 读的是某一组的行，而这一行可以由 --col 先算出来。
rc, env = run('agg', FILE, '-s', '公式', '--header',
              '--col', '千元=金额/1000', '--group-by', '部门',
              '--agg', '合计=SUM(千元)', '--sum', '金额')
check('--agg 读派生列', field(env, '合计'), {'A': 10.46, 'B': 1.2})

# 与既有简写并存：--sum 与 --agg 可以同时出现在一个命令里。
rc, env = run('agg', FILE, '-s', '公式', '--header', '--group-by', '部门',
              '--sum', '金额', '--agg', '中位=MEDIAN(金额)', '--count')
check('简写与公式聚合并存', field(env, 'sum_金额'), {'A': sum(A), 'B': sum(B)})

# ── P0-B 占比 ──────────────────────────────────────────────────
print('=== P0-B --share: a share of the same scan ===')
rc, env = run('agg', FILE, '-s', '公式', '--header', '--group-by', '部门',
              '--sum', '金额', '--count', '--share')
check_near('share_sum_金额 A', field(env, 'share_sum_金额')['A'], sum(A) / TOTAL, 9)
check_near('share_sum_金额 B', field(env, 'share_sum_金额')['B'], sum(B) / TOTAL, 9)
check_near('share_count A', field(env, 'share_count')['A'], len(A) / COUNT, 9)
check_near('share_count B', field(env, 'share_count')['B'], len(B) / COUNT, 9)
check('share 字段进 fields', data(env).get('fields'),
      ['sum_金额', 'count', 'share_sum_金额', 'share_count'])
shares = field(env, 'share_sum_金额')
check_near('各组合计为 1', shares['A'] + shares['B'], 1.0, 9)
counts = field(env, 'share_count')
check_near('计数占比合计为 1', counts['A'] + counts['B'], 1.0, 9)

# 总计必须与分组同源：有 --where 时，分母是过滤后的总计。
rc, env = run('agg', FILE, '-s', '公式', '--header', '--group-by', '部门',
              '--where', '金额>=120', '--sum', '金额', '--count', '--share')
check_near('过滤后的分母', field(env, 'share_sum_金额')['A'], 10250 / 11450, 9)
check_near('过滤后的计数占比', field(env, 'share_count')['A'], 0.5, 9)

# --derive 里的 _total_ 与 --share 是同一个数。
rc, one = run('agg', FILE, '-s', '公式', '--header', '--group-by', '部门',
              '--sum', '金额', '--count', '--share')
rc, two = run('agg', FILE, '-s', '公式', '--header', '--group-by', '部门',
              '--sum', '金额', '--count',
              '--derive', '占比=sum_金额/_total_sum_金额',
              '--derive', '计数占比=count/_total_count')
check('_total_ 与 --share 同值', field(two, '占比'), field(one, 'share_sum_金额'))
check('_total_count 可用', field(two, '计数占比'), field(one, 'share_count'))

# 不能相加的字段没有"占比"可言：平均值不是整体平均的一部分。
rejects('--share 需要可加的字段',
        ['agg', FILE, '-s', '公式', '--header', '--group-by', '部门', '--avg', '金额', '--share'])
message = rejects('_total_ 只对可加字段成立',
                  ['agg', FILE, '-s', '公式', '--header', '--group-by', '部门',
                   '--avg', '金额', '--derive', 'x=_total_avg_金额'])
check('  错误信息解释原因', 'total' in message, True)

# 总计为 0 时给 null，并说明，而不是除以 0。
rc, env = run('agg', FILE, '-s', '公式', '--header', '--group-by', '部门',
              '--sum', '零=金额-金额', '--share')
check('总计为 0 → null', field(env, 'share_零'), {'A': None, 'B': None})
check('并给出说明', data(env).get('warning_count') > 0, True)

# ── 边界：临时工作表不能留下来 ──────────────────────────────────
print('=== 边界：求值用的临时工作表 ===')
rc, env = run('info', FILE)
sheets = [s['name'] for s in data(env).get('sheets') or []]
check('info 的 sheet 列表干净', [s for s in sheets if s.startswith('__xlpeek')], [])

# 同一会话里连问两次，工作表列表不能长出来——scratch 表是在缓存的工作簿上
# 增删的，一次没删干净就会累积。
proc = subprocess.Popen([EXE, 'serve'], stdin=subprocess.PIPE, stdout=subprocess.PIPE)


def send(payload):
    proc.stdin.write((json.dumps(payload) + '\n').encode('utf-8'))
    proc.stdin.flush()
    line = proc.stdout.readline()
    return json.loads(line.decode('utf-8'))


send({'command': 'agg', 'args': [FILE, '-s', '公式', '--header',
                                 '--col', '月=MONTH(编号)', '--group-by', '月',
                                 '--agg', '中位=MEDIAN(金额)', '--share', '--sum', '金额']})
env = send({'command': 'info', 'args': [FILE]})
sheets = [s['name'] for s in data(env).get('sheets') or []]
check('serve 会话里 sheet 列表也没变', [s for s in sheets if s.startswith('__xlpeek')], [])
send({'command': 'quit'})
proc.wait(timeout=60)

# 同一命令两次必须给出同一答案 —— 这是拒绝易变函数的理由。
rc, first = run('agg', FILE, '-s', '公式', '--header', '--group-by', '部门',
                '--agg', '中位=MEDIAN(金额)', '--col', '月=MONTH(编号)',
                '--group-by', '部门,月', '--sum', '金额', '--count', '--share')
rc, second = run('agg', FILE, '-s', '公式', '--header', '--group-by', '部门',
                 '--agg', '中位=MEDIAN(金额)', '--col', '月=MONTH(编号)',
                 '--group-by', '部门,月', '--sum', '金额', '--count', '--share')
check('两次运行结果一致', first == second, True)

_common.finish(FAILURES, '第三轮能力')
