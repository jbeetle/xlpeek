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
  field name to rename; a mistyped aggregate name with the one you meant.
- **Exit codes that mean something.** `2` when editing the command would fix it,
  `1` when it would not — so a caller can tell "I asked wrong" from "the file is
  odd" without parsing the message.
- **`serve` mode.** One process, many questions, workbooks parsed once —
  measured at ~2 ms per query against ~265 ms for a fresh process.

## Documentation

This README documents what the tool does and the contract it keeps. The rest
covers how it is meant to be *driven*, and how it is checked:

| File | For | Size |
| --- | --- | --- |
| [docs/SYSTEM_PROMPT.md](docs/SYSTEM_PROMPT.md) | paste into an agent's system prompt | ~1,000 tokens |
| [docs/AGENTS.md](docs/AGENTS.md) | the agent's reference, loaded on demand | ~9,500 tokens |
| [examples/nodejs](examples/nodejs) | a Node wrapper and the stdio traps it avoids | |
| [examples/regression](examples/regression) | thirteen cross-validation suites, `run_all.py` | |

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
CGO_ENABLED=0 GOOS=linux   GOARCH=amd64 go build -trimpath -buildvcs=false -o bin/xlpeek-linux-amd64  .
CGO_ENABLED=0 GOOS=linux   GOARCH=arm64 go build -trimpath -buildvcs=false -o bin/xlpeek-linux-arm64  .
CGO_ENABLED=0 GOOS=darwin  GOARCH=arm64 go build -trimpath -buildvcs=false -o bin/xlpeek-darwin-arm64 .
CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build -trimpath -buildvcs=false -o bin/xlpeek.exe .
```

`-trimpath` keeps the build machine's own paths out of the binary and
`-buildvcs=false` keeps its git state out of it, so the build is reproducible:
same source and same Go version, same bytes, whether the tree is clean or not.
The binaries attached to a release are built with exactly these commands, and a
rebuild from the release tag reproduces them.

### The release bundle

A binary on its own cannot be checked: the regression suites need the fixtures
they measure against, and a checksum needs something to be a checksum *of*. A
release is therefore assembled rather than collected by hand:

```bash
python scripts/package_release.py --test
```

That builds the four targets with the flags above, copies in `testdata/`, the
suites, the docs and the licence, **runs the whole suite against the assembled
copies**, and writes a `SHA256SUMS` over everything shipped. Unpack it anywhere
and `sha256sum -c SHA256SUMS` verifies it; run
`python examples/regression/run_all.py` inside it and the thirteen suites run off
the bundled binary and the bundled fixtures, with nothing to install.

### Provenance

The project's home and Go module path is
[`github.com/jbeetle/xlpeek`](https://github.com/jbeetle/xlpeek); the copyright
holder named in the binaries and in `LICENSE` is `henryyu@163.com`, who publishes
under that account. They are the same origin, and the whole thing is MIT
licensed — one author, one repository, one licence.

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
go test .            # 58 unit tests
python examples/regression/run_all.py   # the suites below, ~75s
```

`go test` covers the pure logic. The suites under
[`examples/regression`](examples/regression) cover what unit tests cannot:
**cross-validation against an independent XML parser**, behaviour on real and
malformed files, and whether the commands written in these docs actually run.
Thirteen suites, exit code is the verdict:

| | |
| --- | --- |
| `regress_agg` / `regress_crosscheck` | aggregates and cells checked against a from-scratch XML reader |
| `regress_filter` | nine filter expressions against a full-scan recount |
| `regress_paging` | four page sizes must yield the same rows, with no gap or overlap |
| `regress_new` | merged cells, `--header-row`, profile statistics, TSV layout |
| `regress_precision` | every difference from the raw XML explained by 15-digit normalisation |
| `regress_shapes` | the shapes a real report has — ¥/`%` formats, uncached formulas, merged labels, bracketed column names, percentage text |
| `regress_round2` | one case per finding of the second external review, most of them asserting the *error* |
| `regress_round3` | the third round's three capabilities, each against a hand-sized sheet, and the boundary: a volatile function refused, an unimplemented one named, a failed row distinguishable from an empty one, and no scratch worksheet left behind |
| `verify_docs` | every command in docs/AGENTS.md and this file, actually executed |
| `regress_small` | ten files × seven commands: no panic, valid envelope, no null arrays |
| `serve_mem` | memory converges over 300 requests |
| `nodejs/test.js` | the Node wrapper and the maxBuffer behaviour it exists to avoid |

Set `XLPEEK` to point them at a specific build; leave it unset and they pick
`bin/xlpeek.exe` or `bin/xlpeek-linux-<arch>` for the platform. The workbooks
they read are committed under `testdata/` — `bin/` is not, so build first and the
suites run on a fresh clone.

The suites check the delivery before they check the tool: a missing fixture or an
unrunnable binary is reported once, by name, instead of failing every assertion
with `FILE_NOT_FOUND`. One fixture is generated rather than hand-made —
`python examples/regression/make_fixtures.py` rebuilds `testdata/round2.xlsx`
from the description of its sheets (it needs openpyxl; the suites themselves do
not) — so a fixture whose shape is questioned can be re-derived.

## Output contract

Every invocation writes **exactly one JSON envelope to stdout** and nothing
else. This holds on the failure path too, so a caller has a single parse path.

```json
{"ok":true,"command":"read","version":"1.1.0","data":{ ... }}
{"ok":false,"command":"read","version":"1.1.0","error":{"code":"SHEET_NOT_FOUND","message":"..."}}
```

Exit codes: `0` success, `1` runtime failure, `2` usage failure. The distinction
is whether editing the command could fix it: a column that does not exist, a
filter that does not parse and a sheet that is not there are all `2`, because
retrying them unchanged cannot help. A missing file or a file that is not a
workbook is `1`, because the command was fine and the environment was not.

Errors go to stdout rather than stderr for exactly that reason. Anything on
stderr is human-oriented noise, such as the flag package's own diagnostics.
`version` is emitted the same way, so a capability check parses like any other
call, and it carries the copyright alongside the number:

```json
{"ok":true,"command":"version","version":"1.1.0",
 "data":{"name":"xlpeek","version":"1.1.0",
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

Flags may come before or after the file. A value-taking flag takes either
`--limit 10` or `--limit=10`. A boolean flag is set by being present, and accepts
all three spellings — `--ignore-case`, `--ignore-case=true`, and
`--ignore-case true` — because the last one is what almost everyone writes first,
and having it land in the operand list produced an error about a workbook path
that was never the problem.

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
`populated_rows`, `last_populated_row`, `merged_ranges` and `merged_sample`, and
sets `scanned: true`; without it those are absent rather than wrong, because a
shallow scan stops early and any number it reported would just be where it
stopped.

`last_populated_row` is the row number of the last row holding anything, and it
is the number to plan paging with: `max_row` is the file's own claim and counts
formatted empty rows, `last_row` is where the scan stopped, and `populated_rows`
is a count rather than a position. It also names the sheet by index, so
`xlpeek read book.xlsx -s 0` reaches the first one without a name.

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
xlpeek read book.xlsx -s 明细 --header --columns "A:D" -l 100   # a range, not a list
xlpeek read book.xlsx -s 明细 --header --dates iso -l 100       # stable dates
xlpeek read book.xlsx -s 明细 --header --col "净利=收入金额-成本金额" --columns "客户名称,净利"
xlpeek read book.xlsx -s 0 -l 1                                 # sheet by index
xlpeek read book.xlsx -s 明细 --header --format csv -l 100
xlpeek read book.xlsx -s 明细 --header --format markdown -l 100
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
  data starts at row 19 returns 18 blank rows first. Pass `--skip-empty` to drop
  rows whose cells are all empty. Row numbers stay absolute either way, so
  `first_row` still tells you where you are.
- **`complete` answers about content, not position.** A sheet's last row is its
  last *formatted* row, so a border or a fill dragged past the data leaves the
  file claiming rows that hold nothing. Those are not data and no longer count
  as more to fetch: the page on which the sheet's content ends reports
  `complete: true` rather than sending the caller through blank pages. A gap in
  the middle of a sheet is unaffected — the lookahead still scans across it.
- Columns are projected in the order requested and padded to a uniform width so
  a row never silently changes shape. `columns` tells you the width per page.
- In `--header` mode each row is an object whose key order follows the worksheet
  column order. Blank header cells fall back to the column letter, and duplicate
  headers get a `_2`, `_3` suffix rather than collapsing two columns into one.

`--dates` decides how a *date* cell is written, because a date is a number
wearing a number format and the same value otherwise reads as
`2026-01-01 0:00:00` in one workbook and `01-01-26` in the next:

```bash
xlpeek read book.xlsx -s 明细 --header --dates iso -l 100   # 2026-01-01
xlpeek read book.xlsx -s 明细 --header --dates serial -l 100
```

`iso` gives `2026-01-01`, or `2026-01-01T09:30:00` when a time of day is
stored; `serial` gives the stored number. The default, `display`, is what the
sheet itself shows. This one is not free: telling a date from a number means
reading the cell's number format, which parses the worksheet — so, like `--calc`
and `--fill-merged`, it is opt-in. A cell that merely *contains* a date as text
is left alone.

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

# A trend by month: derive the period from the date column, then group by it.
xlpeek agg book.xlsx -s 明细 --header --col "月=MONTH(过账日期)" --group-by 月 --sum 收入金额

# A median the outlier cannot move, and a share of the whole.
xlpeek agg book.xlsx -s 明细 --header --group-by 分部 --sum 收入金额 --count --share \
    --agg "中位金额=MEDIAN(收入金额)" --agg "p90=PERCENTILE(收入金额,0.9)"

# A workbook written by a script: its formulas have no cached value.
xlpeek agg book.xlsx -s 明细 --header --sum 收入金额 --calc
```

Aggregates: `--sum`, `--avg`, `--min`, `--max`, `--count`, `--count-distinct`,
`--agg` (any Excel formula, over the group's rows) — plus `--share` for the
percentage of the total.
Each accepts an arithmetic expression over columns (`+ - * /`, parentheses), and
a leading `name=` renames the output field. Every one is repeatable. A column
whose name contains an operator, a bracket or a space is written in brackets —
`--sum "[金额(万元)]"` — because the parenthesis in the name would otherwise be
read as arithmetic; getting it wrong says so and repeats the form.

`--derive "name=expression"` computes a metric from the aggregate fields already
computed, which is what ratios need: `sum(收入-成本)/sum(收入)` is the overall
margin, whereas averaging per-row margins is not. Referencing a field that was
never computed is a usage error rather than a null, and the message names the
field you probably meant.

The name a bracketed column produces is the bracketed name, so a derived metric
refers to it exactly as `agg` reports it:

```bash
xlpeek agg book.xlsx -s 明细 --header --sum "[金额(万元)]" --sum "[成本(万元)]" \
    --derive "毛利率=(sum_[金额(万元)]-sum_[成本(万元)])/sum_[金额(万元)]"
```

Field naming is predictable in two halves: leave the name out and the aggregate
kind is prefixed (`sum_收入金额`, `avg_收入金额`, `distinct_客户编号`, `count`);
name it yourself with `name=expression` and the field is called *exactly* that,
with no prefix, because that is what naming it is for. So
`--sum "收入=[金额(万元)]"` produces `收入`, and a later `--derive` that refers
to `sum_收入` is a usage error — one the message will tell you how to fix.

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
  contract as `read`. **`--limit` defaults to 1000**, and `--limit 0` asks for
  every group: grouping by a near-unique column is an easy mistake to make and
  an expensive one to receive, so the default truncates and says so rather than
  sending the table back. The warning names the flag and the cursor.
- Cells that cannot be read as numbers are skipped and counted in `warnings`.
- A value is read the way the sheet presents it, so a `¥1,234.50` or `12.35%`
  column aggregates without `--raw`, and the sum agrees with the one `--raw`
  produces. Three warnings cover what that tolerance could otherwise hide: a
  referenced column that produced **no** numbers at all — its aggregates are
  `null`, and the message says whether the cells are empty (which is what a
  formula with no cached result looks like, and points at `--calc`) or simply
  hold text; a column that mixed currencies while being summed; and rows
  summarised under an empty grouping key, which is what a merged label column
  looks like and points at `--fill-merged`.
- `--calc` and `--fill-merged` are accepted here too: the first evaluates
  formulas that have no cached value, the second copies a merged region's label
  into every row it spans. Both make excelize load the whole worksheet, so
  neither is on by default.

#### `--share` — this group's part of the whole

```bash
xlpeek agg book.xlsx -s 明细 --header --group-by 结算方式 --sum 收入金额 --count --share
```

```json
{"结算方式":"信用证","sum_收入金额":659101283.13,"count":668,
 "share_sum_收入金额":0.253620782765361,"share_count":0.256923076923077}
```

One `share_<field>` per summed field and for `count`, as a fraction rather than
a percentage, so `share_count` summing to 1 across the groups is a check the
caller can run. The total comes from the same scan that produced the groups —
never from a second query — so a workbook edited in between cannot be divided by
a total it no longer has, and a `--where` narrows the total exactly as it
narrows the groups. The same number is reachable from `--derive` as
`_total_<field>`:

```bash
xlpeek agg book.xlsx -s 明细 --header --group-by 结算方式 --sum 收入金额 \
    --derive "占比=sum_收入金额/_total_sum_收入金额"
```

Only additive fields have a share: `--avg`, `--min`, `--max`, `--count-distinct`
and `--agg` are not parts of a total, and asking for `--share` with none of
`--sum`/`--count` in the command is a usage error rather than a column of
nonsense. A zero total gives `null` and a warning, not a division.

### `--col` and `--agg` — the workbook's own formula engine

Both commands can hand a formula to the engine excelize already carries — the
one `--calc` uses to fill in a formula cell that has no cached result. A formula
is written the way a spreadsheet user writes it, with column names instead of
cell addresses:

```bash
# A period derived from a date column, then grouped.
xlpeek agg book.xlsx -s 明细 --header --col "月=MONTH(过账日期)" --group-by 月 --sum 收入金额

# A unit and currency conversion, as a row-level calculation.
xlpeek agg book.xlsx -s 明细 --header \
    --col "金额CNY=收入金额*IF(单位=\"千元\",1000,1)*IF(币种=\"USD\",7.2043,1)" \
    --group-by 结算方式 --sum 金额CNY

# Text cleaned before it is grouped.
xlpeek agg book.xlsx -s 明细 --header --col "干净备注=TRIM(SUBSTITUTE(备注,\" \",\"\"))" \
    --group-by 干净备注 --count

# A lookup into another sheet — free, because it is just a formula.
# Bound the lookup range: it is re-scanned for every row (see below).
xlpeek agg book.xlsx -s 明细 --header \
    --col "目标=VLOOKUP(客户编号,目标表!$A$1:$B$100,2,FALSE)" \
    --group-by 客户编号 --sum 收入金额 --avg 目标

# Distribution and conditional counts, per group.
xlpeek agg book.xlsx -s 明细 --header --group-by 分部 \
    --agg "中位金额=MEDIAN(收入金额)" --agg "p90=PERCENTILE(收入金额,0.9)" \
    --agg "超标单数=COUNTIFS(收入金额,\">100000\",销售区域,\"华东\")" \
    --agg "离散系数=STDEV(收入金额)/AVERAGE(收入金额)"

# The same derived column on a page of rows.
xlpeek read book.xlsx -s 明细 --header --col "月=MONTH(过账日期)" \
    --columns "凭证号,月" --where "月=3"
```

- **`--col "name=FORMULA"`** (`agg` and `read`, repeatable) computes a column
  per row. The result is a column like any other: `--group-by 月`, `--where 月=3`,
  `--sort-by 月`, `--sum 月`, and `--agg` may all name it, and a later `--col`
  may read it. In `read` it appears after the sheet's own columns, is listed in
  `header` and `derived_columns`, and can be projected with `--columns`.
- **`--agg "name=FORMULA"`** (`agg`, repeatable) computes one value per group
  over that group's rows. It sits beside `--sum` and friends — the existing
  flags are the common cases, this is the rest of the engine — and `--derive`
  works on its result like any other field.
- **A header row is required** for `--col` (`--header` or `--header-row N`),
  because a derived column is referred to by name and a sheet read without a
  header row has no names. A derived column may not be called something that is
  already a column of the sheet, or a column letter (`B`), an index (`2`) or a
  cell reference (`A1`): every later reference to it would otherwise be
  ambiguous, and the ambiguity would be silent.
- **The formula sees the values the file stores**, not the ones `--raw` or
  `--dates` render — a formula is evaluated by the engine against the worksheet,
  not against this tool's output. A column holding text dates is therefore
  directly usable: `MONTH(过账日期)` works on a cell holding `"2023-05-02"`,
  because the engine coerces it, while a column holding something else (`MONTH(客户名称)`)
  gives `#VALUE!` rather than a silent 0. To the engine, `12.35%` is text: the
  `--sum` tolerance for formatted numbers is not applied to a formula's inputs.
- **Cross-sheet references work as written and cost what they cost.** `目标表!$A:$B`
  is passed through untouched, which is what makes `VLOOKUP` free rather than a
  feature of its own; `--col` and `--agg` only rewrite the names that resolve to
  columns of the sheet being read. Referencing a sheet that does not exist is a
  usage error naming the sheets that do. **Bound a lookup range**: the cost of a
  reference is the rows the formula is evaluated over times the rows the range
  holds, so `VLOOKUP(x,目标表!$A:$B,2,FALSE)` re-scans the whole column for every
  row — 44 s for 2,700 rows against a 2,700-row column, and 0.8 s for the same
  lookup against `$A$1:$B$100` — and the response says so in `warnings` when a
  formula reads whole columns, because a 44-second command reads as a hang.
- **Volatile functions are refused.** `NOW`, `TODAY`, `RAND` and `RANDBETWEEN`
  are evaluated silently by the engine, so a command containing one answers a
  different question every time it runs — which is worse than not supporting it
  in a tool whose output is used to reconcile figures. The error names the
  function; filter on a date column, or pass the constant in the command.
- **A function the engine does not implement says so once.** `QUARTER` is the
  one an office report reaches for that is absent (write
  `ROUNDUP(MONTH(d)/3,0)`); a misspelled column or a malformed formula is
  caught before the scan rather than once per row, and exits 2 with the engine's
  own message.
- **A column that held nothing is named, not summed.** A formula whose cached
  result was never written — what openpyxl, pandas and xlsxwriter produce — reads
  as an empty cell, and the engine answers `SUM` over it with `0`: a number, and
  the wrong one to pass on quietly. A column an `--agg` reads that held nothing
  in any matched row says so and points at `--calc`, exactly as `--sum` does, and
  with `--calc` the same aggregate returns the computed total. A `--col` formula
  needs no `--calc` for this: it reads through the engine, which evaluates the
  formula cell it references.
- **A row the engine cannot compute is data.** `#DIV/0!`, `#VALUE!`, `#NUM!`
  and `VLOOKUP no result found` become the cell's value — an empty cell and a
  lookup that found nothing are different facts — and are counted per column in
  `warnings` (`--col`) or per formula in `warnings` (`--agg`, where such a group
  reports `null`).
- **Evaluation costs time and memory.** Both flags add roughly 0.3–0.5 ms per
  row to the scan, because each row is evaluated through the engine; a `--col`
  over 2,600 rows measures at about 0.7 s against 0.15 s without it, and `--agg`
  is 0.7 s whether the grouping produces 65 groups or 2,600, since every group is
  evaluated before the output is paged. Both also give up the streaming profile
  the way `--calc` does — the engine holds the worksheet in memory — and `--agg`
  keeps each group's values for the columns its formulas read until the scan
  ends, so the memory a run needs is proportional to the columns it reads.
  Nothing is written to the workbook: the formula and the group's values live on
  a scratch worksheet that is created on first use, removed before the command
  returns — failure paths included — and hidden from every sheet list in the
  response.

> **The output stays long-format.** `--group-by 分部,结算方式` returns one row per
> combination, not a matrix with one dimension across the top: a caller parsing
> the result needs a fixed schema, and a cross-tab's column count is decided by
> the data. Transposing it is a rendering decision, and it belongs to whoever is
> rendering.

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
xlpeek profile book.xlsx -s 明细 --header --calc          # resolve uncached formulas
xlpeek profile book.xlsx -s 明细 --header --fill-merged   # count merged labels
```

A type is read from the value the cell presents rather than from bare digits, so
`¥1,234.50` and `12.35%` are `number` — a number format is decoration on a stored
number, not a different kind of value. `--calc` and `--fill-merged` are accepted
here for the same reason they are on `agg`: without them a formula column reads
as `empty` and a merged label column as barely filled, and both are reported as
though that were a property of the data.

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

**`type` is the *usable* type, not the stored one.** `date` means the values can
be used as dates — compared, sorted, and passed to `MONTH` and the rest through
`--col` — and a column of dates held as text is `date` for exactly that reason.
It does not mean the cell stores a date: `--raw` on the same column returns the
string `"2023-05-02"` rather than a serial number, and a caller checking storage
types should read that instead of the profile.

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

`--offset` pages the matches the way `read` pages rows, counting matches rather
than rows, and `next_offset` is where to continue:

```bash
xlpeek find book.xlsx -s 明细 -v "已取消" --header -l 20 -o 20
```

`truncated: true` means there is at least one more match; the search settles
that by looking one match ahead rather than assuming, so the last page reports
`truncated: false` and `complete: true`. `truncated_approximate: true` marks the
case where it gave up looking rather than answered — the next match is more than
10,000 rows further on — and there it is a "may exist" rather than an assertion.

`find` takes `--dates` too, and rewrites the cells *before* searching them, so a
pattern written the way the response will read back — `2026-01-01` — is the
pattern that finds it.

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

The column may be a header name (in `--header` mode), a column letter (`B`), a
1-based index (`2`), or in `-s` the sheet likewise. A header name wins over a
letter, so a column literally named `A` is still addressable by name.

Everything after the first operator is the value, and a filter is exactly one
column, one operator and one value. An expression that does not parse is a usage
error rather than an interpretation, because the interpretations were worse than
the error: `金额>>100` read as the value `>100` and matched nothing,
`金额>100>200` matched one row, and `金额>` read as the empty value, which every
cell compares greater than, so it matched the whole sheet — all three reporting
success. A value that genuinely contains one of `> < = ~` is written in quotes:

```bash
xlpeek read book.xlsx -s 明细 --header --where "备注~'a>b'" -l 20
```

Comparison is numeric as soon as the filter value reads as a number; a cell that
does not read as one then fails to match rather than falling back to text
comparison. "Reads as a number" includes what a number format puts around a
value: thousands separators, one currency symbol on either side, and a trailing
`%`, which divides — `--where "比率>20%"` compares against 0.2, so a cell showing
`5.00%` does not match it, and tightening the threshold to `2%` can only ever
shrink the result.

When the filter value is *not* a number but the cells are, the comparison is
text — that is what a date range needs — and the response says so:

```
"warnings": ["filter \"金额>1OO\": the value \"1OO\" is not a number, but 3
cell(s) in column \"金额\" read as one (first: \"100\"), so they were compared
as text — ordered by their digits, not by their value; check the value for a
typo, or use ~ to search the text deliberately"]
```

That is the shape a mistyped numeric literal takes, and the rows it matches have
nothing to do with the rows the caller meant. `warning_count > 0` means the
filter answered a different question than it looks like it asked.

Equality is compared at the 15 significant digits the tool reports in, so
`--raw --where "比率=25.67%"` matches a cell holding `0.2567` — writing the value
as a percentage divides it by 100, and that quotient is one ulp away from the
double the cell stores. The range comparisons are unaffected.

A currency symbol is presentation only: it is stripped, never converted, so the
default path and `--raw` answer the same question, and a column mixing symbols
compares on magnitude alone — `agg` is where that mixture gets reported rather
than passed on. Values that are not numbers at all — `A-1`, `2024-08-21` — still
compare as text, which is what makes date ranges work. `--raw` remains the way to
compare at full stored precision, since a formatted cell is compared as it is
displayed.

## Header rows

`--header` is shorthand for `--header-row 1`. Both are accepted by `read`, `agg`,
`profile`, `find` and `info`; an explicit row wins. Use it when the table sits
below a title block, which is the common shape of a real report:

```bash
xlpeek read report.xlsx -s Sheet1 --header-row 3
```

`--header-row 0` means there is no header, and columns are addressed by letter or
index. `--col` needs a header row for a different reason — a derived column is
referred to by name, and a name needs a row of names — and says so rather than
guessing a position.

## Merged cells

A spreadsheet stores a merged value only in the top-left cell of the region and
leaves the rest empty, so a reader that ignores merges sees blanks wherever a
report used a merged heading or a label spanning several rows — and reasons
confidently from them.

```bash
xlpeek info book.xlsx --deep              # reports merged_ranges and a sample
xlpeek read book.xlsx -s 明细 --fill-merged
xlpeek agg book.xlsx -s 明细 --header --group-by 销售区域 --sum 收入金额 --fill-merged
xlpeek profile book.xlsx -s 明细 --header --fill-merged
```

`--fill-merged` copies each region's value into every cell it spans. Only blanks
are filled, so a region can never overwrite real data. `read`, `agg` and
`profile` all accept it, because a merged label distorts all three: a page with
blanks in it, a group filed under an empty key, a fill rate that is really a
merge. `agg` names the empty group when it sees one, so the case is visible even
without the flag. `--deep` surfaces merged regions because it is easy not to know
they are there.

## Output formats

`read --format tsv|csv|markdown` writes the JSON envelope, then a header line,
then the rows — tab-separated, comma-separated, or a markdown table. `tsv` is
the cheapest to feed to a model, `csv` is for anything that already reads CSV,
and `markdown` is for pasting into a report.

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
  opt-in on every command that takes them (`read`, `agg`, `profile`).
- `has_more` is exact when the end of the sheet is reached during the lookahead.
  If the lookahead exceeds 10,000 rows without a verdict (possible with a
  restrictive `--where`), it reports `has_more: true` together with
  `has_more_approximate: true`. The bias is deliberate: a false `false` would
  make a caller stop early and silently miss data, while a false `true` only
  costs one extra request. Rows that are formatted but empty do not count as
  more data, so a sheet with a long formatted tail ends instead of paging.
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

The tool is read-only by design. It can *evaluate* a formula the workbook
already carries — `--calc` fills in a formula cell with no cached result, and
`--col` / `--agg` run one — but it never sets one, and nothing is written to the
file: the scratch worksheet the engine needs is created on first use and removed
before the command returns.

Charts, images and conditional formatting are out of scope because excelize can
create them but exposes no getter, so there is nothing to read. Cell styles and
pivot-table definitions are readable in principle and are not implemented here.
A cross-tab or pivot *layout* is deliberately not offered: `agg` returns the long
form, one row per dimension combination, which is the shape a parser can rely on.
