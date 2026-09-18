// Copyright (c) 2026 henryyu@163.com. All rights reserved.
//
// This file is part of xlpeek, a read-only spreadsheet reader for
// agents. See README.md for what it does and docs/AGENTS.md for how it is
// meant to be driven.

// Command xlpeek is a read-only command-line spreadsheet reader designed to
// be driven by an agent.
//
// It exposes the parts of excelize that an agent needs in order to explore and
// page through a workbook without loading it all into memory: a workbook
// overview, a streaming row reader with paging and filtering, and a cell
// search. Every invocation writes exactly one JSON envelope to stdout so the
// caller only has to parse one shape, on both the success and failure paths.
package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
)

const (
	tool    = "xlpeek"
	version = "1.2.2"
	// Copyright holder, reported by the version command and carried as the
	// header of every source file in this command.
	author    = "henryyu@163.com"
	copyright = "Copyright (c) 2026 " + author + ". All rights reserved."
)

func main() {
	os.Exit(run(os.Args[1:]))
}

// run dispatches a command and returns the process exit code.
func run(args []string) int {
	if len(args) == 0 {
		writeUsage(os.Stderr)
		return exitUsage
	}
	command, rest := args[0], args[1:]
	switch command {
	case "info":
		return cmdInfo(rest)
	case "read":
		return cmdRead(rest)
	case "agg":
		return cmdAgg(rest)
	case "profile":
		return cmdProfile(rest)
	case "find":
		return cmdFind(rest)
	case "serve":
		return cmdServe(rest)
	case "ping":
		return cmdPing()
	case "help", "-h", "--help":
		writeUsage(os.Stdout)
		return exitOK
	case "version", "--version":
		// Emitted as an envelope rather than plain text so that a caller can
		// parse every command's output the same way. "help" is the one
		// exception: usage text is documentation, not data.
		return respondOK("version", map[string]string{
			"name":      tool,
			"version":   version,
			"copyright": copyright,
		}, false)
	default:
		return respondErr(command, codeUsage,
			fmt.Sprintf("unknown command %q", command), false, exitUsage)
	}
}

// newFlagSet builds a flag set that reports its own usage on -h.
func newFlagSet(name, synopsis string) *flag.FlagSet {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	if sessionEnabled {
		// Inside serve, flag diagnostics would interleave with the protocol.
		// The envelope on stdout already reports the error.
		fs.SetOutput(io.Discard)
	}
	fs.Usage = func() {
		fmt.Fprintf(os.Stderr, "Usage: %s %s\n\nFlags:\n", tool, synopsis)
		fs.PrintDefaults()
	}
	return fs
}

// boolFlag matches the flag package's internal interface for flags that never
// consume the following argument. Interfaces are satisfied structurally, so
// declaring the same method set here is enough to recognise them.
type boolFlag interface {
	IsBoolFlag() bool
}

// reorderArgs moves flags ahead of the positional operands.
//
// The standard flag package stops parsing at the first non-flag argument, so
// "read book.xlsx -s Sheet1" would otherwise leave "-s" and "Sheet1" in the
// operand list and be rejected as too many arguments. Flags must be recognised
// here without knowing their values, which means a value-taking flag has to
// swallow the next token while a boolean flag must not.
func reorderArgs(fs *flag.FlagSet, args []string) []string {
	var flags, operands []string
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "--" {
			operands = append(operands, args[i+1:]...)
			break
		}
		if len(arg) < 2 || arg[0] != '-' {
			operands = append(operands, arg)
			continue
		}
		flags = append(flags, arg)
		name := strings.TrimLeft(arg, "-")
		if strings.ContainsRune(name, '=') {
			continue // the value is attached, as in --limit=10
		}
		declared := fs.Lookup(name)
		if declared == nil {
			continue // unknown flag: let Parse produce the error
		}
		if boolean, ok := declared.Value.(boolFlag); ok && boolean.IsBoolFlag() {
			// A boolean flag does not take a separate value, which is not
			// visible from the outside: "--ignore-case true" reads as correct
			// to anyone who has not memorised the flag package, and the "true"
			// then fell through to the operand list, where it was reported as a
			// stray workbook path — sending the caller to check a path that was
			// never the problem. The two words a boolean can be set to are
			// accepted as its value so that the natural spelling works.
			if i+1 < len(args) && isBoolWord(args[i+1]) {
				i++
				flags = append(flags, arg+"="+args[i])
			}
			continue
		}
		if i+1 < len(args) {
			i++
			flags = append(flags, args[i])
		}
	}
	return append(flags, operands...)
}

// isBoolWord reports whether a token is one of the two words a boolean flag can
// be set to. Only the words: "1" and "0" are also accepted by the flag package
// for a boolean, and treating those as values would swallow an operand — a
// sheet index, a file called 1 — that the caller meant literally.
func isBoolWord(token string) bool {
	return strings.EqualFold(token, "true") || strings.EqualFold(token, "false")
}

// operandError reports a command invoked with the wrong number of positional
// arguments.
//
// It names what it received, because the usual cause is a token that was meant
// as a value rather than as a path: "expected exactly one workbook path" does
// not say which argument was taken for one, and a caller reads it as an
// instruction to check a path that is perfectly fine.
func operandError(command string, operands []string, want string) int {
	if len(operands) == 0 {
		return failUsage(command, fmt.Sprintf("expected %s, got none", want))
	}
	quoted := make([]string, len(operands))
	for i, operand := range operands {
		quoted[i] = fmt.Sprintf("%q", operand)
	}
	return failUsage(command, fmt.Sprintf(
		"expected %s, got %d: %s", want, len(operands), strings.Join(quoted, " ")))
}

// parseFlags parses args and returns the positional operands. A usage error is
// reported as a JSON envelope on stdout, matching every other failure path;
// the flag package's own message goes to stderr for a human reader. The bool
// result reports whether the caller should continue.
func parseFlags(fs *flag.FlagSet, args []string) ([]string, int, bool) {
	if err := fs.Parse(reorderArgs(fs, args)); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil, exitOK, false
		}
		return nil, respondErr(fs.Name(), codeUsage, err.Error(), false, exitUsage), false
	}
	return fs.Args(), exitOK, true
}

func writeUsage(w io.Writer) {
	fmt.Fprintf(w, `%s %s - read-only spreadsheet reader for agents

Usage:
  %s <command> <file.xlsx> [flags]

Commands:
  info    Summarise a workbook: sheets, dimensions, header, sample rows.
  read    Stream one page of rows out of a worksheet.
  agg     Group and summarise rows without moving them all across the wire.
  profile Describe each column: type, fill rate, distinct values.
  find    Locate cells by substring or regular expression.
  serve   Answer many requests over stdin/stdout, holding workbooks open.
  ping    Report whether the process is alive and what it is holding open.

Output contract:
  Every run writes exactly one JSON envelope to stdout and nothing else.
  Success:  {"ok":true,"command":"read","version":"%s","data":{...}}   exit 0
  Failure:  {"ok":false,"command":"read","error":{"code":"...","message":"..."}}
            exit 1 for a runtime failure, 2 for a usage failure.

  Errors go to stdout rather than stderr so a caller has a single parse path;
  anything written to stderr is human-oriented noise (flag diagnostics).
  The single exception is "help", which prints plain usage text.

  Add --pretty to any command for indented output. The default is compact,
  single-line JSON, which is cheaper to feed to a model.

Run "%s <command> -h" for the flags of a specific command.

%s
`, tool, version, tool, version, tool, copyright)
}
