package js

import (
	"fmt"
	"strings"
)

// Parse parses a program (a module: imports/exports allowed at top level).
func Parse(src, file string) (*Program, error) {
	toks, err := lex(src, file)
	if err != nil {
		return nil, err
	}
	p := &parser{toks: toks, file: file}
	prog := &Program{base: base{Pos{1, 1}}}
	for !p.at(tEOF, "") {
		s, err := p.statement(true)
		if err != nil {
			return nil, err
		}
		prog.Body = append(prog.Body, s)
	}
	return prog, nil
}

// ParseExpression parses a single expression (for `code eval`).
func ParseExpression(src, file string) (Node, error) {
	toks, err := lex(src, file)
	if err != nil {
		return nil, err
	}
	p := &parser{toks: toks, file: file}
	e, err := p.expression()
	if err != nil {
		return nil, err
	}
	if p.is(";") {
		p.i++
	}
	if !p.at(tEOF, "") {
		return nil, p.errHere("unexpected %s after expression", p.desc())
	}
	return e, nil
}

type parser struct {
	toks       []token
	i          int
	file       string
	fnDepth    int
	asyncDepth []bool
}

func (p *parser) cur() token { return p.toks[p.i] }
func (p *parser) peekTok(o int) token {
	if p.i+o < len(p.toks) {
		return p.toks[p.i+o]
	}
	return p.toks[len(p.toks)-1]
}

func (p *parser) at(k tokKind, v string) bool {
	t := p.cur()
	return t.kind == k && (v == "" || t.val == v)
}
func (p *parser) is(punct string) bool { return p.at(tPunct, punct) }
func (p *parser) isKw(kw string) bool  { return p.at(tKeyword, kw) }
func (p *parser) isCtx(w string) bool  { return p.at(tIdent, w) }

func (p *parser) desc() string {
	t := p.cur()
	switch t.kind {
	case tEOF:
		return "end of input"
	case tString:
		return fmt.Sprintf("string %q", t.val)
	case tNumber:
		return "number"
	case tTemplate:
		return "template literal"
	}
	return fmt.Sprintf("%q", t.val)
}

func (p *parser) errAt(pos Pos, f string, a ...any) error {
	return &Error{Kind: "syntax", Pos: pos, Msg: fmt.Sprintf(f, a...), File: p.file}
}
func (p *parser) errHere(f string, a ...any) error { return p.errAt(p.cur().pos, f, a...) }

func (p *parser) expect(punct string) (token, error) {
	if !p.is(punct) {
		return token{}, p.errHere("expected %q but found %s", punct, p.desc())
	}
	t := p.cur()
	p.i++
	return t, nil
}

func (p *parser) ident() (string, error) {
	if p.cur().kind != tIdent {
		return "", p.errHere("expected identifier but found %s", p.desc())
	}
	t := p.cur()
	p.i++
	return t.val, nil
}

// semicolon applies automatic semicolon insertion: ";" or newline or "}" or EOF.
func (p *parser) semicolon() error {
	if p.is(";") {
		p.i++
		return nil
	}
	if p.is("}") || p.at(tEOF, "") || p.cur().nlBefore {
		return nil
	}
	return p.errHere("expected \";\" but found %s", p.desc())
}

var unsupportedKw = map[string]string{
	"class":  "classes are not supported; use plain objects and functions",
	"new":    "`new` is not supported; use factory functions and literals",
	"this":   "`this` is not supported; pass values explicitly",
	"yield":  "generators are not supported",
	"switch": "`switch` is not supported yet; use if/else",
	"delete": "`delete` is not supported; build a new object instead",
	"var":    "",
}

func (p *parser) statement(top bool) (Node, error) {
	t := p.cur()
	if t.kind == tKeyword {
		if msg, bad := unsupportedKw[t.val]; bad && msg != "" {
			return nil, p.errHere("%s", msg)
		}
		switch t.val {
		case "const", "let", "var":
			d, err := p.varDecl(true)
			if err != nil {
				return nil, err
			}
			return d, p.semicolon()
		case "if":
			return p.ifStmt()
		case "for":
			return p.forStmt()
		case "while":
			p.i++
			if _, err := p.expect("("); err != nil {
				return nil, err
			}
			test, err := p.expression()
			if err != nil {
				return nil, err
			}
			if _, err := p.expect(")"); err != nil {
				return nil, err
			}
			body, err := p.statement(false)
			if err != nil {
				return nil, err
			}
			return &While{base: base{t.pos}, Test: test, Body: body}, nil
		case "do":
			p.i++
			body, err := p.statement(false)
			if err != nil {
				return nil, err
			}
			if !p.isKw("while") {
				return nil, p.errHere("expected `while` after do body")
			}
			p.i++
			if _, err := p.expect("("); err != nil {
				return nil, err
			}
			test, err := p.expression()
			if err != nil {
				return nil, err
			}
			if _, err := p.expect(")"); err != nil {
				return nil, err
			}
			if p.is(";") {
				p.i++
			}
			return &While{base: base{t.pos}, Test: test, Body: body, Do: true}, nil
		case "break", "continue":
			p.i++
			label := ""
			if p.cur().kind == tIdent && !p.cur().nlBefore {
				label = p.cur().val
				p.i++
			}
			if err := p.semicolon(); err != nil {
				return nil, err
			}
			if t.val == "break" {
				return &Break{base{t.pos}, label}, nil
			}
			return &Continue{base{t.pos}, label}, nil
		case "return":
			if p.fnDepth == 0 {
				return nil, p.errHere("`return` outside a function")
			}
			p.i++
			var x Node
			if !p.is(";") && !p.is("}") && !p.at(tEOF, "") && !p.cur().nlBefore {
				var err error
				if x, err = p.expression(); err != nil {
					return nil, err
				}
			}
			return &Return{base{t.pos}, x}, p.semicolon()
		case "throw":
			p.i++
			if p.cur().nlBefore {
				return nil, p.errHere("no line break allowed after `throw`")
			}
			x, err := p.expression()
			if err != nil {
				return nil, err
			}
			return &ThrowStmt{base{t.pos}, x}, p.semicolon()
		case "try":
			return p.tryStmt()
		case "function":
			f, err := p.function(false)
			if err != nil {
				return nil, err
			}
			return &VarDecl{base: base{t.pos}, Kind: "function", Decls: []Declarator{{Target: &Ident{base: base{t.pos}, Name: f.Name}, Init: f}}}, nil
		case "import":
			if !top {
				return nil, p.errHere("`import` is only allowed at the top level of a module")
			}
			if p.peekTok(1).val == "(" {
				return nil, p.errHere("dynamic import() is not supported; use a static import at the top of the module")
			}
			return p.importStmt()
		case "export":
			if !top {
				return nil, p.errHere("`export` is only allowed at the top level of a module")
			}
			return p.exportStmt()
		}
	}
	if p.isCtx("async") && p.peekTok(1).val == "function" && !p.peekTok(1).nlBefore {
		pos := p.cur().pos
		p.i++
		f, err := p.function(true)
		if err != nil {
			return nil, err
		}
		return &VarDecl{base: base{pos}, Kind: "function", Decls: []Declarator{{Target: &Ident{base: base{pos}, Name: f.Name}, Init: f}}}, nil
	}
	if p.is("{") {
		return p.block()
	}
	if p.is(";") {
		p.i++
		return &Empty{base{t.pos}}, nil
	}
	x, err := p.expression()
	if err != nil {
		return nil, err
	}
	return &ExprStmt{base{t.pos}, x}, p.semicolon()
}

func (p *parser) block() (*Block, error) {
	t, err := p.expect("{")
	if err != nil {
		return nil, err
	}
	b := &Block{base: base{t.pos}}
	for !p.is("}") {
		if p.at(tEOF, "") {
			return nil, p.errAt(t.pos, "unclosed block: missing \"}\"")
		}
		s, err := p.statement(false)
		if err != nil {
			return nil, err
		}
		b.Body = append(b.Body, s)
	}
	p.i++
	return b, nil
}

func (p *parser) varDecl(requireInitForConst bool) (*VarDecl, error) {
	t := p.cur()
	p.i++
	d := &VarDecl{base: base{t.pos}, Kind: t.val}
	for {
		target, err := p.bindingTarget()
		if err != nil {
			return nil, err
		}
		var init Node
		if p.is("=") {
			p.i++
			if init, err = p.assignment(); err != nil {
				return nil, err
			}
		} else if requireInitForConst && t.val == "const" {
			return nil, p.errAt(target.P(), "`const` declaration needs an initial value")
		} else if _, isId := target.(*Ident); !isId && requireInitForConst {
			return nil, p.errAt(target.P(), "destructuring declaration needs an initial value")
		}
		d.Decls = append(d.Decls, Declarator{Target: target, Init: init})
		if !p.is(",") {
			return d, nil
		}
		p.i++
	}
}

// bindingTarget parses an identifier or a destructuring pattern.
func (p *parser) bindingTarget() (Node, error) {
	pos := p.cur().pos
	switch {
	case p.is("["):
		p.i++
		ap := &ArrayPattern{base: base{pos}}
		for !p.is("]") {
			if p.is(",") {
				p.i++
				ap.Elems = append(ap.Elems, Param{})
				continue
			}
			prm, err := p.param()
			if err != nil {
				return nil, err
			}
			ap.Elems = append(ap.Elems, prm)
			if prm.Rest && !p.is("]") {
				return nil, p.errHere("rest element must be last")
			}
			if !p.is("]") {
				if _, err := p.expect(","); err != nil {
					return nil, err
				}
			}
		}
		p.i++
		return ap, nil
	case p.is("{"):
		p.i++
		op := &ObjectPattern{base: base{pos}}
		for !p.is("}") {
			if p.is("...") {
				p.i++
				n, err := p.ident()
				if err != nil {
					return nil, err
				}
				op.Rest = &Ident{base: base{pos}, Name: n}
				break
			}
			kp := p.cur().pos
			key, err := p.propertyName()
			if err != nil {
				return nil, err
			}
			pp := PatternProp{Key: key, Target: &Ident{base: base{kp}, Name: key}}
			if p.is(":") {
				p.i++
				if pp.Target, err = p.bindingTarget(); err != nil {
					return nil, err
				}
			} else if p.cur().kind == tPunct && p.cur().val != "=" && p.cur().val != "," && p.cur().val != "}" {
				return nil, p.errHere("unexpected %s in object pattern", p.desc())
			}
			if p.is("=") {
				p.i++
				if pp.Default, err = p.assignment(); err != nil {
					return nil, err
				}
			}
			op.Props = append(op.Props, pp)
			if !p.is("}") {
				if _, err := p.expect(","); err != nil {
					return nil, err
				}
			}
		}
		if _, err := p.expect("}"); err != nil {
			return nil, err
		}
		return op, nil
	}
	n, err := p.ident()
	if err != nil {
		return nil, err
	}
	return &Ident{base: base{pos}, Name: n}, nil
}

func (p *parser) param() (Param, error) {
	var prm Param
	if p.is("...") {
		p.i++
		prm.Rest = true
	}
	t, err := p.bindingTarget()
	if err != nil {
		return prm, err
	}
	prm.Target = t
	if p.is("=") && !prm.Rest {
		p.i++
		if prm.Default, err = p.assignment(); err != nil {
			return prm, err
		}
	}
	return prm, nil
}

func (p *parser) propertyName() (string, error) {
	t := p.cur()
	switch t.kind {
	case tIdent, tKeyword:
		p.i++
		return t.val, nil
	case tString:
		p.i++
		return t.val, nil
	case tNumber:
		p.i++
		return numberToString(t.num), nil
	}
	return "", p.errHere("expected property name but found %s", p.desc())
}

func (p *parser) ifStmt() (Node, error) {
	t := p.cur()
	p.i++
	if _, err := p.expect("("); err != nil {
		return nil, err
	}
	test, err := p.expression()
	if err != nil {
		return nil, err
	}
	if _, err := p.expect(")"); err != nil {
		return nil, err
	}
	then, err := p.statement(false)
	if err != nil {
		return nil, err
	}
	n := &If{base: base{t.pos}, Test: test, Then: then}
	if p.isKw("else") {
		p.i++
		if n.Else, err = p.statement(false); err != nil {
			return nil, err
		}
	}
	return n, nil
}

func (p *parser) forStmt() (Node, error) {
	t := p.cur()
	p.i++
	if p.isKw("await") {
		return nil, p.errHere("`for await` is not supported")
	}
	if _, err := p.expect("("); err != nil {
		return nil, err
	}
	// for (const x of y) / for (x of y) / for (const k in o)
	save := p.i
	kind := ""
	if p.isKw("const") || p.isKw("let") || p.isKw("var") {
		kind = p.cur().val
		p.i++
	}
	if p.cur().kind == tIdent || p.is("[") || p.is("{") {
		if target, err := p.bindingTarget(); err == nil && (p.isCtx("of") || p.isKw("in")) {
			isIn := p.isKw("in")
			p.i++
			right, err := p.assignment()
			if err != nil {
				return nil, err
			}
			if _, err := p.expect(")"); err != nil {
				return nil, err
			}
			body, err := p.statement(false)
			if err != nil {
				return nil, err
			}
			return &ForOf{base: base{t.pos}, Kind: kind, Left: target, Right: right, Body: body, In: isIn}, nil
		}
	}
	p.i = save
	n := &For{base: base{t.pos}}
	var err error
	if !p.is(";") {
		if p.isKw("const") || p.isKw("let") || p.isKw("var") {
			if n.Init, err = p.varDecl(true); err != nil {
				return nil, err
			}
		} else {
			x, err := p.expression()
			if err != nil {
				return nil, err
			}
			n.Init = &ExprStmt{base{x.P()}, x}
		}
	}
	if _, err := p.expect(";"); err != nil {
		return nil, err
	}
	if !p.is(";") {
		if n.Test, err = p.expression(); err != nil {
			return nil, err
		}
	}
	if _, err := p.expect(";"); err != nil {
		return nil, err
	}
	if !p.is(")") {
		if n.Update, err = p.expression(); err != nil {
			return nil, err
		}
	}
	if _, err := p.expect(")"); err != nil {
		return nil, err
	}
	if n.Body, err = p.statement(false); err != nil {
		return nil, err
	}
	Walk(n.Body, func(x Node) {
		if _, ok := x.(*Func); ok {
			n.Closures = true
		}
	})
	return n, nil
}

func (p *parser) tryStmt() (Node, error) {
	t := p.cur()
	p.i++
	body, err := p.block()
	if err != nil {
		return nil, err
	}
	n := &Try{base: base{t.pos}, Body: body}
	if p.isKw("catch") {
		p.i++
		if p.is("(") {
			p.i++
			if n.Param, err = p.bindingTarget(); err != nil {
				return nil, err
			}
			if _, err := p.expect(")"); err != nil {
				return nil, err
			}
		}
		if n.Catch, err = p.block(); err != nil {
			return nil, err
		}
	}
	if p.isKw("finally") {
		p.i++
		if n.Finally, err = p.block(); err != nil {
			return nil, err
		}
	}
	if n.Catch == nil && n.Finally == nil {
		return nil, p.errAt(t.pos, "`try` needs a catch or finally block")
	}
	return n, nil
}

func (p *parser) importStmt() (Node, error) {
	t := p.cur()
	p.i++
	n := &Import{base: base{t.pos}, Names: map[string]string{}}
	if p.cur().kind == tString {
		n.Source = p.cur().val
		p.i++
		return n, p.semicolon()
	}
	if p.cur().kind == tIdent {
		n.Default = p.cur().val
		p.i++
		if p.is(",") {
			p.i++
		}
	}
	if p.is("*") {
		p.i++
		if !p.isCtx("as") {
			return nil, p.errHere("expected `as` after `import *`")
		}
		p.i++
		ns, err := p.ident()
		if err != nil {
			return nil, err
		}
		n.Namespace = ns
	} else if p.is("{") {
		p.i++
		for !p.is("}") {
			imported, err := p.propertyName()
			if err != nil {
				return nil, err
			}
			local := imported
			if p.isCtx("as") {
				p.i++
				if local, err = p.ident(); err != nil {
					return nil, err
				}
			}
			n.Names[local] = imported
			if !p.is("}") {
				if _, err := p.expect(","); err != nil {
					return nil, err
				}
			}
		}
		p.i++
	}
	if !p.isCtx("from") {
		return nil, p.errHere("expected `from` in import")
	}
	p.i++
	if p.cur().kind != tString {
		return nil, p.errHere("expected module path string after `from`")
	}
	n.Source = p.cur().val
	p.i++
	return n, p.semicolon()
}

func (p *parser) exportStmt() (Node, error) {
	t := p.cur()
	p.i++
	n := &Export{base: base{t.pos}}
	switch {
	case p.isKw("default"):
		p.i++
		var err error
		if p.isKw("function") || (p.isCtx("async") && p.peekTok(1).val == "function") {
			async := p.isCtx("async")
			if async {
				p.i++
			}
			n.Default, err = p.function(async)
		} else {
			n.Default, err = p.assignment()
			if err == nil {
				err = p.semicolon()
			}
		}
		return n, err
	case p.isKw("const") || p.isKw("let") || p.isKw("var") || p.isKw("function") || p.isCtx("async"):
		d, err := p.statement(true)
		if err != nil {
			return nil, err
		}
		n.Decl = d
		return n, nil
	case p.is("{"):
		p.i++
		n.Names = map[string]string{}
		for !p.is("}") {
			local, err := p.ident()
			if err != nil {
				return nil, err
			}
			exported := local
			if p.isCtx("as") {
				p.i++
				if exported, err = p.propertyName(); err != nil {
					return nil, err
				}
			}
			n.Names[exported] = local
			if !p.is("}") {
				if _, err := p.expect(","); err != nil {
					return nil, err
				}
			}
		}
		p.i++
		if p.isCtx("from") {
			return nil, p.errHere("re-export (`export ... from`) is not supported")
		}
		return n, p.semicolon()
	}
	return nil, p.errHere("unsupported export form")
}

// function parses `function name(params) { body }`; current token is `function`.
func (p *parser) function(async bool) (*Func, error) {
	t := p.cur()
	p.i++
	if p.is("*") {
		return nil, p.errHere("generator functions are not supported")
	}
	f := &Func{base: base{t.pos}, Async: async}
	if p.cur().kind == tIdent {
		f.Name = p.cur().val
		p.i++
	}
	params, err := p.paramList()
	if err != nil {
		return nil, err
	}
	f.Params = params
	body, err := p.funcBody(async)
	if err != nil {
		return nil, err
	}
	f.Body = body
	return f, nil
}

func (p *parser) paramList() ([]Param, error) {
	if _, err := p.expect("("); err != nil {
		return nil, err
	}
	var ps []Param
	for !p.is(")") {
		prm, err := p.param()
		if err != nil {
			return nil, err
		}
		ps = append(ps, prm)
		if prm.Rest && !p.is(")") {
			return nil, p.errHere("rest parameter must be last")
		}
		if !p.is(")") {
			if _, err := p.expect(","); err != nil {
				return nil, err
			}
		}
	}
	p.i++
	return ps, nil
}

func (p *parser) funcBody(async bool) ([]Node, error) {
	p.fnDepth++
	p.asyncDepth = append(p.asyncDepth, async)
	defer func() { p.fnDepth--; p.asyncDepth = p.asyncDepth[:len(p.asyncDepth)-1] }()
	b, err := p.block()
	if err != nil {
		return nil, err
	}
	return b.Body, nil
}

// inAsync: top level of a module allows await (top-level await).
func (p *parser) inAsync() bool {
	if len(p.asyncDepth) == 0 {
		return true
	}
	return p.asyncDepth[len(p.asyncDepth)-1]
}

// ---------------- expressions (Pratt-style precedence climbing) ----------------

func (p *parser) expression() (Node, error) {
	x, err := p.assignment()
	if err != nil {
		return nil, err
	}
	if p.is(",") {
		s := &Seq{base: base{x.P()}, List: []Node{x}}
		for p.is(",") {
			p.i++
			y, err := p.assignment()
			if err != nil {
				return nil, err
			}
			s.List = append(s.List, y)
		}
		return s, nil
	}
	return x, nil
}

var assignOps = map[string]bool{"=": true, "+=": true, "-=": true, "*=": true, "/=": true, "%=": true, "**=": true,
	"&&=": true, "||=": true, "??=": true, "<<=": true, ">>=": true, ">>>=": true, "&=": true, "|=": true, "^=": true}

func (p *parser) assignment() (Node, error) {
	if arrow, ok, err := p.tryArrow(); ok || err != nil {
		return arrow, err
	}
	pos := p.cur().pos
	left, err := p.conditional()
	if err != nil {
		return nil, err
	}
	if p.cur().kind == tPunct && assignOps[p.cur().val] {
		op := p.cur().val
		switch left.(type) {
		case *Ident, *Member:
		case *ArrayLit, *ObjectLit:
			if op != "=" {
				return nil, p.errAt(pos, "invalid assignment target")
			}
			target, err := toPattern(left)
			if err != nil {
				return nil, p.errAt(pos, "%s", err.Error())
			}
			left = target
		default:
			return nil, p.errAt(pos, "invalid assignment target")
		}
		p.i++
		val, err := p.assignment()
		if err != nil {
			return nil, err
		}
		return &Assign{base: base{pos}, Op: op, Target: left, Value: val}, nil
	}
	return left, nil
}

// toPattern converts an array/object literal used as an assignment target.
func toPattern(n Node) (Node, error) {
	switch v := n.(type) {
	case *Ident, *Member:
		return v, nil
	case *ArrayLit:
		ap := &ArrayPattern{base: v.base}
		for _, e := range v.Elems {
			if e == nil {
				ap.Elems = append(ap.Elems, Param{})
				continue
			}
			prm := Param{}
			if sp, ok := e.(*Spread); ok {
				prm.Rest = true
				e = sp.X
			}
			if as, ok := e.(*Assign); ok && as.Op == "=" {
				prm.Default = as.Value
				e = as.Target
			}
			t, err := toPattern(e)
			if err != nil {
				return nil, err
			}
			prm.Target = t
			ap.Elems = append(ap.Elems, prm)
		}
		return ap, nil
	case *ObjectLit:
		op := &ObjectPattern{base: v.base}
		for _, pr := range v.Props {
			if pr.Spread {
				op.Rest = pr.Value
				continue
			}
			if pr.Computed != nil {
				return nil, fmt.Errorf("computed keys are not supported in patterns")
			}
			pp := PatternProp{Key: pr.Key}
			val := pr.Value
			if as, ok := val.(*Assign); ok && as.Op == "=" {
				pp.Default = as.Value
				val = as.Target
			}
			t, err := toPattern(val)
			if err != nil {
				return nil, err
			}
			pp.Target = t
			op.Props = append(op.Props, pp)
		}
		return op, nil
	}
	return nil, fmt.Errorf("invalid destructuring target")
}

// tryArrow detects `x => ...`, `(a, b) => ...`, `async x => ...`, `async (a) => ...`.
func (p *parser) tryArrow() (Node, bool, error) {
	start := p.i
	pos := p.cur().pos
	async := false
	if p.isCtx("async") && !p.peekTok(1).nlBefore && (p.peekTok(1).kind == tIdent || p.peekTok(1).val == "(") {
		// `async(...)` could be a call to a function named async; we only treat it
		// as an arrow if `=>` follows the parameter list.
		async = true
		p.i++
	}
	var params []Param
	if p.cur().kind == tIdent && p.peekTok(1).val == "=>" {
		params = []Param{{Target: &Ident{base: base{p.cur().pos}, Name: p.cur().val}}}
		p.i++
	} else if p.is("(") {
		// scan to the matching paren and check for =>
		depth, j := 0, p.i
		for ; j < len(p.toks); j++ {
			v := p.toks[j]
			if v.kind == tPunct {
				if v.val == "(" || v.val == "[" || v.val == "{" {
					depth++
				} else if v.val == ")" || v.val == "]" || v.val == "}" {
					depth--
					if depth == 0 {
						break
					}
				}
			}
			if v.kind == tEOF {
				break
			}
		}
		if j+1 >= len(p.toks) || p.toks[j+1].val != "=>" || p.toks[j+1].kind != tPunct {
			p.i = start
			return nil, false, nil
		}
		var err error
		if params, err = p.paramList(); err != nil {
			return nil, true, err
		}
	} else {
		p.i = start
		return nil, false, nil
	}
	if p.cur().nlBefore {
		return nil, true, p.errHere("no line break allowed before `=>`")
	}
	if _, err := p.expect("=>"); err != nil {
		return nil, true, err
	}
	f := &Func{base: base{pos}, Params: params, Arrow: true, Async: async}
	if p.is("{") {
		body, err := p.funcBody(async)
		if err != nil {
			return nil, true, err
		}
		f.Body = body
		return f, true, nil
	}
	p.fnDepth++
	p.asyncDepth = append(p.asyncDepth, async)
	x, err := p.assignment()
	p.fnDepth--
	p.asyncDepth = p.asyncDepth[:len(p.asyncDepth)-1]
	if err != nil {
		return nil, true, err
	}
	f.Body = []Node{&Return{base{x.P()}, x}}
	return f, true, nil
}

func (p *parser) conditional() (Node, error) {
	test, err := p.binary(0)
	if err != nil {
		return nil, err
	}
	if !p.is("?") {
		return test, nil
	}
	p.i++
	then, err := p.assignment()
	if err != nil {
		return nil, err
	}
	if _, err := p.expect(":"); err != nil {
		return nil, err
	}
	els, err := p.assignment()
	if err != nil {
		return nil, err
	}
	return &Cond{base: base{test.P()}, Test: test, Then: then, Else: els}, nil
}

var binPrec = map[string]int{
	"??": 1,
	"||": 2, "&&": 3,
	"|": 4, "^": 5, "&": 6,
	"==": 7, "!=": 7, "===": 7, "!==": 7,
	"<": 8, ">": 8, "<=": 8, ">=": 8, "in": 8,
	"<<": 9, ">>": 9, ">>>": 9,
	"+": 10, "-": 10,
	"*": 11, "/": 11, "%": 11,
	"**": 12,
}

func (p *parser) binOp() (string, int, bool) {
	t := p.cur()
	if t.kind == tPunct || (t.kind == tKeyword && t.val == "in") {
		if pr, ok := binPrec[t.val]; ok {
			return t.val, pr, true
		}
	}
	return "", 0, false
}

func (p *parser) binary(minPrec int) (Node, error) {
	left, err := p.unary()
	if err != nil {
		return nil, err
	}
	for {
		op, prec, ok := p.binOp()
		if !ok || prec <= minPrec && !(op == "**" && prec == minPrec && false) {
			if !ok || prec <= minPrec {
				return left, nil
			}
		}
		p.i++
		next := prec
		if op == "**" {
			next = prec - 1 // right-associative
		}
		right, err := p.binary(next)
		if err != nil {
			return nil, err
		}
		switch op {
		case "&&", "||", "??":
			left = &Logical{base: base{left.P()}, Op: op, L: left, R: right}
		default:
			left = &Binary{base: base{left.P()}, Op: op, L: left, R: right}
		}
	}
}

func (p *parser) unary() (Node, error) {
	t := p.cur()
	if t.kind == tPunct {
		switch t.val {
		case "!", "-", "+", "~":
			p.i++
			x, err := p.unary()
			if err != nil {
				return nil, err
			}
			if t.val == "-" && p.is("**") {
				return nil, p.errHere("unary minus before ** needs parentheses")
			}
			return &Unary{base: base{t.pos}, Op: t.val, X: x}, nil
		case "++", "--":
			p.i++
			x, err := p.unary()
			if err != nil {
				return nil, err
			}
			if err := checkUpdateTarget(p, x); err != nil {
				return nil, err
			}
			return &Update{base: base{t.pos}, Op: t.val, Prefix: true, X: x}, nil
		}
	}
	if t.kind == tKeyword {
		switch t.val {
		case "typeof", "void":
			p.i++
			x, err := p.unary()
			if err != nil {
				return nil, err
			}
			return &Unary{base: base{t.pos}, Op: t.val, X: x}, nil
		case "await":
			if !p.inAsync() {
				return nil, p.errHere("`await` is only valid in async functions and at the top level of a module")
			}
			p.i++
			x, err := p.unary()
			if err != nil {
				return nil, err
			}
			return &Await{base: base{t.pos}, X: x}, nil
		}
	}
	x, err := p.postfix()
	if err != nil {
		return nil, err
	}
	if (p.is("++") || p.is("--")) && !p.cur().nlBefore {
		op := p.cur().val
		if err := checkUpdateTarget(p, x); err != nil {
			return nil, err
		}
		p.i++
		return &Update{base: base{x.P()}, Op: op, X: x}, nil
	}
	return x, nil
}

func checkUpdateTarget(p *parser, x Node) error {
	switch x.(type) {
	case *Ident, *Member:
		return nil
	}
	return p.errAt(x.P(), "invalid increment/decrement target")
}

func (p *parser) postfix() (Node, error) {
	x, err := p.primary()
	if err != nil {
		return nil, err
	}
	for {
		t := p.cur()
		switch {
		case p.is("."):
			p.i++
			name, err := p.propertyName()
			if err != nil {
				return nil, err
			}
			x = &Member{base: base{t.pos}, Obj: x, Name: name}
		case p.is("?."):
			p.i++
			switch {
			case p.is("("):
				args, err := p.args()
				if err != nil {
					return nil, err
				}
				x = &Call{base: base{t.pos}, Callee: x, Args: args, Optional: true}
			case p.is("["):
				p.i++
				k, err := p.expression()
				if err != nil {
					return nil, err
				}
				if _, err := p.expect("]"); err != nil {
					return nil, err
				}
				x = &Member{base: base{t.pos}, Obj: x, Computed: k, Optional: true}
			default:
				name, err := p.propertyName()
				if err != nil {
					return nil, err
				}
				x = &Member{base: base{t.pos}, Obj: x, Name: name, Optional: true}
			}
		case p.is("["):
			p.i++
			k, err := p.expression()
			if err != nil {
				return nil, err
			}
			if _, err := p.expect("]"); err != nil {
				return nil, err
			}
			x = &Member{base: base{t.pos}, Obj: x, Computed: k}
		case p.is("("):
			args, err := p.args()
			if err != nil {
				return nil, err
			}
			x = &Call{base: base{t.pos}, Callee: x, Args: args}
		case t.kind == tTemplate:
			return nil, p.errHere("tagged templates are not supported")
		default:
			return x, nil
		}
	}
}

func (p *parser) args() ([]Node, error) {
	p.i++ // (
	var args []Node
	for !p.is(")") {
		var a Node
		var err error
		if p.is("...") {
			sp := p.cur().pos
			p.i++
			x, err := p.assignment()
			if err != nil {
				return nil, err
			}
			a = &Spread{base{sp}, x}
		} else if a, err = p.assignment(); err != nil {
			return nil, err
		}
		args = append(args, a)
		if !p.is(")") {
			if _, err := p.expect(","); err != nil {
				return nil, err
			}
		}
	}
	p.i++
	return args, nil
}

func (p *parser) primary() (Node, error) {
	t := p.cur()
	switch t.kind {
	case tNumber:
		p.i++
		return &NumberLit{base: base{t.pos}, V: t.num, boxed: num(t.num)}, nil
	case tString:
		p.i++
		return &StringLit{base: base{t.pos}, V: t.val, boxed: t.val}, nil
	case tTemplate:
		p.i++
		return p.templateLit(t)
	case tIdent:
		if t.val == "eval" {
			return nil, p.errHere("`eval` is not supported; nothing is evaluated from strings")
		}
		if t.val == "async" && p.peekTok(1).val == "function" {
			p.i++
			return p.function(true)
		}
		p.i++
		return &Ident{base: base{t.pos}, Name: t.val}, nil
	case tKeyword:
		if msg, bad := unsupportedKw[t.val]; bad && msg != "" {
			return nil, p.errHere("%s", msg)
		}
		switch t.val {
		case "true", "false":
			p.i++
			return &BoolLit{base{t.pos}, t.val == "true"}, nil
		case "null":
			p.i++
			return &NullLit{base{t.pos}}, nil
		case "undefined":
			p.i++
			return &UndefLit{base{t.pos}}, nil
		case "function":
			return p.function(false)
		case "import":
			return nil, p.errHere("dynamic import() is not supported")
		}
	case tPunct:
		switch t.val {
		case "(":
			p.i++
			x, err := p.expression()
			if err != nil {
				return nil, err
			}
			if _, err := p.expect(")"); err != nil {
				return nil, err
			}
			return x, nil
		case "[":
			return p.arrayLit()
		case "{":
			return p.objectLit()
		case "/", "/=":
			return nil, p.errHere("regex literals are not supported; use string methods such as includes, startsWith, split and replaceAll")
		}
	case tEOF:
		return nil, p.errHere("unexpected end of input")
	}
	return nil, p.errHere("unexpected %s", p.desc())
}

func (p *parser) arrayLit() (Node, error) {
	t := p.cur()
	p.i++
	a := &ArrayLit{base: base{t.pos}}
	for !p.is("]") {
		if p.at(tEOF, "") {
			return nil, p.errAt(t.pos, "unclosed array literal")
		}
		if p.is(",") {
			p.i++
			a.Elems = append(a.Elems, nil)
			continue
		}
		var e Node
		var err error
		if p.is("...") {
			sp := p.cur().pos
			p.i++
			x, err := p.assignment()
			if err != nil {
				return nil, err
			}
			e = &Spread{base{sp}, x}
		} else if e, err = p.assignment(); err != nil {
			return nil, err
		}
		a.Elems = append(a.Elems, e)
		if !p.is("]") {
			if _, err := p.expect(","); err != nil {
				return nil, err
			}
		}
	}
	p.i++
	return a, nil
}

func (p *parser) objectLit() (Node, error) {
	t := p.cur()
	p.i++
	o := &ObjectLit{base: base{t.pos}}
	for !p.is("}") {
		if p.at(tEOF, "") {
			return nil, p.errAt(t.pos, "unclosed object literal")
		}
		if p.is("...") {
			p.i++
			x, err := p.assignment()
			if err != nil {
				return nil, err
			}
			o.Props = append(o.Props, Property{Value: x, Spread: true})
		} else if p.is("[") {
			p.i++
			k, err := p.assignment()
			if err != nil {
				return nil, err
			}
			if _, err := p.expect("]"); err != nil {
				return nil, err
			}
			if _, err := p.expect(":"); err != nil {
				return nil, err
			}
			v, err := p.assignment()
			if err != nil {
				return nil, err
			}
			o.Props = append(o.Props, Property{Computed: k, Value: v})
		} else {
			kt := p.cur()
			async := false
			if kt.kind == tIdent && kt.val == "async" && p.peekTok(1).kind != tPunct {
				async = true
				p.i++
				kt = p.cur()
			}
			key, err := p.propertyName()
			if err != nil {
				return nil, err
			}
			switch {
			case p.is("("): // method shorthand
				params, err := p.paramList()
				if err != nil {
					return nil, err
				}
				body, err := p.funcBody(async)
				if err != nil {
					return nil, err
				}
				o.Props = append(o.Props, Property{Key: key, Value: &Func{base: base{kt.pos}, Name: key, Params: params, Body: body, Async: async}})
			case p.is(":"):
				p.i++
				v, err := p.assignment()
				if err != nil {
					return nil, err
				}
				o.Props = append(o.Props, Property{Key: key, Value: v})
			default:
				if kt.kind != tIdent {
					return nil, p.errAt(kt.pos, "expected \":\" after property name %q", key)
				}
				var v Node = &Ident{base: base{kt.pos}, Name: key}
				if p.is("=") { // only valid when re-parsed as a pattern
					p.i++
					d, err := p.assignment()
					if err != nil {
						return nil, err
					}
					v = &Assign{base: base{kt.pos}, Op: "=", Target: v, Value: d}
				}
				o.Props = append(o.Props, Property{Key: key, Value: v})
			}
		}
		if !p.is("}") {
			if _, err := p.expect(","); err != nil {
				return nil, err
			}
		}
	}
	p.i++
	return o, nil
}

func (p *parser) templateLit(t token) (Node, error) {
	parts := strings.Split(t.val, "\x00")
	n := &TemplateLit{base: base{t.pos}}
	for k, part := range parts {
		if k%2 == 0 {
			n.Quasis = append(n.Quasis, strings.ReplaceAll(part, "␀", "\x00"))
			continue
		}
		toks, err := lex(part, p.file)
		var x Node
		if err == nil {
			sub := &parser{toks: toks, file: p.file, fnDepth: p.fnDepth, asyncDepth: p.asyncDepth}
			if x, err = sub.expression(); err == nil && !sub.at(tEOF, "") {
				err = sub.errHere("unexpected %s in template expression", sub.desc())
			}
		}
		if err != nil {
			if e, ok := err.(*Error); ok {
				e.Pos = t.pos // best effort: point at the template start
			}
			return nil, err
		}
		n.Exprs = append(n.Exprs, x)
	}
	return n, nil
}
