# Changelog

All notable changes to this project are documented here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and the project
adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

## [1.1.0] - 2026-09-18

The third external review was a capability request rather than a defect report:
the workbook's own formula engine was already wired in for `--calc`, and the
office questions it was not reachable from — trend by month, a median that an
outlier cannot move, a lookup, a share of the total — had to be answered outside
the tool or not at all. Three flags open it up, and the boundary work is the
part that matters: an engine that answers a slightly different question every
run, or that turns a failed formula into an empty cell, would be worse than the
gap it fills.

### Added

- **`--col "name=FORMULA"` — a column computed per row** (`agg` and `read`,
  repeatable). Column names are written the way a spreadsheet user writes them,
  so the formula is `MONTH(过账日期)`, not `MONTH($B2)`. The result is a column
  like any other: groupable, filterable, sortable, usable inside `--sum` and
  `--agg`, and referenceable by a later `--col`. A text date works directly —
  `MONTH` on a cell holding `"2023-05-02"` coerces it, so a column that merely
  looks like a date can still be used to derive one, and a column that is not a
  date gives `#VALUE!` rather than a silent 0. Cross-sheet references pass
  through untouched, which is what makes `VLOOKUP(客户编号,目标!$A:$B,2,FALSE)`
  work without any lookup syntax of our own.
- **`--agg "name=FORMULA"` — an aggregate computed per group** (`agg`,
  repeatable). The rows of a group are almost never contiguous, so each column
  the formula reads is written to a scratch worksheet as one block per group and
  handed to the engine as a range: `MEDIAN(金额)`, `PERCENTILE(金额,0.9)`,
  `STDEV(金额)`, `COUNTIFS(金额,">100000",区域,"华东")` and the rest of the
  engine's 900-odd functions work over exactly the rows the group holds. It
  coexists with `--sum`/`--avg`/`--min`/`--max`/`--count`, and `--derive` still
  works on the result.
- **`--share` — each group's percentage of the total** (`agg`). Emits
  `share_<field>` for every summed field and for `count`. The total comes from
  the same scan as the groups, so a numerator and its denominator cannot belong
  to two different versions of the file, and the shares of a grouping add up to
  exactly 1. A `--where` narrows the total as well as the groups. The same
  number is available to `--derive` as `_total_<field>`, as in
  `--derive "占比=sum_收入金额/_total_sum_收入金额"`.
- Formula evaluation is documented as seeing the **stored** values of the sheet
  — the same values whatever `--raw` and `--dates` say — and as needing a header
  row (`--header` or `--header-row N`), because a derived column is referred to
  by name.

### Fixed / Guarded against

- **Volatile functions are refused, not evaluated.** `NOW`, `TODAY`, `RAND` and
  `RANDBETWEEN` are silently computed by the engine, so a command containing one
  returns a different number on every run — the opposite of what a tool used to
  reconcile figures is for. They are a usage error naming the function.
- **A function the engine does not implement says so once, with its name.**
  `QUARTER(过账日期)` fails the command (`USAGE`, exit 2) instead of producing a
  column of empty values; the message repeats that `ROUNDUP(MONTH(d)/3,0)` is
  the same value. A misspelled column name, a reference to a sheet that is not
  in the workbook, and a malformed formula are all caught before the scan starts
  rather than once per row.
- **An `--agg` over a column that held nothing reports the column, not a
  quiet zero.** A formula cell whose result was never cached — what openpyxl,
  pandas and xlsxwriter produce — reads as empty, and the engine answers `SUM`
  over it with `0`: a number where the same question asked through `--sum`
  returns `null` and a warning. The warning now exists on both paths, and
  `--calc` is the fix on both. A `--col` formula needs no `--calc` for this, and
  that is documented rather than left to be discovered: it reads through the
  engine, which evaluates the formula cell it references.
- **A function the engine gets wrong is named when it is handed a column.**
  `NPV(0.1,列)` discounts only the first cell of the range — measured at -909.09
  on `[-1000,1000,2000]`, where 1419.98 is correct — and the engine returns that
  number without complaint. An `--agg` formula that does this now says so and
  names the ways out; the same call with separate arguments is correct and stays
  silent. `IRR`, `XIRR`, `XNPV`, `PMT`, `PV` and `FV` were verified at the same
  time and read their arguments correctly.
- **A row the engine cannot compute is data, and says so.** `#DIV/0!`,
  `#VALUE!`, `#NUM!` and `VLOOKUP no result found` are passed through as the
  cell's value — an empty cell and a lookup that found nothing are different
  facts — counted per column, and summarised in `warnings`. A group whose
  `--agg` could not be computed reports `null`, with one warning naming how many
  of how many groups were affected.
- **The scratch worksheet never survives the command.** It is created on first
  evaluation, removed on the way out including on the failure paths, and hidden
  from every message that lists the workbook's sheets. A `serve` session that
  runs a formula request leaves the cached workbook exactly as it found it.
- **A whole-column reference is reported as the cost it is.** `VLOOKUP(x,目标!$A:$B,2,FALSE)`
  is ordinary spreadsheet practice and, here, the rows evaluated times the rows
  the range holds: 44 s for 2,700 rows against a 2,700-row column, 0.8 s against
  a 100-row range. The answer is right, so this is a warning rather than a
  refusal — but a 44-second command with no explanation reads as a hang, so the
  response names the reference and the arithmetic.
- `profile`'s `type` is documented for what it is: the **usable** type of a
  column, read from how the value presents itself, not from how the cell stores
  it. A date stored as text is `date` — and is directly usable as one, as above
  — while `--raw` still returns the string the file holds.

### Changed

- `--max-columns` caps the columns read from the worksheet; `--col` columns are
  added after them rather than being capped away, because a column the caller
  asked to have computed is not something to drop silently.

## [1.0.3] - 2026-09-17

Fixes for the fifteen findings of a second external review of 1.0.2, which
re-checked the four fixes above and found more of the same kind. Three of them
are the dangerous shape again — the command succeeded, the exit status was 0,
and the answer was wrong — and they are first.

### Fixed

- **A mistyped filter value silently turned a numeric comparison into a text
  one.** `--where "金额>1OO"`, with the digit 0 struck as the letter O, does not
  read as a number, so the comparison fell back to comparing text: `"2000" >
  "1OO"` holds and `"1000"` does not, and the caller got a plausible, complete,
  wrong set of rows. The result is still the one a text comparison produces —
  that is what was asked for, and dates need it — but the comparison is now
  counted, and a filter that compared text against cells that read as numbers
  says so in `warnings`, naming the value, the column, the number of cells and
  the first value it happened to.
- **A malformed filter expression returned a row count instead of an error.**
  `金额>>100` was parsed as the operator `>` applied to the value `>100` and
  compared as text, matching nothing; `金额>` was parsed as the value `""`, which
  every cell compares greater than, and matched the whole sheet; `金额>100>200`
  matched one row. All three returned `ok: true` and a count that looked like an
  answer. Filters are now parsed strictly: the column ends at the first operator,
  the value may not be empty, and a second operator in it is an error rather than
  part of the value — unless the value is quoted, which is how a value that
  really contains one is written. `金额>>=100` also reports a syntax error now,
  instead of blaming a column named `金额>`.
- **Paging treated formatted empty rows as data.** A spreadsheet's last row is
  its last *formatted* row: dragging a border past the data leaves the file
  claiming two hundred rows. `has_more` was computed from position, so the reader
  handed back blank pages with `complete: false` at the end of a sheet whose data
  had ended, and a caller that trusted `complete` never got to stop. `has_more`
  and `complete` now answer about content: a row with nothing in it does not
  count as more to fetch. A gap in the middle of a sheet is unaffected — the
  lookahead still scans it, exactly as before.
- **`--derive` could not reference an aggregate of a bracketed column.** For
  `--sum "[金额(万元)]"` the tool reports the field `sum_[金额(万元)]`, and the
  expression parser read that as a name followed by arithmetic, so a ratio — the
  thing derived metrics exist for — could not be computed from it. A bracketed
  run inside an operand is now part of the identifier, and brackets nest, so
  `[sum_[金额(万元)]]` is the same field. The bracket hint is offered only when
  the input could be a bare name, so it is no longer appended to a broken
  arithmetic expression as advice that does not work.
- **`agg --limit` defaulted to 0, meaning all groups.** Grouping by a
  near-unique column — an invoice number, a customer id — is a natural thing to
  do by accident, and returning one group per row is the whole table in a shape
  nobody reads. The default is now 1000, with `has_more`/`next_offset` to page
  the rest, and the response explains that it was the default that cut the list.
  `--limit 0` still asks for every group explicitly.
- **`--raw` made a percentage equality filter miss.** 25.67% is scaled to
  0.2567 when the filter value is parsed, and that product is one ulp away from
  the double the cell holds, so `--raw --where "比率=25.67%"` found nothing while
  `>25.66%` found the row. Equality now compares at the 15 significant digits the
  tool reports in; the range comparisons were never affected.
- **A bad column name in a filter was reported as a runtime failure.** It exits 2
  (`COLUMN_NOT_FOUND`) now, like every other error a caller can fix by editing
  the command, instead of 1, which says the workbook is the problem. Sheet
  references moved with it. A missing file is still exit 1: the path is not
  something the flag syntax can fix.
- **A boolean flag rejected its own value.** `--ignore-case true` put `true` in
  the operand list and reported "expected exactly one workbook path", which
  sends the caller to look at a path that is fine. Both words are accepted
  after a boolean flag now, and the operand error lists the arguments it
  received so the stray one is visible.

### Added

- **`--dates=display|iso|serial`** on `read` and `find`. A date is a number
  wearing a number format, so the same value reads as `2026-01-01 0:00:00`, as
  `01-01-26` or as `46023` depending on the file — and `01-01-26` is ambiguous
  to anything that parses it. `iso` gives `2026-01-01`, or
  `2026-01-01T09:30:00` when a time of day is stored; `serial` gives the stored
  number. The default is unchanged (`display`), because deciding which cells are
  dates means reading their number formats, which parses the worksheet — so the
  option is opt-in, like `--calc` and `--fill-merged`. Text that merely looks
  like a date is left alone.
- **`last_populated_row`** on `info --deep`: the row number of the last row
  holding anything, which is the one number that answers "where does the data
  end". `max_row` is the file's own claim and counts formatted empty rows,
  `last_row` is where the scan stopped, and `populated_rows` is a count.
- **`find --offset`**, with `next_offset` in the response and `truncated` now
  settled by a bounded lookahead instead of assumed: the last page of a search
  reports `truncated: false`, and `truncated_approximate` marks the case where
  the lookahead gave up rather than answered.
- **`read --format csv|markdown`**, alongside `json` and `tsv`.
- **`--columns "A:D"` ranges**, and the same between two header names, since
  writing out a run of letters is how the wrong columns get projected.
- **`-s` accepts a sheet index**, counted from zero because that is what the
  `index` and `sheet_index` fields already report. A sheet whose name is a
  number is still reached by name.
- `testdata/round2.xlsx` and `examples/regression/regress_round2.py`: a fixture
  of the shapes this round turned on (a percentage column, two hundred formatted
  empty rows, dates in three notations, a bracketed column name, a near-unique
  column) and one case per finding, with hand-written expectations.
  `examples/regression/make_fixtures.py` regenerates it.
- The regression suite checks its own delivery first: a missing fixture or an
  unrunnable binary is now reported once, by name, instead of failing every
  check with `FILE_NOT_FOUND`.
- `scripts/package_release.py`: builds the four targets, assembles them with the
  fixtures, the suites and the docs, runs the whole suite against the assembled
  copies, and writes a `SHA256SUMS` over everything shipped. The delivery is
  assembled rather than collected by hand, because the last one was missing the
  fixtures the suites measure against.

### Changed

- **`agg --limit` defaults to 1000.** See above; `--limit 0` is unlimited.
- **`complete` on `read` means "no more rows with content".** Rows that are
  formatted and empty at the end of a sheet are no longer offered as pages.
- `--format` and `--dates` values are validated with the same message shape, and
  the operand-count error names the arguments it received.

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

[Unreleased]: https://github.com/jbeetle/xlpeek/compare/v1.1.0...HEAD
[1.1.0]: https://github.com/jbeetle/xlpeek/compare/v1.0.3...v1.1.0
[1.0.3]: https://github.com/jbeetle/xlpeek/compare/v1.0.2...v1.0.3
[1.0.2]: https://github.com/jbeetle/xlpeek/compare/v1.0.1...v1.0.2
[1.0.1]: https://github.com/jbeetle/xlpeek/releases/tag/v1.0.1
