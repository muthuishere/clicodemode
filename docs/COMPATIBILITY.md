# Legacy YAML compatibility

Rule: existing AOS batch YAML and WFNexus workflow YAML keep **exactly** their
current behaviour. CodeMode adds a parallel path; it never reinterprets or
"improves" these formats. Facts below were gathered on 2026-11-22 by reading
the source and confirmed with each project's maintainers.
They are the contract adapters will be golden-tested against.

## AOS batch (module `github.com/muthuishere/aos`)

Source: `internal/groups/batch.go` (single file). **Not importable** from
outside the module (`internal/`), and it depends on `internal/cli`.

- Parsing is a hand walk of a `yaml.v3` node tree (no structs). Top level:
  a bare step list, or a map with only `steps:` and optional `subtitle:`
  (caption flags applied to every `say`). Any other key is an error.
- Step = one-key map `verb: value`; a bare scalar is `verb` with no args.
  Value: null, scalar (split on whitespace honouring quotes), list (one word
  each), or map long-form (`click: {app, target}`, `type: {app, text}`,
  `key: {app, combo}`, `screenshot: {app}`, `say: {text}`, `wait: {time}`,
  `do: {command}`; `args:` split; other keys become `--k=v`).
- Verbs: ~255 generated `<group>-<command>` from the live registry
  (`batchVerbs`), excluding `batch, serve, completion, skill, debug`; system
  groups `power, system, pkg, service, network` are only reachable via `do:`
  (warning). Short names `click, type, key, screenshot, say, wait, do` map to
  `mouse click`, `key type`, `key press`, `capture screenshot`, child-process
  `aos say`, sleep, raw argv.
- Pre-validation: everything is parsed and built before step 1. Any error:
  findings to stderr, exit 2, "N error(s); nothing was run".
- Execution: sequential, stop at first failed step (exit 1). `parallel:`
  groups are flat (no nesting, not empty); group fails if any child fails but
  all children finish. **Say-first**: say children start first; the others
  wait until speech is heard (marker file / exit / 20 s), and `key type`
  children are paced across `spoken * 0.85` seconds.
- `wait`: bare number = ms, else Go duration, negative rejected.
- `--json` output: one line per finished leaf
  `{"step","run","ok","exit","ms","at_ms","output"?}` (`step` "N" or "N.M";
  order within a parallel group not fixed), then
  `{"ms","ok","ran","steps","summary":true}`. No data flows between steps.
- Tests: `internal/groups/batch_test.go` (good/bad scripts with exact
  findings). **The run phase, JSONL output and exit codes have no tests.**

**Adapter plan.** Because batch is internal, the compatible path is for AOS to
import CodeMode (not the reverse): AOS registers one operation per batch verb
whose handler calls the same `cli.Invoke(ctx, argv, "")` the batch leaf uses,
so JS operations wrap the existing verbs and `aos batch` stays untouched.
Before any AOS change: add golden tests for the run phase (JSONL bytes, exit
codes, stop-on-first-error, say-first ordering) inside AOS, agreed with the
AOS owner. Nothing in this repo re-implements batch semantics.

## WFNexus (module `github.com/muthuishere/wfnexus/apps/api`)

Schema: `apps/api/internal/workflow/` (`workflow.go`, `jobs.go`, `loop.go`,
`start.go`, `send.go`, `inputs.go`); engine `apps/api/internal/engine/`.
**Not importable** (`internal/`); repo is release-parked, so a public `pkg/`
export would be an owner decision.

- `Guard {path, equals|exists}`: path head = longest matching step id (or
  `input.`), rest walks maps; `exists` passes when resolved == bool (present
  null counts as existing); `equals` compares `json.Marshal` bytes (types
  matter: `"true"` != `true`); unresolved path = guard fails = step skipped.
- `Retry {max_attempts >= 1, backoff_sec}`.
- `needs` anywhere makes the workflow a DAG (validated: unknown ids, self,
  cycles); ready steps run up to `max_parallel` (default 4). No `needs` =
  strictly sequential. `goal`/`consumes`/`produces` = planned mode; mixing
  with `needs` is an error.
- Loops: exactly one of `foreach|until|while`; `until/while` require
  `max_iterations` (1..999) and `parallel` <= 1, and must read their own
  output; `foreach` takes a schema-checked path or a non-empty literal list;
  bodies cannot nest loops or use needs/consumes/produces/skip_to. Output
  `{iterations, count, last}`; iteration ids `step[i]`.
- Templates: Go `text/template`; data `.Input .Steps .Item .Index ...`;
  `.Steps.a.b` rewritten to `stepval` (longest dotted id wins); missing =
  "" (strict mode errors, used by dry run). Helpers `json join list stepval
  fromJson default`.
- `run` steps output `{ok, exitCode, outcome, stdout, stderr}`;
  `exit-codes` maps codes to outcomes.
- Fixtures are inline YAML in Go tests (no `testdata/`): see
  `internal/workflow/*_test.go`, `internal/engine/*_test.go`, plus
  `workflows/*.yaml` and `templates/*/workflow.yaml`.

**Adapter plan.** CodeMode's declarative plan emits exactly this YAML subset
(steps with `needs`, `when`, `retry`, `foreach/until/while` +
`max_iterations`, `run`), and nothing WFNexus doesn't already accept.
Verification: round-trip CodeMode-emitted plans through WFNexus's own
`ParseYAML` + validation (via `wfx` CLI or a test in WFNexus, with the owner's
OK). CodeMode never executes WFNexus YAML itself.

### Verified against WFNexus (2026-11-22, read-only)

`examples/health-workflow/health-check.wfnexus.yaml` passed `workflow.ParseYAML`
on wfnexus main and a server dry run (`POST /api/dryrun`, nothing installed):
waves inspect → wait-ready → report. Verdicts:

- `until`/`while` directly on a `run` step is allowed (the step repeats,
  rows `step[i]`). `needs` on a loop step is allowed.
- Never declare `output_schema` on a run step; its output is fixed:
  `{ok, exitCode, stdout, stderr, outcome}`. **stdout JSON is not merged into
  fields**, so a guard on a JS return field can never resolve.
- Exhausting `max_iterations` fails the step.
- An undeclared non-zero exit fails the step and the run; a declared
  `exit-codes` entry finishes ok with `outcome` set.

Consequence in CodeMode: guards in exported plans may only read `<id>.ok`,
`<id>.exitCode`, `<id>.outcome` or `input.*` (`toYAML` refuses anything else
with a pointed error). A step that branches declares `outcomes: [...]` and
returns `{outcome}`; `toYAML` emits `exit-codes` (10 + index) and `run-step
--outcomes` exits with that code. The same guard works in-process, so both
runtimes agree. Dry runs do not catch unresolvable guard paths (a WFNexus
gap its owner is tracking); CodeMode catches them at export time.

### Running an exported plan in WFNexus (verified with real runs, 2026-11-22)

- Ship it as a **workflow directory**: `workflow.yaml` plus `steps/*.js`.
  Sidecar files are staged into the run workspace, which is the working
  directory, so `./steps/health.js` resolves. A single YAML file fails with
  `stat ./steps/health.js`.
- Put the CLI in a directory on the runner's step `PATH` (`~/.local/bin`
  worked).
- WFNexus gives a `run` step nothing but its `run:` text: step `env:` values are
  literal and template functions are refused. Plain value refs in `run:`
  (`{{ .Input }}`, `{{ .Steps.<id>.stdout }}`, `{{ .Item }}`) are rewritten by
  the engine into safe `${WFX_DATA_n}` expansions, so the exporter passes the
  step context as inline assignments and `run-step` rebuilds
  `{input, steps, item, index}`. A step that never ran renders empty and is
  left out of `steps`.
- Verified: exit 10 gives `outcome=ready` and the loop finishes; 5 waitings
  fail the step ("until condition not met after 5 iterations"); retry works.
