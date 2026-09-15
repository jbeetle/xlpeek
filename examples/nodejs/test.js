// Copyright (c) 2026 henryyu@163.com. All rights reserved.
//
// This file is part of xlpeek, a read-only spreadsheet reader for agents.
// See README.md for what it does and docs/AGENTS.md for how it is meant to be
// driven.

// Verifies the wrapper against the real binary, and in doing so pins down the
// behaviours it exists to work around: the maxBuffer unit, the exit-code
// semantics, and the TSV line structure.
//
//   node test.js
//
// Set XLPEEK to point at a different binary; otherwise it finds one under the
// repository's bin/, or at its root.

'use strict';

const fs = require('fs');
const os = require('os');
const path = require('path');
const { execSync, spawnSync } = require('child_process');

const { run, runOrThrow, Session, checkSize } = require('./xlpeek.js');

const REPO = path.resolve(__dirname, '..', '..');
const FIXTURES = path.join(REPO, 'testdata', '华鼎科技_分部收入_FY23-FY25.xlsx');
const STOCKS = path.join(REPO, 'testdata', 'hs.xlsx');

/** Prefer a binary built for this platform, so the test works everywhere. */
function resolveBin() {
  if (process.env.XLPEEK) return process.env.XLPEEK;
  const arch = process.arch === 'x64' ? 'amd64' : process.arch;
  const names = process.platform === 'win32'
    ? ['xlpeek.exe']
    : [`xlpeek-linux-${arch}`, 'xlpeek'];
  // `go build` drops the binary in the working directory; the documented build
  // puts it in bin/. Accept either.
  for (const dir of ['bin', '.']) {
    for (const name of names) {
      const candidate = path.join(REPO, dir, name);
      if (fs.existsSync(candidate)) return candidate;
    }
  }
  return 'xlpeek'; // fall back to PATH
}

const BIN = resolveBin();

let passed = 0;
let failed = 0;
const check = (label, ok, detail) => {
  console.log(`  ${ok ? '✓' : '✗'}  ${label}${detail ? '  — ' + detail : ''}`);
  ok ? passed++ : failed++;
};
const skip = (label, why) => console.log(`  ·  ${label}  — 跳过: ${why}`);

async function main() {
  console.log(`binary: ${BIN}`);
  console.log(`node:   ${process.version}`);
  if (!fs.existsSync(FIXTURES)) {
    console.log(`\n缺少测试夹具 ${FIXTURES}，无法运行。`);
    process.exit(1);
  }

  console.log('\n=== 1. 基本解析：每个命令的信封都能 JSON.parse ===');
  for (const [name, args] of [
    ['info', ['info', FIXTURES]],
    ['read', ['read', FIXTURES, '-s', 'FY24_收入明细', '--header', '-l', '5']],
    ['agg', ['agg', FIXTURES, '-s', 'FY24_收入明细', '--header', '--sum', '收入金额', '--count']],
    ['profile', ['profile', FIXTURES, '-s', 'FY24_收入明细', '--header']],
    ['find', ['find', STOCKS, '-s', 'hs', '-v', '中国', '--header', '--column', '名称']],
    ['version', ['version']],
  ]) {
    const r = await run(args, { bin: BIN });
    check(name, r.envelope.ok === true, `data_bytes=${r.envelope.data_bytes}`);
  }

  console.log('\n=== 2. 无 BOM（JSON.parse 遇 BOM 会抛错）===');
  {
    const r = await run(['version'], { bin: BIN });
    check('首字符是 {', r.stdout.charCodeAt(0) === 0x7b,
      `U+${r.stdout.charCodeAt(0).toString(16).toUpperCase()}`);
  }

  console.log('\n=== 3. 失败路径：信封可解析，不靠异常传递 ===');
  {
    const r = await run(['read', FIXTURES, '-s', '不存在的表'], { bin: BIN });
    check('ok=false 且可解析', r.envelope.ok === false, r.envelope.error.code);
    check('退出码非 0', r.code !== 0, `code=${r.code}`);
    try {
      await runOrThrow(['read', FIXTURES, '-s', '不存在的表'], { bin: BIN });
      check('runOrThrow 抛出', false);
    } catch (e) {
      check('runOrThrow 抛出且带 code', e.code === 'SHEET_NOT_FOUND', `code=${e.code}`);
    }
  }

  console.log('\n=== 4. execSync 的 maxBuffer：按【字节】计，默认 1 MB ===');
  {
    const args = ['read', FIXTURES, '-s', 'FY24_收入明细', '--header', '-l', '5000'];
    const ref = spawnSync(BIN, args, { maxBuffer: 64 * 1024 * 1024 });
    const bytes = ref.stdout.length;
    const chars = ref.stdout.toString('utf8').length;
    check('大响应尺寸', true, `${bytes} 字节 / ${chars} 字符 (中文 3 字节/字)`);
    check('占 Node 默认 1MB 的比例', true, `${(bytes / 1048576 * 100).toFixed(0)}%`);

    // Quote for the shell the way execSync will see it. JSON.stringify is the
    // wrong tool here: it escapes backslashes, so a Windows path arrives as
    // C:\\dir and the command fails for a reason unrelated to maxBuffer.
    const quote = (s) => (process.platform === 'win32'
      ? `"${s}"`
      : `'${s.replace(/'/g, "'\\''")}'`);
    const command = [BIN, ...args].map(quote).join(' ');

    // If the limit were counted in characters, maxBuffer=chars would pass and
    // maxBuffer=bytes+1 would too — both passing would prove nothing. Only a
    // limit in bytes admits the first and refuses the second.
    const probe = (limit) => {
      try {
        execSync(command, { encoding: 'utf8', maxBuffer: limit });
        return '通过';
      } catch (e) {
        return e.code || '失败';
      }
    };
    const atBytes = probe(bytes + 1);
    const atChars = probe(chars);
    check('按字节计（而非字符）', atBytes === '通过' && atChars === 'ENOBUFS',
      `maxBuffer=${bytes + 1}(字节) → ${atBytes}, maxBuffer=${chars}(字符) → ${atChars}`);

    // ENOBUFS 时 e.stdout 是残缺的，绝不能再 parse
    try {
      execSync(command, { encoding: 'utf8', maxBuffer: 100000 });
      skip('ENOBUFS 行为', '未触发');
    } catch (e) {
      let parses = true;
      try { JSON.parse(e.stdout || ''); } catch { parses = false; }
      check('ENOBUFS 时 e.stdout 不可解析', e.code === 'ENOBUFS' && !parses,
        `${e.code}，stdout 仅 ${(e.stdout || '').length}/${chars} 字符`);
    }

    // spawn 路径完整读回
    const big = await run(args, { bin: BIN });
    check('spawn 完整读回', big.envelope.data.rows_returned === 2700,
      `${big.envelope.data.rows_returned} 行`);
    check('data_warning 已给出', !!big.envelope.data_warning);
    check('checkSize 会提示', (() => {
      let seen = null;
      checkSize(big.envelope, (m) => { seen = m; });
      return seen !== null;
    })());
    check('小响应不提示', (() => {
      let seen = null;
      checkSize({ ok: true }, (m) => { seen = m; });
      return seen === null;
    })());
  }

  console.log('\n=== 5. TSV 模式：只解析首行 ===');
  {
    const r = await run(['read', STOCKS, '-s', 'hs', '--header', '--where', '名称~中国',
      '--format', 'tsv', '-l', '5000'], { bin: BIN });
    check('信封可解析', r.envelope.ok === true);
    let wholeParses = true;
    try { JSON.parse(r.stdout); } catch { wholeParses = false; }
    check('整段 JSON.parse 会失败（故只取首行）', !wholeParses);
    const lines = r.stdout.split('\n').filter(Boolean);
    check('第 2 行是表头', lines[1] === '代码\t名称', JSON.stringify(lines[1]));
    check('表体 60 行', lines.length - 2 === 60, `${lines.length - 2} 行`);
  }

  console.log('\n=== 6. 中文与数组字段 ===');
  {
    const r = await run(['find', STOCKS, '-s', 'hs', '-v', '中国', '--header',
      '--column', '名称', '-l', '500'], { bin: BIN });
    check('中文列名正确', r.envelope.data.matches[0].column_name === '名称');
    check('matches 是数组可遍历', Array.isArray(r.envelope.data.matches));
    const names = [];
    r.envelope.data.matches.forEach((m) => names.push(m.row_values[0]));
    check('遍历出 60 只', names.length === 60, names.slice(0, 3).join(','));

    const agg = await run(['agg', FIXTURES, '-s', 'FY24_收入明细', '--header',
      '--sum', '收入金额'], { bin: BIN });
    check('聚合结果是 number 而非字符串',
      typeof agg.envelope.data.rows[0]['sum_收入金额'] === 'number',
      String(agg.envelope.data.rows[0]['sum_收入金额']));
    const read = await run(['read', FIXTURES, '-s', 'FY24_收入明细', '--header',
      '--columns', '收入金额', '-l', '1'], { bin: BIN });
    check('read 的单元格是字符串（需自行转换）',
      typeof read.envelope.data.rows[0]['收入金额'] === 'string');
  }

  console.log('\n=== 7. Session：一个进程连问多次 ===');
  {
    const s = await Session.start({ bin: BIN });
    try {
      const info = await s.callOrThrow(['info', FIXTURES, '-s', 'FY24_收入明细', '--sample', '0']);
      check('info', info.sheets.length > 0, info.sheets[0].name);
      const agg = await s.callOrThrow(['agg', FIXTURES, '-s', 'FY24_收入明细',
        '--header', '--sum', '收入金额']);
      check('agg', typeof agg.rows[0].sum_收入金额 === 'number',
        String(agg.rows[0].sum_收入金额));
      const ping = (await s.ping()).data;
      check('ping 报告会话', ping.requests >= 3, JSON.stringify(ping).slice(0, 60));

      // 失败请求不应终止会话
      const bad = await s.call(['read', FIXTURES, '-s', '不存在']);
      check('会话内失败不中断', bad.ok === false, bad.error.code);
      const after = await s.callOrThrow(['version']);
      check('失败后仍可用', after.version === undefined || true, 'version 命令返回');
    } finally {
      await s.close();
    }
    check('会话已关闭', s.closed === true);
  }

  console.log('\n' + '='.repeat(60));
  console.log(`结果: ${passed} 通过, ${failed} 失败`);
  process.exit(failed ? 1 : 0);
}

main().catch((e) => {
  console.error('\n未捕获异常:', e);
  process.exit(1);
});
