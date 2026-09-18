A wording fix in one warning, and nothing else.

The hidden-row warning said the rows had been "hidden by hand". That is a cause
it cannot know: excelize's streaming iterator carries the `hidden` attribute and
not `outlineLevel`, so a row collapsed under an outline group, a row a saved
filter removed and a row someone hid are indistinguishable from inside the
file. The message now states what the file holds — how many rows, and where the
first one is — and names the three ways a row gets there without choosing one:

    2 of the 6 rows this answer read are hidden in the sheet (first: row 3);
    a saved filter, a collapsed outline group and a row hidden by hand all leave
    the same mark in the file — Excel shows a view without them while this
    answer includes them, so pass --visible-only to read what the sheet shows

Found while writing the delivery note for this batch, by checking the claim
against the library source rather than against the output. No command behaves
differently and no figure changes.

### Fixed

- The hidden-row warning names the possibilities instead of asserting a cause.
- `docs/AGENTS.md` and `README.md` say the warning reports how many rows are
  hidden and where the first one is, which is what it does.

## Verification

The bundle is assembled by `scripts/package_release.py`, which builds the four
targets with the documented flags, copies the fixtures and suites in, **runs the
full suite against the assembled copies**, and writes `SHA256SUMS` over
everything shipped. `sha256sum -c SHA256SUMS` in the unpacked directory
verifies it; nothing in it is generated after the checksums are taken.

```bash
python scripts/package_release.py --test    # what produced this release
```

Fourteen suites, 64 unit tests, and the Node wrapper's 33 checks. What each
suite covers is listed in `README.md`; the office shapes this batch added are in
`testdata/office.xlsx`, asserted in `examples/regression/regress_office.py`
against what the person looking at the sheet would say.

## Binaries

`CGO_ENABLED=0`, `-trimpath` and `-buildvcs=false`: statically linked, no
runtime or C library needed alongside them, no build-machine paths or git state
inside, and rebuildable to the same bytes from this tag.

| Asset | Platform | Size | SHA-256 |
| --- | --- | --- | --- |
| `xlpeek.exe` | Windows / amd64 | 14.4 MB | `961c1e74ff59c7e67d8ecbb83228599440296887a0db9f75841095bb95b6119a` |
| `xlpeek-linux-amd64` | Linux / amd64 | 14.3 MB | `e19a2af51d9c6038e6c8f2bbbc6e813fd8238c64b531d5b8ef9cdccb263d0758` |
| `xlpeek-linux-arm64` | Linux / arm64 | 13.2 MB | `787a1d5e9bedbc247aba9b920062cf9489f7e96b47080113f0d3fb91ff7cf7b8` |
| `xlpeek-darwin-arm64` | macOS / arm64 | 13.5 MB | `132d91f668511985452a07856aa92de7ce7987858db4bf1d6b7acbf0719212a5` |

The 1.2.2 bundle is `xlpeek-1.2.2.zip`, 29.5 MB, holding 48 files: the four
binaries above, `testdata/`, the suites, the docs and the licence. Its own
SHA-256 travels with the delivery rather than in this file — a checksum of a
file printed inside that file can never be right — and every file it contains is
listed in the `SHA256SUMS` beside them.

Install from source instead: `go install github.com/jbeetle/xlpeek@v1.2.2`.
