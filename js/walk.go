package js

// Walk calls fn for every node in the tree, parents before children.
func Walk(n Node, fn func(Node)) {
	if n == nil {
		return
	}
	fn(n)
	w := func(c Node) { Walk(c, fn) }
	ws := func(cs []Node) {
		for _, c := range cs {
			if c != nil {
				Walk(c, fn)
			}
		}
	}
	params := func(ps []Param) {
		for _, p := range ps {
			if p.Target != nil {
				w(p.Target)
			}
			if p.Default != nil {
				w(p.Default)
			}
		}
	}
	switch x := n.(type) {
	case *Program:
		ws(x.Body)
	case *TemplateLit:
		ws(x.Exprs)
	case *ArrayLit:
		ws(x.Elems)
	case *ObjectLit:
		for _, p := range x.Props {
			if p.Computed != nil {
				w(p.Computed)
			}
			w(p.Value)
		}
	case *Spread:
		w(x.X)
	case *Unary:
		w(x.X)
	case *Update:
		w(x.X)
	case *Binary:
		w(x.L)
		w(x.R)
	case *Logical:
		w(x.L)
		w(x.R)
	case *Assign:
		w(x.Target)
		w(x.Value)
	case *Cond:
		w(x.Test)
		w(x.Then)
		w(x.Else)
	case *Member:
		w(x.Obj)
		if x.Computed != nil {
			w(x.Computed)
		}
	case *Call:
		w(x.Callee)
		ws(x.Args)
	case *Await:
		w(x.X)
	case *Func:
		params(x.Params)
		ws(x.Body)
	case *Seq:
		ws(x.List)
	case *ArrayPattern:
		params(x.Elems)
	case *ObjectPattern:
		for _, p := range x.Props {
			w(p.Target)
			if p.Default != nil {
				w(p.Default)
			}
		}
	case *VarDecl:
		for _, d := range x.Decls {
			w(d.Target)
			if d.Init != nil {
				w(d.Init)
			}
		}
	case *ExprStmt:
		w(x.X)
	case *Block:
		ws(x.Body)
	case *If:
		w(x.Test)
		w(x.Then)
		if x.Else != nil {
			w(x.Else)
		}
	case *For:
		if x.Init != nil {
			w(x.Init)
		}
		if x.Test != nil {
			w(x.Test)
		}
		if x.Update != nil {
			w(x.Update)
		}
		w(x.Body)
	case *ForOf:
		w(x.Left)
		w(x.Right)
		w(x.Body)
	case *While:
		w(x.Test)
		w(x.Body)
	case *Return:
		if x.X != nil {
			w(x.X)
		}
	case *ThrowStmt:
		w(x.X)
	case *Try:
		w(x.Body)
		if x.Param != nil {
			w(x.Param)
		}
		if x.Catch != nil {
			w(x.Catch)
		}
		if x.Finally != nil {
			w(x.Finally)
		}
	case *Export:
		if x.Decl != nil {
			w(x.Decl)
		}
		if x.Default != nil {
			w(x.Default)
		}
	}
}

// WithLock runs fn holding the interpreter lock, then waits for background
// work like Run does. Use it to call a function value a program returned.
func (it *Interp) WithLock(fn func(r *Runner)) {
	it.gil.Lock()
	fn(&Runner{it: it})
	it.gil.Unlock()
	if err := it.finish(nil, "", nil); err != nil {
		it.lastErr = err
	}
}

// LastError returns a fatal error or unhandled rejection found by WithLock.
func (it *Interp) LastError() error { return it.lastErr }
