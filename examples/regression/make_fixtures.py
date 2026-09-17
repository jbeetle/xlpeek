# Copyright (c) 2026 henryyu@163.com. All rights reserved.
#
# This file is part of xlpeek, a read-only spreadsheet reader for agents.
# See README.md for what it does and docs/AGENTS.md for how it is meant to be
# driven.

"""Rebuild the fixture suite reads.

`testdata/round2.xlsx` is committed, so nothing here has to run for a suite to
pass. It exists because the committed file has to be reproducible: a fixture
whose provenance is "someone's machine" cannot be re-derived when the shape it
encodes is questioned, and the shapes below are the ones an external review
found the tool answering wrongly. Regenerating it and re-running the suites
proves the file still means what the expectations say it means.

Needs openpyxl — the only step of the regression suite that does. The suites
themselves are stdlib-only.

    python examples/regression/make_fixtures.py
"""
import sys
from pathlib import Path

try:
    from openpyxl import Workbook
    from openpyxl.styles import Font
    from openpyxl.utils import get_column_letter
except ImportError:  # pragma: no cover - a readable failure beats a traceback
    sys.exit("make_fixtures.py needs openpyxl: pip install openpyxl")

REPO = Path(__file__).resolve().parents[2]
TARGET = REPO / "testdata" / "round2.xlsx"


def sheet_filters(wb):
    """筛选: three numeric rows, one of them a percentage column."""
    ws = wb.create_sheet("筛选")
    ws.append(["名称", "金额", "比率", "状态"])
    for name, amount, ratio, state in (
        ("A", 100, 0.05, "完成"),
        ("B", 1000, 0.2, "进行"),
        ("C", 2000, 0.2567, "完成"),
    ):
        ws.append([name, amount, ratio, state])
        ws.cell(row=ws.max_row, column=3).number_format = "0.00%"


def sheet_dates(wb):
    """日期: the same day written three ways, plus a number and a text date.

    A date is a number wearing a number format, so the same value reads as
    "2026-01-01 0:00:00", "01-01-26" or "46023" depending on the file. The
    text column is here to hold --dates to its promise: it rewrites dates, and
    a string that merely looks like one is not a date.
    """
    import datetime

    ws = wb.create_sheet("日期")
    ws.append(["日期", "短日期", "时间", "金额", "文本日期"])
    rows = (
        (datetime.datetime(2026, 1, 1), datetime.datetime(2026, 1, 1),
         datetime.datetime(2026, 1, 1, 9, 30), 1234.5, "2026-01-01"),
        (datetime.datetime(2026, 1, 15), datetime.datetime(2026, 1, 15),
         datetime.datetime(2026, 1, 15, 18, 5, 2), 999.99, "2026-01-15"),
    )
    for date, short, stamp, amount, text in rows:
        ws.append([date, short, stamp, amount, text])
        row = ws.max_row
        ws.cell(row=row, column=2).number_format = "mm-dd-yy"
        ws.cell(row=row, column=3).number_format = "yyyy-mm-dd hh:mm"
        ws.cell(row=row, column=4).number_format = "¥#,##0.00"


def sheet_trailing(wb):
    """尾部空行: two data rows, then formatted rows holding nothing.

    A spreadsheet's last row is its last formatted row, not its last filled
    one, so a border or a fill dragged down past the data leaves the file
    claiming two hundred rows. Paging used to treat those as pending data and
    hand back blank pages with complete:false, which is the failure this sheet
    is here to catch.
    """
    ws = wb.create_sheet("尾部空行")
    ws.append(["名称", "金额"])
    ws.append(["A", 100])
    ws.append(["B", 200])
    for row in range(5, 201):
        ws.cell(row=row, column=1).font = Font()


def sheet_bracketed(wb):
    """括号列名: a parenthesis in the column name, as real reports write it."""
    ws = wb.create_sheet("括号列名")
    ws.append(["金额(万元)", "成本(万元)"])
    ws.append([500, 300])
    ws.append([400, 200])


def sheet_high_cardinality(wb):
    """高基数: past the point where grouping stops summarising.

    Grouping by a near-unique column returns the table with extra steps. The
    count is over 1000 on purpose: that is where the default group cap bites.
    """
    ws = wb.create_sheet("高基数")
    ws.append(["凭证号", "金额"])
    for i in range(1200):
        ws.append(["V%04d" % i, i])


def build():
    wb = Workbook()
    wb.remove(wb.active)  # the default sheet has no part in the fixture
    for sheet in (sheet_filters, sheet_dates, sheet_trailing,
                  sheet_bracketed, sheet_high_cardinality):
        sheet(wb)
    TARGET.parent.mkdir(parents=True, exist_ok=True)
    wb.save(str(TARGET))
    print("wrote %s (%d bytes)" % (TARGET.relative_to(REPO), TARGET.stat().st_size))


if __name__ == "__main__":
    build()
