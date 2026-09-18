# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## What this is

`xlpeek` is a read-only, agent-facing CLI for reading and analysing XLSX workbooks.
Go 1.25+, a single flat `package main` at the repo root, one direct dependency
(`github.com/xuri/excelize/v2`). There are no subpackages and no importable library
API — the deliverable is the static binary, so everything shares one namespace.

Three documents define behaviour, and they are kept deliberately in sync:

- `README.md` — the user-facing contract and rationale. The "why" behind most decisions lives here.
- `docs/AGENTS.md` — the agent-facing reference (Chinese). Authoritative for how the tool is driven.
- `docs/SYSTEM_PROMPT.md` — a ~500-token compressed copy of the above.
- `CHANGELOG.md` — Keep-a-Changelog; bump `version` in `main.go` alongside it.

`examples/regression/verify_docs.py` **executes every bash command found in README.md and
docs/AGENTS.md**, so any doc edit must keep its examples runnable.

## Commands

```bash
go build -o bin/xlpeek.exe .     # Windows build
go build -o bin/xlpeek .         # macOS / Linux build
go test .                        # unit tests
go test -run TestParseFilter .   # a single test
gofmt -l .                       # CI gate: must print nothing
go vet ./...                     # CI gate
go test -race -timeout 20m ./...  # what CI actually runs (3 OSes × Go 1.25/1.26)
```

> `gofmt -l .` prints every `.go` file on a Windows checkout, because
> `core.autocrlf` writes CRLF and gofmt wants LF. The gate is meaningful on CI
> and on an LF checkout; locally, check the files you touched with
> `gofmt -l $(git diff --name-only -- '*.go')` after stripping CR.

Cross-compile (the shipped `bin/` artifacts):

```bash
CGO_ENABLED=0 GOOS=linux  GOARCH=amd64 go build -o bin/xlpeek-linux-amd64  .
CGO_ENABLED=0 GOOS=linux  GOARCH=arm64 go build -o bin/xlpeek-linux-arm64  .
CGO_ENABLED=0 GOOS=darwin GOARCH=arm64 go build -o bin/xlpeek-darwin-arm64 .
CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build -o bin/xlpeek.exe .
```

Behavioural suites (Python 3.7+; not part of `go test`):

```bash
python examples/regression/run_all.py     # all fourteen suites, ~130s, exit code is the verdict
python examples/regression/regress_agg.py # one suite
XLPEEK=/path/to/xlpeek python run_all.py  # test a specific build
node examples/nodejs/test.js              # 33 Node-wrapper checks
python examples/regression/make_fixtures.py  # rebuild testdata/round2.xlsx (needs openpyxl)
```

Packaging (the thing the last two review rounds asked for):

```bash
python scripts/package_release.py --test  # build 4 targets, assemble, run the suite in place, zip
```

Suites resolve the binary as `$XLPEEK`, else `bin/xlpeek.exe` / `bin/xlpeek-linux-<arch>`, else
`./xlpeek*`, else `PATH`.

`regress_shapes.py` is the suite to extend when a defect turns out to depend on the
*shape* of a workbook rather than the values in it: it reads `testdata/shapes.xlsx`
(one sheet per shape: number formats, uncached formulas, merged labels, bracketed
column names, percentage text) and its expectations are written out by hand, so the
suite stays stdlib-only and any change to that fixture must change the expectations
with it.

> **Build first; the fixtures are committed.** `bin/` is gitignored, so
> `go build -o bin/xlpeek.exe .` precedes any suite run. Nine of the ten read
> `testdata/华鼎科技_分部收入_FY23-FY25.xlsx` — a four-sheet synthetic workbook (FY23/FY24/FY25
> revenue details plus a 口径与汇率说明 sheet) that was referenced by the suites but absent from
> the repo until 2026-09-16, when all ten could not run. A suite failing with `FILE_NOT_FOUND` on
> that path means the fixture is missing again, not that the expectations are wrong.

## Architecture

Dispatch is `main.go:run()` → one `cmdX(args)` per command. Each command builds its own
`flag.FlagSet` via `newFlagSet` and parses with `parseFlags`, which calls `reorderArgs` first —
that is why flags may appear after the file operand (`read book.xlsx -s Sheet1`) even though
the stdlib `flag` package stops at the first positional.

| File | Owns |
| --- | --- |
| `main.go` | dispatch, flag reordering, usage text, `version`/`copyright` constants |
| `output.go` | the output contract: envelope, exit codes, error classification, `orderedRow`, TSV/CSV/markdown |
| `reader.go` | workbook open + session cache, sheet/column resolution, streaming paging, merges, formula fill |
| `commands.go` | `info`, `read`, `find` |
| `agg.go` | `agg`: grouping, aggregates, the comparability guardrail |
| `profile.go` | `profile`: per-column type, fill rate, distinct values |
| `filter.go` | `--where` parsing (strict) and matching |
| `expr.go` | arithmetic expression parser/evaluator shared by `agg` column and derive expressions |
| `dates.go` | `--dates`: number-format inspection and date serialisation |
| `serve.go` | the stdio line protocol and `ping` |
| `formula.go` | `--col` / `--agg`: the formula scanner, the scratch-sheet engine, volatile-function refusal |
| `office.go` | the spreadsheets habits that change an answer: hidden rows, subtotal rows, two-level headers |

### The output contract (`output.go`)

Every invocation writes **exactly one JSON envelope to stdout** — success or failure — so callers
have one parse path. Errors go to stdout, not stderr; exit codes are 0 success / 1 runtime /
2 usage. `help` is the single exception (plain usage text). Array fields are never `null`, and
two outputs sharing a field name are rejected rather than serialised as a duplicate JSON key
(`validateUniqueFields`), because a parser keeps only the last one. Every response reports its own
`data_bytes` and adds `data_warning` above 512 KB. `orderedRow` exists so header-mode rows keep
worksheet column order and are not HTML-escaped by `encoding/json`.

Errors are mapped to stable codes by `classify`; failures that a caller can recover from name the
alternatives (`failWithSheet` lists the real sheet names, column errors give the expected format).

### Reading pipeline (`reader.go`)

`openWorkbook` → `resolveSheet` (empty name = first sheet, name match is case-insensitive) →
`resolveColumn` (header name wins over column letter, then 1-based index) → `f.Rows()`.

The excelize `Rows` iterator is **forward-only and positional**: it yields one iteration per
spreadsheet row number, filling sparse gaps with empty rows, so the loop counter *is* the 1-based
row number. Offsets therefore cost a scan but not memory; `--where` is applied while walking, which
is why `--offset` counts *returned* rows rather than sheet rows. Rows are padded to a uniform width
(`padRow`) so a page never changes shape, and `headerKeys` guarantees unique keys (blank header →
column letter, duplicates → `_2` suffix).

Three flags give up the streaming profile by making excelize parse the whole worksheet into memory,
so they are opt-in: `--calc`, `--fill-merged`, and `info --deep`.

### Cross-cutting invariants

These are the reason the code looks the way it does; preserve them when editing.

- **Honest truncation.** `complete`, `truncated`, `has_more`, `has_more_approximate`
  (set after a 10,000-row lookahead gives no verdict — biased to over-report), `columns_capped`,
  `distinct_capped`, `values_omitted`. A caller must never have to infer whether it holds all the data.
- **Absent is not zero.** Blank cells are skipped by aggregates rather than counted as zero;
  `expr.go` propagates presence through its `lookup func(string) (float64, bool)` callback, and
  inside an expression a missing operand becomes 0 the way a spreadsheet treats an empty cell.
  A group with nothing to aggregate reports `null`, not `0`.
- **Numeric exactness.** `compensatedSum` (Neumaier) then `roundToExcel` (15 significant digits,
  Excel's own limit). `--raw` bypasses to stored values. `parseNumber` tolerates thousands
  separators but deliberately does *not* strip currency symbols or `%`.
- **The comparability guardrail** (`agg.go`): `unaccounted_columns` reports columns that neither
  group nor aggregate yet vary across the aggregated rows — the mixed-units/mixed-currency case
  where a total is arithmetically right and semantically meaningless. `isHazardName` flags names
  that look like units/currencies (Chinese substrings; English whole segments, so `unit_price`
  fires but `opportunity` does not). Grouping beyond 1,000 groups without `--limit` warns, and
  `--limit` itself now defaults to 1,000 (`aggDefaultGroupLimit`) so that accident costs a page
  rather than a context window.
- **A comparison that answers in text what was asked in numbers says so.** `filter.match` counts
  every cell that read as a number while the filter value did not (`fallbacks`, reported by
  `filterWarnings` in `read`/`agg`/`profile`), because that is the shape a mistyped numeric
  literal takes and the rows it matches are unrelated to the question. Filter *syntax* errors are
  rejected outright (`operatorAt`): one column, one operator, one value, quoting to escape.
- **Exit 2 means editing the command could fix it.** `argumentFailure` carries its own error code
  (`badColumn`) and `exitFor` turns it into the status; a missing file or an unreadable workbook
  stays exit 1. Anything new that reports a *bad argument* should go through these, or callers
  lose the only signal that separates "I asked wrong" from "the file is odd".
- **Paging answers about content, not position.** `readPage`'s lookahead only counts a row that
  holds something as "more to fetch", so a formatted-but-empty tail ends the scan instead of
  producing blank pages with `complete: false`. `info --deep`'s `last_populated_row` is the
  position a caller should plan against; `max_row` is the file's own hint.
- **A formula is evaluated, never written, and never trusted to be stable.** `formula.go` owns
  the boundary: volatile functions (`NOW`/`TODAY`/`RAND`/`RANDBETWEEN`) are refused because the
  same command must not answer differently twice; an unsupported function, a misspelled column, a
  bad sheet reference and an unbalanced formula are all usage errors *before the scan*, because
  they fail identically on every row; and everything else the engine fails on (`#DIV/0!`,
  `#VALUE!`, `VLOOKUP no result found`) becomes the cell's value and is counted, since an empty
  cell and a lookup that found nothing are different facts. Evaluation happens on a scratch
  worksheet that is created lazily and deleted on the way out — writing into the sheet being read
  invalidates the row iterator (measured at 3m43s for 2,600 rows against 1.3s), and a sheet left
  behind would show up in every later sheet list, including `serve`'s cached workbooks.
  A whole-column reference is allowed but warned about: its cost is the rows evaluated times the
  rows the range holds.

### `serve` mode

One request line in, one response line out; `handleRequest` re-enters `run()` with exactly the
one-shot CLI arguments, so there is no second syntax. Session state (the LRU workbook cache, the
flags that make it work) lives in `reader.go` as package globals guarded by `sessionEnabled`.
Commands must release handles with `releaseWorkbook(f)`, not `f.Close()`, or the cache will hold a
closed file; every command does now, but it is the thing to check when adding one. The idle
timeout exits silently and deliberately: an unsolicited line would be mistaken for the next
response.

## Untracked local material

`bugs/` holds the reviews, which are untracked: `round1/` is the black-box review of the shipped
1.0.1 binary (2026-09-16, tickets D1–D7) and `round2/` the review of 1.0.2 (2026-09-17, fifteen
findings P1-1…P3-5 plus D6/D7). Both are worth reading before changing `--where`, paging or the
delivery: nearly everything in them is a case where the tool returned `ok: true` with an answer
that was plausible and wrong.

The round-1 repro script is usable as an acceptance test (`python bugs/round1/repro_xlpeek_defects.py
bin/xlpeek.exe`), with one caveat: its D4 assertion runs `agg` **without** `--fill-merged` and
expects merged labels to be filled anyway. Merge filling is opt-in on every command, so five of
its six assertions pass and that one reports a failure by design.

Every round-2 finding has a case in `examples/regression/regress_round2.py`, which is the
tracked counterpart — when one of those tickets is reopened, extend that suite rather than
re-running the review by hand. The reports themselves stay untracked because they describe a
past binary, but the cases they produced are the part worth keeping.

`round3/` (2026-09-17) is the first round that is a **capability request** rather than a defect
report: expose the excelize formula engine that `--calc` already used, as `--col` (a formula per
row), `--agg` (a formula per group) and `--share` (a group's part of the total). It ships a
`xlpeek-feasibility-check/` program that measures the engine's behaviour and cost — the numbers
in §2 of its request are worth re-running when excelize is upgraded, because the request's
argument is that the work is cheap and that argument expires with the dependency. Its §3 names
what it does *not* want: no cross-tab output (the long form is deliberate), no pivot-table or
chart reading (excelize has no chart getter), and `--sheets` as the lowest priority of the
optional items. What it asks for in §5 is the part that became the boundary work in
`regress_round3.py`: volatile functions refused, unimplemented ones named, a failed row
distinguishable from an empty one.

`bugs/roadmap-office-gaps.md` (2026-09-18) is the internal counterpart to all of that: the office
habits that make an answer disagree with what the person looking at the sheet sees, each with a
measured repro and a source-verified feasibility note. Its first batch shipped as 1.2.0 (hidden
rows, subtotal rows, two-level headers, file fingerprint, full-width digits) and is asserted in
`examples/regression/regress_office.py` against `testdata/office.xlsx`, where the expectations are
stated as the numbers the person would read off the screen. The rest of the list — multi-sheet
union, CSV import, cell fill colours, pivot-table definitions — is unstarted and ordered by
frequency times risk.

`bugs/round3/xlpeek-remediation-request-round3.md` also carries the client's own measurements,
which are optimistic in one place worth knowing about: their 0.02 ms/row for a per-row formula
was measured without streaming the sheet at the same time and with the formula cell on the same
worksheet. Through the scratch worksheet, with the row iterator running, it is ~0.3–0.5 ms/row
(0.7 s for the 2,600-row fixture).
