// Copyright (c) 2026 henryyu@163.com. All rights reserved.
//
// This file is part of xlpeek, a read-only spreadsheet reader for
// agents. See README.md for what it does and docs/AGENTS.md for how it is
// meant to be driven.

package main

import (
	"strconv"
	"strings"

	"github.com/xuri/excelize/v2"
)

// How a date cell is written out. A date is stored as a number and shown
// through a number format, so the same value reads as "2026-01-01 0:00:00" in
// one workbook and "01-01-26" in another: the shape belongs to the file, not to
// the value, and two shapes of one date are not comparable as text. --dates
// pins the shape so that a caller can compare, sort and parse what it gets.
const (
	datesDisplay = "display"
	datesISO     = "iso"
	datesSerial  = "serial"
)

const dateModeHelp = "how date cells are written: display (as the sheet shows them, " +
	"the default), iso (2026-01-01, or 2026-01-01T09:30:00 when a time of day is " +
	"stored), or serial (the stored number); reads the worksheet's number formats"

func validDateMode(mode string) bool {
	switch mode {
	case datesDisplay, datesISO, datesSerial:
		return true
	}
	return false
}

// dateResolver rewrites date cells into the requested shape.
//
// Knowing that a cell holds a date means knowing its number format, and the
// streaming row iterator does not carry one: Columns() returns the string the
// format produced, with nothing left to say which format it was — "01-01-26"
// is not decidable into a date without guessing, and guessing between
// month-day-year and day-month-year is exactly the ambiguity this option exists
// to remove. The format lives on the cell's style, so it is read from there.
//
// That read parses the worksheet, giving up the streaming profile the way
// --calc and --fill-merged do, which is why it is opt-in and why the default is
// the display form the sheet itself uses. Within one workbook the style table
// is small and shared, so the format is resolved once per style index and every
// further cell is a map lookup.
type dateResolver struct {
	f        *excelize.File
	mode     string
	date1904 bool
	styles   map[int]bool
}

func newDateResolver(f *excelize.File, mode string) *dateResolver {
	// A 1904-based workbook counts days from 1904-01-01 instead of 1899-12-30.
	// It is rare, and it is a property of the file, so it is read rather than
	// assumed: getting it wrong would move every date by four years.
	date1904 := false
	if props, err := f.GetWorkbookProps(); err == nil && props.Date1904 != nil {
		date1904 = *props.Date1904
	}
	return &dateResolver{f: f, mode: mode, date1904: date1904, styles: map[int]bool{}}
}

// apply rewrites the date cells of one row. displayed is what the sheet shows
// and stored is what it holds; a cell that is not a date-formatted number comes
// back exactly as displayed, so a currency column, a text column and a column
// of dates that are really text are all left alone.
func (d *dateResolver) apply(sheet string, row int, displayed, stored []string) []string {
	var out []string
	for i, value := range displayed {
		if value == "" {
			continue
		}
		ref, err := excelize.CoordinatesToCellName(i+1, row)
		if err != nil || !d.isDate(sheet, ref) {
			continue
		}
		serial, ok := parseNumber(valueAt(stored, i))
		if !ok {
			continue
		}
		replacement, ok := d.render(serial)
		if !ok {
			continue
		}
		if out == nil {
			out = append([]string(nil), displayed...)
		}
		out[i] = replacement
	}
	if out == nil {
		return displayed
	}
	return out
}

// render writes one stored value in the requested form.
func (d *dateResolver) render(serial float64) (string, bool) {
	if d.mode == datesSerial {
		return strconv.FormatFloat(serial, 'f', -1, 64), true
	}
	stamp, err := excelize.ExcelDateToTime(serial, d.date1904)
	if err != nil {
		return "", false
	}
	// A whole day is written as a date; anything carrying a time of day keeps
	// it. No zone is attached, because a spreadsheet time is a wall-clock value
	// that carries no offset — appending Z or +08:00 would assert one the file
	// never stated.
	if stamp.Hour() == 0 && stamp.Minute() == 0 && stamp.Second() == 0 && stamp.Nanosecond() == 0 {
		return stamp.Format("2006-01-02"), true
	}
	return stamp.Format("2006-01-02T15:04:05"), true
}

// isDate reports whether a cell's number format presents it as a date or a
// time.
func (d *dateResolver) isDate(sheet, ref string) bool {
	style, err := d.f.GetCellStyle(sheet, ref)
	if err != nil {
		return false
	}
	if known, ok := d.styles[style]; ok {
		return known
	}
	isDate := false
	if format, err := d.f.GetStyle(style); err == nil {
		isDate = isDateFormat(format)
	}
	d.styles[style] = isDate
	return isDate
}

// isDateFormat reports whether a style presents its number as a date or a time.
func isDateFormat(style *excelize.Style) bool {
	if style.CustomNumFmt != nil {
		return hasDateField(*style.CustomNumFmt)
	}
	// The built-in formats are fixed by the file format: 14-22 are the date and
	// time formats, 45-47 the elapsed-time ones. Everything else built in is a
	// number, a currency, an accounting format, a fraction or text.
	return (style.NumFmt >= 14 && style.NumFmt <= 22) || (style.NumFmt >= 45 && style.NumFmt <= 47)
}

// hasDateField reports whether a number-format code carries a date or time
// field.
//
// The code is scanned rather than searched, because the letters that mean a
// field also occur in the words a format can print: "#,##0.00 \"per day\""
// shows the word "day" without being a date, and "\"days\"" quotes it as a
// literal. Quoted runs, bracketed conditions and colour names ([Red], [$-409])
// and backslash escapes are therefore stepped over, and only what remains can
// name a field.
func hasDateField(code string) bool {
	for i := 0; i < len(code); i++ {
		switch code[i] {
		case '"':
			end := strings.IndexByte(code[i+1:], '"')
			if end < 0 {
				return false
			}
			i += end + 1
		case '[':
			// Only an elapsed-time section is a field: [h]:mm:ss counts hours
			// that a 24-hour clock would wrap. A colour ([Red]), a condition
			// ([>100]) and a locale ([$-409]) all live in the same syntax and
			// none of them is a date — "Red" holds a d, and reading it as one
			// would label every red-number format a date.
			end := strings.IndexByte(code[i+1:], ']')
			if end < 0 {
				return false
			}
			if isElapsedField(code[i+1 : i+1+end]) {
				return true
			}
			i += end + 1
		case '\\', '_', '*':
			// An escape, a padding skip and a repeat each consume the character
			// after them as a literal, which may well be a letter.
			i++
		case 'y', 'Y', 'm', 'M', 'd', 'D', 'h', 'H', 's', 'S':
			return true
		}
	}
	return false
}

// isElapsedField reports whether a bracketed section is an elapsed-time code
// such as [h], [mm] or [ss].
func isElapsedField(section string) bool {
	for i := 0; i < len(section); i++ {
		switch section[i] {
		case 'h', 'H', 'm', 'M', 's', 'S':
		default:
			return false
		}
	}
	return section != ""
}
