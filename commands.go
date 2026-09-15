// Copyright (c) 2026 henryyu@163.com. All rights reserved.
//
// This file is part of xlpeek, a read-only spreadsheet reader for
// agents. See README.md for what it does and docs/AGENTS.md for how it is
// meant to be driven.

package main

import (
	"fmt"
	"os"
	"regexp"
	"strings"

	"github.com/xuri/excelize/v2"
)

// stringSlice collects a repeatable flag such as --where.
type stringSlice []string

func (s *stringSlice) String() string { return strings.Join(*s, ", ") }

func (s *stringSlice) Set(value string) error {
	*s = append(*s, value)
	return nil
}

// splitList parses a comma-separated flag value, ignoring empty entries so
// that trailing commas and stray spaces are harmless.
func splitList(s string) []string {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		if trimmed := strings.TrimSpace(part); trimmed != "" {
			out = append(out, trimmed)
		}
	}
	return out
}

func valueAt(row []string, idx int) string {
	if idx >= 0 && idx < len(row) {
		return row[idx]
	}
	return ""
}

// ---------------------------------------------------------------------------
// info
// ---------------------------------------------------------------------------

type infoResult struct {
	File       string      `json:"file"`
	FileSize   int64       `json:"file_size"`
	SheetCount int         `json:"sheet_count"`
	Sheets     []sheetInfo `json:"sheets"`
}

type sheetInfo struct {
	Index         int        `json:"index"`
	ID            int        `json:"id"`
	Name          string     `json:"name"`
	Visible       *bool      `json:"visible,omitempty"`
	Dimension     string     `json:"dimension,omitempty"`
	MaxRow        int        `json:"max_row,omitempty"`
	MaxColumn     int        `json:"max_column,omitempty"`
	ColumnNames   []string   `json:"column_names,omitempty"`
	Header        []string   `json:"header,omitempty"`
	HeaderKeys    []string   `json:"header_keys,omitempty"`
	SampleRows    [][]string `json:"sample_rows,omitempty"`
	ColumnsCapped bool       `json:"columns_capped,omitempty"`
	Scanned       bool       `json:"scanned,omitempty"`
	LastRow       int        `json:"last_row,omitempty"`
	PopulatedRows int        `json:"populated_rows,omitempty"`
	MergedRanges  int        `json:"merged_ranges,omitempty"`
	MergedSample  []string   `json:"merged_sample,omitempty"`
	ScanError     string     `json:"scan_error,omitempty"`
}

// cmdInfo summarises a workbook so a caller can decide which sheet to read and
// how to page through it, without reading any data rows it does not need.
func cmdInfo(args []string) int {
	fs := newFlagSet("info", "info <file.xlsx> [flags]")
	var sheet string
	fs.StringVar(&sheet, "sheet", "", "restrict the report to one sheet")
	fs.StringVar(&sheet, "s", "", "restrict the report to one sheet (shorthand)")
	headerRow := fs.Int("header-row", 1, "worksheet row holding the column names; 0 means there is no header")
	sample := fs.Int("sample", 3, "data rows to preview after the header; 0 disables")
	deep := fs.Bool("deep", false, "walk every row to report exact counts; slower on large files")
	maxColumns := fs.Int("max-columns", defaultMaxColumns, "cap on the number of columns reported")
	password := fs.String("password", "", "password for an encrypted workbook")
	raw := fs.Bool("raw", false, "report stored values instead of number-formatted values")
	tmpDir := fs.String("tmpdir", "", "directory for temporary files (default: system temp)")
	pretty := fs.Bool("pretty", false, "indent the JSON output")

	operands, code, ok := parseFlags(fs, args)
	if !ok {
		return code
	}
	if len(operands) != 1 {
		return failUsage("info", "expected exactly one workbook path")
	}
	if *sample < 0 {
		return failUsage("info", "--sample must not be negative")
	}
	if *maxColumns < 1 {
		return failUsage("info", "--max-columns must be positive")
	}
	if *headerRow < 0 {
		return failUsage("info", "--header-row must not be negative")
	}

	path := operands[0]
	f, err := openWorkbook(path, workbookOptions{password: *password, raw: *raw, tmpDir: *tmpDir})
	if err != nil {
		return fail("info", err, *pretty)
	}
	defer releaseWorkbook(f)

	// Sheets is pre-allocated so that a workbook with none reports an empty
	// list rather than null: a caller iterating the field should not have to
	// guard against a missing array.
	result := &infoResult{
		File:       path,
		SheetCount: len(f.GetSheetList()),
		Sheets:     make([]sheetInfo, 0),
	}
	if stat, statErr := os.Stat(path); statErr == nil {
		result.FileSize = stat.Size()
	}

	ids := f.GetSheetMap()
	idByName := make(map[string]int, len(ids))
	for id, name := range ids {
		idByName[name] = id
	}

	// Report either every sheet or just the requested one, but always keep the
	// workbook-wide index so that the numbers stay comparable between calls.
	all := f.GetSheetList()
	targets := make([]int, 0, len(all))
	if sheet != "" {
		_, index, resolveErr := resolveSheet(f, sheet)
		if resolveErr != nil {
			return failWithSheet("info", resolveErr, all, *pretty)
		}
		targets = append(targets, index)
	} else {
		for i := range all {
			targets = append(targets, i)
		}
	}
	for _, index := range targets {
		name := all[index]
		result.Sheets = append(result.Sheets, describeSheet(f, name, index, idByName[name],
			*headerRow, *sample, *deep, *maxColumns))
	}
	return respondOK("info", result, *pretty)
}

// describeSheet gathers what is cheap about one sheet, and a little more when
// the caller asked for a deep scan. A sheet that cannot be read as a
// worksheet (a chartsheet, for example) records the failure and the remaining
// sheets are still reported.
func describeSheet(f *excelize.File, name string, index, id, headerRow, sampleRows int, deep bool, maxColumns int) sheetInfo {
	info := sheetInfo{Index: index, ID: id, Name: name}

	// Visibility is a pointer so that "could not be determined" stays distinct
	// from "hidden" — a caller that reads a false default would skip a sheet
	// that is merely unreadable.
	if visible, err := f.GetSheetVisible(name); err == nil {
		info.Visible = &visible
	}
	if dimension, err := f.GetSheetDimension(name); err == nil {
		info.Dimension = dimension
		info.MaxRow, info.MaxColumn = parseDimension(dimension)
	}

	scan, err := scanSheet(f, name, headerRow, sampleRows, deep)
	if err != nil {
		info.ScanError = err.Error()
		return info
	}

	width := len(scan.header)
	if scan.maxColumn > width {
		width = scan.maxColumn
	}
	if maxColumns > 0 && width > maxColumns {
		width, info.ColumnsCapped = maxColumns, true
	}

	info.Header = scan.header
	info.HeaderKeys = headerKeys(scan.header, width)
	info.ColumnNames = make([]string, width)
	for i := range info.ColumnNames {
		info.ColumnNames[i] = columnLetter(i + 1)
	}
	info.SampleRows = make([][]string, 0, len(scan.sample))
	for _, row := range scan.sample {
		info.SampleRows = append(info.SampleRows, padRow(row, width))
	}
	if scan.deep {
		info.Scanned = true
		info.LastRow = scan.lastRow
		info.PopulatedRows = scan.populatedRows
		// Merged regions are a correctness hazard for a reader that does not
		// know about them, so a deep scan surfaces them. It is kept out of the
		// shallow path because reading them parses the whole worksheet.
		if ranges, mergeErr := loadMergeRanges(f, name); mergeErr == nil && len(ranges) > 0 {
			info.MergedRanges = len(ranges)
			for i, r := range ranges {
				if i == 5 {
					break
				}
				info.MergedSample = append(info.MergedSample, r.ref)
			}
		}
	}
	return info
}

// parseDimension turns a dimension reference such as "A1:F100" into row and
// column counts. It is a hint from the file, not a measurement.
func parseDimension(ref string) (rows, cols int) {
	if ref == "" {
		return 0, 0
	}
	parts := strings.Split(ref, ":")
	last := strings.ReplaceAll(parts[len(parts)-1], "$", "")
	col, row, err := excelize.CellNameToCoordinates(last)
	if err != nil {
		return 0, 0
	}
	return row, col
}

// ---------------------------------------------------------------------------
// read
// ---------------------------------------------------------------------------

type readResult struct {
	File         string   `json:"file"`
	Sheet        string   `json:"sheet"`
	SheetIndex   int      `json:"sheet_index"`
	HeaderMode   bool     `json:"header_mode"`
	HeaderRow    int      `json:"header_row,omitempty"`
	SkipEmpty    bool     `json:"skip_empty,omitempty"`
	FillMerged   bool     `json:"fill_merged,omitempty"`
	Header       []string `json:"header,omitempty"`
	Columns      []string `json:"columns"`
	Format       string   `json:"format,omitempty"`
	Offset       int      `json:"offset"`
	Limit        int      `json:"limit"`
	RowsReturned int      `json:"rows_returned"`
	FirstRow     int      `json:"first_row,omitempty"`
	LastRow      int      `json:"last_row,omitempty"`
	// Complete states plainly whether this page is the whole answer. A caller
	// that has to invert has_more to work that out may not bother, and then
	// reports a page as if it were the table.
	Complete      bool        `json:"complete"`
	HasMore       bool        `json:"has_more"`
	HasMoreApprox bool        `json:"has_more_approximate,omitempty"`
	NextOffset    *int        `json:"next_offset,omitempty"`
	RowsScanned   int         `json:"rows_scanned"`
	Rows          interface{} `json:"rows"`
	WarningCount  int         `json:"warning_count"`
	Warnings      []string    `json:"warnings,omitempty"`
}

func cmdRead(args []string) int {
	fs := newFlagSet("read", "read <file.xlsx> [flags]")
	var sheet, columns string
	fs.StringVar(&sheet, "sheet", "", "worksheet name; defaults to the first sheet")
	fs.StringVar(&sheet, "s", "", "worksheet name (shorthand)")
	fs.StringVar(&columns, "columns", "", "comma-separated columns to project, by header name, letter or index")
	header := fs.Bool("header", false, "treat row 1 as the header and return each row as an object")
	headerRow := fs.Int("header-row", 0, "worksheet row holding the column names; overrides --header")
	offset := fs.Int("offset", 0, "rows to skip; counts returned rows, so it always walks forward from the start")
	fs.IntVar(offset, "o", 0, "rows to skip (shorthand)")
	limit := fs.Int("limit", defaultLimit, "maximum rows to return")
	fs.IntVar(limit, "l", defaultLimit, "maximum rows to return (shorthand)")
	skipEmpty := fs.Bool("skip-empty", false, "drop rows whose cells are all empty; sparse sheets otherwise pad every page with blanks")
	calc := fs.Bool("calc", false, "evaluate formulas that have no cached value; loads the sheet into memory")
	fillMerged := fs.Bool("fill-merged", false, "copy each merged region's value into every cell it spans; loads the sheet into memory")
	format := fs.String("format", "json", "output format: json, or tsv for a header line plus tab-separated rows after the envelope")
	maxColumns := fs.Int("max-columns", defaultMaxColumns, "cap on the number of columns returned")
	password := fs.String("password", "", "password for an encrypted workbook")
	raw := fs.Bool("raw", false, "return stored values instead of number-formatted values")
	tmpDir := fs.String("tmpdir", "", "directory for temporary files (default: system temp)")
	pretty := fs.Bool("pretty", false, "indent the JSON output")
	var wheres stringSlice
	fs.Var(&wheres, "where", "row filter, repeatable and ANDed, e.g. --where \"金额>1000\" or --where \"状态~完成\"")

	operands, code, ok := parseFlags(fs, args)
	if !ok {
		return code
	}
	if len(operands) != 1 {
		return failUsage("read", "expected exactly one workbook path")
	}
	if *offset < 0 {
		return failUsage("read", "--offset must not be negative")
	}
	if *limit < 1 || *limit > maxLimit {
		return failUsage("read", fmt.Sprintf("--limit must be between 1 and %d", maxLimit))
	}
	if *maxColumns < 1 {
		return failUsage("read", "--max-columns must be positive")
	}
	if *headerRow < 0 {
		return failUsage("read", "--header-row must not be negative")
	}
	if *format != "json" && *format != "tsv" {
		return failUsage("read", fmt.Sprintf("--format must be json or tsv, got %q", *format))
	}
	// --header is shorthand for --header-row 1; an explicit row wins, which is
	// what a caller wants for a sheet whose table starts below a title block.
	headerAt := 0
	if *header {
		headerAt = 1
	}
	if *headerRow > 0 {
		headerAt = *headerRow
	}
	filters, err := parseFilterList(wheres)
	if err != nil {
		return failUsage("read", err.Error())
	}

	path := operands[0]
	f, err := openWorkbook(path, workbookOptions{password: *password, raw: *raw, tmpDir: *tmpDir})
	if err != nil {
		return fail("read", err, *pretty)
	}
	defer releaseWorkbook(f)

	sheetName, sheetIndex, err := resolveSheet(f, sheet)
	if err != nil {
		return failWithSheet("read", err, f.GetSheetList(), *pretty)
	}

	res, err := readPage(f, pageRequest{
		sheet:      sheetName,
		sheetIndex: sheetIndex,
		headerRow:  headerAt,
		offset:     *offset,
		limit:      *limit,
		filters:    filters,
		skipEmpty:  *skipEmpty,
		calc:       *calc,
		fillMerged: *fillMerged,
		maxColumns: *maxColumns,
	})
	if err != nil {
		return failWithSheet("read", err, f.GetSheetList(), *pretty)
	}

	projection, err := buildProjection(columns, res.header, res.width)
	if err != nil {
		return respondErr("read", codeColumnNotFound, err.Error(), *pretty, exitError)
	}

	keys := headerKeys(res.header, res.width)
	outColumns := make([]string, 0, len(projection))
	for _, idx := range projection {
		outColumns = append(outColumns, columnLetter(idx+1))
	}
	// The names go with the projected columns, not the sheet's full width, so
	// that an object row and a TSV header line agree on the same fields. They
	// are deduplicated because projecting the same column twice would otherwise
	// emit the key twice in one object, and a parser keeps only the last copy.
	projectedNames := make([]string, 0, len(projection))
	for _, idx := range projection {
		projectedNames = append(projectedNames, valueAt(keys, idx))
	}
	outNames := headerKeys(projectedNames, len(projectedNames))
	projected := projectRows(res.rows, projection)

	result := &readResult{
		File:          path,
		Sheet:         res.sheet,
		SheetIndex:    res.sheetIndex,
		HeaderMode:    headerAt > 0,
		HeaderRow:     headerAt,
		SkipEmpty:     *skipEmpty,
		FillMerged:    *fillMerged,
		Header:        res.header,
		Columns:       outColumns,
		Offset:        *offset,
		Limit:         *limit,
		RowsReturned:  len(res.rows),
		FirstRow:      res.firstRow,
		LastRow:       res.lastRow,
		HasMore:       res.hasMore,
		HasMoreApprox: res.approximate,
		RowsScanned:   res.scanned,
		Warnings:      res.warnings,
		Format:        *format,
		Rows:          buildRows(projected, outNames, headerAt > 0),
	}
	if res.hasMore {
		next := *offset + len(res.rows)
		result.NextOffset = &next
	}
	result.Complete = !res.hasMore
	result.WarningCount = len(res.warnings)
	if *format == "tsv" {
		// The rows move to the body, so the envelope carries only the metadata.
		// A caller still reads line 1 as JSON and follows next_offset from it.
		result.Rows = nil
		return respondTSV("read", result, buildTSV(outNames, projected), *pretty)
	}
	return respondOK("read", result, *pretty)
}

// buildProjection resolves the --columns value into 0-based column indexes.
// With no projection every column of the page is returned.
func buildProjection(columns string, header []string, width int) ([]int, error) {
	requested := splitList(columns)
	if len(requested) == 0 {
		projection := make([]int, 0, width)
		for i := 0; i < width; i++ {
			projection = append(projection, i)
		}
		return projection, nil
	}
	projection := make([]int, 0, len(requested))
	for _, name := range requested {
		idx, err := resolveColumn(name, header)
		if err != nil {
			return nil, err
		}
		projection = append(projection, idx)
	}
	return projection, nil
}

// projectRows selects and orders the requested columns.
func projectRows(rows [][]string, projection []int) [][]string {
	out := make([][]string, 0, len(rows))
	for _, row := range rows {
		values := make([]string, 0, len(projection))
		for _, idx := range projection {
			values = append(values, valueAt(row, idx))
		}
		out = append(out, values)
	}
	return out
}

// buildRows renders already-projected rows, either as arrays or, in header
// mode, as objects keyed by column name.
func buildRows(rows [][]string, names []string, headerMode bool) interface{} {
	if !headerMode {
		return rows
	}
	out := make([]orderedRow, 0, len(rows))
	for _, row := range rows {
		ordered := orderedRow{
			keys: make([]string, 0, len(row)),
			vals: make([]any, 0, len(row)),
		}
		for i, value := range row {
			ordered.keys = append(ordered.keys, valueAt(names, i))
			ordered.vals = append(ordered.vals, value)
		}
		out = append(out, ordered)
	}
	return out
}

// ---------------------------------------------------------------------------
// find
// ---------------------------------------------------------------------------

type findResult struct {
	File       string   `json:"file"`
	Sheet      string   `json:"sheet"`
	SheetIndex int      `json:"sheet_index"`
	Pattern    string   `json:"pattern"`
	UseRegex   bool     `json:"regex"`
	IgnoreCase bool     `json:"ignore_case"`
	Header     []string `json:"header,omitempty"`
	MatchCount int      `json:"match_count"`
	Truncated  bool     `json:"truncated"`
	// Complete is the plain-language counterpart of Truncated: these are all
	// the matches there are.
	Complete    bool        `json:"complete"`
	RowsScanned int         `json:"rows_scanned"`
	Matches     []findMatch `json:"matches"`
}

type findMatch struct {
	Cell        string   `json:"cell"`
	Row         int      `json:"row"`
	Column      string   `json:"column"`
	ColumnIndex int      `json:"column_index"`
	ColumnName  string   `json:"column_name,omitempty"`
	Value       string   `json:"value"`
	RowValues   []string `json:"row_values,omitempty"`
}

func cmdFind(args []string) int {
	fs := newFlagSet("find", "find <file.xlsx> --value <text> [flags]")
	var sheet, column, needle string
	fs.StringVar(&sheet, "sheet", "", "worksheet name; defaults to the first sheet")
	fs.StringVar(&sheet, "s", "", "worksheet name (shorthand)")
	fs.StringVar(&needle, "value", "", "text or pattern to look for (required)")
	fs.StringVar(&needle, "v", "", "text or pattern to look for (shorthand)")
	fs.StringVar(&column, "column", "", "restrict the search to one column")
	useRegex := fs.Bool("regex", false, "treat --value as a regular expression")
	ignoreCase := fs.Bool("ignore-case", true, "match case-insensitively")
	fs.BoolVar(ignoreCase, "i", true, "match case-insensitively (shorthand)")
	headerMode := fs.Bool("header", false, "treat row 1 as the header; searches rows below it and labels matches")
	headerRowFlag := fs.Int("header-row", 0, "worksheet row holding the column names; overrides --header")
	limit := fs.Int("limit", 50, "maximum matches to return")
	fs.IntVar(limit, "l", 50, "maximum matches to return (shorthand)")
	maxScan := fs.Int("max-scan", 0, "give up after this many rows; 0 means no limit")
	maxColumns := fs.Int("max-columns", defaultMaxColumns, "cap on the number of columns in row context")
	password := fs.String("password", "", "password for an encrypted workbook")
	raw := fs.Bool("raw", false, "search stored values instead of number-formatted values")
	tmpDir := fs.String("tmpdir", "", "directory for temporary files (default: system temp)")
	pretty := fs.Bool("pretty", false, "indent the JSON output")

	operands, code, ok := parseFlags(fs, args)
	if !ok {
		return code
	}
	if len(operands) != 1 {
		return failUsage("find", "expected exactly one workbook path")
	}
	if needle == "" {
		return failUsage("find", "--value must not be empty")
	}
	if *limit < 1 || *limit > maxLimit {
		return failUsage("find", fmt.Sprintf("--limit must be between 1 and %d", maxLimit))
	}
	if *maxScan < 0 {
		return failUsage("find", "--max-scan must not be negative")
	}

	var re *regexp.Regexp
	if *useRegex {
		expr := needle
		if *ignoreCase {
			expr = "(?i)" + expr
		}
		compiled, compileErr := regexp.Compile(expr)
		if compileErr != nil {
			return failUsage("find", fmt.Sprintf("invalid regular expression: %v", compileErr))
		}
		re = compiled
	}
	lowered := strings.ToLower(needle)
	matches := func(cell string) bool {
		switch {
		case re != nil:
			return re.MatchString(cell)
		case *ignoreCase:
			return strings.Contains(strings.ToLower(cell), lowered)
		default:
			return strings.Contains(cell, needle)
		}
	}

	path := operands[0]
	f, err := openWorkbook(path, workbookOptions{password: *password, raw: *raw, tmpDir: *tmpDir})
	if err != nil {
		return fail("find", err, *pretty)
	}
	defer releaseWorkbook(f)

	sheetName, sheetIndex, err := resolveSheet(f, sheet)
	if err != nil {
		return failWithSheet("find", err, f.GetSheetList(), *pretty)
	}

	iter, err := f.Rows(sheetName)
	if err != nil {
		return failWithSheet("find", err, f.GetSheetList(), *pretty)
	}
	defer func() { _ = iter.Close() }()

	result := &findResult{
		File:       path,
		Sheet:      sheetName,
		SheetIndex: sheetIndex,
		Pattern:    needle,
		UseRegex:   *useRegex,
		IgnoreCase: *ignoreCase,
		Matches:    make([]findMatch, 0, *limit),
	}
	headerAt := 0
	if *headerMode {
		headerAt = 1
	}
	if *headerRowFlag > 0 {
		headerAt = *headerRowFlag
	}
	firstDataRow := 1
	if headerAt > 0 {
		firstDataRow = headerAt + 1
	}
	columnIndex := -1
	if column != "" && headerAt == 0 {
		if columnIndex, err = resolveColumn(column, nil); err != nil {
			return respondErr("find", codeColumnNotFound, err.Error(), *pretty, exitError)
		}
	}

	var (
		pos  int
		keys []string
	)
scan:
	for iter.Next() {
		pos++
		if pos < firstDataRow {
			if headerAt > 0 && pos == headerAt {
				if result.Header, err = iter.Columns(); err != nil {
					return failWithSheet("find", err, f.GetSheetList(), *pretty)
				}
				if column != "" {
					if columnIndex, err = resolveColumn(column, result.Header); err != nil {
						return respondErr("find", codeColumnNotFound, err.Error(), *pretty, exitError)
					}
				}
			}
			continue
		}
		cells, err := iter.Columns()
		if err != nil {
			return failWithSheet("find", err, f.GetSheetList(), *pretty)
		}
		// Stop before searching a row that would exceed the budget, so the
		// reported rows_scanned equals --max-scan exactly rather than
		// overshooting by one.
		if *maxScan > 0 && result.RowsScanned >= *maxScan {
			result.Truncated = true
			break
		}
		result.RowsScanned++
		// Pad the context row to at least the header width so that a match
		// lines up with the same columns the read command would report;
		// otherwise a row whose trailing cells are empty comes back narrower
		// than the header and the two commands disagree about column order.
		width := len(cells)
		if len(result.Header) > width {
			width = len(result.Header)
		}
		if *maxColumns > 0 && width > *maxColumns {
			width = *maxColumns
		}
		if keys == nil {
			keys = headerKeys(result.Header, width)
		}
		first, last := 0, width-1
		if columnIndex >= 0 {
			if columnIndex >= len(cells) {
				continue
			}
			first, last = columnIndex, columnIndex
		}
		for i := first; i <= last; i++ {
			if i >= len(cells) || !matches(cells[i]) {
				continue
			}
			ref, coordErr := excelize.CoordinatesToCellName(i+1, pos)
			if coordErr != nil {
				continue
			}
			match := findMatch{
				Cell:        ref,
				Row:         pos,
				Column:      columnLetter(i + 1),
				ColumnIndex: i + 1,
				Value:       cells[i],
				RowValues:   padRow(cells, width),
			}
			if headerAt > 0 && i < len(keys) {
				match.ColumnName = keys[i]
			}
			result.Matches = append(result.Matches, match)
			if len(result.Matches) >= *limit {
				result.Truncated = true
				break scan
			}
		}
	}
	if err = iter.Error(); err != nil {
		return failWithSheet("find", err, f.GetSheetList(), *pretty)
	}
	// In header mode the column can only be resolved once row 1 has been seen.
	// If the sheet had no rows at all the reference was never bound, and
	// silently searching every column would be worse than saying so.
	if column != "" && columnIndex < 0 {
		return respondErr("find", codeColumnNotFound,
			fmt.Sprintf("column %q could not be resolved because the sheet has no header row", column),
			*pretty, exitError)
	}
	result.MatchCount = len(result.Matches)
	result.Complete = !result.Truncated
	return respondOK("find", result, *pretty)
}
