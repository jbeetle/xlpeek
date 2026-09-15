# Changelog

All notable changes to this project are documented here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and the project
adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

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

[Unreleased]: https://github.com/henryyu/xlpeek/compare/v1.0.1...HEAD
[1.0.1]: https://github.com/henryyu/xlpeek/releases/tag/v1.0.1
