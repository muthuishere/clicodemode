package codemode

import (
	"fmt"
	"strings"
)

// Docs returns the reference an agent reads before writing a program.
func Docs(reg *Registry, name string) string {
	var b strings.Builder
	fmt.Fprintf(&b, `# Code Mode reference (%s)

Programs are small JavaScript-like modules run by a pure-Go interpreter
embedded in this CLI. There is no Node.js, no npm, no browser APIs.

## Calling operations

    const h = await call("health.check", {verbose: true})   // by name
    const h2 = await ops.health.check({verbose: true})       // same, as a namespace

Every call returns a Promise; await it. Run independent calls in parallel:

    const [a, b] = await Promise.all([call("x.one", {}), call("x.two", {})])

Arguments are checked against the operation's input schema before it runs.
Unknown operation names are reported by "check" with a did-you-mean hint.

## Result

"run" prints the default export (a function is called and awaited), or else
the value of the last expression statement. Use --json for {ok, value, error,
diagnostics, ms}.

## Language (supported subset)

const/let, numbers, strings, template literals, booleans, null/undefined,
arrays, objects, destructuring, spread, all arithmetic/comparison/logical
operators incl. ?? and ?., if/else, ternary, for, for-of, for-in, while,
do-while, break/continue, functions, arrow functions, closures, default and
rest parameters, try/catch/finally/throw, async/await, Promise.resolve/
reject/all/allSettled/race/any, .then/.catch/.finally, static
import/export of ./relative .js modules and .json data files.

Globals: call, ops, workflow, step, print, console.log, JSON, Math, Object, Array.isArray,
Array.from, String, Number, Boolean, parseInt, parseFloat, isNaN, sleep(ms),
pmap(items, fn, {concurrency}), Error(msg) (no "new").

Bounded parallel map: fn(item, index) runs with at most concurrency (default
4) calls in flight; results come back in input order; the first rejection
rejects the whole and stops starting new items:

    const infos = await pmap(names, n => call("pkg.info", {name: n}), {concurrency: 8})

Not supported (reported as errors): class, new, this, generators, switch,
delete, dynamic import(), eval, require, npm packages, regular expressions.

## Processes and streams (only if this host permits them)

    const r = await exec(["git", "status", "--short"])   // {ok, exitCode, stdout, stderr}

argv is an array, never shell-parsed; a non-zero exit is a result, not an
exception. Options: {stdin, timeoutMs, cwd}; cwd must stay inside the host's
working directory (relative paths, ../ and symlinks that escape are rejected). Programs get a minimal
environment. shell("a | b") exists only where the host authorizes a shell.

Streams are lazy pipelines; a terminal runs them and returns a Promise:

    await execStream(["kubectl", "logs", pod])
      .jsonl().filter(x => x.level === "ERROR").take(100).writeJSONL()

Sources: execStream(argv), stream(array). Transforms: lines, jsonl, csv
(first row is the header), yaml (lines form a "---"-separated document
stream; each document becomes one value, map key order kept), map, filter,
take. Terminals: toArray, count,
first, reduce(fn, init), forEach, json, writeJSONL, writeLines. take() and
errors stop early and kill the process; a non-zero exit after a full read
rejects with the stderr tail. Output is backpressured, not buffered.

## Workflows

    import { inspect, report } from "./steps/health.js"
    const w = workflow("health-check", [
      step("inspect", inspect, { retry: { attempts: 3, backoff: "2s" }, timeout: "30s" }),
      step("report", report, { needs: ["inspect"], when: { path: "Steps.inspect.ok", equals: true } }),
    ], { maxParallel: 4 })
    export default await w.run({ host: "h1" })

A step fn gets {input, steps: {id: output}, item, index}; its (awaited)
return value is the step's output. Step options: needs [ids] (a DAG; without
needs anywhere steps run in order), when guard or array of guards (AND),
retry {attempts, backoff}, timeout ("30s" or ms), foreach [items] with
parallel n, while/until guard with a REQUIRED maxIterations (1..999),
continueOnError. A guard is {path, equals} or {path, exists}; paths are
"Steps.<id>.field", "<id>.field" or "input.field" (longest step id wins);
equals compares JSON ("true" is not true); an unresolved path is false and
skips the step. Loop output is {iterations, count, last}; while/until read
their own latest output and fail if maxIterations is reached.

run(input) resolves to {ok, steps: {id: {status: done|skipped|failed,
output, error?, attempts, skipReason?}}}; a failed step skips its dependents
unless it has continueOnError. Runs are in-process and not durable.

plan() returns serializable data and toYAML() returns WFNexus workflow YAML
(steps run as "<cli> code run-step ./module.js#export"). Both need step
functions imported from a module, not inline closures.

## Limits

Runs are bounded by a step budget, call depth, concurrency of operations and
an optional timeout. Exceeding a limit stops the program; it cannot be caught.
The interpreter is not a sandbox for hostile code: operations run with this
CLI's permissions.

## Operations (%d)

`, name, len(reg.Operations()))
	for _, op := range reg.Operations() {
		b.WriteString(describeText(op))
		b.WriteString("\n")
	}
	return b.String()
}
