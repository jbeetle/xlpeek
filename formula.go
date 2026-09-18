// Copyright (c) 2026 henryyu@163.com. All rights reserved.
//
// This file is part of xlpeek, a read-only spreadsheet reader for
// agents. See README.md for what it does and docs/AGENTS.md for how it is
// meant to be driven.

package main

import (
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/xuri/excelize/v2"
)

// Formula support, shared by --col (a formula per row) and --agg (a formula per
// group).
//
// A caller writes a formula the way a spreadsheet user would — MONTH(过账日期),
// VLOOKUP(客户编号,目标表!$A:$B,2,FALSE) — and the column names in it are
// rewritten into cell references before excelize's engine evaluates it. That
// rewriting is what makes the feature usable: nobody wants to write $J2601, and
// a caller who does not know where a group's rows sit *cannot* write it.
//
// Two things are deliberately not left to the library:
//
//   - Volatile functions are rejected at the door. NOW, TODAY, RAND and
//     RANDBETWEEN are evaluated silently by the engine, so the same command run
//     twice would report different numbers. For a tool whose output is used to
//     reconcile figures, that is worse than not supporting them.
//   - Errors are kept in two classes. A formula that cannot work at all — an
//     unsupported function, a syntax error, a reference to a sheet that does
//     not exist — is a usage error, because it fails identically on every row
//     and scanning the whole sheet to say so wastes the caller's time. An error
//     that depends on this row's data (#DIV/0!, a VLOOKUP that finds nothing)
//     is data: it becomes the value and is counted in warnings, so a caller can
//     see how many rows did not compute rather than wondering why a total looks
//     small.

// volatileFunctions are the functions whose value changes between two runs of
// the same command. The engine computes all four without complaint.
var volatileFunctions = map[string]string{
	"NOW":         "the current date and time",
	"TODAY":       "today's date",
	"RAND":        "a random number",
	"RANDBETWEEN": "a random number",
}

// riskyRangeFunctions name the engine functions that silently mishandle a
// range argument. They are not refused — the caller may well be using them
// correctly — but a formula that hands one a column of the sheet is warned
// about, because the number it produces looks like an answer.
//
// Verified against excelize 2.11.0: NPV iterates its arguments and calls
// ToNumber on each, so a range collapses to its first cell. IRR, XIRR, XNPV,
// PMT, PV and FV were checked at the same time and read their arguments
// correctly.
var riskyRangeFunctions = map[string]string{
	"NPV": "the engine discounts only the first cell of a range",
}

// scratchPrefix names the worksheets this tool adds to a workbook in order to
// evaluate a formula. They are deleted before the command returns, and hidden
// from every message that lists the workbook's sheets — a caller told to pick
// from a sheet list must not be offered a worksheet that will not be there on
// the next call.
const scratchPrefix = "__xlpeek_scratch"

// userSheets lists a workbook's own sheets, without the scratch sheets this
// process adds for formula evaluation.
func userSheets(f *excelize.File) []string {
	all := f.GetSheetList()
	out := make([]string, 0, len(all))
	for _, name := range all {
		if strings.HasPrefix(name, scratchPrefix) {
			continue
		}
		out = append(out, name)
	}
	return out
}

// ---------------------------------------------------------------------------
// Reading a formula: which of its names are columns of the sheet being read?
// ---------------------------------------------------------------------------

// formulaPart is one piece of a formula: literal text, or a reference to a
// column the caller named.
type formulaPart struct {
	text string
	// ref is the column's index, or a negative value for a derived column
	// (--col output, which has no cell of its own). Only meaningful when isRef.
	ref   int
	isRef bool
}

// formulaParts is a scanned formula, ready to be rendered once per row or once
// per group.
type formulaParts []formulaPart

// riskCall is the span of one call to a function that mishandles a range,
// measured on the formula as written.
type riskCall struct {
	name       string
	start, end int
}

// columns lists the columns the formula reads, in the order they first appear
// and without repeats, so that each can be resolved once.
func (parts formulaParts) columns() []int {
	var (
		out  []int
		seen = map[int]bool{}
	)
	for _, part := range parts {
		if !part.isRef || seen[part.ref] {
			continue
		}
		seen[part.ref] = true
		out = append(out, part.ref)
	}
	return out
}

// render rebuilds the formula, asking ref for the text that replaces each
// column reference.
func (parts formulaParts) render(ref func(int) (string, error)) (string, error) {
	var b strings.Builder
	for _, part := range parts {
		if !part.isRef {
			b.WriteString(part.text)
			continue
		}
		text, err := ref(part.ref)
		if err != nil {
			return "", err
		}
		b.WriteString(text)
	}
	return b.String(), nil
}

// formulaContext is what the scanner needs to know about the workbook: which
// names are columns, which are sheets, and which the engine resolves itself.
type formulaContext struct {
	header  []string
	derived []string
	sheets  []string
	defined map[string]bool
	// flag names the flag the formula came from, for error messages.
	flag string
	// broadRefs collects the whole-column references the formula being scanned
	// makes, so that the command can warn about what they cost. The caller
	// empties it before each scan.
	broadRefs []string
	// misused names the risky functions the formula called over a column of this
	// sheet. Only a formula evaluated per group collects them: there a column
	// becomes a range, which is the argument the engine mishandles.
	misused []string
}

// column resolves a name written in a formula to a column index. Derived
// columns are looked up first and are negative, so that a name can never
// resolve to a cell of the sheet by accident.
func (ctx *formulaContext) column(name string) (int, bool) {
	if idx, ok := ctx.nameIndex(name, true); ok {
		return idx, true
	}
	// A bare letter or index names a column of the sheet, the way it does in
	// --where and --group-by. A full cell reference (A1, A1:B5) is not a column
	// reference and is left to the engine by the caller.
	idx, err := resolveColumn(name, ctx.header)
	if err != nil {
		return 0, false
	}
	return idx, true
}

// bracketed resolves a name written [like this]. Only real column names are
// accepted, because [] is also Excel's structured-reference syntax for tables:
// a bracketed run that names no column belongs to the engine.
func (ctx *formulaContext) bracketed(name string) (int, bool) {
	return ctx.nameIndex(name, false)
}

// nameIndex resolves a name against the derived columns and the header, trying
// an exact match before a case-insensitive one — the same order in which every
// other column reference in this tool is resolved.
func (ctx *formulaContext) nameIndex(name string, byLetter bool) (int, bool) {
	name = strings.TrimSpace(name)
	if name == "" {
		return 0, false
	}
	for _, exact := range []bool{true, false} {
		for i, derived := range ctx.derived {
			if matchesName(derived, name, exact) {
				// Derived columns are numbered from -2, so that the -1 that means
				// "not resolved anywhere else in this tool" stays unambiguous.
				return -i - 2, true
			}
		}
		for i, cell := range ctx.header {
			if matchesName(cell, name, exact) {
				return i, true
			}
		}
		if !byLetter {
			continue
		}
		if idx, err := resolveColumn(name, nil); err == nil {
			return idx, true
		}
	}
	return 0, false
}

// matchesName compares a header or derived-column name with a reference written
// in a formula. Header cells are compared trimmed, because that is how they are
// reported everywhere else: a sheet whose header reads "    名称" is listed by
// info as "名称", and a caller who then writes 名称 must be understood.
func matchesName(candidate, name string, exact bool) bool {
	candidate = strings.TrimSpace(candidate)
	if candidate == "" {
		return false
	}
	if exact {
		return candidate == name
	}
	return strings.EqualFold(candidate, name)
}

// scanFormula splits a formula into literal text and column references.
//
// The rewrite is textual but not blind: string literals are stepped over, a
// name followed by "(" is a function and never a column, and a name followed by
// "!" is a sheet. A name that resolves to nothing at all is an error rather
// than being handed to the engine, because the engine's answer to a typo is
// #NAME? on every single row — thousands of identical failures where one
// message would have done.
func scanFormula(formula string, ctx *formulaContext) (formulaParts, error) {
	reject := func(format string, args ...any) (formulaParts, error) {
		return nil, fmt.Errorf("--%s %q: %s", ctx.flag, formula,
			fmt.Sprintf(format, args...))
	}
	if !balancedParens(formula) {
		// The engine says "formula not valid" for an unbalanced run, which does
		// not say where. This one does, and it is caught before the scan.
		return reject("unbalanced parentheses")
	}

	var (
		parts   formulaParts
		literal strings.Builder
		// refAt records where each column reference was written, and riskyCalls
		// the span of every call to a function that mishandles a range. Together
		// they answer "did this formula hand a column to NPV", which a per-group
		// formula must not do quietly.
		refAt     []int
		riskCalls []riskCall
	)
	flush := func() {
		if literal.Len() > 0 {
			parts = append(parts, formulaPart{text: literal.String()})
			literal.Reset()
		}
	}
	addColumn := func(idx int, at int) {
		flush()
		refAt = append(refAt, at)
		parts = append(parts, formulaPart{ref: idx, isRef: true})
	}
	unknownName := func(name string) error {
		// The bracket hint is offered only when it is what the caller needs: for
		// a word like 金额(万元) the brackets are the fix, and for a plain typo
		// they are advice that does not work.
		return fmt.Errorf("names %q, which is not a column of this sheet, a cell reference, "+
			"a function, a sheet or a defined name%s", name, bracketHint(name))
	}

	for i := 0; i < len(formula); {
		c := formula[i]
		switch {
		case c == '"':
			// A string literal, where a column name is text and not a name.
			// Doubled quotes are an escaped quote, not the end of the literal.
			end := i + 1
			for end < len(formula) {
				if formula[end] != '"' {
					end++
					continue
				}
				if end+1 < len(formula) && formula[end+1] == '"' {
					end += 2
					continue
				}
				end++
				break
			}
			literal.WriteString(formula[i:end])
			i = end
		case c == '[':
			// A bracketed column name, for a name the scanner would otherwise
			// read as arithmetic — or as a structured table reference, which is
			// left to the engine.
			end := strings.IndexByte(formula[i+1:], ']')
			if end < 0 {
				return reject("missing closing bracket")
			}
			name := strings.TrimSpace(formula[i+1 : i+1+end])
			if idx, ok := ctx.bracketed(name); ok {
				addColumn(idx, i)
			} else {
				literal.WriteString(formula[i : i+1+end+1])
			}
			i += end + 2
		case c == '\'':
			// A quoted sheet name: 'FY24 明细'!A1.
			end, closed := i+1, false
			for end < len(formula) {
				if formula[end] != '\'' {
					end++
					continue
				}
				if end+1 < len(formula) && formula[end+1] == '\'' {
					end += 2
					continue
				}
				end++
				closed = true
				break
			}
			if !closed {
				return reject("missing closing quote in a sheet name")
			}
			name := strings.ReplaceAll(formula[i+1:end-1], "''", "'")
			if !isSheetName(ctx, name) {
				return reject("references sheet %q, which does not exist; available sheets: %s",
					name, strings.Join(ctx.sheets, ", "))
			}
			next := skipSpaces(formula, end)
			if next >= len(formula) || formula[next] != '!' {
				return reject("a quoted sheet name must be followed by ! and a reference")
			}
			// Everything up to and including the "!" is copied; what follows is
			// scanned by the loop like any other reference, so that a range, a
			// single cell and a whole column all reach the engine exactly as they
			// were written.
			literal.WriteString(formula[i : next+1])
			i = next + 1
		case c == '#':
			// An error literal such as #REF! or #N/A, which is not a sheet name
			// even though it ends in "!".
			end := i + 1
			for end < len(formula) && isFormulaWordByte(formula[end]) {
				end++
			}
			literal.WriteString(formula[i:end])
			i = end
		case isFormulaWordByte(c):
			end := i
			for end < len(formula) && isFormulaWordByte(formula[end]) {
				end++
			}
			word := formula[i:end]
			next := skipSpaces(formula, end)
			switch {
			case next < len(formula) && formula[next] == '!':
				// A sheet-qualified reference: the word is a sheet, and what
				// follows the "!" belongs to the engine.
				if !isSheetName(ctx, word) {
					return reject("references sheet %q, which does not exist; available sheets: %s",
						word, strings.Join(ctx.sheets, ", "))
				}
				literal.WriteString(formula[i : next+1])
				i = next + 1
			case next < len(formula) && formula[next] == '(':
				// A column can be named like a call — 金额(万元) is an ordinary
				// header in a Chinese report — so the whole run is tried as a
				// name before the word is taken as a function.
				if end := closingParen(formula, next); end > 0 {
					if idx, ok := ctx.bracketed(formula[i:end]); ok {
						addColumn(idx, i)
						i = end
						continue
					}
				}
				if _, risky := riskyRangeFunctions[strings.ToUpper(word)]; risky {
					if end := closingParen(formula, next); end > 0 {
						riskCalls = append(riskCalls, riskCall{
							name: strings.ToUpper(word), start: next, end: end,
						})
					}
				}
				if what, ok := volatileFunctions[strings.ToUpper(word)]; ok {
					return reject(
						"%s() returns %s, so the same command would report a different answer "+
							"every time it runs; a figure that has to be reconcilable cannot come "+
							"from a formula — filter on a date column, or pass the constant in the "+
							"command", strings.ToUpper(word), what)
				}
				literal.WriteString(formula[i:end])
				i = end
			case isNumberWord(word), strings.EqualFold(word, "TRUE"), strings.EqualFold(word, "FALSE"):
				literal.WriteString(formula[i:end])
				i = end
			default:
				switch {
				case ctx.defined[strings.ToUpper(word)]:
					literal.WriteString(formula[i:end])
				case isCellReference(word):
					// A reference to whole columns is left to the engine, but it
					// is remembered: the engine re-scans the range it names every
					// time the formula runs, which for a per-row formula is once
					// per row.
					if span := wholeColumnSpan(formula, i, end); span != "" {
						ctx.broadRefs = append(ctx.broadRefs, span)
					}
					literal.WriteString(formula[i:end])
				default:
					idx, ok := ctx.column(word)
					if !ok {
						return reject("%s", unknownName(word))
					}
					addColumn(idx, i)
				}
				i = end
			}
		default:
			literal.WriteByte(c)
			i++
		}
	}
	flush()
	// A per-group formula turns a column reference into a range, which is the
	// argument these functions mishandle; a per-row formula turns it into a
	// single cell, where there is nothing to collapse. Only the first case is
	// worth a warning, so only the first is collected.
	if ctx.flag == "agg" {
		seen := map[string]bool{}
		for _, call := range riskCalls {
			if seen[call.name] {
				continue
			}
			for _, at := range refAt {
				if at > call.start && at < call.end {
					seen[call.name] = true
					ctx.misused = append(ctx.misused, call.name)
					break
				}
			}
		}
	}
	return parts, nil
}

// balancedParens reports whether every "(" in a formula is closed, ignoring
// the ones inside string literals and quoted sheet names.
func balancedParens(formula string) bool {
	depth := 0
	for i := 0; i < len(formula); i++ {
		switch formula[i] {
		case '"', '\'':
			quote := formula[i]
			for i++; i < len(formula); i++ {
				if formula[i] != quote {
					continue
				}
				if i+1 < len(formula) && formula[i+1] == quote {
					i++
					continue
				}
				break
			}
		case '(':
			depth++
		case ')':
			if depth--; depth < 0 {
				return false
			}
		}
	}
	return depth == 0
}

// closingParen returns the index just past the ")" matching the "(" at open,
// taking nested parentheses and string literals into account, or -1 when the
// run never closes.
func closingParen(s string, open int) int {
	depth := 0
	for i := open; i < len(s); i++ {
		switch s[i] {
		case '"':
			end := i + 1
			for end < len(s) {
				if s[end] != '"' {
					end++
					continue
				}
				if end+1 < len(s) && s[end+1] == '"' {
					end += 2
					continue
				}
				break
			}
			i = end
		case '(':
			depth++
		case ')':
			if depth--; depth == 0 {
				return i + 1
			}
		}
	}
	return -1
}

// wholeColumnSpan reports the whole-column reference that starts at a word, if
// that is what it is: A:A, $A:$B. The scanner reads the two column names as
// separate words with a ":" between them, so recognising one needs the
// lookahead this does.
func wholeColumnSpan(formula string, start, end int) string {
	if !isColumnWord(formula[start:end]) {
		return ""
	}
	next := skipSpaces(formula, end)
	if next >= len(formula) || formula[next] != ':' {
		return ""
	}
	next++
	right := next
	for right < len(formula) && isFormulaWordByte(formula[right]) {
		right++
	}
	if !isColumnWord(formula[next:right]) {
		return ""
	}
	return formula[start:right]
}

// isColumnWord reports whether a word names a column rather than a cell: A, $AB,
// but not A1 and not a name that is anything else.
func isColumnWord(word string) bool {
	word = strings.ReplaceAll(word, "$", "")
	if word == "" {
		return false
	}
	_, err := excelize.ColumnNameToNumber(word)
	return err == nil
}

// isSheetName reports whether a workbook has a sheet with this name. The
// comparison is case-insensitive because that is how resolveSheet resolves a
// sheet, and the sheet in 'sheet1'!A1 is no more wrong than the one in -s SHEET1.
func isSheetName(ctx *formulaContext, name string) bool {
	for _, sheet := range ctx.sheets {
		if strings.EqualFold(sheet, name) {
			return true
		}
	}
	return false
}

func skipSpaces(s string, i int) int {
	for i < len(s) && (s[i] == ' ' || s[i] == '\t') {
		i++
	}
	return i
}

// isFormulaWordByte reports whether a byte can be part of a name, a reference
// or a number inside a formula. Everything else is punctuation: operators,
// argument separators and the brackets the scanner handles itself.
//
// The set is deliberately wide — a workbook's column names are Chinese, English
// and dotted in equal measure — and the interesting cases are decided after the
// word has been read, not by narrowing this.
func isFormulaWordByte(b byte) bool {
	switch b {
	case 0, ' ', '\t', '\n', '\r', '"', '\'', '(', ')', ',', ';', '+', '-', '*',
		'/', '^', '&', '%', '<', '>', '=', '!', '{', '}', '[', ']', '#', '@', ':':
		return false
	}
	return true
}

// isFormulaReferenceByte is the wider set used after a sheet name, where ":"
// and "$" belong to the reference rather than separating anything.
func isFormulaReferenceByte(b byte) bool {
	switch b {
	case 0, ' ', '\t', '\n', '\r', '"', '\'', '(', ')', ',', ';', '+', '-', '*',
		'/', '^', '&', '%', '<', '>', '=', '!', '{', '}':
		return false
	}
	return true
}

// isNumberWord reports whether a word is a numeric literal.
func isNumberWord(word string) bool {
	if word == "" {
		return false
	}
	_, err := strconv.ParseFloat(word, 64)
	return err == nil
}

// isCellReference reports whether a word is an A1-style cell reference or
// range: A1, $A$1, A1:B5, Z:Z, $A:$B.
func isCellReference(word string) bool {
	if word == "" {
		return false
	}
	parts := strings.Split(word, ":")
	if len(parts) > 2 {
		return false
	}
	for _, part := range parts {
		part = strings.ReplaceAll(part, "$", "")
		if part == "" {
			return false
		}
		if _, _, err := excelize.CellNameToCoordinates(part); err == nil {
			continue
		}
		if _, err := excelize.ColumnNameToNumber(part); err == nil {
			continue
		}
		return false
	}
	return true
}

// ---------------------------------------------------------------------------
// Evaluating a formula
// ---------------------------------------------------------------------------

// formulaEngine evaluates formulas against a workbook.
//
// excelize evaluates a formula from a cell, so the formula has to live
// somewhere. It lives on a scratch worksheet this tool adds and removes, never
// on the sheet being read: writing into that sheet invalidates the streaming
// row iterator, measured at 3m43s for 2600 rows against 1.3s through a scratch
// sheet. Nothing the caller handed in has changed by the time the command
// returns.
type formulaEngine struct {
	f *excelize.File
	// sheet holds the formula cell and the columns --agg writes for a group.
	sheet string
	// values holds the derived-column values a later --col reads back. It is a
	// separate sheet because --agg overwrites whole columns of the formula sheet
	// once per group, and a value written there would not survive.
	values string
	// cell is where formulas are set. Every evaluation overwrites it and the
	// result is read before the next one, so a single cell is enough.
	cell string
}

func newFormulaEngine(f *excelize.File) *formulaEngine {
	return &formulaEngine{f: f, cell: "A1"}
}

// ensure creates the scratch worksheet on first use, so that a command which
// never evaluates a formula never touches the workbook.
func (e *formulaEngine) ensure() error {
	if e.sheet != "" {
		return nil
	}
	name, err := e.freeSheetName()
	if err != nil {
		return err
	}
	if _, err = e.f.NewSheet(name); err != nil {
		return err
	}
	e.sheet = name
	return nil
}

// valuesSheet returns the worksheet that holds a derived column's value for the
// row being computed, creating it on first use.
func (e *formulaEngine) valuesSheet() (string, error) {
	if e.values != "" {
		return e.values, nil
	}
	name, err := e.freeSheetName()
	if err != nil {
		return "", err
	}
	if _, err = e.f.NewSheet(name); err != nil {
		return "", err
	}
	e.values = name
	return name, nil
}

// freeSheetName picks a name no sheet in the workbook is using.
func (e *formulaEngine) freeSheetName() (string, error) {
	taken := map[string]bool{}
	for _, sheet := range e.f.GetSheetList() {
		taken[strings.ToLower(sheet)] = true
	}
	for i := 0; ; i++ {
		name := scratchPrefix
		if i > 0 {
			name = fmt.Sprintf("%s_%d", scratchPrefix, i)
		}
		if !taken[strings.ToLower(name)] {
			return name, nil
		}
		if i > 1000 {
			return "", errors.New("the workbook has no free scratch worksheet name")
		}
	}
}

// close removes the scratch worksheets, leaving the workbook as it was found.
func (e *formulaEngine) close() {
	if e == nil {
		return
	}
	for _, sheet := range []string{e.sheet, e.values} {
		if sheet != "" {
			_ = e.f.DeleteSheet(sheet)
		}
	}
	e.sheet, e.values = "", ""
}

// setValue writes a derived column's value where a formula that references that
// column can read it.
func (e *formulaEngine) setValue(cell string, value any) error {
	sheet, err := e.valuesSheet()
	if err != nil {
		return err
	}
	return e.f.SetCellValue(sheet, cell, value)
}

// derivedCellRef is the reference a formula uses to read a value written by
// setValue.
func (e *formulaEngine) derivedCellRef(index int) string {
	return fmt.Sprintf("%s!$A$%d", quoteSheet(e.values), index+1)
}

// eval evaluates one formula and returns its value.
//
// A non-nil error means the formula could not be computed for this input. It is
// either the formula's own fault — see formulaErrorFatal, which the caller uses
// to reject the command instead of scanning the rest of the sheet for the same
// failure — or this row's, in which case the message is the value the caller
// sees and the row is counted.
func (e *formulaEngine) eval(formula string) (string, error) {
	if err := e.ensure(); err != nil {
		return "", err
	}
	if err := e.f.SetCellFormula(e.sheet, e.cell, formula); err != nil {
		return "", err
	}
	value, err := e.f.CalcCellValue(e.sheet, e.cell)
	if err != nil {
		// The engine reports a failed evaluation with a value as well as an
		// error for some functions ("#N/A" beside "VLOOKUP no result found").
		// The message names the cause, so it is the message that is passed on;
		// an empty string would be indistinguishable from an empty cell.
		return "", err
	}
	return value, nil
}

// formulaErrorFatal reports whether a failed evaluation says the formula itself
// cannot work, rather than that these particular values cannot be computed.
//
// The distinction is what keeps a typo from costing a full scan: an unsupported
// function or a malformed formula fails on every row, so the command is
// rejected at the first one, with the engine's own message — which names the
// function.
func formulaErrorFatal(err error) bool {
	if err == nil {
		return false
	}
	message := err.Error()
	if strings.HasPrefix(message, "not support ") && strings.HasSuffix(message, " function") {
		return true
	}
	return message == "formula not valid"
}

// formulaHint adds what the message can say about a failure the engine named
// and the caller is likely to meet: QUARTER is the one Excel function the
// engine does not implement that office data reaches for, and the replacement
// is not something a caller discovers by trying.
func formulaHint(message string) string {
	if strings.Contains(message, "not support QUARTER function") {
		return "; QUARTER is not in the engine's function list — ROUNDUP(MONTH(d)/3,0) is " +
			"the same value"
	}
	return ""
}

// quoteSheet renders a sheet name for use in a formula, quoting it the way
// Excel does. Quoting is always safe; not quoting is only safe for a name
// without spaces, and a workbook's sheets are named by its author.
func quoteSheet(name string) string {
	return "'" + strings.ReplaceAll(name, "'", "''") + "'"
}

// cellRef renders an absolute reference to one cell of a sheet.
func cellRef(sheet string, col, row int) string {
	return fmt.Sprintf("%s!$%s$%d", quoteSheet(sheet), columnLetter(col), row)
}

// rangeRef renders an absolute reference to a run of cells in one column.
func rangeRef(sheet string, col, firstRow, lastRow int) string {
	letter := columnLetter(col)
	return fmt.Sprintf("%s!$%s$%d:$%s$%d", quoteSheet(sheet), letter, firstRow, letter, lastRow)
}

// formulaColumnName rejects a derived column name that would be ambiguous later.
//
// A name that is also a column letter or an index would be read as one by every
// later reference — --group-by B would reach the sheet's column B rather than
// the derived column — and the confusion would be silent, which is the one
// outcome this tool refuses to ship.
func formulaColumnName(name string) error {
	if name == "" {
		return errors.New("the column needs a name: write name=formula")
	}
	if _, err := strconv.Atoi(name); err == nil {
		return fmt.Errorf("a derived column cannot be called %q, because that is how a column "+
			"index is written; give it a name of its own", name)
	}
	if _, err := excelize.ColumnNameToNumber(name); err == nil {
		return fmt.Errorf("a derived column cannot be called %q, because that is how a column "+
			"letter is written; give it a name of its own", name)
	}
	if isCellReference(name) {
		// A1 is a valid name for a column of the sheet — the header wins over the
		// cell reference, and it has to, because the data is addressed by its
		// own name. A derived column has no such claim, and one named A1 would
		// silently capture every =A1 in every formula.
		return fmt.Errorf("a derived column cannot be called %q, because that is how a cell "+
			"reference is written; give it a name of its own", name)
	}
	return nil
}

// splitNamedFormula splits a "name=formula" flag value.
func splitNamedFormula(item string) (name, formula string, err error) {
	eq := strings.IndexByte(item, '=')
	if eq <= 0 {
		return "", "", errors.New("expected name=formula, as in \"月=MONTH(过账日期)\"")
	}
	return strings.TrimSpace(item[:eq]), strings.TrimSpace(item[eq+1:]), nil
}

// resolveRowColumn resolves a column reference written on a command line: a
// derived column first, then a header name, then a column letter or index.
//
// A derived column cannot be shadowed because one that shares a name with an
// existing column is refused outright, so the order here is never in question.
func resolveRowColumn(ref string, header []string, derived *derivedSet) (int, error) {
	if derived != nil {
		trimmed := strings.TrimSpace(ref)
		for _, exact := range []bool{true, false} {
			for i, col := range derived.cols {
				if matchesName(col.name, trimmed, exact) {
					return -i - 2, nil
				}
			}
		}
	}
	return resolveColumn(ref, header)
}

// definedNames lists the workbook's defined names. The engine resolves them
// itself, so a formula that uses one must be left alone rather than reported as
// an unknown column.
func definedNames(f *excelize.File) map[string]bool {
	names := map[string]bool{}
	for _, name := range f.GetDefinedName() {
		names[strings.ToUpper(name.Name)] = true
	}
	return names
}

// formulaValueAt reads a column out of a row, where a negative index names a
// derived column rather than a cell of the sheet.
//
// A row's derived columns are computed alongside it rather than appended to it:
// a page's width is not known until the last row has been read, so their
// position in the cell slice cannot be assigned while the scan is running.
// Negative indexes keep the two apart without the caller having to know which
// kind of column it named.
func formulaValueAt(cells, derived []string, idx int) string {
	if idx < 0 {
		return valueAt(derived, -idx-2)
	}
	return valueAt(cells, idx)
}

// scratchValue renders a cell the way a formula reads it: as a number where the
// cell holds one, and as text otherwise, so that MEDIAN and STDEV see numbers
// and COUNTIFS can still match a label.
//
// The number is the one the tool would aggregate — currency symbols stripped,
// a trailing % scaled — because a column that --sum can add is a column --agg
// must be able to average, and two different answers to "what is this column's
// total" would be worse than either.
func scratchValue(cell string) any {
	if cell == "" {
		return ""
	}
	if number, _, ok := parseNumberUnit(cell); ok {
		return number
	}
	return cell
}

// ---------------------------------------------------------------------------
// --col: a formula per row
// ---------------------------------------------------------------------------

// derivedColumn is one --col: a name and the formula that computes it for each
// row of the sheet.
type derivedColumn struct {
	name   string
	source string
	parts  formulaParts
	// broadRefs are the whole-column references the formula makes, kept only so
	// that the response can say what they cost.
	broadRefs []string
}

// derivedSet is a command's --col columns, in the order they were given.
//
// A later formula may read an earlier column by name — the values are written
// to the scratch sheet as they are computed, so --col "净利=收入-成本" and
// --col "净利率=净利/收入" work in that order and only in that order.
type derivedSet struct {
	cols []*derivedColumn
	// failures counts, per column, the rows the engine could not compute, and
	// samples names the first one. Both are what the caller is told at the end:
	// a row that failed is reported the way a skipped cell is, because a
	// silently empty column is what this is here to prevent.
	failures []int
	sampled  []string
}

// parseDerivedColumns scans every --col formula against the sheet's header.
func parseDerivedColumns(items []string, ctx *formulaContext) (*derivedSet, error) {
	set := &derivedSet{}
	seen := map[string]bool{}
	for _, item := range items {
		name, source, err := splitNamedFormula(item)
		if err != nil {
			return nil, fmt.Errorf("--%s %q: %w", ctx.flag, item, err)
		}
		if err = formulaColumnName(name); err != nil {
			return nil, fmt.Errorf("--%s %q: %w", ctx.flag, item, err)
		}
		if seen[name] {
			return nil, fmt.Errorf("--%s %q: two derived columns are both named %q",
				ctx.flag, item, name)
		}
		if err = checkNameClash(name, ctx); err != nil {
			return nil, fmt.Errorf("--%s %q: %w", ctx.flag, item, err)
		}
		ctx.broadRefs = nil
		parts, err := scanFormula(source, ctx)
		if err != nil {
			return nil, err
		}
		seen[name] = true
		set.cols = append(set.cols, &derivedColumn{
			name: name, source: source, parts: parts, broadRefs: ctx.broadRefs,
		})
		set.failures = append(set.failures, 0)
		set.sampled = append(set.sampled, "")
		// Later formulas may name this one.
		ctx.derived = append(ctx.derived, name)
	}
	return set, nil
}

// checkNameClash refuses a derived column whose name is already a column of the
// sheet. Letting one shadow the other would make --group-by 月 mean one thing to
// the caller and another to the tool, and the wrong choice would be silent.
func checkNameClash(name string, ctx *formulaContext) error {
	for _, cell := range ctx.header {
		if matchesName(cell, name, true) {
			return fmt.Errorf(
				"this sheet already has a column named %q; give the derived column another name, "+
					"or group by the existing column", strings.TrimSpace(cell))
		}
	}
	return nil
}

// names returns the derived columns' names, in order.
func (d *derivedSet) names() []string {
	if d == nil {
		return nil
	}
	out := make([]string, 0, len(d.cols))
	for _, col := range d.cols {
		out = append(out, col.name)
	}
	return out
}

// eval computes every derived column for one row. The returned error is fatal —
// the formula cannot work for any row — and ends the command; the values it
// could not compute for this row are recorded and summarised by warnings.
func (d *derivedSet) eval(e *formulaEngine, sheet string, row int, cells []string) ([]string, error) {
	if d == nil || len(d.cols) == 0 {
		return nil, nil
	}
	out := make([]string, len(d.cols))
	for i, col := range d.cols {
		formula, err := col.format(e, sheet, row, out)
		if err != nil {
			return nil, fmt.Errorf("--col %q: %w", col.source, err)
		}
		value, err := e.eval(formula)
		if err != nil {
			if formulaErrorFatal(err) {
				return nil, fmt.Errorf("--col %q: %s%s", col.source, err.Error(),
					formulaHint(err.Error()))
			}
			d.failures[i]++
			if d.sampled[i] == "" {
				d.sampled[i] = err.Error()
			}
			// The engine's message stands in for the value, so that a row it
			// could not compute is never read as a row whose column is empty.
			value = err.Error()
		}
		out[i] = value
	}
	return out, nil
}

// format renders a column's formula for one row, replacing each column name
// with a reference to the cell holding that row's value.
func (c *derivedColumn) format(e *formulaEngine, sheet string, row int, derived []string) (string, error) {
	return c.parts.render(func(idx int) (string, error) {
		if idx >= 0 {
			return cellRef(sheet, idx+1, row), nil
		}
		// An earlier derived column. It has no cell in the worksheet, so its
		// value is written to the scratch sheet — as a value, not as the text
		// the output renders, so that arithmetic on it works.
		ordinal := -idx - 2
		if ordinal >= len(derived) {
			return "", fmt.Errorf("derived column %d is not available to this formula", ordinal+1)
		}
		if err := e.setValue(derivedCellName(ordinal), scratchValue(derived[ordinal])); err != nil {
			return "", err
		}
		return e.derivedCellRef(ordinal), nil
	})
}

// warnings describes the rows the engine could not compute, one line per
// column.
func (d *derivedSet) warnings() []string {
	if d == nil {
		return nil
	}
	var out []string
	for i, col := range d.cols {
		if len(col.broadRefs) > 0 {
			out = append(out, broadRefWarning("--col", col.name, col.broadRefs, "for every row"))
		}
		if d.failures[i] == 0 {
			continue
		}
		out = append(out, fmt.Sprintf(
			"--col %q could not be computed for %s (first: %s); those rows carry the engine's "+
				"message in the column, which is not the same as an empty one",
			col.name, rowsPhrase(d.failures[i]), d.sampled[i]))
	}
	return out
}

// misusedWarning says what a call the engine gets wrong actually computed.
//
// It is not a refusal: the caller may know exactly what they are doing, and the
// function is right when its arguments are scalars. What it is not allowed to
// be is silent, because the number it returns looks like an answer.
func misusedWarning(flag, name string, funcs []string) string {
	return fmt.Sprintf(
		"%s %q calls %s over a column, and the engine discounts only the first cell of a range: "+
			"measured on [-1000,1000,2000] at 10%%, NPV returned -909.09 where the correct value is "+
			"1419.98; write the values out as separate arguments, use XNPV with --raw dates, or "+
			"discount the column outside the tool",
		flag, name, strings.Join(funcs, ", "))
}

// broadRefWarning says what a whole-column reference costs. It is not a
// correctness warning: the answer is right, it simply
// costs the rows evaluated times the rows the range holds, which reads as a
// hang rather than as a slow query.
func broadRefWarning(flag, name string, refs []string, when string) string {
	return fmt.Sprintf(
		"%s %q reads whole columns (%s), and the engine re-scans the range it names %s, so the "+
			"cost is the rows it evaluates times the rows that range holds — 2,700 rows against "+
			"a 2,700-row column measured 44s, and against a 100-row range 0.8s; bound it with "+
			"$A$1:$B$100 unless the sheet it names is small",
		flag, name, strings.Join(refs, ", "), when)
}

// derivedCellName is the scratch cell holding one derived column's value for
// the row being computed.
func derivedCellName(ordinal int) string {
	return fmt.Sprintf("A%d", ordinal+1)
}

// ---------------------------------------------------------------------------
// --agg: a formula per group
// ---------------------------------------------------------------------------

// formulaAgg is one --agg: a formula computed once per group, over the rows the
// group holds.
type formulaAgg struct {
	name   string
	source string
	parts  formulaParts
	// cols are the columns it reads, in first-use order.
	cols []int
	// broadRefs are the whole-column references the formula makes, and misused
	// names the risky functions it called over a column.
	broadRefs []string
	misused   []string
}

// formulaAggRunner evaluates every --agg formula against each group.
//
// A group's rows are almost never contiguous — a group is defined by a value,
// not by a position — so a function that takes a pair of ranges (COUNTIFS,
// SUMIFS) cannot be given the sheet's own cells. Each column a formula reads is
// therefore written to the scratch sheet as one block per group, which turns
// "the rows of this group" into a range the engine can be handed.
type formulaAggRunner struct {
	specs []*formulaAgg
	// cols is the union of the columns the formulas read, and slot maps a
	// column index to its place in that union, which is also its scratch column.
	cols []int
	slot map[int]int
	// failures counts, per formula, the groups the engine could not compute, and
	// sampled keeps the first message. A formula that fails for one group
	// usually fails for several, and a line per group would bury the rest of the
	// warnings — the count is also the more useful number.
	failures []int
	sampled  []string
	// names names each column the formulas read, and filled counts the matched
	// rows that held anything in it. A column that held nothing anywhere is
	// worth saying out loud: SUM of nothing is 0, which is a number.
	names  []string
	filled []int
	rows   int
}

func newFormulaAggRunner(specs []*formulaAgg, header []string) *formulaAggRunner {
	runner := &formulaAggRunner{
		specs:    specs,
		slot:     map[int]int{},
		failures: make([]int, len(specs)),
		sampled:  make([]string, len(specs)),
	}
	for _, spec := range specs {
		for _, idx := range spec.cols {
			if _, ok := runner.slot[idx]; ok {
				continue
			}
			runner.slot[idx] = len(runner.cols)
			runner.cols = append(runner.cols, idx)
			name := valueAt(header, idx)
			if name == "" {
				name = columnLetter(idx + 1)
			}
			runner.names = append(runner.names, name)
		}
	}
	runner.filled = make([]int, len(runner.cols))
	return runner
}

// warnings describes the groups the engine could not compute for, one line per
// formula.
func (r *formulaAggRunner) warnings(groups int) []string {
	if r == nil {
		return nil
	}
	var out []string
	for _, spec := range r.specs {
		if len(spec.misused) > 0 {
			out = append(out, misusedWarning("--agg", spec.name, spec.misused))
		}
	}
	for slot, name := range r.names {
		// A column that held nothing is what an uncached formula looks like, and
		// the engine answers SUM over it with 0 — a number, and a wrong one to
		// report without a word. --calc is the fix, exactly as it is for --sum.
		if r.cols[slot] >= 0 && r.filled[slot] == 0 && r.rows > 0 {
			out = append(out, fmt.Sprintf(
				"column %q held nothing in any of the %s this aggregation read, so a formula "+
					"over it computed from nothing — SUM of nothing is 0, MEDIAN of nothing is "+
					"null; a formula cell whose result was never cached reads this way, so re-run "+
					"with --calc if that is what these are",
				name, rowsPhrase(r.rows)))
		}
	}
	for i, spec := range r.specs {
		if len(spec.broadRefs) > 0 {
			out = append(out, broadRefWarning("--agg", spec.name, spec.broadRefs, "for every group"))
		}
		if r.failures[i] == 0 {
			continue
		}
		out = append(out, fmt.Sprintf(
			"--agg %q could not be computed for %d of %d groups (first: %s); those groups "+
				"report null, which is not the same as a group with nothing to aggregate",
			spec.name, r.failures[i], groups, r.sampled[i]))
	}
	return out
}

// addRow collects one matched row's values for the group it belongs to.
func (r *formulaAggRunner) addRow(group *groupState, cells, derived []string) {
	if r == nil || len(r.cols) == 0 {
		return
	}
	if group.formulaValues == nil {
		group.formulaValues = make([][]any, len(r.cols))
	}
	for slot, idx := range r.cols {
		raw := formulaValueAt(cells, derived, idx)
		if raw != "" {
			r.filled[slot]++
		}
		group.formulaValues[slot] = append(group.formulaValues[slot], scratchValue(raw))
	}
	r.rows++
}

// run computes every formula of one group. The returned error is fatal: the
// formula cannot work for any group, so the command is rejected rather than
// repeated for every group left.
func (r *formulaAggRunner) run(e *formulaEngine, group *groupState) (map[string]any, []string, error) {
	if err := e.ensure(); err != nil {
		return nil, nil, err
	}
	// The formula goes in a column past the data it reads. A formula cell inside
	// the range it aggregates is counted as one of the values — it made every
	// median in a test off by one row, which is the worst kind of wrong: close
	// enough to look right.
	e.cell = columnLetter(len(r.cols)+1) + "1"
	n := group.rows
	for slot := range r.cols {
		values := group.formulaValues[slot]
		if len(values) == 0 {
			continue
		}
		if err := e.f.SetSheetCol(e.sheet, columnLetter(slot+1)+"1", &values); err != nil {
			return nil, nil, err
		}
	}
	out := make(map[string]any, len(r.specs))
	for i, spec := range r.specs {
		formula, err := spec.parts.render(func(idx int) (string, error) {
			return rangeRef(e.sheet, r.slot[idx]+1, 1, n), nil
		})
		if err != nil {
			return nil, nil, fmt.Errorf("--agg %q: %w", spec.source, err)
		}
		value, err := e.eval(formula)
		if err != nil {
			if formulaErrorFatal(err) {
				return nil, nil, fmt.Errorf("--agg %q: %s%s", spec.source, err.Error(),
					formulaHint(err.Error()))
			}
			r.failures[i]++
			if r.sampled[i] == "" {
				r.sampled[i] = err.Error()
			}
			out[spec.name] = nil
			continue
		}
		out[spec.name] = formulaResult(value)
	}
	return out, nil, nil
}

// formulaResult renders what a formula evaluated to. An empty result is no
// value at all rather than an empty string, matching how every other aggregate
// reports "nothing to compute"; a text result is kept, because INDEX and
// TEXTJOIN are as reasonable to ask for as MEDIAN.
func formulaResult(value string) any {
	if value == "" {
		return nil
	}
	if number, ok := parseNumber(value); ok {
		return roundToExcel(number)
	}
	return value
}
