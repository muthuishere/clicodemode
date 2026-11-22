package codemode

import (
	"encoding/json"
	"fmt"
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
	fmt.Fprintf(&b, "Version: %s\n\n## Operations\n\n", reg.Version)
	if len(ops) == 0 {
		b.WriteString("This CLI registers no operations yet.\n")
	}
	for _, op := range ops {
		in := "{}"
		if len(op.InputSchema) > 0 {
			in = compact(op.InputSchema)
		}
		fmt.Fprintf(&b, "- `%s(%s)` — %s [side effects: %s", op.Name, in, op.Description, op.SideEffects)
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
	return b.String()
}
