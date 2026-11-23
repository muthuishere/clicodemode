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
        .run("export default async () => await ops.health.check({})", RunOptions::default())
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
    cm.register(Operation::new("disk.fail", "Always fails."), |_| Err("disk on fire".into())).unwrap();
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
    let r = cm.run("export default async () => await ops.disk.fail({})", RunOptions::default()).unwrap();
    assert!(!r.ok);
    assert!(r.error.contains("disk on fire"), "{}", r.error);
}

#[test]
fn handler_panic_is_a_rejection() {
    let mut cm = CodeMode::new("1.0.0").unwrap();
    cm.register(Operation::new("x.boom", "Panics."), |_| panic!("boom")).unwrap();
    let r = cm.run("export default async () => await ops.x.boom({})", RunOptions::default()).unwrap();
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
    let r = cm.run("export default async () => await ops.math.add({a: 'no'})", RunOptions::default()).unwrap();
    assert!(!r.ok);
    // eval and print capture
    assert_eq!(cm.eval("1 + 2").unwrap().value, json!(3));
    let r = cm.eval_with("input.n * 2", RunOptions::default().input(json!({"n": 4}))).unwrap();
    assert_eq!(r.value, json!(8));
    let r = cm.run("console.log('hi'); 1", RunOptions::default()).unwrap();
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
    a.register(Operation::new("only.a", "A."), |_| Ok(json!("a"))).unwrap();
    b.register(Operation::new("only.b", "B."), |_| Ok(json!("b"))).unwrap();
    assert_eq!(a.run("export default async () => await ops.only.a({})", RunOptions::default()).unwrap().value, json!("a"));
    assert_eq!(b.run("export default async () => await ops.only.b({})", RunOptions::default()).unwrap().value, json!("b"));
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
    assert_eq!(before.fingerprint, cm.capabilities().unwrap().fingerprint, "stable");
    cm.register(Operation::new("disk.free", "Free."), |_| Ok(Value::Null)).unwrap();
    let after = cm.capabilities().unwrap();
    assert_ne!(before.fingerprint, after.fingerprint);
    assert_eq!(after.operations.len(), 2);
    assert_eq!(after.spec, "codemode/v0");
    assert!(after.language.unsupported.iter().any(|u| u == "class"));
}

#[test]
fn exec_and_shell_are_unavailable() {
    let mut cm = CodeMode::new("1.0.0").unwrap();
    let r = cm.run("export default async () => await shell('echo hi')", RunOptions::default()).unwrap();
    assert!(!r.ok);
    let r = cm.run("export default async () => await exec(['ls'])", RunOptions::default()).unwrap();
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
    assert!(cm.register(Operation::new("health.check", "dup"), |_| Ok(Value::Null)).is_err());
    assert!(cm.register(Operation::new("Bad Name", "x"), |_| Ok(Value::Null)).is_err());
    assert_eq!(cm.describe("health.check").unwrap().side_effects, SideEffects::None);
}

#[test]
fn timers_and_loops_work() {
    let mut cm = CodeMode::new("1.0.0").unwrap();
    let r = cm
        .run("export default async () => { await sleep(30); let s = 0; for (let i = 0; i < 5000; i++) { s += i; } return s; }", RunOptions::default())
        .unwrap();
    assert_eq!(r.value, json!(12497500), "{r:?}");
}
