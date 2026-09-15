// Copyright (c) 2026 henryyu@163.com. All rights reserved.
//
// This file is part of xlpeek, a read-only spreadsheet reader for
// agents. See README.md for what it does and docs/AGENTS.md for how it is
// meant to be driven.

package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/xuri/excelize/v2"
)

// Exit codes. Any non-zero value means the run failed and the envelope on
// stdout carries "ok": false.
const (
	exitOK    = 0
	exitError = 1
	exitUsage = 2
)

// Error codes reported in the envelope's error.code field.
const (
	codeUsage            = "USAGE"
	codeFileNotFound     = "FILE_NOT_FOUND"
	codeSheetNotFound    = "SHEET_NOT_FOUND"
	codeInvalidPassword  = "INVALID_PASSWORD"
	codePasswordRequired = "PASSWORD_REQUIRED"
	codeBadFormat        = "UNSUPPORTED_FORMAT"
	codeColumnNotFound   = "COLUMN_NOT_FOUND"
	codeRead             = "READ_ERROR"
)

// Errors this layer derives from the shape of the file, so that a caller is
// told what to do rather than handed the raw failure from the zip reader.
var (
	errPasswordRequired = errors.New("this workbook is encrypted; supply its password with --password")
	errNotSpreadsheet   = errors.New("not a spreadsheet file: expected an XLSX/XLSM package or an encrypted workbook")
)

// envelope is the single JSON object every invocation writes to stdout.
//
// Data is carried as RawMessage because it is marshalled before the envelope is
// built, so that the response's own size can be reported in it. Marshalling it
// once and embedding the bytes also avoids serialising a large payload twice.
type envelope struct {
	OK      bool   `json:"ok"`
	Command string `json:"command"`
	Version string `json:"version"`
	// DataBytes is the size of this response's data in bytes. A caller feeding
	// a model can watch it and tighten --limit or --columns before the cost
	// accumulates; the envelope wrapper itself adds well under 100 bytes.
	DataBytes int `json:"data_bytes,omitempty"`
	// DataWarning states outright that a response is large enough to crowd out
	// a context window. The number alone is easy to skip past; the sentence is
	// not. It says nothing about correctness, only about size.
	DataWarning string          `json:"data_warning,omitempty"`
	Error       *cliError       `json:"error,omitempty"`
	Data        json.RawMessage `json:"data,omitempty"`
}

// sizeWarning returns a sentence when a response is big enough to matter, and
// nothing when it is not.
func sizeWarning(bytes int) string {
	if bytes < largeResponseBytes {
		return ""
	}
	return fmt.Sprintf(
		"this response is %d bytes; narrow it with --columns, a smaller --limit, or --format tsv",
		bytes)
}

// largeResponseBytes is where a response stops being something a caller can
// skim and starts being something that will dominate a context window.
const largeResponseBytes = 512 * 1024

// marshalNoEscape encodes v the way the outer encoder would: without HTML
// escaping, so that a value like "> 23 Inch" survives unmangled.
func marshalNoEscape(v any) ([]byte, error) {
	var buf bytes.Buffer
	encoder := json.NewEncoder(&buf)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(v); err != nil {
		return nil, err
	}
	return bytes.TrimRight(buf.Bytes(), "\n"), nil
}

type cliError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// orderedRow marshals as a JSON object that preserves worksheet column order.
// A plain map would be sorted alphabetically by encoding/json, which scrambles
// the columns for anyone reading the output. Values are untyped so that an
// aggregate can emit a real JSON number where a cell read emits a string.
type orderedRow struct {
	keys []string
	vals []any
}

func (r orderedRow) MarshalJSON() ([]byte, error) {
	var buf bytes.Buffer
	buf.WriteByte('{')
	encoder := json.NewEncoder(&buf)
	// Mirror the outer encoder's escaping. Left at its default, json.Marshal
	// rewrites angle brackets and ampersands into numeric unicode escapes:
	// still valid JSON, but ugly to read and, worse, inconsistent with the
	// array-form rows, which the outer encoder emits unescaped.
	encoder.SetEscapeHTML(false)
	for i, key := range r.keys {
		if i > 0 {
			buf.WriteByte(',')
		}
		if err := encodeValue(encoder, &buf, key); err != nil {
			return nil, err
		}
		buf.WriteByte(':')
		if err := encodeValue(encoder, &buf, r.vals[i]); err != nil {
			return nil, err
		}
	}
	buf.WriteByte('}')
	return buf.Bytes(), nil
}

// encodeValue appends the JSON form of v, dropping the trailing newline that
// json.Encoder.Encode always writes.
func encodeValue(encoder *json.Encoder, buf *bytes.Buffer, v any) error {
	before := buf.Len()
	if err := encoder.Encode(v); err != nil {
		return err
	}
	if buf.Len() > before {
		buf.Truncate(buf.Len() - 1)
	}
	return nil
}

// writeEnvelope serialises one envelope to stdout. Marshal failures are
// reported on stderr because stdout is already the failure channel here.
func writeEnvelope(e envelope, pretty bool) {
	enc := json.NewEncoder(os.Stdout)
	enc.SetEscapeHTML(false)
	if pretty {
		enc.SetIndent("", "  ")
	}
	if err := enc.Encode(e); err != nil {
		fmt.Fprintf(os.Stderr, "%s: cannot encode response: %v\n", tool, err)
	}
}

func respondOK(command string, data interface{}, pretty bool) int {
	encoded, err := marshalNoEscape(data)
	if err != nil {
		return respondErr(command, codeRead,
			"cannot encode response: "+err.Error(), pretty, exitError)
	}
	writeEnvelope(envelope{
		OK: true, Command: command, Version: version,
		DataBytes:   len(encoded),
		DataWarning: sizeWarning(len(encoded)),
		Data:        encoded,
	}, pretty)
	return exitOK
}

// respondTSV writes the envelope followed by a tab-separated body.
//
// The envelope still comes first and still carries the paging metadata, so a
// caller keeps one control-flow path (read line 1, follow next_offset) while
// the rows themselves avoid JSON's per-row quoting and repeated keys. Those
// two are what make tabular output materially cheaper to feed to a model.
func respondTSV(command string, data interface{}, body string, pretty bool) int {
	encoded, err := marshalNoEscape(data)
	if err != nil {
		return respondErr(command, codeRead,
			"cannot encode response: "+err.Error(), pretty, exitError)
	}
	// The body is the bulk of a TSV response, so it counts towards the size the
	// caller is being told about.
	total := len(encoded) + len(body)
	writeEnvelope(envelope{
		OK: true, Command: command, Version: version,
		DataBytes:   total,
		DataWarning: sizeWarning(total),
		Data:        encoded,
	}, pretty)
	if body != "" {
		fmt.Fprint(os.Stdout, body)
	}
	return exitOK
}

// tsvField renders one cell, quoting it the way CSV does when it contains a
// delimiter, a quote or a line break. Remark columns routinely carry embedded
// newlines, and flattening those silently would corrupt the data.
func tsvField(s string) string {
	if !strings.ContainsAny(s, "\t\n\r\"") {
		return s
	}
	return `"` + strings.ReplaceAll(s, `"`, `""`) + `"`
}

// buildTSV renders a header line of column names followed by one line per row.
func buildTSV(names []string, rows [][]string) string {
	var buf strings.Builder
	for i, name := range names {
		if i > 0 {
			buf.WriteByte('\t')
		}
		buf.WriteString(tsvField(name))
	}
	buf.WriteByte('\n')
	for _, row := range rows {
		for i, cell := range row {
			if i > 0 {
				buf.WriteByte('\t')
			}
			buf.WriteString(tsvField(cell))
		}
		buf.WriteByte('\n')
	}
	return buf.String()
}

func respondErr(command, code, message string, pretty bool, exitCode int) int {
	writeEnvelope(envelope{
		OK:      false,
		Command: command,
		Version: version,
		Error:   &cliError{Code: code, Message: message},
	}, pretty)
	return exitCode
}

func failUsage(command, message string) int {
	return respondErr(command, codeUsage, message, false, exitUsage)
}

// classify maps an error out of excelize onto a stable code so that callers can
// branch on it without matching message text.
func classify(err error) (string, string) {
	if err == nil {
		return "", ""
	}
	var notExist excelize.ErrSheetNotExist
	switch {
	case errors.As(err, &notExist):
		return codeSheetNotFound, err.Error()
	case errors.Is(err, os.ErrNotExist):
		return codeFileNotFound, err.Error()
	case errors.Is(err, errPasswordRequired):
		return codePasswordRequired, err.Error()
	case errors.Is(err, errNotSpreadsheet):
		return codeBadFormat, err.Error()
	case errors.Is(err, excelize.ErrWorkbookPassword):
		return codeInvalidPassword, err.Error()
	case errors.Is(err, excelize.ErrWorkbookFileFormat),
		errors.Is(err, excelize.ErrOptionsUnzipSizeLimit):
		return codeBadFormat, err.Error()
	default:
		return codeRead, err.Error()
	}
}

func fail(command string, err error, pretty bool) int {
	code, message := classify(err)
	return respondErr(command, code, message, pretty, exitError)
}

// failWithSheet reports err, expanding a missing-sheet error with the list of
// sheets that do exist so that a caller can correct itself in one step instead
// of probing for names.
func failWithSheet(command string, err error, sheets []string, pretty bool) int {
	var notExist excelize.ErrSheetNotExist
	if errors.As(err, &notExist) {
		return respondErr(command, codeSheetNotFound, fmt.Sprintf(
			"sheet %q not found; available sheets: %s",
			notExist.SheetName, strings.Join(sheets, ", ")), pretty, exitError)
	}
	return fail(command, err, pretty)
}
