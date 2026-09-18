// Copyright (c) 2026 henryyu@163.com. All rights reserved.
//
// This file is part of xlpeek, a read-only spreadsheet reader for
// agents. See README.md for what it does and docs/AGENTS.md for how it is
// meant to be driven.

package main

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/xuri/excelize/v2"
)

// Paging defaults.
//
// lookaheadCap bounds how far past a full page the reader keeps scanning while
// deciding whether more matching rows exist. Without it, a --where filter that
// matches nothing beyond the page would turn a single page read into a full
// worksheet scan.
const (
	defaultLimit      = 100
	maxLimit          = 5000
	defaultMaxColumns = 128
	lookaheadCap      = 10000
	maxWarnings       = 10
)

// workbookOptions carries the flags that affect how a workbook is opened.
type workbookOptions struct {
	password string
	raw      bool
	tmpDir   string
}

// sessionEnabled is set by the serve command. Outside serve mode it stays
// false, every command opens and closes its own workbook, and nothing below
// has any effect.
var sessionEnabled bool

// defaultSessionCacheSize keeps a handful of workbooks open.
//
// One would be enough for a caller that works through a single file, but a
// caller comparing two workbooks would then re-parse on every swap — measured
// at five times the cost of a cache hit. Four is enough for the usual case and
// few enough that the parsed worksheets cannot quietly become the largest
// thing in the process.
const defaultSessionCacheSize = 4

type workbookEntry struct {
	path    string
	options workbookOptions
	file    *excelize.File
}

// sessionWorkbooks holds the open workbooks, most recently used first. The
// list is at most a few entries long, so a linear scan is the whole index.
var (
	sessionWorkbooks  []workbookEntry
	sessionCacheLimit = defaultSessionCacheSize
	sessionStarted    time.Time
	sessionRequests   int
)

func cacheLookup(path string, o workbookOptions) *excelize.File {
	for i := range sessionWorkbooks {
		entry := sessionWorkbooks[i]
		if entry.path != path || entry.options != o {
			continue
		}
		copy(sessionWorkbooks[1:i+1], sessionWorkbooks[:i])
		sessionWorkbooks[0] = entry
		return entry.file
	}
	return nil
}

func cacheStore(path string, o workbookOptions, f *excelize.File) {
	sessionWorkbooks = append([]workbookEntry{{path: path, options: o, file: f}}, sessionWorkbooks...)
	for len(sessionWorkbooks) > sessionCacheLimit {
		last := len(sessionWorkbooks) - 1
		_ = sessionWorkbooks[last].file.Close()
		sessionWorkbooks = sessionWorkbooks[:last]
	}
}

// cachedWorkbookPaths lists the open workbooks, most recently used first.
func cachedWorkbookPaths() []string {
	if len(sessionWorkbooks) == 0 {
		return nil
	}
	paths := make([]string, 0, len(sessionWorkbooks))
	for _, entry := range sessionWorkbooks {
		paths = append(paths, entry.path)
	}
	return paths
}

// closeSessionWorkbooks releases every cached handle.
func closeSessionWorkbooks() {
	for _, entry := range sessionWorkbooks {
		_ = entry.file.Close()
	}
	sessionWorkbooks = nil
}

// releaseWorkbook closes a handle unless serve mode is holding on to it.
func releaseWorkbook(f *excelize.File) {
	if sessionEnabled {
		for _, entry := range sessionWorkbooks {
			if entry.file == f {
				return
			}
		}
	}
	_ = f.Close()
}

// oleIdentifier is the OLE2/CFB signature. An encrypted workbook has it, and
// so does a VBA project binary; the two are told apart by the error below.
var oleIdentifier = []byte{0xd0, 0xcf, 0x11, 0xe0, 0xa1, 0xb1, 0x1a, 0xe1}

// fileKind classifies a file by its leading bytes.
type fileKind int

const (
	kindOther fileKind = iota // neither a package nor an OLE container
	kindPackage
	kindOLE
)

func sniffFile(path string) fileKind {
	file, err := os.Open(path)
	if err != nil {
		return kindOther
	}
	defer func() { _ = file.Close() }()
	header := make([]byte, 8)
	if _, err = io.ReadFull(file, header); err != nil {
		return kindOther
	}
	switch {
	case bytes.Equal(header, oleIdentifier):
		return kindOLE
	case bytes.Equal(header[:4], []byte{'P', 'K', 0x03, 0x04}),
		bytes.Equal(header[:4], []byte{'P', 'K', 0x05, 0x06}):
		return kindPackage
	}
	return kindOther
}

// openWorkbook opens a workbook for reading. RawCellValue selects between the
// stored value and the value with the cell's number format applied.
func openWorkbook(path string, o workbookOptions) (*excelize.File, error) {
	if sessionEnabled {
		if cached := cacheLookup(path, o); cached != nil {
			return cached, nil
		}
	}
	f, err := excelize.OpenFile(path, excelize.Options{
		Password:     o.password,
		RawCellValue: o.raw,
		TmpDir:       o.tmpDir,
	})
	if err != nil {
		// Translate the failures this layer can explain better than the
		// library can. A missing file is not a format problem, so it is left
		// alone for classify() to report.
		if !errors.Is(err, os.ErrNotExist) {
			switch kind := sniffFile(path); {
			case kind == kindOLE && o.password == "" &&
				!errors.Is(err, excelize.ErrWorkbookFileFormat):
				// Decrypting an OLE container with an empty password does not
				// report a wrong password; it yields bytes that then fail to
				// unzip. Without this the caller is told the file is corrupt.
				//
				// ErrWorkbookFileFormat is excluded because that is how a valid
				// OLE container which is not an encrypted workbook — a VBA
				// project binary — fails, and calling that "encrypted" would be
				// its own kind of misleading.
				return nil, errPasswordRequired
			case kind == kindOther:
				return nil, errNotSpreadsheet
			}
		}
		return nil, err
	}
	if sessionEnabled {
		cacheStore(path, o, f)
	}
	return f, nil
}

// resolveSheet maps a possibly empty, case-mismatched or numeric sheet
// reference onto the canonical name and index used by the workbook. An empty
// reference selects the first sheet, which is what an agent wants when it has
// not looked at the workbook yet.
//
// A number is an index into the sheet list, counted from zero because that is
// what the index and sheet_index fields of info and read already report — a
// caller who has seen "index": 0 has the number in hand and should not have to
// convert it to a name. It is matched only after the names, so a workbook with
// a sheet literally called "2" still reaches it by name.
func resolveSheet(f *excelize.File, name string) (string, int, error) {
	sheets := f.GetSheetList()
	if len(sheets) == 0 {
		return "", -1, errors.New("workbook contains no sheets")
	}
	if name == "" {
		return sheets[0], 0, nil
	}
	for i, sheet := range sheets {
		if sheet == name {
			return sheet, i, nil
		}
	}
	for i, sheet := range sheets {
		if strings.EqualFold(sheet, name) {
			return sheet, i, nil
		}
	}
	if index, err := strconv.Atoi(strings.TrimSpace(name)); err == nil {
		if index >= 0 && index < len(sheets) {
			return sheets[index], index, nil
		}
		// Reported through the missing-sheet path so that the caller is handed
		// the list of sheets — and, with it, the range the index had to be in.
		return "", -1, excelize.ErrSheetNotExist{SheetName: name}
	}
	return "", -1, excelize.ErrSheetNotExist{SheetName: name}
}

// resolveColumn resolves a column reference written as a header name, as a
// spreadsheet column letter (A, B, AA) or as a 1-based index. A header name
// wins over a letter so that a sheet with a column literally named "A" can
// still be addressed by name.
func resolveColumn(ref string, header []string) (int, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return -1, badColumn(errors.New("empty column reference"))
	}
	// Header cells are compared trimmed, because that is how they are reported:
	// a sheet whose header reads "    名称" is listed by info as "名称", and a
	// caller who then asks for "名称" must be understood. Resolving against the
	// raw value would refuse the very name the tool just handed out.
	for _, exact := range []bool{true, false} {
		for i, raw := range header {
			name := strings.TrimSpace(raw)
			if name == "" {
				continue
			}
			if (exact && name == ref) || (!exact && strings.EqualFold(name, ref)) {
				return i, nil
			}
		}
	}
	if n, err := strconv.Atoi(ref); err == nil {
		if n < 1 {
			return -1, badColumn(fmt.Errorf("column index %d is 1-based and must be positive", n))
		}
		return n - 1, nil
	}
	if n, err := excelize.ColumnNameToNumber(ref); err == nil {
		return n - 1, nil
	}
	return -1, badColumn(fmt.Errorf("column %q matches no header name, column letter or index", ref))
}

// columnLetter returns the spreadsheet column name for a 1-based column
// number, falling back to the decimal number when out of range so callers
// always get something printable.
func columnLetter(n int) string {
	name, err := excelize.ColumnNumberToName(n)
	if err != nil {
		return strconv.Itoa(n)
	}
	return name
}

// padRow returns a copy of cells padded with empty strings to width, truncating
// anything beyond it. excelize drops trailing empty cells, so without padding a
// row would silently change shape from one page to the next.
//
// The result is never nil, not even for a zero-width row: a nil slice marshals
// to JSON null, which a reader would take to mean "no row here" rather than
// "an empty row".
func padRow(cells []string, width int) []string {
	out := make([]string, width)
	copy(out, cells)
	return out
}

// headerKeys derives unique JSON object keys from a header row. Blank header
// cells fall back to the column letter, and duplicates get a numeric suffix so
// that a header such as ["", "金额", "金额"] yields distinct keys instead of
// silently collapsing two columns into one.
func headerKeys(header []string, width int) []string {
	keys := make([]string, width)
	seen := make(map[string]bool, width)
	for i := 0; i < width; i++ {
		base := ""
		if i < len(header) {
			base = strings.TrimSpace(header[i])
		}
		if base == "" {
			base = columnLetter(i + 1)
		}
		key := base
		for suffix := 2; seen[key]; suffix++ {
			key = fmt.Sprintf("%s_%d", base, suffix)
		}
		seen[key] = true
		keys[i] = key
	}
	return keys
}

func nonEmpty(cells []string) bool {
	for _, cell := range cells {
		if cell != "" {
			return true
		}
	}
	return false
}

// sheetScan is the result of a pass over one worksheet.
type sheetScan struct {
	header    []string
	sample    [][]string
	maxColumn int
	// lastRow, populatedRows and lastPopulatedRow are only meaningful for a
	// deep scan. A shallow scan stops once it has the sample, so reporting
	// those numbers would hand a caller a row count that is really just where
	// the scan happened to stop.
	deep             bool
	lastRow          int
	populatedRows    int
	lastPopulatedRow int
}

// scanSheet walks a worksheet in streaming order. When deep is false it stops
// as soon as the header and requested sample rows have been read; when deep is
// true it walks every row to produce exact counts.
func scanSheet(f *excelize.File, sheet string, headerRow, sampleRows int, deep bool) (*sheetScan, error) {
	iter, err := f.Rows(sheet)
	if err != nil {
		return nil, err
	}
	defer func() { _ = iter.Close() }()

	firstDataRow := 1
	if headerRow > 0 {
		firstDataRow = headerRow + 1
	}
	scan := &sheetScan{deep: deep}
	pos := 0
	for iter.Next() {
		pos++
		cells, err := iter.Columns()
		if err != nil {
			return nil, err
		}
		if headerRow > 0 && pos == headerRow {
			scan.header = cells
		}
		if len(cells) > scan.maxColumn {
			scan.maxColumn = len(cells)
		}
		if nonEmpty(cells) {
			scan.populatedRows++
			scan.lastPopulatedRow = pos
		}
		if pos >= firstDataRow && len(scan.sample) < sampleRows {
			scan.sample = append(scan.sample, cells)
		}
		// Stop once the header and the requested sample rows are in hand.
		if !deep && pos >= firstDataRow+sampleRows-1 {
			break
		}
	}
	if err = iter.Error(); err != nil {
		return nil, err
	}
	scan.lastRow = pos
	return scan, nil
}

// mergeRange is a merged region together with the value that belongs to every
// cell inside it.
type mergeRange struct {
	ref                string
	startCol, startRow int
	endCol, endRow     int
	value              string
}

// mergeFiller applies a worksheet's merged regions to rows as they stream past.
//
// A spreadsheet stores a merged value only in the top-left cell of the region
// and leaves the rest empty, so a reader that ignores merges sees blanks
// wherever a report used a merged heading or a label spanning several rows —
// and reasons confidently from them. Filling those in is what lets a group-by
// column hold the label its rows are actually filed under.
//
// Rows arrive in ascending order, so the region list is walked exactly once and
// only the regions covering the current row are kept live.
type mergeFiller struct {
	ranges []mergeRange
	active []mergeRange
	next   int
}

// newMergeFiller loads a worksheet's merged regions.
//
// GetMergeCells parses the worksheet structure and reads each region's value,
// so this gives up the streaming memory profile the same way --calc does. It
// is therefore opt-in rather than always on.
func newMergeFiller(f *excelize.File, sheet string) (*mergeFiller, error) {
	ranges, err := loadMergeRanges(f, sheet)
	if err != nil {
		return nil, err
	}
	return &mergeFiller{ranges: ranges, active: make([]mergeRange, 0, 8)}, nil
}

// apply drops the regions that ended before this row, admits the ones starting
// on it, then writes their value into the cells they span.
func (m *mergeFiller) apply(row int, cells []string, maxColumns int) []string {
	if len(m.ranges) == 0 {
		return cells
	}
	alive := m.active[:0]
	for _, r := range m.active {
		if r.endRow >= row {
			alive = append(alive, r)
		}
	}
	m.active = alive
	for m.next < len(m.ranges) && m.ranges[m.next].startRow <= row {
		if m.ranges[m.next].endRow >= row {
			m.active = append(m.active, m.ranges[m.next])
		}
		m.next++
	}
	if len(m.active) == 0 {
		return cells
	}
	return fillMergedRow(cells, m.active, maxColumns)
}

// loadMergeRanges collects the merged regions of a worksheet, ordered by the
// row they start on.
//
// GetMergeCells parses the worksheet structure and reads each region's value,
// so this gives up the streaming memory profile the same way --calc does. It
// is therefore opt-in rather than always on.
func loadMergeRanges(f *excelize.File, sheet string) ([]mergeRange, error) {
	cells, err := f.GetMergeCells(sheet)
	if err != nil {
		return nil, err
	}
	ranges := make([]mergeRange, 0, len(cells))
	for _, cell := range cells {
		if len(cell) < 2 {
			continue
		}
		ref, value := cell[0], cell[1]
		parts := strings.Split(ref, ":")
		startCol, startRow, err := excelize.CellNameToCoordinates(strings.ReplaceAll(parts[0], "$", ""))
		if err != nil {
			continue
		}
		endCol, endRow := startCol, startRow
		if len(parts) == 2 {
			if endCol, endRow, err = excelize.CellNameToCoordinates(
				strings.ReplaceAll(parts[1], "$", "")); err != nil {
				continue
			}
		}
		if endCol < startCol || endRow < startRow {
			continue
		}
		ranges = append(ranges, mergeRange{
			ref:      ref,
			startCol: startCol, startRow: startRow,
			endCol: endCol, endRow: endRow,
			value: value,
		})
	}
	sort.Slice(ranges, func(i, j int) bool { return ranges[i].startRow < ranges[j].startRow })
	return ranges, nil
}

// fillMergedRow writes each covering region's value into the cells it spans,
// growing the row when a region reaches past the cells excelize returned.
// Only blanks are filled, so a region can never overwrite real data.
func fillMergedRow(cells []string, covering []mergeRange, maxColumns int) []string {
	width := len(cells)
	for _, r := range covering {
		if r.endCol > width {
			width = r.endCol
		}
	}
	if maxColumns > 0 && width > maxColumns {
		width = maxColumns
	}
	if width > len(cells) {
		grown := make([]string, width)
		copy(grown, cells)
		cells = grown
	}
	for _, r := range covering {
		for col := r.startCol; col <= r.endCol && col <= len(cells); col++ {
			if cells[col-1] == "" {
				cells[col-1] = r.value
			}
		}
	}
	return cells
}

// pageRequest describes one page of a worksheet.
type pageRequest struct {
	sheet      string
	sheetIndex int
	// headerRow is the 1-based worksheet row holding the column names. Zero
	// means the sheet has no header and columns are addressed by letter or
	// index; data then starts at row 1.
	headerRow  int
	offset     int
	limit      int
	filters    []*filter
	skipEmpty  bool
	calc       bool
	fillMerged bool
	dates      string
	maxColumns int
	// colArgs are the --col formulas as the caller wrote them. They are scanned
	// inside readPage because the header they name columns from is only read
	// there, and cols holds the result.
	colArgs []string
	cols    *derivedSet
}

// pageResult is a page of rows, already padded to a uniform width.
type pageResult struct {
	sheet       string
	sheetIndex  int
	header      []string
	rows        [][]string
	width       int
	widthCapped bool
	firstRow    int
	lastRow     int
	hasMore     bool
	approximate bool
	scanned     int
	warnings    []string
	// derived holds each returned row's --col values, kept alongside the row
	// until the page's width is known and they can be appended to it, and
	// derivedNames are their column names.
	derived      [][]string
	derivedNames []string
}

// readPage streams one page out of a worksheet.
//
// excelize's Rows iterator is forward-only and yields exactly one position per
// spreadsheet row number, filling gaps in a sparse worksheet with empty
// positions. The loop counter is therefore the 1-based row number, and
// skipping a page is cheap: Next advances the XML decoder without
// materialising cell values, so an offset costs a scan but not memory.
func readPage(f *excelize.File, req pageRequest) (*pageResult, error) {
	iter, err := f.Rows(req.sheet)
	if err != nil {
		return nil, err
	}
	defer func() { _ = iter.Close() }()

	res := &pageResult{sheet: req.sheet, sheetIndex: req.sheetIndex}
	firstDataRow := 1
	if req.headerRow > 0 {
		firstDataRow = req.headerRow + 1
	} else {
		if len(req.colArgs) > 0 {
			return nil, usageFailure(errors.New(
				"--col needs a header row: a derived column is referred to by name, and a sheet " +
					"read without one has no names to refer to"))
		}
		if err = resolveFilters(req.filters, nil, nil); err != nil {
			return nil, err
		}
	}

	var (
		pos     int
		skipped int
		probe   int
		merges  *mergeFiller
		dates   *dateResolver
		held    *excelize.Rows
	)
	if req.fillMerged {
		if merges, err = newMergeFiller(f, req.sheet); err != nil {
			return nil, err
		}
	}
	if req.dates != datesDisplay {
		// Two iterators walk the same worksheet in step when the date cells
		// have to be rewritten: one yields what the sheet displays and the
		// other the number it stores, and writing a date in a stable form needs
		// both — the display to know the format's intent, the stored value to
		// have something to reformat.
		dates = newDateResolver(f, req.dates)
		if held, err = f.Rows(req.sheet); err != nil {
			return nil, err
		}
		defer func() { _ = held.Close() }()
	}
	applyMerges := func(row int, cells []string) []string {
		if merges == nil {
			return cells
		}
		return merges.apply(row, cells, req.maxColumns)
	}

	// The engine adds its scratch worksheet lazily and removes it on the way
	// out, so a page that computes nothing never touches the workbook.
	engine := newFormulaEngine(f)
	defer engine.close()

	for iter.Next() {
		pos++
		var stored []string
		if held != nil {
			if held.Next() {
				// The stored number is asked for per call rather than inherited
				// from how the workbook was opened: the row below needs the
				// displayed value whatever --raw says, and this one needs the
				// number behind it either way.
				if stored, err = held.Columns(excelize.Options{RawCellValue: true}); err != nil {
					return nil, err
				}
			}
		}
		if pos < firstDataRow {
			if req.headerRow > 0 && pos == req.headerRow {
				if res.header, err = iter.Columns(); err != nil {
					return nil, err
				}
				res.header = applyMerges(pos, res.header)
				// The --col formulas are scanned here because this is the first
				// moment their column names can be resolved, and they are scanned
				// before the filters because a filter may name one of them.
				if len(req.colArgs) > 0 && req.cols == nil {
					ctx := &formulaContext{
						header:  res.header,
						sheets:  userSheets(f),
						defined: definedNames(f),
						flag:    "col",
					}
					cols, colErr := parseDerivedColumns(req.colArgs, ctx)
					if colErr != nil {
						return nil, usageFailure(colErr)
					}
					req.cols = cols
				}
				if err = resolveFilters(req.filters, res.header, req.cols); err != nil {
					return nil, err
				}
			}
			continue
		}
		cells, err := iter.Columns()
		if err != nil {
			return nil, err
		}
		cells = applyMerges(pos, cells)
		res.scanned++
		if req.calc {
			var warnings []string
			if cells, warnings = fillFormulas(f, req.sheet, pos, cells); len(warnings) > 0 {
				res.warnings = append(res.warnings, warnings...)
			}
		}
		// Dates are rewritten before anything reads the row, so that a filter
		// compares the values the caller will see rather than the ones the file
		// happened to display.
		if dates != nil {
			cells = dates.apply(req.sheet, pos, cells, stored)
		}
		// Derived columns are computed before anything reads the row, so that
		// --where compares the value the caller named rather than the formula
		// that produces it.
		rowDerived, err := req.cols.eval(engine, req.sheet, pos, cells)
		if err != nil {
			// A formula that cannot work at all — an unsupported function, a
			// misspelled column — ends the scan with its own message instead of
			// being repeated for every row left in the sheet.
			return nil, usageFailure(err)
		}
		if len(res.rows) == req.limit {
			// The page is complete. Keep going, but only to establish whether
			// another matching row exists, and give up after a bounded number
			// of rows so a filter matching nothing further cannot scan the
			// whole worksheet.
			probe++
			if probe > lookaheadCap {
				res.hasMore, res.approximate = true, true
				break
			}
		}
		if len(req.filters) > 0 && !matchFilters(req.filters, cells, rowDerived) {
			continue
		}
		// A sparse worksheet yields a position for every row number up to its
		// last row, so a sheet whose data starts at row 19 would otherwise
		// begin with 18 blank rows and one stray formatted row at the bottom
		// would pad every page out to a million.
		if req.skipEmpty && !nonEmpty(cells) {
			continue
		}
		if skipped < req.offset {
			skipped++
			continue
		}
		if len(res.rows) >= req.limit {
			// The page is full, so what remains is decided here. Whether there
			// is more to fetch is a question about content rather than about
			// position: a worksheet's last rows are routinely formatted and
			// empty (a stray border, a fill, a validation list dragged down),
			// and treating position as the answer made a caller page through
			// blank pages that never ended, with complete:false withheld for
			// rows that hold nothing.
			//
			// A blank row does not end the scan outright — real sheets have
			// gaps in the middle — it just does not count as more to fetch. The
			// bounded lookahead above still applies, so a gap deeper than the
			// cap reports has_more_approximate rather than scanning forever.
			if !nonEmpty(cells) {
				continue
			}
			res.hasMore = true
			break
		}
		if len(res.rows) == 0 {
			res.firstRow = pos
		}
		res.lastRow = pos
		res.rows = append(res.rows, cells)
		res.derived = append(res.derived, rowDerived)
	}
	if err = iter.Error(); err != nil {
		return nil, err
	}
	// A filter that answered in text something the caller asked in numbers says
	// so here, once, rather than in the result set where it cannot be seen.
	res.warnings = append(res.warnings, filterWarnings(req.filters)...)
	res.warnings = append(res.warnings, req.cols.warnings()...)

	// Pad every row to a common width so the page has a stable shape. The
	// width comes from the rows actually returned rather than from the
	// worksheet dimension, because a sheet carrying an oversized dimension
	// (whole-column formatting, for example) would otherwise pad every row out
	// to thousands of columns.
	width := len(res.header)
	for _, row := range res.rows {
		if len(row) > width {
			width = len(row)
		}
	}
	if req.maxColumns > 0 && width > req.maxColumns {
		width, res.widthCapped = req.maxColumns, true
	}
	// The derived columns go after the sheet's own, at a position that does not
	// depend on the width of any individual row: a row is padded to the page
	// width first and the derived values follow it, so every row in the page has
	// them in the same place. --max-columns bounds what is read out of the
	// sheet; a column the caller asked to have computed is added on top rather
	// than capped away.
	if req.cols != nil {
		res.derivedNames = req.cols.names()
		res.header = padRow(res.header, width)
		res.header = append(res.header, res.derivedNames...)
		for i := range res.rows {
			res.rows[i] = append(padRow(res.rows[i], width), res.derived[i]...)
		}
		width += len(req.cols.cols)
	} else {
		for i := range res.rows {
			res.rows[i] = padRow(res.rows[i], width)
		}
	}
	res.width = width
	if res.widthCapped {
		res.warnings = append(res.warnings, fmt.Sprintf(
			"page is wider than --max-columns=%d; columns beyond %s were dropped",
			req.maxColumns, columnLetter(req.maxColumns)))
	}
	return res, nil
}

// fillFormulas evaluates formula cells whose cached value is empty, which is
// what a workbook looks like when it was written by a tool that never saved a
// computed result.
//
// Resolving a formula makes excelize load the whole worksheet into memory, so
// this is strictly opt-in via --calc and is only consulted for cells that came
// back empty. The returned slice is the input slice unless something was
// actually computed.
func fillFormulas(f *excelize.File, sheet string, rowNum int, cells []string) ([]string, []string) {
	if len(cells) == 0 {
		return cells, nil
	}
	var (
		out      []string
		warnings []string
	)
	for i, value := range cells {
		if value != "" {
			continue
		}
		ref, err := excelize.CoordinatesToCellName(i+1, rowNum)
		if err != nil {
			continue
		}
		formula, err := f.GetCellFormula(sheet, ref)
		if err != nil || formula == "" {
			continue
		}
		computed, err := f.CalcCellValue(sheet, ref)
		if err != nil {
			if len(warnings) < maxWarnings {
				warnings = append(warnings, fmt.Sprintf("%s: %v", ref, err))
			}
			continue
		}
		if out == nil {
			out = make([]string, len(cells))
			copy(out, cells)
		}
		out[i] = computed
	}
	if out == nil {
		return cells, warnings
	}
	return out, warnings
}
