"""Core behaviour: handlers, results, policy, limits, cancellation, isolation."""

from __future__ import annotations

import threading
import time
from dataclasses import dataclass

import pytest

from clicodemode import CodeMode, EngineError, Limits, Operation, Policy, UsageError
from clicodemode._guest import Guest


def make(**kw) -> CodeMode:
    return CodeMode("1.0.0", **kw)


# ---------------------------------------------------------------- basics


def test_handler_result_exact(cm: CodeMode) -> None:
    @cm.operation("math.add", side_effects="none", idempotent=True)
    def add(args):
        return args["a"] + args["b"]

    r = cm.run('await call("math.add", {a: 2, b: 40})')
    assert r.ok and r.value == 42 and r.error == "" and r.diagnostics == []


def test_ops_proxy_and_export_default(cm: CodeMode) -> None:
    cm.register(Operation("health.check", "h", side_effects="none"), lambda a: {"ok": True, "n": [1, 2]})
    r = cm.run("export default async () => await ops.health.check({})")
    assert r.ok and r.value == {"ok": True, "n": [1, 2]}


def test_handler_without_args_gets_empty_dict(cm: CodeMode) -> None:
    seen = []
    cm.register(Operation("a.b"), lambda a: seen.append(a) or "x")
    assert cm.run('await call("a.b")').value == "x"
    assert seen == [{}]


def test_multiple_ops_compose(cm: CodeMode) -> None:
    store = {}
    cm.register(Operation("kv.set"), lambda a: store.__setitem__(a["k"], a["v"]))
    cm.register(Operation("kv.get", side_effects="none"), lambda a: store.get(a["k"]))
    r = cm.run(
        """
        await call("kv.set", {k: "x", v: 10});
        await call("kv.set", {k: "y", v: 32});
        const a = await call("kv.get", {k: "x"});
        const b = await call("kv.get", {k: "y"});
        a + b
        """
    )
    assert r.value == 42 and store == {"x": 10, "y": 32}


def test_promise_all_parallel_calls_all_answered(cm: CodeMode) -> None:
    order = []

    def square(a):
        order.append(a["n"])
        return a["n"] ** 2

    cm.register(Operation("m.sq", side_effects="none"), square)
    r = cm.run("await Promise.all([1,2,3,4,5,6,7,8].map(n => call('m.sq', {n})))")
    assert r.ok and r.value == [1, 4, 9, 16, 25, 36, 49, 64]  # results match their calls
    assert sorted(order) == list(range(1, 9))  # every call answered exactly once


def test_promise_all_mixed_ops(cm: CodeMode) -> None:
    cm.register(Operation("a.one"), lambda a: 1)
    cm.register(Operation("b.two"), lambda a: "two")
    r = cm.run("await Promise.all([call('a.one'), call('b.two'), call('a.one')])")
    assert r.value == [1, "two", 1]


def test_stdout_captured(cm: CodeMode) -> None:
    r = cm.run('print("hi"); console.log("there", 1); 3')
    assert r.value == 3 and r.stdout == "hi\nthere 1\n"


def test_eval(cm: CodeMode) -> None:
    assert cm.eval("1 + 2 * 3").value == 7
    assert cm.eval("input.x * 2", input={"x": 21}).value == 42


def test_sleep_works(cm: CodeMode) -> None:
    t = time.perf_counter()
    assert cm.run("await sleep(30); 'done'").value == "done"
    assert time.perf_counter() - t >= 0.03


# ---------------------------------------------------------------- errors


def test_handler_exception_is_catchable_and_message_preserved(cm: CodeMode) -> None:
    def boom(a):
        raise ValueError("kaboom é 日本")

    cm.register(Operation("x.boom"), boom)
    r = cm.run('let m = ""; try { await call("x.boom", {}) } catch (e) { m = e.message } m')
    assert r.ok and "kaboom é 日本" in r.value


def test_uncaught_handler_exception_fails_the_run_not_python(cm: CodeMode) -> None:
    def boom(a):
        raise KeyError("missing-thing")

    cm.register(Operation("x.boom"), boom)
    r = cm.run('await call("x.boom", {})')
    assert not r.ok and "missing-thing" in r.error


def test_exception_without_message_uses_type_name(cm: CodeMode) -> None:
    def boom(a):
        raise RuntimeError()

    cm.register(Operation("x.boom"), boom)
    assert "RuntimeError" in cm.run('await call("x.boom", {})').error


def test_engine_usable_after_handler_exception(cm: CodeMode) -> None:
    cm.register(Operation("x.boom"), lambda a: 1 / 0)
    cm.register(Operation("x.ok"), lambda a: "fine")
    assert not cm.run('await call("x.boom")').ok
    assert cm.run('await call("x.ok")').value == "fine"
    assert cm.guests_started == 1  # no restart was needed


def test_syntax_error_is_a_result_with_diagnostics(cm: CodeMode) -> None:
    r = cm.run("const = 1", file="bad.js")
    assert not r.ok and r.nothing_ran
    assert r.diagnostics[0].file == "bad.js" and r.diagnostics[0].line == 1


def test_unsupported_construct_names_it(cm: CodeMode) -> None:
    r = cm.run("class A {}")
    assert not r.ok and r.nothing_ran and "class" in r.diagnostics[0].message


def test_exec_and_shell_denied_under_wasm(cm: CodeMode) -> None:
    # literal commands are rejected statically, dynamic ones at run time
    for src in ('await shell("echo hi")', 'await exec(["echo", "hi"])', 'const c = input.c; await shell(c)'):
        r = cm.run(src, input={"c": "echo hi"})
        text = r.error + " ".join(d.message for d in r.diagnostics)
        assert not r.ok and "not permitted" in text, (src, text)


def test_imports_unavailable(cm: CodeMode) -> None:
    r = cm.run('import x from "./other.js"\nx')
    assert not r.ok


# ---------------------------------------------------------------- policy


def test_policy_denial_never_executes_handler_and_is_catchable() -> None:
    ran = []
    cm = make(policy=Policy(allow=lambda op: op.side_effects != "external"))
    cm.register(Operation("w.push", side_effects="external"), lambda a: ran.append(1))
    cm.register(Operation("r.read", side_effects="none"), lambda a: "read")
    r = cm.run(
        """
        let msg = ""
        try { await call("w.push", {}) } catch (e) { msg = e.message }
        [msg, await call("r.read", {})]
        """
    )
    assert r.ok and "denied by policy" in r.value[0] and "w.push" in r.value[0]
    assert r.value[1] == "read" and ran == []


def test_policy_uncaught_denial_fails_run() -> None:
    ran = []
    cm = make(policy=Policy(allow=lambda op: False))
    cm.register(Operation("a.b"), lambda a: ran.append(1))
    r = cm.run('await call("a.b")')
    assert not r.ok and "denied by policy" in r.error and ran == []


def test_policy_consulted_on_every_call_and_receives_operation() -> None:
    seen = []

    def allow(op: Operation):
        seen.append(op.name)
        return op.name != "b.no"

    cm = make(policy=Policy(allow=allow))
    cm.register(Operation("a.yes"), lambda a: 1)
    cm.register(Operation("b.no"), lambda a: 2)
    cm.run('await call("a.yes"); await call("a.yes"); try { await call("b.no") } catch (e) {}')
    assert seen == ["a.yes", "a.yes", "b.no"]


def test_policy_raising_denies_with_reason() -> None:
    def allow(op: Operation):
        raise PermissionError("not for you")

    cm = make(policy=Policy(allow=allow))
    cm.register(Operation("a.b"), lambda a: 1)
    r = cm.run('await call("a.b")')
    assert not r.ok and "not for you" in r.error


# ---------------------------------------------------------------- limits, timeout, cancel


def test_timeout_stops_infinite_loop_and_engine_stays_usable() -> None:
    cm = make()
    assert cm.run("1").value == 1
    t = time.perf_counter()
    r = cm.run("let i = 0; while (true) { i++ }", timeout=0.3)
    took = time.perf_counter() - t
    assert not r.ok and r.error.startswith("timeout") and took < 3
    assert cm.run("40 + 2").value == 42  # reusable
    assert cm.guests_started == 2  # restarted once, documented
    cm.close()


def test_timeout_during_async_sleep() -> None:
    cm = make()
    t = time.perf_counter()
    r = cm.run("await sleep(60000)", timeout=0.2)
    assert not r.ok and "timeout" in r.error and time.perf_counter() - t < 3
    assert cm.eval("1").value == 1
    cm.close()


def test_cancel_with_event_stops_infinite_loop() -> None:
    cm = make()
    ev = threading.Event()
    threading.Timer(0.2, ev.set).start()
    t = time.perf_counter()
    r = cm.run("while (true) {}", cancel=ev)
    assert not r.ok and r.error.startswith("cancelled") and time.perf_counter() - t < 3
    assert cm.run("'alive'").value == "alive"
    cm.close()


def test_cancel_method_from_another_thread() -> None:
    cm = make()
    threading.Timer(0.2, cm.cancel).start()
    r = cm.run("while (true) {}")
    assert not r.ok and "cancelled" in r.error
    assert cm.run("2 * 21").value == 42
    cm.close()


def test_cancel_when_idle_does_not_poison_next_run(cm: CodeMode) -> None:
    cm.cancel()
    assert cm.run("1 + 1").value == 2


def test_cancel_applies_after_running_handler_returns() -> None:
    cm = make()
    ev = threading.Event()

    def slow(a):
        ev.set()  # cancel while the handler is executing
        time.sleep(0.2)
        return "late"

    cm.register(Operation("s.slow"), slow)
    r = cm.run('const v = await call("s.slow", {}); await sleep(0); "after"', cancel=ev)
    assert not r.ok and "cancelled" in r.error
    cm.close()


def test_limits_max_steps_enforced() -> None:
    cm = make(limits=Limits(max_steps=10_000))
    r = cm.run("let i = 0; while (true) { i++ }")
    assert not r.ok and "step budget" in r.error
    assert cm.run("1").value == 1 and cm.guests_started == 1  # engine-side limit: no restart
    cm.close()


def test_limits_call_depth_enforced() -> None:
    cm = make(limits=Limits(max_call_depth=20))
    r = cm.run("function f(n) { return n == 0 ? 0 : 1 + f(n - 1) }; f(100)")
    assert not r.ok and "call depth" in r.error
    cm.close()


# ---------------------------------------------------------------- data conversion


def test_input_passing(cm: CodeMode) -> None:
    r = cm.run("input.items.map(x => x.n * input.k)", input={"k": 3, "items": [{"n": 1}, {"n": 2}]})
    assert r.value == [3, 6]


def test_no_input_is_null(cm: CodeMode) -> None:
    assert cm.run("input === null || input === undefined").value is True


def test_none_null_both_ways(cm: CodeMode) -> None:
    got = []
    cm.register(Operation("n.echo"), lambda a: got.append(a) or {"back": None, "list": [None, 1]})
    r = cm.run('await call("n.echo", {x: null, y: [null]})')
    assert got == [{"x": None, "y": [None]}]
    assert r.value == {"back": None, "list": [None, 1]}


def test_unicode_both_ways(cm: CodeMode) -> None:
    text = "héllo 日本語 \U0001f600   line\nbreak \"q\" \\"
    cm.register(Operation("u.echo"), lambda a: {"s": a["s"], "n": len(a["s"])})
    r = cm.run('await call("u.echo", {s: input.s})', input={"s": text})
    assert r.value["s"] == text
    assert cm.eval("input.s", input={"s": text}).value == text


def test_large_payload_through_handler_and_input(cm: CodeMode) -> None:
    big = "x" * 1_500_000
    rows = [{"i": i, "s": f"row-{i}"} for i in range(20_000)]
    cm.register(Operation("big.echo", side_effects="none"), lambda a: {"len": len(a["s"]), "rows": a["rows"]})
    r = cm.run('const r = await call("big.echo", {s: input.s, rows: input.rows}); [r.len, r.rows.length, r.rows[19999].s]', input={"s": big, "rows": rows})
    assert r.ok, r.error
    assert r.value == [1_500_000, 20_000, "row-19999"]


def test_non_json_result_gives_clear_error(cm: CodeMode) -> None:
    @dataclass
    class Thing:
        x: int

    cm.register(Operation("bad.obj"), lambda a: Thing(1))
    cm.register(Operation("bad.set"), lambda a: {1, 2})
    cm.register(Operation("bad.nan"), lambda a: float("nan"))
    cm.register(Operation("bad.ok"), lambda a: "fine")
    for name in ("bad.obj", "bad.set", "bad.nan"):
        r = cm.run(f'let m = ""; try {{ await call("{name}") }} catch (e) {{ m = e.message }} m')
        assert r.ok and "not JSON-serialisable" in r.value and name in r.value, (name, r)
    assert cm.run('await call("bad.ok")').value == "fine"


def test_non_json_input_raises_in_python(cm: CodeMode) -> None:
    with pytest.raises((TypeError, ValueError)):
        cm.run("1", input={"x": object()})
    assert cm.run("1").value == 1  # engine intact


# ---------------------------------------------------------------- tooling surface


def test_check_diagnostics_file_line_col(cm: CodeMode) -> None:
    cm.register(Operation("health.check", side_effects="none"), lambda a: 1)
    d = cm.check('const a = 1\nawait call("health.chek", {})', file="prog.js")
    assert len(d) == 1
    assert (d[0].file, d[0].line, d[0].col, d[0].severity) == ("prog.js", 2, 12, "error")
    assert "unknown operation" in d[0].message and "health.check" in d[0].hint
    assert str(d[0]).startswith("prog.js:2:12: error:")
    assert cm.check('await call("health.check", {})') == []


def test_compile_plan(cm: CodeMode) -> None:
    cm.register(Operation("a.b"), lambda a: 1)
    p = cm.compile('await call("a.b", {})')
    assert p.operations == ["a.b"] and p.hash and p.cache_key and p.diagnostics == []
    assert cm.compile('await call("a.b", {})').hash == p.hash
    assert cm.compile('await call("a.b", {x: 1})').hash != p.hash


def test_capabilities_fingerprint_changes_with_ops() -> None:
    a = make()
    a.register(Operation("a.one"), lambda x: 1)
    fp1 = a.capabilities().fingerprint
    a.register(Operation("a.two"), lambda x: 1)  # registering restarts the engine
    fp2 = a.capabilities().fingerprint
    b = make()
    b.register(Operation("a.one"), lambda x: 1)
    assert fp1 != fp2 and fp1 == b.capabilities().fingerprint
    caps = a.capabilities()
    assert [o.name for o in caps.operations] == ["a.one", "a.two"]
    assert caps.version == "1.0.0" and caps.spec
    a.close(), b.close()


def test_docs_and_skill_non_empty(cm: CodeMode) -> None:
    cm.register(Operation("health.check", "Report health.", side_effects="none"), lambda a: 1)
    assert "health.check" in cm.docs() and len(cm.docs()) > 500
    sk = cm.skill()
    assert sk.startswith("---") or "health.check" in sk
    assert "health.check" in sk


def test_operations_and_describe(cm: CodeMode) -> None:
    cm.register(Operation("z.last", "Z", {"type": "object"}, side_effects="none", idempotent=True), lambda a: 1)
    cm.register(Operation("a.first", "A"), lambda a: 1)
    assert [o.name for o in cm.operations()] == ["a.first", "z.last"]
    d = cm.describe("z.last")
    assert d.description == "Z" and d.idempotent and d.side_effects == "none" and d.input_schema == {"type": "object"}
    with pytest.raises(EngineError):
        cm.describe("nope.nope")


def test_engine_version(cm: CodeMode) -> None:
    assert cm.engine_version().startswith("clicodemode ")


# ---------------------------------------------------------------- registration & lifecycle


def test_invalid_and_duplicate_registration(cm: CodeMode) -> None:
    cm.register(Operation("a.b"), lambda a: 1)
    for bad in ("", "A.b", "a..b", "1a", "a b", ".a"):
        with pytest.raises(UsageError):
            cm.register(Operation(bad), lambda a: 1)
    with pytest.raises(UsageError):
        cm.register(Operation("a.b"), lambda a: 1)
    with pytest.raises(UsageError):
        cm.register(Operation("c.d", side_effects="sometimes"), lambda a: 1)
    with pytest.raises(UsageError):
        cm.register(Operation("c.d"), None)  # type: ignore[arg-type]


def test_register_after_start_restarts_with_new_contract(cm: CodeMode) -> None:
    cm.register(Operation("a.one"), lambda a: 1)
    assert cm.run('await call("a.one")').value == 1
    cm.register(Operation("a.two"), lambda a: 2)
    assert cm.run('(await call("a.one")) + (await call("a.two"))').value == 3


def test_operation_decorator_uses_docstring_and_returns_function(cm: CodeMode) -> None:
    @cm.operation("doc.op", side_effects="none")
    def fn(args):
        """First paragraph.

        More detail that is not the description.
        """
        return 5

    assert fn({}) == 5
    assert cm.describe("doc.op").description == "First paragraph."


def test_two_engines_are_isolated() -> None:
    a, b = make(), make()
    a.register(Operation("only.a"), lambda x: "A")
    b.register(Operation("only.b"), lambda x: "B")
    assert a.run('await call("only.a")').value == "A"
    assert b.run('await call("only.b")').value == "B"
    assert not a.run('await call("only.b")').ok and not b.run('await call("only.a")').ok
    a.run("globalThis.leak = 1") if False else None
    assert a.capabilities().fingerprint != b.capabilities().fingerprint
    a.close(), b.close()


def test_state_does_not_leak_between_runs(cm: CodeMode) -> None:
    cm.run("var leaked = 41")
    r = cm.run("typeof leaked")
    assert r.value == "undefined"


def test_close_then_reuse_and_context_manager() -> None:
    with make() as cm:
        assert cm.run("1").value == 1
        cm.close()
        assert cm.run("2").value == 2
    assert cm._guest is None


def test_thread_safety_one_engine_many_threads() -> None:
    cm = make()
    cm.register(Operation("t.id", side_effects="none"), lambda a: a["n"])
    out, errs = {}, []

    def work(n: int) -> None:
        try:
            out[n] = cm.run('await call("t.id", {n: input.n})', input={"n": n}).value
        except Exception as e:  # pragma: no cover
            errs.append(e)

    ts = [threading.Thread(target=work, args=(i,)) for i in range(8)]
    [t.start() for t in ts]
    [t.join() for t in ts]
    assert not errs and out == {i: i for i in range(8)}
    cm.close()


def test_handler_cannot_reenter_its_engine() -> None:
    cm = make()
    cm.register(Operation("re.enter"), lambda a: cm.run("1").value)
    r = cm.run('await call("re.enter")')
    assert not r.ok and "cannot re-enter" in r.error
    cm.close()


def test_engines_in_parallel_threads_run_concurrently() -> None:
    results = []

    def work(i: int) -> None:
        with make() as e:
            e.register(Operation("p.i"), lambda a, i=i: i)
            results.append(e.run('await call("p.i")').value)

    ts = [threading.Thread(target=work, args=(i,)) for i in range(4)]
    [t.start() for t in ts]
    [t.join() for t in ts]
    assert sorted(results) == [0, 1, 2, 3]


def test_warm_module_is_reused_across_engines() -> None:
    first = Guest()  # make sure the process-wide compile cache is filled
    first.stop()
    g = Guest()
    try:
        assert g.compiled_cold is False  # deserialized, not recompiled
        assert g.load_ms < 500
    finally:
        g.stop()
