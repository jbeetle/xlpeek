# xlpeek for Node.js

A wrapper around [xlpeek](https://github.com/jbeetle/xlpeek) for Node agents, plus the tests that pin down
the three stdio traps it exists to avoid. Every number below was measured, and
`node test.js` re-measures them.

```bash
node test.js          # 33 checks against the real binary
```

```js
const { run, runOrThrow, Session, checkSize } = require('./xlpeek.js');

const { envelope } = await run(['info', 'book.xlsx']);
if (!envelope.ok) {
  console.log(envelope.error.code, envelope.error.message);
}
checkSize(envelope);   // says so when a response is big enough to matter
```

## The three traps

### 1. `execSync` caps output at `maxBuffer`, counted in **bytes**

Node's default is 1 MB. It counts **bytes, not characters** — so a Chinese
sheet, where each character is 3 bytes, overflows three times sooner than the
character count suggests.

| Response | |
| --- | --- |
| `read -l 5000` on a 13-column Chinese sheet | 919,482 bytes / 546,926 characters |
| Node's default `maxBuffer` | 1,048,576 bytes |
| **How full that is** | **88%** |

A slightly wider sheet, or values a little longer, and the call throws.
The failing measurement is unambiguous:

```
maxBuffer = 919,483 (one more than the byte count)  → 通过
maxBuffer = 546,926 (exactly the character count)   → ENOBUFS
```

Both passing would have proved nothing; only a byte-counted limit admits the
first and refuses the second.

**Use `spawn`.** It streams and has no such cap — the wrapper does, which is why
it reads 919 KB back intact.

### 2. `execSync` throws on non-zero exit, and this tool exits non-zero on failure

xlpeek exits `1` on a runtime error and `2` on a usage error, while still
writing a complete JSON error envelope to stdout. `execSync` turns that into an
exception, so the obvious fix is to catch it and parse `e.stdout`.

That works — until the failure was `ENOBUFS`, where `e.stdout` is a **truncated
partial**: 78,904 of 546,926 characters, which `JSON.parse` cannot read. The two
failures arrive through the same `catch`.

`spawn` avoids the confusion entirely: a failure is a normal resolved response
with `ok: false`, and the caller branches on `ok` rather than on an exception.

### 3. `--format tsv` prints a table after the envelope

```
{"ok":true,...,"data_bytes":1681}     ← line 1 is JSON
代码	名称                            ← line 2 is the header
SH601668	中国建筑                    ← the rest is data
```

`JSON.parse` on the whole output throws. **Only the first line is the envelope**,
so `data_bytes` in TSV mode covers the body as well as the metadata.

## What is *not* a problem

Checked and clean, so you do not have to worry about them:

- **No BOM.** `JSON.parse` throws on one; the tool's output starts with `{`.
- **No duplicate JSON keys.** These were fixed at the source — `JSON.parse`
  silently keeps the last value, which would have discarded data.
- **Array fields are never `null`.** `rows`, `matches`, `columns` and `sheets`
  are real arrays even when empty, so `forEach` is always safe.
- **UTF-8 throughout**, read as Buffers and decoded explicitly.
- **Numbers are numbers.** Aggregates arrive as JSON numbers; cells read by
  `read` arrive as strings, because a formatted cell can be `25.67%` or
  `1,234,567.89` and those are not numbers. Convert before doing arithmetic.
- **`serve` is line-delimited**, one response per request, `quit` included.

## Session

`Session` keeps one process alive so workbooks are parsed once. Measured at
about 2 ms per query against 265 ms for a fresh process, and it removes the
chance of naming the sheet or the header row differently on one call out of
twenty.

```js
const s = await Session.start();
try {
  await s.callOrThrow(['info', 'book.xlsx']);
  await s.callOrThrow(['agg', 'book.xlsx', '-s', '明细', '--header', '--sum', '收入金额']);
  console.log((await s.ping()).data);   // uptime, request count, open workbooks
} finally {
  await s.close();
}
```

A failing request does not end the session. If the process does die, every
pending call rejects rather than hanging, and `close()` is idempotent.
