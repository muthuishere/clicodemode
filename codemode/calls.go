package codemode

import (
	"encoding/json"
	"strings"

	"github.com/muthuishere/clicodemode/js"
)

// Unknown stands for an argument value only known at run time (a variable,
// a call, a spread) in StaticCall.Args. ValidateArgs skips it.
type Unknown struct{}

// MarshalJSON renders Unknown as the string "<unknown>".
func (Unknown) MarshalJSON() ([]byte, error) { return []byte(`"<unknown>"`), nil }

// StaticCall is a call site found in a program's source: any call whose
// callee is a dotted member chain (gh.repos.list(...), ops.a.b(...)) or
// call("name", ...).
type StaticCall struct {
	// Callee is the dotted path as written, e.g. "ops.repo.status" or
	// "gh.listPRs". For call("x.y", ...) it is "call".
	Callee string `json:"callee"`
	// Operation is the registered-operation name the call reaches through
	// ops.* or call("..."), or "" when the callee is something else.
	Operation string `json:"operation,omitempty"`
	// Args is the first argument as JSON-like data (map, slice, string,
	// float64, bool, nil), with Unknown where the value is only known at run
	// time; nil when there is no argument.
	Args any    `json:"args"`
	File string `json:"file"`
	Line int    `json:"line"`
	Col  int    `json:"col"`
}

// Calls lists the program's static call sites in source order, so a host
// that exposes operations under its own names (an alias namespace defined
// with Session.Define) can map them and validate literal arguments with
// Engine.ValidateArgs, as check does for ops.* and call("...").
func (p *Program) Calls() []StaticCall {
	var out []StaticCall
	if p == nil || p.ast == nil {
		return nil
	}
	js.Walk(p.ast, func(n js.Node) {
		c, ok := n.(*js.Call)
		if !ok {
			return
		}
		args := c.Args
		at := rootPos(c.Callee)
		sc := StaticCall{File: p.File, Line: at.Line, Col: at.Col}
		if id, ok := c.Callee.(*js.Ident); ok && id.Name == "call" && len(args) > 0 {
			s, ok := args[0].(*js.StringLit)
			if !ok {
				return
			}
			sc.Callee, sc.Operation, args = "call", s.V, args[1:]
		} else if path, ok := memberPath(c.Callee); ok {
			sc.Callee = path
			if rest, ok := strings.CutPrefix(path, "ops."); ok {
				sc.Operation = rest
			}
		} else {
			return
		}
		if len(args) > 0 {
			sc.Args = exportLiteral(literalValue(args[0]))
		}
		out = append(out, sc)
	})
	return out
}

// rootPos is the position of the identifier a member chain starts with.
func rootPos(n js.Node) js.Pos {
	for {
		m, ok := n.(*js.Member)
		if !ok {
			return n.P()
		}
		n = m.Obj
	}
}

// memberPath returns "a.b.c" for the callee a.b.c (string-literal computed
// keys included, as ops["x-y"]).
func memberPath(n js.Node) (string, bool) {
	var parts []string
	for {
		m, ok := n.(*js.Member)
		if !ok {
			break
		}
		name := m.Name
		if m.Computed != nil {
			s, ok := m.Computed.(*js.StringLit)
			if !ok {
				return "", false
			}
			name = s.V
		}
		parts = append([]string{name}, parts...)
		n = m.Obj
	}
	id, ok := n.(*js.Ident)
	if !ok || len(parts) == 0 {
		return "", false
	}
	return id.Name + "." + strings.Join(parts, "."), true
}

func exportLiteral(v any) any {
	switch x := v.(type) {
	case unknownArg:
		return Unknown{}
	case map[string]any:
		out := make(map[string]any, len(x))
		for k, e := range x {
			out[k] = exportLiteral(e)
		}
		return out
	case []any:
		out := make([]any, len(x))
		for i, e := range x {
			out[i] = exportLiteral(e)
		}
		return out
	}
	return v
}

func importLiteral(v any) any {
	switch x := v.(type) {
	case Unknown:
		return unknownArg{}
	case map[string]any:
		out := make(map[string]any, len(x))
		for k, e := range x {
			out[k] = importLiteral(e)
		}
		return out
	case []any:
		out := make([]any, len(x))
		for i, e := range x {
			out[i] = importLiteral(e)
		}
		return out
	}
	return v
}

// ValidateArgs checks args (as StaticCall.Args, or any JSON-like value)
// against the input schema of the registered operation name, skipping
// Unknown parts. It returns one message per problem; none means valid as far
// as can be known statically. An unknown operation is reported too.
func (e *Engine) ValidateArgs(name string, args any) []string {
	op, ok := e.Registry.Lookup(name)
	if !ok {
		msg := "unknown operation " + strconvQuote(name)
		if s := e.suggest(name); s != "" {
			msg += " (did you mean " + strconvQuote(s) + "?)"
		}
		return []string{msg}
	}
	v := importLiteral(args)
	if _, unknown := v.(unknownArg); unknown || v == nil {
		return nil
	}
	if _, isObj := v.(map[string]any); !isObj {
		return []string{name + ": arguments must be an object, got " + jsonType(v)}
	}
	if len(op.InputSchema) == 0 {
		return nil
	}
	var s map[string]any
	if json.Unmarshal(op.InputSchema, &s) != nil {
		return nil
	}
	var errs []string
	validateValue(s, v, "", &errs)
	for i := range errs {
		errs[i] = name + ": " + errs[i]
	}
	return errs
}

func strconvQuote(s string) string { b, _ := json.Marshal(s); return string(b) }
