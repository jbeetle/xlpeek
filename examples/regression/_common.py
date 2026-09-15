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

import os
import platform
import sys
from pathlib import Path

REPO = Path(__file__).resolve().parents[2]
FIXTURES = REPO / "testdata"


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
