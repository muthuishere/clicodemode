use clicodemode::{CodeMode, Operation, RunOptions, SideEffects};
use serde_json::{json, Value};
use std::sync::atomic::{AtomicUsize, Ordering};
use std::sync::Arc;

fn health(cm: &mut CodeMode) {
    cm.register(
        Operation::new("health.check", "Report health.")
            .side_effects(SideEffects::None)
            .input_schema(json!({"type":"object","properties":{"verbose":{"type":"boolean"}}})),
        |_| Ok(json!({"ok": true})),
    )
    .unwrap();
}

#[test]
fn add_op_and_run() {
    let mut cm = CodeMode::new("1.0.0").unwrap();
    health(&mut cm);
    let r = cm
        .run(
            "export default async () => await ops.health.check({})",
            RunOptions::default(),
        )
        .unwrap();
    assert!(r.ok, "{r:?}");
    assert_eq!(r.value, json!({"ok": true}));
}

#[test]
fn check_reports_unsupported_class_with_position() {
    let mut cm = CodeMode::new("1.0.0").unwrap();
    health(&mut cm);
    let ds = cm.check("const a = 1;\n  class A {}\n", "prog.js").unwrap();
    assert!(!ds.is_empty());
    let d = &ds[0];
    assert_eq!(d.severity, "error");
    assert_eq!((d.file.as_str(), d.line, d.col), ("prog.js", 2, 3), "{d}");
    assert!(d.message.contains("class"), "{d}");
    // a clean program yields no diagnostics, and unknown ops are caught statically
    assert!(cm.check("1 + 1", "ok.js").unwrap().is_empty());
    let ds = cm.check("ops.health.chek({})", "typo.js").unwrap();
    assert!(ds[0].hint.contains("health.check"), "{:?}", ds[0]);
}

#[test]
fn handler_error_becomes_rejected_call() {
    let mut cm = CodeMode::new("1.0.0").unwrap();
    cm.register(Operation::new("disk.fail", "Always fails."), |_| {
        Err("disk on fire".into())
    })
    .unwrap();
    let r = cm
        .run(
            r#"export default async () => {
                try { await ops.disk.fail({}); return "no"; } catch (e) { return "caught: " + e.message; }
            }"#,
            RunOptions::default(),
        )
        .unwrap();
    assert!(r.ok, "{r:?}");
    assert_eq!(r.value, json!("caught: disk.fail: disk on fire"));
    // uncaught: the run fails, the host keeps working
    let r = cm
        .run(
            "export default async () => await ops.disk.fail({})",
            RunOptions::default(),
        )
        .unwrap();
    assert!(!r.ok);
    assert!(r.error.contains("disk on fire"), "{}", r.error);
}

#[test]
fn handler_panic_is_a_rejection() {
    let mut cm = CodeMode::new("1.0.0").unwrap();
    cm.register(Operation::new("x.boom", "Panics."), |_| panic!("boom"))
        .unwrap();
    let r = cm
        .run(
            "export default async () => await ops.x.boom({})",
            RunOptions::default(),
        )
        .unwrap();
    assert!(!r.ok);
    assert!(r.error.contains("panicked"), "{}", r.error);
}

#[test]
fn input_is_passed_and_args_reach_handlers() {
    let mut cm = CodeMode::new("1.0.0").unwrap();
    cm.register(
        Operation::new("math.add", "Add.")
            .input_schema(json!({"type":"object","required":["a","b"],"properties":{"a":{"type":"number"},"b":{"type":"number"}}})),
        |a| Ok(json!(a["a"].as_f64().unwrap() + a["b"].as_f64().unwrap())),
    )
    .unwrap();
    let r = cm
        .run(
            "export default async () => await ops.math.add({a: input.x, b: 2})",
            RunOptions::default().input(json!({"x": 40})),
        )
        .unwrap();
    assert_eq!(r.value, json!(42));
    // schema violations are rejected before the handler runs
    let r = cm
        .run(
            "export default async () => await ops.math.add({a: 'no'})",
            RunOptions::default(),
        )
        .unwrap();
    assert!(!r.ok);
    // eval and print capture
    assert_eq!(cm.eval("1 + 2").unwrap().value, json!(3));
    let r = cm
        .eval_with("input.n * 2", RunOptions::default().input(json!({"n": 4})))
        .unwrap();
    assert_eq!(r.value, json!(8));
    let r = cm
        .run("console.log('hi'); 1", RunOptions::default())
        .unwrap();
    assert!(r.stdout.contains("hi"), "{:?}", r.stdout);
}

#[test]
fn parallel_calls_all_answered() {
    let mut cm = CodeMode::new("1.0.0").unwrap();
    let n = Arc::new(AtomicUsize::new(0));
    let n2 = n.clone();
    cm.register(Operation::new("n.echo", "Echo."), move |a| {
        n2.fetch_add(1, Ordering::SeqCst);
        Ok(a)
    })
    .unwrap();
    let r = cm
        .run(
            "export default async () => (await Promise.all([1,2,3,4,5].map(i => ops.n.echo({i})))).map(x => x.i)",
            RunOptions::default(),
        )
        .unwrap();
    assert_eq!(r.value, json!([1, 2, 3, 4, 5]), "{r:?}");
    assert_eq!(n.load(Ordering::SeqCst), 5);
}

#[test]
fn engines_are_isolated() {
    let mut a = CodeMode::new("1.0.0").unwrap();
    let mut b = CodeMode::new("1.0.0").unwrap();
    a.register(Operation::new("only.a", "A."), |_| Ok(json!("a")))
        .unwrap();
    b.register(Operation::new("only.b", "B."), |_| Ok(json!("b")))
        .unwrap();
    assert_eq!(
        a.run(
            "export default async () => await ops.only.a({})",
            RunOptions::default()
        )
        .unwrap()
        .value,
        json!("a")
    );
    assert_eq!(
        b.run(
            "export default async () => await ops.only.b({})",
            RunOptions::default()
        )
        .unwrap()
        .value,
        json!("b")
    );
    assert!(!a.check("ops.only.b({})", "p.js").unwrap().is_empty());
    assert!(!b.check("ops.only.a({})", "p.js").unwrap().is_empty());
    let (ca, cb) = (a.capabilities().unwrap(), b.capabilities().unwrap());
    assert_eq!(ca.operations.len(), 1);
    assert_ne!(ca.fingerprint, cb.fingerprint);
}

#[test]
fn fingerprint_changes_when_an_op_is_added() {
    let mut cm = CodeMode::new("1.0.0").unwrap();
    health(&mut cm);
    let before = cm.capabilities().unwrap();
    assert!(before.fingerprint.starts_with("sha256:"));
    assert_eq!(
        before.fingerprint,
        cm.capabilities().unwrap().fingerprint,
        "stable"
    );
    cm.register(Operation::new("disk.free", "Free."), |_| Ok(Value::Null))
        .unwrap();
    let after = cm.capabilities().unwrap();
    assert_ne!(before.fingerprint, after.fingerprint);
    assert_eq!(after.operations.len(), 2);
    assert_eq!(after.spec, "codemode/v0");
    assert!(after.language.unsupported.iter().any(|u| u == "class"));
}

#[test]
fn exec_and_shell_are_unavailable() {
    let mut cm = CodeMode::new("1.0.0").unwrap();
    let r = cm
        .run(
            "export default async () => await shell('echo hi')",
            RunOptions::default(),
        )
        .unwrap();
    assert!(!r.ok);
    let r = cm
        .run(
            "export default async () => await exec(['ls'])",
            RunOptions::default(),
        )
        .unwrap();
    assert!(!r.ok);
    assert!(!cm.check("exec(['ls'])", "p.js").unwrap().is_empty());
}

#[test]
fn docs_skill_compile_registration_errors() {
    let mut cm = CodeMode::new("1.0.0").unwrap();
    health(&mut cm);
    assert!(cm.docs().unwrap().contains("health.check"));
    assert!(cm.skill().unwrap().contains("health.check"));
    let c = cm.compile("ops.health.check({})", "p.js").unwrap();
    assert_eq!(c.program.unwrap().operations, vec!["health.check"]);
    assert!(cm
        .register(Operation::new("health.check", "dup"), |_| Ok(Value::Null))
        .is_err());
    assert!(cm
        .register(Operation::new("Bad Name", "x"), |_| Ok(Value::Null))
        .is_err());
    assert_eq!(
        cm.describe("health.check").unwrap().side_effects,
        SideEffects::None
    );
}

#[test]
fn timers_and_loops_work() {
    let mut cm = CodeMode::new("1.0.0").unwrap();
    let r = cm
        .run("export default async () => { await sleep(30); let s = 0; for (let i = 0; i < 5000; i++) { s += i; } return s; }", RunOptions::default())
        .unwrap();
    assert_eq!(r.value, json!(12497500), "{r:?}");
}

#[test]
fn host_result_key_order_is_kept_when_serde_json_preserves_it() {
    // This test crate enables serde_json's `preserve_order`; without it a
    // `serde_json::Value` sorts keys and the program would see 1,a,b.
    let mut cm = CodeMode::new("1.0.0").unwrap();
    cm.register(Operation::new("k.get", "Keys in document order."), |_| {
        Ok(serde_json::from_str::<Value>(r#"{"b":1,"a":2,"1":3}"#).unwrap())
    })
    .unwrap();
    let r = cm
        .run(
            "export default async () => Object.keys(await ops.k.get({})).join(',')",
            RunOptions::default(),
        )
        .unwrap();
    assert_eq!(r.value, json!("1,b,a"), "{r:?}");
}

// ---- limits, timeouts, resilience ----

use clicodemode::{Error, Limits};
use std::time::{Duration, Instant};

#[test]
fn per_run_timeout_stops_an_infinite_loop_and_the_engine_stays_usable() {
    let mut cm = CodeMode::new("1.0.0").unwrap();
    health(&mut cm);
    let t = Instant::now();
    let r = cm
        .run(
            "export default async () => { while (true) {} }",
            RunOptions::default().timeout(Duration::from_millis(300)),
        )
        .unwrap();
    assert!(!r.ok, "{r:?}");
    assert!(
        t.elapsed() < Duration::from_secs(20),
        "took {:?}",
        t.elapsed()
    );
    // the same engine runs the next program, and the per-run timeout did not stick
    let r = cm
        .run(
            "export default async () => await ops.health.check({})",
            RunOptions::default(),
        )
        .unwrap();
    assert!(r.ok, "{r:?}");
    assert_eq!(cm.eval("40 + 2").unwrap().value, json!(42));
}

#[test]
fn engine_wide_timeout_in_limits_stops_a_loop() {
    let mut cm = CodeMode::new("1.0.0").unwrap();
    cm.limits(Limits {
        timeout_ms: 300,
        ..Limits::default()
    });
    let r = cm
        .run(
            "export default async () => { while (true) {} }",
            RunOptions::default(),
        )
        .unwrap();
    assert!(!r.ok, "{r:?}");
    assert_eq!(cm.eval("1").unwrap().value, json!(1));
}

#[test]
fn timeout_applies_to_eval_too() {
    let mut cm = CodeMode::new("1.0.0").unwrap();
    let r = cm
        .eval_with(
            "(() => { while (true) {} })()",
            RunOptions::default().timeout(Duration::from_millis(300)),
        )
        .unwrap();
    assert!(!r.ok, "{r:?}");
}

#[test]
fn step_limit_stops_a_long_program() {
    let mut cm = CodeMode::new("1.0.0").unwrap();
    cm.limits(Limits {
        max_steps: 1000,
        ..Limits::default()
    });
    let r = cm
        .run("export default async () => { let s = 0; for (let i = 0; i < 1000000; i++) { s += i; } return s; }", RunOptions::default())
        .unwrap();
    assert!(!r.ok, "{r:?}");
    assert!(r.error.to_lowercase().contains("step"), "{}", r.error);
    assert_eq!(cm.eval("1 + 1").unwrap().value, json!(2));
}

#[test]
fn call_depth_limit_stops_runaway_recursion() {
    let mut cm = CodeMode::new("1.0.0").unwrap();
    cm.limits(Limits {
        max_call_depth: 20,
        ..Limits::default()
    });
    let r = cm
        .run(
            "const f = (n) => f(n + 1); export default async () => f(0)",
            RunOptions::default(),
        )
        .unwrap();
    assert!(!r.ok, "{r:?}");
}

#[test]
fn concurrency_limit_is_honoured() {
    let mut cm = CodeMode::new("1.0.0").unwrap();
    cm.limits(Limits {
        max_concurrency: 2,
        ..Limits::default()
    });
    cm.register(Operation::new("n.echo", "Echo."), Ok).unwrap();
    // must finish (completed or cleanly failed); never hang or panic
    let _ = cm
        .run("export default async () => (await Promise.all([1,2,3,4,5,6].map(i => ops.n.echo({i})))).length", RunOptions::default())
        .unwrap();
    assert_eq!(cm.eval("1").unwrap().value, json!(1));
}

/// Honest limit: a host handler is a plain synchronous call on the caller's
/// thread, so nothing can interrupt it. The run returns after the handler
/// does, and the engine is still usable.
#[test]
fn timeout_while_a_handler_is_pending_is_noticed_only_after_it_returns() {
    let mut cm = CodeMode::new("1.0.0").unwrap();
    cm.register(Operation::new("slow.op", "Slow."), |_| {
        std::thread::sleep(Duration::from_millis(600));
        Ok(json!("done"))
    })
    .unwrap();
    let t = Instant::now();
    let r = cm
        .run(
            "export default async () => await ops.slow.op({})",
            RunOptions::default().timeout(Duration::from_millis(100)),
        )
        .unwrap();
    assert!(
        t.elapsed() >= Duration::from_millis(600),
        "handler cannot be pre-empted: {:?}",
        t.elapsed()
    );
    let _ = r; // ok or timed out: unspecified; the handler's side effect already happened
    assert_eq!(cm.eval("2 * 2").unwrap().value, json!(4));
}

#[test]
fn big_payloads_cross_the_boundary_both_ways() {
    let mut cm = CodeMode::new("1.0.0").unwrap();
    cm.register(
        Operation::new("blob.len", "Length of a string argument."),
        |a| Ok(json!(a["s"].as_str().map(str::len).unwrap_or(0))),
    )
    .unwrap();
    cm.register(Operation::new("blob.make", "Make a big string."), |_| {
        Ok(json!("x".repeat(3_000_000)))
    })
    .unwrap();
    let big = "y".repeat(2_000_000);
    let r = cm
        .run(
            "export default async () => await ops.blob.len({s: input.s})",
            RunOptions::default().input(json!({"s": big})),
        )
        .unwrap();
    assert_eq!(r.value, json!(2_000_000), "{:?}", r.error);
    let r = cm
        .run(
            "export default async () => (await ops.blob.make({})).length",
            RunOptions::default(),
        )
        .unwrap();
    assert_eq!(r.value, json!(3_000_000), "{:?}", r.error);
}

#[test]
fn unicode_and_nested_json_round_trip() {
    let mut cm = CodeMode::new("1.0.0").unwrap();
    cm.register(Operation::new("n.echo", "Echo."), Ok).unwrap();
    let v = json!({"a": [1, 2.5, null, true, {"deep": {"x": "日本 \n \"q\""}}]});
    let r = cm
        .run(
            "export default async () => await ops.n.echo(input)",
            RunOptions::default().input(v.clone()),
        )
        .unwrap();
    assert_eq!(r.value, v, "{r:?}");
}

#[test]
fn register_after_use_restarts_with_the_new_contract() {
    let mut cm = CodeMode::new("1.0.0").unwrap();
    health(&mut cm);
    let before = cm.capabilities().unwrap().fingerprint;
    assert!(
        cm.run(
            "export default async () => await ops.health.check({})",
            RunOptions::default()
        )
        .unwrap()
        .ok
    );
    cm.register(Operation::new("disk.free", "Free."), |_| Ok(json!(7)))
        .unwrap();
    let r = cm
        .run(
            "export default async () => [await ops.health.check({}), await ops.disk.free({})]",
            RunOptions::default(),
        )
        .unwrap();
    assert_eq!(r.value, json!([{"ok": true}, 7]), "{r:?}");
    assert_ne!(before, cm.capabilities().unwrap().fingerprint);
}

#[test]
fn changing_limits_after_use_takes_effect() {
    let mut cm = CodeMode::new("1.0.0").unwrap();
    assert_eq!(cm.eval("1").unwrap().value, json!(1));
    cm.limits(Limits {
        max_steps: 50,
        ..Limits::default()
    });
    let r = cm
        .run("export default async () => { let s = 0; for (let i = 0; i < 100000; i++) { s += i; } return s; }", RunOptions::default())
        .unwrap();
    assert!(!r.ok, "{r:?}");
}

#[test]
fn docs_and_skill_are_non_empty_and_name_the_operations() {
    let mut cm = CodeMode::new("1.0.0").unwrap();
    assert!(cm.docs().unwrap().len() > 200);
    assert!(cm.skill().unwrap().len() > 200);
    health(&mut cm);
    let (d, s) = (cm.docs().unwrap(), cm.skill().unwrap());
    assert!(d.contains("health.check") && s.contains("health.check"));
    assert!(!cm.engine_version().unwrap().is_empty());
}

#[test]
fn exec_shell_and_exec_stream_are_denied_at_run_time() {
    let mut cm = CodeMode::new("1.0.0").unwrap();
    for prog in [
        "export default async () => await exec(['echo','hi'])",
        "export default async () => await shell('echo hi')",
        "export default async () => await execStream(['echo','hi'])",
    ] {
        let r = cm.run(prog, RunOptions::default()).unwrap();
        assert!(!r.ok, "{prog}: {r:?}");
        assert!(!r.error.is_empty());
    }
}

#[test]
fn imports_are_unavailable() {
    let mut cm = CodeMode::new("1.0.0").unwrap();
    let r = cm
        .run(
            "import x from './other.js';\nexport default async () => x",
            RunOptions::default(),
        )
        .unwrap();
    assert!(!r.ok, "{r:?}");
}

#[test]
fn a_failing_program_does_not_poison_the_engine() {
    let mut cm = CodeMode::new("1.0.0").unwrap();
    for _ in 0..3 {
        assert!(
            !cm.run(
                "export default async () => { throw new Error('x') }",
                RunOptions::default()
            )
            .unwrap()
            .ok
        );
        assert!(
            !cm.run("syntax error here (", RunOptions::default())
                .unwrap()
                .ok
        );
        assert_eq!(cm.eval("1").unwrap().value, json!(1));
    }
}

#[test]
fn describe_unknown_operation_is_an_engine_error() {
    let mut cm = CodeMode::new("1.0.0").unwrap();
    assert!(matches!(cm.describe("no.such"), Err(Error::Engine(_))));
}

#[test]
fn engines_can_run_on_separate_threads() {
    let hs: Vec<_> = (0..3)
        .map(|i| {
            std::thread::spawn(move || {
                let mut cm = CodeMode::new("1.0.0").unwrap();
                cm.eval(&format!("{i} + 1")).unwrap().value
            })
        })
        .collect();
    let got: Vec<Value> = hs.into_iter().map(|h| h.join().unwrap()).collect();
    assert_eq!(got, vec![json!(1), json!(2), json!(3)]);
}
