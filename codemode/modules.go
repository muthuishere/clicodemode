package codemode

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io/fs"
	"path"
	"regexp"
	"sort"
	"strings"

	"github.com/muthuishere/clicodemode/js"
)

// Module is a JavaScript (or JSON data) module a host ships inside its build
// (go:embed, include_str!, package data, a bundled string) so programs can
// import it by name:
//
//	import { coding } from "aos:layout"
//
// Modules compose operations; they cannot do anything the registered
// operations do not allow. Names are "<scheme>:<path>"; a .js module is named
// without its extension ("aos:layout"), a JSON module keeps it
// ("aos:presets.json").
type Module struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Source      string `json:"-"`
}

// ModuleInfo is what capabilities and describe show about a module.
type ModuleInfo struct {
	Name        string         `json:"name"`
	Description string         `json:"description,omitempty"`
	Exports     []ModuleExport `json:"exports"`
}

// ModuleExport is one exported name with the doc comment written above it.
type ModuleExport struct {
	Name string `json:"name"`
	Doc  string `json:"doc,omitempty"`
}

var moduleNameRe = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_-]*:[A-Za-z0-9_-]+(/[A-Za-z0-9_-]+)*(\.json)?$`)

// RegisterModule adds a module. Its source must parse (a .json module must
// be valid JSON); operation references are checked when a program imports
// it, and by Engine.CheckModules for the host's own tests.
func (r *Registry) RegisterModule(m Module) error {
	m.Name = strings.TrimSuffix(m.Name, ".js")
	if m.Description == "" && !strings.HasSuffix(m.Name, ".json") {
		m.Description = leadingComment(m.Source)
	}
	if !moduleNameRe.MatchString(m.Name) {
		return fmt.Errorf("codemode: invalid module name %q (want scheme:path, e.g. aos:layout or aos:data/presets.json)", m.Name)
	}
	if strings.HasSuffix(m.Name, ".json") {
		if !json.Valid([]byte(m.Source)) {
			return fmt.Errorf("codemode: module %q is not valid JSON", m.Name)
		}
	} else if _, err := js.Parse(m.Source, m.Name); err != nil {
		return fmt.Errorf("codemode: module %q: %v", m.Name, err)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.modules == nil {
		r.modules = map[string]*Module{}
	}
	if _, dup := r.modules[m.Name]; dup {
		return fmt.Errorf("codemode: module %q already registered", m.Name)
	}
	cp := m
	r.modules[m.Name] = &cp
	r.rev.Add(1)
	return nil
}

// MustRegisterModule is RegisterModule that panics; for static setup code.
func (r *Registry) MustRegisterModule(m Module) {
	if err := r.RegisterModule(m); err != nil {
		panic(err)
	}
}

// RegisterModules registers every .js and .json file under dir of fsys as
// "<scheme>:<path relative to dir>", for example with go:embed:
//
//	//go:embed js
//	var jsFiles embed.FS
//	reg.MustRegisterModules("aos", jsFiles, "js") // js/layout.js -> aos:layout
//
// A file's leading comment block (// or /** */) becomes the description.
func (r *Registry) RegisterModules(scheme string, fsys fs.FS, dir string) error {
	if dir == "" {
		dir = "."
	}
	n := 0
	err := fs.WalkDir(fsys, dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		ext := path.Ext(p)
		if ext != ".js" && ext != ".json" {
			return nil
		}
		b, err := fs.ReadFile(fsys, p)
		if err != nil {
			return err
		}
		rel := strings.TrimPrefix(strings.TrimPrefix(p, dir), "/")
		if dir == "." {
			rel = p
		}
		n++
		return r.RegisterModule(Module{Name: scheme + ":" + rel, Source: string(b), Description: leadingComment(string(b))})
	})
	if err == nil && n == 0 {
		err = fmt.Errorf("codemode: no .js or .json files under %q", dir)
	}
	return err
}

// MustRegisterModules is RegisterModules that panics.
func (r *Registry) MustRegisterModules(scheme string, fsys fs.FS, dir string) {
	if err := r.RegisterModules(scheme, fsys, dir); err != nil {
		panic(err)
	}
}

// RegisterModuleFile registers one .js or .json file of fsys as
// "<scheme>:<file name without .js>", e.g. RegisterModuleFile("aos",
// jsFiles, "js/layout.js") -> aos:layout.
func (r *Registry) RegisterModuleFile(scheme string, fsys fs.FS, file string) error {
	ext := path.Ext(file)
	if ext != ".js" && ext != ".json" {
		return fmt.Errorf("codemode: module file %q must end in .js or .json", file)
	}
	b, err := fs.ReadFile(fsys, file)
	if err != nil {
		return err
	}
	return r.RegisterModule(Module{Name: scheme + ":" + path.Base(file), Source: string(b), Description: leadingComment(string(b))})
}

// UnregisterModule removes a module; it reports whether it existed.
func (r *Registry) UnregisterModule(name string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.modules[name]; !ok {
		return false
	}
	delete(r.modules, name)
	r.rev.Add(1)
	return true
}

// LookupModule returns a registered module.
func (r *Registry) LookupModule(name string) (*Module, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	m, ok := r.modules[name]
	return m, ok
}

// Modules returns all modules sorted by name.
func (r *Registry) Modules() []*Module {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]*Module, 0, len(r.modules))
	for _, m := range r.modules {
		out = append(out, m)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// Info returns the module's exports with their doc comments.
func (m *Module) Info() ModuleInfo {
	info := ModuleInfo{Name: m.Name, Description: m.Description, Exports: []ModuleExport{}}
	if strings.HasSuffix(m.Name, ".json") {
		info.Exports = append(info.Exports, ModuleExport{Name: "default", Doc: "JSON data"})
		return info
	}
	prog, err := js.Parse(m.Source, m.Name)
	if err != nil {
		return info
	}
	lines := strings.Split(m.Source, "\n")
	for _, s := range prog.Body {
		ex, ok := s.(*js.Export)
		if !ok {
			continue
		}
		doc := commentAbove(lines, ex.P().Line)
		for _, name := range exportNames(ex) {
			info.Exports = append(info.Exports, ModuleExport{Name: name, Doc: doc})
		}
	}
	return info
}

func exportNames(ex *js.Export) []string {
	if ex.Default != nil {
		return []string{"default"}
	}
	switch d := ex.Decl.(type) {
	case *js.VarDecl:
		var out []string
		for _, dc := range d.Decls {
			out = append(out, js.PatternNames(dc.Target)...)
		}
		return out
	case *js.ClassStmt:
		return []string{d.Class.Name}
	}
	out := make([]string, 0, len(ex.Names))
	for name := range ex.Names {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// commentAbove returns the // or /** */ comment that ends on the line above
// line (1-based), without comment markers.
func commentAbove(lines []string, line int) string {
	i := line - 2
	if i < 0 || i >= len(lines) {
		return ""
	}
	t := strings.TrimSpace(lines[i])
	var parts []string
	switch {
	case strings.HasPrefix(t, "//"):
		for ; i >= 0 && strings.HasPrefix(strings.TrimSpace(lines[i]), "//"); i-- {
			parts = append([]string{strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(lines[i]), "//"))}, parts...)
		}
	case strings.HasSuffix(t, "*/"):
		for ; i >= 0; i-- {
			l := strings.TrimSpace(lines[i])
			start := strings.HasPrefix(l, "/*")
			l = strings.TrimSuffix(strings.TrimPrefix(strings.TrimPrefix(strings.TrimPrefix(l, "/**"), "/*"), "*"), "*/")
			if l = strings.TrimSpace(l); l != "" {
				parts = append([]string{l}, parts...)
			}
			if start {
				break
			}
		}
	}
	return strings.Join(parts, " ")
}

// leadingComment is the comment block at the top of a file.
func leadingComment(src string) string {
	lines := strings.Split(src, "\n")
	end := 0
	for end < len(lines) {
		t := strings.TrimSpace(lines[end])
		if t == "" && end == 0 {
			end++
			continue
		}
		if !strings.HasPrefix(t, "//") && !strings.HasPrefix(t, "/*") && !strings.HasPrefix(t, "*") {
			break
		}
		end++
		if strings.HasSuffix(t, "*/") {
			break
		}
	}
	if end == 0 {
		return ""
	}
	return commentAbove(lines, end+1)
}

// modulesHash feeds the capability fingerprint ("" when there are none, so
// fingerprints of CLIs without modules are unchanged).
func modulesHash(ms []*Module) string {
	if len(ms) == 0 {
		return ""
	}
	h := sha256.New()
	for _, m := range ms {
		fmt.Fprintf(h, "%s\n%s\n%s\n", m.Name, m.Description, m.Source)
	}
	return hex.EncodeToString(h.Sum(nil))
}

// resolveModule maps an import spec to a registered module name. spec is
// either "scheme:path" or, inside a registered module, a relative path that
// stays within the same scheme.
func resolveModule(from, spec string) (string, bool, error) {
	if i := strings.Index(spec, ":"); i > 0 && !strings.HasPrefix(spec, ".") && !isDrivePath(spec, i) {
		return strings.TrimSuffix(spec, ".js"), true, nil
	}
	i := strings.Index(from, ":")
	if i <= 0 || isDrivePath(from, i) || strings.HasPrefix(from, "<") || !(strings.HasPrefix(spec, "./") || strings.HasPrefix(spec, "../")) {
		return "", false, nil
	}
	scheme, rest := from[:i], from[i+1:]
	p := path.Join(path.Dir(rest), spec)
	if p == ".." || strings.HasPrefix(p, "../") {
		return "", true, fmt.Errorf("%s leaves module %s:", spec, scheme)
	}
	return scheme + ":" + strings.TrimSuffix(p, ".js"), true, nil
}

// CheckModules checks every registered module as check would check a program
// that imports it (unknown operations, bad literal arguments, imports of
// missing modules), for a host's own tests.
func (e *Engine) CheckModules() []Diagnostic {
	var out []Diagnostic
	for _, m := range e.Registry.Modules() {
		if strings.HasSuffix(m.Name, ".json") {
			continue
		}
		_, ds := e.Compile(m.Source, m.Name)
		out = append(out, ds...)
	}
	return out
}

// isDrivePath reports a Windows path such as C:\x or C:/x (not a module).
func isDrivePath(s string, colon int) bool {
	return colon == 1 && len(s) > 2 && (s[2] == '\\' || s[2] == '/')
}
