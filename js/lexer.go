// Package js is a small, pure-Go interpreter for a limited JavaScript subset.
// It is NOT a full JavaScript engine and NOT a sandbox for hostile code: it
// bounds steps, time and call depth, and exposes only what the host passes in.
package js

import (
	"fmt"
	"strings"
)

// Pos is a 1-based source position.
type Pos struct {
	Line, Col int
}

func (p Pos) String() string { return fmt.Sprintf("%d:%d", p.Line, p.Col) }

// Error is a diagnostic with a source span.
type Error struct {
	Kind string // "syntax" | "runtime"
	Pos  Pos
	Msg  string
	File string
}

func (e *Error) Error() string {
	f := e.File
	if f == "" {
		f = "<program>"
	}
	return fmt.Sprintf("%s:%s: %s error: %s", f, e.Pos, e.Kind, e.Msg)
}

type tokKind int

const (
	tEOF tokKind = iota
	tIdent
	tKeyword
	tNumber
	tString
	tTemplate
	tPunct
	tRegex // val = source, flags in flags
)

type token struct {
	kind     tokKind
	val      string
	num      float64
	pos      Pos
	nlBefore bool // newline between previous token and this one
	flags    string
	raws     []string // template literals: each quasi's source text, escapes not applied
}

var keywords = map[string]bool{
	"const": true, "let": true, "var": true, "if": true, "else": true, "for": true, "of": true,
	"in": true, "while": true, "do": true, "break": true, "continue": true, "return": true,
	"function": true, "true": true, "false": true, "null": true, "undefined": true,
	"try": true, "catch": true, "finally": true, "throw": true, "new": true, "typeof": true,
	"async": true, "await": true, "import": true, "export": true, "from": true, "class": true,
	"this": true, "delete": true, "void": true, "yield": true, "switch": true, "case": true, "default": true,
	"super": true, "instanceof": true,
}

// "of", "from", "async" are contextual: they lex as identifiers and the
// parser checks their text.
var contextual = map[string]bool{"of": true, "from": true, "async": true}

var puncts = []string{
	">>>=", "...", "===", "!==", "**=", "<<=", ">>=", ">>>", "&&=", "||=", "??=",
	"=>", "==", "!=", "<=", ">=", "&&", "||", "??", "?.", "++", "--", "+=", "-=", "*=", "/=", "%=",
	"**", "<<", ">>", "&=", "|=", "^=",
	"{", "}", "(", ")", "[", "]", ";", ",", "<", ">", "+", "-", "*", "/", "%", "&", "|", "^",
	"!", "~", "?", ":", "=", ".",
}

type lexer struct {
	src  []rune
	i    int
	line int
	col  int
	file string
}

func lex(src, file string) ([]token, error) {
	l := &lexer{src: []rune(src), line: 1, col: 1, file: file}
	var toks []token
	for {
		nl, err := l.skipSpace()
		if err != nil {
			return nil, err
		}
		var t token
		if l.i < len(l.src) && l.peek(0) == '/' && regexAllowed(toks) {
			t, err = l.regex(l.pos())
		} else {
			t, err = l.next()
		}
		if err != nil {
			return nil, err
		}
		t.nlBefore = nl
		toks = append(toks, t)
		if t.kind == tEOF {
			return toks, nil
		}
	}
}

func (l *lexer) errf(p Pos, f string, a ...any) error {
	return &Error{Kind: "syntax", Pos: p, Msg: fmt.Sprintf(f, a...), File: l.file}
}

func (l *lexer) peek(o int) rune {
	if l.i+o < len(l.src) {
		return l.src[l.i+o]
	}
	return 0
}

func (l *lexer) adv() rune {
	r := l.src[l.i]
	l.i++
	if r == '\n' {
		l.line++
		l.col = 1
	} else {
		l.col++
	}
	return r
}

func (l *lexer) pos() Pos { return Pos{l.line, l.col} }

func (l *lexer) skipSpace() (bool, error) {
	nl := false
	for l.i < len(l.src) {
		r := l.peek(0)
		switch {
		case r == '\n':
			nl = true
			l.adv()
		case r == ' ' || r == '\t' || r == '\r' || r == 0xFEFF:
			l.adv()
		case r == '/' && l.peek(1) == '/':
			for l.i < len(l.src) && l.peek(0) != '\n' {
				l.adv()
			}
		case r == '/' && l.peek(1) == '*':
			p := l.pos()
			l.adv()
			l.adv()
			for {
				if l.i >= len(l.src) {
					return nl, l.errf(p, "unterminated comment")
				}
				if l.peek(0) == '*' && l.peek(1) == '/' {
					l.adv()
					l.adv()
					break
				}
				if l.adv() == '\n' {
					nl = true
				}
			}
		default:
			return nl, nil
		}
	}
	return nl, nil
}

func isIdentStart(r rune) bool {
	return r == '_' || r == '$' || (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || r > 127
}
func isDigit(r rune) bool { return r >= '0' && r <= '9' }

func (l *lexer) next() (token, error) {
	p := l.pos()
	if l.i >= len(l.src) {
		return token{kind: tEOF, pos: p}, nil
	}
	r := l.peek(0)
	switch {
	case isIdentStart(r):
		s := l.i
		for l.i < len(l.src) && (isIdentStart(l.peek(0)) || isDigit(l.peek(0))) {
			l.adv()
		}
		w := string(l.src[s:l.i])
		if keywords[w] && !contextual[w] {
			return token{kind: tKeyword, val: w, pos: p}, nil
		}
		return token{kind: tIdent, val: w, pos: p}, nil
	case isDigit(r) || (r == '.' && isDigit(l.peek(1))):
		return l.number(p)
	case r == '"' || r == '\'':
		return l.str(p, r)
	case r == '`':
		return l.template(p)
	}
	for _, pu := range puncts {
		if l.hasPrefix(pu) {
			// "?." followed by a digit is a ternary then a number.
			if pu == "?." && isDigit(l.peek(2)) {
				continue
			}
			for range pu {
				l.adv()
			}
			return token{kind: tPunct, val: pu, pos: p}, nil
		}
	}
	return token{}, l.errf(p, "unexpected character %q", r)
}

func (l *lexer) hasPrefix(s string) bool {
	rs := []rune(s)
	if l.i+len(rs) > len(l.src) {
		return false
	}
	for k, c := range rs {
		if l.src[l.i+k] != c {
			return false
		}
	}
	return true
}

func (l *lexer) number(p Pos) (token, error) {
	s := l.i
	if l.peek(0) == '0' && (l.peek(1) == 'x' || l.peek(1) == 'X') {
		l.adv()
		l.adv()
		var v float64
		n := 0
		for {
			c := l.peek(0)
			var d int
			switch {
			case isDigit(c):
				d = int(c - '0')
			case c >= 'a' && c <= 'f':
				d = int(c-'a') + 10
			case c >= 'A' && c <= 'F':
				d = int(c-'A') + 10
			default:
				if n == 0 {
					return token{}, l.errf(p, "bad hex literal")
				}
				return token{kind: tNumber, num: v, pos: p}, nil
			}
			v = v*16 + float64(d)
			n++
			l.adv()
		}
	}
	for isDigit(l.peek(0)) || l.peek(0) == '_' {
		l.adv()
	}
	if l.peek(0) == '.' && isDigit(l.peek(1)) || (l.peek(0) == '.' && !isIdentStart(l.peek(1))) {
		l.adv()
		for isDigit(l.peek(0)) {
			l.adv()
		}
	}
	if l.peek(0) == 'e' || l.peek(0) == 'E' {
		l.adv()
		if l.peek(0) == '+' || l.peek(0) == '-' {
			l.adv()
		}
		for isDigit(l.peek(0)) {
			l.adv()
		}
	}
	text := strings.ReplaceAll(string(l.src[s:l.i]), "_", "")
	var v float64
	if _, err := fmt.Sscan(text, &v); err != nil {
		return token{}, l.errf(p, "bad number %q", text)
	}
	return token{kind: tNumber, num: v, pos: p}, nil
}

func (l *lexer) escape(p Pos) (rune, error) {
	if l.i >= len(l.src) {
		return 0, l.errf(p, "unterminated string")
	}
	c := l.adv()
	switch c {
	case 'n':
		return '\n', nil
	case 't':
		return '\t', nil
	case 'r':
		return '\r', nil
	case '0':
		return 0, nil
	case 'b':
		return '\b', nil
	case 'f':
		return '\f', nil
	case 'v':
		return '\v', nil
	case 'u':
		var v rune
		if l.peek(0) == '{' {
			l.adv()
			for l.peek(0) != '}' {
				if l.i >= len(l.src) {
					return 0, l.errf(p, "bad unicode escape")
				}
				v = v*16 + hexVal(l.adv())
			}
			l.adv()
			return v, nil
		}
		for k := 0; k < 4; k++ {
			if l.i >= len(l.src) {
				return 0, l.errf(p, "bad unicode escape")
			}
			v = v*16 + hexVal(l.adv())
		}
		return v, nil
	default:
		return c, nil
	}
}

func hexVal(r rune) rune {
	switch {
	case r >= '0' && r <= '9':
		return r - '0'
	case r >= 'a' && r <= 'f':
		return r - 'a' + 10
	case r >= 'A' && r <= 'F':
		return r - 'A' + 10
	}
	return 0
}

func (l *lexer) str(p Pos, q rune) (token, error) {
	l.adv()
	var b strings.Builder
	for {
		if l.i >= len(l.src) || l.peek(0) == '\n' {
			return token{}, l.errf(p, "unterminated string")
		}
		c := l.adv()
		if c == q {
			return token{kind: tString, val: b.String(), pos: p}, nil
		}
		if c == '\\' {
			e, err := l.escape(p)
			if err != nil {
				return token{}, err
			}
			b.WriteRune(e)
			continue
		}
		b.WriteRune(c)
	}
}

// template lexes a whole `...` literal into a raw string; the parser splits
// it into parts. Interpolations are stored as "\x00expr\x00" segments.
func (l *lexer) template(p Pos) (token, error) {
	l.adv()
	var b, raw strings.Builder
	var raws []string
	for {
		if l.i >= len(l.src) {
			return token{}, l.errf(p, "unterminated template literal")
		}
		c := l.adv()
		switch {
		case c == '`':
			return token{kind: tTemplate, val: b.String(), pos: p, raws: append(raws, raw.String())}, nil
		case c == '\\':
			start := l.i - 1
			e, err := l.escape(p)
			if err != nil {
				return token{}, err
			}
			raw.WriteString(string(l.src[start:l.i]))
			if e == 0 {
				e = '␀' // keep NUL out of the delimiter channel
			}
			b.WriteRune(e)
		case c == '$' && l.peek(0) == '{':
			l.adv()
			raws = append(raws, raw.String())
			raw.Reset()
			b.WriteRune(0)
			depth := 1
			for depth > 0 {
				if l.i >= len(l.src) {
					return token{}, l.errf(p, "unterminated ${ in template literal")
				}
				d := l.adv()
				if d == '{' {
					depth++
				} else if d == '}' {
					depth--
					if depth == 0 {
						break
					}
				}
				b.WriteRune(d)
			}
			b.WriteRune(0)
		default:
			b.WriteRune(c)
			raw.WriteRune(c)
		}
	}
}

// regexAllowed reports whether a '/' starts a regex literal: it does where
// an expression may begin, i.e. not after a value (identifier, literal, `)`
// or `]`).
func regexAllowed(toks []token) bool {
	if len(toks) == 0 {
		return true
	}
	t := toks[len(toks)-1]
	switch t.kind {
	case tIdent, tNumber, tString, tTemplate, tRegex:
		return false
	case tKeyword:
		switch t.val {
		case "this", "super", "true", "false", "null", "undefined":
			return false
		}
		return true
	case tPunct:
		switch t.val {
		case ")", "]", "}", "++", "--":
			return false
		}
	}
	return true
}

// regex lexes /source/flags.
func (l *lexer) regex(p Pos) (token, error) {
	l.adv()
	var b strings.Builder
	inClass := false
	for {
		if l.i >= len(l.src) || l.peek(0) == '\n' {
			return token{}, l.errf(p, "unterminated regex literal")
		}
		c := l.adv()
		switch {
		case c == '\\':
			if l.i >= len(l.src) {
				return token{}, l.errf(p, "unterminated regex literal")
			}
			b.WriteRune(c)
			b.WriteRune(l.adv())
			continue
		case c == '[':
			inClass = true
		case c == ']':
			inClass = false
		case c == '/' && !inClass:
			var f strings.Builder
			for l.i < len(l.src) && isIdentStart(l.peek(0)) {
				f.WriteRune(l.adv())
			}
			return token{kind: tRegex, val: b.String(), flags: f.String(), pos: p}, nil
		}
		b.WriteRune(c)
	}
}
