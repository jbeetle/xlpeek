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

Cross-compile (the shipped `bin/` artifacts):

```bash
CGO_ENABLED=0 GOOS=linux  GOARCH=amd64 go build -o bin/xlpeek-linux-amd64  .
CGO_ENABLED=0 GOOS=linux  GOARCH=arm64 go build -o bin/xlpeek-linux-arm64  .
CGO_ENABLED=0 GOOS=darwin GOARCH=arm64 go build -o bin/xlpeek-darwin-arm64 .
CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build -o bin/xlpeek.exe .
```

Behavioural suites (Python 3.7+; not part of `go test`):

```bash
python examples/regression/run_all.py     # all eleven suites, ~80s, exit code is the verdict
python examples/regression/regress_agg.py # one suite
XLPEEK=/path/to/xlpeek python run_all.py  # test a specific build
node examples/nodejs/test.js              # 33 Node-wrapper checks
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
| `output.go` | the output contract: envelope, exit codes, error classification, `orderedRow`, TSV |
| `reader.go` | workbook open + session cache, sheet/column resolution, streaming paging, merges, formula fill |
| `commands.go` | `info`, `read`, `find` |
| `agg.go` | `agg`: grouping, aggregates, the comparability guardrail |
| `profile.go` | `profile`: per-column type, fill rate, distinct values |
| `filter.go` | `--where` parsing and matching |
| `expr.go` | arithmetic expression parser/evaluator shared by `agg` column and derive expressions |
| `serve.go` | the stdio line protocol and `ping` |

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
  fires but `opportunity` does not). Grouping beyond 1,000 groups without `--limit` warns.

### `serve` mode

One request line in, one response line out; `handleRequest` re-enters `run()` with exactly the
one-shot CLI arguments, so there is no second syntax. Session state (the LRU workbook cache, the
flags that make it work) lives in `reader.go` as package globals guarded by `sessionEnabled`.
Commands must release handles with `releaseWorkbook(f)`, not `f.Close()`, or the cache will hold a
closed file; every command does now, but it is the thing to check when adding one. The idle
timeout exits silently and deliberately: an unsolicited line would be mistaken for the next
response.

## Untracked local material

`bugs/` is an untracked black-box review of the shipped 1.0.1 binary (dated 2026-09-16): a report,
a ticket list (D1–D7), and a Python repro script. D1–D6 are fixed in 1.0.2 — `--where` no longer
degrades to lexicographic comparison on formatted values, `profile` reads through number formats,
a formula column that yields nothing is reported by `agg` instead of returning a bare `null`,
`agg`/`profile` take `--calc` and `--fill-merged`, and the regression fixture was restored. D7 is
about the binary-only delivery that predates this repository.

The repro script is useful as an acceptance test (`python bugs/repro_xlpeek_defects.py
bin/xlpeek.exe`), with one caveat: its D4 assertion runs `agg` **without** `--fill-merged` and
expects merged labels to be filled anyway. Merge filling is opt-in on every command, so five of
its six assertions pass and that one reports a failure by design.
