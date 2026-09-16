// Copyright (c) 2026 henryyu@163.com. All rights reserved.
//
// This file is part of xlpeek, a read-only spreadsheet reader for
// agents. See README.md for what it does and docs/AGENTS.md for how it is
// meant to be driven.

package main

import (
	"fmt"
	"sort"
)

// Tracking cap for distinct values, and the share of the budget one column may
// take. A profile walks every row, so the accumulators are the only thing that
// grows with the data; the budget is what keeps a sheet with a million distinct
// customer names from turning a summary into a memory problem.
const (
	distinctCap   = 20000
	distinctTotal = 500000
)

// Cell type labels reported per column.
const (
	typeNumber = "number"
	typeDate   = "date"
	typeText   = "text"
	typeEmpty  = "empty"
	typeMixed  = "mixed"
)

type valueCount struct {
	Value string `json:"value"`
	Count int    `json:"count"`
}

type columnProfile struct {
	Index          int          `json:"index"`
	Column         string       `json:"column"`
	Name           string       `json:"name,omitempty"`
	Type           string       `json:"type"`
	NonEmpty       int          `json:"non_empty"`
	Empty          int          `json:"empty"`
	FillRate       float64      `json:"fill_rate"`
	Distinct       int          `json:"distinct"`
	DistinctCapped bool         `json:"distinct_capped,omitempty"`
	Min            *float64     `json:"min,omitempty"`
	Max            *float64     `json:"max,omitempty"`
	Mean           *float64     `json:"mean,omitempty"`
	TopValues      []valueCount `json:"top_values,omitempty"`
	// ValuesOmitted explains an absent TopValues, so that "there are too many
	// to list" cannot be mistaken for "there are none".
	ValuesOmitted string `json:"values_omitted,omitempty"`
}

type profileResult struct {
	File         string          `json:"file"`
	Sheet        string          `json:"sheet"`
	SheetIndex   int             `json:"sheet_index"`
	HeaderRow    int             `json:"header_row,omitempty"`
	RowsScanned  int             `json:"rows_scanned"`
	RowsMatched  int             `json:"rows_matched"`
	Columns      []columnProfile `json:"columns"`
	WarningCount int             `json:"warning_count"`
	Warnings     []string        `json:"warnings,omitempty"`
}

type columnAccumulator struct {
	name     string
	index    int
	nonEmpty int
	// empty is deliberately not counted here. excelize drops trailing empty
	// cells, so an accumulator is only created once its column first carries a
	// value, and any row read before that would be counted as neither empty nor
	// filled. The blank count is instead derived from the number of rows the
	// scan actually matched, which every column shares.
	counts    map[string]int
	capped    bool
	numbers   int
	dates     int
	texts     int
	sum       compensatedSum
	min, max  float64
	haveRange bool
}

func newColumnAccumulator(index int, name string) *columnAccumulator {
	return &columnAccumulator{index: index, name: name, counts: map[string]int{}}
}

// observe folds one cell into the accumulator. budget is the number of distinct
// entries the whole profile may still track; once it runs out a column keeps
// counting values it already knows but stops admitting new ones, and reports
// that its distinct count is a lower bound.
func (a *columnAccumulator) observe(raw string, budget *int) {
	if raw == "" {
		return
	}
	a.nonEmpty++
	if _, known := a.counts[raw]; known {
		a.counts[raw]++
	} else if !a.capped && len(a.counts) < distinctCap && *budget > 0 {
		a.counts[raw] = 1
		*budget--
	} else {
		a.capped = true
	}

	switch {
	case looksLikeDate(raw):
		a.dates++
	default:
		if number, ok := parseNumber(raw); ok {
			a.numbers++
			a.sum.add(number)
			if !a.haveRange || number < a.min {
				a.min = number
			}
			if !a.haveRange || number > a.max {
				a.max = number
			}
			a.haveRange = true
		} else {
			a.texts++
		}
	}
}

// typeLabel classifies the column. A column counts as a type only when every
// non-empty value agrees; anything else is reported as mixed rather than being
// rounded to the majority, because a caller filtering on a numeric column
// needs to know that some rows will not parse.
func (a *columnAccumulator) typeLabel() string {
	switch {
	case a.nonEmpty == 0:
		return typeEmpty
	case a.numbers == a.nonEmpty:
		return typeNumber
	case a.dates == a.nonEmpty:
		return typeDate
	case a.texts == a.nonEmpty:
		return typeText
	default:
		return typeMixed
	}
}

// profile renders the accumulated state. rows is the number of rows the scan
// matched, which is the denominator every column shares.
func (a *columnAccumulator) profile(topValues, rows int) columnProfile {
	empty := rows - a.nonEmpty
	if empty < 0 {
		empty = 0
	}
	result := columnProfile{
		Index:          a.index,
		Column:         columnLetter(a.index + 1),
		Name:           a.name,
		Type:           a.typeLabel(),
		NonEmpty:       a.nonEmpty,
		Empty:          empty,
		Distinct:       len(a.counts),
		DistinctCapped: a.capped,
	}
	if rows > 0 {
		result.FillRate = roundToExcel(float64(a.nonEmpty) / float64(rows))
	}
	if a.haveRange {
		minimum, maximum := a.min, a.max
		mean := roundToExcel(a.sum.value() / float64(a.numbers))
		result.Min, result.Max, result.Mean = &minimum, &maximum, &mean
	}
	// Enumerate the values only while the full set fits in the report; listing
	// the top few of a hundred thousand categories tells a caller nothing.
	switch {
	case topValues <= 0:
		result.ValuesOmitted = "disabled"
	case len(a.counts) == 0:
		result.ValuesOmitted = "empty"
	case len(a.counts) > topValues:
		result.ValuesOmitted = "cardinality"
	default:
		result.TopValues = make([]valueCount, 0, len(a.counts))
		for value, count := range a.counts {
			result.TopValues = append(result.TopValues, valueCount{Value: value, Count: count})
		}
		sort.Slice(result.TopValues, func(i, j int) bool {
			if result.TopValues[i].Count != result.TopValues[j].Count {
				return result.TopValues[i].Count > result.TopValues[j].Count
			}
			return result.TopValues[i].Value < result.TopValues[j].Value
		})
	}
	return result
}

// looksLikeDate reports whether s begins with a YYYY-MM-DD or YYYY/MM/DD date.
// Date columns are worth calling out because their sort order and their
// comparability as strings both surprise callers.
func looksLikeDate(s string) bool {
	if len(s) < 10 {
		return false
	}
	for i := 0; i < 10; i++ {
		switch i {
		case 4, 7:
			if s[i] != '-' && s[i] != '/' {
				return false
			}
		default:
			if s[i] < '0' || s[i] > '9' {
				return false
			}
		}
	}
	return true
}

// cmdProfile describes every column of a sheet — its type, how densely it is
// filled, how many distinct values it holds and, when that number is small,
// what those values are. It is what lets a caller write a correct filter the
// first time instead of discovering the vocabulary by trial and error.
func cmdProfile(args []string) int {
	fs := newFlagSet("profile", "profile <file.xlsx> [flags]")
	var sheet, columns string
	fs.StringVar(&sheet, "sheet", "", "worksheet name; defaults to the first sheet")
	fs.StringVar(&sheet, "s", "", "worksheet name (shorthand)")
	fs.StringVar(&columns, "columns", "", "comma-separated columns to profile; default is all of them")
	headerFlag := fs.Bool("header", false, "treat row 1 as the header, so columns are named")
	headerRow := fs.Int("header-row", 0, "worksheet row holding the column names; overrides --header")
	skipEmpty := fs.Bool("skip-empty", false, "ignore rows whose cells are all empty")
	calc := fs.Bool("calc", false, "evaluate formulas that have no cached value; loads the sheet into memory")
	fillMerged := fs.Bool("fill-merged", false, "copy each merged region's value into every cell it spans; loads the sheet into memory")
	maxValues := fs.Int("max-values", 20, "enumerate a column's values when it has at most this many; 0 disables")
	maxColumns := fs.Int("max-columns", defaultMaxColumns, "cap on the number of columns profiled")
	password := fs.String("password", "", "password for an encrypted workbook")
	raw := fs.Bool("raw", false, "profile stored values instead of number-formatted values")
	tmpDir := fs.String("tmpdir", "", "directory for temporary files (default: system temp)")
	pretty := fs.Bool("pretty", false, "indent the JSON output")
	var wheres stringSlice
	fs.Var(&wheres, "where", "row filter applied before profiling; repeatable and ANDed")

	operands, code, ok := parseFlags(fs, args)
	if !ok {
		return code
	}
	if len(operands) != 1 {
		return failUsage("profile", "expected exactly one workbook path")
	}
	if *headerRow < 0 {
		return failUsage("profile", "--header-row must not be negative")
	}
	if *maxValues < 0 {
		return failUsage("profile", "--max-values must not be negative")
	}
	if *maxColumns < 1 {
		return failUsage("profile", "--max-columns must be positive")
	}
	filters, err := parseFilterList(wheres)
	if err != nil {
		return failUsage("profile", err.Error())
	}

	path := operands[0]
	f, err := openWorkbook(path, workbookOptions{password: *password, raw: *raw, tmpDir: *tmpDir})
	if err != nil {
		return fail("profile", err, *pretty)
	}
	defer releaseWorkbook(f)

	sheetName, sheetIndex, err := resolveSheet(f, sheet)
	if err != nil {
		return failWithSheet("profile", err, f.GetSheetList(), *pretty)
	}

	iter, err := f.Rows(sheetName)
	if err != nil {
		return failWithSheet("profile", err, f.GetSheetList(), *pretty)
	}
	defer func() { _ = iter.Close() }()

	// A merged label lives only in the top-left cell of its region, so without
	// filling it in a profile reports the label column as barely filled and the
	// rows it covers as carrying nothing.
	var merges *mergeFiller
	if *fillMerged {
		if merges, err = newMergeFiller(f, sheetName); err != nil {
			return failWithSheet("profile", err, f.GetSheetList(), *pretty)
		}
	}

	headerAt := 0
	if *headerFlag {
		headerAt = 1
	}
	if *headerRow > 0 {
		headerAt = *headerRow
	}
	firstDataRow := 1
	if headerAt > 0 {
		firstDataRow = headerAt + 1
	}

	var (
		header    []string
		accums    []*columnAccumulator
		requested []int
		wanted    []string
		resolved  bool
		budget    = distinctTotal
		// Warnings raised while evaluating formulas, capped so a workbook full
		// of broken ones cannot pad the response.
		formulaWarnings []string
	)
	wanted = splitList(columns)

	// Columns are created lazily from the first row that carries them, so that
	// a sheet whose header is narrower than its data still profiles every
	// column that actually holds something.
	ensure := func(width int) {
		for len(accums) < width && len(accums) < *maxColumns {
			index := len(accums)
			name := ""
			if index < len(header) {
				name = header[index]
			}
			accums = append(accums, newColumnAccumulator(index, name))
		}
	}

	var rowsScanned, rowsMatched int
	pos := 0
	for iter.Next() {
		pos++
		if pos < firstDataRow {
			if headerAt > 0 && pos == headerAt {
				if header, err = iter.Columns(); err != nil {
					return failWithSheet("profile", err, f.GetSheetList(), *pretty)
				}
				if merges != nil {
					header = merges.apply(pos, header, *maxColumns)
				}
				// Create a column for every named header, so that a column that
				// is empty throughout is still reported as empty rather than
				// vanishing from the profile.
				ensure(len(header))
			}
			continue
		}
		cells, err := iter.Columns()
		if err != nil {
			return failWithSheet("profile", err, f.GetSheetList(), *pretty)
		}
		if merges != nil {
			cells = merges.apply(pos, cells, *maxColumns)
		}
		// A formula with no cached result reads as empty, which would type its
		// column "empty" and hide it from the profile entirely; resolving one
		// makes excelize load the whole worksheet, so it is opt-in.
		if *calc {
			var warnings []string
			cells, warnings = fillFormulas(f, sheetName, pos, cells)
			for _, warning := range warnings {
				if len(formulaWarnings) < maxWarnings {
					formulaWarnings = append(formulaWarnings, warning)
				}
			}
		}
		if !resolved {
			if err = resolveFilters(filters, header); err != nil {
				return respondErr("profile", codeColumnNotFound, err.Error(), *pretty, exitUsage)
			}
			for _, name := range wanted {
				idx, err := resolveColumn(name, header)
				if err != nil {
					return respondErr("profile", codeColumnNotFound, err.Error(), *pretty, exitUsage)
				}
				requested = append(requested, idx)
			}
			resolved = true
		}
		rowsScanned++
		if len(filters) > 0 && !matchFilters(filters, cells) {
			continue
		}
		if *skipEmpty && !nonEmpty(cells) {
			continue
		}
		rowsMatched++
		ensure(len(cells))
		for i, accumulator := range accums {
			accumulator.observe(valueAt(cells, i), &budget)
		}
	}
	if err = iter.Error(); err != nil {
		return failWithSheet("profile", err, f.GetSheetList(), *pretty)
	}
	if !resolved {
		// An empty sheet still has to resolve, or a bad column name would look
		// like an empty result instead of an error.
		if err = resolveFilters(filters, header); err != nil {
			return respondErr("profile", codeColumnNotFound, err.Error(), *pretty, exitUsage)
		}
		for _, name := range wanted {
			idx, err := resolveColumn(name, header)
			if err != nil {
				return respondErr("profile", codeColumnNotFound, err.Error(), *pretty, exitUsage)
			}
			requested = append(requested, idx)
		}
		ensure(len(header))
	}

	selected := accums
	if len(requested) > 0 {
		selected = make([]*columnAccumulator, 0, len(requested))
		for _, idx := range requested {
			if idx >= 0 && idx < len(accums) {
				selected = append(selected, accums[idx])
			}
		}
	}

	result := &profileResult{
		File:        path,
		Sheet:       sheetName,
		SheetIndex:  sheetIndex,
		HeaderRow:   headerAt,
		RowsScanned: rowsScanned,
		RowsMatched: rowsMatched,
		Columns:     make([]columnProfile, 0, len(selected)),
	}
	capped := false
	for _, accumulator := range selected {
		profile := accumulator.profile(*maxValues, rowsMatched)
		if profile.DistinctCapped {
			capped = true
		}
		result.Columns = append(result.Columns, profile)
	}
	result.Warnings = append(result.Warnings, formulaWarnings...)
	if capped {
		result.Warnings = append(result.Warnings, fmt.Sprintf(
			"a column had more than %d distinct values, so its distinct count is a lower bound",
			distinctCap))
	}
	if len(result.Columns) == 0 {
		result.Warnings = append(result.Warnings, "the sheet has no data rows to profile")
	}
	result.WarningCount = len(result.Warnings)
	return respondOK("profile", result, *pretty)
}
