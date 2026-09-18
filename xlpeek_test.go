package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/xuri/excelize/v2"
)

// evalWith parses expr and evaluates it against a map of variables.
func evalWith(t *testing.T, expr string, vars map[string]float64) (float64, bool) {
	t.Helper()
	node, err := parseExpr(expr)
	if err != nil {
		t.Fatalf("parseExpr(%q): %v", expr, err)
	}
	return node.eval(func(name string) (float64, bool) {
		v, ok := vars[name]
		return v, ok
	})
}

func TestParseExprArithmetic(t *testing.T) {
	vars := map[string]float64{"a": 10, "b": 3, "收入": 100, "成本": 40}
	cases := []struct {
		expr string
		want float64
		ok   bool
	}{
		{expr: "1+2*3", want: 7, ok: true},   // multiplication binds tighter
		{expr: "(1+2)*3", want: 9, ok: true}, // parentheses override
		{expr: "a-b", want: 7, ok: true},
		{expr: "收入-成本", want: 60, ok: true},
		{expr: "收入/4", want: 25, ok: true},
		{expr: "-5", want: -5, ok: true},
		{expr: "-a+2", want: -8, ok: true},
		{expr: "2*3+4*5", want: 26, ok: true},
		{expr: "1-2-3", want: -4, ok: true},    // left associative
		{expr: "a/0", ok: false},               // division by zero yields no value
		{expr: "missing+1", want: 1, ok: true}, // an absent operand counts as zero
		{expr: "missing", ok: false},           // ...but a bare one stays absent
		{expr: "missing*收入", want: 0, ok: true},
	}
	for _, tc := range cases {
		got, ok := evalWith(t, tc.expr, vars)
		if ok != tc.ok {
			t.Errorf("eval(%q) present = %v, want %v", tc.expr, ok, tc.ok)
			continue
		}
		if ok && math.Abs(got-tc.want) > 1e-9 {
			t.Errorf("eval(%q) = %v, want %v", tc.expr, got, tc.want)
		}
	}
}

// A column name may start with a digit; scanning the whole operand before
// deciding whether it is a number keeps "2023年收入" from being read as 2023.
func TestParseExprIdentifierStartingWithDigit(t *testing.T) {
	if _, ok := evalWith(t, "2023", map[string]float64{}); !ok {
		t.Error("a bare number should evaluate")
	}
	vars := map[string]float64{"2023年收入": 500}
	if got, ok := evalWith(t, "2023年收入", vars); !ok || got != 500 {
		t.Errorf("eval(2023年收入) = %v, %v; want 500, true", got, ok)
	}
	if got, ok := evalWith(t, "2023年收入*2", vars); !ok || got != 1000 {
		t.Errorf("eval(2023年收入*2) = %v, %v; want 1000, true", got, ok)
	}
}

// A name containing an operator has to be escapable, or it can never be used.
func TestParseExprBracketedName(t *testing.T) {
	vars := map[string]float64{"收入-成本": 60, "净 利": 25}
	if got, ok := evalWith(t, "[收入-成本]", vars); !ok || got != 60 {
		t.Errorf("eval([收入-成本]) = %v, %v; want 60, true", got, ok)
	}
	if got, ok := evalWith(t, "[净 利]/5", vars); !ok || got != 5 {
		t.Errorf("eval([净 利]/5) = %v, %v; want 5, true", got, ok)
	}
}

func TestParseExprErrors(t *testing.T) {
	for _, expr := range []string{"", "1+", "(1+2", "1)", "[abc", "[]", "1 2"} {
		if _, err := parseExpr(expr); err == nil {
			t.Errorf("parseExpr(%q) should fail", expr)
		}
	}
}

func TestBracketHint(t *testing.T) {
	cases := []struct {
		in       string
		wantHint bool
	}{
		// A column name from a real workbook: the parenthesis is arithmetic to
		// the parser, and the bracket escape is not something to be guessed.
		{in: "金额(万元)", wantHint: true},
		{in: "Order Amount", wantHint: true},
		// Ordinary mistakes stay ordinary: no suggestion to rename them.
		{in: "1+*2", wantHint: false},
		{in: "收入金额-", wantHint: false},
		{in: "", wantHint: false},
	}
	for _, tc := range cases {
		got := bracketHint(tc.in)
		if (got != "") != tc.wantHint {
			t.Errorf("bracketHint(%q) = %q, want hint=%v", tc.in, got, tc.wantHint)
		}
	}
}

func TestIdentifiersAreCollected(t *testing.T) {
	node, err := parseExpr("(a+b)*c-2023")
	if err != nil {
		t.Fatalf("parseExpr: %v", err)
	}
	seen := map[string]bool{}
	identifiers(node, seen)
	for _, want := range []string{"a", "b", "c"} {
		if !seen[want] {
			t.Errorf("identifiers missed %q, got %v", want, seen)
		}
	}
	if seen["2023"] {
		t.Error("a numeric literal should not be reported as an identifier")
	}
}

// Plain addition drifts over thousands of terms; the compensation is what keeps
// a revenue total from coming out as ...24999 instead of ...25.
func TestCompensatedSumBeatsNaive(t *testing.T) {
	var compensated compensatedSum
	var naive float64
	for i := 0; i < 10000; i++ {
		compensated.add(0.1)
		naive += 0.1
	}
	if got := compensated.value(); math.Abs(got-1000) > 1e-9 {
		t.Errorf("compensatedSum = %v, want 1000", got)
	}
	if math.Abs(naive-1000) < 1e-9 {
		t.Skip("naive summation happened to be exact here")
	}
	if math.Abs(compensated.value()-1000) >= math.Abs(naive-1000) {
		t.Errorf("compensation did not improve accuracy: compensated=%v naive=%v",
			compensated.value(), naive)
	}
}

func TestRoundToExcel(t *testing.T) {
	cases := []struct {
		in   float64
		want float64
	}{
		// Sixteen significant digits collapse to fifteen.
		{in: 420940492.1000002, want: 420940492.1},
		// Fifteen significant digits are already inside Excel's precision, so
		// this is deliberately left alone. Rounding here cannot rescue a
		// drifted total; compensated summation is what prevents the drift.
		{in: 2839456194.24999, want: 2839456194.24999},
		{in: 0, want: 0},
		{in: 375, want: 375},
	}
	for _, tc := range cases {
		if got := roundToExcel(tc.in); got != tc.want {
			t.Errorf("roundToExcel(%v) = %v, want %v", tc.in, got, tc.want)
		}
	}
	if got := roundToExcel(math.NaN()); !math.IsNaN(got) {
		t.Errorf("roundToExcel(NaN) = %v, want NaN", got)
	}
}

func TestTSVField(t *testing.T) {
	cases := []struct{ in, want string }{
		{in: "abc", want: "abc"},
		{in: "2024-01-01", want: "2024-01-01"},
		// Remark columns routinely carry tabs and newlines; flattening those
		// silently would corrupt the data, so they are quoted as CSV does.
		{in: "a\tb", want: "\"a\tb\""},
		{in: "a\nb", want: "\"a\nb\""},
		{in: "say \"hi\"", want: "\"say \"\"hi\"\"\""},
	}
	for _, tc := range cases {
		if got := tsvField(tc.in); got != tc.want {
			t.Errorf("tsvField(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestBuildTSV(t *testing.T) {
	got := buildTSV([]string{"a", "b"}, [][]string{{"1", "2"}, {"3", "4"}})
	const want = "a\tb\n1\t2\n3\t4\n"
	if got != want {
		t.Errorf("buildTSV = %q, want %q", got, want)
	}
}

// Two output fields sharing a name would emit the same JSON key twice, and a
// parser keeps only the last copy — a total of 2,839,456,194.25 silently
// becomes 1. The check has to reject that rather than let it through.
func TestValidateUniqueFields(t *testing.T) {
	if err := validateUniqueFields([]string{"sum_收入金额", "count", "毛利率"}); err != nil {
		t.Errorf("distinct names should be accepted, got %v", err)
	}
	err := validateUniqueFields([]string{"sum_收入金额", "count", "sum_收入金额"})
	if err == nil {
		t.Fatal("duplicate names should be rejected")
	}
	if !strings.Contains(err.Error(), "sum_收入金额") {
		t.Errorf("error should name the offender, got %q", err)
	}
	if !strings.Contains(err.Error(), "name=expression") {
		t.Errorf("error should say how to fix it, got %q", err)
	}
}

// The same hazard in read: projecting one column twice must not produce two
// identical object keys.
func TestProjectedHeaderKeysAreUnique(t *testing.T) {
	names := []string{"收入金额", "收入金额"}
	got := headerKeys(names, len(names))
	if len(got) != 2 || got[0] == got[1] {
		t.Fatalf("projected keys must be unique, got %v", got)
	}
	row := orderedRow{keys: got, vals: []any{"1", "2"}}
	const want = `{"收入金额":"1","收入金额_2":"2"}`
	if rendered := marshalLikeCLI(t, row); rendered != want {
		t.Errorf("Marshal = %s, want %s", rendered, want)
	}
}

// The comparability check only shouts about a column whose name reads like a
// unit, currency or basis. These cases pin down where that line sits, because
// a false positive trains a caller to ignore the warning and a false negative
// lets a meaningless total through unremarked.
func TestIsHazardName(t *testing.T) {
	cases := []struct {
		name string
		want bool
	}{
		{name: "单位", want: true},
		{name: "金额单位", want: true},
		{name: "币种", want: true},
		{name: "本位币", want: true},
		{name: "期末汇率", want: true},
		{name: "口径", want: true},
		{name: "unit", want: true},
		{name: "unit_price", want: true},
		{name: "Currency", want: true},
		{name: "exchange_rate", want: true},
		// Substring matches must not fire inside unrelated English words.
		{name: "opportunity", want: false},
		{name: "corporate", want: false},
		{name: "销售区域", want: false},
		{name: "客户名称", want: false},
		{name: "", want: false},
	}
	for _, tc := range cases {
		if got := isHazardName(tc.name); got != tc.want {
			t.Errorf("isHazardName(%q) = %v, want %v", tc.name, got, tc.want)
		}
	}
}

// A column with a single value cannot make a total incomparable, and one with
// hundreds of values is an identifier rather than a dimension; neither belongs
// in the report.
func TestBuildUnaccountedReport(t *testing.T) {
	tracked := map[int]*unaccountedColumn{
		0: {name: "单位", counts: map[string]int{"元": 2103, "千元": 597}},
		1: {name: "产品线", counts: map[string]int{"A": 5, "B": 3}},
		2: {name: "备注", counts: map[string]int{"关联方": 86}},             // single value
		3: {name: "凭证号", counts: map[string]int{"a": 1}, capped: true}, // too many
	}
	report := buildUnaccountedReport(tracked)
	if len(report) != 2 {
		t.Fatalf("report = %+v, want only 单位 and 产品线", report)
	}
	if report[0].Column != "单位" || !report[0].Suspected {
		t.Errorf("the unit column must sort first and be flagged: %+v", report[0])
	}
	if report[1].Column != "产品线" || report[1].Suspected {
		t.Errorf("an ordinary dimension must be listed but not flagged: %+v", report[1])
	}
	if got := report[0].Sample; len(got) != 2 || got[0] != "元" {
		t.Errorf("sample should be the most frequent first, got %v", got)
	}
}

// A file that cannot be opened has to be classified well enough that the
// caller knows what to do. Handing back the raw "zip: not a valid zip file"
// for an encrypted workbook sends the caller looking for corruption instead of
// asking for a password.
func TestSniffFile(t *testing.T) {
	dir := t.TempDir()
	write := func(name string, head []byte) string {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, head, 0o600); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
		return path
	}
	cases := []struct {
		name string
		path string
		want fileKind
	}{
		{name: "ole", path: write("enc.xlsx",
			[]byte{0xd0, 0xcf, 0x11, 0xe0, 0xa1, 0xb1, 0x1a, 0xe1, 0, 0}), want: kindOLE},
		{name: "zip", path: write("book.xlsx",
			[]byte{'P', 'K', 0x03, 0x04, 0, 0, 0, 0}), want: kindPackage},
		{name: "empty zip", path: write("empty.xlsx",
			[]byte{'P', 'K', 0x05, 0x06, 0, 0, 0, 0}), want: kindPackage},
		{name: "png", path: write("pic.png",
			[]byte{0x89, 'P', 'N', 'G', 0x0d, 0x0a, 0x1a, 0x0a}), want: kindOther},
		{name: "too short", path: write("tiny", []byte{'P', 'K'}), want: kindOther},
		{name: "missing", path: filepath.Join(dir, "nope"), want: kindOther},
	}
	for _, tc := range cases {
		if got := sniffFile(tc.path); got != tc.want {
			t.Errorf("sniffFile(%s) = %v, want %v", tc.name, got, tc.want)
		}
	}
}

// The derived errors must reach the caller as codes they can branch on.
func TestDerivedErrorCodes(t *testing.T) {
	cases := []struct {
		err  error
		want string
	}{
		{err: errPasswordRequired, want: codePasswordRequired},
		{err: errNotSpreadsheet, want: codeBadFormat},
		{err: excelize.ErrWorkbookPassword, want: codeInvalidPassword},
		{err: excelize.ErrWorkbookFileFormat, want: codeBadFormat},
		{err: excelize.ErrSheetNotExist{SheetName: "x"}, want: codeSheetNotFound},
		{err: errors.New("something else"), want: codeRead},
	}
	for _, tc := range cases {
		if got, _ := classify(tc.err); got != tc.want {
			t.Errorf("classify(%v) = %q, want %q", tc.err, got, tc.want)
		}
	}
}

// withSession takes over the package-level session state so a test can drive
// the cache, and puts everything back afterwards.
func withSession(t *testing.T, limit int, body func()) {
	t.Helper()
	saved := struct {
		enabled  bool
		books    []workbookEntry
		limit    int
		started  time.Time
		requests int
	}{sessionEnabled, sessionWorkbooks, sessionCacheLimit, sessionStarted, sessionRequests}
	defer func() {
		closeSessionWorkbooks()
		sessionEnabled, sessionWorkbooks = saved.enabled, saved.books
		sessionCacheLimit, sessionStarted, sessionRequests = saved.limit, saved.started, saved.requests
	}()
	sessionEnabled, sessionWorkbooks, sessionCacheLimit = true, nil, limit
	sessionStarted, sessionRequests = time.Now(), 0
	body()
}

func writeWorkbook(t *testing.T, path string) {
	t.Helper()
	f := excelize.NewFile()
	defer func() { _ = f.Close() }()
	if err := f.SaveAs(path); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

// A session must hand back the handle it already has rather than re-parsing,
// and must drop the least recently used one when it runs out of room — that
// eviction is what bounds the memory a long-lived session uses.
func TestSessionCacheReusesAndEvicts(t *testing.T) {
	dir := t.TempDir()
	paths := make([]string, 3)
	for i := range paths {
		paths[i] = filepath.Join(dir, fmt.Sprintf("book%d.xlsx", i))
		writeWorkbook(t, paths[i])
	}

	withSession(t, 2, func() {
		first, err := openWorkbook(paths[0], workbookOptions{})
		if err != nil {
			t.Fatalf("open: %v", err)
		}
		again, err := openWorkbook(paths[0], workbookOptions{})
		if err != nil {
			t.Fatalf("reopen: %v", err)
		}
		if first != again {
			t.Error("the same workbook should come back from the cache, not be re-parsed")
		}

		for _, path := range paths[1:] {
			if _, err = openWorkbook(path, workbookOptions{}); err != nil {
				t.Fatalf("open %s: %v", path, err)
			}
		}
		cached := cachedWorkbookPaths()
		if len(cached) != 2 {
			t.Fatalf("cache holds %d workbooks, want 2: %v", len(cached), cached)
		}
		if cached[0] != paths[2] {
			t.Errorf("most recently used should be first, got %v", cached)
		}

		// paths[0] fell out of the cache, so opening it must parse afresh.
		reopened, err := openWorkbook(paths[0], workbookOptions{})
		if err != nil {
			t.Fatalf("reopen after eviction: %v", err)
		}
		if reopened == first {
			t.Error("an evicted workbook must be re-parsed, not handed back")
		}
	})
}

// Outside a session nothing is cached: every command opens and closes its own
// workbook, which is what keeps the one-shot CLI self-contained.
func TestNoCacheOutsideSession(t *testing.T) {
	path := filepath.Join(t.TempDir(), "book.xlsx")
	writeWorkbook(t, path)

	first, err := openWorkbook(path, workbookOptions{})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	second, err := openWorkbook(path, workbookOptions{})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if first == second {
		t.Error("without a session each open must be independent")
	}
	if entries := cachedWorkbookPaths(); len(entries) != 0 {
		t.Errorf("nothing should be cached outside a session, got %v", entries)
	}
	releaseWorkbook(first)
	releaseWorkbook(second)
}

func TestPingReportsSessionState(t *testing.T) {
	path := filepath.Join(t.TempDir(), "book.xlsx")
	writeWorkbook(t, path)

	withSession(t, 4, func() {
		if _, err := openWorkbook(path, workbookOptions{}); err != nil {
			t.Fatalf("open: %v", err)
		}
		sessionRequests = 7
		data := pingData()
		if data["status"] != "ok" {
			t.Errorf("status = %v, want ok", data["status"])
		}
		if data["requests"] != 7 {
			t.Errorf("requests = %v, want 7", data["requests"])
		}
		paths, ok := data["cached_workbooks"].([]string)
		if !ok || len(paths) != 1 || paths[0] != path {
			t.Errorf("cached_workbooks = %v, want [%s]", data["cached_workbooks"], path)
		}
	})

	// With nothing open the field is omitted rather than reported as null.
	withSession(t, 4, func() {
		if _, present := pingData()["cached_workbooks"]; present {
			t.Error("cached_workbooks should be absent when nothing is open")
		}
	})
}

// A caller feeding a model needs to see the cost of a response before it
// accumulates, and needs to be told plainly when one would dominate a context
// window rather than left to notice a number.
func TestSizeWarning(t *testing.T) {
	for _, small := range []int{0, 1024, largeResponseBytes - 1} {
		if got := sizeWarning(small); got != "" {
			t.Errorf("sizeWarning(%d) = %q, want empty", small, got)
		}
	}
	got := sizeWarning(largeResponseBytes)
	if got == "" {
		t.Fatal("a response at the threshold should warn")
	}
	for _, want := range []string{"--columns", "--limit", "--format tsv"} {
		if !strings.Contains(got, want) {
			t.Errorf("the warning should name %s as a way out, got %q", want, got)
		}
	}
}

// The data payload is marshalled before the envelope so its size can be
// reported; embedding it as RawMessage must not change it.
func TestEnvelopeReportsDataBytes(t *testing.T) {
	data := map[string]any{"rows": []string{"> 23 Inch", "A&B"}, "n": 3}
	encoded, err := marshalNoEscape(data)
	if err != nil {
		t.Fatalf("marshalNoEscape: %v", err)
	}
	if bytes.HasSuffix(encoded, []byte("\n")) {
		t.Error("the size must not count a trailing newline")
	}
	if bytes.Contains(encoded, []byte(`\u003`)) || bytes.Contains(encoded, []byte(`\u002`)) {
		t.Errorf("data should not be HTML-escaped: %s", encoded)
	}

	out, err := marshalNoEscape(envelope{
		OK: true, Command: "read", Version: version,
		DataBytes: len(encoded), Data: encoded,
	})
	if err != nil {
		t.Fatalf("envelope: %v", err)
	}
	var parsed struct {
		DataBytes int             `json:"data_bytes"`
		Data      json.RawMessage `json:"data"`
	}
	if err = json.Unmarshal(out, &parsed); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if parsed.DataBytes != len(encoded) {
		t.Errorf("data_bytes = %d, want %d", parsed.DataBytes, len(encoded))
	}
	if string(parsed.Data) != string(encoded) {
		t.Errorf("the payload changed in transit:\n got %s\nwant %s", parsed.Data, encoded)
	}
}

// Grouping by a near-unique column returns the table with extra steps; the
// warning exists to say so before the cost lands in a caller's context.
func TestGroupWarning(t *testing.T) {
	if got := groupWarning(11, 0, false); got != "" {
		t.Errorf("a small grouping needs no warning, got %q", got)
	}
	if got := groupWarning(aggGroupWarnThreshold, 0, false); got != "" {
		t.Errorf("at the threshold there is still nothing to say, got %q", got)
	}
	got := groupWarning(aggGroupWarnThreshold+1, 0, true)
	if got == "" {
		t.Fatal("an unbounded large grouping should warn")
	}
	if !strings.Contains(got, "--limit") {
		t.Errorf("the warning should name the way out, got %q", got)
	}
	if got := groupWarning(50000, 100, true); got != "" {
		t.Errorf("a caller who set --limit has already handled it, got %q", got)
	}
	// A cap the caller did not ask for is a truncation, and the caller has to
	// be told which it is holding: the default cut the list, not their choice.
	capped := groupWarning(aggGroupWarnThreshold+1, aggDefaultGroupLimit, false)
	if capped == "" {
		t.Fatal("a grouping cut off by the default limit should say so")
	}
	for _, want := range []string{"--limit", "next_offset", "1000"} {
		if !strings.Contains(capped, want) {
			t.Errorf("the warning should mention %q, got %q", want, capped)
		}
	}
}

func TestLooksLikeDate(t *testing.T) {
	cases := []struct {
		in   string
		want bool
	}{
		{in: "2024-01-01", want: true},
		{in: "2024/01/01", want: true},
		{in: "2024-01-01 12:30", want: true},
		{in: "20240101", want: false},
		{in: "2024-1-1", want: false},
		{in: "not a date", want: false},
		{in: "", want: false},
		{in: "1234.5", want: false},
	}
	for _, tc := range cases {
		if got := looksLikeDate(tc.in); got != tc.want {
			t.Errorf("looksLikeDate(%q) = %v, want %v", tc.in, got, tc.want)
		}
	}
}

// marshalLikeCLI encodes v exactly the way writeEnvelope does. Using
// json.Marshal here instead would HTML-escape the output itself and mask the
// very bug these tests exist to catch.
func marshalLikeCLI(t *testing.T, v any) string {
	t.Helper()
	var buf bytes.Buffer
	encoder := json.NewEncoder(&buf)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(v); err != nil {
		t.Fatalf("Encode: %v", err)
	}
	return strings.TrimRight(buf.String(), "\n")
}

func TestParseFilter(t *testing.T) {
	cases := []struct {
		expr   string
		col    string
		op     string
		value  string
		isNum  bool
		number float64
		wantOK bool
	}{
		{expr: "金额>1000", col: "金额", op: ">", value: "1000", isNum: true, number: 1000, wantOK: true},
		// The two-character operators must win over their one-character
		// prefixes, so "A>=5" has to parse as ">=" and not as ">".
		{expr: "A>=5", col: "A", op: ">=", value: "5", isNum: true, number: 5, wantOK: true},
		{expr: "A<=5", col: "A", op: "<=", value: "5", isNum: true, number: 5, wantOK: true},
		{expr: "status!=done", col: "status", op: "!=", value: "done", wantOK: true},
		{expr: "status=done", col: "status", op: "=", value: "done", wantOK: true},
		{expr: "name~张", col: "name", op: "~", value: "张", wantOK: true},
		{expr: `备注="has space"`, col: "备注", op: "=", value: "has space", wantOK: true},
		{expr: "", wantOK: false},
		{expr: "金额", wantOK: false},
		{expr: ">1000", wantOK: false},
	}
	for _, tc := range cases {
		got, err := parseFilter(tc.expr)
		if tc.wantOK != (err == nil) {
			t.Errorf("parseFilter(%q) error = %v, wantOK = %v", tc.expr, err, tc.wantOK)
			continue
		}
		if !tc.wantOK {
			continue
		}
		if got.col != tc.col || got.op != tc.op || got.value != tc.value {
			t.Errorf("parseFilter(%q) = {%q %q %q}, want {%q %q %q}",
				tc.expr, got.col, got.op, got.value, tc.col, tc.op, tc.value)
		}
		if got.isNum != tc.isNum {
			t.Errorf("parseFilter(%q).isNum = %v, want %v", tc.expr, got.isNum, tc.isNum)
		}
		if tc.isNum && got.num != tc.number {
			t.Errorf("parseFilter(%q).num = %v, want %v", tc.expr, got.num, tc.number)
		}
	}
}

func TestFilterMatch(t *testing.T) {
	cases := []struct {
		expr string
		cell string
		want bool
	}{
		// A numeric filter against a cell that is not a number must fail to
		// match rather than falling back to comparing text.
		{expr: "金额>1000", cell: "2000", want: true},
		{expr: "金额>1000", cell: "500", want: false},
		{expr: "金额>1000", cell: "无", want: false},
		{expr: "金额>=1000", cell: "1,000", want: true},
		// Equality and substring are case-insensitive.
		{expr: "status=done", cell: "DONE", want: true},
		{expr: "status!=done", cell: "DONE", want: false},
		{expr: "status!=done", cell: "other", want: true},
		{expr: "name~张", cell: "张三", want: true},
		{expr: "name~张", cell: "李四", want: false},
		// A non-numeric filter falls back to a lexicographic comparison.
		{expr: "name>m", cell: "z", want: true},
		{expr: "name>m", cell: "a", want: false},
	}
	for _, tc := range cases {
		flt, err := parseFilter(tc.expr)
		if err != nil {
			t.Fatalf("parseFilter(%q): %v", tc.expr, err)
		}
		if got := flt.match(tc.cell); got != tc.want {
			t.Errorf("match(%q, %q) = %v, want %v", tc.expr, tc.cell, got, tc.want)
		}
	}
}

func TestMatchFiltersANDsClauses(t *testing.T) {
	filters, err := parseFilterList([]string{"金额>1000", "status=done"})
	if err != nil {
		t.Fatalf("parseFilterList: %v", err)
	}
	for i, flt := range filters {
		flt.idx = i
	}
	if !matchFilters(filters, []string{"5000", "DONE"}, nil) {
		t.Error("both clauses hold, want match")
	}
	if matchFilters(filters, []string{"5000", "open"}, nil) {
		t.Error("second clause fails, want no match")
	}
	if matchFilters(filters, []string{"10", "DONE"}, nil) {
		t.Error("first clause fails, want no match")
	}
	// A row shorter than the filter's column index reads as empty.
	if matchFilters(filters, []string{"5000"}, nil) {
		t.Error("missing cell should not satisfy status=done")
	}
}

func TestParseNumber(t *testing.T) {
	cases := []struct {
		in     string
		want   float64
		wantOK bool
	}{
		{in: "1000", want: 1000, wantOK: true},
		{in: "1,000", want: 1000, wantOK: true},
		{in: " 42 ", want: 42, wantOK: true},
		{in: "-3.5", want: -3.5, wantOK: true},
		{in: "", wantOK: false},
		{in: "abc", wantOK: false},
		// A number format decorates the stored value, and the decoration is
		// read with it — on either side, and in either sign position, because
		// "-¥1,234.50" and "¥-1,234.50" are both things a format can produce.
		{in: "¥100", want: 100, wantOK: true},
		{in: "¥1,234.50", want: 1234.5, wantOK: true},
		{in: "-¥1,234.50", want: -1234.5, wantOK: true},
		{in: "¥-1,234.50", want: -1234.5, wantOK: true},
		{in: "1,234.50€", want: 1234.5, wantOK: true},
		{in: "￥100", want: 100, wantOK: true},
		// A percent sign is a scale rather than decoration: it divides, so that
		// the display value and the stored value agree.
		{in: "50%", want: 0.5, wantOK: true},
		{in: "12.35%", want: 0.1235, wantOK: true},
		{in: "-2.5%", want: -0.025, wantOK: true},
		// Text that merely mentions an amount stays text.
		{in: "50% off", wantOK: false},
		{in: "USD 100", wantOK: false},
		{in: "¥", wantOK: false},
		{in: "¥$100", wantOK: false},
	}
	for _, tc := range cases {
		got, ok := parseNumber(tc.in)
		if ok != tc.wantOK || (ok && got != tc.want) {
			t.Errorf("parseNumber(%q) = (%v, %v), want (%v, %v)", tc.in, got, ok, tc.want, tc.wantOK)
		}
	}
}

func TestParseNumberUnitReportsCurrency(t *testing.T) {
	cases := []struct {
		in     string
		want   float64
		unit   string
		wantOK bool
	}{
		{in: "¥1,234.50", want: 1234.5, unit: "¥", wantOK: true},
		{in: "$1,234.50", want: 1234.5, unit: "$", wantOK: true},
		{in: "1,234.50€", want: 1234.5, unit: "€", wantOK: true},
		{in: "￥100", want: 100, unit: "￥", wantOK: true},
		// A percent sign scales the value and is not a currency of its own.
		{in: "12.35%", want: 0.1235, unit: "", wantOK: true},
		{in: "1234.5", want: 1234.5, unit: "", wantOK: true},
	}
	for _, tc := range cases {
		got, unit, ok := parseNumberUnit(tc.in)
		if ok != tc.wantOK || unit != tc.unit || (ok && got != tc.want) {
			t.Errorf("parseNumberUnit(%q) = (%v, %q, %v), want (%v, %q, %v)",
				tc.in, got, unit, ok, tc.want, tc.unit, tc.wantOK)
		}
	}
}

// A filtered comparison must not fall back to comparing strings once the value
// carries a number format: "5.00%" is not greater than "20%" under any reading
// of the two, but it is greater lexicographically, and that is how a filter
// silently returns the wrong rows.
func TestFilterMatchesFormattedNumbers(t *testing.T) {
	cases := []struct {
		expr string
		cell string
		want bool
	}{
		{expr: "比率>20%", cell: "25.67%", want: true},
		{expr: "比率>20%", cell: "5.00%", want: false},
		{expr: "比率<20%", cell: "5.00%", want: true},
		{expr: "比率>2%", cell: "5.00%", want: true},
		{expr: "比率>0.2", cell: "12.35%", want: false},
		{expr: "金额>¥2000", cell: "¥2,345.25", want: true},
		{expr: "金额>¥2000", cell: "¥999.99", want: false},
		{expr: "金额>2000", cell: "¥2,345.25", want: true},
		{expr: "金额>2000", cell: "$2,345.25", want: true},
		// A text comparison is still a text comparison.
		{expr: "名称>A", cell: "B", want: true},
	}
	for _, tc := range cases {
		flt, err := parseFilter(tc.expr)
		if err != nil {
			t.Fatalf("parseFilter(%q): %v", tc.expr, err)
		}
		if got := flt.match(tc.cell); got != tc.want {
			t.Errorf("%q matches %q = %v, want %v", tc.expr, tc.cell, got, tc.want)
		}
	}
}

func TestColumnWarnings(t *testing.T) {
	// Cells that are all empty: the aggregates are null, and the likeliest
	// reason — a formula with no cached result — comes with a way to fix it.
	got := columnWarnings([]string{"公式列"}, []int{0}, []int{0}, []map[string]bool{nil}, []bool{true}, 2, false)
	if len(got) != 1 || !strings.Contains(got[0], "公式列") || !strings.Contains(got[0], "--calc") {
		t.Errorf("expected a column warning naming the column and --calc, got %q", got)
	}
	// Already asked for --calc: state the fact without repeating the advice.
	got = columnWarnings([]string{"公式列"}, []int{0}, []int{0}, []map[string]bool{nil}, []bool{true}, 2, true)
	if len(got) != 1 || strings.Contains(got[0], "--calc") {
		t.Errorf("expected a hint-free column warning, got %q", got)
	}
	// A filled text column is not an empty one, and must not be sent down the
	// --calc path: that would cost a whole-sheet parse to learn nothing.
	got = columnWarnings([]string{"备注"}, []int{0}, []int{86}, []map[string]bool{nil}, []bool{true}, 2700, false)
	if len(got) != 1 || !strings.Contains(got[0], "text, not numbers") || strings.Contains(got[0], "--calc") {
		t.Errorf("expected a text-column warning without the --calc advice, got %q", got)
	}
	// Every cell parsed, but two currencies were summed together.
	got = columnWarnings([]string{"金额"}, []int{2}, []int{2}, []map[string]bool{{"¥": true, "$": true}}, []bool{true}, 2, false)
	if len(got) != 1 || !strings.Contains(got[0], "currencies") {
		t.Errorf("expected a mixed-currency warning, got %q", got)
	}
	// A count-distinct column is never read as a number, so a text column is
	// not a missing one.
	if got = columnWarnings([]string{"客户"}, []int{0}, []int{5}, []map[string]bool{nil}, []bool{false}, 2, false); got != nil {
		t.Errorf("count-distinct column should not be warned about, got %q", got)
	}
	// Nothing matched: the empty result is the message.
	if got = columnWarnings([]string{"金额"}, []int{0}, []int{0}, []map[string]bool{nil}, []bool{true}, 0, false); got != nil {
		t.Errorf("no matched rows should produce no column warnings, got %q", got)
	}
}

func TestBlankGroupWarning(t *testing.T) {
	groups := map[string]*groupState{
		"华北": {keys: []string{"华北"}, rows: 1},
		"":   {keys: []string{""}, rows: 2},
	}
	order := []string{"", "华北"}
	warning := blankGroupWarning(groups, order, []string{"区域"}, false)
	if !strings.Contains(warning, "2 rows") || !strings.Contains(warning, "--fill-merged") {
		t.Errorf("expected the blank rows to be counted and --fill-merged suggested, got %q", warning)
	}
	// With the flag already given there is nothing left to suggest, but the
	// rows still went into an empty group and the count still stands.
	if warning = blankGroupWarning(groups, order, []string{"区域"}, true); !strings.Contains(warning, "2 rows") ||
		strings.Contains(warning, "--fill-merged") {
		t.Errorf("expected a hint-free blank-group warning, got %q", warning)
	}
	// A grand total groups nothing, so there is no key to be blank.
	if warning = blankGroupWarning(groups, order, nil, false); warning != "" {
		t.Errorf("an ungrouped aggregate should not warn about blank keys, got %q", warning)
	}
}

func TestHeaderKeys(t *testing.T) {
	cases := []struct {
		name   string
		header []string
		width  int
		want   []string
	}{
		{name: "as-is", header: []string{"name", "age"}, width: 2, want: []string{"name", "age"}},
		{name: "blank falls back to column letter", header: []string{"", "x"}, width: 2, want: []string{"A", "x"}},
		{name: "duplicates get a suffix", header: []string{"a", "a"}, width: 2, want: []string{"a", "a_2"}},
		{name: "no header at all", header: nil, width: 2, want: []string{"A", "B"}},
		{name: "wider than the header", header: []string{"a"}, width: 3, want: []string{"a", "B", "C"}},
	}
	for _, tc := range cases {
		got := headerKeys(tc.header, tc.width)
		if len(got) != len(tc.want) {
			t.Errorf("%s: headerKeys(%v, %d) = %v, want %v", tc.name, tc.header, tc.width, got, tc.want)
			continue
		}
		for i := range got {
			if got[i] != tc.want[i] {
				t.Errorf("%s: headerKeys(%v, %d) = %v, want %v", tc.name, tc.header, tc.width, got, tc.want)
				break
			}
		}
	}
}

func TestPadRow(t *testing.T) {
	got := padRow([]string{"a"}, 3)
	if len(got) != 3 || got[0] != "a" || got[1] != "" || got[2] != "" {
		t.Errorf("padRow short = %v, want [a  ]", got)
	}
	got = padRow([]string{"a", "b", "c"}, 2)
	if len(got) != 2 || got[0] != "a" || got[1] != "b" {
		t.Errorf("padRow long = %v, want [a b]", got)
	}
	got = padRow([]string{"a", "b"}, 2)
	if len(got) != 2 || got[0] != "a" || got[1] != "b" {
		t.Errorf("padRow exact = %v, want [a b]", got)
	}
}

func TestResolveColumn(t *testing.T) {
	header := []string{"名称", "金额"}
	cases := []struct {
		ref    string
		want   int
		wantOK bool
	}{
		{ref: "名称", want: 0, wantOK: true},
		{ref: "金额", want: 1, wantOK: true},
		{ref: "金额 ", want: 1, wantOK: true},
		{ref: "A", want: 0, wantOK: true},
		{ref: "b", want: 1, wantOK: true},
		{ref: "1", want: 0, wantOK: true},
		{ref: "2", want: 1, wantOK: true},
		{ref: "!!!", wantOK: false},
		{ref: "0", wantOK: false},
		{ref: "", wantOK: false},
	}
	for _, tc := range cases {
		got, err := resolveColumn(tc.ref, header)
		if tc.wantOK != (err == nil) {
			t.Errorf("resolveColumn(%q) error = %v, wantOK = %v", tc.ref, err, tc.wantOK)
			continue
		}
		if tc.wantOK && got != tc.want {
			t.Errorf("resolveColumn(%q) = %d, want %d", tc.ref, got, tc.want)
		}
	}
	// Without a header a name is not resolvable, but letters and indexes are.
	if _, err := resolveColumn("名称", nil); err == nil {
		t.Error("resolveColumn(\"名称\", nil) should fail without a header")
	}
	if got, err := resolveColumn("B", nil); err != nil || got != 1 {
		t.Errorf("resolveColumn(\"B\", nil) = (%d, %v), want (1, nil)", got, err)
	}
}

// Real sheets pad header cells with spaces. info reports such a column as
// "名称" because headerKeys trims it, so asking for "名称" has to work — a tool
// that hands out a name and then refuses it leaves the caller guessing.
func TestResolveColumnTrimsPaddedHeaders(t *testing.T) {
	header := []string{"  代码 ", "\t名称 ", "  "}
	cases := []struct {
		ref  string
		want int
	}{
		{ref: "名称", want: 1},
		{ref: " 名称", want: 1},
		{ref: "代码", want: 0},
	}
	for _, tc := range cases {
		got, err := resolveColumn(tc.ref, header)
		if err != nil || got != tc.want {
			t.Errorf("resolveColumn(%q, padded) = (%d, %v), want (%d, nil)", tc.ref, got, err, tc.want)
		}
	}
	// A header of nothing but whitespace is not a name at all.
	if _, err := resolveColumn("", header); err == nil {
		t.Error("an empty reference should still fail")
	}
}

func TestParseDimension(t *testing.T) {
	cases := []struct {
		ref       string
		rows, col int
	}{
		{ref: "A1:F100", rows: 100, col: 6},
		{ref: "$A$1:$F$100", rows: 100, col: 6},
		{ref: "A1", rows: 1, col: 1},
		{ref: "", rows: 0, col: 0},
		{ref: "nonsense", rows: 0, col: 0},
	}
	for _, tc := range cases {
		rows, cols := parseDimension(tc.ref)
		if rows != tc.rows || cols != tc.col {
			t.Errorf("parseDimension(%q) = (%d, %d), want (%d, %d)",
				tc.ref, rows, cols, tc.rows, tc.col)
		}
	}
}

func TestSplitList(t *testing.T) {
	if got := splitList(""); got != nil {
		t.Errorf("splitList(\"\") = %v, want nil", got)
	}
	got := splitList(" a , b ,, ")
	if len(got) != 2 || got[0] != "a" || got[1] != "b" {
		t.Errorf("splitList = %v, want [a b]", got)
	}
}

func TestColumnLetter(t *testing.T) {
	if got := columnLetter(1); got != "A" {
		t.Errorf("columnLetter(1) = %q, want A", got)
	}
	if got := columnLetter(27); got != "AA" {
		t.Errorf("columnLetter(27) = %q, want AA", got)
	}
}

// orderedRow exists so that column order survives marshalling; a plain map
// would be sorted alphabetically by encoding/json.
func TestOrderedRowPreservesColumnOrder(t *testing.T) {
	row := orderedRow{keys: []string{"z", "a", "m"}, vals: []any{"1", "2", "3"}}
	if got, want := marshalLikeCLI(t, row), `{"z":"1","a":"2","m":"3"}`; got != want {
		t.Errorf("Marshal(orderedRow) = %s, want %s", got, want)
	}
}

// Object-form rows must escape exactly like array-form rows. A spreadsheet
// value such as "> 23 Inch" or a company name containing an ampersand must not
// come out with numeric unicode escapes in one mode and not the other.
func TestOrderedRowDoesNotHTMLEscape(t *testing.T) {
	row := orderedRow{keys: []string{"a", "b"}, vals: []any{"> 23 Inch", "A&B"}}
	if got, want := marshalLikeCLI(t, row), `{"a":"> 23 Inch","b":"A&B"}`; got != want {
		t.Errorf("Marshal(orderedRow) = %s, want %s", got, want)
	}
	arrays := [][]string{{"> 23 Inch", "A&B"}}
	if got, want := marshalLikeCLI(t, arrays), `[["> 23 Inch","A&B"]]`; got != want {
		t.Errorf("Marshal(array rows) = %s, want %s", got, want)
	}
}

// An aggregate must come back as a JSON number, not a quoted string, so that a
// caller does not have to re-parse it before doing arithmetic.
func TestOrderedRowEmitsNumbers(t *testing.T) {
	row := orderedRow{
		keys: []string{"region", "total"},
		vals: []any{"华北", 2839456194.25},
	}
	if got, want := marshalLikeCLI(t, row), `{"region":"华北","total":2839456194.25}`; got != want {
		t.Errorf("Marshal(orderedRow) = %s, want %s", got, want)
	}
}

// The first operator decides where the column ends, and operators are matched
// whole: "!=" is one operator, and the "!" of a value like "hello!" is not an
// operator at all. Matching by leading character got "status!=done" wrong.
func TestOperatorAt(t *testing.T) {
	cases := []struct {
		in string
		at int
		op string
	}{
		{in: "status!=done", at: 6, op: "!="},
		{in: "金额>1000", at: 6, op: ">"},
		{in: "金额 >= 100", at: 7, op: ">="},
		// ">>=" is ">" followed by ">=": the second one is a second operator,
		// which is what makes it a syntax error rather than a comparison.
		{in: "金额 >>= 100", at: 7, op: ">"},
		{in: "备注=hello!", at: 6, op: "="},
		{in: "状态~完成", at: 6, op: "~"},
		{in: "金额", at: -1, op: ""},
		{in: "", at: -1, op: ""},
	}
	for _, tc := range cases {
		at, op := operatorAt(tc.in)
		if at != tc.at || op != tc.op {
			t.Errorf("operatorAt(%q) = (%d, %q), want (%d, %q)", tc.in, at, op, tc.at, tc.op)
		}
	}
}

// Every expression here was accepted before 1.0.3 and returned ok:true with a
// row count that looked like an answer — "金额>" matched the whole sheet,
// because every cell compares greater than the empty string.
func TestParseFilterRejectsMalformedExpressions(t *testing.T) {
	for _, expr := range []string{
		"金额>>100", "金额>", "金额>100>200", "金额>=>100", "金额>>=100", "金额==>100", "金额",
	} {
		if _, err := parseFilter(expr); err == nil {
			t.Errorf("parseFilter(%q) was accepted; a malformed filter must be rejected", expr)
		}
	}
}

// Strict does not mean inexpressible: quoting is how a value that really holds
// an operator is written, and an empty quoted value still selects blank cells.
func TestParseFilterQuotingEscapesTheGrammar(t *testing.T) {
	cases := []struct {
		in    string
		value string
	}{
		{in: "备注~'a>b'", value: "a>b"},
		{in: `备注~"a=b"`, value: "a=b"},
		{in: "状态='完成'", value: "完成"},
		{in: "备注=''", value: ""},
	}
	for _, tc := range cases {
		flt, err := parseFilter(tc.in)
		if err != nil {
			t.Errorf("parseFilter(%q): %v", tc.in, err)
			continue
		}
		if flt.value != tc.value {
			t.Errorf("parseFilter(%q).value = %q, want %q", tc.in, flt.value, tc.value)
		}
	}
}

// A filter value written as a percentage is scaled by 0.01 when it is parsed,
// and that product is one ulp away from the decimal a cell stores. Equality has
// to absorb that, or "比率=25.67%" finds nothing while "比率>25.66%" finds the row.
func TestFilterEqualityIgnoresOneUlp(t *testing.T) {
	flt, err := parseFilter("比率=25.67%")
	if err != nil {
		t.Fatalf("parseFilter: %v", err)
	}
	if !flt.match("25.67%") {
		t.Error("比率=25.67% did not match a cell showing 25.67%")
	}
	if !flt.match("0.2567") {
		t.Error("比率=25.67% did not match the stored value 0.2567 (the --raw path)")
	}
	if flt.match("0.2568") {
		t.Error("比率=25.67% matched 0.2568; the 15-digit comparison is too wide")
	}
}

// The comparison that answers in text what was asked in numbers is the shape a
// mistyped numeric literal takes, and the rows it matches are unrelated to the
// question. It has to be visible; comparing text with text is normal and is not.
func TestFilterReportsTextFallbackAgainstNumbers(t *testing.T) {
	numeric, err := parseFilter("金额>1OO")
	if err != nil {
		t.Fatalf("parseFilter: %v", err)
	}
	for _, cell := range []string{"100", "1000", "2000"} {
		numeric.match(cell)
	}
	warnings := filterWarnings([]*filter{numeric})
	if len(warnings) != 1 {
		t.Fatalf("got %d warnings, want 1: %v", len(warnings), warnings)
	}
	for _, want := range []string{"1OO", "金额", "3 cell"} {
		if !strings.Contains(warnings[0], want) {
			t.Errorf("warning %q does not mention %q", warnings[0], want)
		}
	}

	text, _ := parseFilter("状态=完成")
	text.match("完成")
	if got := filterWarnings([]*filter{text}); len(got) != 0 {
		t.Errorf("a text filter against a text column warned: %v", got)
	}

	// A date range is the reason text comparison exists.
	dates, _ := parseFilter("日期>2024-08-21")
	dates.match("2024-09-01")
	if got := filterWarnings([]*filter{dates}); len(got) != 0 {
		t.Errorf("a date range warned: %v", got)
	}
}

// A bracket inside an operand belongs to the name, so the field this tool
// reports for --sum "[金额(万元)]" can be referenced by --derive. The brackets
// also nest, because an expression may be bracketed as a whole.
func TestBracketScanningHandlesNesting(t *testing.T) {
	// Byte offsets: each CJK character is three of them, so the "]" that closes
	// the bracket at index 4 sits at 19.
	if got, want := closingBracket("sum_[金额(万元)]-1", 4), 19; got != want {
		t.Errorf("closingBracket = %d, want %d", got, want)
	}
	if got := closingBracket("sum_[金额", 4); got != -1 {
		t.Errorf("an unclosed bracket should report -1, got %d", got)
	}
	if _, err := parseExpr("sum_[金额(万元)]-sum_[成本(万元)]"); err != nil {
		t.Errorf("an aggregate field with a bracketed column should parse: %v", err)
	}
	if _, err := parseExpr("[sum_[金额(万元)]]*2"); err != nil {
		t.Errorf("a doubly bracketed field should parse: %v", err)
	}

	// The bracket hint is for a bare name, not for a broken expression: telling
	// a caller who wrote arithmetic to wrap it in brackets answers a question
	// they did not ask, and the advice does not work when they take it.
	if !bareName("金额(万元)") {
		t.Error("a bracketed column name should count as a bare name")
	}
	if bareName("(收入-成本)/收入") {
		t.Error("an expression should not count as a bare name")
	}
	if got := bracketHint("(sum_[金额(万元)]-sum_[成本(万元)])/sum_[金额(万元)]"); got != "" {
		t.Errorf("the hint was offered for an expression: %q", got)
	}
	if got := bracketHint("金额(万元)"); !strings.Contains(got, "[金额(万元)]") {
		t.Errorf("the hint was not offered for a bracketed name: %q", got)
	}
}

// Which cells hold dates is decided by their number format, and the letters
// that mean a field also occur in the words a format can print.
func TestHasDateField(t *testing.T) {
	cases := []struct {
		code string
		want bool
	}{
		{code: "yyyy-mm-dd", want: true},
		{code: "mm-dd-yy", want: true},
		{code: "yyyy\"年\"m\"月\"", want: true},
		{code: "[$-409]d-mmm-yy", want: true},
		{code: "[h]:mm:ss", want: true}, // elapsed time is a field too
		{code: "#,##0.00", want: false},
		{code: "0.00%", want: false},
		{code: "¥#,##0.00", want: false},
		{code: `#,##0.00 "per day"`, want: false},
		{code: "[Red]#,##0", want: false},
		{code: "General", want: false},
		{code: "0.0_);(0.0)", want: false},
	}
	for _, tc := range cases {
		if got := hasDateField(tc.code); got != tc.want {
			t.Errorf("hasDateField(%q) = %v, want %v", tc.code, got, tc.want)
		}
	}

	// The built-in formats are fixed by the file format: 14-22 are dates and
	// times, 45-47 elapsed time, and everything else is a number or text.
	custom := "yyyy-mm-dd"
	builtin := []struct {
		id   int
		want bool
	}{
		{id: 14, want: true}, {id: 22, want: true}, {id: 45, want: true},
		{id: 0, want: false}, {id: 9, want: false}, {id: 49, want: false},
	}
	for _, tc := range builtin {
		if got := isDateFormat(&excelize.Style{NumFmt: tc.id}); got != tc.want {
			t.Errorf("isDateFormat(NumFmt %d) = %v, want %v", tc.id, got, tc.want)
		}
	}
	if !isDateFormat(&excelize.Style{NumFmt: 0, CustomNumFmt: &custom}) {
		t.Error("a custom date format should win over the built-in id")
	}
}

// "--ignore-case true" is what most people write first, and the "true" landing
// in the operand list produced an error about a workbook path. Only the two
// words are accepted: "1" and "0" would swallow an operand meant literally.
func TestIsBoolWord(t *testing.T) {
	cases := []struct {
		in   string
		want bool
	}{
		{in: "true", want: true},
		{in: "TRUE", want: true},
		{in: "False", want: true},
		{in: "false", want: true},
		{in: "1", want: false},
		{in: "0", want: false},
		{in: "yes", want: false},
		{in: "book.xlsx", want: false},
	}
	for _, tc := range cases {
		if got := isBoolWord(tc.in); got != tc.want {
			t.Errorf("isBoolWord(%q) = %v, want %v", tc.in, got, tc.want)
		}
	}
}

// A remark column holding a comma, a quote or a line break is exactly what a
// finance export contains, and the reader splitting on commas is the thing most
// likely to consume this.
func TestBuildCSVQuotesTheWayRFC4180Does(t *testing.T) {
	body := buildCSV([]string{"名称", "备注"}, [][]string{
		{"甲,乙", "行1\n行2"},
		{`说"明`, "x"},
	})
	want := "名称,备注\n\"甲,乙\",\"行1\n行2\"\n\"说\"\"明\",x\n"
	if body != want {
		t.Errorf("buildCSV =\n%q\nwant\n%q", body, want)
	}
}

// A pipe would end the cell early and shift every column after it; a line break
// would end the row.
func TestBuildMarkdownKeepsCellsInsideTheirColumns(t *testing.T) {
	body := buildMarkdown([]string{"a", "b"}, [][]string{{"x|y", "l1\nl2"}})
	want := "| a | b |\n| --- | --- |\n| x\\|y | l1 l2 |\n"
	if body != want {
		t.Errorf("buildMarkdown =\n%q\nwant\n%q", body, want)
	}
}

// ---------------------------------------------------------------------------
// Formulas (formula.go)
// ---------------------------------------------------------------------------

// scanWith scans a formula against a fixed header, and renders it with a cell
// reference for each column, which is what --col does per row.
func scanWith(t *testing.T, formula string, header ...string) (string, error) {
	t.Helper()
	ctx := &formulaContext{
		header: header,
		sheets: []string{"明细", "目标表"},
		flag:   "col",
	}
	parts, err := scanFormula(formula, ctx)
	if err != nil {
		return "", err
	}
	return parts.render(func(idx int) (string, error) {
		return "<" + columnLetter(idx+1) + ">", nil
	})
}

func TestScanFormulaRewritesColumnNames(t *testing.T) {
	cases := []struct {
		formula string
		want    string
	}{
		// A bare column name becomes a reference, and functions do not.
		{"MONTH(过账日期)", "MONTH(<B>)"},
		{"收入金额*IF(单位=\"千元\",1000,1)", "<J>*IF(<I>=\"千元\",1000,1)"},
		// A name inside a string literal is text, not a column.
		{"IF(单位=\"收入金额\",1,2)", "IF(<I>=\"收入金额\",1,2)"},
		// A sheet-qualified reference is left to the engine.
		{"VLOOKUP(客户编号,目标表!$A:$B,2,FALSE)", "VLOOKUP(<E>,目标表!$A:$B,2,FALSE)"},
		{"'目标表'!A1*收入金额", "'目标表'!A1*<J>"},
		// Cell references and numbers survive.
		{"A1*2+$B$3", "A1*2+$B$3"},
		{"SUM(B:B)", "SUM(B:B)"},
		// A name that holds a parenthesis is read as a whole when it matches.
		{"金额(万元)*2", "<C>*2"},
		{"[金额(万元)]*2", "<C>*2"},
		// A function the engine knows, with a name that is also a column,
		// resolves as the function: IF is not a column here.
		{"TRIM(备注)", "TRIM(<M>)"},
	}
	for _, test := range cases {
		got, err := scanWith(t, test.formula, "凭证号", "过账日期", "金额(万元)", "E",
			"客户编号", "F", "G", "H", "单位", "收入金额", "K", "L", "备注")
		if err != nil {
			t.Errorf("scanFormula(%q): %v", test.formula, err)
			continue
		}
		if got != test.want {
			t.Errorf("scanFormula(%q) = %q, want %q", test.formula, got, test.want)
		}
	}
}

func TestScanFormulaRejectsWhatCannotWork(t *testing.T) {
	cases := []struct {
		formula string
		message string
	}{
		// The same command twice must not answer differently.
		{"MONTH(NOW())", "NOW"},
		{"TODAY()", "TODAY"},
		{"RAND()", "RAND"},
		{"RANDBETWEEN(1,10)", "RANDBETWEEN"},
		// A typo is one message, not one per row.
		{"MONTH(过帐日期)", "过帐日期"},
		{"VLOOKUP(客户编号,不存在的表!$A:$B,2,FALSE)", "does not exist"},
		{"SUM(金额", "unbalanced"},
	}
	for _, test := range cases {
		_, err := scanWith(t, test.formula, "过账日期", "客户编号", "金额")
		if err == nil {
			t.Errorf("scanFormula(%q) accepted, want an error naming %q", test.formula, test.message)
			continue
		}
		if !strings.Contains(err.Error(), test.message) {
			t.Errorf("scanFormula(%q) = %v, want it to mention %q", test.formula, err, test.message)
		}
	}
}

func TestFormulaValueAtReadsDerivedColumns(t *testing.T) {
	cells := []string{"a", "b"}
	derived := []string{"x", "y"}
	// A derived column is numbered from -2, so that -1 stays "unresolved".
	if got := formulaValueAt(cells, derived, -2); got != "x" {
		t.Errorf("formulaValueAt(-2) = %q, want x", got)
	}
	if got := formulaValueAt(cells, derived, -3); got != "y" {
		t.Errorf("formulaValueAt(-3) = %q, want y", got)
	}
	if got := formulaValueAt(cells, derived, -1); got != "" {
		t.Errorf("formulaValueAt(-1) = %q, want an unresolved index to read empty", got)
	}
	if got := formulaValueAt(cells, derived, 1); got != "b" {
		t.Errorf("formulaValueAt(1) = %q, want b", got)
	}
}

func TestFormulaColumnNameRejectsAmbiguity(t *testing.T) {
	for _, name := range []string{"", "2", "B", "AA", "A1", "$B$2"} {
		if err := formulaColumnName(name); err == nil {
			t.Errorf("formulaColumnName(%q) accepted, want a rejection", name)
		}
	}
	for _, name := range []string{"月", "净利率", "share"} {
		if err := formulaColumnName(name); err != nil {
			t.Errorf("formulaColumnName(%q) = %v, want it accepted", name, err)
		}
	}
}

func TestAdditiveFieldsExcludeWhatHasNoTotal(t *testing.T) {
	specs := []*aggSpec{
		{kind: kindSum, name: "sum_收入"},
		{kind: kindAvg, name: "avg_收入"},
		{kind: kindMax, name: "max_收入"},
		{kind: kindCountDistinct, name: "distinct_客户"},
		{kind: kindSum, name: "收入"},
	}
	got := strings.Join(additiveFields(specs, true), ",")
	if got != "sum_收入,收入,count" {
		t.Errorf("additiveFields = %q, want the sums and the count", got)
	}
	if len(additiveFields(specs, false)) != 2 {
		t.Errorf("additiveFields without --count = %v, want two sums",
			additiveFields(specs, false))
	}
}

func TestFormulaResultKeepsTextAndDropsEmpty(t *testing.T) {
	cases := []struct {
		value string
		want  any
	}{
		{"120", float64(120)},
		{"1.5E+3", float64(1500)},
		{"", nil},
		{"2023-05-02", "2023-05-02"},
		{"VLOOKUP no result found", "VLOOKUP no result found"},
	}
	for _, test := range cases {
		got := formulaResult(test.value)
		if got != test.want {
			t.Errorf("formulaResult(%q) = %#v, want %#v", test.value, got, test.want)
		}
	}
}

func TestScratchValueReadsNumbersTheWaySumDoes(t *testing.T) {
	cases := []struct {
		cell string
		want any
	}{
		{"1,234.50", float64(1234.5)},
		{"12.35%", float64(0.1235)},
		{"¥100", float64(100)},
		{"华东", "华东"},
		{"", ""},
	}
	for _, test := range cases {
		if got := scratchValue(test.cell); got != test.want {
			t.Errorf("scratchValue(%q) = %#v, want %#v", test.cell, got, test.want)
		}
	}
}
