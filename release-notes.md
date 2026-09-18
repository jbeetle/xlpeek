Closes the gap between what the file holds and what the person looking at the
sheet sees. A saved filter, a collapsed outline group, a subtotal row written
into the detail column, a title merged over the column names — each one makes a
faithful read of the file produce a number that is arithmetically right and not
the answer to the question, and three of them used to do it silently:

    hidden rows       the file totalled 2100 where Excel shows 1600
    subtotal rows     1700 where the report itself states 700
    full-width        the column totalled 500 where it holds 2968

This release is the first that comes from our own review rather than a client
finding, and every claim in it was measured before it was written.

### Added

- **Rows the sheet hides are counted and can be skipped.** A saved filter, a
  collapsed outline group and a row hidden by hand all leave `hidden="1"` on the
  row, which the streaming iterator already carries — so the count costs one
  comparison per row and no memory. An answer that read rows the sheet hides now
  says how many and why, and `--visible-only` reads the view instead of the
  file. `info --deep` reports `hidden_rows`.
- **Subtotal rows are named and can be dropped.** A row whose first cell reads
  小计 / 合计 / 总计 / 累计 / 其中 / subtotal / total — optionally with a scope, as
  in `小计：华东` — is the report's own total of other rows, and summing the
  column counts it twice. It is reported by row and wording, and
  `--exclude-totals` leaves it out.
- **`--header-rows N` reads a header written on two rows.** A title merged over
  the columns it groups is the shape of most real reports; the span is joined
  into one name per column (`2024年` + `金额` → `2024年金额`), with
  `--fill-merged` spreading a merged title to the columns it covers. Accepted by
  `read`, `agg`, `profile`, `find` and `info`.
- **Full-width digits are numbers.** `１２３４` and `１，２３４` — what a figure
  looks like after Word or WeChat — were text to every parser, so the column
  totalled 500 instead of 2968 with only the skipped-cell count hinting at it.
  Digits, the comma, the period, the percent sign and the signs are normalised.
- **Every response carries the file's fingerprint.** `file_size` and
  `file_mtime` always; `--fingerprint` adds `file_sha256`, the one that survives
  a copy, a rename or a checkout, so a figure that has to be reconciled a week
  later says which version of the workbook produced it.

### Changed

- `read` reports `header_rows`, `visible_only` and `exclude_totals`; `find` gained
  `warnings` and `warning_count`, which it did not have before.

## Verification

The bundle is assembled by `scripts/package_release.py`, which builds the four
targets with the documented flags, copies the fixtures and suites in, **runs the
full suite against the assembled copies**, and writes `SHA256SUMS` over
everything shipped. `sha256sum -c SHA256SUMS` in the unpacked directory
verifies it; nothing in it is generated after the checksums are taken.

```bash
python scripts/package_release.py --test    # what produced this release
```

Fourteen suites, 64 unit tests, and the Node wrapper's 33 checks. The office
shapes live in `testdata/office.xlsx` and are asserted in
`examples/regression/regress_office.py` against what the person looking at the
sheet would say, not against what the file contains.

## Binaries

`CGO_ENABLED=0`, `-trimpath` and `-buildvcs=false`: statically linked, no
runtime or C library needed alongside them, no build-machine paths or git state
inside, and rebuildable to the same bytes from this tag.

| Asset | Platform | Size | SHA-256 |
| --- | --- | --- | --- |
| `xlpeek.exe` | Windows / amd64 | 14.4 MB | `2230657f78a94994be3ec875e0dfe530969d4be467a7e03c45528959007b4baa` |
| `xlpeek-linux-amd64` | Linux / amd64 | 14.3 MB | `5ff793bf7995b72030085b048e2aaacda73f9c5657996379b01efc826e2d9d1f` |
| `xlpeek-linux-arm64` | Linux / arm64 | 13.2 MB | `5d654378b821feb264b09a810293798ddc34edeb97ce0afe6213796c8fb677cf` |
| `xlpeek-darwin-arm64` | macOS / arm64 | 13.5 MB | `37a47a52f081034789a15eb8dac015b3a6107b6c795465b119e369eb748d1ab7` |

The 1.2.0 bundle is `xlpeek-1.2.0.zip`, 29.5 MB, holding 48 files: the four
binaries above, `testdata/`, the suites, the docs and the licence. Its own
SHA-256 travels with the delivery rather than in this file — a checksum of a
file printed inside that file can never be right — and every file it contains is
listed in the `SHA256SUMS` beside them.

Install from source instead: `go install github.com/jbeetle/xlpeek@v1.2.0`.
