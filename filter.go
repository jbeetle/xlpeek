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
	"unicode/utf8"
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

	// fallbacks and sample record cells that read as numbers but had to be
	// compared as text, because the filter value does not read as one. That is
	// the shape a mistyped numeric literal takes — "金额>1OO", the digit 0
	// struck as the letter O — and the rows it matches are unrelated to the
	// rows the caller meant to ask for. Counting them here is what lets the
	// command that owns this filter say so.
	fallbacks int
	sample    string
}

// filterOps is ordered so that two-character operators are recognised before
// their one-character prefixes.
var filterOps = []string{">=", "<=", "!=", ">", "<", "=", "~"}

// filterForm is the shape every filter error restates, so that a caller who has
// just had one rejected does not have to go and look it up.
const filterForm = "expected <column><operator><value> using one of " +
	">=, <=, !=, >, <, = or ~ (for example \"金额>1000\")"

// operatorAt reports the position and text of the first operator in s, or -1
// when it holds none. Operators are matched whole rather than by their leading
// character, so that the "!" of "!= " is not an operator on its own — a value
// like "hello!" is a value, while "hello!=" is a second comparison.
func operatorAt(s string) (int, string) {
	for i := 0; i < len(s); i++ {
		for _, op := range filterOps {
			if strings.HasPrefix(s[i:], op) {
				return i, op
			}
		}
	}
	return -1, ""
}

// parseFilter parses an expression such as "金额>1000" or "状态~完成".
//
// The grammar is deliberately strict, because the alternative is not neutrality
// but a plausible wrong answer: "金额>>100" used to be read as the value ">100"
// and compared as text, and "金额>" — an operator with nothing after it — as
// the value "", which every cell then compared greater than. Both returned
// ok:true with a row count, and neither said the expression had not been
// understood.
func parseFilter(expr string) (*filter, error) {
	at, op := operatorAt(expr)
	if at < 0 {
		return nil, fmt.Errorf("invalid filter %q: %s", expr, filterForm)
	}
	col := strings.TrimSpace(expr[:at])
	if col == "" {
		return nil, fmt.Errorf(
			"invalid filter %q: no column before the operator at position %d; %s",
			expr, at, filterForm)
	}

	raw := strings.TrimSpace(expr[at+len(op):])
	if raw == "" {
		return nil, fmt.Errorf(
			"invalid filter %q: operator %q has no value after it; %s", expr, op, filterForm)
	}
	// Quoting is checked before the value is parsed, so that a filter for an
	// empty cell — --where "备注=''" — stays expressible.
	value, quoted := unquoteValue(raw)
	if !quoted {
		if _, second := operatorAt(value); second != "" {
			return nil, fmt.Errorf(
				"invalid filter %q: %q is a second operator, and a filter takes exactly one; "+
					"if the value really holds one, quote it, as in %s%s'%s'",
				expr, second, col, op, value)
		}
	}

	f := &filter{raw: expr, col: col, op: op, value: value, idx: -1}
	if op != "~" {
		if n, ok := parseNumber(value); ok {
			f.num, f.isNum = n, true
		}
	}
	return f, nil
}

// unquoteValue removes one layer of matching quotes from a filter value and
// reports whether it did. Quoting is how a value that contains an operator
// character — the "a>b" of a substring search — gets past the grammar above,
// and how an empty value is distinguished from a missing one.
func unquoteValue(s string) (string, bool) {
	if len(s) >= 2 {
		for _, quote := range []byte{'"', '\''} {
			if s[0] == quote && s[len(s)-1] == quote {
				return s[1 : len(s)-1], true
			}
		}
	}
	return s, false
}

// filterWarnings reports the clauses whose comparison fell back to text
// against cells that read as numbers. The count is what makes the warning
// actionable: one such cell in a column of names is a coincidence, and every
// cell is a typo in the filter value.
func filterWarnings(filters []*filter) []string {
	var warnings []string
	for _, f := range filters {
		if f.fallbacks == 0 {
			continue
		}
		warnings = append(warnings, fmt.Sprintf(
			"filter %q: the value %q is not a number, but %d cell(s) in column %q read as one "+
				"(first: %q), so they were compared as text — ordered by their digits, not by "+
				"their value; check the value for a typo, or use ~ to search the text deliberately",
			f.raw, f.value, f.fallbacks, f.col, f.sample))
	}
	return warnings
}

// noteTextFallback records one numeric cell that a text comparison was applied
// to, keeping the first as a sample.
func (f *filter) noteTextFallback(cell string) {
	if f.fallbacks == 0 {
		f.sample = cell
	}
	f.fallbacks++
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
// 1-based index. A --col column can be filtered on too, where it is measured
// after it has been computed and before anything else reads the row.
func resolveFilters(filters []*filter, header []string, derived *derivedSet) error {
	for _, flt := range filters {
		idx, err := resolveRowColumn(flt.col, header, derived)
		if err != nil {
			return fmt.Errorf("filter %q: %w", flt.raw, err)
		}
		flt.idx = idx
	}
	return nil
}

// matchFilters reports whether a row satisfies every clause. derived holds the
// row's --col values, which a clause may name.
func matchFilters(filters []*filter, cells, derived []string) bool {
	for _, flt := range filters {
		if !flt.match(formulaValueAt(cells, derived, flt.idx)) {
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
// is not numeric the comparison is lexicographic, which is what makes a date
// range work — but a cell that *is* numeric is counted on the way past, so the
// clause can report that it answered by text something the caller probably
// asked numerically. Equality and the substring operator are case-insensitive,
// matching how spreadsheet auto-filters behave.
func (f *filter) match(cell string) bool {
	switch f.op {
	case "~":
		return strings.Contains(strings.ToLower(cell), strings.ToLower(f.value))
	case "=", "!=":
		equal := false
		if f.isNum {
			if n, ok := parseNumber(cell); ok {
				equal = equalNumeric(n, f.num)
			}
		} else {
			if _, numeric := parseNumber(cell); numeric {
				f.noteTextFallback(cell)
			}
			equal = strings.EqualFold(strings.TrimSpace(cell), f.value)
		}
		if f.op == "!=" {
			return !equal
		}
		return equal
	default:
		n, ok := parseNumber(cell)
		if f.isNum {
			if !ok {
				return false
			}
			return compareNumeric(f.op, n, f.num)
		}
		if ok {
			f.noteTextFallback(cell)
		}
		order := strings.Compare(cell, f.value)
		switch f.op {
		case ">":
			return order > 0
		case ">=":
			return order >= 0
		case "<":
			return order < 0
		case "<=":
			return order <= 0
		}
		return false
	}
}

// equalNumeric compares two numbers at the 15 significant digits this tool
// reports its results in.
//
// A filter value written as a percentage is scaled by 0.01 when it is parsed,
// and that product is not always the same double as the decimal it stands for:
// 25.67/100 is one ulp away from the 0.2567 the cell holds, so an exact
// comparison finds no row while the range comparisons beside it find one. The
// reported precision is the right place to absorb that: it is the same
// normalisation sums already go through, and it is far too narrow to make two
// values that differ in any digit the tool reports compare equal.
func equalNumeric(a, b float64) bool {
	return roundToExcel(a) == roundToExcel(b)
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

// parseNumber reports whether s reads as a number, and returns its value.
func parseNumber(s string) (float64, bool) {
	value, _, ok := parseNumberUnit(s)
	return value, ok
}

// currencySymbols are the marks a number format can put in front of or behind a
// value. Symbols only, deliberately: a three-letter code such as "CNY" could be
// part of a word, and a cell that says USD is not proof that it holds money. The
// signs are written as escapes because the two yen signs, U+00A5 and fullwidth
// U+FFE5, are indistinguishable in a diff.
const currencySymbols = "\u00a5\uffe5\u0024\u20ac\u00a3\u20a9\u20b9"

// parseNumberUnit parses a value the way a spreadsheet presents it, and reports
// the currency it was presented in.
//
// A number format is decoration on top of a stored number: "¥1,234.50" and
// "12.35%" are how a workbook shows 1234.5 and 0.1235. Recognising that is what
// keeps the default, formatted read path comparable and aggregatable instead of
// treating every money column as text — the failure mode where a filter matches
// nothing, or matches the wrong rows, without saying so.
//
// Two rules keep the tolerance from swallowing meaning. A percent sign is a
// scale rather than decoration, so it divides: 12.35% is 0.1235, which is both
// what the cell stores and what --raw reports. And at most one currency symbol
// is accepted, on either side, so a text cell that merely mentions a currency
// is still text.
//
// The symbol is returned rather than discarded because summing across two
// currencies is exactly the kind of plausible-looking nonsense this tool exists
// to catch: the caller can see that a column mixed ¥ and $ even though both
// parsed.
func parseNumberUnit(s string) (float64, string, bool) {
	text := normalizeNumberText(strings.TrimSpace(s))
	// Thousands separators, plus the spaces a locale or a number format pads with.
	for _, separator := range []string{",", " ", "\u00a0", "\u3000"} {
		text = strings.ReplaceAll(text, separator, "")
	}
	if text == "" {
		return 0, "", false
	}
	scale := 1.0
	if strings.HasSuffix(text, "%") {
		text, scale = strings.TrimSuffix(text, "%"), 0.01
	}
	text, unit := trimCurrency(text)
	if text == "" {
		return 0, "", false
	}
	value, err := strconv.ParseFloat(text, 64)
	if err != nil {
		return 0, "", false
	}
	return value * scale, unit, true
}

// normalizeNumberText rewrites the full-width forms of digits and punctuation
// as their ASCII equivalents.
//
// This is how a number looks after it has been through a word processor or a
// chat client: "１２３４" and "１，２３４" are what a Chinese report holds when
// someone pasted a figure in from Word or WeChat rather than typing it. Every
// parser reads those as text, so every aggregate skipped them — the value was
// there, the total was smaller, and only the skipped-cell count said so.
//
// Only characters that cannot change meaning are rewritten: digits, the comma,
// the period, the percent sign and the two signs. A full stop in a sentence
// stays a full stop because nothing else in a value is parsed as a number.
func normalizeNumberText(s string) string {
	rewrite := func(r rune) (rune, bool) {
		switch {
		case r >= '０' && r <= '９': // ０-９
			return r - '０' + '0', true
		case r == '，': // ，
			return ',', true
		case r == '．': // ．
			return '.', true
		case r == '％': // ％
			return '%', true
		case r == '－': // －
			return '-', true
		case r == '＋': // ＋
			return '+', true
		}
		return r, false
	}
	found := false
	for _, r := range s {
		if _, ok := rewrite(r); ok {
			found = true
			break
		}
	}
	if !found {
		return s // the common case: nothing to rewrite, no allocation
	}
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		if replaced, ok := rewrite(r); ok {
			r = replaced
		}
		b.WriteRune(r)
	}
	return b.String()
}

// trimCurrency removes at most one currency symbol from either end of text,
// leaving any sign in place, and reports which symbol it was. A leading sign is
// stepped over first so that a negative amount is understood in both of the
// forms a number format can produce: "-¥1,234.50" and "¥-1,234.50".
func trimCurrency(text string) (string, string) {
	head, sign := text, ""
	if len(head) > 0 && (head[0] == '+' || head[0] == '-') {
		sign, head = head[:1], head[1:]
	}
	if r, size := utf8.DecodeRuneInString(head); size > 0 && strings.ContainsRune(currencySymbols, r) {
		return sign + head[size:], string(r)
	}
	if r, size := utf8.DecodeLastRuneInString(text); size > 0 && strings.ContainsRune(currencySymbols, r) {
		return text[:len(text)-size], string(r)
	}
	return text, ""
}
