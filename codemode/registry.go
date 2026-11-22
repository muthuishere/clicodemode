// Package codemode turns a Go CLI's commands into discoverable, self-describing
// operations that small JavaScript-like programs can call and compose.
package codemode

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"sync"
)

// Handler runs one operation. args is the decoded JSON-like object passed by
// the program; the result must be JSON-encodable (nil, bool, float64, string,
// []any, map[string]any, or anything json.Marshal accepts).
type Handler func(ctx context.Context, args map[string]any) (any, error)

// SideEffect classifies what an operation may change.
type SideEffect string

const (
	SideEffectNone     SideEffect = "none"     // pure read
	SideEffectLocal    SideEffect = "local"    // changes this machine (files, windows, processes)
	SideEffectExternal SideEffect = "external" // changes something remote (network, accounts)
)

// Example is a worked call shown by describe and docs.
type Example struct {
	Description string         `json:"description,omitempty"`
	Args        map[string]any `json:"args,omitempty"`
	Result      any            `json:"result,omitempty"`
}

// Operation is one callable unit. Name uses dotted lower-case segments,
// e.g. "health.check" or "window.focus".
type Operation struct {
	Name          string          `json:"name"`
	Description   string          `json:"description"`
	InputSchema   json.RawMessage `json:"input_schema,omitempty"`
	OutputSchema  json.RawMessage `json:"output_schema,omitempty"`
	SideEffects   SideEffect      `json:"side_effects"`
	Idempotent    bool            `json:"idempotent"`
	Authorization string          `json:"authorization,omitempty"` // scope the host must grant, empty = none
	Examples      []Example       `json:"examples,omitempty"`
	Handler       Handler         `json:"-"`
}

var opNameRe = regexp.MustCompile(`^[a-z][a-z0-9_-]*(\.[a-z][a-z0-9_-]*)*$`)

// Registry holds operations. It is safe for concurrent use.
type Registry struct {
	mu  sync.RWMutex
	ops map[string]*Operation
	// Version is the host CLI version; it feeds the capability fingerprint.
	Version string
}

// NewRegistry returns an empty registry for a CLI at the given version.
func NewRegistry(version string) *Registry {
	return &Registry{ops: map[string]*Operation{}, Version: version}
}

// Register adds an operation. It rejects bad names, duplicates, a missing
// handler and schemas that are not valid JSON.
func (r *Registry) Register(op Operation) error {
	if !opNameRe.MatchString(op.Name) {
		return fmt.Errorf("codemode: invalid operation name %q (want dotted lower-case, e.g. health.check)", op.Name)
	}
	if op.Handler == nil {
		return fmt.Errorf("codemode: operation %q has no handler", op.Name)
	}
	for label, s := range map[string]json.RawMessage{"input_schema": op.InputSchema, "output_schema": op.OutputSchema} {
		if len(s) > 0 && !json.Valid(s) {
			return fmt.Errorf("codemode: operation %q: %s is not valid JSON", op.Name, label)
		}
	}
	if op.SideEffects == "" {
		op.SideEffects = SideEffectLocal // conservative default
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, dup := r.ops[op.Name]; dup {
		return fmt.Errorf("codemode: operation %q already registered", op.Name)
	}
	cp := op
	r.ops[op.Name] = &cp
	return nil
}

// MustRegister is Register that panics; for static setup code.
func (r *Registry) MustRegister(op Operation) {
	if err := r.Register(op); err != nil {
		panic(err)
	}
}

// Lookup returns the operation with this name.
func (r *Registry) Lookup(name string) (*Operation, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	op, ok := r.ops[name]
	return op, ok
}

// Operations returns all operations sorted by name.
func (r *Registry) Operations() []*Operation {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]*Operation, 0, len(r.ops))
	for _, op := range r.ops {
		out = append(out, op)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// Capabilities is the machine-readable summary agents cache.
type Capabilities struct {
	Spec        string       `json:"spec"`
	Version     string       `json:"version"`
	Fingerprint string       `json:"fingerprint"`
	Language    LanguageInfo `json:"language"`
	Operations  []*Operation `json:"operations"`
}

// LanguageInfo states what the program language supports today.
type LanguageInfo struct {
	Name        string   `json:"name"`
	Features    []string `json:"features"`
	Unsupported []string `json:"unsupported"`
	Globals     []string `json:"globals"`
}

// SpecVersion is the version of the capabilities document format.
const SpecVersion = "codemode/v0"

// LibraryVersion is this library's release, printed by `code version` so a
// runner can tell which build is on its PATH.
const LibraryVersion = "0.5.0"

// Capabilities returns the full capability document with a fingerprint that
// changes whenever any operation's public contract changes.
func (r *Registry) Capabilities() Capabilities {
	ops := r.Operations()
	h := sha256.New()
	fmt.Fprintf(h, "%s\n%s\n", SpecVersion, r.Version)
	for _, op := range ops {
		b, _ := json.Marshal(op)
		h.Write(b)
		h.Write([]byte{'\n'})
	}
	return Capabilities{
		Spec:        SpecVersion,
		Version:     r.Version,
		Fingerprint: "sha256:" + hex.EncodeToString(h.Sum(nil))[:32],
		Language:    languageInfo,
		Operations:  ops,
	}
}

var languageInfo = LanguageInfo{
	Name: "codemode-js (subset)",
	Features: []string{
		"const/let", "null/undefined/boolean/number/string/array/object", "template literals",
		"operators incl. ?? ?. ** and logical assignment", "destructuring", "spread/rest",
		"if/else", "ternary", "for/for-of/for-in/while/do-while", "break/continue",
		"functions/arrow functions/closures/default params", "try/catch/finally/throw",
		"async/await", "Promise.resolve/reject/all/allSettled/race/any", ".then/.catch/.finally",
		"static import/export of ./relative .js modules and .json data",
		"lazy streams: lines/jsonl/json/csv/yaml, map/filter/take, backpressure",
		"pmap bounded-concurrency parallel map",
	},
	Unsupported: []string{"class", "new", "this", "generators", "switch", "delete", "regex", "dynamic import()", "eval", "npm", "Node/browser APIs"},
	Globals: []string{"call(name, args)", "input (from --input JSON, else null)", "ops.<op path>(args)", "print", "console.log", "JSON", "Math",
		"Object.keys/values/entries/fromEntries/assign", "Array.isArray/from", "String", "Number", "Boolean",
		"parseInt", "parseFloat", "isNaN", "sleep(ms)", "stream(array)", "pmap(items, fn, {concurrency})",
		"exec(argv, opts) (host policy)", "execStream(argv, opts) (host policy)", "shell(cmd) (separately authorized)", "Error/TypeError/RangeError(msg)", "Promise"},
}
