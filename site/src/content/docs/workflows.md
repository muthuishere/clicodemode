---
title: Workflows and WFNexus
description: "Multi-step workflows with dependencies, retries and loops, run in-process or exported as WFNexus YAML."
---

## When do you need a workflow?

Most programs are a handful of awaited calls. Reach for `workflow` when steps have dependencies, retries, timeouts, guards or bounded loops, or when the same steps should also run in [WFNexus](https://github.com/muthuishere/wfnexus).

## Define and run

From `examples/health-workflow/main.js` in the repository:

```js
import { inspect, report, poll } from "./steps/health.js"

const wf = workflow("health-check", [
  step("inspect", inspect, { retry: { attempts: 3, backoff: "2s" }, timeout: "30s" }),
  step("wait-ready", poll, {
    needs: ["inspect"],
    outcomes: ["ready", "waiting"],   // exported as WFNexus exit-codes 10/11
    until: { path: "Steps.wait-ready.outcome", equals: "ready" },
    maxIterations: 5,
  }),
  step("report", report, { needs: ["inspect", "wait-ready"] }),
])

export default async function main() {
  const run = await wf.run()
  return { ok: run.ok, yaml: wf.toYAML() }
}
```

A step function gets `{input, steps: {id: output}, item, index}`; its awaited return value is the step's output.

## Step options

| Option | Meaning |
|---|---|
| `needs: [ids]` | DAG edges. Without `needs` anywhere, steps run in order. Unknown ids, self-dependency and cycles (`a → b → a`) are rejected before running. |
| `when` | Guard `{path, equals}` or `{path, exists}`, or an array (AND). Paths: `Steps.<id>.field`, `<id>.field`, `input.field`. Unresolved = false, step skipped with a reason. |
| `retry: {attempts, backoff}` | Retries on failure. |
| `timeout` | `"30s"` or milliseconds. |
| `foreach: [items]`, `parallel: n` | Bounded fan-out. |
| `while` / `until` + `maxIterations` (1..999) | Loops; `maxIterations` is required and exhausting it fails the step. |
| `outcomes: [names]` | The step returns `{outcome}` from this list. |
| `continueOnError` | Dependents still run after this step fails. |

Workflow options: `maxParallel` (default 4).

`run(input)` resolves to `{ok, steps: {id: {status: done|skipped|failed, output, error?, attempts, skipReason?}}}`. In-process runs are not durable; for durable runs, export to WFNexus.

## Export to WFNexus

`.plan()` returns serializable data and `.toYAML()` returns WFNexus workflow YAML. Each step becomes:

```sh
<cli> code run-step ./steps/health.js#inspect
```

served by the `run-step` subcommand, which calls one export with `{input, steps: {}}` and prints its JSON result. With `--outcomes a,b` the process exits `10 + index` of the returned outcome (and never 0), which WFNexus reads as `exit-codes`.

Rules the exporter enforces:

- Step functions must be **imported from modules**; inline closures are an export error.
- Guards may read only `<id>.ok`, `<id>.exitCode`, `<id>.outcome` or `input.*`.
- Only fields WFNexus accepts are emitted.
- Step context reaches `run-step` through inline assignments in the `run:` line (`CODEMODE_INPUT`, `CODEMODE_STEP_<ID>`, `CODEMODE_STEP_IDS`, `CODEMODE_ITEM`, `CODEMODE_INDEX`), because WFNexus passes a run step only its `run:` text. An explicit `--input` wins over `CODEMODE_INPUT`.

Exported plans were validated against WFNexus's own parser and dry run, and ran for real. Ship them as a workflow directory: `workflow.yaml` plus `steps/*.js`. Details: [Compatibility](/clicodemode/reference/compatibility/).

## Existing YAML is untouched

CLI CodeMode never reinterprets existing AOS batch YAML or WFNexus YAML. It adds a parallel path.
