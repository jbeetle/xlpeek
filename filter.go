// Copyright (c) 2026 henryyu@163.com. All rights reserved.
//
// This file is part of xlpeek, a read-only spreadsheet reader for
// agents. See README.md for what it does and docs/AGENTS.md for how it is
// meant to be driven.

package main

import (
	"fmt"
	"strconv"
	"strings"
)

// filter is one --where clause: a column reference, an operator and a literal
// value. Several clauses on one command are combined with AND.
type filter struct {
	raw   string
	col   string
	op    string
	value string
	num   float64
	isNum bool
	idx   int // resolved 0-based column index, -1 until resolved
}

// filterOps is ordered so that two-character operators are recognised before
// their one-character prefixes.
var filterOps = []string{">=", "<=", "!=", ">", "<", "=", "~"}

// parseFilter parses an expression such as "金额>1000" or "状态~完成".
func parseFilter(expr string) (*filter, error) {
	for _, op := range filterOps {
		i := strings.Index(expr, op)
		if i <= 0 {
			continue
		}
		f := &filter{
			raw:   expr,
			col:   strings.TrimSpace(expr[:i]),
			op:    op,
			value: strings.Trim(strings.TrimSpace(expr[i+len(op):]), `"'`),
			idx:   -1,
		}
		if op != "~" {
			if n, ok := parseNumber(f.value); ok {
				f.num, f.isNum = n, true
			}
		}
		return f, nil
	}
	return nil, fmt.Errorf(
		"invalid filter %q: expected <column><operator><value> using one of "+
			">=, <=, !=, >, <, = or ~ (for example \"金额>1000\")", expr)
}

func parseFilterList(exprs []string) ([]*filter, error) {
	filters := make([]*filter, 0, len(exprs))
	for _, expr := range exprs {
		flt, err := parseFilter(expr)
		if err != nil {
			return nil, err
		}
		filters = append(filters, flt)
	}
	return filters, nil
}

// resolveFilters binds each filter to a column index. Header names are only
// available in header mode; otherwise a filter must name a column letter or a
// 1-based index.
func resolveFilters(filters []*filter, header []string) error {
	for _, flt := range filters {
		idx, err := resolveColumn(flt.col, header)
		if err != nil {
			return fmt.Errorf("filter %q: %w", flt.raw, err)
		}
		flt.idx = idx
	}
	return nil
}

// matchFilters reports whether a row satisfies every clause.
func matchFilters(filters []*filter, cells []string) bool {
	for _, flt := range filters {
		cell := ""
		if flt.idx >= 0 && flt.idx < len(cells) {
			cell = cells[flt.idx]
		}
		if !flt.match(cell) {
			return false
		}
	}
	return true
}

// match applies one comparison.
//
// A numeric comparison is performed whenever the filter value parses as a
// number: a cell that does not parse as a number then simply fails to match,
// rather than silently falling back to comparing text. When the filter value
// is not numeric the comparison is lexicographic. Equality and the substring
// operator are case-insensitive, matching how spreadsheet auto-filters behave.
func (f *filter) match(cell string) bool {
	switch f.op {
	case "~":
		return strings.Contains(strings.ToLower(cell), strings.ToLower(f.value))
	case "=", "!=":
		equal := false
		if f.isNum {
			if n, ok := parseNumber(cell); ok {
				equal = n == f.num
			}
		} else {
			equal = strings.EqualFold(strings.TrimSpace(cell), f.value)
		}
		if f.op == "!=" {
			return !equal
		}
		return equal
	default:
		if f.isNum {
			n, ok := parseNumber(cell)
			if !ok {
				return false
			}
			return compareNumeric(f.op, n, f.num)
		}
		return compareNumeric(f.op, float64(strings.Compare(cell, f.value)), 0)
	}
}

func compareNumeric(op string, a, b float64) bool {
	switch op {
	case ">":
		return a > b
	case ">=":
		return a >= b
	case "<":
		return a < b
	case "<=":
		return a <= b
	}
	return false
}

// parseNumber reports whether s looks like a plain number, tolerating the
// thousands separators that formatted spreadsheet values often carry.
// Currency symbols and percent signs are deliberately left alone: guessing at
// them would silently change what a comparison means. Use --raw for exact
// numeric filtering.
func parseNumber(s string) (float64, bool) {
	s = strings.ReplaceAll(strings.TrimSpace(s), ",", "")
	s = strings.ReplaceAll(s, " ", "")
	if s == "" {
		return 0, false
	}
	n, err := strconv.ParseFloat(s, 64)
	return n, err == nil
}
