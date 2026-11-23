package js

import (
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"sync"
)

// Value is a runtime value:
//
//	Undefined, nil (null), bool, float64, string, *Array, *Object,
//	*Function, *Builtin, *Promise
type Value = any

// Undefined is the JavaScript undefined value.
type undefinedT struct{}

// Undefined is the single undefined value.
var Undefined Value = undefinedT{}

// Array is a JS array.
type Array struct{ Elems []Value }

// Object is a JS plain object with insertion-ordered keys.
type Object struct {
	keys []string
	vals map[string]Value
	// Frozen objects reject writes (used for host-provided namespaces).
	Frozen bool
}

// NewObject returns an empty object.
func NewObject() *Object { return &Object{vals: map[string]Value{}} }

func (o *Object) Get(k string) (Value, bool) { v, ok := o.vals[k]; return v, ok }
func (o *Object) Set(k string, v Value) {
	if _, ok := o.vals[k]; !ok {
		o.keys = append(o.keys, k)
	}
	o.vals[k] = v
}
func (o *Object) Delete(k string) {
	if _, ok := o.vals[k]; !ok {
		return
	}
	delete(o.vals, k)
	for i, kk := range o.keys {
		if kk == k {
			o.keys = append(o.keys[:i], o.keys[i+1:]...)
			break
		}
	}
}

// Keys returns own enumerable keys in JS order: integer-like keys ascending,
// then string keys in insertion order.
func (o *Object) Keys() []string {
	var ints []string
	var strs []string
	for _, k := range o.keys {
		if isArrayIndex(k) {
			ints = append(ints, k)
		} else {
			strs = append(strs, k)
		}
	}
	sort.Slice(ints, func(i, j int) bool {
		a, _ := strconv.ParseUint(ints[i], 10, 32)
		b, _ := strconv.ParseUint(ints[j], 10, 32)
		return a < b
	})
	return append(ints, strs...)
}

func isArrayIndex(k string) bool {
	if k == "" || (len(k) > 1 && k[0] == '0') {
		return false
	}
	_, err := strconv.ParseUint(k, 10, 32)
	return err == nil
}

// Function is a closure defined in the program.
type Function struct {
	Decl *Func
	Env  *Env
	Name string
	// Module holds the file for stack-free diagnostics.
	File string
	// ExportModule and ExportName record where the function was first
	// exported from (set by `export`); declarative workflow plans use them
	// to turn a step function into a static ref "module#export".
	ExportModule string
	ExportName   string
}

// Builtin is a host function.
type Builtin struct {
	Name string
	Fn   func(r *Runner, this Value, args []Value) (Value, error)
}

// Promise is a JS promise settled from any goroutine.
type Promise struct {
	mu      sync.Mutex
	done    chan struct{}
	state   int // 0 pending 1 fulfilled 2 rejected
	value   Value
	handled bool
	// reactions run, in registration order, when the promise settles (see
	// onSettle). They must be cheap and never block.
	reactions []func()
}

func newPromise() *Promise { return &Promise{done: make(chan struct{})} }

func (p *Promise) settle(state int, v Value) {
	p.mu.Lock()
	if p.state != 0 {
		p.mu.Unlock()
		return
	}
	p.state, p.value = state, v
	close(p.done)
	rs := p.reactions
	p.reactions = nil
	p.mu.Unlock()
	for _, f := range rs {
		f()
	}
}

// onSettle registers f to run when p settles, after every reaction
// registered earlier; if p is already settled f runs now. Reactions are how
// await continuations and .then jobs join the interpreter's single FIFO job
// queue (see fifoLock) in the order JavaScript specifies.
func (p *Promise) onSettle(f func()) {
	p.mu.Lock()
	if p.state != 0 {
		p.mu.Unlock()
		f()
		return
	}
	p.reactions = append(p.reactions, f)
	p.mu.Unlock()
}

// ---- conversions ----

func typeOf(v Value) string {
	switch v.(type) {
	case undefinedT:
		return "undefined"
	case nil:
		return "object"
	case bool:
		return "boolean"
	case float64:
		return "number"
	case string:
		return "string"
	case *Function, *Builtin:
		return "function"
	}
	return "object"
}

func truthy(v Value) bool {
	switch x := v.(type) {
	case undefinedT, nil:
		return false
	case bool:
		return x
	case float64:
		return x != 0 && !math.IsNaN(x)
	case string:
		return x != ""
	}
	return true
}

func numberToString(f float64) string {
	switch {
	case math.IsNaN(f):
		return "NaN"
	case math.IsInf(f, 1):
		return "Infinity"
	case math.IsInf(f, -1):
		return "-Infinity"
	case f == 0:
		return "0"
	case f == math.Trunc(f) && math.Abs(f) < 1e21:
		return strconv.FormatFloat(f, 'f', -1, 64)
	}
	// Shortest round-trip digits, laid out as ECMAScript Number::toString:
	// positional for 1e-6 <= |f| < 1e21, exponent form otherwise.
	e := strconv.FormatFloat(f, 'e', -1, 64) // d.ddddde[+-]XX
	neg := ""
	if e[0] == '-' {
		neg, e = "-", e[1:]
	}
	mant, expS, _ := strings.Cut(e, "e")
	exp, _ := strconv.Atoi(expS)
	digits := strings.Replace(mant, ".", "", 1)
	k := len(digits)
	n := exp + 1 // decimal point position relative to digits
	switch {
	case k <= n && n <= 21:
		return neg + digits + strings.Repeat("0", n-k)
	case 0 < n && n <= 21:
		return neg + digits[:n] + "." + digits[n:]
	case -6 < n && n <= 0:
		return neg + "0." + strings.Repeat("0", -n) + digits
	}
	sign := "+"
	ae := n - 1
	if ae < 0 {
		sign, ae = "-", -ae
	}
	if k == 1 {
		return neg + digits + "e" + sign + strconv.Itoa(ae)
	}
	return neg + digits[:1] + "." + digits[1:] + "e" + sign + strconv.Itoa(ae)
}

func toString(v Value) string {
	switch x := v.(type) {
	case undefinedT:
		return "undefined"
	case nil:
		return "null"
	case bool:
		if x {
			return "true"
		}
		return "false"
	case float64:
		return numberToString(x)
	case string:
		return x
	case *Array:
		parts := make([]string, len(x.Elems))
		for i, e := range x.Elems {
			if e == nil || e == Undefined {
				continue
			}
			parts[i] = toString(e)
		}
		return strings.Join(parts, ",")
	case *Object:
		if isErrorObject(x) {
			n, _ := x.Get("name")
			m, _ := x.Get("message")
			return toString(n) + ": " + toString(m)
		}
		return "[object Object]"
	case *Function:
		return "function " + x.Name + "() { [code] }"
	case *Builtin:
		return "function " + x.Name + "() { [native code] }"
	case *Promise:
		return "[object Promise]"
	case *Stream:
		return "[object Stream]"
	}
	return fmt.Sprint(v)
}

func toNumber(v Value) float64 {
	switch x := v.(type) {
	case undefinedT:
		return math.NaN()
	case nil:
		return 0
	case bool:
		if x {
			return 1
		}
		return 0
	case float64:
		return x
	case string:
		s := strings.TrimSpace(x)
		if s == "" {
			return 0
		}
		if strings.HasPrefix(s, "0x") || strings.HasPrefix(s, "0X") {
			if n, err := strconv.ParseUint(s[2:], 16, 64); err == nil {
				return float64(n)
			}
			return math.NaN()
		}
		switch s {
		case "Infinity", "+Infinity":
			return math.Inf(1)
		case "-Infinity":
			return math.Inf(-1)
		}
		f, err := strconv.ParseFloat(s, 64)
		if err != nil || strings.ContainsAny(s, "_infINFn") {
			return math.NaN()
		}
		return f
	case *Array:
		if len(x.Elems) == 0 {
			return 0
		}
		if len(x.Elems) == 1 {
			return toNumber(toString(x.Elems[0]))
		}
	}
	return math.NaN()
}

func toInt32(v Value) int32 {
	f := toNumber(v)
	if math.IsNaN(f) || math.IsInf(f, 0) {
		return 0
	}
	return int32(uint32(int64(math.Trunc(f))))
}

func toUint32(v Value) uint32 { return uint32(toInt32(v)) }

func isPrimitive(v Value) bool {
	switch v.(type) {
	case undefinedT, nil, bool, float64, string:
		return true
	}
	return false
}

func strictEquals(a, b Value) bool {
	switch x := a.(type) {
	case undefinedT:
		_, ok := b.(undefinedT)
		return ok
	case nil:
		return b == nil
	case bool:
		y, ok := b.(bool)
		return ok && x == y
	case float64:
		y, ok := b.(float64)
		return ok && x == y
	case string:
		y, ok := b.(string)
		return ok && x == y
	}
	return a == b // reference identity for objects/functions
}

func looseEquals(a, b Value) bool {
	an, bn := a == nil || a == Undefined, b == nil || b == Undefined
	if an || bn {
		return an && bn
	}
	if typeOf(a) == typeOf(b) {
		return strictEquals(a, b)
	}
	if _, ok := a.(bool); ok {
		return looseEquals(toNumber(a), b)
	}
	if _, ok := b.(bool); ok {
		return looseEquals(a, toNumber(b))
	}
	_, aNum := a.(float64)
	_, bNum := b.(float64)
	_, aStr := a.(string)
	_, bStr := b.(string)
	if (aNum && bStr) || (aStr && bNum) {
		return toNumber(a) == toNumber(b)
	}
	if !isPrimitive(a) && isPrimitive(b) {
		return looseEquals(toString(a), b)
	}
	if isPrimitive(a) && !isPrimitive(b) {
		return looseEquals(a, toString(b))
	}
	return false
}

// ---- Go <-> JS conversion ----

// FromGo converts a Go value (as produced by encoding/json or plain Go data)
// into a JS value.
func FromGo(v any) Value {
	switch x := v.(type) {
	case nil:
		return nil
	case undefinedT, *Array, *Object, *Function, *Builtin, *Promise:
		return x
	case bool:
		return x
	case string:
		return x
	case float64:
		return x
	case float32:
		return float64(x)
	case int:
		return float64(x)
	case int8:
		return float64(x)
	case int16:
		return float64(x)
	case int32:
		return float64(x)
	case int64:
		return float64(x)
	case uint:
		return float64(x)
	case uint8:
		return float64(x)
	case uint16:
		return float64(x)
	case uint32:
		return float64(x)
	case uint64:
		return float64(x)
	case json.Number:
		f, _ := x.Float64()
		return f
	case []any:
		a := &Array{Elems: make([]Value, len(x))}
		for i, e := range x {
			a.Elems[i] = FromGo(e)
		}
		return a
	case []string:
		a := &Array{Elems: make([]Value, len(x))}
		for i, e := range x {
			a.Elems[i] = e
		}
		return a
	case map[string]any:
		o := NewObject()
		keys := make([]string, 0, len(x))
		for k := range x {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			o.Set(k, FromGo(x[k]))
		}
		return o
	case map[string]string:
		o := NewObject()
		keys := make([]string, 0, len(x))
		for k := range x {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			o.Set(k, x[k])
		}
		return o
	case error:
		return makeError("Error", x.Error())
	}
	// Anything else: round-trip through JSON.
	b, err := json.Marshal(v)
	if err != nil {
		return toString(fmt.Sprint(v))
	}
	out, err := jsonParse(string(b))
	if err != nil {
		return fmt.Sprint(v)
	}
	return out // structs keep their field order
}

// ToGo converts a JS value to plain Go data (nil, bool, float64, string,
// []any, map[string]any). Functions and promises become nil.
func ToGo(v Value) any {
	return toGo(v, 0)
}

func toGo(v Value, depth int) any {
	if depth > 200 {
		return nil
	}
	switch x := v.(type) {
	case undefinedT, nil:
		return nil
	case bool, string:
		return x
	case float64:
		if x == math.Trunc(x) && math.Abs(x) < 1<<53 {
			return x
		}
		return x
	case *Array:
		out := make([]any, len(x.Elems))
		for i, e := range x.Elems {
			out[i] = toGo(e, depth+1)
		}
		return out
	case *Object:
		out := make(map[string]any, len(x.keys))
		for _, k := range x.keys {
			e := x.vals[k]
			switch e.(type) {
			case undefinedT, *Function, *Builtin:
				continue
			}
			out[k] = toGo(e, depth+1)
		}
		return out
	}
	return nil
}

// ---- JSON (key order preserved) ----

func jsonStringify(it *Interp, v Value, indent string) (Value, error) {
	var b strings.Builder
	seen := map[any]bool{}
	ok, err := writeJSON(it, &b, v, indent, "", seen)
	if err != nil {
		return nil, err
	}
	if !ok {
		return Undefined, nil
	}
	return b.String(), nil
}

func writeJSON(it *Interp, b *strings.Builder, v Value, indent, cur string, seen map[any]bool) (bool, error) {
	switch x := v.(type) {
	case undefinedT, *Function, *Builtin:
		return false, nil
	case nil:
		b.WriteString("null")
	case bool:
		if x {
			b.WriteString("true")
		} else {
			b.WriteString("false")
		}
	case float64:
		if math.IsNaN(x) || math.IsInf(x, 0) {
			b.WriteString("null")
		} else {
			b.WriteString(numberToString(x))
		}
	case string:
		enc, _ := json.Marshal(x)
		// encoding/json escapes <, >, & — JS does not.
		s := strings.NewReplacer(`<`, "<", `>`, ">", `&`, "&").Replace(string(enc))
		b.WriteString(s)
	case *Promise:
		b.WriteString("{}")
	case *Array:
		if seen[x] {
			return false, it.typeError("Converting circular structure to JSON")
		}
		seen[x] = true
		defer delete(seen, x)
		if len(x.Elems) == 0 {
			b.WriteString("[]")
			return true, nil
		}
		inner := cur + indent
		b.WriteByte('[')
		for i, e := range x.Elems {
			if i > 0 {
				b.WriteByte(',')
			}
			if indent != "" {
				b.WriteString("\n" + inner)
			}
			ok, err := writeJSON(it, b, e, indent, inner, seen)
			if err != nil {
				return false, err
			}
			if !ok {
				b.WriteString("null")
			}
		}
		if indent != "" {
			b.WriteString("\n" + cur)
		}
		b.WriteByte(']')
	case *Object:
		if seen[x] {
			return false, it.typeError("Converting circular structure to JSON")
		}
		seen[x] = true
		defer delete(seen, x)
		inner := cur + indent
		b.WriteByte('{')
		n := 0
		for _, k := range x.Keys() {
			e := x.vals[k]
			switch e.(type) {
			case undefinedT, *Function, *Builtin:
				continue
			}
			if n > 0 {
				b.WriteByte(',')
			}
			if indent != "" {
				b.WriteString("\n" + inner)
			}
			kb, _ := json.Marshal(k)
			b.Write(kb)
			b.WriteByte(':')
			if indent != "" {
				b.WriteByte(' ')
			}
			if _, err := writeJSON(it, b, e, indent, inner, seen); err != nil {
				return false, err
			}
			n++
		}
		if n > 0 && indent != "" {
			b.WriteString("\n" + cur)
		}
		b.WriteByte('}')
	default:
		b.WriteString("null")
	}
	return true, nil
}

// ParseJSON decodes JSON text into a JS value keeping object key order
// (document order, with integer-like keys iterating first, as in JavaScript).
func ParseJSON(b []byte) (Value, error) { return jsonParse(string(b)) }

// jsonParse decodes JSON keeping object key order.
func jsonParse(s string) (Value, error) {
	d := json.NewDecoder(strings.NewReader(s))
	d.UseNumber()
	v, err := decodeOrdered(d)
	if err != nil {
		return nil, err
	}
	if d.More() {
		return nil, fmt.Errorf("unexpected data after JSON value")
	}
	return v, nil
}

func decodeOrdered(d *json.Decoder) (Value, error) {
	t, err := d.Token()
	if err != nil {
		return nil, err
	}
	switch x := t.(type) {
	case json.Delim:
		switch x {
		case '{':
			o := NewObject()
			for d.More() {
				kt, err := d.Token()
				if err != nil {
					return nil, err
				}
				v, err := decodeOrdered(d)
				if err != nil {
					return nil, err
				}
				o.Set(kt.(string), v)
			}
			_, err := d.Token()
			return o, err
		case '[':
			a := &Array{}
			for d.More() {
				v, err := decodeOrdered(d)
				if err != nil {
					return nil, err
				}
				a.Elems = append(a.Elems, v)
			}
			_, err := d.Token()
			return a, err
		}
	case json.Number:
		f, _ := x.Float64()
		return f, nil
	case string, bool, nil:
		return x, nil
	}
	return nil, fmt.Errorf("bad JSON")
}

// ---- errors as values ----

func makeError(name, msg string) *Object {
	o := NewObject()
	o.Set("name", name)
	o.Set("message", msg)
	return o
}

func isErrorObject(o *Object) bool {
	n, ok1 := o.vals["name"]
	_, ok2 := o.vals["message"]
	s, isStr := n.(string)
	return ok1 && ok2 && isStr && strings.HasSuffix(s, "Error")
}

// Inspect renders a value for print/console output (like a compact
// console.log): strings bare at top level, everything else JSON-ish.
func Inspect(v Value) string {
	if s, ok := v.(string); ok {
		return s
	}
	return inspect(v, 0)
}

func inspect(v Value, depth int) string {
	switch x := v.(type) {
	case string:
		b, _ := json.Marshal(x)
		return string(b)
	case *Array:
		if depth > 4 {
			return "[Array]"
		}
		parts := make([]string, len(x.Elems))
		for i, e := range x.Elems {
			parts[i] = inspect(e, depth+1)
		}
		return "[" + strings.Join(parts, ", ") + "]"
	case *Object:
		if isErrorObject(x) {
			return toString(x)
		}
		if depth > 4 {
			return "[Object]"
		}
		keys := x.Keys()
		if len(keys) == 0 {
			return "{}"
		}
		parts := make([]string, len(keys))
		for i, k := range keys {
			kk := k
			if !isIdentName(k) {
				b, _ := json.Marshal(k)
				kk = string(b)
			}
			parts[i] = kk + ": " + inspect(x.vals[k], depth+1)
		}
		return "{ " + strings.Join(parts, ", ") + " }"
	case *Promise:
		x.mu.Lock()
		defer x.mu.Unlock()
		switch x.state {
		case 1:
			return "Promise { " + inspect(x.value, depth+1) + " }"
		case 2:
			return "Promise { <rejected> " + inspect(x.value, depth+1) + " }"
		}
		return "Promise { <pending> }"
	case *Function:
		if x.Name == "" {
			return "[Function (anonymous)]"
		}
		return "[Function: " + x.Name + "]"
	case *Builtin:
		return "[Function: " + x.Name + "]"
	}
	return toString(v)
}

func isIdentName(s string) bool {
	if s == "" {
		return false
	}
	for i, r := range s {
		if !(isIdentStart(r) || (i > 0 && isDigit(r))) {
			return false
		}
	}
	return true
}

// Small integers are pre-boxed: converting a float64 to an interface
// allocates, and loop counters, indexes and remainders are mostly small.
var smallNums = func() []Value {
	t := make([]Value, 2048)
	for i := range t {
		t[i] = float64(i - 1024)
	}
	return t
}()

// num boxes f, reusing a shared interface for small integers.
func num(f float64) Value {
	if f >= -1024 && f < 1024 && f == float64(int(f)) && !(f == 0 && math.Signbit(f)) {
		return smallNums[int(f)+1024]
	}
	return f
}
