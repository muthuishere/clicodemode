package codemode

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/muthuishere/clicodemode/js"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"text/tabwriter"
)

// CLI mounts the `code` subcommands into any host CLI. The host passes the
// arguments after its own `code` word, e.g. for `mycli code run x.js` it
// calls cli.Main(ctx, []string{"run", "x.js"}).
//
// Exit codes: 0 ok, 1 the program ran and failed, 2 usage error or the
// program has diagnostics (nothing ran).
type CLI struct {
	Engine *Engine
	// Name is the host command shown in help, e.g. "mycli code".
	Name   string
	Stdin  io.Reader
	Stdout io.Writer
	Stderr io.Writer
}

// NewCLI returns a CLI for the engine, wired to the process's stdio.
func NewCLI(e *Engine, name string) *CLI {
	if e.WorkflowCLI == "" {
		// "mycli code" -> plans call "mycli code run-step ..."
		e.WorkflowCLI = strings.TrimSuffix(strings.TrimSpace(name), " code")
	}
	return &CLI{Engine: e, Name: name, Stdin: os.Stdin, Stdout: os.Stdout, Stderr: os.Stderr}
}

const usage = `Code Mode for this CLI: discover operations and run small programs that call them.

Usage:
  %[1]s operations [--json]            list operations
  %[1]s describe <operation> [--json]  show one operation's contract
  %[1]s capabilities --json            full machine-readable capability document
  %[1]s docs                           language and API reference for writing programs
  %[1]s check <program.js|->           parse and validate; nothing runs
  %[1]s compile <program.js|->         check, then print the compiled plan and cache key
  %[1]s run <program.js|-> [--json]    run a program; prints its result
  %[1]s eval <expression> [--json]     evaluate one expression
  %[1]s skill                          print a SKILL.md for agents driving this CLI
  %[1]s run-step <module.js>#<export> [--input JSON] [--outcomes a,b]
                                       call one exported function with {input, steps:{}}

A program reads '-' from stdin. Exit codes: 0 ok, 1 failed, 2 errors (nothing ran).
run-step exits 0 ok, 1 on any failure, 2 on usage errors; with --outcomes
it exits 10+index of the returned outcome and never 0.

Set CODEMODE_CACHE_DIR to a directory to cache compiled plans for check and
compile (off by default; only plans and diagnostics are stored, never secrets
or approvals).
`

// Main runs one subcommand and returns the exit code.
func (c *CLI) Main(ctx context.Context, args []string) int {
	if len(args) == 0 || args[0] == "help" || args[0] == "-h" || args[0] == "--help" {
		fmt.Fprintf(c.Stdout, usage, c.Name)
		return 0
	}
	sub, rest := args[0], args[1:]
	jsonOut, pos := false, []string{}
	input, hasInput := "", false
	var outcomes []string
	for i := 0; i < len(rest); i++ {
		a := rest[i]
		switch {
		case a == "--json":
			jsonOut = true
		case sub == "run-step" && a == "--input":
			if i+1 >= len(rest) {
				return c.usageErr("--input needs a JSON value")
			}
			i++
			input, hasInput = rest[i], true
		case sub == "run-step" && strings.HasPrefix(a, "--input="):
			input, hasInput = strings.TrimPrefix(a, "--input="), true
		case sub == "run-step" && (a == "--outcomes" || strings.HasPrefix(a, "--outcomes=")):
			v := strings.TrimPrefix(a, "--outcomes=")
			if a == "--outcomes" {
				if i+1 >= len(rest) {
					return c.usageErr("--outcomes needs a comma-separated list")
				}
				i++
				v = rest[i]
			}
			outcomes = strings.Split(v, ",")
		default:
			pos = append(pos, a)
		}
	}
	c.wireCache()
	switch sub {
	case "operations", "ops":
		return c.operations(jsonOut)
	case "describe":
		if len(pos) != 1 {
			return c.usageErr("describe needs exactly one operation name")
		}
		return c.describe(pos[0], jsonOut)
	case "capabilities", "caps":
		return c.writeJSON(c.Engine.Registry.Capabilities())
	case "docs":
		fmt.Fprint(c.Stdout, Docs(c.Engine.Registry, c.Name))
		return 0
	case "check", "compile":
		if len(pos) != 1 {
			return c.usageErr(sub + " needs a program file or -")
		}
		src, file, err := c.readProgram(pos[0])
		if err != nil {
			return c.usageErr(err.Error())
		}
		p, diags, _ := c.Engine.CompilePlan(src, file)
		if sub == "check" {
			if jsonOut {
				if diags == nil {
					diags = []Diagnostic{}
				}
				c.writeJSON(diags)
			} else {
				for _, d := range diags {
					fmt.Fprintln(c.Stderr, d)
				}
			}
			if hasErrors(diags) {
				if !jsonOut {
					fmt.Fprintf(c.Stderr, "%d error(s)\n", countErrors(diags))
				}
				return 2
			}
			if !jsonOut {
				fmt.Fprintf(c.Stdout, "ok: %s (%d operation(s), %d import(s))\n", file, len(p.Operations), len(p.Imports))
			}
			return 0
		}
		for _, d := range diags {
			fmt.Fprintln(c.Stderr, d)
		}
		if hasErrors(diags) {
			return 2
		}
		return c.writeJSON(p)
	case "run":
		if len(pos) != 1 {
			return c.usageErr("run needs a program file or -")
		}
		src, file, err := c.readProgram(pos[0])
		if err != nil {
			return c.usageErr(err.Error())
		}
		return c.report(c.Engine.Run(ctx, src, file), jsonOut)
	case "eval":
		if len(pos) == 0 {
			return c.usageErr("eval needs an expression")
		}
		expr := strings.Join(pos, " ")
		if expr == "-" {
			b, err := io.ReadAll(c.Stdin)
			if err != nil {
				return c.usageErr(err.Error())
			}
			expr = string(b)
		}
		return c.report(c.Engine.Eval(ctx, expr), jsonOut)
	case "skill":
		fmt.Fprint(c.Stdout, Skill(c.Engine.Registry, c.Name))
		return 0
	case "run-step":
		if len(pos) != 1 {
			return c.usageErr("run-step needs <module.js>#<export>")
		}
		if !hasInput {
			input = "null"
		}
		return c.runStep(ctx, pos[0], input, hasInput, outcomes)
	}
	return c.usageErr(fmt.Sprintf("unknown subcommand %q", sub))
}

// wireCache turns on the plan cache from CODEMODE_CACHE_DIR when the host
// has not set Engine.Cache itself. Unset = off.
func (c *CLI) wireCache() {
	if c.Engine.Cache != nil {
		return
	}
	if dir := os.Getenv("CODEMODE_CACHE_DIR"); dir != "" {
		c.Engine.Cache = &Cache{Dir: dir}
	}
}

var identRe = regexp.MustCompile(`^[A-Za-z_$][A-Za-z0-9_$]*$`)

// runStep loads module.js through the engine's loader rules, calls the named
// export with {input, steps: {}}, awaits it and prints the JSON result.
// runStep exits 0 on success. With outcomes, the step must return
// {outcome: outcomes[i]} and exits js.OutcomeExitBase+i, which the exported
// WFNexus YAML declares in exit-codes so the guard <id>.outcome works there.
func (c *CLI) runStep(ctx context.Context, target, input string, explicitInput bool, outcomes []string) int {
	mod, export, ok := strings.Cut(target, "#")
	if !ok || mod == "" || export == "" {
		return c.usageErr("run-step target must be <module.js>#<export>")
	}
	if export != "default" && !identRe.MatchString(export) {
		return c.usageErr(fmt.Sprintf("bad export name %q", export))
	}
	if filepath.Ext(mod) != ".js" {
		return c.usageErr("run-step module must be a .js file")
	}
	if !json.Valid([]byte(input)) {
		return c.usageErr("--input is not valid JSON")
	}
	if _, err := os.Stat(mod); err != nil {
		fmt.Fprintln(c.Stderr, err)
		return 1
	}
	// A virtual entry beside the module, so the normal loader rules
	// (relative only, confined to the module root) apply to it and its imports.
	imp := "{ " + export + " as __step }"
	if export == "default" {
		imp = "__step"
	}
	ctxJSON, err := stepContextFromEnv(input, explicitInput)
	if err != nil {
		return c.usageErr(err.Error())
	}
	src := fmt.Sprintf("import %s from %q\nexport default async function () { return await __step(%s) }\n",
		imp, "./"+filepath.Base(mod), ctxJSON)
	entry := filepath.Join(filepath.Dir(mod), "__run_step__.js")
	r := c.Engine.Run(ctx, src, entry)
	if !r.OK {
		for _, d := range r.Diagnostics {
			fmt.Fprintln(c.Stderr, d)
		}
		if len(r.Diagnostics) == 0 && r.Error != "" {
			fmt.Fprintln(c.Stderr, r.Error)
		}
		return 1
	}
	if c.writeJSON(r.Value) != 0 {
		return 1
	}
	if len(outcomes) > 0 {
		m, _ := r.Value.(map[string]any)
		got, _ := m["outcome"].(string)
		for i, o := range outcomes {
			if got == o {
				return js.OutcomeExitBase + i
			}
		}
		fmt.Fprintf(c.Stderr, "run-step: step must return {outcome: one of %s}, got %q\n", strings.Join(outcomes, ", "), got)
		return 1
	}
	return 0
}

func countErrors(ds []Diagnostic) int {
	n := 0
	for _, d := range ds {
		if d.Severity == "error" {
			n++
		}
	}
	return n
}

func (c *CLI) usageErr(msg string) int {
	fmt.Fprintf(c.Stderr, "%s: %s\nrun `%s help` for usage\n", c.Name, msg, c.Name)
	return 2
}

func (c *CLI) readProgram(arg string) (src, file string, err error) {
	if arg == "-" {
		b, err := io.ReadAll(c.Stdin)
		return string(b), "<stdin>", err
	}
	b, err := os.ReadFile(arg)
	return string(b), arg, err
}

func (c *CLI) writeJSON(v any) int {
	enc := json.NewEncoder(c.Stdout)
	enc.SetIndent("", "  ")
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		fmt.Fprintln(c.Stderr, err)
		return 1
	}
	return 0
}

func (c *CLI) report(r Result, jsonOut bool) int {
	if jsonOut {
		c.writeJSON(r)
	} else if r.OK {
		if s, ok := r.Value.(string); ok {
			fmt.Fprintln(c.Stdout, s)
		} else if r.Value != nil {
			c.writeJSON(r.Value)
		}
	} else {
		for _, d := range r.Diagnostics {
			fmt.Fprintln(c.Stderr, d)
		}
		if len(r.Diagnostics) == 0 && r.Error != "" {
			fmt.Fprintln(c.Stderr, r.Error)
		}
	}
	switch {
	case r.OK:
		return 0
	case r.NotRun:
		return 2
	}
	return 1
}

func (c *CLI) operations(jsonOut bool) int {
	ops := c.Engine.Registry.Operations()
	if jsonOut {
		type row struct {
			Name        string     `json:"name"`
			Description string     `json:"description"`
			SideEffects SideEffect `json:"side_effects"`
		}
		rows := make([]row, len(ops))
		for i, op := range ops {
			rows[i] = row{op.Name, op.Description, op.SideEffects}
		}
		return c.writeJSON(rows)
	}
	tw := tabwriter.NewWriter(c.Stdout, 0, 0, 2, ' ', 0)
	for _, op := range ops {
		fmt.Fprintf(tw, "%s\t%s\t%s\n", op.Name, op.SideEffects, op.Description)
	}
	tw.Flush()
	return 0
}

func (c *CLI) describe(name string, jsonOut bool) int {
	op, ok := c.Engine.Registry.Lookup(name)
	if !ok {
		msg := fmt.Sprintf("unknown operation %q", name)
		if s := c.Engine.suggest(name); s != "" {
			msg += fmt.Sprintf(" (did you mean %q?)", s)
		}
		return c.usageErr(msg)
	}
	if jsonOut {
		return c.writeJSON(op)
	}
	fmt.Fprint(c.Stdout, describeText(op))
	return 0
}

func describeText(op *Operation) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s\n  %s\n", op.Name, op.Description)
	fmt.Fprintf(&b, "  side effects: %s   idempotent: %v", op.SideEffects, op.Idempotent)
	if op.Authorization != "" {
		fmt.Fprintf(&b, "   authorization: %s", op.Authorization)
	}
	b.WriteString("\n")
	if len(op.InputSchema) > 0 {
		fmt.Fprintf(&b, "  input:  %s\n", compact(op.InputSchema))
	}
	if len(op.OutputSchema) > 0 {
		fmt.Fprintf(&b, "  output: %s\n", compact(op.OutputSchema))
	}
	for _, ex := range op.Examples {
		args, _ := json.Marshal(ex.Args)
		fmt.Fprintf(&b, "  example: await call(%q, %s)", op.Name, args)
		if ex.Description != "" {
			fmt.Fprintf(&b, "  // %s", ex.Description)
		}
		b.WriteString("\n")
	}
	return b.String()
}

func compact(raw json.RawMessage) string {
	var v any
	if json.Unmarshal(raw, &v) != nil {
		return string(raw)
	}
	b, _ := json.Marshal(v)
	return string(b)
}

// stepContextFromEnv builds the JSON for {input, steps, item, index}. A plan
// exported to WFNexus passes them as environment variables (see
// js.StepEnvName): CODEMODE_INPUT, CODEMODE_STEP_IDS (id=VAR,...),
// CODEMODE_STEP_<ID> (that step's printed result), CODEMODE_ITEM and
// CODEMODE_INDEX. Values that are not JSON are passed as strings.
func stepContextFromEnv(input string, explicit bool) (string, error) {
	asJSON := func(s string) json.RawMessage {
		s = strings.TrimSpace(s)
		if s != "" && json.Valid([]byte(s)) {
			return json.RawMessage(s)
		}
		b, _ := json.Marshal(s)
		return b
	}
	ctx := map[string]any{"input": json.RawMessage(input), "steps": map[string]json.RawMessage{}}
	if !explicit {
		if v, ok := os.LookupEnv("CODEMODE_INPUT"); ok && strings.TrimSpace(v) != "" && strings.TrimSpace(v) != "<no value>" {
			if !json.Valid([]byte(v)) {
				return "", fmt.Errorf("CODEMODE_INPUT is not valid JSON")
			}
			ctx["input"] = json.RawMessage(v)
		}
	}
	steps := map[string]json.RawMessage{}
	if ids := os.Getenv("CODEMODE_STEP_IDS"); ids != "" {
		for _, pair := range strings.Split(ids, ",") {
			id, name, ok := strings.Cut(pair, "=")
			if !ok {
				continue
			}
			if v, ok := os.LookupEnv(name); ok && strings.TrimSpace(v) != "" { // a step that never ran renders empty
				steps[id] = asJSON(v)
			}
		}
	}
	ctx["steps"] = steps
	if v, ok := os.LookupEnv("CODEMODE_ITEM"); ok {
		ctx["item"] = asJSON(v)
	}
	if v, ok := os.LookupEnv("CODEMODE_INDEX"); ok {
		if n, err := strconv.Atoi(strings.TrimSpace(v)); err == nil {
			ctx["index"] = n
		}
	}
	b, err := json.Marshal(ctx)
	return string(b), err
}
