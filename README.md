# xlpeek

[![ci](https://github.com/jbeetle/xlpeek/actions/workflows/ci.yml/badge.svg)](https://github.com/jbeetle/xlpeek/actions/workflows/ci.yml)
[![Go Reference](https://pkg.go.dev/badge/github.com/jbeetle/xlpeek.svg)](https://pkg.go.dev/github.com/jbeetle/xlpeek)
[![License: MIT](https://img.shields.io/badge/license-MIT-blue.svg)](LICENSE)

**Give an agent a spreadsheet without giving it your whole context window.**

`xlpeek` is a read-only Excel reader built for AI agents. Reading is streamed, so
memory stays flat no matter how large the sheet; results are paginated, so a
caller takes one page at a time; and every response is a single JSON envelope
designed to be parsed, not eyeballed.

It is also built to say **"this answer may be meaningless"** when that is true —
which matters more than it sounds, because the failures that hurt are the ones
that look like successes.

```console
$ xlpeek info book.xlsx
$ xlpeek agg  book.xlsx -s 明细 --header --group-by 销售区域 --sum 收入金额 --count
$ xlpeek read book.xlsx -s 明细 --header -l 500 --where "金额>10000"
$ xlpeek find book.xlsx -s 明细 -v 客户名 --header
```

Read-only by construction: there is no way to write, modify, format or generate
a workbook. If that is not what you want, you want
[excelize](https://github.com/xuri/excelize), which this is built on.

## Install

```bash
go install github.com/jbeetle/xlpeek@latest
```

Or build from source (Go 1.25+):

```bash
go build -o bin/xlpeek.exe .     # Windows
go build -o bin/xlpeek .         # macOS / Linux
```

The result is a single static binary — no Go toolchain, no C library, no runtime
needed at the other end. See [Distribution](#distribution).

## Why not just use a spreadsheet library?

Because the hard part is not parsing the file. It is that an agent cannot see
the data, so it cannot tell when a plausible-looking number is wrong.

A column holding both `元` and `千元`. A currency column mixing `CNY`, `USD` and
`HKD`. A total that is arithmetically perfect and off by 30%. A remark column
that is 3% filled. `xlpeek` reports these:

```json
{
  "sum_收入金额": 2839456194.25,
  "unaccounted_columns": [
    {"column": "单位", "distinct": 2, "sample": ["元", "千元"], "suspected": true},
    {"column": "币种", "distinct": 3, "sample": ["CNY", "USD", "HKD"], "suspected": true}
  ],
  "warning_count": 1,
  "complete": true,
  "data_bytes": 1191
}
```

The tool cannot know that 千元 means a thousand. It can see that the column
varies while the caller summed straight through it — and that is enough to stop
a confident wrong answer.

## Other things it gets right on purpose

- **Streaming, not loading.** Memory stays flat regardless of sheet size.
- **Capped and honest about it.** `complete`, `truncated`, `has_more`,
  `columns_capped`, `distinct_capped`, `values_omitted` — a caller never has to
  infer whether it is holding all the data.
- **Size-aware.** Every response reports `data_bytes`, and warns above 512 KB,
  because a 5,000-row page over 128 columns of CJK text is 11 MB.
- **No duplicate JSON keys, ever.** A parser keeps only the last one, which
  would discard data silently. Collisions are rejected instead.
- **No `null` where an array belongs.** Iterating `rows` is always safe.
- **Errors that correct the caller.** A bad sheet name comes back with the list
  of real ones; a bad column with the format expected; a cold call with the
  field name to rename.
- **`serve` mode.** One process, many questions, workbooks parsed once —
  measured at ~2 ms per query against ~265 ms for a fresh process.

## Documentation

This README documents what the tool does and the contract it keeps. The rest
covers how it is meant to be *driven*, and how it is checked:

| File | For | Size |
| --- | --- | --- |
| [docs/SYSTEM_PROMPT.md](docs/SYSTEM_PROMPT.md) | paste into an agent's system prompt | ~730 tokens |
| [docs/AGENTS.md](docs/AGENTS.md) | the agent's reference, loaded on demand | ~6,400 tokens |
| [examples/nodejs](examples/nodejs) | a Node wrapper and the stdio traps it avoids | |
| [examples/regression](examples/regression) | ten cross-validation suites, `run_all.py` | |

The first two exist because an agent that does not know the traps will walk into
them confidently — the units-and-currencies case above is not hypothetical, it
is a real file in `testdata/`.

### Distribution

The binary is **standalone**: Go compiles it with `CGO_ENABLED=0`, so it is
statically linked and carries the Go runtime and every dependency inside it.
Nothing needs to be installed alongside it — no Go toolchain, no C library, no
runtime. On Linux, `ldd` reports "not a dynamic executable", so the same file
runs on glibc, musl/Alpine and `FROM scratch` images alike.

Cross-compile for wherever the caller runs:

```bash
CGO_ENABLED=0 GOOS=linux  GOARCH=amd64 go build -o bin/xlpeek-linux-amd64  .
CGO_ENABLED=0 GOOS=linux  GOARCH=arm64 go build -o bin/xlpeek-linux-arm64  .
CGO_ENABLED=0 GOOS=darwin GOARCH=arm64 go build -o bin/xlpeek-darwin-arm64 .
CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build -o bin/xlpeek.exe .
```

A binary is tied to one OS and architecture: a `.exe` will not run on Linux and
vice versa. The cost of self-containment is size — roughly 14 MB per platform,
because the runtime is baked in rather than shared. That also means a Go
runtime security fix requires rebuilding rather than updating a shared library.

Output is UTF-8. On a Windows console whose code page is not UTF-8, a consumer
that decodes stdout with the locale encoding will see mojibake for non-ASCII
data; decode explicitly as UTF-8.

### Calling it from Node.js

[`examples/nodejs`](examples/nodejs) wraps the stdio interface for Node agents
and documents the three traps it avoids: `exec`/`execSync` cap output at
`maxBuffer`, which is counted in **bytes** and defaults to 1 MB — a Chinese
sheet reaches 919 KB (88% of it) with a single `read -l 5000`; `execSync` throws
on the non-zero exit this tool uses for failures, and its `e.stdout` is a
truncated partial when the failure was `ENOBUFS`; and `--format tsv` puts a
table after the envelope, so only the first line parses as JSON.
`node examples/nodejs/test.js` re-measures all of it — 33 checks.

## Testing

```bash
go test .            # 35 unit tests
python examples/regression/run_all.py   # the suites below, ~75s
```

`go test` covers the pure logic. The suites under
[`examples/regression`](examples/regression) cover what unit tests cannot:
**cross-validation against an independent XML parser**, behaviour on real and
malformed files, and whether the commands written in these docs actually run.
Ten suites, exit code is the verdict:

| | |
| --- | --- |
| `regress_agg` / `regress_crosscheck` | aggregates and cells checked against a from-scratch XML reader |
| `regress_filter` | nine filter expressions against a full-scan recount |
| `regress_paging` | four page sizes must yield the same rows, with no gap or overlap |
| `regress_new` | merged cells, `--header-row`, profile statistics, TSV layout |
| `regress_precision` | every difference from the raw XML explained by 15-digit normalisation |
| `verify_docs` | every command in docs/AGENTS.md and this file, actually executed |
| `regress_small` | ten files × seven commands: no panic, valid envelope, no null arrays |
| `serve_mem` | memory converges over 300 requests |
| `nodejs/test.js` | the Node wrapper and the maxBuffer behaviour it exists to avoid |

Set `XLPEEK` to point them at a specific build; leave it unset and they pick
`bin/xlpeek.exe` or `bin/xlpeek-linux-<arch>` for the platform.

## Output contract

Every invocation writes **exactly one JSON envelope to stdout** and nothing
else. This holds on the failure path too, so a caller has a single parse path.

```json
{"ok":true,"command":"read","version":"1.0.1","data":{ ... }}
{"ok":false,"command":"read","version":"1.0.1","error":{"code":"SHEET_NOT_FOUND","message":"..."}}
```

Exit codes: `0` success, `1` runtime failure, `2` usage failure.

Errors go to stdout rather than stderr for exactly that reason. Anything on
stderr is human-oriented noise, such as the flag package's own diagnostics.
`version` is emitted the same way, so a capability check parses like any other
call, and it carries the copyright alongside the number:

```json
{"ok":true,"command":"version","version":"1.0.1",
 "data":{"name":"xlpeek","version":"1.0.1",
         "copyright":"Copyright (c) 2026 henryyu@163.com. All rights reserved."}}
```

The same line closes `help`, and the same notice heads every source file.
`help` is the one exception to the envelope contract — it prints plain usage
text, because usage is documentation rather than data.

Output is UTF-8. On a Windows console whose code page is not UTF-8, a consumer
that decodes stdout with the locale encoding will see mojibake for non-ASCII
data; decode as UTF-8 explicitly.

`--pretty` indents the JSON. The default is compact single-line output, which is
cheaper to feed to a model.

Error codes: `USAGE`, `FILE_NOT_FOUND`, `SHEET_NOT_FOUND`, `PASSWORD_REQUIRED`,
`INVALID_PASSWORD`, `UNSUPPORTED_FORMAT`, `COLUMN_NOT_FOUND`, `READ_ERROR`.

`PASSWORD_REQUIRED` and `INVALID_PASSWORD` are deliberately separate: the first
means supply a password, the second means the one you supplied is wrong. And a
file that is neither an OOXML package nor an OLE container is reported as
`UNSUPPORTED_FORMAT` rather than being handed back the zip reader's "not a valid
zip file", which sends a caller looking for corruption.

Array fields are never `null` — a page with no rows reports `[]`, and a workbook
with no sheets reports `[]`. Absent optional fields are omitted, but nothing a
caller would iterate over is ever null.

### Response size

Every response reports its own `data_bytes`, and adds a `data_warning` above
512 KB:

```json
{"ok":true,"data_bytes":919262,
 "data_warning":"this response is 919262 bytes; narrow it with --columns, a smaller --limit, or --format tsv"}
```

The shape is capped — at most 5,000 rows and 128 columns — but the product is
not: 5,000 rows across 128 columns of CJK text is on the order of 11 MB, which
is enough to blow past any context window in one call. Measured worst cases on a
13-column sheet:

| Query | Output |
| --- | --- |
| `read -l 5000 --header` | 919 KB |
| the same with `--format tsv` | 398 KB |
| the same with `--columns a,b,c` | 288 KB |
| `agg --group-by` a real dimension | 1.7 KB |

The warning says nothing about correctness — only that the response is large.
`agg` raises a second one when an unbounded grouping produces more than 1,000
groups, because grouping by a near-unique column returns the table rather than a
summary. Neither changes the result; both are there to be noticed.

## Commands

### `info` — what is in this workbook?

Cheap by default: it reads the dimension element and a short preview, not the
whole sheet.

```bash
xlpeek info book.xlsx
xlpeek info book.xlsx --deep          # walk every row for exact counts
xlpeek info book.xlsx --header-row 3  # the table starts below a title block
xlpeek info book.xlsx -s 明细 --sample 5
```

Per sheet it reports `index`, `id`, `name`, `visible`, `dimension`, `max_row`,
`max_column`, the `header` row, `header_keys` (the JSON keys `--header` would
produce), `column_names`, and `sample_rows`. `--deep` adds `last_row`,
`populated_rows`, `merged_ranges` and `merged_sample`, and sets `scanned: true`;
without it those are absent rather than wrong, because a shallow scan stops
early and any number it reported would just be where it stopped.

A sheet that cannot be streamed records `scan_error` and the remaining sheets are
still reported. Chartsheets and dialog sheets are listed, but carry no rows and
no dimension, because they contain no cell data. `visible` is omitted rather
than reported as `false` when it could not be determined, so a missing value
means unknown rather than hidden.

`dimension`, `max_row` and `max_column` are copied from the file's own dimension
element, which is only a hint: producers sometimes write it wrong. Treat them as
a cheap upper bound for planning, not as a measurement.

### `read` — one page of rows

```bash
xlpeek read book.xlsx -s Sheet1 -l 100                          # rows as arrays
xlpeek read book.xlsx -s Sheet1 --header -l 100                 # rows as objects
xlpeek read book.xlsx -s Sheet1 --header -o 100 -l 100          # next page
xlpeek read book.xlsx -s Sheet1 --header --skip-empty -l 100    # ignore blank rows
xlpeek read book.xlsx -s 明细 --header-row 3 --header -l 100    # table starts at row 3
xlpeek read book.xlsx -s 明细 --header --columns "订单号,金额"
xlpeek read book.xlsx -s 明细 --header --where "金额>10000" -l 50
xlpeek read book.xlsx -s 明细 --header --fill-merged            # merge-aware
xlpeek read book.xlsx -s 明细 --header --format tsv -l 500      # cheaper than JSON
```

The response carries everything needed to fetch the next page:

```json
{
  "sheet": "明细", "header_mode": true, "header_row": 1,
  "header": ["订单号", "金额"], "columns": ["A", "B"],
  "offset": 0, "limit": 100, "rows_returned": 100,
  "first_row": 2, "last_row": 101,
  "has_more": true, "next_offset": 100, "rows_scanned": 100,
  "rows": [{"订单号": "A-1", "金额": "12000"}]
}
```

- **`offset` counts returned rows, not spreadsheet rows.** With `--header`, offset
  `0` is the first row below the header. With `--where`, it counts *matching*
  rows, so it behaves like `LIMIT/OFFSET` in SQL: the scan always starts at the
  top and walks forward.
- `first_row` / `last_row` are absolute 1-based spreadsheet row numbers, so a
  caller can map a page back to cell references.
- **Sparse sheets produce blank rows.** Reading is positional: a position exists
  for every row number up to the last one the sheet defines, so a sheet whose
  data starts at row 19 returns 18 blank rows first, and a single stray
  formatted row at the bottom pads every page out to a million rows. Pass
  `--skip-empty` to drop rows whose cells are all empty. Row numbers stay
  absolute either way, so `first_row` still tells you where you are.
- Columns are projected in the order requested and padded to a uniform width so
  a row never silently changes shape. `columns` tells you the width per page.
- In `--header` mode each row is an object whose key order follows the worksheet
  column order. Blank header cells fall back to the column letter, and duplicate
  headers get a `_2`, `_3` suffix rather than collapsing two columns into one.

### `agg` — ask an analytical question, get a small answer

This is the command that keeps a caller from having to page 2,700 rows across
the wire in order to report one number.

```bash
# One row per region: total, count, and a ratio computed from the totals.
xlpeek agg book.xlsx -s 明细 --header --group-by 销售区域 \
    --sum 收入金额 --sum 成本金额 --count \
    --derive "毛利率=(sum_收入金额-sum_成本金额)/sum_收入金额"

# Grand total, no grouping.
xlpeek agg book.xlsx -s 明细 --header --sum 收入金额 --count

# Top 10 customers by revenue.
xlpeek agg book.xlsx -s 明细 --header --group-by 客户名称 \
    --sum 收入金额 --sort-by sum_收入金额 --limit 10

# Aggregate an expression, not just a column.
xlpeek agg book.xlsx -s 明细 --header --sum "净利=收入金额-成本金额"

# Filter first.
xlpeek agg book.xlsx -s 明细 --header --where "销售区域=华北" --sum 收入金额
```

Aggregates: `--sum`, `--avg`, `--min`, `--max`, `--count`, `--count-distinct`.
Each accepts an arithmetic expression over columns (`+ - * /`, parentheses), and
a leading `name=` renames the output field. Every one is repeatable.

`--derive "name=expression"` computes a metric from the aggregate fields already
computed, which is what ratios need: `sum(收入-成本)/sum(收入)` is the overall
margin, whereas averaging per-row margins is not. Referencing a field that was
never computed is a usage error rather than a null.

Field naming is predictable: `sum_收入金额`, `avg_收入金额`, `distinct_客户编号`,
`count`, plus whatever `--derive` names.

- **Missing values are skipped, not zeroed.** A blank cell does not drag an
  average down; inside an expression it counts as zero, the way a spreadsheet
  treats an empty cell in arithmetic. A group with nothing to aggregate reports
  `null` rather than a misleading `0`.
- **Sums are compensated.** Plain float addition drifts about one ulp per term,
  which turns a total of 2,839,456,194.25 into 2,839,456,194.24999 over a few
  thousand rows. Neumaier compensation keeps it correctly rounded, then results
  are reported to 15 significant digits, matching Excel's own precision.
- `--sort-by` sorts aggregates descending and group keys ascending; `--limit` and
  `--offset` page the resulting groups with the same `has_more`/`next_offset`
  contract as `read`.
- Cells that cannot be read as numbers are skipped and counted in `warnings`.

### `profile` — what does each column actually contain?

Runs a full scan and reports, per column: inferred type, fill rate, distinct
count, numeric range and mean, and — when the column has few enough distinct
values — the values themselves with counts. This is what lets a caller write a
correct filter the first time instead of discovering the vocabulary by trial and
error.

```bash
xlpeek profile book.xlsx -s 明细 --header
xlpeek profile book.xlsx -s 明细 --header --columns 销售区域,币种
xlpeek profile book.xlsx -s 明细 --header --max-values 50
```

```
  G  销售区域  type=text    填充=100.0% 去重=11
        取值: 西南(395), 华南(388), 华北(378), 东北(375), ...
  J  收入金额  type=number  填充=100.0% 去重=2698  min=107.90 max=25967570.47 均值=1051650.44
  M  备注     type=text    填充=  3.3% 去重=1
        取值: 关联方(86)
```

A column is labelled with a type only when **every** non-empty value agrees;
anything else is `mixed`, because a caller filtering on a numeric column needs to
know that some rows will not parse. Distinct tracking is capped (20,000 per
column, 500,000 in total) and a column that exceeds it reports
`distinct_capped: true`, so its distinct count is a lower bound rather than a
memory problem.

**Profile before aggregating.** It is the cheapest way to find the columns whose
values are not comparable — mixed units, mixed currencies, negative amounts in a
cost column, a 3%-filled remark column — and every one of those will silently
corrupt a total.

### `find` — where is this value?

```bash
xlpeek find book.xlsx -s 明细 -v "张三"
xlpeek find book.xlsx -s 明细 -v "^A-\d+$" --regex
xlpeek find book.xlsx -s 明细 -v "已取消" --header --column 状态 -l 20
```

Returns each hit with its cell reference, row and column, the matched value, and
the surrounding row for context. `row_values` is padded to the same width the
`read` command would report, so column positions agree between the two.
`truncated: true` means the search stopped at `--limit` (or `--max-scan`) and
more matches may exist — it does not assert that they do.

### `serve` — hold the workbook open across requests

Opening a workbook costs a parse. A caller asking twenty questions about one
file should pay that once.

```bash
$ xlpeek serve
{"command":"info","args":["book.xlsx","-s","明细"]}
{"ok":true,...}
{"command":"agg","args":["book.xlsx","-s","明细","--header","--sum","收入金额"]}
{"ok":true,...}
{"command":"quit"}
```

One request line in, one response line out — failures included, so a reader can
pair them up without inspecting the content, and a bad request does not end the
session. `args` are exactly the arguments the one-shot CLI takes, so there is no
second syntax to learn.

Measured over 20 queries against the same sheet:

| | per query |
| --- | --- |
| separate processes | 265 ms |
| `serve` | **2 ms** |

The workbook handle is cached underneath the ordinary commands, so a request
that names a different file, or the same file with different `--raw`/`--password`
options, transparently opens its own.

Managing the session:

| Flag | Meaning |
| --- | --- |
| `--cache N` | workbooks held open at once, default 4. Alternating between four files costs 0 ms per call at the default and 6 ms at `--cache 1` |
| `--idle-timeout 5m` | exit after this long with no requests; 0 (the default) never exits on idle |

```bash
{"command":"ping"}
{"ok":true,"command":"ping","data":{"status":"ok","uptime_seconds":12,
 "requests":3,"cached_workbooks":["a.xlsx","b.xlsx"]}}
```

`ping` is how a supervisor tells a busy process from a wedged one. The session
ends on `quit` (which is acknowledged) or on stdin closing, and both exit 0.
An idle timeout leaves **silently**: an unsolicited line would be waiting when
the host next read, and it would take it for the answer to the request it had
just sent, so end of stream is the unambiguous signal.

Memory stays bounded — measured at +8 MB over 300 requests across four sheets,
flat after the first hundred — and eviction closes the least recently used
workbook rather than letting parsed worksheets accumulate.

## Filters (`--where`)

Repeatable, combined with AND, and applied before aggregating.

| Operator | Meaning |
| --- | --- |
| `=` `!=` | equality, case-insensitive |
| `>` `>=` `<` `<=` | comparison |
| `~` | case-insensitive substring |

The column may be a header name (in `--header` mode), a column letter (`B`), or a
1-based index (`2`). A header name wins over a letter, so a column literally
named `A` is still addressable by name.

Comparison is numeric when the filter value parses as a number; a cell that does
not parse as a number then simply fails to match rather than falling back to text
comparison. Thousands separators are tolerated; currency symbols and `%` are not
stripped, because guessing at them would silently change what a comparison means.
For exact numeric filtering on formatted columns, use `--raw`.

## Header rows

`--header` is shorthand for `--header-row 1`. Both are accepted by `read`, `agg`,
`profile`, `find` and `info`; an explicit row wins. Use it when the table sits
below a title block, which is the common shape of a real report:

```bash
xlpeek read report.xlsx -s Sheet1 --header-row 3
```

`--header-row 0` means there is no header, and columns are addressed by letter or
index.

## Merged cells

A spreadsheet stores a merged value only in the top-left cell of the region and
leaves the rest empty, so a reader that ignores merges sees blanks wherever a
report used a merged heading or a label spanning several rows — and reasons
confidently from them.

```bash
xlpeek info book.xlsx --deep              # reports merged_ranges and a sample
xlpeek read book.xlsx -s 明细 --fill-merged
```

`--fill-merged` copies each region's value into every cell it spans. Only blanks
are filled, so a region can never overwrite real data. `--deep` surfaces merged
regions because it is easy not to know they are there.

## Output formats

`read --format tsv` writes the JSON envelope, then a header line, then
tab-separated rows:

```
{"ok":true,...,"format":"tsv","rows":null}
凭证号	过账日期	客户名称	收入金额
FI-24000001	2024-08-21	北方智造装备集团有限公司	1866279.23
```

The envelope still comes first and still carries the paging metadata, so control
flow is unchanged; only the rows move out of JSON. Measured on a 100-row page
with 13 columns:

| Format | Size | |
| --- | --- | --- |
| `--header` objects | 34,419 B | |
| arrays (no `--header`) | 17,712 B | 51% of objects |
| `--format tsv --header` | 15,260 B | 44% of objects |

Array mode captures most of the saving, because it stops repeating the column
names on every row; TSV trims the JSON punctuation on top of that. Fields
containing a tab, quote or newline are quoted the way CSV does — a remark column
with embedded line breaks would otherwise corrupt the table.

## Performance and limits

- Reading is streamed through excelize's row iterator, so memory stays flat
  regardless of sheet size. Parts larger than 16 MB are spilled to temporary
  files by excelize on open; use `--tmpdir` to control where.
- **Page size matters far more than the offset does.** Measured on a 2,900-row
  sheet (Windows, warm cache, Go 1.27):

  | | time |
  | --- | --- |
  | process start + open workbook | ~0.09 s |
  | scanning all 2,900 rows | ~0.11 s (≈38 µs/row) |
  | paging 2,900 rows at `--limit 100` (29 calls) | 4.57 s |
  | paging 2,900 rows at `--limit 2900` (1 call) | 0.30 s |

  The forward scan is cheap; the per-invocation cost is not. 29 small calls cost
  15× one large call for identical data. **Prefer `--limit` of 500–5000**, and
  narrow with `--where` rather than walking to a deep offset.
- `--calc` evaluates formulas that have no cached value, `--fill-merged` reads
  merged regions, and `info --deep` reports them. All three make excelize load
  the whole worksheet into memory, giving up the streaming profile — so they are
  opt-in.
- `has_more` is exact when the end of the sheet is reached during the lookahead.
  If the lookahead exceeds 10,000 rows without a verdict (possible with a
  restrictive `--where`), it reports `has_more: true` together with
  `has_more_approximate: true`. The bias is deliberate: a false `false` would
  make a caller stop early and silently miss data, while a false `true` only
  costs one extra request.
- `--max-columns` (default 128) caps the width of a page. Truncation is reported
  via `columns_capped` on `info` and a warning on `read`.
- Reading formatted values means dates arrive as formatted strings and numbers as
  formatted text. Pass `--raw` for the stored values.
- **`--raw` also gives exact precision.** The default path rounds to 15
  significant digits, which is Excel's own limit and what a spreadsheet displays.
  A cell stored as `677675.5699999999` comes back as `677675.57` by default and
  as `677675.5699999999` with `--raw`. Use `--raw` when feeding values into
  further arithmetic or an audit that must reconcile against the raw file.

## Recipe for an agent

1. `xlpeek info book.xlsx` — find the sheet, see the header and a sample.
2. `xlpeek profile book.xlsx -s <sheet> --header` — learn each column's type,
   fill rate and vocabulary, and spot columns whose values are not comparable
   before any total depends on them.
3. `xlpeek agg ...` — ask the analytical question directly rather than paging
   rows to compute it.
4. `xlpeek read book.xlsx -s <sheet> --header --skip-empty -l 1000` — page only
   the rows you actually need, following `next_offset` while `has_more` is true.
5. `xlpeek find` to locate a specific value before reading around it.

## Not implemented

The tool is read-only by design. Writing, formula *setting*, charts, images,
pivot tables, conditional formatting and style inspection are out of scope.
