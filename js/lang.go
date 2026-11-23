package js

// Classes, `new`, instanceof, Map, Set, Date, RegExp and tagged templates.

import (
	"fmt"
	"math"
	"math/rand/v2"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf16"
	"unicode/utf8"
)

// ---------------- classes ----------------

// Class is a class value. typeof is "function"; it can only be called with
// new.
type Class struct {
	Name        string
	Ctor        *Function // nil: the default constructor
	Proto       *Object   // instance methods
	Statics     *Object   // static methods and fields
	Super       *Class
	SuperNative *Builtin // extends a built-in constructor such as Error
	fields      []ClassMember
	env         *Env
	file        string
}

func (r *Runner) evalClass(c *ClassExpr, env *Env) (*Class, error) {
	cls := &Class{Name: c.Name, Proto: NewObject(), Statics: NewObject(), file: r.file}
	if c.Super != nil {
		sv, err := r.eval(c.Super, env)
		if err != nil {
			return nil, err
		}
		switch s := sv.(type) {
		case *Class:
			cls.Super = s
			cls.Proto.proto = s.Proto
		case *Builtin:
			if s.New == nil {
				return nil, r.throwAt(c, "TypeError", "class %s extends %s, which is not a constructor", c.Name, s.Name)
			}
			cls.SuperNative = s
		default:
			return nil, r.throwAt(c, "TypeError", "class %s extends a value that is not a class (%s)", c.Name, typeOf(sv))
		}
	}
	cenv := env
	if c.Name != "" {
		cenv = newEnv(env)
		cenv.declare(c.Name, cls, "const")
	}
	cls.env = cenv
	for _, m := range c.Members {
		switch m.Kind {
		case "constructor":
			cls.Ctor = &Function{Decl: m.Fn, Env: cenv, Name: c.Name, File: r.file, Home: cls}
		case "method":
			f := &Function{Decl: m.Fn, Env: cenv, Name: m.Name, File: r.file, Home: cls}
			if m.Static {
				cls.Statics.Set(m.Name, f)
			} else {
				cls.Proto.Set(m.Name, f)
			}
		case "field":
			if !m.Static {
				cls.fields = append(cls.fields, m)
				continue
			}
			v, err := r.fieldValue(cls, cls, m)
			if err != nil {
				return nil, err
			}
			cls.Statics.Set(m.Name, v)
		}
	}
	return cls, nil
}

func (r *Runner) fieldValue(cls *Class, this Value, m ClassMember) (Value, error) {
	if m.Value == nil {
		return Undefined, nil
	}
	fenv := newEnv(cls.env)
	fenv.declare("this", this, "const")
	fenv.declare("%home", cls, "const")
	return r.eval(m.Value, fenv)
}

func (r *Runner) initFields(cls *Class, this *Object) error {
	for _, m := range cls.fields {
		v, err := r.fieldValue(cls, this, m)
		if err != nil {
			return err
		}
		this.Set(m.Name, v)
	}
	return nil
}

// initInstance runs cls's construction on this: fields, then the
// constructor; a derived class without a constructor passes its arguments
// to the parent.
func (r *Runner) initInstance(cls *Class, this *Object, args []Value, at Node) error {
	if cls.Ctor == nil {
		if cls.Super != nil || cls.SuperNative != nil {
			return r.superInit(cls, this, args, at)
		}
		return r.initFields(cls, this)
	}
	if cls.Super == nil && cls.SuperNative == nil {
		if err := r.initFields(cls, this); err != nil {
			return err
		}
	}
	_, err := r.CallWithThis(cls.Ctor, this, args)
	return err
}

// superInit is super(...args) inside cls: construct the parent part of this,
// then cls's own fields.
func (r *Runner) superInit(cls *Class, this *Object, args []Value, at Node) error {
	switch {
	case cls.Super != nil:
		if err := r.initInstance(cls.Super, this, args, at); err != nil {
			return err
		}
	case cls.SuperNative != nil:
		v, err := cls.SuperNative.New(r, args)
		if err != nil {
			return err
		}
		if o, ok := v.(*Object); ok {
			for _, k := range o.keys {
				this.Set(k, o.vals[k])
			}
		}
	}
	return r.initFields(cls, this)
}

// construct evaluates new callee(...args).
func (r *Runner) construct(callee Value, args []Value, at Node) (Value, error) {
	switch c := callee.(type) {
	case *Class:
		obj := NewObject()
		obj.proto, obj.class = c.Proto, c
		if err := r.initInstance(c, obj, args, at); err != nil {
			return nil, err
		}
		return obj, nil
	case *Builtin:
		if c.New != nil {
			return c.New(r, args)
		}
	case *Object:
		switch c.native {
		case "Promise":
			return r.newPromiseFrom(arg(args, 0), at)
		case "Object":
			return NewObject(), nil
		case "Array":
			if len(args) == 1 {
				if n, ok := args[0].(float64); ok {
					if n < 0 || n != math.Trunc(n) || n > float64(r.it.cfg.Limits.MaxArrayLen) {
						return nil, r.throwAt(at, "RangeError", "invalid array length")
					}
					a := &Array{Elems: make([]Value, int(n))}
					for i := range a.Elems {
						a.Elems[i] = Undefined
					}
					return a, nil
				}
			}
			return &Array{Elems: append([]Value{}, args...)}, nil
		}
	case *Function:
		if !c.Decl.Arrow && !c.Decl.Async && c.Home == nil {
			obj := NewObject()
			v, err := r.CallWithThis(c, obj, args)
			if err != nil {
				return nil, err
			}
			if o, ok := v.(*Object); ok {
				return o, nil
			}
			return obj, nil
		}
	}
	return nil, r.throwAt(at, "TypeError", "%s is not a constructor", Inspect(callee))
}

func (r *Runner) newPromiseFrom(exec Value, at Node) (Value, error) {
	switch exec.(type) {
	case *Function, *Builtin:
	default:
		return nil, r.throwAt(at, "TypeError", "Promise resolver %s is not a function", Inspect(exec))
	}
	p := r.it.newPromise()
	resolve := bi("resolve", func(r *Runner, _ Value, a []Value) (Value, error) {
		r.it.resolveWith(p, arg(a, 0))
		return Undefined, nil
	})
	reject := bi("reject", func(_ *Runner, _ Value, a []Value) (Value, error) {
		p.settle(2, arg(a, 0))
		return Undefined, nil
	})
	if _, err := r.Call(exec, resolve, reject); err != nil {
		t, ok := catchable(err)
		if !ok {
			return nil, err
		}
		p.settle(2, t)
	}
	return p, nil
}

func instanceOf(v, c Value) (bool, error) {
	switch k := c.(type) {
	case *Class:
		o, ok := v.(*Object)
		if !ok {
			return false, nil
		}
		for cl := o.class; cl != nil; cl = cl.Super {
			if cl == k {
				return true, nil
			}
		}
		return false, nil
	case *Builtin:
		switch k.Name {
		case "Map":
			_, ok := v.(*MapV)
			return ok, nil
		case "Set":
			_, ok := v.(*SetV)
			return ok, nil
		case "Date":
			_, ok := v.(*DateV)
			return ok, nil
		case "RegExp":
			_, ok := v.(*RegExp)
			return ok, nil
		case "Function":
			return typeOf(v) == "function", nil
		}
		if k.New != nil && strings.HasSuffix(k.Name, "Error") {
			o, ok := v.(*Object)
			if !ok {
				return false, nil
			}
			for cl := o.class; cl != nil; cl = cl.Super {
				if cl.SuperNative == k {
					return true, nil
				}
			}
			if !isErrorObject(o) {
				return false, nil
			}
			n, _ := o.Get("name")
			return k.Name == "Error" || n == k.Name, nil
		}
		return false, nil
	case *Object:
		switch k.native {
		case "Array":
			_, ok := v.(*Array)
			return ok, nil
		case "Promise":
			_, ok := v.(*Promise)
			return ok, nil
		case "Object":
			switch v.(type) {
			case nil, undefinedT, bool, float64, string:
				return false, nil
			}
			return true, nil
		}
	}
	return false, fmt.Errorf("right-hand side of instanceof is not callable")
}

// ---------------- Map and Set ----------------

type nanKey struct{}

func mapKey(v Value) any {
	if f, ok := v.(float64); ok {
		if math.IsNaN(f) {
			return nanKey{}
		}
		if f == 0 {
			return float64(0)
		}
	}
	return v
}

// MapV is a JS Map: insertion-ordered, keys compared like ===
// (NaN equals NaN).
type MapV struct {
	keys, vals []Value
	idx        map[any]int
}

func newMap() *MapV { return &MapV{idx: map[any]int{}} }

func (m *MapV) get(k Value) (Value, bool) {
	if i, ok := m.idx[mapKey(k)]; ok {
		return m.vals[i], true
	}
	return Undefined, false
}

func (m *MapV) set(k, v Value) {
	mk := mapKey(k)
	if i, ok := m.idx[mk]; ok {
		m.vals[i] = v
		return
	}
	m.idx[mk] = len(m.keys)
	m.keys = append(m.keys, k)
	m.vals = append(m.vals, v)
}

func (m *MapV) del(k Value) bool {
	mk := mapKey(k)
	i, ok := m.idx[mk]
	if !ok {
		return false
	}
	m.keys = append(m.keys[:i], m.keys[i+1:]...)
	m.vals = append(m.vals[:i], m.vals[i+1:]...)
	delete(m.idx, mk)
	for j := i; j < len(m.keys); j++ {
		m.idx[mapKey(m.keys[j])] = j
	}
	return true
}

// SetV is a JS Set.
type SetV struct{ m *MapV }

func (r *Runner) mapMember(m *MapV, key string) Value {
	switch key {
	case "size":
		return float64(len(m.keys))
	case "get":
		return bi("get", func(_ *Runner, _ Value, a []Value) (Value, error) { v, _ := m.get(arg(a, 0)); return v, nil })
	case "has":
		return bi("has", func(_ *Runner, _ Value, a []Value) (Value, error) { _, ok := m.get(arg(a, 0)); return ok, nil })
	case "set":
		return bi("set", func(_ *Runner, _ Value, a []Value) (Value, error) { m.set(arg(a, 0), arg(a, 1)); return m, nil })
	case "delete":
		return bi("delete", func(_ *Runner, _ Value, a []Value) (Value, error) { return m.del(arg(a, 0)), nil })
	case "clear":
		return bi("clear", func(_ *Runner, _ Value, _ []Value) (Value, error) {
			*m = *newMap()
			return Undefined, nil
		})
	case "keys":
		return bi("keys", func(_ *Runner, _ Value, _ []Value) (Value, error) {
			return &Array{Elems: append([]Value{}, m.keys...)}, nil
		})
	case "values":
		return bi("values", func(_ *Runner, _ Value, _ []Value) (Value, error) {
			return &Array{Elems: append([]Value{}, m.vals...)}, nil
		})
	case "entries":
		return bi("entries", func(_ *Runner, _ Value, _ []Value) (Value, error) { return &Array{Elems: mapEntries(m)}, nil })
	case "forEach":
		return bi("forEach", func(r *Runner, _ Value, a []Value) (Value, error) {
			ks, vs := append([]Value{}, m.keys...), append([]Value{}, m.vals...)
			for i := range ks {
				if _, err := r.Call(arg(a, 0), vs[i], ks[i], m); err != nil {
					return nil, err
				}
			}
			return Undefined, nil
		})
	}
	return Undefined
}

func mapEntries(m *MapV) []Value {
	out := make([]Value, len(m.keys))
	for i := range m.keys {
		out[i] = &Array{Elems: []Value{m.keys[i], m.vals[i]}}
	}
	return out
}

func (r *Runner) setVMember(s *SetV, key string) Value {
	m := s.m
	switch key {
	case "size", "has", "delete", "clear", "keys", "values":
		if key == "keys" {
			key = "values"
		}
		if key == "values" {
			return bi("values", func(_ *Runner, _ Value, _ []Value) (Value, error) {
				return &Array{Elems: append([]Value{}, m.keys...)}, nil
			})
		}
		return r.mapMember(m, key)
	case "add":
		return bi("add", func(_ *Runner, _ Value, a []Value) (Value, error) { m.set(arg(a, 0), arg(a, 0)); return s, nil })
	case "entries":
		return bi("entries", func(_ *Runner, _ Value, _ []Value) (Value, error) {
			out := make([]Value, len(m.keys))
			for i, k := range m.keys {
				out[i] = &Array{Elems: []Value{k, k}}
			}
			return &Array{Elems: out}, nil
		})
	case "forEach":
		return bi("forEach", func(r *Runner, _ Value, a []Value) (Value, error) {
			for _, k := range append([]Value{}, m.keys...) {
				if _, err := r.Call(arg(a, 0), k, k, s); err != nil {
					return nil, err
				}
			}
			return Undefined, nil
		})
	}
	return Undefined
}

// ---------------- Date ----------------

// DateV is a JS Date: milliseconds since the Unix epoch (NaN if invalid).
type DateV struct{ ms float64 }

func (d *DateV) t() time.Time { return time.UnixMilli(int64(d.ms)) }

func (d *DateV) iso() (string, bool) {
	if math.IsNaN(d.ms) {
		return "", false
	}
	return d.t().UTC().Format("2006-01-02T15:04:05.000Z"), true
}

func nowMS() float64 { return float64(time.Now().UnixMilli()) }

func parseDate(s string) float64 {
	s = strings.TrimSpace(s)
	for _, layout := range []string{time.RFC3339Nano, "2006-01-02T15:04:05.000Z07:00", "2006-01-02T15:04:05Z07:00", "2006-01-02T15:04:05", "2006-01-02T15:04", "2006-01-02", "2006-01", "2006", time.RFC1123, time.RFC1123Z, "Mon Jan 02 2006 15:04:05 GMT-0700", "Jan 2, 2006", "January 2, 2006", "2006/01/02"} {
		loc := time.Local
		if layout == "2006-01-02" || layout == "2006-01" || layout == "2006" {
			loc = time.UTC // date-only forms are UTC in JS
		}
		if t, err := time.ParseInLocation(layout, s, loc); err == nil {
			return float64(t.UnixMilli())
		}
	}
	return math.NaN()
}

func dateFromParts(a []Value, loc *time.Location) float64 {
	p := [7]float64{0, 0, 1, 0, 0, 0, 0}
	for i := 0; i < len(a) && i < 7; i++ {
		p[i] = toNumber(a[i])
		if math.IsNaN(p[i]) {
			return math.NaN()
		}
	}
	if p[0] >= 0 && p[0] <= 99 {
		p[0] += 1900
	}
	t := time.Date(int(p[0]), time.Month(int(p[1])+1), int(p[2]), int(p[3]), int(p[4]), int(p[5]), int(p[6])*int(time.Millisecond), loc)
	return float64(t.UnixMilli())
}

func newDate(_ *Runner, a []Value) (Value, error) {
	switch {
	case len(a) == 0:
		return &DateV{nowMS()}, nil
	case len(a) == 1:
		switch x := a[0].(type) {
		case *DateV:
			return &DateV{x.ms}, nil
		case string:
			return &DateV{parseDate(x)}, nil
		}
		return &DateV{math.Trunc(toNumber(a[0]))}, nil
	}
	return &DateV{dateFromParts(a, time.Local)}, nil
}

func (r *Runner) dateMember(d *DateV, key string) Value {
	num := func(name string, f func(t time.Time) float64) Value {
		return bi(name, func(_ *Runner, _ Value, _ []Value) (Value, error) {
			if math.IsNaN(d.ms) {
				return math.NaN(), nil
			}
			return f(d.t()), nil
		})
	}
	local := func(t time.Time) time.Time { return t.Local() }
	utc := func(t time.Time) time.Time { return t.UTC() }
	for _, z := range []struct {
		suffix string
		f      func(time.Time) time.Time
	}{{"", local}, {"UTC", utc}} {
		f := z.f
		name := key
		if z.suffix != "" {
			if !strings.HasPrefix(key, "getUTC") {
				continue
			}
			name = "get" + strings.TrimPrefix(key, "getUTC")
		}
		switch name {
		case "getFullYear":
			return num(key, func(t time.Time) float64 { return float64(f(t).Year()) })
		case "getMonth":
			return num(key, func(t time.Time) float64 { return float64(f(t).Month() - 1) })
		case "getDate":
			return num(key, func(t time.Time) float64 { return float64(f(t).Day()) })
		case "getDay":
			return num(key, func(t time.Time) float64 { return float64(f(t).Weekday()) })
		case "getHours":
			return num(key, func(t time.Time) float64 { return float64(f(t).Hour()) })
		case "getMinutes":
			return num(key, func(t time.Time) float64 { return float64(f(t).Minute()) })
		case "getSeconds":
			return num(key, func(t time.Time) float64 { return float64(f(t).Second()) })
		case "getMilliseconds":
			return num(key, func(t time.Time) float64 { return float64(f(t).Nanosecond() / 1e6) })
		}
	}
	switch key {
	case "getTime", "valueOf":
		return bi(key, func(_ *Runner, _ Value, _ []Value) (Value, error) { return d.ms, nil })
	case "getTimezoneOffset":
		return num(key, func(t time.Time) float64 { _, off := t.Local().Zone(); return float64(-off / 60) })
	case "toISOString", "toJSON":
		return bi(key, func(r *Runner, _ Value, _ []Value) (Value, error) {
			s, ok := d.iso()
			if !ok {
				if key == "toJSON" {
					return nil, nil
				}
				return nil, &Throw{Value: makeError("RangeError", "Invalid time value")}
			}
			return s, nil
		})
	case "toString", "toDateString", "toTimeString", "toLocaleString", "toLocaleDateString", "toLocaleTimeString", "toUTCString":
		return bi(key, func(_ *Runner, _ Value, _ []Value) (Value, error) {
			if math.IsNaN(d.ms) {
				return "Invalid Date", nil
			}
			t := d.t().Local()
			switch key {
			case "toDateString":
				return t.Format("Mon Jan 02 2006"), nil
			case "toTimeString":
				return t.Format("15:04:05 GMT-0700"), nil
			case "toLocaleDateString":
				return t.Format("1/2/2006"), nil
			case "toLocaleTimeString":
				return t.Format("3:04:05 PM"), nil
			case "toLocaleString":
				return t.Format("1/2/2006, 3:04:05 PM"), nil
			case "toUTCString":
				return d.t().UTC().Format("Mon, 02 Jan 2006 15:04:05 GMT"), nil
			}
			return t.Format("Mon Jan 02 2006 15:04:05 GMT-0700"), nil
		})
	}
	return Undefined
}

// ---------------- RegExp (RE2) ----------------

// RegExp is a JS regular expression run by Go's RE2 engine: matching time
// is linear in the input, so there is no catastrophic backtracking.
// Lookaround and backreferences are not available (a check error).
type RegExp struct {
	Source, Flags string
	re            *regexp.Regexp
	LastIndex     int // UTF-16 units, used by g and y

	scanned  bool
	scanStr  string
	scanLocs [][]int
}

func (x *RegExp) global() bool { return strings.Contains(x.Flags, "g") }
func (x *RegExp) sticky() bool { return strings.Contains(x.Flags, "y") }

func compileRegex(src, flags string) (*RegExp, error) {
	for _, f := range flags {
		if !strings.ContainsRune("gimsuyd", f) || strings.Count(flags, string(f)) > 1 {
			return nil, fmt.Errorf("invalid regex flags %q", flags)
		}
	}
	prefix := ""
	for _, f := range "ims" {
		if strings.ContainsRune(flags, f) {
			prefix += string(f)
		}
	}
	pat, err := translateRegex(src)
	if err != nil {
		return nil, err
	}
	if prefix != "" {
		pat = "(?" + prefix + ")" + pat
	}
	re, err := regexp.Compile(pat)
	if err != nil {
		msg := err.Error()
		if strings.Contains(src, "(?=") || strings.Contains(src, "(?!") || strings.Contains(src, "(?<=") || strings.Contains(src, "(?<!") {
			msg = "lookahead and lookbehind are not supported (regexes run on RE2)"
		} else if strings.Contains(msg, "escape sequence") && regexp.MustCompile(`\\[1-9]`).MatchString(src) {
			msg = "backreferences are not supported (regexes run on RE2)"
		}
		return nil, fmt.Errorf("invalid regex /%s/: %s", src, msg)
	}
	return &RegExp{Source: src, Flags: flags, re: re}, nil
}

// translateRegex rewrites the JS escapes RE2 spells differently.
func translateRegex(src string) (string, error) {
	var b strings.Builder
	rs := []rune(src)
	for i := 0; i < len(rs); i++ {
		c := rs[i]
		if c != '\\' || i+1 >= len(rs) {
			b.WriteRune(c)
			continue
		}
		n := rs[i+1]
		switch {
		case n == 'u' && i+2 < len(rs) && rs[i+2] == '{':
			end := strings.IndexRune(string(rs[i+3:]), '}')
			if end < 0 {
				return "", fmt.Errorf("invalid regex /%s/: bad \\u{...} escape", src)
			}
			b.WriteString(`\x{` + string(rs[i+3:i+3+end]) + `}`)
			i += 3 + end
		case n == 'u' && i+5 < len(rs):
			b.WriteString(`\x{` + string(rs[i+2:i+6]) + `}`)
			i += 5
		case n == 'd' || n == 'D' || n == 'w' || n == 'W' || n == 's' || n == 'S' || n == 'b' || n == 'B':
			b.WriteRune(c)
			b.WriteRune(n)
			i++
		case n == '/':
			b.WriteRune('/')
			i++
		case n == '-':
			b.WriteString(`\-`)
			i++
		default:
			b.WriteRune(c)
			b.WriteRune(n)
			i++
		}
	}
	return b.String(), nil
}

func newRegExp(_ *Runner, a []Value) (Value, error) {
	src, flags := "", ""
	switch x := arg(a, 0).(type) {
	case *RegExp:
		src, flags = x.Source, x.Flags
	case undefinedT:
		src = "(?:)"
	default:
		src = toString(x)
	}
	if f := arg(a, 1); f != Undefined {
		flags = toString(f)
	}
	re, err := compileRegex(src, flags)
	if err != nil {
		return nil, &Throw{Value: makeError("SyntaxError", err.Error())}
	}
	return re, nil
}

// u16 converts a byte offset in s to a UTF-16 index.
func u16(s string, byteOff int) int {
	n := 0
	for _, r := range s[:byteOff] {
		n += utf16.RuneLen(r)
	}
	return n
}

// byteAt converts a UTF-16 index in s to a byte offset.
func byteAt(s string, unit int) int {
	n := 0
	for i, r := range s {
		if n >= unit {
			return i
		}
		n += utf16.RuneLen(r)
	}
	return len(s)
}

// execAt finds the next match starting at or after byte offset from (or
// exactly at from when sticky). It returns submatch byte indexes or nil.
// Matches come from one scan of the whole string, so ^, $ and \b see the
// real start and end of the input wherever a global search resumes.
func (x *RegExp) execAt(s string, from int, sticky bool) []int {
	if from > len(s) {
		return nil
	}
	for _, loc := range x.scan(s) {
		if loc[0] < from {
			continue
		}
		if sticky && loc[0] != from {
			return nil
		}
		return append([]int(nil), loc...)
	}
	return nil
}

// scan returns every non-overlapping match of s, cached for the last string
// so an exec loop does not rescan from the start each time.
func (x *RegExp) scan(s string) [][]int {
	if x.scanned && x.scanStr == s {
		return x.scanLocs
	}
	x.scanStr, x.scanLocs, x.scanned = s, x.re.FindAllStringSubmatchIndex(s, -1), true
	return x.scanLocs
}

// matchArray builds the exec() result: [match, ...groups] with index, input
// and groups.
func (x *RegExp) matchArray(s string, loc []int) *Array {
	a := &Array{}
	for i := 0; i < len(loc); i += 2 {
		if loc[i] < 0 {
			a.Elems = append(a.Elems, Undefined)
		} else {
			a.Elems = append(a.Elems, s[loc[i]:loc[i+1]])
		}
	}
	a.extra = map[string]Value{"index": float64(u16(s, loc[0])), "input": s, "groups": Undefined}
	if names := x.re.SubexpNames(); len(names) > 1 {
		var g *Object
		for i, n := range names {
			if n != "" {
				if g == nil {
					g = NewObject()
				}
				g.Set(n, a.Elems[i])
			}
		}
		if g != nil {
			a.extra["groups"] = g
		}
	}
	return a
}

func (x *RegExp) exec(s string) Value {
	from, sticky := 0, x.sticky()
	if x.global() || sticky {
		from = byteAt(s, x.LastIndex)
		if x.LastIndex > utf16Len(s) {
			x.LastIndex = 0
			return nil
		}
	}
	loc := x.execAt(s, from, sticky)
	if loc == nil {
		if x.global() || sticky {
			x.LastIndex = 0
		}
		return nil
	}
	if x.global() || sticky {
		end := loc[1]
		if end == loc[0] {
			end = nextRune(s, end)
		}
		x.LastIndex = u16(s, end)
	}
	return x.matchArray(s, loc)
}

func utf16Len(s string) int { return u16(s, len(s)) }

func nextRune(s string, i int) int {
	if i >= len(s) {
		return i + 1
	}
	_, n := utf8.DecodeRuneInString(s[i:])
	return i + n
}

// allMatches returns every non-overlapping match from the start.
func (x *RegExp) allMatches(s string) [][]int { return x.scan(s) }

func (r *Runner) regexMember(x *RegExp, key string) Value {
	switch key {
	case "source":
		return x.Source
	case "flags":
		return x.Flags
	case "global":
		return x.global()
	case "ignoreCase":
		return strings.Contains(x.Flags, "i")
	case "multiline":
		return strings.Contains(x.Flags, "m")
	case "sticky":
		return x.sticky()
	case "lastIndex":
		return float64(x.LastIndex)
	case "test":
		return bi("test", func(_ *Runner, _ Value, a []Value) (Value, error) { return x.exec(toString(arg(a, 0))) != nil, nil })
	case "exec":
		return bi("exec", func(_ *Runner, _ Value, a []Value) (Value, error) { return x.exec(toString(arg(a, 0))), nil })
	case "toString":
		return bi("toString", func(_ *Runner, _ Value, _ []Value) (Value, error) { return "/" + x.Source + "/" + x.Flags, nil })
	}
	return Undefined
}

// expandReplacement applies $&, $1..$99, $<name>, $$, $` and $'.
func expandReplacement(x *RegExp, tpl, s string, loc []int) string {
	if !strings.Contains(tpl, "$") {
		return tpl
	}
	var b strings.Builder
	for i := 0; i < len(tpl); i++ {
		c := tpl[i]
		if c != '$' || i+1 >= len(tpl) {
			b.WriteByte(c)
			continue
		}
		n := tpl[i+1]
		switch {
		case n == '$':
			b.WriteByte('$')
			i++
		case n == '&':
			b.WriteString(s[loc[0]:loc[1]])
			i++
		case n == '`':
			b.WriteString(s[:loc[0]])
			i++
		case n == '\'':
			b.WriteString(s[loc[1]:])
			i++
		case n >= '0' && n <= '9':
			j := i + 2
			if j < len(tpl) && tpl[j] >= '0' && tpl[j] <= '9' {
				if g, _ := strconv.Atoi(tpl[i+1 : j+1]); g > 0 && 2*g+1 < len(loc) {
					j++
				}
			}
			g, _ := strconv.Atoi(tpl[i+1 : j])
			if g == 0 || 2*g+1 >= len(loc) {
				b.WriteByte(c)
				continue
			}
			if loc[2*g] >= 0 {
				b.WriteString(s[loc[2*g]:loc[2*g+1]])
			}
			i = j - 1
		case n == '<':
			end := strings.IndexByte(tpl[i:], '>')
			if end < 0 {
				b.WriteByte(c)
				continue
			}
			name := tpl[i+2 : i+end]
			if gi := x.re.SubexpIndex(name); gi > 0 && loc[2*gi] >= 0 {
				b.WriteString(s[loc[2*gi]:loc[2*gi+1]])
			}
			i += end
		default:
			b.WriteByte(c)
		}
	}
	return b.String()
}

func (r *Runner) regexReplace(x *RegExp, s string, repl Value, all bool) (Value, error) {
	var locs [][]int
	if all {
		locs = x.allMatches(s)
	} else if loc := x.execAt(s, 0, false); loc != nil {
		locs = [][]int{loc}
	}
	var b strings.Builder
	last := 0
	for _, loc := range locs {
		b.WriteString(s[last:loc[0]])
		switch fn := repl.(type) {
		case *Function, *Builtin:
			m := x.matchArray(s, loc)
			args := append([]Value{}, m.Elems...)
			args = append(args, m.extra["index"], s)
			if g := m.extra["groups"]; g != Undefined {
				args = append(args, g)
			}
			v, err := r.Call(fn, args...)
			if err != nil {
				return nil, err
			}
			b.WriteString(toString(v))
		default:
			b.WriteString(expandReplacement(x, toString(repl), s, loc))
		}
		last = loc[1]
	}
	b.WriteString(s[last:])
	if x.global() {
		x.LastIndex = 0
	}
	return b.String(), nil
}

// regexStringMethod handles the string methods whose first argument is a
// RegExp (match, matchAll, replace, replaceAll, split, search). ok is false
// when the argument is not a regex and the plain string method applies.
func (r *Runner) regexStringMethod(s, key string) *Builtin {
	switch key {
	case "match", "matchAll", "search", "replace", "replaceAll", "split":
	default:
		return nil
	}
	plain := stringMethod(s, key)
	return bi(key, func(r *Runner, this Value, a []Value) (Value, error) {
		x, isRe := arg(a, 0).(*RegExp)
		if !isRe {
			switch key {
			case "match", "matchAll", "search":
				src := ""
				if v := arg(a, 0); v != Undefined {
					src = regexp.QuoteMeta(toString(v))
				}
				flags := ""
				if key == "matchAll" {
					flags = "g"
				}
				var err error
				if x, err = compileRegex(src, flags); err != nil {
					return nil, &Throw{Value: makeError("SyntaxError", err.Error())}
				}
			default:
				if plain == nil {
					return Undefined, nil
				}
				return plain.Fn(r, this, a)
			}
		}
		switch key {
		case "match":
			if !x.global() {
				loc := x.execAt(s, 0, false)
				if loc == nil {
					return nil, nil
				}
				return x.matchArray(s, loc), nil
			}
			locs := x.allMatches(s)
			if len(locs) == 0 {
				return nil, nil
			}
			out := &Array{}
			for _, l := range locs {
				out.Elems = append(out.Elems, s[l[0]:l[1]])
			}
			x.LastIndex = 0
			return out, nil
		case "matchAll":
			if !x.global() {
				return nil, &Throw{Value: makeError("TypeError", "matchAll needs a regex with the g flag")}
			}
			out := &Array{}
			for _, l := range x.allMatches(s) {
				out.Elems = append(out.Elems, x.matchArray(s, l))
			}
			return out, nil
		case "search":
			loc := x.execAt(s, 0, false)
			if loc == nil {
				return float64(-1), nil
			}
			return float64(u16(s, loc[0])), nil
		case "replace":
			return r.regexReplace(x, s, arg(a, 1), x.global())
		case "replaceAll":
			if !x.global() {
				return nil, &Throw{Value: makeError("TypeError", "replaceAll needs a regex with the g flag")}
			}
			return r.regexReplace(x, s, arg(a, 1), true)
		case "split":
			limit := -1
			if l := arg(a, 1); l != Undefined {
				limit = int(toNumber(l))
			}
			out := &Array{}
			add := func(v Value) bool {
				if limit >= 0 && len(out.Elems) >= limit {
					return false
				}
				out.Elems = append(out.Elems, v)
				return true
			}
			if s == "" {
				if x.execAt(s, 0, false) == nil {
					add("")
				}
				return out, nil
			}
			last := 0
			for _, loc := range x.allMatches(s) {
				if loc[1] == loc[0] && (loc[0] == 0 || loc[0] >= len(s)) {
					continue
				}
				if !add(s[last:loc[0]]) {
					return out, nil
				}
				for g := 2; g < len(loc); g += 2 {
					var v Value = Undefined
					if loc[g] >= 0 {
						v = s[loc[g]:loc[g+1]]
					}
					if !add(v) {
						return out, nil
					}
				}
				last = loc[1]
			}
			add(s[last:])
			return out, nil
		}
		return Undefined, nil
	})
}

// ---------------- tagged templates ----------------

func (r *Runner) evalTagged(n *TaggedTemplate, env *Env) (Value, error) {
	var fn, this Value = nil, Undefined
	if m, ok := n.Tag.(*Member); ok && m.Computed == nil {
		obj, err := r.eval(m.Obj, env)
		if err != nil {
			return nil, err
		}
		if fn, err = r.getMember(obj, m.Name, m); err != nil {
			return nil, err
		}
		this = obj
	} else {
		var err error
		if fn, err = r.eval(n.Tag, env); err != nil {
			return nil, err
		}
	}
	switch fn.(type) {
	case *Function, *Builtin:
	default:
		return nil, r.throwAt(n, "TypeError", "%s is not a function (used as a template tag)", describeCallee(n.Tag))
	}
	strs := &Array{}
	for _, q := range n.Tpl.Quasis {
		strs.Elems = append(strs.Elems, q)
	}
	raw := &Array{}
	for i, q := range n.Tpl.Quasis {
		if i < len(n.Tpl.Raws) {
			raw.Elems = append(raw.Elems, n.Tpl.Raws[i])
		} else {
			raw.Elems = append(raw.Elems, q)
		}
	}
	strs.extra = map[string]Value{"raw": raw}
	args := []Value{strs}
	for _, x := range n.Tpl.Exprs {
		v, err := r.eval(x, env)
		if err != nil {
			return nil, err
		}
		args = append(args, v)
	}
	res, err := r.CallWithThis(fn, this, args)
	if err != nil {
		return nil, r.positioned(n, err)
	}
	return res, nil
}

// ---------------- switch ----------------

func (r *Runner) execSwitch(n *Switch, env *Env) (ctrl, string, Value, error) {
	d, err := r.eval(n.Disc, env)
	if err != nil {
		return cNormal, "", nil, err
	}
	senv := newEnv(env)
	start := -1
	for i, c := range n.Cases {
		if c.Test == nil {
			continue
		}
		v, err := r.eval(c.Test, senv)
		if err != nil {
			return cNormal, "", nil, err
		}
		if strictEquals(d, v) {
			start = i
			break
		}
	}
	if start < 0 {
		for i, c := range n.Cases {
			if c.Test == nil {
				start = i
			}
		}
	}
	if start < 0 {
		return cNormal, "", nil, nil
	}
	var body []Node
	for _, c := range n.Cases[start:] {
		body = append(body, c.Body...)
	}
	c, l, v, err := r.execBlock(body, senv)
	if err != nil {
		return cNormal, "", nil, err
	}
	if c == cBreak && l == "" {
		return cNormal, "", nil, nil
	}
	return c, l, v, nil
}

// ---------------- super ----------------

func isSuper(n Node) bool { _, ok := n.(*Super); return ok }

func (r *Runner) homeAndThis(at Node, env *Env) (*Class, Value, error) {
	hb, ok := env.lookup("%home")
	tb, ok2 := env.lookup("this")
	if !ok || !ok2 {
		return nil, nil, r.throwAt(at, "SyntaxError", "`super` is only valid inside class methods")
	}
	return hb.v.(*Class), tb.v, nil
}

func (r *Runner) superCall(n *Call, env *Env) (Value, error) {
	home, this, err := r.homeAndThis(n, env)
	if err != nil {
		return nil, err
	}
	obj, ok := this.(*Object)
	if !ok || (home.Super == nil && home.SuperNative == nil) {
		return nil, r.throwAt(n, "SyntaxError", "super() is only valid in the constructor of a class that extends another")
	}
	args, err := r.evalArgs(n.Args, env)
	if err != nil {
		return nil, err
	}
	if err := r.superInit(home, obj, args, n); err != nil {
		return nil, r.positioned(n, err)
	}
	return Undefined, nil
}

// superMember resolves super.key: the parent's method (or static, inside a
// static method), with this unchanged.
func (r *Runner) superMember(m *Member, env *Env) (Value, Value, error) {
	home, this, err := r.homeAndThis(m, env)
	if err != nil {
		return nil, nil, err
	}
	key, err := r.memberKey(m, env)
	if err != nil {
		return nil, nil, err
	}
	if home.Super == nil {
		return Undefined, this, nil
	}
	if _, static := this.(*Class); static {
		v, err := r.getMember(home.Super, key, m)
		return v, this, err
	}
	for p := home.Super.Proto; p != nil; p = p.proto {
		if v, ok := p.Get(key); ok {
			return v, this, nil
		}
	}
	return Undefined, this, nil
}

func (r *Runner) evalArgs(nodes []Node, env *Env) ([]Value, error) {
	args := make([]Value, 0, len(nodes))
	for _, a := range nodes {
		if sp, ok := a.(*Spread); ok {
			v, err := r.eval(sp.X, env)
			if err != nil {
				return nil, err
			}
			items, err := r.iterate(v, sp)
			if err != nil {
				return nil, err
			}
			args = append(args, items...)
			continue
		}
		v, err := r.eval(a, env)
		if err != nil {
			return nil, err
		}
		args = append(args, v)
	}
	return args, nil
}

// ---------------- globals ----------------

func installLang(g *Env) {
	g.declare("this", Undefined, "const")
	for _, n := range []string{"Array", "Object", "Promise"} {
		if b, ok := g.vars[n]; ok {
			if o, ok := b.v.(*Object); ok {
				o.native = n
			}
		}
	}
	for _, n := range []string{"Error", "TypeError", "RangeError"} {
		if b, ok := g.vars[n]; ok {
			if f, ok := b.v.(*Builtin); ok {
				fn := f.Fn
				f.New = func(r *Runner, a []Value) (Value, error) { return fn(r, Undefined, a) }
			}
		}
	}
	for _, n := range []string{"SyntaxError", "ReferenceError", "EvalError", "URIError"} {
		e := errorCtor(n)
		fn := e.Fn
		e.New = func(r *Runner, a []Value) (Value, error) { return fn(r, Undefined, a) }
		g.declare(n, e, "const")
	}

	mapB := bi("Map", func(*Runner, Value, []Value) (Value, error) {
		return nil, &Throw{Value: makeError("TypeError", "Map must be called with new")}
	})
	mapB.New = func(r *Runner, a []Value) (Value, error) {
		m := newMap()
		if src := arg(a, 0); src != Undefined && src != nil {
			items, err := r.iterate(src, nil)
			if err != nil {
				return nil, err
			}
			for _, it := range items {
				pair, ok := it.(*Array)
				if !ok {
					return nil, &Throw{Value: makeError("TypeError", "new Map() needs [key, value] pairs")}
				}
				m.set(arg(pair.Elems, 0), arg(pair.Elems, 1))
			}
		}
		return m, nil
	}
	g.declare("Map", mapB, "const")

	setB := bi("Set", func(*Runner, Value, []Value) (Value, error) {
		return nil, &Throw{Value: makeError("TypeError", "Set must be called with new")}
	})
	setB.New = func(r *Runner, a []Value) (Value, error) {
		s := &SetV{m: newMap()}
		if src := arg(a, 0); src != Undefined && src != nil {
			items, err := r.iterate(src, nil)
			if err != nil {
				return nil, err
			}
			for _, it := range items {
				s.m.set(it, it)
			}
		}
		return s, nil
	}
	g.declare("Set", setB, "const")

	dateB := bi("Date", func(*Runner, Value, []Value) (Value, error) {
		return (&DateV{nowMS()}).t().Local().Format("Mon Jan 02 2006 15:04:05 GMT-0700"), nil
	})
	dateB.New = newDate
	dateB.Statics = frozen(
		"now", bi("Date.now", func(*Runner, Value, []Value) (Value, error) { return nowMS(), nil }),
		"parse", bi("Date.parse", func(_ *Runner, _ Value, a []Value) (Value, error) { return parseDate(toString(arg(a, 0))), nil }),
		"UTC", bi("Date.UTC", func(_ *Runner, _ Value, a []Value) (Value, error) { return dateFromParts(a, time.UTC), nil }),
	)
	g.declare("Date", dateB, "const")

	reB := bi("RegExp", func(r *Runner, _ Value, a []Value) (Value, error) { return newRegExp(r, a) })
	reB.New = newRegExp
	g.declare("RegExp", reB, "const")

	if b, ok := g.vars["Number"]; ok {
		if f, ok := b.v.(*Builtin); ok {
			isInt := func(v Value) (float64, bool) {
				x, ok := v.(float64)
				return x, ok && !math.IsInf(x, 0) && !math.IsNaN(x) && x == math.Trunc(x)
			}
			f.Statics = frozen(
				"isInteger", bi("Number.isInteger", func(_ *Runner, _ Value, a []Value) (Value, error) { _, ok := isInt(arg(a, 0)); return ok, nil }),
				"isSafeInteger", bi("Number.isSafeInteger", func(_ *Runner, _ Value, a []Value) (Value, error) {
					x, ok := isInt(arg(a, 0))
					return ok && math.Abs(x) <= 1<<53-1, nil
				}),
				"isFinite", bi("Number.isFinite", func(_ *Runner, _ Value, a []Value) (Value, error) {
					x, ok := arg(a, 0).(float64)
					return ok && !math.IsInf(x, 0) && !math.IsNaN(x), nil
				}),
				"isNaN", bi("Number.isNaN", func(_ *Runner, _ Value, a []Value) (Value, error) {
					x, ok := arg(a, 0).(float64)
					return ok && math.IsNaN(x), nil
				}),
				"parseFloat", g.vars["parseFloat"].v,
				"parseInt", g.vars["parseInt"].v,
				"MAX_SAFE_INTEGER", float64(1<<53-1),
				"MIN_SAFE_INTEGER", -float64(1<<53-1),
				"EPSILON", math.Nextafter(1, 2)-1,
				"MAX_VALUE", math.MaxFloat64,
				"MIN_VALUE", 5e-324,
				"POSITIVE_INFINITY", math.Inf(1),
				"NEGATIVE_INFINITY", math.Inf(-1),
				"NaN", math.NaN(),
			)
		}
	}
	if b, ok := g.vars["String"]; ok {
		if f, ok := b.v.(*Builtin); ok {
			f.Statics = frozen(
				"fromCharCode", bi("String.fromCharCode", func(_ *Runner, _ Value, a []Value) (Value, error) {
					u := make([]uint16, len(a))
					for i, v := range a {
						u[i] = uint16(toNumber(v))
					}
					return string(utf16.Decode(u)), nil
				}),
				"fromCodePoint", bi("String.fromCodePoint", func(_ *Runner, _ Value, a []Value) (Value, error) {
					var b strings.Builder
					for _, v := range a {
						b.WriteRune(rune(toNumber(v)))
					}
					return b.String(), nil
				}),
				"raw", bi("String.raw", func(_ *Runner, _ Value, a []Value) (Value, error) {
					strs, ok := arg(a, 0).(*Array)
					if !ok {
						return nil, &Throw{Value: makeError("TypeError", "String.raw is a template tag")}
					}
					if raw, ok := strs.extra["raw"].(*Array); ok {
						strs = raw
					}
					var b strings.Builder
					for i, s := range strs.Elems {
						b.WriteString(toString(s))
						if i+1 < len(strs.Elems) && i+1 < len(a) {
							b.WriteString(toString(a[i+1]))
						}
					}
					return b.String(), nil
				}),
			)
		}
	}
	if b, ok := g.vars["Math"]; ok {
		if m, ok := b.v.(*Object); ok {
			f1 := func(name string, f func(float64) float64) {
				if _, ok := m.Get(name); !ok {
					m.Set(name, bi("Math."+name, func(_ *Runner, _ Value, a []Value) (Value, error) { return f(toNumber(arg(a, 0))), nil }))
				}
			}
			f1("cbrt", math.Cbrt)
			f1("exp", math.Exp)
			f1("expm1", math.Expm1)
			f1("log2", math.Log2)
			f1("log10", math.Log10)
			f1("log1p", math.Log1p)
			f1("sin", math.Sin)
			f1("cos", math.Cos)
			f1("tan", math.Tan)
			f1("asin", math.Asin)
			f1("acos", math.Acos)
			f1("atan", math.Atan)
			f1("sinh", math.Sinh)
			f1("cosh", math.Cosh)
			f1("tanh", math.Tanh)
			f1("fround", func(x float64) float64 { return float64(float32(x)) })
			if _, ok := m.Get("atan2"); !ok {
				m.Set("atan2", bi("Math.atan2", func(_ *Runner, _ Value, a []Value) (Value, error) {
					return math.Atan2(toNumber(arg(a, 0)), toNumber(arg(a, 1))), nil
				}))
			}
			if _, ok := m.Get("hypot"); !ok {
				m.Set("hypot", bi("Math.hypot", func(_ *Runner, _ Value, a []Value) (Value, error) {
					s := 0.0
					for _, v := range a {
						x := toNumber(v)
						s += x * x
					}
					return math.Sqrt(s), nil
				}))
			}
			if _, ok := m.Get("random"); !ok {
				m.Set("random", bi("Math.random", func(*Runner, Value, []Value) (Value, error) { return rand.Float64(), nil }))
			}
			for k, v := range map[string]float64{"LN2": math.Ln2, "LN10": math.Ln10, "LOG2E": math.Log2E, "LOG10E": math.Log10E, "SQRT2": math.Sqrt2, "SQRT1_2": math.Sqrt2 / 2} {
				if _, ok := m.Get(k); !ok {
					m.Set(k, v)
				}
			}
		}
	}
	if b, ok := g.vars["Array"]; ok {
		if o, ok := b.v.(*Object); ok {
			if _, ok := o.Get("of"); !ok {
				o.Set("of", bi("Array.of", func(_ *Runner, _ Value, a []Value) (Value, error) { return &Array{Elems: append([]Value{}, a...)}, nil }))
			}
		}
	}
	if b, ok := g.vars["Object"]; ok {
		if o, ok := b.v.(*Object); ok {
			if _, ok := o.Get("freeze"); !ok {
				o.Set("freeze", bi("Object.freeze", func(_ *Runner, _ Value, a []Value) (Value, error) {
					if x, ok := arg(a, 0).(*Object); ok {
						x.Frozen = true
					}
					return arg(a, 0), nil
				}))
			}
		}
	}
}

// ---------------- more array and string methods ----------------

func arrayMethod2(a *Array, key string) *Builtin {
	switch key {
	case "findLast", "findLastIndex":
		return bi(key, func(r *Runner, _ Value, args []Value) (Value, error) {
			for i := len(a.Elems) - 1; i >= 0; i-- {
				v := a.Elems[i]
				res, err := r.Call(arg(args, 0), v, float64(i), a)
				if err != nil {
					return nil, err
				}
				if truthy(res) {
					if key == "findLast" {
						return v, nil
					}
					return float64(i), nil
				}
			}
			if key == "findLast" {
				return Undefined, nil
			}
			return float64(-1), nil
		})
	case "fill":
		return bi(key, func(_ *Runner, _ Value, args []Value) (Value, error) {
			n := len(a.Elems)
			s, e := relIndex(arg(args, 1), n, 0), relIndex(arg(args, 2), n, n)
			for i := s; i < e; i++ {
				a.Elems[i] = arg(args, 0)
			}
			return a, nil
		})
	case "toReversed":
		return bi(key, func(_ *Runner, _ Value, _ []Value) (Value, error) {
			out := make([]Value, len(a.Elems))
			for i, v := range a.Elems {
				out[len(out)-1-i] = v
			}
			return &Array{Elems: out}, nil
		})
	case "toSorted":
		return bi(key, func(r *Runner, _ Value, args []Value) (Value, error) {
			cp := &Array{Elems: append([]Value{}, a.Elems...)}
			return r.CallWithThis(arrayMethod(cp, "sort"), cp, args)
		})
	case "with":
		return bi(key, func(_ *Runner, _ Value, args []Value) (Value, error) {
			i := int(toNumber(arg(args, 0)))
			if i < 0 {
				i += len(a.Elems)
			}
			if i < 0 || i >= len(a.Elems) {
				return nil, &Throw{Value: makeError("RangeError", "invalid index")}
			}
			out := append([]Value{}, a.Elems...)
			out[i] = arg(args, 1)
			return &Array{Elems: out}, nil
		})
	case "keys", "values", "entries":
		return bi(key, func(_ *Runner, _ Value, _ []Value) (Value, error) {
			out := make([]Value, len(a.Elems))
			for i, v := range a.Elems {
				switch key {
				case "keys":
					out[i] = float64(i)
				case "values":
					out[i] = v
				default:
					out[i] = &Array{Elems: []Value{float64(i), v}}
				}
			}
			return &Array{Elems: out}, nil
		})
	}
	return nil
}

func stringMethod2(s, key string) *Builtin {
	switch key {
	case "localeCompare":
		return bi(key, func(_ *Runner, _ Value, a []Value) (Value, error) {
			return float64(strings.Compare(s, toString(arg(a, 0)))), nil
		})
	case "lastIndexOf":
		return bi(key, func(_ *Runner, _ Value, a []Value) (Value, error) {
			i := strings.LastIndex(s, toString(arg(a, 0)))
			if i < 0 {
				return float64(-1), nil
			}
			return float64(u16(s, i)), nil
		})
	case "concat":
		return bi(key, func(_ *Runner, _ Value, a []Value) (Value, error) {
			var b strings.Builder
			b.WriteString(s)
			for _, v := range a {
				b.WriteString(toString(v))
			}
			return b.String(), nil
		})
	case "normalize", "toLocaleLowerCase", "toLocaleUpperCase":
		return bi(key, func(_ *Runner, _ Value, _ []Value) (Value, error) {
			switch key {
			case "toLocaleLowerCase":
				return strings.ToLower(s), nil
			case "toLocaleUpperCase":
				return strings.ToUpper(s), nil
			}
			return s, nil
		})
	}
	return nil
}
