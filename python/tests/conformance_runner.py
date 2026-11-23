"""Runs ``../conformance/cases/*.json`` through the Python API.

Port of ``conformance/run-js.mjs``. Outcomes per case:

* ``pass``        - behaves as the fixture says
* ``fail``        - does not (a genuine failure unless listed in KNOWN_ISSUES)
* ``deviation``   - ``known_deviation`` case that fails here too (Go engine bug, issues #1-#5)
* ``fixed``       - ``known_deviation`` case that passes anyway
* ``unsupported`` - needs a feature the Python wrapper cannot express (reason given)

Run it directly for a report: ``python tests/conformance_runner.py [-v]``.
"""

from __future__ import annotations

import json
import re
import sys
import time
from collections import Counter
from pathlib import Path
from typing import Any, Dict, List, Optional, Tuple

from clicodemode import CodeMode, Limits, Operation, Policy, UsageError

CASES_DIR = Path(__file__).resolve().parents[2] / "conformance" / "cases"

# Real failures that are Go engine bugs not (yet) flagged known_deviation in the
# fixtures. Never weaken a fixture: record it here with the evidence.
KNOWN_ISSUES: Dict[str, str] = {}

TALLY: Counter = Counter()
DETAILS: Dict[str, List[Tuple[str, str]]] = {"unsupported": [], "deviation": [], "fixed": [], "fail": []}


class Unsupported(Exception):
    pass


def load_cases() -> List[Dict[str, Any]]:
    cases: List[Dict[str, Any]] = []
    seen = set()
    for f in sorted(CASES_DIR.glob("*.json")):
        for c in json.loads(f.read_text(encoding="utf-8")):
            assert c["id"] not in seen, f"duplicate case id {c['id']}"
            seen.add(c["id"])
            cases.append(c)
    return cases


def canon(v: Any) -> Any:
    if isinstance(v, list):
        return [canon(x) for x in v]
    if isinstance(v, dict):
        return {k: canon(v[k]) for k in sorted(v)}
    return v


class Tracker:
    def __init__(self) -> None:
        self.calls: Dict[str, int] = {}
        self.in_flight = 0
        self.peak = 0


def _stub(o: Dict[str, Any], t: Tracker) -> Any:
    if o.get("blocks_until_cancel"):
        raise Unsupported(
            "operation that never settles: Python handlers are synchronous and run on the caller's "
            "thread, so a blocked handler cannot be pre-empted by the run timeout"
        )

    def handler(args: Dict[str, Any]) -> Any:
        t.calls[o["name"]] = t.calls.get(o["name"], 0) + 1
        t.in_flight += 1
        t.peak = max(t.peak, t.in_flight)
        try:
            if o.get("panics"):
                raise RuntimeError("stub panic")
            if o.get("delay_ms"):
                time.sleep(o["delay_ms"] / 1000)
            if o.get("throws"):
                raise RuntimeError(o["throws"])
            return o.get("returns")
        finally:
            t.in_flight -= 1

    return handler


def _operation(o: Dict[str, Any]) -> Operation:
    return Operation(
        name=o["name"],
        description=o.get("description", ""),
        input_schema=o.get("input_schema"),
        side_effects=o.get("side_effects", "local"),
        idempotent=bool(o.get("idempotent", False)),
    )


def _register(cm: CodeMode, ops: List[Dict[str, Any]], t: Tracker) -> None:
    for o in ops:
        cm.register(_operation(o), None if o.get("no_handler") else _stub(o, t))  # type: ignore[arg-type]


def _build(case: Dict[str, Any], ops: List[Dict[str, Any]], t: Tracker) -> CodeMode:
    lim = case.get("limits") or {}
    pol = case.get("policy") or {}
    deny = set(pol.get("deny_side_effects") or [])
    unknown = set(pol) - {"deny_side_effects"}
    if unknown:
        raise Unsupported(f"policy option(s) {sorted(unknown)} have no Python mapping")
    cm = CodeMode(
        "conformance 1.0",
        limits=Limits(
            max_steps=lim.get("max_steps", 0),
            max_call_depth=lim.get("max_call_depth", 0),
            max_concurrency=lim.get("max_concurrency", 0),
            timeout_ms=lim.get("timeout_ms", 0),
        ),
        policy=Policy(allow=(lambda op: op.side_effects not in deny) if deny else None),
    )
    _register(cm, ops, t)
    return cm


_NOT_RUN = re.compile(r"nothing was run| error: syntax error")


def run_case(c: Dict[str, Any]) -> None:
    """Raise AssertionError on mismatch, Unsupported when the wrapper cannot express the case."""
    mode = c.get("mode", "run")
    x = c["expect"]
    t = Tracker()

    if mode == "register":
        cm = CodeMode("conformance 1.0")
        failed_on: Optional[str] = None
        for o in c.get("ops", []):
            try:
                cm.register(_operation(o), None if o.get("no_handler") else _stub(o, t))  # type: ignore[arg-type]
            except UsageError:
                failed_on = o["name"]
                break
        if "register_error" in x:
            assert failed_on is not None, f"registration must fail for {x['register_error']['op']!r}"
            assert failed_on == x["register_error"]["op"], f"registration failed on {failed_on!r}"
        else:
            assert failed_on is None, f"unexpected registration error on {failed_on}"
        return

    if mode == "fingerprint":
        fps = []
        for key in ("ops", "ops_b"):
            cm = CodeMode("conformance 1.0")
            _register(cm, c.get(key) or [], t)
            fps.append(cm.capabilities().fingerprint)
        for fp in fps:
            assert re.fullmatch(r"sha256:[0-9a-f]{32}", fp), f"fingerprint format: {fp}"
        assert (fps[0] == fps[1]) == (x["fingerprint"] == "equal"), f"fingerprints {fps}, want {x['fingerprint']}"
        return

    cm = _build(c, c.get("ops") or [], t)
    file = c.get("file", "program.js")
    res = None
    program_ops: Optional[List[str]] = None
    if mode == "check":
        diags = cm.check(c["program"], file)
        if "operations" in x:
            program_ops = cm.compile(c["program"], file).operations
    elif mode == "eval":
        res = cm.eval(c["program"], input=c.get("input"))
        diags = res.diagnostics
    else:
        res = cm.run(c["program"], file=file, input=c.get("input"))
        diags = res.diagnostics

    for name, n in (x.get("calls") or {}).items():
        got = t.calls.get(name, 0)
        assert got == n, f"op {name} called {got} times, want {n}"
    if "stdout" in x:
        assert res is not None and res.stdout == x["stdout"], f"stdout {getattr(res, 'stdout', None)!r}, want {x['stdout']!r}"
    if x.get("max_in_flight_at_most"):
        assert t.peak <= x["max_in_flight_at_most"], f"peak in-flight {t.peak}"
    if mode == "check" and "operations" in x:
        assert canon(program_ops or []) == canon(x["operations"]), f"operations {program_ops}, want {x['operations']}"
    if "exit" in x:
        if mode == "check":
            exit_class = 2 if any(d.severity == "error" for d in diags) else 0
        else:
            assert res is not None
            exit_class = 0 if res.ok else (2 if _NOT_RUN.search(res.error) else 1)
        assert exit_class == x["exit"], f"exit class {exit_class}, want {x['exit']} ({res or diags})"

    if "diagnostics" in x:
        if mode != "check":
            assert res is not None and not res.ok and _NOT_RUN.search(res.error), f"expected diagnostics and nothing run, got {res}"
        want = x["diagnostics"]
        assert len(diags) == len(want), f"got {len(diags)} diagnostics {diags}, want {len(want)}"
        for i, (w, g) in enumerate(zip(want, diags)):
            if w.get("file"):
                assert g.file == w["file"], f"diag {i} file {g.file}"
            if w.get("severity"):
                assert g.severity == w["severity"], f"diag {i} severity {g.severity}"
            if w.get("line"):
                assert g.line == w["line"], f"diag {i} line {g.line}:{g.col}, want {w['line']}:{w.get('col')} ({g.message})"
            if w.get("col"):
                assert g.col == w["col"], f"diag {i} col {g.line}:{g.col}, want {w.get('line')}:{w['col']} ({g.message})"
            assert w.get("messageContains", "") in g.message, f"diag {i} message {g.message!r} lacks {w.get('messageContains')!r}"
            assert w.get("hintContains", "") in g.hint, f"diag {i} hint {g.hint!r} lacks {w.get('hintContains')!r}"
    elif "error" in x:
        assert res is not None and not res.ok, f"expected failure containing {x['error'].get('messageContains')!r}, got {res and res.value!r}"
        assert x["error"].get("messageContains", "") in res.error, f"error {res.error!r} lacks {x['error'].get('messageContains')!r}"
        if x["error"].get("line"):
            assert diags and diags[-1].line == x["error"]["line"], f"error line {diags[-1].line if diags else None}, want {x['error']['line']}"
    elif "result" in x:
        assert res is not None and res.ok, f"run failed: {res and res.error} {res and res.diagnostics}"
        assert canon(res.value) == canon(x["result"]), f"result {res.value!r}, want {x['result']!r}"


def classify(c: Dict[str, Any]) -> Tuple[str, str]:
    """Run one case and return (outcome, message)."""
    try:
        run_case(c)
    except Unsupported as e:
        return "unsupported", str(e)
    except AssertionError as e:
        msg = str(e)
    except Exception as e:  # noqa: BLE001 - report, do not crash the sweep
        msg = f"{type(e).__name__}: {e}"
    else:
        return ("fixed", "passes although the Go engine deviates") if c.get("known_deviation") else ("pass", "")
    if c.get("known_deviation"):
        return "deviation", msg
    if c["id"] in KNOWN_ISSUES:
        return "known_issue", f"{KNOWN_ISSUES[c['id']]} -- {msg}"
    return "fail", msg


def record(case_id: str, outcome: str, msg: str) -> None:
    TALLY[outcome] += 1
    if outcome in DETAILS:
        DETAILS[outcome].append((case_id, msg))


def summary() -> str:
    t = TALLY
    return (
        f"conformance via Python: {t['pass']} passed, {t['fail']} failed, "
        f"{t['deviation'] + t['fixed']} skipped (known Go deviations: {t['deviation']} still deviate, {t['fixed']} pass anyway), "
        f"{t['unsupported']} unsupported, {t['known_issue']} known issues, "
        f"{sum(t.values())} total"
    )


def main(argv: List[str]) -> int:
    verbose = "-v" in argv
    for c in load_cases():
        outcome, msg = classify(c)
        record(c["id"], outcome, msg)
        if verbose or outcome not in ("pass",):
            print(f"{outcome.upper():11} {c['id']}: {msg}")
    print("\n" + summary())
    return 1 if TALLY["fail"] else 0


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
