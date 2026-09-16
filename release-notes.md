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

## Binaries

`CGO_ENABLED=0` and `-trimpath`: statically linked, no runtime or C library needed alongside them, and rebuildable to the same bytes from this tag.

| Asset | Platform | Size | SHA-256 |
| --- | --- | --- | --- |
| `xlpeek.exe` | Windows / amd64 | 14.2 MB | `7c23c1282c53e87f3d9dcf74b967287214c5dab0eac3682578688f52ce804df3` |
| `xlpeek-linux-amd64` | Linux / amd64 | 14.1 MB | `927b0bc7b3f8a6550a0b98bcd2070d0908910d0db2111b55fd7a05646acce430` |
| `xlpeek-linux-arm64` | Linux / arm64 | 13.0 MB | `bf08b189e66b3187c3bcf18e33dd482f56c2ed0338b2c6374977f44db4818d1b` |
| `xlpeek-darwin-arm64` | macOS / arm64 | 13.3 MB | `2b7ee8adcb31e713983d2a08607d6c7d9ab564942832ea0fa085bbf6df0b9528` |

Install from source instead: `go install github.com/jbeetle/xlpeek@v1.0.2`.
