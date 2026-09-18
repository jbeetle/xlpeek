// Copyright (c) 2026 henryyu@163.com. All rights reserved.
//
// This file is part of xlpeek, a read-only spreadsheet reader for
// agents. See README.md for what it does and docs/AGENTS.md for how it is
// meant to be driven.

package main

import (
	"fmt"
	"strings"

	"github.com/xuri/excelize/v2"
)

// Guards against two habits of a real office spreadsheet — hiding rows and
// writing subtotals into the detail column — that make an answer disagree with
// what the person looking at the sheet sees.
//
// Neither is a parsing problem: the rows are there, and every cell in them reads
// cleanly. The number is arithmetically right and answers a different question,
// which is the shape of failure this tool exists to refuse to pass on quietly.

// ---------------------------------------------------------------------------
// Hidden rows
// ---------------------------------------------------------------------------

// rowVisibility counts the rows a sheet hides from its reader.
//
// A saved filter, a collapsed outline group and a row someone hid by hand all
// leave the same mark in the file: hidden="1" on the row. Excel then shows a
// view without those rows, so the total on screen and the total here diverge —
// with nothing in either to say so. Counting them is free: the streaming row
// iterator carries the attribute already, so this costs one comparison per row
// and no memory.
type rowVisibility struct {
	hidden      int
	firstHidden int
	// set is only built when a caller asks to skip hidden rows, which needs to
	// know *which* rows rather than how many. A sheet where a filter hid most of
	// the rows would otherwise hold a set as large as its row count for nothing.
	set map[int]bool
}

func newRowVisibility(keepSet bool) *rowVisibility {
	visibility := &rowVisibility{firstHidden: -1}
	if keepSet {
		visibility.set = map[int]bool{}
	}
	return visibility
}

// note records one row's visibility, as reported by the row iterator.
func (v *rowVisibility) note(row int, opts excelize.RowOpts) {
	if v == nil || !opts.Hidden {
		return
	}
	v.hidden++
	if v.firstHidden < 0 {
		v.firstHidden = row
	}
	if v.set != nil {
		v.set[row] = true
	}
}

// hiddenRow reports whether a row is hidden in the sheet.
func (v *rowVisibility) hiddenRow(row int) bool {
	return v != nil && v.set != nil && v.set[row]
}

// skippedNote states what --visible-only left out, since a total that is smaller
// than the file's own total invites the question this answers.
func (v *rowVisibility) skippedNote() string {
	if v == nil || v.hidden == 0 {
		return ""
	}
	return fmt.Sprintf(
		"--visible-only skipped %d hidden row(s); the figures below are what the sheet shows, "+
			"not what it holds", v.hidden)
}

// warning explains the gap between the sheet and the screen.
//
// It names the possibilities rather than picking one, because the file does not
// distinguish them: a saved filter, a collapsed outline group and a row hidden
// by hand all leave the same attribute, and the streaming iterator carries that
// attribute and nothing else. Saying "someone hid them by hand" would be a
// guess presented as a fact.
func (v *rowVisibility) warning(rowsScanned int) string {
	if v == nil || v.hidden == 0 {
		return ""
	}
	const why = "a saved filter, a collapsed outline group and a row hidden by hand all leave the " +
		"same mark in the file"
	if rowsScanned > 0 && v.hidden >= rowsScanned {
		// Every row it saw is hidden, which is what a filtered view that kept
		// nothing looks like — worth saying without the arithmetic.
		return fmt.Sprintf(
			"every one of the %s this answer read is hidden in the sheet, so Excel shows a view "+
				"without them while this answer includes them; pass --visible-only to read what "+
				"the sheet shows", rowsPhrase(rowsScanned))
	}
	return fmt.Sprintf(
		"%d of the %s this answer read are hidden in the sheet (first: row %d); %s — Excel shows "+
			"a view without them while this answer includes them, so pass --visible-only to read "+
			"what the sheet shows",
		v.hidden, rowsPhrase(rowsScanned), v.firstHidden, why)
}

// visibilityNote picks the sentence a scan owes about hidden rows: what it left
// out when --visible-only was asked for, or what it counted anyway.
func visibilityNote(v *rowVisibility, skipped bool, rowsScanned int) string {
	if skipped {
		return v.skippedNote()
	}
	return v.warning(rowsScanned)
}

// totalsNote picks the sentence about summary rows, the same way.
func totalsNote(s *subtotalCounters, excluded bool, rows int) string {
	if excluded {
		return s.exclusionNote()
	}
	return s.warning(rows)
}

// ---------------------------------------------------------------------------
// Two-level headers
// ---------------------------------------------------------------------------

// headerSpan resolves --header-rows against the header row the command ended up
// using. A span without a header row is a usage error rather than a silent 1:
// the caller asked for something the command cannot do, and the fix is to tell
// it where the header is.
func headerSpan(headerAt, rows int) error {
	if rows < 1 {
		return fmt.Errorf("--header-rows must be at least 1, got %d", rows)
	}
	if rows > 1 && headerAt == 0 {
		return fmt.Errorf("--header-rows %d needs --header or --header-row N: the span starts at "+
			"the header, and this command was not told where that is", rows)
	}
	return nil
}

// mergeHeaderRows joins a header written on more than one row into one name per
// column.
//
// A report that groups its columns writes a title row over them — 2024年 over
// 金额 and 数量 — and the name a caller needs is the two together. Excel says so
// by merging the title cell, which leaves the value only in the first column of
// the region; --fill-merged spreads it first, and this joins what is there.
//
// The parts are joined with nothing between them, because a separator would put
// an operator into the name: "--sum 2024年-金额" is arithmetic to the expression
// parser, and the column could only be named in brackets. A part identical to
// the one before it is dropped, which is what a cell merged vertically looks
// like when it appears on both header rows.
func mergeHeaderRows(rows [][]string) []string {
	width := 0
	for _, row := range rows {
		if len(row) > width {
			width = len(row)
		}
	}
	merged := make([]string, width)
	for i := 0; i < width; i++ {
		parts := make([]string, 0, len(rows))
		for _, row := range rows {
			part := strings.TrimSpace(valueAt(row, i))
			if part == "" || (len(parts) > 0 && parts[len(parts)-1] == part) {
				continue
			}
			parts = append(parts, part)
		}
		merged[i] = strings.Join(parts, "")
	}
	return merged
}

// ---------------------------------------------------------------------------
// Subtotal rows
// ---------------------------------------------------------------------------

// subtotalWords are the labels a spreadsheet's own summary rows carry. The list
// is deliberately short: it holds the words a row label uses when it is a total
// of other rows, and nothing else.
var subtotalWords = []string{
	"小计", "合计", "总计", "累计", "总合计", "其中",
	"subtotal", "grandtotal", "total", "sum",
}

// subtotalLabel reports the summary-row wording a row carries, or "".
//
// The check reads the row's first cell that holds anything, which is where a
// report writes the label — 科目, 项目, 客户 — rather than every cell, because a
// remark column reading "合计" is a remark and not a total. A label may be
// followed by a colon and a scope ("小计：华东"), which is the same row; it may
// not be a prefix of a longer word ("合计额" is a column heading), or every
// column named that way would look like a total.
func subtotalLabel(cells []string) string {
	for _, cell := range cells {
		text := strings.TrimSpace(cell)
		if text == "" {
			continue
		}
		return matchSubtotal(text)
	}
	return ""
}

// matchSubtotal reports the word that makes text look like a summary row.
func matchSubtotal(text string) string {
	squashed := strings.ToLower(strings.Join(strings.Fields(text), ""))
	for _, word := range subtotalWords {
		if squashed == word {
			return word
		}
		for _, colon := range []string{":", "："} {
			if strings.HasPrefix(squashed, word+colon) {
				return word
			}
		}
	}
	return ""
}

// subtotalCounters accumulate what a scan saw, so the command can say it once at
// the end rather than per row.
type subtotalCounters struct {
	rows      int
	excluded  int
	firstRow  int
	firstWord string
}

func (s *subtotalCounters) note(row int, word string, excluded bool) {
	if word == "" {
		return
	}
	if excluded {
		s.excluded++
	} else {
		s.rows++
	}
	if s.firstRow == 0 {
		s.firstRow, s.firstWord = row, word
	}
}

// warning explains summary rows that were counted as detail.
//
// This is the double-count that a report invites: the detail rows and the row
// that totals them sit in the same column, so including both adds the same
// money twice — and the answer is a number nobody questions.
func (s *subtotalCounters) warning(rowsMatched int) string {
	if s == nil || s.rows == 0 {
		return ""
	}
	return fmt.Sprintf(
		"%s of the %s this answer aggregated look like subtotal or total rows (first: row %d, "+
			"%q); a report that holds both detail and its own totals is counted twice here — "+
			"pass --exclude-totals to drop them, or filter them with --where",
		rowsPhrase(s.rows), rowsPhrase(rowsMatched), s.firstRow, s.firstWord)
}

// exclusionNote states how many rows --exclude-totals removed, so that the
// figure which follows can be explained without re-running the command.
func (s *subtotalCounters) exclusionNote() string {
	if s == nil || s.excluded == 0 {
		return ""
	}
	return fmt.Sprintf(
		"--exclude-totals dropped %s that look like subtotal or total rows (first: row %d, %q)",
		rowsPhrase(s.excluded), s.firstRow, s.firstWord)
}
