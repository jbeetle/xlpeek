Opens up the workbook's own formula engine — the one `--calc` already used to fill
in an uncached result — as three flags: `--col` computes a column per row,
`--agg` computes an aggregate per group, and `--share` gives each group's
percentage of the total. Together they reach the office questions that were
previously out of range: a trend by month, a median an outlier cannot move, a
lookup into another sheet, a share of the whole.

The boundary work is the part that matters. This release is mostly about
refusing to answer quietly: a formula whose value changes between two runs is
refused, one that cannot work at all fails once with its name instead of once
per row, and a row the engine cannot compute is reported as data rather than
smoothed into an empty cell.

### Added

- **`--col "name=FORMULA"` — a column computed per row** (`agg` and `read`,
  repeatable). Column names are written the way a spreadsheet user writes them,
  so the formula is `MONTH(过账日期)`, not `MONTH($B2)`. The result is a column
  like any other: groupable, filterable, sortable, usable inside `--sum` and
  `--agg`, and readable by a later `--col`. A column of text dates is directly
  usable — the engine coerces `"2023-05-02"` — and a column that is not a date
  gives `#VALUE!` rather than a silent 0. Cross-sheet references pass through
  untouched, which is what makes `VLOOKUP(客户编号,目标表!$A$1:$B$100,2,FALSE)`
  work without a lookup syntax of its own.
- **`--agg "name=FORMULA"` — an aggregate computed per group** (`agg`,
  repeatable). A group's rows are almost never contiguous, so each column the
  formula reads is written to a scratch worksheet as one block per group and
  handed to the engine as a range: `MEDIAN`, `PERCENTILE`, `STDEV`, `COUNTIFS`,
  `SUMIFS` and the rest of the engine's 912 functions work over exactly the rows
  the group holds. It coexists with `--sum`/`--avg`/`--min`/`--max`/`--count`,
  and `--derive` still works on the result.
- **`--share` — each group's percentage of the total** (`agg`). Emits
  `share_<field>` for every summed field and for `count`. The total comes from
  the same scan as the groups, so a numerator and its denominator cannot belong
  to two versions of the file, and a `--where` narrows both. The same number is
  available to `--derive` as `_total_<field>`.
- Formula evaluation is documented as seeing the **stored** values of the sheet
  — the same values whatever `--raw` and `--dates` say — and as needing a header
  row, because a derived column is referred to by name.

### Guarded against

- **Volatile functions are refused, not evaluated.** `NOW`, `TODAY`, `RAND` and
  `RANDBETWEEN` are computed silently by the engine, so a command containing one
  returns a different number on every run. They are a usage error naming the
  function.
- **A function the engine does not implement says so once, with its name.**
  `QUARTER` fails the command (`USAGE`, exit 2) instead of producing a column of
  empty values, and the message repeats that `ROUNDUP(MONTH(d)/3,0)` is the same
  value. A misspelled column, a sheet that is not in the workbook and an
  unbalanced formula are caught before the scan rather than once per row.
- **A row the engine cannot compute is data.** `#DIV/0!`, `#VALUE!`, `#NUM!` and
  `VLOOKUP no result found` become the cell's value — an empty cell and a lookup
  that found nothing are different facts — and are counted per column or per
  group in `warnings`.
- **A whole-column reference is reported as the cost it is.** `目标表!$A:$B` is
  ordinary spreadsheet practice and, here, the rows evaluated times the rows the
  range holds: 44 s for 2,700 rows against a 2,700-row column, 0.8 s against a
  100-row range.
- **A column that held nothing is named, not summed.** A formula cell whose
  result was never cached reads as empty, and the engine answers `SUM` over it
  with `0` — a number. `--agg` now warns exactly as `--sum` does; `--calc` is the
  fix on both paths.
- **An engine function that misreads a column argument is named.**
  `NPV(0.1,列)` discounts only the first cell of a range — measured at -909.09 on
  `[-1000,1000,2000]`, where 1419.98 is correct — and the engine returns that as
  a number. The warning gives the measurement and the ways out. `IRR`, `XIRR`,
  `XNPV`, `PMT`, `PV` and `FV` were verified at the same time.
- **Two ways a row could carry the same JSON key twice are refused**
  (`--group-by count --count`, and a grouping column an aggregate is also named
  after), because a parser keeps only the last copy.
- The scratch worksheet the engine needs is created on first use, removed on the
  way out including on failure paths, and hidden from every sheet list — a
  `serve` session leaves a cached workbook exactly as it found it.

## Verification

The bundle is assembled by `scripts/package_release.py`, which builds the four
targets with the documented flags, copies the fixtures and suites in, **runs the
full suite against the assembled copies**, and writes `SHA256SUMS` over
everything shipped. `sha256sum -c SHA256SUMS` in the unpacked directory
verifies it; nothing in it is generated after the checksums are taken.

```bash
python scripts/package_release.py --test    # what produced this release
```

Thirteen suites, 58 unit tests, and the Node wrapper's 33 checks. The third
review's cases live in `examples/regression/regress_round3.py`; the medians, the
conditional counts and the currency conversion it asserts were cross-checked
against an independent openpyxl pass, and the fixture sheet it measures against
— mean 2092 against median 120 — is the illustration from that review.

## Binaries

`CGO_ENABLED=0`, `-trimpath` and `-buildvcs=false`: statically linked, no
runtime or C library needed alongside them, no build-machine paths or git state
inside, and rebuildable to the same bytes from this tag.

| Asset | Platform | Size | SHA-256 |
| --- | --- | --- | --- |
| `xlpeek.exe` | Windows / amd64 | 14.4 MB | `a8d10651953dd01984fabfc96ab98216353db32a0541e2e6a1651ba17ffd371f` |
| `xlpeek-linux-amd64` | Linux / amd64 | 14.2 MB | `5964f4be759b5674a0a786ce4edd6e3404a9f40e7045b6c63ae4b9e79f526fe4` |
| `xlpeek-linux-arm64` | Linux / arm64 | 13.2 MB | `cb453b3e48a7eb1e2222146d7ce06a7a31da3a5355781255a4ca9ba814e7773e` |
| `xlpeek-darwin-arm64` | macOS / arm64 | 13.5 MB | `069f96ce2695d37b54816de50ca9c22629ce747a8a2fc72a5f9ffa7a08a1fe85` |

The 1.1.0 bundle is `xlpeek-1.1.0.zip`, 29.4 MB, holding 46 files: the four
binaries above, `testdata/`, the suites, the docs and the licence. Its own
SHA-256 travels with the delivery rather than in this file — a checksum of a
file printed inside that file can never be right — and every file it contains is
listed in the `SHA256SUMS` beside them.

Install from source instead: `go install github.com/jbeetle/xlpeek@v1.1.0`.
