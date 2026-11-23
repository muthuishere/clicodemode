//! Drives the repository's language-neutral conformance suite
//! (`../conformance/cases/*.json`) through this crate.
//!
//! `known_deviation` cases describe bugs of the Go reference engine; if one
//! still fails it is counted as skipped, and if it passes it is counted as
//! fixed, never as an ordinary pass. Cases the Rust API cannot express (the
//! Policy hook; operations that never settle; a registration without a
//! handler) are counted as unsupported. Run with `-- --nocapture` for totals.

use clicodemode::{CodeMode, Limits, Operation, RunOptions, SideEffects};
use serde_json::{json, Value};
use std::collections::BTreeMap;
use std::path::PathBuf;
use std::sync::{Arc, Mutex};
use std::time::Duration;

#[derive(Default)]
struct Tracker {
    calls: BTreeMap<String, u64>,
    in_flight: u64,
    peak: u64,
}

fn canon(v: &Value) -> Value {
    match v {
        Value::Array(a) => Value::Array(a.iter().map(canon).collect()),
        Value::Object(m) => {
            let b: BTreeMap<_, _> = m.iter().map(|(k, x)| (k.clone(), canon(x))).collect();
            Value::Object(b.into_iter().collect())
        }
        o => o.clone(),
    }
}

enum Outcome {
    Pass,
    Unsupported,
}

fn operation(o: &Value) -> Operation {
    let se = match o["side_effects"].as_str() {
        Some("none") => SideEffects::None,
        Some("external") => SideEffects::External,
        _ => SideEffects::Local,
    };
    let mut op = Operation::new(
        o["name"].as_str().unwrap(),
        o["description"].as_str().unwrap_or(""),
    )
    .side_effects(se)
    .idempotent(o["idempotent"].as_bool().unwrap_or(false));
    if let Some(s) = o.get("input_schema") {
        op = op.input_schema(s.clone());
    }
    op
}

/// Err(name) is the operation whose registration failed.
fn register(cm: &mut CodeMode, o: &Value, t: &Arc<Mutex<Tracker>>) -> Result<(), String> {
    let name = o["name"].as_str().unwrap().to_string();
    if o["no_handler"].as_bool() == Some(true) {
        return Err(name); // the Rust type system has no handler-less registration
    }
    let (panics, delay) = (
        o["panics"].as_bool() == Some(true),
        o["delay_ms"].as_u64().unwrap_or(0),
    );
    let throws = o["throws"].as_str().map(String::from);
    let returns = o.get("returns").cloned().unwrap_or(Value::Null);
    let (t, n2) = (t.clone(), name.clone());
    cm.register(operation(o), move |_| {
        {
            let mut g = t.lock().unwrap();
            *g.calls.entry(n2.clone()).or_default() += 1;
            g.in_flight += 1;
            g.peak = g.peak.max(g.in_flight);
        }
        if delay > 0 {
            std::thread::sleep(Duration::from_millis(delay));
        }
        t.lock().unwrap().in_flight -= 1;
        if panics {
            panic!("stub panic");
        }
        match &throws {
            Some(m) => Err(m.clone()),
            None => Ok(returns.clone()),
        }
    })
    .map_err(|_| name)
}

fn not_run(err: &str) -> bool {
    err.contains("nothing was run") || err.contains(" error: syntax error")
}

fn run_case(c: &Value) -> Result<Outcome, String> {
    let mode = c["mode"].as_str().unwrap_or("run");
    let x = &c["expect"];
    let t = Arc::new(Mutex::new(Tracker::default()));
    let ops: Vec<Value> = c["ops"].as_array().cloned().unwrap_or_default();

    if mode == "register" {
        let mut cm = CodeMode::new("conformance 1.0").unwrap();
        let failed = ops.iter().find_map(|o| register(&mut cm, o, &t).err());
        return match (x.get("register_error"), failed) {
            (Some(re), Some(f)) if f == re["op"].as_str().unwrap() => Ok(Outcome::Pass),
            (Some(re), f) => Err(format!("registration failed on {f:?}, want {}", re["op"])),
            (None, None) => Ok(Outcome::Pass),
            (None, Some(f)) => Err(format!("unexpected registration error on {f}")),
        };
    }

    if mode == "fingerprint" {
        let mut fps = vec![];
        for k in ["ops", "ops_b"] {
            let mut cm = CodeMode::new("conformance 1.0").unwrap();
            for o in c[k].as_array().cloned().unwrap_or_default() {
                register(&mut cm, &o, &t).map_err(|n| format!("register {n}"))?;
            }
            fps.push(cm.capabilities().map_err(|e| e.to_string())?.fingerprint);
        }
        for f in &fps {
            if !(f.starts_with("sha256:") && f.len() == 7 + 32) {
                return Err(format!("fingerprint format {f}"));
            }
        }
        return if (fps[0] == fps[1]) == (x["fingerprint"] == "equal") {
            Ok(Outcome::Pass)
        } else {
            Err(format!("fingerprints {fps:?}"))
        };
    }

    if c.get("policy")
        .and_then(Value::as_object)
        .is_some_and(|m| !m.is_empty())
    {
        return Ok(Outcome::Unsupported);
    }
    if ops
        .iter()
        .any(|o| o["blocks_until_cancel"].as_bool() == Some(true))
    {
        return Ok(Outcome::Unsupported);
    }
    if ops.iter().any(|o| o["no_handler"].as_bool() == Some(true)) {
        return Ok(Outcome::Unsupported);
    }

    let mut cm = CodeMode::new("conformance 1.0").unwrap();
    let lim = &c["limits"];
    cm.limits(Limits {
        max_steps: lim["max_steps"].as_u64().unwrap_or(0),
        max_call_depth: lim["max_call_depth"].as_u64().unwrap_or(0) as u32,
        max_concurrency: lim["max_concurrency"].as_u64().unwrap_or(0) as u32,
        timeout_ms: lim["timeout_ms"].as_u64().unwrap_or(0),
    });
    for o in &ops {
        register(&mut cm, o, &t).map_err(|n| format!("register {n}"))?;
    }
    let file = c["file"].as_str().unwrap_or("program.js");
    let prog = c["program"].as_str().unwrap();
    let mut opts = RunOptions::default().file(file);
    if let Some(i) = c.get("input").filter(|v| !v.is_null()) {
        opts = opts.input(i.clone());
    }

    let (mut res, mut program_ops) = (None, None);
    let diags = match mode {
        "check" => {
            if x.get("operations").is_some() {
                program_ops = Some(
                    cm.compile(prog, file)
                        .map_err(|e| e.to_string())?
                        .program
                        .map(|p| p.operations)
                        .unwrap_or_default(),
                );
            }
            cm.check(prog, file).map_err(|e| e.to_string())?
        }
        m => {
            let r = if m == "eval" {
                cm.eval_with(prog, opts)
            } else {
                cm.run(prog, opts)
            }
            .map_err(|e| e.to_string())?;
            let d = r.diagnostics.clone();
            res = Some(r);
            d
        }
    };

    {
        let tr = t.lock().unwrap();
        for (n, want) in x
            .get("calls")
            .and_then(Value::as_object)
            .into_iter()
            .flatten()
        {
            let got = tr.calls.get(n).copied().unwrap_or(0);
            if Some(got) != want.as_u64() {
                return Err(format!("op {n} called {got} times, want {want}"));
            }
        }
        if let Some(m) = x.get("max_in_flight_at_most").and_then(Value::as_u64) {
            if tr.peak > m {
                return Err(format!("peak in-flight {}", tr.peak));
            }
        }
    }
    if let Some(want) = x.get("stdout").and_then(Value::as_str) {
        let got = res.as_ref().map(|r| r.stdout.as_str()).unwrap_or("");
        if got != want {
            return Err(format!("stdout {got:?}, want {want:?}"));
        }
    }
    if let (Some(want), Some(got)) = (x.get("operations"), &program_ops) {
        let mut g = got.clone();
        g.sort();
        if canon(&json!(g)) != canon(want) {
            return Err(format!("operations {got:?}, want {want}"));
        }
    }
    if let Some(e) = x.get("exit").and_then(Value::as_u64) {
        let class = match &res {
            None if diags.iter().any(|d| d.severity == "error") => 2,
            None => 0,
            Some(r) if r.ok => 0,
            Some(r) if not_run(&r.error) => 2,
            Some(_) => 1,
        };
        if class != e {
            return Err(format!(
                "exit class {class}, want {e} ({:?})",
                res.as_ref().map(|r| &r.error)
            ));
        }
    }

    if let Some(want) = x.get("diagnostics").and_then(Value::as_array) {
        if let Some(r) = &res {
            if r.ok || !not_run(&r.error) {
                return Err(format!("expected diagnostics and nothing run, got {r:?}"));
            }
        }
        if diags.len() != want.len() {
            return Err(format!(
                "got {} diagnostics {diags:?}, want {}",
                diags.len(),
                want.len()
            ));
        }
        for (i, (w, g)) in want.iter().zip(&diags).enumerate() {
            let at = format!("diag {i} at {}:{} ({})", g.line, g.col, g.message);
            if w["file"].as_str().is_some_and(|f| g.file != f) {
                return Err(format!("{at}: file {}", g.file));
            }
            if w["severity"].as_str().is_some_and(|s| g.severity != s) {
                return Err(format!("{at}: severity {}", g.severity));
            }
            if w["line"]
                .as_u64()
                .filter(|l| *l > 0)
                .is_some_and(|l| g.line as u64 != l)
            {
                return Err(format!("{at}: want line {}", w["line"]));
            }
            if w["col"]
                .as_u64()
                .filter(|l| *l > 0)
                .is_some_and(|l| g.col as u64 != l)
            {
                return Err(format!("{at}: want col {}", w["col"]));
            }
            let (m, h) = (
                w["messageContains"].as_str().unwrap_or(""),
                w["hintContains"].as_str().unwrap_or(""),
            );
            if !g.message.contains(m) || !g.hint.contains(h) {
                return Err(format!(
                    "{at}: hint {:?}, want message~{m:?} hint~{h:?}",
                    g.hint
                ));
            }
        }
    } else if let Some(e) = x.get("error") {
        let r = res.as_ref().filter(|r| !r.ok).ok_or("expected a failure")?;
        let m = e["messageContains"].as_str().unwrap_or("");
        if !r.error.contains(m) {
            return Err(format!("error {:?} lacks {m:?}", r.error));
        }
        if let Some(l) = e["line"].as_u64() {
            if diags.last().map(|d| d.line as u64) != Some(l) {
                return Err(format!(
                    "error line {:?}, want {l}",
                    diags.last().map(|d| d.line)
                ));
            }
        }
    } else if let Some(want) = x.get("result") {
        let r = res.as_ref().filter(|r| r.ok).ok_or_else(|| {
            format!(
                "run failed: {:?}",
                res.as_ref().map(|r| (&r.error, &r.diagnostics))
            )
        })?;
        if canon(&r.value) != canon(want) {
            return Err(format!("result {}, want {want}", r.value));
        }
    }
    Ok(Outcome::Pass)
}

#[test]
fn conformance_suite() {
    let dir = PathBuf::from(env!("CARGO_MANIFEST_DIR"))
        .join("..")
        .join("conformance")
        .join("cases");
    if !dir.is_dir() {
        eprintln!(
            "conformance cases not found at {} (packaged crate?): skipped",
            dir.display()
        );
        return;
    }
    let mut files: Vec<_> = std::fs::read_dir(&dir)
        .unwrap()
        .map(|e| e.unwrap().path())
        .filter(|p| p.extension().is_some_and(|e| e == "json"))
        .collect();
    files.sort();
    let (mut pass, mut skipped, mut fixed, mut unsupported, mut total) = (0, 0, 0, 0, 0);
    let mut failures = vec![];
    let mut seen = std::collections::HashSet::new();
    for f in files {
        let cases: Vec<Value> = serde_json::from_slice(&std::fs::read(&f).unwrap()).unwrap();
        for c in cases {
            total += 1;
            let id = c["id"].as_str().unwrap().to_string();
            assert!(seen.insert(id.clone()), "duplicate case id {id}");
            let deviation = c["known_deviation"].as_bool() == Some(true);
            let r = std::panic::catch_unwind(|| run_case(&c))
                .unwrap_or_else(|_| Err("harness panic".into()));
            match (r, deviation) {
                (Ok(Outcome::Unsupported), _) => unsupported += 1,
                (Ok(Outcome::Pass), true) => {
                    skipped += 1;
                    fixed += 1;
                }
                (Err(_), true) => skipped += 1,
                (Ok(Outcome::Pass), false) => pass += 1,
                (Err(m), false) => failures.push(format!("  FAIL {id}: {m}")),
            }
        }
    }
    println!(
        "conformance via Rust: {pass} passed, {} failed, {skipped} skipped (known_deviation; {fixed} of them pass anyway), {unsupported} unsupported, {total} total",
        failures.len()
    );
    for f in &failures {
        println!("{f}");
    }
    assert!(
        failures.is_empty(),
        "{} conformance failures",
        failures.len()
    );
    assert!(pass > 100, "suspiciously few cases ran");
}
