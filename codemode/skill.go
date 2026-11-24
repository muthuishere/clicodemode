package codemode

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// Skill renders a CLI-specific SKILL.md for agents. name is the host command
// as shown in help (e.g. "mycli code"); its first word is the CLI name.
func Skill(reg *Registry, name string) string {
	cli := "cli"
	if f := strings.Fields(name); len(f) > 0 {
		cli = f[0]
	}
	code := name
	if code == "" {
		code = cli + " code"
	}
	ops := reg.Operations()
	var b strings.Builder
	fmt.Fprintf(&b, "---\nname: %s-code\n", cli)
	fmt.Fprintf(&b, "description: Drive %s through Code Mode (`%s ...`): write a small JavaScript-like program that calls and composes its %d operation(s) instead of running commands one at a time. Use when a task needs several %s calls, parallel calls, loops or data shaping over results.\n---\n\n", cli, code, len(ops), cli)
	fmt.Fprintf(&b, "# %s Code Mode\n\n", cli)
	fmt.Fprintf(&b, "General language and workflow rules: see the global `code` skill (code/SKILL.md). Live reference: `%s docs`.\n\n", code)
	fmt.Fprintf(&b, "Version: %s\n\n", reg.Version)
	if mods := reg.Modules(); len(mods) > 0 {
		// Modules first: they are the CLI's own higher-level building blocks,
		// so an agent should reach for them before composing raw operations.
		fmt.Fprintf(&b, "## Modules\n\nPrefer these over composing operations by hand. Read one with `%s describe <module>`.\n\n", code)
		for _, m := range mods {
			info := m.Info()
			var names []string
			for _, ex := range info.Exports {
				names = append(names, ex.Name)
			}
			fmt.Fprintf(&b, "- `import { %s } from %q`", strings.Join(names, ", "), info.Name)
			if info.Description != "" {
				fmt.Fprintf(&b, " — %s", oneLine(info.Description))
			}
			b.WriteString("\n")
			for _, ex := range info.Exports {
				if ex.Doc != "" {
					fmt.Fprintf(&b, "  - `%s`: %s\n", ex.Name, oneLine(ex.Doc))
				}
			}
		}
		b.WriteString("\n")
	}
	b.WriteString("## Operations\n\n")
	if len(ops) == 0 {
		b.WriteString("This CLI registers no operations yet.\n")
	}
	if len(ops) > skillListLimit {
		// Large CLIs: group names by their first word and leave contracts to
		// describe, so the skill stays a few KB instead of tens.
		fmt.Fprintf(&b, "%d operations, grouped. Get any contract with `%s describe <op> --json`; list all with `%s operations`.\n\n", len(ops), code, code)
		groups := map[string][]string{}
		var order []string
		for _, op := range ops {
			g := op.Name
			if i := strings.IndexAny(g, ".-"); i > 0 {
				g = g[:i]
			}
			if _, ok := groups[g]; !ok {
				order = append(order, g)
			}
			groups[g] = append(groups[g], op.Name)
		}
		for _, g := range order {
			fmt.Fprintf(&b, "- **%s**: %s\n", g, strings.Join(groups[g], ", "))
		}
		ops = ops[:1] // the example below still uses the first operation
	}
	for _, op := range ops[:min(len(ops), skillListLimit)] {
		if len(reg.Operations()) > skillListLimit {
			break
		}
		// One line per operation: argument names only (required first, optional
		// marked ?). Full schemas stay in `describe --json` to keep the skill small.
		fmt.Fprintf(&b, "- `%s(%s)` — %s [%s", op.Name, argSummary(op.InputSchema), oneLine(op.Description), op.SideEffects)
		if op.Authorization != "" {
			fmt.Fprintf(&b, ", authorization: %s", op.Authorization)
		}
		b.WriteString("]\n")
	}
	if len(ops) > 0 {
		op := ops[0]
		args := map[string]any{}
		if len(op.Examples) > 0 && op.Examples[0].Args != nil {
			args = op.Examples[0].Args
		}
		a, _ := json.Marshal(args)
		fmt.Fprintf(&b, "\n## Example\n\n```js\nconst r = await call(%q, %s)\nr\n```\n\n", op.Name, a)
		fmt.Fprintf(&b, "```sh\n%s check prog.js && %s run prog.js --json\n```\n", code, code)
	}
	fmt.Fprintf(&b, "\nDescribe an operation before using it: `%s describe <op> --json`. Exit codes: 0 ok, 1 failed, 2 errors (nothing ran).\n", code)
	fmt.Fprintf(&b, "\n## From Claude Code Code Mode or other agent code\n\nSend one program per task, not one command per step: `%s run - --input '<json>' --json` with the program on stdin. The program reads `input`; the result is `value` in the JSON. Your own code joins results across different CLIs.\n", code)
	return b.String()
}

// skillListLimit is the most operations listed one per line in `code skill`.
const skillListLimit = 40

// argSummary renders an input schema as "{a, b?, c?}" from its properties.
func argSummary(schema json.RawMessage) string {
	var s struct {
		Properties map[string]json.RawMessage `json:"properties"`
		Required   []string                   `json:"required"`
	}
	if len(schema) == 0 || json.Unmarshal(schema, &s) != nil || len(s.Properties) == 0 {
		return ""
	}
	req := map[string]bool{}
	var out []string
	for _, r := range s.Required {
		req[r] = true
		out = append(out, r)
	}
	var opt []string
	for k := range s.Properties {
		if !req[k] {
			opt = append(opt, k+"?")
		}
	}
	sort.Strings(opt)
	return "{" + strings.Join(append(out, opt...), ", ") + "}"
}

// oneLine keeps a description to its first line, at most 120 characters.
func oneLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	if r := []rune(s); len(r) > 120 {
		s = string(r[:117]) + "..."
	}
	return s
}
