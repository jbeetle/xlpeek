// Copyright (c) 2026 henryyu@163.com. All rights reserved.
//
// This file is part of xlpeek, a read-only spreadsheet reader for
// agents. See README.md for what it does and docs/AGENTS.md for how it is
// meant to be driven.

package main

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// A small arithmetic expression evaluator, shared by the agg command's column
// expressions ("收入金额-成本金额") and its derived metrics ("毛利率=净利/sum_收入金额").
//
// Variables are resolved through a callback rather than a map so that the two
// callers can look up completely different things — worksheet cells for a
// column expression, already-computed aggregate values for a derived metric —
// without either paying for the other's representation.

type exprNode interface {
	// eval returns the value and whether the variable was present at all.
	// Absent is not the same as zero: an aggregate must skip a missing cell
	// rather than treat it as a zero, or an average would be dragged down.
	eval(lookup func(string) (float64, bool)) (float64, bool)
}

type numberNode float64

type identNode string

type unaryNode struct{ operand exprNode }

type binaryNode struct {
	op          byte
	left, right exprNode
}

func (n numberNode) eval(func(string) (float64, bool)) (float64, bool) {
	return float64(n), true
}

func (n identNode) eval(lookup func(string) (float64, bool)) (float64, bool) {
	return lookup(string(n))
}

func (n unaryNode) eval(lookup func(string) (float64, bool)) (float64, bool) {
	value, ok := n.operand.eval(lookup)
	return -value, ok
}

func (n binaryNode) eval(lookup func(string) (float64, bool)) (float64, bool) {
	left, leftOK := n.left.eval(lookup)
	right, rightOK := n.right.eval(lookup)
	if !leftOK && !rightOK {
		return 0, false
	}
	// A missing operand counts as zero inside an expression, which is how a
	// spreadsheet treats an empty cell in arithmetic. The distinction above
	// still applies to a bare column reference.
	if !leftOK {
		left = 0
	}
	if !rightOK {
		right = 0
	}
	switch n.op {
	case '+':
		return left + right, true
	case '-':
		return left - right, true
	case '*':
		return left * right, true
	case '/':
		if right == 0 {
			return 0, false
		}
		return left / right, true
	}
	return 0, false
}

// bracketHint names the escape when an expression failed because a column name
// contains an operator character.
//
// "金额(万元)" is a column name a caller will meet in a real workbook, and an
// expression parser necessarily reads the parenthesis as arithmetic. The fix —
// writing [金额(万元)] — cannot be inferred from "unexpected "(" at position 6",
// so it is spelled out instead.
//
// The hint is offered only when the input contains a character a name would
// need the escape for and bracketing it in fact parses, so an ordinary typo
// ("1+*2") stays a plain error rather than being answered with a suggestion to
// make it a column name.
func bracketHint(source string) string {
	name := strings.TrimSpace(source)
	if name == "" || !strings.ContainsAny(name, "()[] \t") {
		return ""
	}
	if _, err := parseExpr("[" + name + "]"); err != nil {
		return ""
	}
	return fmt.Sprintf("; if that is a column name, wrap it in brackets: [%s]", name)
}

// identifiers walks the tree collecting every variable name it references, so
// that the caller can resolve them all up front instead of per row.
func identifiers(n exprNode, seen map[string]bool) {
	switch node := n.(type) {
	case identNode:
		seen[string(node)] = true
	case unaryNode:
		identifiers(node.operand, seen)
	case binaryNode:
		identifiers(node.left, seen)
		identifiers(node.right, seen)
	}
}

type exprParser struct {
	input string
	pos   int
}

// parseExpr parses an arithmetic expression over + - * / and parentheses.
func parseExpr(input string) (exprNode, error) {
	p := &exprParser{input: input}
	node, err := p.parseSum()
	if err != nil {
		return nil, err
	}
	if p.peek() != 0 {
		return nil, fmt.Errorf("unexpected %q at position %d", string(p.peek()), p.pos)
	}
	return node, nil
}

func (p *exprParser) skipSpace() {
	for p.pos < len(p.input) && (p.input[p.pos] == ' ' || p.input[p.pos] == '\t') {
		p.pos++
	}
}

// peek returns the next significant byte, or 0 at the end of input.
func (p *exprParser) peek() byte {
	p.skipSpace()
	if p.pos < len(p.input) {
		return p.input[p.pos]
	}
	return 0
}

func (p *exprParser) parseSum() (exprNode, error) {
	left, err := p.parseProduct()
	if err != nil {
		return nil, err
	}
	for {
		op := p.peek()
		if op != '+' && op != '-' {
			return left, nil
		}
		p.pos++
		right, err := p.parseProduct()
		if err != nil {
			return nil, err
		}
		left = binaryNode{op: op, left: left, right: right}
	}
}

func (p *exprParser) parseProduct() (exprNode, error) {
	left, err := p.parseFactor()
	if err != nil {
		return nil, err
	}
	for {
		op := p.peek()
		if op != '*' && op != '/' {
			return left, nil
		}
		p.pos++
		right, err := p.parseFactor()
		if err != nil {
			return nil, err
		}
		left = binaryNode{op: op, left: left, right: right}
	}
}

func (p *exprParser) parseFactor() (exprNode, error) {
	switch p.peek() {
	case '-':
		p.pos++
		operand, err := p.parseFactor()
		if err != nil {
			return nil, err
		}
		return unaryNode{operand: operand}, nil
	case '+':
		p.pos++
		return p.parseFactor()
	}
	return p.parsePrimary()
}

// isOperatorByte reports whether b delimits an operand rather than belonging
// to one.
func isOperatorByte(b byte) bool {
	switch b {
	case '+', '-', '*', '/', '(', ')', ' ', '\t', 0:
		return true
	}
	return false
}

func (p *exprParser) parsePrimary() (exprNode, error) {
	switch c := p.peek(); {
	case c == 0:
		return nil, errors.New("expression ends unexpectedly")
	case c == '(':
		p.pos++
		inner, err := p.parseSum()
		if err != nil {
			return nil, err
		}
		if p.peek() != ')' {
			return nil, errors.New("missing closing parenthesis")
		}
		p.pos++
		return inner, nil
	case c == '[':
		// Bracketed form, for a column whose name contains an operator or a
		// space, as in --sum "[收入-成本]".
		p.pos++
		start := p.pos
		for p.pos < len(p.input) && p.input[p.pos] != ']' {
			p.pos++
		}
		if p.pos >= len(p.input) {
			return nil, errors.New("missing closing bracket")
		}
		name := strings.TrimSpace(p.input[start:p.pos])
		p.pos++
		if name == "" {
			return nil, errors.New("empty bracketed name")
		}
		return identNode(name), nil
	}

	// Scan the whole operand run first, then decide whether it is a number or
	// a name. Deciding by the first byte would misread a column called
	// "2023年收入" as the number 2023.
	start := p.pos
	for p.pos < len(p.input) && !isOperatorByte(p.input[p.pos]) {
		p.pos++
	}
	if p.pos == start {
		// The switch above already handled end of input, so there is a byte
		// here: it is an operator that cannot start an operand.
		return nil, fmt.Errorf("unexpected %q at position %d", string(p.input[p.pos]), p.pos)
	}
	token := p.input[start:p.pos]
	if value, err := strconv.ParseFloat(token, 64); err == nil {
		return numberNode(value), nil
	}
	return identNode(token), nil
}
