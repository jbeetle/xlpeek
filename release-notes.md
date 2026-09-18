A documentation fix, and nothing else. The envelope examples in
`docs/AGENTS.md` still showed `"version":"1.1.0"` after 1.2.0 shipped — the
first thing anyone checking a delivery looks at, and the one thing the SHA256
of a bundle cannot tell you.

No command behaves differently, no output field changed, and every suite that
passed against 1.2.0 passes against this. The binaries differ from 1.2.0's
because the version string is compiled into them, which is also why this is a
release rather than an amended one: what you download and what the tag points
at stay the same thing.

### Fixed

- `docs/AGENTS.md` shows the version this release actually is, in the two places
  it prints an envelope.
- `CHANGELOG.md` gained the `[1.2.1]` entry and the compare links that go with it.

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
suite covers is listed in `README.md`; the office shapes this release's
predecessor added are in `testdata/office.xlsx`, asserted in
`examples/regression/regress_office.py` against what the person looking at the
sheet would say.

## Binaries

`CGO_ENABLED=0`, `-trimpath` and `-buildvcs=false`: statically linked, no
runtime or C library needed alongside them, no build-machine paths or git state
inside, and rebuildable to the same bytes from this tag.

| Asset | Platform | Size | SHA-256 |
| --- | --- | --- | --- |
| `xlpeek.exe` | Windows / amd64 | 14.4 MB | `4aca174e44d59a9d09509df18d1526479bccaba7d97ea146a6d5ece940745737` |
| `xlpeek-linux-amd64` | Linux / amd64 | 14.3 MB | `1bfa8ddb8b50a27d8296c0433275bee21813c30deb6f6462c53d9411beb6a226` |
| `xlpeek-linux-arm64` | Linux / arm64 | 13.2 MB | `6236b7f5217a8b8ddd6d3e8d42e4a813b8bf736102d1f10b5a1f7151ab6c59c6` |
| `xlpeek-darwin-arm64` | macOS / arm64 | 13.5 MB | `0ebba1253e12a0c30d5506170f435a9da97dbf5e29cd56ed78b4a170741cba7c` |

The 1.2.1 bundle is `xlpeek-1.2.1.zip`, 29.5 MB, holding 48 files: the four
binaries above, `testdata/`, the suites, the docs and the licence. Its own
SHA-256 travels with the delivery rather than in this file — a checksum of a
file printed inside that file can never be right — and every file it contains is
listed in the `SHA256SUMS` beside them.

Install from source instead: `go install github.com/jbeetle/xlpeek@v1.2.1`.
