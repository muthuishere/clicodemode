package codemode

import (
	"context"
	"embed"
	"encoding/json"
	"sort"
	"strings"
	"sync"
	"testing"
)

//go:embed testdata/mods
var testMods embed.FS

func moduleEngine(t *testing.T) (*Engine, *[]string) {
	t.Helper()
	reg := NewRegistry("aos 1.0")
	var moves []string
	var mu sync.Mutex
	reg.MustRegister(Operation{Name: "window.move", Description: "Move a window.",
		InputSchema: []byte(`{"type":"object","required":["app","zone"],"properties":{"app":{"type":"string"},"zone":{"type":"string"},"monitor":{"type":"number"}}}`),
		Handler: func(_ context.Context, a map[string]any) (any, error) {
			mu.Lock() // the two moves run in parallel (Promise.all)
			moves = append(moves, a["app"].(string)+"@"+a["zone"].(string))
			sort.Strings(moves)
			mu.Unlock()
			return true, nil
		}})
	reg.MustRegisterModules("aos", testMods, "testdata/mods")
	return NewEngine(reg), &moves
}

// MOD-1: a folder of embedded files registers as scheme:path modules; .js
// drops its extension, .json keeps it, other files are ignored.
func TestModulesFromFolder(t *testing.T) {
	e, _ := moduleEngine(t)
	var names []string
	for _, m := range e.Registry.Modules() {
		names = append(names, m.Name)
	}
	if got := strings.Join(names, ","); got != "aos:layout,aos:presets.json,aos:util/math" {
		t.Fatalf("modules %s", got)
	}
	m, _ := e.Registry.LookupModule("aos:layout")
	if m.Description != "Window layouts built from aos primitives." {
		t.Fatalf("description %q", m.Description)
	}
	if err := e.Registry.RegisterModuleFile("x", testMods, "testdata/mods/util/math.js"); err != nil {
		t.Fatal(err)
	}
	if _, ok := e.Registry.LookupModule("x:math"); !ok {
		t.Fatal("single file not registered")
	}
	for _, bad := range []Module{{Name: "layout", Source: "1"}, {Name: "a:b c", Source: "1"}, {Name: "a:x", Source: "class {"}, {Name: "a:d.json", Source: "{"}} {
		if e.Registry.RegisterModule(bad) == nil {
			t.Errorf("accepted bad module %+v", bad)
		}
	}
	if e.Registry.RegisterModule(Module{Name: "aos:layout", Source: "1"}) == nil {
		t.Error("duplicate accepted")
	}
}

// MOD-2: programs import modules by name, also from stdin and sessions;
// modules import each other relatively and read JSON data.
func TestModulesImport(t *testing.T) {
	e, moves := moduleEngine(t)
	r := e.Run(context.Background(), `import { sideBySide, names } from "aos:layout"
await sideBySide("Code", "Safari", 5)
names`, "<stdin>")
	if !r.OK {
		t.Fatalf("%+v", r)
	}
	if b, _ := json.Marshal(r.Value); string(b) != `["coding","meeting"]` || strings.Join(*moves, ",") != "Code@left,Safari@right" {
		t.Fatalf("%s %v", b, *moves)
	}
	s := e.NewSession(SessionConfig{})
	defer s.Close()
	if r := s.Run(context.Background(), `import { clamp } from "aos:util/math"; clamp(9, 0, 3)`, RunOptions{}); r.Value != float64(3) {
		t.Fatalf("session import: %+v", r)
	}
	r = e.Run(context.Background(), `import x from "aos:nope"`, "<stdin>")
	if r.OK || !r.NotRun || !strings.Contains(r.Error+r.Diagnostics[0].Message, "aos:layout") {
		t.Fatalf("missing module should list the registered ones: %+v", r)
	}
	e.Registry.MustRegisterModule(Module{Name: "evil:escape", Source: `import x from "../../etc.js"`})
	if r := e.Run(context.Background(), `import "evil:escape"`, "<stdin>"); r.OK {
		t.Fatal("relative import must not leave the module's scheme")
	}
}

// MOD-3: check covers imported modules (operations and literal args), and
// CheckModules checks them all for the host's own tests.
func TestModulesChecked(t *testing.T) {
	e, _ := moduleEngine(t)
	if ds := e.CheckModules(); len(ds) != 0 {
		t.Fatalf("clean modules: %v", ds)
	}
	e.Registry.MustRegisterModule(Module{Name: "aos:broken", Source: `export const f = () => ops.window.move({ app: 1 })`})
	ds := e.CheckModules()
	if len(ds) == 0 || ds[0].File != "aos:broken" {
		t.Fatalf("%v", ds)
	}
	_, ds = e.Compile(`import { f } from "aos:broken"`, "p.js")
	if len(ds) == 0 {
		t.Fatal("check must cover imported modules")
	}
}

// MOD-4: capabilities, describe and skill show modules with their exports and
// doc comments; the fingerprint changes with module content and is unchanged
// for CLIs without modules.
func TestModulesDiscovery(t *testing.T) {
	e, _ := moduleEngine(t)
	caps := e.Registry.Capabilities()
	if len(caps.Modules) != 3 || caps.Modules[0].Name != "aos:layout" {
		t.Fatalf("%+v", caps.Modules)
	}
	ex := caps.Modules[0].Exports
	if len(ex) != 2 || ex[0].Name != "sideBySide" || ex[0].Doc != "Put two apps side by side on a monitor." || ex[1].Doc != "The named presets." {
		t.Fatalf("%+v", ex)
	}
	skill := Skill(e.Registry, "aos code")
	if !strings.Contains(skill, `import { sideBySide, names } from "aos:layout"`) || strings.Index(skill, "## Modules") > strings.Index(skill, "## Operations") {
		t.Fatalf("skill:\n%s", skill)
	}
	before := caps.Fingerprint
	e.Registry.UnregisterModule("aos:util/math")
	e.Registry.MustRegisterModule(Module{Name: "aos:util/math", Source: "export function clamp(n) { return n }"})
	if e.Registry.Capabilities().Fingerprint == before {
		t.Fatal("fingerprint must change with module content")
	}
	plain := NewRegistry("v")
	if len(plain.Capabilities().Modules) != 0 {
		t.Fatal("no modules section without modules")
	}
	var out, errb strings.Builder
	c := NewCLI(e, "aos code")
	c.Stdout, c.Stderr = &out, &errb
	if code := c.Main(context.Background(), []string{"describe", "aos:layout"}); code != 0 || !strings.Contains(out.String(), "export sideBySide: Put two apps") {
		t.Fatalf("%d %s %s", code, out.String(), errb.String())
	}
}
