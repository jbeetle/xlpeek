// Copyright (c) 2026 henryyu@163.com. All rights reserved.
//
// This file is part of xlpeek, a read-only spreadsheet reader for agents.
// See README.md for what it does and docs/AGENTS.md for how it is meant to be
// driven.

// A Node wrapper around xlpeek that avoids the three traps in its stdio
// interface. All three were measured, not assumed — see test.js.
//
//   1. exec/execSync cap output at maxBuffer, counted in BYTES, default 1 MB.
//      A Chinese sheet reaches 919 KB with a single `read -l 5000`, so the
//      default sits 88% full and the next wider sheet overflows it. spawn has
//      no such cap.
//   2. execSync throws on any non-zero exit, and this tool exits 1 or 2 while
//      still writing a perfectly good JSON error envelope. Catching and then
//      reading e.stdout looks like a fix but hands you a TRUNCATED partial
//      when the failure was ENOBUFS.
//   3. --format tsv prints the envelope and then a table, so JSON.parse on the
//      whole output fails. Only the first line is JSON.
//
// Output is read as Buffers and decoded as UTF-8 explicitly, because the tool
// writes UTF-8 and a platform's default encoding may not be.

'use strict';

const { spawn } = require('child_process');

const DEFAULT_BIN = process.env.XLPEEK || 'xlpeek';

/**
 * Run one xlpeek command and resolve with its parsed envelope.
 *
 * Resolves for both ok:true and ok:false, because a failure envelope is a
 * normal, parseable answer — branch on `ok`, not on a thrown exception.
 * Rejects only when no envelope could be parsed at all.
 *
 * @param {string[]} args arguments, exactly as the CLI takes them
 * @param {{bin?:string, cwd?:string, timeout?:number}} [opts]
 * @returns {Promise<{envelope:object, stdout:string, stderr:string, code:number}>}
 */
function run(args, opts = {}) {
  const bin = opts.bin || DEFAULT_BIN;
  return new Promise((resolve, reject) => {
    const child = spawn(bin, args, { cwd: opts.cwd, windowsHide: true });
    const out = [];
    const err = [];
    let timer = null;

    const settle = (fn, value) => {
      if (timer) clearTimeout(timer);
      timer = null;
      fn(value);
    };

    if (opts.timeout) {
      timer = setTimeout(() => {
        child.kill();
        reject(new Error(`xlpeek timed out after ${opts.timeout}ms: ${args.join(' ')}`));
      }, opts.timeout);
    }

    child.stdout.on('data', (c) => out.push(c));
    child.stderr.on('data', (c) => err.push(c));
    child.on('error', (e) => settle(reject, e));
    child.on('close', (code) => {
      const stdout = Buffer.concat(out).toString('utf8');
      // Only the first line is the envelope: --format tsv appends a table.
      const firstLine = stdout.split('\n', 1)[0].trim();
      if (!firstLine) {
        return settle(reject, new Error(
          `xlpeek produced no envelope (exit ${code}): `
          + Buffer.concat(err).toString('utf8').slice(0, 300)));
      }
      let envelope;
      try {
        envelope = JSON.parse(firstLine);
      } catch (e) {
        return settle(reject, new Error(`xlpeek envelope is not JSON: ${firstLine.slice(0, 200)}`));
      }
      settle(resolve, {
        envelope,
        stdout,
        stderr: Buffer.concat(err).toString('utf8'),
        code,
      });
    });
  });
}

/**
 * Run one command and throw if the envelope reports a failure.
 * The thrown error carries `code` (the tool's own error code) and `envelope`.
 */
async function runOrThrow(args, opts) {
  const { envelope } = await run(args, opts);
  if (!envelope.ok) {
    const e = new Error(envelope.error.message);
    e.code = envelope.error.code;
    e.envelope = envelope;
    throw e;
  }
  return envelope.data;
}

/**
 * A serve session: one process, many questions, workbooks parsed once.
 *
 * Measured at roughly 2 ms per query against 265 ms for a fresh process, and
 * it removes the chance of specifying the sheet or the header row differently
 * on one call out of twenty.
 *
 *   const s = await Session.start();
 *   await s.call(['info', 'book.xlsx']);
 *   await s.call(['agg', 'book.xlsx', '-s', '明细', '--header', '--sum', '收入金额']);
 *   await s.close();
 */
class Session {
  static start(opts = {}) {
    return new Promise((resolve, reject) => {
      const bin = opts.bin || DEFAULT_BIN;
      const args = ['serve'];
      if (opts.idleTimeout) args.push('--idle-timeout', opts.idleTimeout);
      if (opts.cache) args.push('--cache', String(opts.cache));

      const child = spawn(bin, args, { windowsHide: true });
      const session = new Session(child);
      child.on('error', reject);
      child.on('close', (code) => session._onClose(code));
      // The reader is always waiting, so responses cannot pile up unread.
      session._readLoop();
      resolve(session);
    });
  }

  constructor(child) {
    this.child = child;
    this.buffer = '';
    this.pending = [];
    this.closed = false;
  }

  _readLoop() {
    this.child.stdout.on('data', (chunk) => {
      this.buffer += chunk.toString('utf8');
      let i;
      while ((i = this.buffer.indexOf('\n')) >= 0) {
        const line = this.buffer.slice(0, i).trim();
        this.buffer = this.buffer.slice(i + 1);
        if (!line) continue;
        const waiter = this.pending.shift();
        if (!waiter) continue; // an unsolicited line; the protocol sends none
        clearTimeout(waiter.timer);
        try {
          waiter.resolve(JSON.parse(line));
        } catch (e) {
          waiter.reject(new Error(`bad response line: ${line.slice(0, 200)}`));
        }
      }
    });
  }

  _onClose(code) {
    this.closed = true;
    const err = new Error(`xlpeek serve exited (code ${code})`);
    while (this.pending.length) {
      const waiter = this.pending.shift();
      clearTimeout(waiter.timer);
      waiter.reject(err);
    }
  }

  /** Send one request; resolves with its envelope. */
  call(args, { timeout = 0 } = {}) {
    if (this.closed) return Promise.reject(new Error('session is closed'));
    return new Promise((resolve, reject) => {
      const waiter = { resolve, reject, timer: null };
      if (timeout) {
        waiter.timer = setTimeout(() => {
          const i = this.pending.indexOf(waiter);
          if (i >= 0) this.pending.splice(i, 1);
          reject(new Error(`serve request timed out: ${args.join(' ')}`));
        }, timeout);
      }
      this.pending.push(waiter);
      this.child.stdin.write(JSON.stringify({ command: args[0], args: args.slice(1) }) + '\n');
    });
  }

  /** Send one request and throw if it reports a failure. */
  async callOrThrow(args, opts) {
    const envelope = await this.call(args, opts);
    if (!envelope.ok) {
      const e = new Error(envelope.error.message);
      e.code = envelope.error.code;
      e.envelope = envelope;
      throw e;
    }
    return envelope.data;
  }

  /** Ask whether the process is alive and what it is holding open. */
  ping() {
    return this.call(['ping']);
  }

  /** Close the session. Resolves once the process has exited. */
  close() {
    if (this.closed) return Promise.resolve();
    return new Promise((resolve) => {
      this.child.once('close', () => resolve());
      this.child.stdin.write(JSON.stringify({ command: 'quit' }) + '\n');
      this.child.stdin.end();
    });
  }
}

/**
 * Warn when a response is big enough to crowd out a context window. The tool
 * says so itself in data_warning; this just makes it hard to ignore.
 */
function checkSize(envelope, onWarn = console.warn) {
  if (envelope && envelope.data_warning) onWarn('xlpeek: ' + envelope.data_warning);
  return envelope;
}

module.exports = { run, runOrThrow, Session, checkSize };
