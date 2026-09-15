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

// resolveSheet maps a possibly empty or case-mismatched sheet name onto the
// canonical name and index used by the workbook. An empty name selects the
// first sheet, which is what an agent wants when it has not looked at the
// workbook yet.
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
	return "", -1, excelize.ErrSheetNotExist{SheetName: name}
}

// resolveColumn resolves a column reference written as a header name, as a
// spreadsheet column letter (A, B, AA) or as a 1-based index. A header name
// wins over a letter so that a sheet with a column literally named "A" can
// still be addressed by name.
func resolveColumn(ref string, header []string) (int, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return -1, errors.New("empty column reference")
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
			return -1, fmt.Errorf("column index %d is 1-based and must be positive", n)
		}
		return n - 1, nil
	}
	if n, err := excelize.ColumnNameToNumber(ref); err == nil {
		return n - 1, nil
	}
	return -1, fmt.Errorf("column %q matches no header name, column letter or index", ref)
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
	// lastRow and populatedRows are only meaningful for a deep scan. A shallow
	// scan stops once it has the sample, so reporting those numbers would hand
	// a caller a row count that is really just where the scan happened to stop.
	deep          bool
	lastRow       int
	populatedRows int
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
//
// A spreadsheet stores a merged value only in the top-left cell of the region
// and leaves the rest empty, so a reader that ignores merges sees blanks
// wherever a report used a merged heading or a label spanning several rows —
// and reasons confidently from them.
type mergeRange struct {
	ref                string
	startCol, startRow int
	endCol, endRow     int
	value              string
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
	maxColumns int
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
	} else if err = resolveFilters(req.filters, nil); err != nil {
		return nil, err
	}

	var (
		pos          int
		skipped      int
		probe        int
		mergeRanges  []mergeRange
		nextRange    int
		activeMerges = make([]mergeRange, 0, 8)
	)
	if req.fillMerged {
		if mergeRanges, err = loadMergeRanges(f, req.sheet); err != nil {
			return nil, err
		}
	}
	// applyMerges drops the regions that ended before this row, admits the ones
	// starting on it, then fills their value into the cells they span. Rows
	// arrive in ascending order, so the region list is walked exactly once.
	applyMerges := func(row int, cells []string) []string {
		if len(mergeRanges) == 0 {
			return cells
		}
		alive := activeMerges[:0]
		for _, r := range activeMerges {
			if r.endRow >= row {
				alive = append(alive, r)
			}
		}
		activeMerges = alive
		for nextRange < len(mergeRanges) && mergeRanges[nextRange].startRow <= row {
			if mergeRanges[nextRange].endRow >= row {
				activeMerges = append(activeMerges, mergeRanges[nextRange])
			}
			nextRange++
		}
		if len(activeMerges) == 0 {
			return cells
		}
		return fillMergedRow(cells, activeMerges, req.maxColumns)
	}

	for iter.Next() {
		pos++
		if pos < firstDataRow {
			if req.headerRow > 0 && pos == req.headerRow {
				if res.header, err = iter.Columns(); err != nil {
					return nil, err
				}
				res.header = applyMerges(pos, res.header)
				if err = resolveFilters(req.filters, res.header); err != nil {
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
		if len(req.filters) > 0 && !matchFilters(req.filters, cells) {
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
			res.hasMore = true
			break
		}
		if len(res.rows) == 0 {
			res.firstRow = pos
		}
		res.lastRow = pos
		res.rows = append(res.rows, cells)
	}
	if err = iter.Error(); err != nil {
		return nil, err
	}

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
	res.width = width
	for i := range res.rows {
		res.rows[i] = padRow(res.rows[i], width)
	}
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
