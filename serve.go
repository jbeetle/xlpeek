// Copyright (c) 2026 henryyu@163.com. All rights reserved.
//
// This file is part of xlpeek, a read-only spreadsheet reader for
// agents. See README.md for what it does and docs/AGENTS.md for how it is
// meant to be driven.

package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"time"
)

// maxRequestBytes bounds one protocol line. Requests are short — a command and
// its flags — so this only exists to stop a malformed stream from buffering
// without limit.
const maxRequestBytes = 1 << 20

// cmdPing reports whether the process is alive and what it is holding.
//
// It is the one command whose answer is about the session rather than a
// workbook, which is what a supervisor needs in order to tell a busy process
// from a wedged one. Outside a serve session it still answers, simply with
// nothing cached and no requests behind it.
func cmdPing() int {
	return respondOK("ping", pingData(), false)
}

// pingData describes the session. Split out from the command so that what it
// reports can be asserted without capturing stdout.
func pingData() map[string]any {
	data := map[string]any{
		"status":         "ok",
		"uptime_seconds": int(time.Since(sessionStarted).Seconds()),
		"requests":       sessionRequests,
	}
	if paths := cachedWorkbookPaths(); len(paths) > 0 {
		data["cached_workbooks"] = paths
	}
	return data
}

// cmdServe answers a stream of requests against workbooks held open between
// them.
//
// Opening a workbook costs a parse, and a caller that asks twenty questions
// about the same file should pay that once rather than twenty times. The
// protocol is deliberately a thin wrapper over the ordinary command line: each
// line is {"command": "...", "args": [...]} using exactly the arguments the
// one-shot CLI takes, so nothing new has to be learned to use it.
//
//	{"command":"info","args":["book.xlsx"]}
//	{"ok":true,...}
//	{"command":"ping"}
//	{"ok":true,"data":{"status":"ok","uptime_seconds":12,"requests":3}}
//	{"command":"quit"}
//	{"ok":true,"data":{"status":"closing"}}
//
// One request produces exactly one response line, including on failure, so a
// reader can pair them up without looking at the content.
func cmdServe(args []string) int {
	fs := newFlagSet("serve", "serve")
	idleTimeout := fs.Duration("idle-timeout", 0,
		"exit after this long with no requests, e.g. 5m; 0 never exits on idle")
	cacheSize := fs.Int("cache", defaultSessionCacheSize,
		"workbooks to keep open at once")
	operands, code, ok := parseFlags(fs, args)
	if !ok {
		return code
	}
	if len(operands) != 0 {
		return failUsage("serve", "serve takes no operands; name the workbook in each request's args")
	}
	if *cacheSize < 1 {
		return failUsage("serve", "--cache must be at least 1")
	}
	if *idleTimeout < 0 {
		return failUsage("serve", "--idle-timeout must not be negative")
	}

	sessionEnabled = true
	sessionCacheLimit = *cacheSize
	sessionStarted = time.Now()
	sessionRequests = 0
	defer closeSessionWorkbooks()

	// stdin is read on its own goroutine so the idle timer can fire while the
	// read is blocked: a pipe has no deadline to set.
	lines := make(chan []byte, 4)
	go func() {
		defer close(lines)
		scanner := bufio.NewScanner(os.Stdin)
		scanner.Buffer(make([]byte, 0, 64*1024), maxRequestBytes)
		for scanner.Scan() {
			line := make([]byte, len(scanner.Bytes()))
			copy(line, scanner.Bytes())
			lines <- line
		}
	}()

	var (
		idle  <-chan time.Time
		timer *time.Timer
	)
	if *idleTimeout > 0 {
		timer = time.NewTimer(*idleTimeout)
		defer timer.Stop()
		idle = timer.C
	}

	for {
		var raw []byte
		select {
		case line, more := <-lines:
			if !more {
				return exitOK // stdin closed
			}
			raw = line
		case <-idle:
			// Leave silently. An unsolicited line would be waiting when the
			// host next reads, and it would take it for the answer to the
			// request it just sent. End of stream is the unambiguous signal,
			// and the exit code is the same as a normal close.
			return exitOK
		}
		if timer != nil {
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			timer.Reset(*idleTimeout)
		}
		if stop := handleRequest(bytes.TrimSpace(raw)); stop {
			return exitOK
		}
	}
}

// handleRequest runs one protocol line and reports whether the session should
// end.
func handleRequest(line []byte) (stop bool) {
	if len(line) == 0 {
		return false
	}
	var request struct {
		Command string   `json:"command"`
		Args    []string `json:"args"`
	}
	if err := json.Unmarshal(line, &request); err != nil {
		respondErr("serve", codeUsage, fmt.Sprintf(
			`each line must be a JSON object like {"command":"read","args":["book.xlsx"]}: %v`, err),
			false, exitUsage)
		return false
	}
	switch request.Command {
	case "":
		respondErr("serve", codeUsage, `the request needs a "command" field`, false, exitUsage)
		return false
	case "quit", "exit":
		// Acknowledge before leaving. The protocol promises one response line
		// per request line, and a host that waits for the reply to its own
		// shutdown would otherwise block until the pipe closed.
		respondOK("serve", map[string]string{"status": "closing"}, false)
		return true
	case "serve":
		respondErr("serve", codeUsage, "serve cannot be nested", false, exitUsage)
		return false
	}

	sessionRequests++
	// run() dispatches exactly as the one-shot CLI does and writes the envelope
	// to stdout. Its exit code is not meaningful here — the envelope already
	// carries ok — so it is discarded. This is also what makes ping work: it
	// is an ordinary command that happens to read session state.
	_ = run(append([]string{request.Command}, request.Args...))
	return false
}
