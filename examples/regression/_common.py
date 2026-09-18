# Copyright (c) 2026 henryyu@163.com. All rights reserved.
#
# This file is part of xlpeek, a read-only spreadsheet reader for agents.
# See README.md for what it does and docs/AGENTS.md for how it is meant to be
# driven.

"""Shared setup for the regression suites.

Importing this module has two side effects, both deliberate:

  * the working directory moves to the repository root, so a suite can name
    ``bin/xlpeek.exe`` and ``testdata/Book1.xlsx`` without caring where it was
    invoked from;
  * stdout and stderr are switched to UTF-8, because every suite prints Chinese
    labels and fixtures, and a Windows console defaults to a code page that
    cannot encode them.

A side effect on import is normally worth avoiding. Here it is the point: the
alternative is ten scripts each carrying the same path arithmetic.
"""

import json
import os
import platform
import subprocess
import sys
from pathlib import Path

REPO = Path(__file__).resolve().parents[2]
FIXTURES = REPO / "testdata"

# Every workbook the suites read. Listed here rather than per suite so that an
# incomplete delivery is reported once, before any check runs.
REQUIRED_FIXTURES = [
    FIXTURES / "华鼎科技_分部收入_FY23-FY25.xlsx",
    FIXTURES / "hs.xlsx",
    FIXTURES / "shapes.xlsx",
    FIXTURES / "round2.xlsx",
    FIXTURES / "office.xlsx",
    REPO / "examples" / "regression" / "fixtures" / "header_row.xlsx",
]


def _binary() -> str:
    """The xlpeek build for this platform, or XLPEEK if that is set."""
    override = os.environ.get("XLPEEK")
    if override:
        return override
    if platform.system() == "Windows":
        names = ["xlpeek.exe"]
    else:
        arch = {"x86_64": "amd64", "aarch64": "arm64"}.get(
            platform.machine(), platform.machine())
        names = [f"xlpeek-linux-{arch}", "xlpeek"]
    # `go build` drops the binary in the working directory; the documented
    # build puts it in bin/. Accept either.
    for directory in ("bin", "."):
        for name in names:
            candidate = REPO / directory / name
            if candidate.exists():
                return str(candidate)
    return "xlpeek"  # fall back to PATH


EXE = _binary()

# A workbook with four sheets, gaps in the data, and mixed units and currencies
# — the fixture most suites work against. Synthetic, and documented as such in
# its own 口径与汇率说明 sheet.
WORKBOOK = str(FIXTURES / "华鼎科技_分部收入_FY23-FY25.xlsx")
STOCKS = str(FIXTURES / "hs.xlsx")


def _force_utf8(stream) -> None:
    if hasattr(stream, "reconfigure"):
        try:
            stream.reconfigure(encoding="utf-8", errors="replace")
        except (ValueError, OSError):
            pass  # a redirected or closed stream; leave it alone


_force_utf8(sys.stdout)
_force_utf8(sys.stderr)

os.chdir(REPO)


def _give_up(problem, remedy):
    """Report a broken delivery and stop, instead of failing every check.

    A suite that cannot find its fixtures used to report FILE_NOT_FOUND for
    every one of its checks, and one of them crashed on a missing field before
    it printed anything. Both read as "the tool is broken" when what is
    actually broken is the delivery — so the delivery is checked first, once,
    and a failure names what is missing rather than what it caused.
    """
    print("=" * 70)
    print("SETUP FAILED: %s" % problem)
    print(remedy)
    print("=" * 70)
    sys.exit(1)


def _check_binary():
    try:
        proc = subprocess.run([EXE, "version"], capture_output=True, timeout=120)
    except OSError as exc:
        _give_up("cannot run the xlpeek binary at %s (%s)" % (EXE, exc),
                 "Build it first (go build -o bin/xlpeek.exe .), or point XLPEEK "
                 "at a build that exists.")
    out = proc.stdout.decode("utf-8", "replace").strip()
    try:
        envelope = json.loads(out.splitlines()[0])
    except (IndexError, ValueError):
        _give_up("%s version wrote no JSON envelope" % EXE,
                 "Output was: %s" % out[:200])
    if not envelope.get("ok"):
        _give_up("%s version reported %s" % (EXE, envelope.get("error")),
                 "The file is not a working xlpeek build.")


def _check_fixtures():
    missing = [path for path in REQUIRED_FIXTURES if not path.exists()]
    if not missing:
        return
    # POSIX separators whatever the platform: the message is read by whoever
    # assembled the delivery, and a backslash looks like an escape in the
    # ticket it gets pasted into.
    _give_up("%d of %d fixture workbooks are missing" % (len(missing), len(REQUIRED_FIXTURES)),
             "Missing:\n  " + "\n  ".join(path.relative_to(REPO).as_posix() for path in missing) +
             "\nThe suites read committed fixtures; a delivery without them cannot "
             "be verified.\nRegenerate the generated one with "
             "python examples/regression/make_fixtures.py,\nand re-fetch the rest "
             "from the repository.")


_check_binary()
_check_fixtures()


def finish(failures, label="RESULT"):
    """Print the verdict, then exit with a status a runner can act on.

    Every suite ends here. Four of them originally reported a failure in a line
    of text while still exiting 0, which is the same thing as passing to
    anything that only looks at exit codes.
    """
    failures = list(failures)
    print()
    print("=" * 70)
    if failures:
        print("%s: %d FAILURES" % (label, len(failures)))
        for item in failures[:15]:
            print("  - %s" % str(item)[:160])
        if len(failures) > 15:
            print("  ... and %d more" % (len(failures) - 15))
        sys.exit(1)
    print("%s: ALL PASS" % label)
    sys.exit(0)
