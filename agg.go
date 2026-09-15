// Copyright (c) 2026 henryyu@163.com. All rights reserved.
//
// This file is part of xlpeek, a read-only spreadsheet reader for
// agents. See README.md for what it does and docs/AGENTS.md for how it is
// meant to be driven.

package main

import (
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"unicode"
)

// compensatedSum adds floats while tracking the rounding error of each
// addition.
//
// Plain addition loses up to one ulp per term, so summing a few thousand
// revenue figures drifts into the fifteenth significant digit — a total of
// 2,839,456,194.25 comes out as 2,839,456,194.24999. Compensation keeps the
// running total correctly rounded, so the drift never accumulates in the first
// place. This is Neumaier's variant of Kahan summation.
type compensatedSum struct {
	total float64
	error float64
}

func (c *compensatedSum) add(v float64) {
	sum := c.total + v
	if math.Abs(c.total) >= math.Abs(v) {
		c.error += (c.total - sum) + v
	} else {
		c.error += (v - sum) + c.total
	}
	c.total = sum
}

func (c *compensatedSum) value() float64 { return c.total + c.error }

// roundToExcel rounds to 15 significant digits, the precision Excel itself
// computes in. Compensation above removes accumulated drift; this removes the
// last representation artefacts of the individual inputs, so that a total
// reads as 420940492.1 rather than 420940492.1000002 and compares equal to the
// same figure read straight out of a cell.
func roundToExcel(v float64) float64 {
	if v == 0 || math.IsNaN(v) || math.IsInf(v, 0) {
		return v
	}
	rounded, err := strconv.ParseFloat(strconv.FormatFloat(v, 'g', 15, 64), 64)
	if err != nil {
		return v
	}
	return rounded
}

// Aggregate kinds. Row counting is tracked per group rather than per spec,
// because it does not read a column.
const (
	kindSum           = "sum"
	kindAvg           = "avg"
	kindMin           = "min"
	kindMax           = "max"
	kindCountDistinct = "countdistinct"
)

var aggPrefix = map[string]string{
	kindSum:           "sum_",
	kindAvg:           "avg_",
	kindMin:           "min_",
	kindMax:           "max_",
	kindCountDistinct: "distinct_",
}

type aggSpec struct {
	kind   string
	name   string // output field, e.g. sum_收入金额
	source string // the expression or column as written by the caller
	expr   exprNode
	index  int // resolved column, used by count-distinct
}

type aggState struct {
	spec     *aggSpec
	sum      compensatedSum
	seen     int
	best     float64
	haveBest bool
	distinct map[string]struct{}
}

func newAggState(spec *aggSpec) *aggState {
	state := &aggState{spec: spec}
	if spec.kind == kindCountDistinct {
		state.distinct = make(map[string]struct{})
	}
	return state
}

// add folds one row into the aggregate. present reports whether the value
// existed at all: a blank cell is skipped rather than counted as zero, or an
// average would be dragged down by every gap in the column.
func (s *aggState) add(value float64, present bool, raw string) {
	switch s.spec.kind {
	case kindSum, kindAvg:
		if present {
			s.sum.add(value)
			s.seen++
		}
	case kindMin:
		if present && (!s.haveBest || value < s.best) {
			s.best, s.haveBest = value, true
		}
	case kindMax:
		if present && (!s.haveBest || value > s.best) {
			s.best, s.haveBest = value, true
		}
	case kindCountDistinct:
		if raw != "" {
			s.distinct[raw] = struct{}{}
		}
	}
}

// result reports the aggregate and whether there was anything to aggregate. A
// group with no usable values yields no value rather than a misleading zero,
// which is also what SQL does with SUM over an empty set.
func (s *aggState) result() (float64, bool) {
	switch s.spec.kind {
	case kindSum:
		return roundToExcel(s.sum.value()), s.seen > 0
	case kindAvg:
		if s.seen == 0 {
			return 0, false
		}
		return roundToExcel(s.sum.value() / float64(s.seen)), true
	case kindMin, kindMax:
		return s.best, s.haveBest
	case kindCountDistinct:
		return float64(len(s.distinct)), true
	}
	return 0, false
}

// maxUnaccountedValues bounds the distinct-value tracking behind the
// comparability check. A column with more values than this is an identifier
// rather than a dimension, and flagging it would only train a caller to ignore
// the flag.
const maxUnaccountedValues = 20

// aggGroupWarnThreshold is where a grouping stops being a summary. Grouping by
// a near-unique column — an invoice number, a customer id — produces one output
// row per input row, which is the table again with extra steps, and the cost
// lands in the caller's context rather than in this process.
const aggGroupWarnThreshold = 1000

// hazardTokens name the kind of column that decides what a figure *means*
// rather than how much of it there is. Summing across one of these is how a
// total quietly stops being a total.
var hazardTokens = []string{"单位", "币种", "本位币", "汇率", "口径", "计量", "金额单位"}

var hazardSegments = map[string]bool{"unit": true, "currency": true, "fx": true, "rate": true}

// isHazardName reports whether a column name looks like it carries a unit,
// currency or basis.
//
// Chinese tokens are matched as substrings. English ones must match a whole
// word-like segment, so "unit" fires on "unit_price" but not on "opportunity"
// and "rate" does not fire on "corporate".
func isHazardName(name string) bool {
	trimmed := strings.TrimSpace(name)
	for _, token := range hazardTokens {
		if strings.Contains(trimmed, token) {
			return true
		}
	}
	segments := strings.FieldsFunc(strings.ToLower(trimmed), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	})
	for _, segment := range segments {
		if hazardSegments[segment] {
			return true
		}
	}
	return false
}

// unaccountedColumn tracks a column that feeds neither the grouping nor any
// aggregate, and therefore silently shapes the meaning of every number beside
// it.
type unaccountedColumn struct {
	name   string
	counts map[string]int
	capped bool
}

type unaccountedReport struct {
	Column    string   `json:"column"`
	Distinct  int      `json:"distinct"`
	Sample    []string `json:"sample"`
	Suspected bool     `json:"suspected,omitempty"`
}

// report renders the columns worth telling the caller about, most suspicious
// first. A column whose values are not all the same is a candidate; one whose
// name also reads like a unit or currency is worth a warning.
func buildUnaccountedReport(tracked map[int]*unaccountedColumn) []unaccountedReport {
	report := make([]unaccountedReport, 0, len(tracked))
	for _, column := range tracked {
		// Capped means the column had more distinct values than a dimension
		// plausibly would; it is almost certainly an identifier.
		if column.capped || len(column.counts) < 2 {
			continue
		}
		sample := make([]string, 0, len(column.counts))
		for value := range column.counts {
			sample = append(sample, value)
		}
		sort.Slice(sample, func(i, j int) bool {
			if column.counts[sample[i]] != column.counts[sample[j]] {
				return column.counts[sample[i]] > column.counts[sample[j]]
			}
			return sample[i] < sample[j]
		})
		if len(sample) > 4 {
			sample = sample[:4]
		}
		report = append(report, unaccountedReport{
			Column:    column.name,
			Distinct:  len(column.counts),
			Sample:    sample,
			Suspected: isHazardName(column.name),
		})
	}
	sort.Slice(report, func(i, j int) bool {
		if report[i].Suspected != report[j].Suspected {
			return report[i].Suspected
		}
		if report[i].Distinct != report[j].Distinct {
			return report[i].Distinct < report[j].Distinct
		}
		return report[i].Column < report[j].Column
	})
	return report
}

// groupWarning explains an unbounded grouping that has stopped summarising.
// It returns nothing when the caller capped the output themselves.
func groupWarning(groups, limit int) string {
	if limit > 0 || groups <= aggGroupWarnThreshold {
		return ""
	}
	return fmt.Sprintf(
		"%d groups were produced and no --limit was given, so every one of them is in this "+
			"response; grouping by a near-unique column returns the table rather than a "+
			"summary — set --limit, or group by something coarser",
		groups)
}

// validateUniqueFields rejects two outputs sharing a name.
//
// Duplicates would serialise as a JSON object with the same key twice, and
// every parser keeps only the last one — the result loses data while still
// reporting ok, which is exactly the kind of failure a caller cannot notice.
func validateUniqueFields(names []string) error {
	seen := make(map[string]bool, len(names))
	for _, name := range names {
		if seen[name] {
			return fmt.Errorf(
				"two outputs are both named %q; rename one with name=expression, as in --sum \"净利=收入金额\"",
				name)
		}
		seen[name] = true
	}
	return nil
}

type groupState struct {
	keys []string
	aggs []*aggState
	rows int
}

type deriveSpec struct {
	name   string
	source string
	expr   exprNode
}

type aggResult struct {
	File        string       `json:"file"`
	Sheet       string       `json:"sheet"`
	SheetIndex  int          `json:"sheet_index"`
	GroupBy     []string     `json:"group_by,omitempty"`
	Fields      []string     `json:"fields"`
	RowsScanned int          `json:"rows_scanned"`
	RowsMatched int          `json:"rows_matched"`
	GroupCount  int          `json:"group_count"`
	Offset      int          `json:"offset,omitempty"`
	Limit       int          `json:"limit,omitempty"`
	HasMore     bool         `json:"has_more"`
	NextOffset  *int         `json:"next_offset,omitempty"`
	Rows        []orderedRow `json:"rows"`
	// UnaccountedColumns lists the columns that take no part in the grouping or
	// the aggregates yet vary across the aggregated rows. They are the reason a
	// total can be arithmetically right and semantically meaningless.
	UnaccountedColumns []unaccountedReport `json:"unaccounted_columns,omitempty"`
	// Complete states plainly that these groups are the whole answer. A caller
	// that has to invert has_more to work that out may not bother.
	Complete     bool     `json:"complete"`
	WarningCount int      `json:"warning_count"`
	Warnings     []string `json:"warnings,omitempty"`
}

// cmdAgg groups rows and computes aggregates over them, so that a caller can
// ask an analytical question and get a small answer instead of paging every
// row across the wire to sum it up itself.
func cmdAgg(args []string) int {
	fs := newFlagSet("agg", "agg <file.xlsx> [flags]")
	var sheet, groupBy, sortBy string
	fs.StringVar(&sheet, "sheet", "", "worksheet name; defaults to the first sheet")
	fs.StringVar(&sheet, "s", "", "worksheet name (shorthand)")
	fs.StringVar(&groupBy, "group-by", "", "comma-separated columns to group by; omit for one grand total")
	fs.StringVar(&sortBy, "sort-by", "", "output field to sort by; anything not a group key sorts descending")
	headerFlag := fs.Bool("header", false, "treat row 1 as the header, so columns can be referred to by name")
	headerRow := fs.Int("header-row", 0, "worksheet row holding the column names; overrides --header")
	skipEmpty := fs.Bool("skip-empty", false, "drop rows whose cells are all empty")
	offset := fs.Int("offset", 0, "output groups to skip")
	fs.IntVar(offset, "o", 0, "output groups to skip (shorthand)")
	limit := fs.Int("limit", 0, "maximum output groups; 0 means all")
	fs.IntVar(limit, "l", 0, "maximum output groups (shorthand)")
	password := fs.String("password", "", "password for an encrypted workbook")
	raw := fs.Bool("raw", false, "aggregate stored values instead of number-formatted values")
	tmpDir := fs.String("tmpdir", "", "directory for temporary files (default: system temp)")
	pretty := fs.Bool("pretty", false, "indent the JSON output")
	count := fs.Bool("count", false, "include the number of matched rows per group")
	var sums, avgs, mins, maxs, distincts, deriveArgs, wheres stringSlice
	fs.Var(&sums, "sum", "sum of an expression, e.g. --sum 收入金额 or --sum \"净利=收入金额-成本金额\"; repeatable")
	fs.Var(&avgs, "avg", "average of an expression; repeatable")
	fs.Var(&mins, "min", "minimum of an expression; repeatable")
	fs.Var(&maxs, "max", "maximum of an expression; repeatable")
	fs.Var(&distincts, "count-distinct", "count of distinct non-empty values in a column; repeatable")
	fs.Var(&deriveArgs, "derive", "metric computed from aggregate fields, e.g. --derive \"毛利率=(sum_收入金额-sum_成本金额)/sum_收入金额\"; repeatable")
	fs.Var(&wheres, "where", "row filter applied before aggregating; repeatable and ANDed")

	operands, code, ok := parseFlags(fs, args)
	if !ok {
		return code
	}
	if len(operands) != 1 {
		return failUsage("agg", "expected exactly one workbook path")
	}
	if *headerRow < 0 {
		return failUsage("agg", "--header-row must not be negative")
	}
	if *offset < 0 {
		return failUsage("agg", "--offset must not be negative")
	}
	if *limit < 0 {
		return failUsage("agg", "--limit must not be negative")
	}

	// Build the aggregate list. A leading "name=" renames the output field,
	// which keeps a long expression from producing an unreadable key.
	var specs []*aggSpec
	for _, group := range []struct {
		kind  string
		items stringSlice
	}{
		{kindSum, sums}, {kindAvg, avgs}, {kindMin, mins}, {kindMax, maxs},
	} {
		for _, item := range group.items {
			name, source := item, item
			if eq := strings.IndexByte(item, '='); eq > 0 {
				name, source = strings.TrimSpace(item[:eq]), strings.TrimSpace(item[eq+1:])
			} else {
				name = aggPrefix[group.kind] + item
			}
			expr, err := parseExpr(source)
			if err != nil {
				return failUsage("agg", fmt.Sprintf("--%s %q: %v", group.kind, item, err))
			}
			specs = append(specs, &aggSpec{
				kind: group.kind, name: name, source: source, expr: expr, index: -1,
			})
		}
	}
	for _, item := range distincts {
		name, source := "distinct_"+item, item
		if eq := strings.IndexByte(item, '='); eq > 0 {
			name, source = strings.TrimSpace(item[:eq]), strings.TrimSpace(item[eq+1:])
		}
		specs = append(specs, &aggSpec{
			kind: kindCountDistinct, name: name, source: source, index: -1,
		})
	}
	if len(specs) == 0 && !*count {
		return failUsage("agg", "nothing to compute: give at least one of "+
			"--sum, --avg, --min, --max, --count-distinct or --count")
	}

	var derives []*deriveSpec
	for _, item := range deriveArgs {
		eq := strings.IndexByte(item, '=')
		if eq <= 0 {
			return failUsage("agg", fmt.Sprintf(
				"--derive %q: expected name=expression, e.g. \"毛利率=(sum_收入金额-sum_成本金额)/sum_收入金额\"", item))
		}
		name, source := strings.TrimSpace(item[:eq]), strings.TrimSpace(item[eq+1:])
		expr, err := parseExpr(source)
		if err != nil {
			return failUsage("agg", fmt.Sprintf("--derive %q: %v", item, err))
		}
		derives = append(derives, &deriveSpec{name: name, source: source, expr: expr})
	}

	// Output field order: aggregate results, then the row count, then the
	// derived metrics.
	fieldNames := make([]string, 0, len(specs)+1+len(derives))
	for _, spec := range specs {
		fieldNames = append(fieldNames, spec.name)
	}
	if *count {
		fieldNames = append(fieldNames, "count")
	}

	// A derived metric can only reference fields that were actually computed,
	// so a typo fails immediately instead of silently yielding null.
	if len(derives) > 0 {
		known := make(map[string]bool, len(fieldNames))
		for _, name := range fieldNames {
			known[name] = true
		}
		for _, d := range derives {
			used := map[string]bool{}
			identifiers(d.expr, used)
			for id := range used {
				if !known[id] {
					return failUsage("agg", fmt.Sprintf(
						"--derive %q references %q, which is not one of the computed fields: %s",
						d.source, id, strings.Join(fieldNames, ", ")))
				}
			}
		}
	}
	for _, d := range derives {
		fieldNames = append(fieldNames, d.name)
	}

	if err := validateUniqueFields(fieldNames); err != nil {
		return failUsage("agg", err.Error())
	}

	filters, err := parseFilterList(wheres)
	if err != nil {
		return failUsage("agg", err.Error())
	}

	path := operands[0]
	f, err := openWorkbook(path, workbookOptions{password: *password, raw: *raw, tmpDir: *tmpDir})
	if err != nil {
		return fail("agg", err, *pretty)
	}
	defer func() { _ = f.Close() }()

	sheetName, sheetIndex, err := resolveSheet(f, sheet)
	if err != nil {
		return failWithSheet("agg", err, f.GetSheetList(), *pretty)
	}

	iter, err := f.Rows(sheetName)
	if err != nil {
		return failWithSheet("agg", err, f.GetSheetList(), *pretty)
	}
	defer func() { _ = iter.Close() }()

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
	groupNames := splitList(groupBy)

	var (
		header      []string
		groupIdx    []int
		colIndex    = map[string]int{}
		refByName   = map[string]int{}
		refNames    []string
		refIndexes  []int
		values      []float64
		present     []bool
		groups      = map[string]*groupState{}
		order       []string
		resolved    bool
		used        = map[int]bool{}
		unaccounted = map[int]*unaccountedColumn{}
	)

	// lookup reads a referenced column for the current row. It is defined once
	// and reads the buffers that the row loop refills, so no allocation or
	// closure happens per row.
	lookup := func(name string) (float64, bool) {
		i, ok := refByName[name]
		if !ok {
			return 0, false
		}
		return values[i], present[i]
	}

	resolve := func() error {
		for _, name := range groupNames {
			idx, err := resolveColumn(name, header)
			if err != nil {
				return fmt.Errorf("--group-by %s: %w", name, err)
			}
			groupIdx = append(groupIdx, idx)
		}
		if err = resolveFilters(filters, header); err != nil {
			return err
		}
		wanted := map[string]bool{}
		for _, spec := range specs {
			if spec.expr != nil {
				identifiers(spec.expr, wanted)
			}
			if spec.kind == kindCountDistinct {
				wanted[spec.source] = true
			}
		}
		names := make([]string, 0, len(wanted))
		for name := range wanted {
			names = append(names, name)
		}
		sort.Strings(names)
		for _, name := range names {
			idx, err := resolveColumn(name, header)
			if err != nil {
				return err
			}
			colIndex[name] = idx
			refByName[name] = len(refNames)
			refNames = append(refNames, name)
			refIndexes = append(refIndexes, idx)
		}
		values = make([]float64, len(refNames))
		present = make([]bool, len(refNames))
		for _, spec := range specs {
			if spec.kind == kindCountDistinct {
				spec.index = colIndex[spec.source]
			}
		}
		// Anything a group key or an aggregate already names is accounted for.
		// Every other column is what the comparability check watches.
		for _, idx := range groupIdx {
			used[idx] = true
		}
		for _, idx := range refIndexes {
			used[idx] = true
		}
		resolved = true
		return nil
	}

	var (
		rowsScanned, rowsMatched, nonNumeric int
	)
	pos := 0
	for iter.Next() {
		pos++
		if pos < firstDataRow {
			if headerAt > 0 && pos == headerAt {
				if header, err = iter.Columns(); err != nil {
					return failWithSheet("agg", err, f.GetSheetList(), *pretty)
				}
				if err = resolve(); err != nil {
					return respondErr("agg", codeColumnNotFound, err.Error(), *pretty, exitUsage)
				}
			}
			continue
		}
		cells, err := iter.Columns()
		if err != nil {
			return failWithSheet("agg", err, f.GetSheetList(), *pretty)
		}
		if !resolved {
			// No header row: names cannot be used, but letters and indexes can.
			if err = resolve(); err != nil {
				return respondErr("agg", codeColumnNotFound, err.Error(), *pretty, exitUsage)
			}
		}
		rowsScanned++
		if len(filters) > 0 && !matchFilters(filters, cells) {
			continue
		}
		if *skipEmpty && !nonEmpty(cells) {
			continue
		}
		rowsMatched++

		// Watch the columns that shape what the aggregates mean without taking
		// part in them. This is what catches a total that quietly added 元 to
		// 千元: the tool cannot know the units differ, but it can see that the
		// column varies while the caller summed straight through it.
		for i, cell := range cells {
			if cell == "" || used[i] {
				continue
			}
			tracked, ok := unaccounted[i]
			if !ok {
				name := valueAt(header, i)
				if name == "" {
					name = columnLetter(i + 1)
				}
				tracked = &unaccountedColumn{name: name, counts: map[string]int{}}
				unaccounted[i] = tracked
			}
			if _, known := tracked.counts[cell]; known {
				tracked.counts[cell]++
			} else if len(tracked.counts) < maxUnaccountedValues {
				tracked.counts[cell] = 1
			} else {
				tracked.capped = true
			}
		}

		// Parse each referenced column once, so a cell feeding several
		// aggregates is read once and a non-numeric one is counted once.
		for i, idx := range refIndexes {
			values[i], present[i] = 0, false
			raw := valueAt(cells, idx)
			if raw == "" {
				continue
			}
			number, ok := parseNumber(raw)
			if !ok {
				nonNumeric++
				continue
			}
			values[i], present[i] = number, true
		}

		keys := make([]string, len(groupIdx))
		for i, idx := range groupIdx {
			keys[i] = valueAt(cells, idx)
		}
		groupKey := strings.Join(keys, "\x1f")
		group, seen := groups[groupKey]
		if !seen {
			group = &groupState{keys: keys, aggs: make([]*aggState, len(specs))}
			for i, spec := range specs {
				group.aggs[i] = newAggState(spec)
			}
			groups[groupKey] = group
			order = append(order, groupKey)
		}
		group.rows++
		for _, state := range group.aggs {
			if state.spec.kind == kindCountDistinct {
				state.add(0, false, valueAt(cells, state.spec.index))
				continue
			}
			value, ok := state.spec.expr.eval(lookup)
			state.add(value, ok, "")
		}
	}
	if err = iter.Error(); err != nil {
		return failWithSheet("agg", err, f.GetSheetList(), *pretty)
	}
	if !resolved {
		// A sheet with no data rows still has to resolve, or a bad column name
		// would silently produce an empty result instead of an error.
		if err = resolve(); err != nil {
			return respondErr("agg", codeColumnNotFound, err.Error(), *pretty, exitUsage)
		}
	}

	// Sort groups by their key so that the output is stable between runs even
	// though the map is not.
	sort.Strings(order)

	out := make([]orderedRow, 0, len(order))
	for _, groupKey := range order {
		group := groups[groupKey]
		row := orderedRow{
			keys: make([]string, 0, len(groupNames)+len(fieldNames)),
			vals: make([]any, 0, len(groupNames)+len(fieldNames)),
		}
		for i, name := range groupNames {
			row.keys = append(row.keys, name)
			row.vals = append(row.vals, group.keys[i])
		}
		computed := map[string]float64{}
		for _, state := range group.aggs {
			value, ok := state.result()
			row.keys = append(row.keys, state.spec.name)
			if ok {
				row.vals = append(row.vals, value)
				computed[state.spec.name] = value
			} else {
				row.vals = append(row.vals, nil)
			}
		}
		if *count {
			row.keys = append(row.keys, "count")
			row.vals = append(row.vals, group.rows)
			computed["count"] = float64(group.rows)
		}
		for _, d := range derives {
			value, ok := d.expr.eval(func(name string) (float64, bool) {
				number, found := computed[name]
				return number, found
			})
			row.keys = append(row.keys, d.name)
			if ok {
				row.vals = append(row.vals, roundToExcel(value))
			} else {
				row.vals = append(row.vals, nil)
			}
		}
		out = append(out, row)
	}

	if sortBy != "" {
		if len(out) == 0 {
			return respondErr("agg", codeUsage, fmt.Sprintf(
				"--sort-by %q: no groups were produced", sortBy), *pretty, exitUsage)
		}
		field := -1
		for i, name := range out[0].keys {
			if name == sortBy {
				field = i
				break
			}
		}
		if field < 0 {
			return respondErr("agg", codeUsage, fmt.Sprintf(
				"--sort-by %q matches no output field; available: %s",
				sortBy, strings.Join(out[0].keys, ", ")), *pretty, exitUsage)
		}
		sort.SliceStable(out, func(i, j int) bool {
			left, right := out[i].vals[field], out[j].vals[field]
			leftNumber, leftIsNumber := left.(float64)
			rightNumber, rightIsNumber := right.(float64)
			if leftIsNumber && rightIsNumber {
				return leftNumber > rightNumber // aggregates: biggest first
			}
			return fmt.Sprintf("%v", left) < fmt.Sprintf("%v", right)
		})
	}

	total := len(out)
	start := *offset
	if start > total {
		start = total
	}
	out = out[start:]
	result := &aggResult{
		File:        path,
		Sheet:       sheetName,
		SheetIndex:  sheetIndex,
		GroupBy:     groupNames,
		Fields:      fieldNames,
		RowsScanned: rowsScanned,
		RowsMatched: rowsMatched,
		GroupCount:  total,
		Offset:      *offset,
		Limit:       *limit,
		Rows:        out,
	}
	if *limit > 0 && len(out) > *limit {
		out = out[:*limit]
		result.Rows = out
		result.HasMore = true
		next := start + *limit
		result.NextOffset = &next
	}
	result.UnaccountedColumns = buildUnaccountedReport(unaccounted)
	for _, column := range result.UnaccountedColumns {
		if column.Suspected {
			// One warning, naming the most likely culprit; the full list stays
			// in the structured field. Warning about every column would just
			// teach the caller to skip the warnings.
			result.Warnings = append(result.Warnings, fmt.Sprintf(
				"column %q holds %d distinct values (%s) but takes no part in the grouping or the "+
					"aggregates; if it changes what the figures mean, this result mixes incomparable "+
					"values — add it to --group-by to see the split",
				column.Column, column.Distinct, strings.Join(column.Sample, ", ")))
			break
		}
	}
	if nonNumeric > 0 {
		result.Warnings = append(result.Warnings, fmt.Sprintf(
			"%d of %d cells could not be read as numbers and were skipped",
			nonNumeric, rowsMatched*max(1, len(refIndexes))))
	}
	if warning := groupWarning(total, *limit); warning != "" {
		result.Warnings = append(result.Warnings, warning)
	}
	result.Complete = !result.HasMore
	result.WarningCount = len(result.Warnings)
	return respondOK("agg", result, *pretty)
}
