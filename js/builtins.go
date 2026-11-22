package js

import (
	"context"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"
)

func bi(name string, fn func(r *Runner, this Value, args []Value) (Value, error)) *Builtin {
	return &Builtin{Name: name, Fn: fn}
}

func arg(args []Value, i int) Value {
	if i < len(args) {
		return args[i]
	}
	return Undefined
}

func frozen(kv ...any) *Object {
	o := NewObject()
	for i := 0; i < len(kv); i += 2 {
		o.Set(kv[i].(string), kv[i+1])
	}
	o.Frozen = true
	return o
}

func installGlobals(it *Interp) {
	g := it.globals
	printFn := bi("print", func(r *Runner, _ Value, args []Value) (Value, error) {
		parts := make([]string, len(args))
		for i, a := range args {
			parts[i] = Inspect(a)
		}
		fmt.Fprintln(r.it.cfg.Stdout, strings.Join(parts, " "))
		return Undefined, nil
	})
	g.declare("print", printFn, "const")
	g.declare("console", frozen("log", printFn, "info", printFn, "warn", printFn, "error", printFn), "const")
	g.declare("NaN", math.NaN(), "const")
	g.declare("Infinity", math.Inf(1), "const")

	g.declare("JSON", frozen(
		"stringify", bi("JSON.stringify", func(r *Runner, _ Value, args []Value) (Value, error) {
			indent := ""
			switch sp := arg(args, 2).(type) {
			case float64:
				indent = strings.Repeat(" ", int(math.Min(10, math.Max(0, sp))))
			case string:
				indent = sp
			}
			return jsonStringify(r.it, arg(args, 0), indent)
		}),
		"parse", bi("JSON.parse", func(r *Runner, _ Value, args []Value) (Value, error) {
			v, err := jsonParse(toString(arg(args, 0)))
			if err != nil {
				return nil, &Throw{Value: makeError("SyntaxError", "JSON.parse: "+err.Error())}
			}
			return v, nil
		}),
	), "const")

	m1 := func(name string, f func(float64) float64) *Builtin {
		return bi("Math."+name, func(_ *Runner, _ Value, a []Value) (Value, error) { return f(toNumber(arg(a, 0))), nil })
	}
	g.declare("Math", frozen(
		"PI", math.Pi, "E", math.E,
		"abs", m1("abs", math.Abs), "floor", m1("floor", math.Floor), "ceil", m1("ceil", math.Ceil),
		"round", m1("round", func(x float64) float64 { return math.Floor(x + 0.5) }),
		"trunc", m1("trunc", math.Trunc), "sqrt", m1("sqrt", math.Sqrt), "log", m1("log", math.Log),
		"sign", m1("sign", func(x float64) float64 {
			switch {
			case x > 0:
				return 1
			case x < 0:
				return -1
			}
			return x
		}),
		"pow", bi("Math.pow", func(_ *Runner, _ Value, a []Value) (Value, error) {
			return math.Pow(toNumber(arg(a, 0)), toNumber(arg(a, 1))), nil
		}),
		"min", bi("Math.min", func(_ *Runner, _ Value, a []Value) (Value, error) {
			m := math.Inf(1)
			for _, v := range a {
				n := toNumber(v)
				if math.IsNaN(n) {
					return n, nil
				}
				m = math.Min(m, n)
			}
			return m, nil
		}),
		"max", bi("Math.max", func(_ *Runner, _ Value, a []Value) (Value, error) {
			m := math.Inf(-1)
			for _, v := range a {
				n := toNumber(v)
				if math.IsNaN(n) {
					return n, nil
				}
				m = math.Max(m, n)
			}
			return m, nil
		}),
	), "const")

	g.declare("Object", frozen(
		"keys", bi("Object.keys", func(_ *Runner, _ Value, a []Value) (Value, error) {
			out := &Array{}
			switch o := arg(a, 0).(type) {
			case *Object:
				for _, k := range o.Keys() {
					out.Elems = append(out.Elems, k)
				}
			case *Array:
				for i := range o.Elems {
					out.Elems = append(out.Elems, numberToString(float64(i)))
				}
			}
			return out, nil
		}),
		"values", bi("Object.values", func(_ *Runner, _ Value, a []Value) (Value, error) {
			out := &Array{}
			switch o := arg(a, 0).(type) {
			case *Object:
				for _, k := range o.Keys() {
					out.Elems = append(out.Elems, o.vals[k])
				}
			case *Array:
				out.Elems = append(out.Elems, o.Elems...)
			}
			return out, nil
		}),
		"entries", bi("Object.entries", func(_ *Runner, _ Value, a []Value) (Value, error) {
			out := &Array{}
			if o, ok := arg(a, 0).(*Object); ok {
				for _, k := range o.Keys() {
					out.Elems = append(out.Elems, &Array{Elems: []Value{k, o.vals[k]}})
				}
			}
			return out, nil
		}),
		"fromEntries", bi("Object.fromEntries", func(r *Runner, _ Value, a []Value) (Value, error) {
			o := NewObject()
			items, err := r.iterate(arg(a, 0), nil)
			if err != nil {
				return nil, err
			}
			for _, it := range items {
				pair, ok := it.(*Array)
				if !ok || len(pair.Elems) < 2 {
					return nil, r.it.typeError("Object.fromEntries needs [key, value] pairs")
				}
				o.Set(toString(pair.Elems[0]), pair.Elems[1])
			}
			return o, nil
		}),
		"assign", bi("Object.assign", func(r *Runner, _ Value, a []Value) (Value, error) {
			t, ok := arg(a, 0).(*Object)
			if !ok || t.Frozen {
				return nil, r.it.typeError("Object.assign needs a writable object target")
			}
			for _, s := range a[1:] {
				if so, ok := s.(*Object); ok {
					for _, k := range so.Keys() {
						t.Set(k, so.vals[k])
					}
				}
			}
			return t, nil
		}),
	), "const")

	g.declare("Array", frozen(
		"isArray", bi("Array.isArray", func(_ *Runner, _ Value, a []Value) (Value, error) {
			_, ok := arg(a, 0).(*Array)
			return ok, nil
		}),
		"from", bi("Array.from", func(r *Runner, _ Value, a []Value) (Value, error) {
			src := arg(a, 0)
			var items []Value
			if o, ok := src.(*Object); ok {
				n := int(toNumber(o.vals["length"]))
				if n < 0 || n > 1<<24 {
					return nil, &Throw{Value: makeError("RangeError", "invalid length")}
				}
				items = make([]Value, n)
				for i := range items {
					items[i] = Undefined
				}
			} else {
				var err error
				if items, err = r.iterate(src, nil); err != nil {
					return nil, err
				}
			}
			out := &Array{Elems: items}
			if fn := arg(a, 1); fn != Undefined {
				for i, v := range out.Elems {
					m, err := r.Call(fn, v, float64(i))
					if err != nil {
						return nil, err
					}
					out.Elems[i] = m
				}
			}
			return out, nil
		}),
	), "const")

	g.declare("String", bi("String", func(_ *Runner, _ Value, a []Value) (Value, error) {
		if len(a) == 0 {
			return "", nil
		}
		return toString(a[0]), nil
	}), "const")
	g.declare("Number", bi("Number", func(_ *Runner, _ Value, a []Value) (Value, error) {
		if len(a) == 0 {
			return 0.0, nil
		}
		return toNumber(a[0]), nil
	}), "const")
	g.declare("Boolean", bi("Boolean", func(_ *Runner, _ Value, a []Value) (Value, error) {
		return truthy(arg(a, 0)), nil
	}), "const")
	g.declare("isNaN", bi("isNaN", func(_ *Runner, _ Value, a []Value) (Value, error) {
		return math.IsNaN(toNumber(arg(a, 0))), nil
	}), "const")
	g.declare("parseFloat", bi("parseFloat", func(_ *Runner, _ Value, a []Value) (Value, error) {
		s := strings.TrimSpace(toString(arg(a, 0)))
		end := 0
		for end < len(s) && strings.ContainsRune("0123456789.eE+-", rune(s[end])) {
			end++
		}
		for end > 0 {
			if f, err := strconv.ParseFloat(s[:end], 64); err == nil {
				return f, nil
			}
			end--
		}
		return math.NaN(), nil
	}), "const")
	g.declare("parseInt", bi("parseInt", func(_ *Runner, _ Value, a []Value) (Value, error) {
		s := strings.TrimSpace(toString(arg(a, 0)))
		base := 10
		if b := arg(a, 1); b != Undefined {
			base = int(toNumber(b))
		}
		neg := false
		if strings.HasPrefix(s, "-") || strings.HasPrefix(s, "+") {
			neg = s[0] == '-'
			s = s[1:]
		}
		if (base == 16 || base == 10 && arg(a, 1) == Undefined) && (strings.HasPrefix(s, "0x") || strings.HasPrefix(s, "0X")) {
			base, s = 16, s[2:]
		}
		end := 0
		for end < len(s) {
			c := s[end]
			var d int
			switch {
			case c >= '0' && c <= '9':
				d = int(c - '0')
			case c >= 'a' && c <= 'z':
				d = int(c-'a') + 10
			case c >= 'A' && c <= 'Z':
				d = int(c-'A') + 10
			default:
				d = 99
			}
			if d >= base {
				break
			}
			end++
		}
		if end == 0 {
			return math.NaN(), nil
		}
		n, err := strconv.ParseInt(s[:end], base, 64)
		if err != nil {
			return math.NaN(), nil
		}
		if neg {
			n = -n
		}
		return float64(n), nil
	}), "const")

	g.declare("sleep", bi("sleep", func(r *Runner, _ Value, a []Value) (Value, error) {
		d := time.Duration(toNumber(arg(a, 0)) * float64(time.Millisecond))
		p := r.it.newPromise()
		r.it.tasks.Add(1)
		go func() {
			defer r.it.tasks.Done()
			t := time.NewTimer(d)
			defer t.Stop()
			select {
			case <-t.C:
				p.settle(1, Undefined)
			case <-r.it.ctx.Done():
				p.settle(2, makeError("Error", "cancelled"))
			}
		}()
		return p, nil
	}), "const")

	g.declare("stream", bi("stream", func(r *Runner, _ Value, a []Value) (Value, error) {
		items, err := r.iterate(arg(a, 0), nil)
		if err != nil {
			return nil, err
		}
		return NewStream(FromValues(items)), nil
	}), "const")
	g.declare("pmap", bi("pmap", pmapBuiltin), "const")
	g.declare("Error", errorCtor("Error"), "const")
	g.declare("TypeError", errorCtor("TypeError"), "const")
	g.declare("RangeError", errorCtor("RangeError"), "const")

	g.declare("Promise", frozen(
		"resolve", bi("Promise.resolve", func(r *Runner, _ Value, a []Value) (Value, error) {
			if p, ok := arg(a, 0).(*Promise); ok {
				return p, nil
			}
			p := newPromise()
			p.settle(1, arg(a, 0))
			return p, nil
		}),
		"reject", bi("Promise.reject", func(r *Runner, _ Value, a []Value) (Value, error) {
			p := r.it.newPromise()
			p.settle(2, arg(a, 0))
			return p, nil
		}),
		"all", bi("Promise.all", func(r *Runner, _ Value, a []Value) (Value, error) { return promiseCombine(r, a, "all") }),
		"allSettled", bi("Promise.allSettled", func(r *Runner, _ Value, a []Value) (Value, error) { return promiseCombine(r, a, "allSettled") }),
		"race", bi("Promise.race", func(r *Runner, _ Value, a []Value) (Value, error) { return promiseCombine(r, a, "race") }),
		"any", bi("Promise.any", func(r *Runner, _ Value, a []Value) (Value, error) { return promiseCombine(r, a, "any") }),
	), "const")
}

// errorCtor: Error("msg") makes an error object (no `new` needed or allowed).
func errorCtor(name string) *Builtin {
	return bi(name, func(_ *Runner, _ Value, a []Value) (Value, error) {
		msg := ""
		if m := arg(a, 0); m != Undefined {
			msg = toString(m)
		}
		e := makeError(name, msg)
		if opts, ok := arg(a, 1).(*Object); ok {
			if c, ok := opts.Get("cause"); ok {
				e.Set("cause", c)
			}
		}
		return e, nil
	})
}

func asPromise(v Value) *Promise {
	if p, ok := v.(*Promise); ok {
		return p
	}
	p := newPromise()
	p.settle(1, v)
	return p
}

func promiseCombine(r *Runner, a []Value, mode string) (Value, error) {
	items, err := r.iterate(arg(a, 0), nil)
	if err != nil {
		return nil, err
	}
	ps := make([]*Promise, len(items))
	for i, it := range items {
		ps[i] = asPromise(it)
		ps[i].mu.Lock()
		ps[i].handled = true
		ps[i].mu.Unlock()
	}
	it := r.it
	out := it.newPromise()
	if len(ps) == 0 {
		switch mode {
		case "race":
			return out, nil // forever pending, as in JS
		case "any":
			out.settle(2, makeError("AggregateError", "All promises were rejected"))
		default:
			out.settle(1, &Array{})
		}
		return out, nil
	}
	type res struct {
		i int
		p *Promise
	}
	ch := make(chan res, len(ps))
	for i, p := range ps {
		i, p := i, p
		it.tasks.Add(1)
		go func() {
			defer it.tasks.Done()
			select {
			case <-p.done:
				ch <- res{i, p}
			case <-it.ctx.Done():
			}
		}()
	}
	it.tasks.Add(1)
	go func() {
		defer it.tasks.Done()
		vals := make([]Value, len(ps))
		for n := 0; n < len(ps); n++ {
			var rr res
			select {
			case rr = <-ch:
			case <-it.ctx.Done():
				out.settle(2, makeError("Error", "cancelled"))
				return
			}
			st, v := rr.p.state, rr.p.value
			switch mode {
			case "all":
				if st == 2 {
					out.settle(2, v)
					return
				}
				vals[rr.i] = v
			case "race":
				out.settle(st, v)
				return
			case "any":
				if st == 1 {
					out.settle(1, v)
					return
				}
				vals[rr.i] = v
			case "allSettled":
				o := NewObject()
				if st == 1 {
					o.Set("status", "fulfilled")
					o.Set("value", v)
				} else {
					o.Set("status", "rejected")
					o.Set("reason", v)
				}
				vals[rr.i] = o
			}
		}
		if mode == "any" {
			e := makeError("AggregateError", "All promises were rejected")
			e.Set("errors", &Array{Elems: vals})
			out.settle(2, e)
			return
		}
		out.settle(1, &Array{Elems: vals})
	}()
	return out, nil
}

// ---------------- member access ----------------

func (r *Runner) getMember(obj Value, key string, at Node) (Value, error) {
	switch o := obj.(type) {
	case *Object:
		if v, ok := o.Get(key); ok {
			return v, nil
		}
		if key == "hasOwnProperty" {
			return bi("hasOwnProperty", func(_ *Runner, _ Value, a []Value) (Value, error) {
				_, ok := o.Get(toString(arg(a, 0)))
				return ok, nil
			}), nil
		}
		return Undefined, nil
	case *Array:
		if key == "length" {
			return float64(len(o.Elems)), nil
		}
		if isArrayIndex(key) {
			i := int(toNumber(key))
			if i < len(o.Elems) {
				return o.Elems[i], nil
			}
			return Undefined, nil
		}
		if m := arrayMethod(o, key); m != nil {
			return m, nil
		}
		return Undefined, nil
	case string:
		if key == "length" {
			return float64(len([]rune(o))), nil
		}
		if isArrayIndex(key) {
			rs := []rune(o)
			i := int(toNumber(key))
			if i < len(rs) {
				return string(rs[i]), nil
			}
			return Undefined, nil
		}
		if m := stringMethod(o, key); m != nil {
			return m, nil
		}
		return Undefined, nil
	case float64:
		switch key {
		case "toFixed":
			return bi("toFixed", func(_ *Runner, _ Value, a []Value) (Value, error) {
				d := int(toNumber(arg(a, 0)))
				if d < 0 || d > 100 {
					return nil, &Throw{Value: makeError("RangeError", "toFixed() digits out of range")}
				}
				return strconv.FormatFloat(o, 'f', d, 64), nil
			}), nil
		case "toString":
			return bi("toString", func(_ *Runner, _ Value, a []Value) (Value, error) {
				if b := arg(a, 0); b != Undefined {
					return strconv.FormatInt(int64(o), int(toNumber(b))), nil
				}
				return numberToString(o), nil
			}), nil
		}
		return Undefined, nil
	case bool:
		if key == "toString" {
			return bi("toString", func(_ *Runner, _ Value, _ []Value) (Value, error) { return toString(o), nil }), nil
		}
		return Undefined, nil
	case *Promise:
		switch key {
		case "then":
			return bi("then", func(r *Runner, _ Value, a []Value) (Value, error) {
				return r.it.then(o, arg(a, 0), arg(a, 1), r.depth, r.file), nil
			}), nil
		case "catch":
			return bi("catch", func(r *Runner, _ Value, a []Value) (Value, error) {
				return r.it.then(o, Undefined, arg(a, 0), r.depth, r.file), nil
			}), nil
		case "finally":
			return bi("finally", func(r *Runner, _ Value, a []Value) (Value, error) {
				cb := arg(a, 0)
				pass := bi("finally", func(r *Runner, _ Value, _ []Value) (Value, error) {
					if _, err := r.Call(cb); err != nil {
						return nil, err
					}
					if o.state == 2 {
						return nil, &Throw{Value: o.value}
					}
					return o.value, nil
				})
				return r.it.then(o, pass, pass, r.depth, r.file), nil
			}), nil
		}
		return Undefined, nil
	case *Function, *Builtin:
		if key == "name" {
			if f, ok := o.(*Function); ok {
				return f.Name, nil
			}
			return o.(*Builtin).Name, nil
		}
		return Undefined, nil
	case *Stream:
		return streamMember(o, key), nil
	case nil, undefinedT:
		return nil, r.throwAt(at, "TypeError", "cannot read properties of %s (reading %q)", toString(obj), key)
	}
	return Undefined, nil
}

func relIndex(v Value, n int, def int) int {
	if v == Undefined {
		return def
	}
	f := toNumber(v)
	if math.IsNaN(f) {
		return 0
	}
	i := int(math.Trunc(f))
	if i < 0 {
		i += n
		if i < 0 {
			i = 0
		}
	}
	if i > n {
		i = n
	}
	return i
}

func arrayMethod(a *Array, key string) *Builtin {
	cbLoop := func(r *Runner, fn Value, each func(i int, v, res Value) (stop bool, err error)) error {
		if _, ok := fn.(*Function); !ok {
			if _, ok := fn.(*Builtin); !ok {
				return r.it.typeError(fmt.Sprintf("%s is not a function", Inspect(fn)))
			}
		}
		for i := 0; i < len(a.Elems); i++ {
			v := a.Elems[i]
			res, err := r.Call(fn, v, float64(i), a)
			if err != nil {
				return err
			}
			stop, err := each(i, v, res)
			if err != nil || stop {
				return err
			}
		}
		return nil
	}
	switch key {
	case "push":
		return bi("push", func(_ *Runner, _ Value, args []Value) (Value, error) {
			a.Elems = append(a.Elems, args...)
			return float64(len(a.Elems)), nil
		})
	case "pop":
		return bi("pop", func(_ *Runner, _ Value, _ []Value) (Value, error) {
			if len(a.Elems) == 0 {
				return Undefined, nil
			}
			v := a.Elems[len(a.Elems)-1]
			a.Elems = a.Elems[:len(a.Elems)-1]
			return v, nil
		})
	case "shift":
		return bi("shift", func(_ *Runner, _ Value, _ []Value) (Value, error) {
			if len(a.Elems) == 0 {
				return Undefined, nil
			}
			v := a.Elems[0]
			a.Elems = a.Elems[1:]
			return v, nil
		})
	case "unshift":
		return bi("unshift", func(_ *Runner, _ Value, args []Value) (Value, error) {
			a.Elems = append(append([]Value{}, args...), a.Elems...)
			return float64(len(a.Elems)), nil
		})
	case "slice":
		return bi("slice", func(_ *Runner, _ Value, args []Value) (Value, error) {
			n := len(a.Elems)
			s, e := relIndex(arg(args, 0), n, 0), relIndex(arg(args, 1), n, n)
			if e < s {
				e = s
			}
			return &Array{Elems: append([]Value{}, a.Elems[s:e]...)}, nil
		})
	case "splice":
		return bi("splice", func(_ *Runner, _ Value, args []Value) (Value, error) {
			n := len(a.Elems)
			s := relIndex(arg(args, 0), n, 0)
			del := n - s
			if len(args) > 1 {
				del = int(math.Max(0, math.Min(float64(n-s), toNumber(args[1]))))
			}
			removed := append([]Value{}, a.Elems[s:s+del]...)
			var ins []Value
			if len(args) > 2 {
				ins = args[2:]
			}
			rest := append(append([]Value{}, ins...), a.Elems[s+del:]...)
			a.Elems = append(a.Elems[:s], rest...)
			return &Array{Elems: removed}, nil
		})
	case "concat":
		return bi("concat", func(_ *Runner, _ Value, args []Value) (Value, error) {
			out := &Array{Elems: append([]Value{}, a.Elems...)}
			for _, x := range args {
				if xa, ok := x.(*Array); ok {
					out.Elems = append(out.Elems, xa.Elems...)
				} else {
					out.Elems = append(out.Elems, x)
				}
			}
			return out, nil
		})
	case "join":
		return bi("join", func(_ *Runner, _ Value, args []Value) (Value, error) {
			sep := ","
			if s := arg(args, 0); s != Undefined {
				sep = toString(s)
			}
			parts := make([]string, len(a.Elems))
			for i, e := range a.Elems {
				if e != nil && e != Undefined {
					parts[i] = toString(e)
				}
			}
			return strings.Join(parts, sep), nil
		})
	case "reverse":
		return bi("reverse", func(_ *Runner, _ Value, _ []Value) (Value, error) {
			for i, j := 0, len(a.Elems)-1; i < j; i, j = i+1, j-1 {
				a.Elems[i], a.Elems[j] = a.Elems[j], a.Elems[i]
			}
			return a, nil
		})
	case "indexOf", "includes", "lastIndexOf":
		return bi(key, func(_ *Runner, _ Value, args []Value) (Value, error) {
			x := arg(args, 0)
			idx := -1
			for i, e := range a.Elems {
				eq := strictEquals(e, x)
				if key == "includes" {
					if f, ok := e.(float64); ok && math.IsNaN(f) {
						if g, ok := x.(float64); ok && math.IsNaN(g) {
							eq = true
						}
					}
				}
				if eq {
					idx = i
					if key != "lastIndexOf" {
						break
					}
				}
			}
			if key == "includes" {
				return idx >= 0, nil
			}
			return float64(idx), nil
		})
	case "map":
		return bi("map", func(r *Runner, _ Value, args []Value) (Value, error) {
			out := &Array{Elems: make([]Value, 0, len(a.Elems))}
			err := cbLoop(r, arg(args, 0), func(_ int, _, res Value) (bool, error) {
				out.Elems = append(out.Elems, res)
				return false, nil
			})
			return out, err
		})
	case "flatMap":
		return bi("flatMap", func(r *Runner, _ Value, args []Value) (Value, error) {
			out := &Array{}
			err := cbLoop(r, arg(args, 0), func(_ int, _, res Value) (bool, error) {
				if ra, ok := res.(*Array); ok {
					out.Elems = append(out.Elems, ra.Elems...)
				} else {
					out.Elems = append(out.Elems, res)
				}
				return false, nil
			})
			return out, err
		})
	case "flat":
		return bi("flat", func(_ *Runner, _ Value, args []Value) (Value, error) {
			depth := 1
			if d := arg(args, 0); d != Undefined {
				depth = int(toNumber(d))
			}
			return &Array{Elems: flatten(a.Elems, depth)}, nil
		})
	case "filter":
		return bi("filter", func(r *Runner, _ Value, args []Value) (Value, error) {
			out := &Array{}
			err := cbLoop(r, arg(args, 0), func(_ int, v, res Value) (bool, error) {
				if truthy(res) {
					out.Elems = append(out.Elems, v)
				}
				return false, nil
			})
			return out, err
		})
	case "forEach":
		return bi("forEach", func(r *Runner, _ Value, args []Value) (Value, error) {
			return Undefined, cbLoop(r, arg(args, 0), func(int, Value, Value) (bool, error) { return false, nil })
		})
	case "find", "findIndex":
		return bi(key, func(r *Runner, _ Value, args []Value) (Value, error) {
			var found Value = Undefined
			idx := -1
			err := cbLoop(r, arg(args, 0), func(i int, v, res Value) (bool, error) {
				if truthy(res) {
					found, idx = v, i
					return true, nil
				}
				return false, nil
			})
			if key == "findIndex" {
				return float64(idx), err
			}
			return found, err
		})
	case "some", "every":
		return bi(key, func(r *Runner, _ Value, args []Value) (Value, error) {
			result := key == "every"
			err := cbLoop(r, arg(args, 0), func(_ int, _, res Value) (bool, error) {
				if key == "some" && truthy(res) {
					result = true
					return true, nil
				}
				if key == "every" && !truthy(res) {
					result = false
					return true, nil
				}
				return false, nil
			})
			return result, err
		})
	case "reduce":
		return bi("reduce", func(r *Runner, _ Value, args []Value) (Value, error) {
			fn := arg(args, 0)
			i := 0
			var acc Value
			if len(args) > 1 {
				acc = args[1]
			} else {
				if len(a.Elems) == 0 {
					return nil, r.it.typeError("reduce of empty array with no initial value")
				}
				acc, i = a.Elems[0], 1
			}
			for ; i < len(a.Elems); i++ {
				var err error
				if acc, err = r.Call(fn, acc, a.Elems[i], float64(i), a); err != nil {
					return nil, err
				}
			}
			return acc, nil
		})
	case "sort":
		return bi("sort", func(r *Runner, _ Value, args []Value) (Value, error) {
			fn := arg(args, 0)
			var sortErr error
			sort.SliceStable(a.Elems, func(i, j int) bool {
				if sortErr != nil {
					return false
				}
				x, y := a.Elems[i], a.Elems[j]
				if x == Undefined || y == Undefined {
					return y == Undefined && x != Undefined
				}
				if fn == Undefined {
					return toString(x) < toString(y)
				}
				res, err := r.Call(fn, x, y)
				if err != nil {
					sortErr = err
					return false
				}
				return toNumber(res) < 0
			})
			return a, sortErr
		})
	case "at":
		return bi("at", func(_ *Runner, _ Value, args []Value) (Value, error) {
			i := int(toNumber(arg(args, 0)))
			if i < 0 {
				i += len(a.Elems)
			}
			if i < 0 || i >= len(a.Elems) {
				return Undefined, nil
			}
			return a.Elems[i], nil
		})
	}
	return nil
}

func flatten(in []Value, depth int) []Value {
	var out []Value
	for _, e := range in {
		if ea, ok := e.(*Array); ok && depth > 0 {
			out = append(out, flatten(ea.Elems, depth-1)...)
		} else {
			out = append(out, e)
		}
	}
	return out
}

func stringMethod(s string, key string) *Builtin {
	str := func(name string, f func(args []Value) Value) *Builtin {
		return bi(name, func(_ *Runner, _ Value, args []Value) (Value, error) { return f(args), nil })
	}
	switch key {
	case "toUpperCase":
		return str(key, func([]Value) Value { return strings.ToUpper(s) })
	case "toLowerCase":
		return str(key, func([]Value) Value { return strings.ToLower(s) })
	case "trim":
		return str(key, func([]Value) Value { return strings.TrimSpace(s) })
	case "trimStart":
		return str(key, func([]Value) Value { return strings.TrimLeft(s, " \t\n\r") })
	case "trimEnd":
		return str(key, func([]Value) Value { return strings.TrimRight(s, " \t\n\r") })
	case "includes":
		return str(key, func(a []Value) Value { return strings.Contains(s, toString(arg(a, 0))) })
	case "startsWith":
		return str(key, func(a []Value) Value { return strings.HasPrefix(s, toString(arg(a, 0))) })
	case "endsWith":
		return str(key, func(a []Value) Value { return strings.HasSuffix(s, toString(arg(a, 0))) })
	case "indexOf":
		return str(key, func(a []Value) Value {
			i := strings.Index(s, toString(arg(a, 0)))
			if i < 0 {
				return -1.0
			}
			return float64(len([]rune(s[:i])))
		})
	case "split":
		return str(key, func(a []Value) Value {
			out := &Array{}
			sep := arg(a, 0)
			if sep == Undefined {
				out.Elems = []Value{s}
				return out
			}
			parts := strings.Split(s, toString(sep))
			if toString(sep) == "" {
				parts = nil
				for _, r := range s {
					parts = append(parts, string(r))
				}
			}
			for _, p := range parts {
				out.Elems = append(out.Elems, p)
			}
			return out
		})
	case "replace", "replaceAll":
		return str(key, func(a []Value) Value {
			n := 1
			if key == "replaceAll" {
				n = -1
			}
			return strings.Replace(s, toString(arg(a, 0)), toString(arg(a, 1)), n)
		})
	case "slice", "substring":
		return str(key, func(a []Value) Value {
			rs := []rune(s)
			n := len(rs)
			st, en := relIndex(arg(a, 0), n, 0), relIndex(arg(a, 1), n, n)
			if key == "substring" {
				st = int(math.Max(0, math.Min(float64(n), toNumber(arg(a, 0)))))
				if arg(a, 0) == Undefined || math.IsNaN(toNumber(arg(a, 0))) {
					st = 0
				}
				if arg(a, 1) != Undefined {
					en = int(math.Max(0, math.Min(float64(n), toNumber(arg(a, 1)))))
				}
				if st > en {
					st, en = en, st
				}
			}
			if en < st {
				return ""
			}
			return string(rs[st:en])
		})
	case "padStart", "padEnd":
		return str(key, func(a []Value) Value {
			w := int(toNumber(arg(a, 0)))
			pad := " "
			if p := arg(a, 1); p != Undefined {
				pad = toString(p)
			}
			n := len([]rune(s))
			if w <= n || pad == "" {
				return s
			}
			fill := strings.Repeat(pad, (w-n)/len([]rune(pad))+1)
			fill = string([]rune(fill)[:w-n])
			if key == "padStart" {
				return fill + s
			}
			return s + fill
		})
	case "repeat":
		return bi(key, func(_ *Runner, _ Value, a []Value) (Value, error) {
			n := int(toNumber(arg(a, 0)))
			if n < 0 || n*len(s) > 1<<24 {
				return nil, &Throw{Value: makeError("RangeError", "invalid repeat count")}
			}
			return strings.Repeat(s, n), nil
		})
	case "charAt":
		return str(key, func(a []Value) Value {
			rs := []rune(s)
			i := int(toNumber(arg(a, 0)))
			if i < 0 || i >= len(rs) {
				return ""
			}
			return string(rs[i])
		})
	case "at":
		return str(key, func(a []Value) Value {
			rs := []rune(s)
			i := int(toNumber(arg(a, 0)))
			if i < 0 {
				i += len(rs)
			}
			if i < 0 || i >= len(rs) {
				return Undefined
			}
			return string(rs[i])
		})
	case "toString":
		return str(key, func([]Value) Value { return s })
	}
	return nil
}

// silence unused import when building without context-using helpers
var _ = context.Background
