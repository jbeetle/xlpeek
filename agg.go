// Copyright (c) 2026 henryyu@163.com. All rights reserved.
//
// This file is part of xlpeek, a read-only spreadsheet reader for
// agents. See README.md for what it does and docs/AGENTS.md for how it is
// meant to be driven.

package main

import (
	"flag"
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

// How a share and a grand total are named. A share is prefixed so that it can
// never collide with the field it shares out, and a total is written with a
// leading underscore in --derive because it is not an output field at all.
const (
	sharePrefix = "share_"
	totalPrefix = "_total_"
)

// additiveFields names the fields a share can be taken of: the sums, and the
// row count. An average, a minimum, a maximum, a distinct count and a formula
// aggregate are all non-additive — the average of a group is not a part of the
// overall average — so a percentage of one of them would be a figure with no
// meaning rather than a figure with a meaning that is merely surprising.
func additiveFields(specs []*aggSpec, count bool) []string {
	var out []string
	for _, spec := range specs {
		if spec.kind == kindSum {
			out = append(out, spec.name)
		}
	}
	if count {
		out = append(out, "count")
	}
	return out
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

// aggDefaultGroupLimit is how many groups come back when the caller does not
// say.
//
// Zero — every group — was the old default, and it is the wrong one to fall
// into: grouping by a near-unique column is a natural thing to do by accident,
// and the response is then the whole table in a shape nobody reads, up to a
// measured 181 KB. A caller who wants all of them still asks with --limit 0;
// the default now costs one more call in the rare case and saves a flooded
// context in the common one.
const aggDefaultGroupLimit = 1000

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

// rowsPhrase renders a row count so that a warning reads as a sentence: "1 row"
// and "23 rows" are both correct, and a bare "%d rows" is not.
func rowsPhrase(n int) string {
	if n == 1 {
		return "1 row"
	}
	return fmt.Sprintf("%d rows", n)
}

// columnWarnings reports the two ways a column can make an aggregate
// meaningless while every individual cell in it still looks fine.
//
// The first is producing no numbers at all: a sum over such a column is null,
// and null is easy to read as "no rows" rather than "no values", so the column
// is named — along with the likeliest reason a money or figure column reads
// empty, which is a formula whose cached result was never written. The second
// is producing numbers in more than one currency: each cell parses, the total
// is still nonsense, and no other check would notice because both currencies
// are decoration on the cell rather than a column of their own.
func columnWarnings(names []string, present, filled []int, units []map[string]bool, numeric []bool, rowsMatched int, calc bool) []string {
	if rowsMatched == 0 {
		// An empty result already says there was nothing to aggregate; blaming
		// every referenced column for it would be noise.
		return nil
	}
	var warnings []string
	for i, name := range names {
		if i >= len(numeric) || !numeric[i] {
			continue
		}
		if present[i] == 0 {
			// Distinguish "there is nothing in this column" from "there is
			// something, and it is not a number": only the first can be fixed
			// by --calc, and sending a text column down that path wastes a
			// whole-sheet parse.
			reason := "the filled cells hold text, not numbers"
			switch {
			case filled[i] == 0 && !calc:
				reason = "the cells are empty in these rows; a formula with no cached result reads " +
					"this way, so re-run with --calc if that is what they are"
			case filled[i] == 0:
				reason = "the cells are empty in these rows"
			}
			warnings = append(warnings, fmt.Sprintf(
				"column %q produced no numeric values across %s, so its aggregates are null: %s",
				name, rowsPhrase(rowsMatched), reason))
		}
		if len(units[i]) > 1 {
			symbols := make([]string, 0, len(units[i]))
			for symbol := range units[i] {
				symbols = append(symbols, symbol)
			}
			sort.Strings(symbols)
			warnings = append(warnings, fmt.Sprintf(
				"column %q holds %d currencies (%s) and was aggregated straight through; the total "+
					"adds incomparable amounts — convert to one currency, or group by the currency column",
				name, len(units[i]), strings.Join(symbols, ", ")))
		}
	}
	return warnings
}

// blankGroupWarning reports rows that were summarised under an empty grouping
// key, and points at the usual reason.
//
// A spreadsheet stores a merged region's value only in its top-left cell, so a
// report that merges a label across the rows it applies to leaves the rest of
// them empty — and grouping on that column files those rows under "". An empty
// key is easy to read as a value in the output, which makes this the silent
// half of the problem --fill-merged exists to fix.
func blankGroupWarning(groups map[string]*groupState, order []string, groupNames []string, fillMerged bool) string {
	if len(groupNames) == 0 {
		return ""
	}
	rows := 0
	for _, key := range order {
		group := groups[key]
		blank := true
		for _, value := range group.keys {
			if strings.TrimSpace(value) != "" {
				blank = false
				break
			}
		}
		if blank {
			rows += group.rows
		}
	}
	if rows == 0 {
		return ""
	}
	hint := ""
	if !fillMerged {
		hint = "; if the sheet merges those labels across rows, re-run with --fill-merged"
	}
	if rows == 1 {
		return fmt.Sprintf(
			"1 row has no value in the grouping columns and was summarised under an empty key%s", hint)
	}
	return fmt.Sprintf(
		"%d rows have no value in the grouping columns and were summarised together under an "+
			"empty key%s", rows, hint)
}

// groupWarning explains a grouping that has stopped summarising. It returns
// nothing when the caller capped the output themselves, because a deliberate
// cap is not a mistake: the count and next_offset are in the response already.
func groupWarning(groups, limit int, limitSet bool) string {
	if groups <= aggGroupWarnThreshold || (limitSet && limit > 0) {
		return ""
	}
	if limit > 0 {
		return fmt.Sprintf(
			"%d groups were produced and no --limit was given, so this response holds the first "+
				"%d of them; grouping by a near-unique column returns the table rather than a "+
				"summary — narrow it with --sort-by and --limit, follow next_offset, or pass "+
				"--limit 0 to ask for all of them",
			groups, limit)
	}
	return fmt.Sprintf(
		"%d groups were produced and --limit 0 asked for all of them, so every one is in this "+
			"response; grouping by a near-unique column returns the table rather than a "+
			"summary — set --limit, or group by something coarser",
		groups)
}

// nearField names the field a caller probably meant, when the one they wrote
// does not exist.
//
// The mistake it exists for is writing "sum_收入" for an aggregate the caller
// named themselves: naming an output *replaces* the generated prefix rather
// than joining it, so --sum "收入=[金额(万元)]" produces 收入, not sum_收入. That
// rule is documented, and the naming is still easy to get wrong from memory,
// which is the whole reason the error lists the fields that do exist. Naming
// the near miss costs one line and saves a round trip.
func nearField(id string, known []string) string {
	trimmed := id
	for _, prefix := range aggPrefix {
		if strings.HasPrefix(id, prefix) {
			trimmed = strings.TrimPrefix(id, prefix)
			break
		}
	}
	if trimmed == id {
		return ""
	}
	for _, name := range known {
		if name == trimmed {
			return fmt.Sprintf(
				"; did you mean %q? A field you name yourself is called exactly that name — the "+
					"prefix is only added when you leave the naming to the tool", trimmed)
		}
	}
	return ""
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
	// formulaValues holds this group's rows for the columns the --agg formulas
	// read, one slice per column, filled only when there is an --agg to compute.
	// A group is defined by a value rather than by a position, so its rows are
	// usually scattered across the sheet and cannot be handed to the engine as a
	// range until they have been collected here.
	formulaValues [][]any
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
	calc := fs.Bool("calc", false, "evaluate formulas that have no cached value; loads the sheet into memory")
	fillMerged := fs.Bool("fill-merged", false, "copy each merged region's value into every cell it spans; loads the sheet into memory")
	offset := fs.Int("offset", 0, "output groups to skip")
	fs.IntVar(offset, "o", 0, "output groups to skip (shorthand)")
	limit := fs.Int("limit", aggDefaultGroupLimit, "maximum output groups; 0 means all")
	fs.IntVar(limit, "l", aggDefaultGroupLimit, "maximum output groups (shorthand)")
	password := fs.String("password", "", "password for an encrypted workbook")
	raw := fs.Bool("raw", false, "aggregate stored values instead of number-formatted values")
	tmpDir := fs.String("tmpdir", "", "directory for temporary files (default: system temp)")
	pretty := fs.Bool("pretty", false, "indent the JSON output")
	count := fs.Bool("count", false, "include the number of matched rows per group")
	share := fs.Bool("share", false, "add share_<field> percentages of the total for each summed field")
	var sums, avgs, mins, maxs, distincts, deriveArgs, wheres stringSlice
	var colArgs, aggArgs stringSlice
	fs.Var(&sums, "sum", "sum of an expression, e.g. --sum 收入金额 or --sum \"净利=收入金额-成本金额\"; repeatable")
	fs.Var(&avgs, "avg", "average of an expression; repeatable")
	fs.Var(&mins, "min", "minimum of an expression; repeatable")
	fs.Var(&maxs, "max", "maximum of an expression; repeatable")
	fs.Var(&distincts, "count-distinct", "count of distinct non-empty values in a column; repeatable")
	fs.Var(&aggArgs, "agg", "aggregate computed per group by an Excel formula over the group's rows, "+
		"e.g. --agg \"中位金额=MEDIAN(收入金额)\"; loads the sheet into memory; repeatable")
	fs.Var(&colArgs, "col", "column computed per row by an Excel formula, e.g. "+
		"--col \"月=MONTH(过账日期)\"; usable in --group-by, --where, --sort-by and the "+
		"aggregates; needs a header row and loads the sheet into memory; repeatable")
	fs.Var(&deriveArgs, "derive", "metric computed from aggregate fields, e.g. --derive \"毛利率=(sum_收入金额-sum_成本金额)/sum_收入金额\"; repeatable")
	fs.Var(&wheres, "where", "row filter applied before aggregating; repeatable and ANDed")

	operands, code, ok := parseFlags(fs, args)
	if !ok {
		return code
	}
	if len(operands) != 1 {
		return operandError("agg", operands, "exactly one workbook path")
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
	// Whether the cap was chosen or defaulted decides what the response owes
	// the caller: an explicit --limit is an answer, a defaulted one is a
	// truncation that has to be explained.
	limitSet := false
	fs.Visit(func(set *flag.Flag) {
		if set.Name == "limit" || set.Name == "l" {
			limitSet = true
		}
	})

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
				return failUsage("agg", fmt.Sprintf("--%s %q: %v%s",
					group.kind, item, err, bracketHint(source)))
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
	// A formula aggregate is named here and scanned once the header is known,
	// because a formula that reads a column cannot be checked without the
	// column names — and checking it late is the whole point: an unsupported
	// function or a misspelled column must be one message, not one per row.
	var formulaArgs []formulaAgg
	for _, item := range aggArgs {
		name, source, err := splitNamedFormula(item)
		if err != nil {
			return failUsage("agg", fmt.Sprintf("--agg %q: %v", item, err))
		}
		formulaArgs = append(formulaArgs, formulaAgg{name: name, source: source})
	}

	if len(specs) == 0 && !*count && len(formulaArgs) == 0 {
		return failUsage("agg", "nothing to compute: give at least one of "+
			"--sum, --avg, --min, --max, --count-distinct, --agg or --count")
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
			return failUsage("agg", fmt.Sprintf("--derive %q: %v%s", item, err, bracketHint(source)))
		}
		derives = append(derives, &deriveSpec{name: name, source: source, expr: expr})
	}

	// Output field order: aggregate results, then the row count, then the
	// shares, then the derived metrics.
	fieldNames := make([]string, 0, len(specs)+len(formulaArgs)+1+len(derives)+len(specs))
	for _, spec := range specs {
		fieldNames = append(fieldNames, spec.name)
	}
	for _, spec := range formulaArgs {
		fieldNames = append(fieldNames, spec.name)
	}
	if *count {
		fieldNames = append(fieldNames, "count")
	}
	// Sharing is defined for what is additive: a sum, and a count. A group's
	// average is not a share of the overall average, and a median has no total
	// to be a part of, so emitting a percentage of one would be inventing a
	// number rather than reporting one.
	sharable := additiveFields(specs, *count)
	if *share && len(sharable) == 0 {
		return failUsage("agg", "--share needs something that can be shared, and neither "+
			"--avg, --min, --max, --count-distinct nor --agg produces an additive figure; "+
			"add --sum or --count, or drop --share")
	}
	if *share {
		for _, name := range sharable {
			fieldNames = append(fieldNames, sharePrefix+name)
		}
	}

	// A derived metric can only reference fields that were actually computed,
	// so a typo fails immediately instead of silently yielding null. The
	// grand total of a summed field is referenceable as _total_<field>, which
	// is how a share is written without --share.
	if len(derives) > 0 {
		known := make(map[string]bool, len(fieldNames))
		for _, name := range fieldNames {
			known[name] = true
		}
		additive := make(map[string]bool, len(sharable))
		for _, name := range sharable {
			additive[name] = true
		}
		for _, d := range derives {
			used := map[string]bool{}
			identifiers(d.expr, used)
			for id := range used {
				if known[id] {
					continue
				}
				if rest, ok := strings.CutPrefix(id, totalPrefix); ok {
					if additive[rest] {
						continue
					}
					return failUsage("agg", fmt.Sprintf(
						"--derive %q references %q, but only an additive field has a total: "+
							"%s; an average, a median or a distinct count has none",
						d.source, id, strings.Join(sharable, ", ")))
				}
				return failUsage("agg", fmt.Sprintf(
					"--derive %q references %q, which is not one of the computed fields: %s%s",
					d.source, id, strings.Join(fieldNames, ", "), nearField(id, fieldNames)))
			}
		}
	}
	for _, d := range derives {
		fieldNames = append(fieldNames, d.name)
	}

	if err := validateUniqueFields(fieldNames); err != nil {
		return failUsage("agg", err.Error())
	}
	// A group key and an aggregate are both written into the same row, so the
	// two namespaces cannot overlap either: {"count": "华南", "count": 5} is one
	// key to every parser, and the grouping column would be the copy that is
	// lost. --col makes this easy to reach — a derived column named count, or a
	// grouping by a column that an aggregate is also named after.
	names := map[string]bool{}
	for _, name := range splitList(groupBy) {
		if names[name] {
			return failUsage("agg", fmt.Sprintf(
				"--group-by names %q twice, so every row would carry the key twice and a parser "+
					"would keep only the last", name))
		}
		names[name] = true
	}
	for _, name := range fieldNames {
		if names[name] {
			return failUsage("agg", fmt.Sprintf(
				"the grouping column %q and the output field %q share a name, so every row would "+
					"carry the key twice and a parser would keep only the last; rename the field "+
					"with name=expression, or group by a different column", name, name))
		}
		names[name] = true
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
	defer releaseWorkbook(f)

	sheetName, sheetIndex, err := resolveSheet(f, sheet)
	if err != nil {
		return failWithSheet("agg", err, userSheets(f), *pretty)
	}

	iter, err := f.Rows(sheetName)
	if err != nil {
		return failWithSheet("agg", err, userSheets(f), *pretty)
	}
	defer func() { _ = iter.Close() }()

	// Merged labels are a correctness hazard for aggregation rather than a
	// display detail: a region covering three rows holds its label in the first
	// of them, so grouping on that column would file the other two under "".
	var merges *mergeFiller
	if *fillMerged {
		if merges, err = newMergeFiller(f, sheetName); err != nil {
			return failWithSheet("agg", err, userSheets(f), *pretty)
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
	groupNames := splitList(groupBy)

	// A derived column is referred to by name, so it needs a row of names to be
	// named in. Without one there is nothing to write the name into and no way
	// to address the result, and a column the caller cannot reach is not a
	// feature.
	if len(colArgs) > 0 && headerAt == 0 {
		return failUsage("agg", "--col needs --header (or --header-row N): a derived column is "+
			"referred to by name, and a sheet read without a header row has none")
	}

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
		// Per referenced column: how many cells were filled at all, how many of
		// those produced a number, and which currencies they were presented in.
		// Together these let the result tell "empty" apart from "not a number"
		// when it warns about a total it could not compute.
		refPresent []int
		refFilled  []int
		refUnits   []map[string]bool
		numericRef []bool
		// Warnings raised while evaluating formulas, capped like every other
		// per-cell report so a broken workbook cannot pad the response.
		formulaWarnings []string
		// The --col columns, the --agg formulas, and the engine that evaluates
		// them. All three are built during resolve, once the header is known.
		derived *derivedSet
		runner  *formulaAggRunner
	)

	// The engine adds its scratch worksheet lazily, so a command that never
	// evaluates a formula never touches the workbook, and removes it on the way
	// out either way.
	engine := newFormulaEngine(f)
	defer engine.close()

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

	// resolve binds every name the command mentioned to a column, which can only
	// happen once the header row has been read. The --col formulas are scanned
	// here too: they are the names a --group-by, a --where or an aggregate may
	// refer to, so they have to exist before anything else is resolved.
	resolve := func() error {
		ctx := &formulaContext{
			header:  header,
			sheets:  userSheets(f),
			defined: definedNames(f),
			flag:    "col",
		}
		var err error
		if derived == nil && len(colArgs) > 0 {
			if derived, err = parseDerivedColumns(colArgs, ctx); err != nil {
				// A formula that cannot be read is the command's fault, not the
				// file's: the same command fails the same way on every run.
				return usageFailure(err)
			}
		}
		if runner == nil && len(formulaArgs) > 0 {
			ctx.flag = "agg"
			specs := make([]*formulaAgg, len(formulaArgs))
			for i := range formulaArgs {
				ctx.broadRefs, ctx.misused = nil, nil
				if formulaArgs[i].parts, err = scanFormula(formulaArgs[i].source, ctx); err != nil {
					return usageFailure(err)
				}
				formulaArgs[i].cols = formulaArgs[i].parts.columns()
				formulaArgs[i].broadRefs = ctx.broadRefs
				formulaArgs[i].misused = ctx.misused
				specs[i] = &formulaArgs[i]
			}
			runner = newFormulaAggRunner(specs, header)
		}
		// Every reference below is resolved through the derived columns first, so
		// that a --col is reachable from --group-by, --where, the aggregates and
		// the formulas alike.
		for _, name := range groupNames {
			idx, err := resolveRowColumn(name, header, derived)
			if err != nil {
				return fmt.Errorf("--group-by %s: %w", name, err)
			}
			groupIdx = append(groupIdx, idx)
		}
		if err = resolveFilters(filters, header, derived); err != nil {
			return err
		}
		wanted := map[string]bool{}
		// numeric names the columns an aggregate reads as a number, which is not
		// the same as every column referenced: --count-distinct counts the
		// values of a text column without ever parsing them.
		numeric := map[string]bool{}
		for _, spec := range specs {
			if spec.expr != nil {
				identifiers(spec.expr, wanted)
				if spec.kind != kindCountDistinct {
					identifiers(spec.expr, numeric)
				}
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
			idx, err := resolveRowColumn(name, header, derived)
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
		refPresent = make([]int, len(refNames))
		refFilled = make([]int, len(refNames))
		refUnits = make([]map[string]bool, len(refNames))
		numericRef = make([]bool, len(refNames))
		for i, name := range refNames {
			numericRef[i] = numeric[name]
		}
		for _, spec := range specs {
			if spec.kind == kindCountDistinct {
				spec.index = colIndex[spec.source]
			}
		}
		// Anything a group key or an aggregate already names is accounted for.
		// Every other column is what the comparability check watches. A derived
		// column is not one of the sheet's columns and is therefore not watched:
		// it is computed here, so it varies only as its formula says it does.
		for _, idx := range groupIdx {
			if idx >= 0 {
				used[idx] = true
			}
		}
		for _, idx := range refIndexes {
			if idx >= 0 {
				used[idx] = true
			}
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
					return failWithSheet("agg", err, userSheets(f), *pretty)
				}
				if merges != nil {
					header = merges.apply(pos, header, defaultMaxColumns)
				}
				if err = resolve(); err != nil {
					return fail("agg", err, *pretty)
				}
			}
			continue
		}
		cells, err := iter.Columns()
		if err != nil {
			return failWithSheet("agg", err, userSheets(f), *pretty)
		}
		// A merged label is written into the cells the region spans before
		// anything reads them, so grouping and filtering see the label the
		// report put there rather than the blank the file stores.
		if merges != nil {
			cells = merges.apply(pos, cells, defaultMaxColumns)
		}
		// Formulas are only consulted for cells that came back empty, and only
		// when asked for: resolving one makes excelize load the whole worksheet.
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
			// No header row: names cannot be used, but letters and indexes can.
			if err = resolve(); err != nil {
				return fail("agg", err, *pretty)
			}
		}
		rowsScanned++
		if *skipEmpty && !nonEmpty(cells) {
			continue
		}
		// The derived columns are computed before anything reads the row, so
		// that --where and --group-by see the value the caller named rather than
		// the formula that produces it.
		rowDerived, err := derived.eval(engine, sheetName, pos, cells)
		if err != nil {
			// A formula that cannot work at all — an unsupported function, a
			// misspelled column — stops the command with its message rather than
			// repeating it on every row left in the sheet.
			return respondErr("agg", codeUsage, err.Error(), *pretty, exitUsage)
		}
		if len(filters) > 0 && !matchFilters(filters, cells, rowDerived) {
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
		// aggregates is read once and a non-numeric one is counted once. The
		// currency a value was presented in is kept, because two of them in one
		// column make the aggregate meaningless however well each cell parses.
		for i, idx := range refIndexes {
			values[i], present[i] = 0, false
			if !numericRef[i] {
				// A column that only --count-distinct reads is text on purpose.
				// Parsing it as a number would count its every cell as a
				// skipped one and invent a warning about values that were never
				// meant to be numbers.
				continue
			}
			raw := formulaValueAt(cells, rowDerived, idx)
			if raw == "" {
				continue
			}
			refFilled[i]++
			number, unit, ok := parseNumberUnit(raw)
			if !ok {
				nonNumeric++
				continue
			}
			if unit != "" {
				if refUnits[i] == nil {
					refUnits[i] = map[string]bool{}
				}
				refUnits[i][unit] = true
			}
			refPresent[i]++
			values[i], present[i] = number, true
		}

		keys := make([]string, len(groupIdx))
		for i, idx := range groupIdx {
			keys[i] = formulaValueAt(cells, rowDerived, idx)
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
		// A --agg formula is computed from the group's rows once the scan is
		// over, so each row's values for the columns it reads are kept as they
		// go past.
		runner.addRow(group, cells, rowDerived)
		for _, state := range group.aggs {
			if state.spec.kind == kindCountDistinct {
				state.add(0, false, formulaValueAt(cells, rowDerived, state.spec.index))
				continue
			}
			value, ok := state.spec.expr.eval(lookup)
			state.add(value, ok, "")
		}
	}
	if err = iter.Error(); err != nil {
		return failWithSheet("agg", err, userSheets(f), *pretty)
	}
	if !resolved {
		// A sheet with no data rows still has to resolve, or a bad column name
		// would silently produce an empty result instead of an error.
		if err = resolve(); err != nil {
			return fail("agg", err, *pretty)
		}
	}

	// Sort groups by their key so that the output is stable between runs even
	// though the map is not.
	sort.Strings(order)

	// The total each share is a share of, summed from the groups of this same
	// scan rather than from a second pass over the worksheet: a numerator and
	// its denominator have to come from one read of the file, or a workbook
	// edited between two calls is divided by a total that no longer belongs to
	// it. Summing the groups also makes the shares add up to exactly 1, which a
	// separately scanned total would not.
	totals := map[string]float64{}
	zeroTotals := false
	if len(sharable) > 0 {
		accumulators := map[string]*compensatedSum{}
		for _, name := range sharable {
			if name == "count" {
				totals[name] = float64(rowsMatched)
				continue
			}
			accumulators[name] = &compensatedSum{}
		}
		for _, groupKey := range order {
			for _, state := range groups[groupKey].aggs {
				accumulator, ok := accumulators[state.spec.name]
				if !ok {
					continue
				}
				if value, has := state.result(); has {
					accumulator.add(value)
				}
			}
		}
		for name, accumulator := range accumulators {
			totals[name] = roundToExcel(accumulator.value())
		}
	}

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
		if runner != nil {
			results, _, err := runner.run(engine, group)
			if err != nil {
				// The formula cannot work for any group, so the command is
				// rejected with the engine's own message rather than repeated
				// for every group left.
				return respondErr("agg", codeUsage, err.Error(), *pretty, exitUsage)
			}
			for _, spec := range formulaArgs {
				row.keys = append(row.keys, spec.name)
				row.vals = append(row.vals, results[spec.name])
			}
		}
		if *count {
			row.keys = append(row.keys, "count")
			row.vals = append(row.vals, group.rows)
			computed["count"] = float64(group.rows)
		}
		if *share {
			for _, name := range sharable {
				field := sharePrefix + name
				row.keys = append(row.keys, field)
				value, has := computed[name]
				switch {
				case !has:
					// Nothing was aggregated for this group, so there is no part
					// of the total to report.
					row.vals = append(row.vals, nil)
				case totals[name] == 0:
					zeroTotals = true
					row.vals = append(row.vals, nil)
				default:
					share := roundToExcel(value / totals[name])
					row.vals = append(row.vals, share)
					computed[field] = share
				}
			}
		}
		for _, d := range derives {
			value, ok := d.expr.eval(func(name string) (float64, bool) {
				if rest, isTotal := strings.CutPrefix(name, totalPrefix); isTotal {
					number, found := totals[rest]
					return number, found
				}
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
	if warning := blankGroupWarning(groups, order, groupNames, *fillMerged); warning != "" {
		result.Warnings = append(result.Warnings, warning)
	}
	result.Warnings = append(result.Warnings, filterWarnings(filters)...)
	result.Warnings = append(result.Warnings, formulaWarnings...)
	result.Warnings = append(result.Warnings, runner.warnings(len(order))...)
	result.Warnings = append(result.Warnings, derived.warnings()...)
	if zeroTotals {
		result.Warnings = append(result.Warnings, fmt.Sprintf(
			"the total of %s is zero, so its share of it is null rather than a division by "+
				"nothing; check whether a --where excluded the rows that carried the value",
			strings.Join(sharable, ", ")))
	}
	result.Warnings = append(result.Warnings, columnWarnings(
		refNames, refPresent, refFilled, refUnits, numericRef, rowsMatched, *calc)...)
	if nonNumeric > 0 {
		// The denominator counts the cells that were read as numbers on
		// purpose, so a mostly-text column does not make the ratio read as an
		// alarm about the whole sheet.
		numericCells := 0
		for _, isNumeric := range numericRef {
			if isNumeric {
				numericCells++
			}
		}
		result.Warnings = append(result.Warnings, fmt.Sprintf(
			"%d of %d cells could not be read as numbers and were skipped",
			nonNumeric, rowsMatched*max(1, numericCells)))
	}
	if warning := groupWarning(total, *limit, limitSet); warning != "" {
		result.Warnings = append(result.Warnings, warning)
	}
	result.Complete = !result.HasMore
	result.WarningCount = len(result.Warnings)
	return respondOK("agg", result, *pretty)
}
