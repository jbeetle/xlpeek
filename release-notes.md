Fixes for the fifteen findings of a second external review of 1.0.2. Three of
them returned `ok: true` with a plausible wrong answer, and those are first: a
mistyped filter value that silently became a text comparison, malformed filter
expressions that returned a row count instead of an error, and paging that
treated formatted empty rows as data.

### Fixed

- **A mistyped filter value silently turned a numeric comparison into a text
  one.** `--where "金额>1OO"` — the digit 0 struck as the letter O — does not
  read as a number, so the comparison fell back to comparing text and returned a
  complete, plausible, wrong set of rows. The result is unchanged (dates need
  text comparison) but the comparison is now counted, and the response carries a
  warning naming the value, the column, the number of cells and a sample.
- **Malformed filter expressions returned a row count instead of an error.**
  `金额>>100` matched nothing, `金额>100>200` matched one row, and `金额>` —
  read as the empty value, which every cell compares greater than — matched the
  whole sheet. Filters are parsed strictly now; quoting is how a value that
  really contains an operator is written.
- **Paging counted position rather than content.** A sheet's last row is its last
  *formatted* row, so a border dragged past the data produced blank pages with
  `complete: false` and no way for a caller to stop. `has_more` and `complete`
  now answer about content.
- **`--derive` could not reference an aggregate of a bracketed column**, so a
  ratio — what derived metrics exist for — could not be computed from
  `--sum "[金额(万元)]"`. A bracketed run inside an operand is part of the
  identifier now, and brackets nest.
- **`agg --limit` defaulted to 0 (all groups).** It defaults to 1000, with
  `has_more`/`next_offset` for the rest and a warning that says the default cut
  it; `--limit 0` still asks for everything.
- **`--raw --where "比率=25.67%"` missed.** Equality is compared at the 15
  significant digits the tool reports in, which absorbs the one-ulp difference
  between `25.67/100` and the `0.2567` the cell holds.
- **A bad column name in a filter was reported as a runtime failure.** It is
  exit 2 (`COLUMN_NOT_FOUND`) now, like every other error the command can fix;
  sheet references moved with it. A missing file is still exit 1.
- **`--ignore-case true` was rejected** and reported as a stray workbook path.
  Boolean flags accept both words now, and the operand error names the arguments
  it received.

### Added

- **`--dates=display|iso|serial`** on `read` and `find`: `2026-01-01` instead of
  `01-01-26` or `46023`, for callers that have to compare or sort dates. Opt-in,
  because deciding which cells are dates means reading their number formats.
- **`last_populated_row`** on `info --deep`: where the data actually ends.
- **`find --offset`**, with `next_offset`, and a `truncated` settled by looking
  one match ahead instead of assumed.
- **`read --format csv|markdown`**, `--columns "A:D"` ranges, and `-s` by index.
- `testdata/round2.xlsx` + `regress_round2.py`: one case per finding, most of
  them asserting the error. The fixture is regenerable
  (`examples/regression/make_fixtures.py`).
- The regression suite checks its own delivery before its assertions: a missing
  fixture or an unrunnable binary is reported once, by name.

## Verification

The bundle is assembled by `scripts/package_release.py`, which builds the four
targets with the documented flags, copies the fixtures and suites in, **runs the
full suite against the assembled copies**, and writes `SHA256SUMS` over
everything shipped. `sha256sum -c SHA256SUMS` in the unpacked directory
verifies it; nothing in it is generated after the checksums are taken.

```bash
python scripts/package_release.py --test    # what produced this release
```

## Binaries

`CGO_ENABLED=0`, `-trimpath` and `-buildvcs=false`: statically linked, no
runtime or C library needed alongside them, no build-machine paths or git state
inside, and rebuildable to the same bytes from this tag.

| Asset | Platform | Size | SHA-256 |
| --- | --- | --- | --- |
| `xlpeek.exe` | Windows / amd64 | 14.3 MB | `54881c1cdfe415a1fd7a28b41eef38d4dca04d56ee6ccbdfbdc4dce61b1c5939` |
| `xlpeek-linux-amd64` | Linux / amd64 | 14.1 MB | `535d62d88763c415f32b829c4941f80f24d5a10ffb8c928c4e5adaaa37b7367a` |
| `xlpeek-linux-arm64` | Linux / arm64 | 13.0 MB | `21e73452ad06f6193526bc8cf397df626aec058f015853fb96dff81da65e59cf` |
| `xlpeek-darwin-arm64` | macOS / arm64 | 13.4 MB | `be2ffc3f71bc51dcc7df793f635148951c3db9fc49f032e8ae117f18daf1678e` |

The 1.0.3 bundle is `xlpeek-1.0.3.zip`, 30.5 MB, holding 45 files: the four
binaries above, `testdata/`, the suites, the docs and the licence. Its own
SHA-256 travels with the delivery rather than in this file — a checksum of a
file printed inside that file can never be right — and every file it contains is
listed in the `SHA256SUMS` beside them.

Install from source instead: `go install github.com/jbeetle/xlpeek@v1.0.3`.
