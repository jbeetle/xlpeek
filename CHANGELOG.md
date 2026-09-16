# Changelog

All notable changes to this project are documented here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and the project
adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

## [1.0.2] - 2026-09-16

Fixes for four ways the tool could return a result that looked fine and was not.
All four were found by an external black-box review of 1.0.1, and each was
reproduced here on a real workbook before it was fixed.

### Fixed

- **`--where` fell back to comparing text, silently.** A filter value carrying a
  number format — `比率>20%`, `金额>¥2000` — did not parse as a number, so the
  comparison became lexicographic: `5.00%` matched `>20%`, and `金额>$2000`
  matched every row. Currency symbols, thousands separators and a trailing `%`
  now parse, so the default path and `--raw` answer the same question. `%` is a
  scale (`20%` is 0.2) and a currency symbol is presentation only — stripped,
  never converted.
- **`profile` typed formatted numeric columns as `text`.** `¥#,##0.00` and
  `0.00%` columns reported no `min`/`max`/`mean` and looked like text on the
  strength of their display format. Type inference now reads through the
  format, so the same column is a number either way.
- **A formula column with no cached value was silently empty.** Program-generated
  workbooks (openpyxl, pandas, xlsxwriter) write no cached results, so `agg`
  returned `null` with `warning_count: 0`. A referenced column that produces no
  numeric values is now reported, and the message separates empty cells — which
  is what an uncached formula looks like, and names `--calc` — from cells that
  hold text.
- **`--count-distinct` on a text column reported its values as skipped.** Cells
  read only to be counted distinctly are no longer parsed as numbers, so a text
  column no longer warns that "N of N cells could not be read as numbers".
- **`agg` and `profile` accepted neither `--calc` nor `--fill-merged`.** Both
  commands now take both, matching `read`: formulas evaluate, and a merged label
  reaches the rows it covers instead of filing them under an empty key.

### Added

- `agg` warns when a referenced column mixed currencies while being summed, and
  when rows were summarised under an empty grouping key (the merged-label case).
- Aggregate expressions that fail on a column name carrying a parenthesis or a
  space — `--sum "金额(万元)"` — now name the bracket escape in the error
  (`[金额(万元)]`), and the escape is documented in README and docs/AGENTS.md.
  Real workbooks use such names, and the error gave no way to find the fix.
- `testdata/shapes.xlsx` and `examples/regression/regress_shapes.py`: a
  committed fixture for the five shapes that produced these defects (a
  ¥#,##0.00 money column, a 0.00% ratio column, an uncached formula column, a
  label merged across rows, a column named `金额(万元)`, and a column of
  percentage text), and a stdlib-only suite with hand-written expectations. The
  existing suites all read one unformatted workbook, which is why none of this
  was covered before.
- `agg`/`profile` release their workbook through the session cache like every
  other command, instead of closing a handle `serve` is holding.

## [1.0.1] - 2026-09-15

First public release, as `xlpeek`. Previously developed privately under the
name `excelcli`.

### Commands

- `info` — workbook overview: sheets, dimensions, header, sample rows, merged
  regions.
- `read` — streaming, paginated row reader with column projection, filtering,
  header-row selection, merged-cell filling, formula evaluation and TSV output.
- `agg` — group and aggregate without moving every row across the wire.
- `profile` — per-column type, fill rate, distinct count and value enumeration.
- `find` — locate a value by substring or regular expression.
- `serve` — hold workbooks open across a stream of requests (~30× faster per
  query than a fresh process), with `ping`, `--cache` and `--idle-timeout`.

### Guardrails

Behaviour aimed at a caller that cannot see the data itself:

- `unaccounted_columns` reports columns that shape what an aggregate *means*
  without taking part in it — the mixed-units case that makes a total silently
  wrong.
- `warning_count`, `complete`, `values_omitted` state plainly what would
  otherwise have to be inferred.
- `data_bytes` reports each response's size, with a `data_warning` above 512 KB.
- Duplicate JSON keys are rejected rather than emitted, because a parser keeps
  only the last one.
- Array fields are never `null`.
- Error codes distinguish `PASSWORD_REQUIRED` from `INVALID_PASSWORD`, and name
  the available sheets or columns so a caller can correct itself in one step.

[Unreleased]: https://github.com/jbeetle/xlpeek/compare/v1.0.2...HEAD
[1.0.2]: https://github.com/jbeetle/xlpeek/compare/v1.0.1...v1.0.2
[1.0.1]: https://github.com/jbeetle/xlpeek/releases/tag/v1.0.1
