//! Code Mode for Rust CLIs.
//!
//! Register the commands your CLI already has as [`Operation`]s, then run
//! small JavaScript-like programs that call them. The language engine is the
//! `clicodemode` Go engine compiled to WASI and embedded in this crate; it
//! runs on the pure-Rust `wasmi` interpreter, so there is nothing else to
//! install.
//!
//! ```no_run
//! use clicodemode::{CodeMode, Operation, RunOptions, SideEffects};
//! use serde_json::json;
//!
//! # fn main() -> Result<(), clicodemode::Error> {
//! let mut cm = CodeMode::new("1.0.0")?;
//! cm.register(
//!     Operation::new("health.check", "Report health.").side_effects(SideEffects::None),
//!     |_args| Ok(json!({"ok": true})),
//! )?;
//! let r = cm.run(
//!     "export default async () => await ops.health.check({})",
//!     RunOptions::default(),
//! )?;
//! assert_eq!(r.value, json!({"ok": true}));
//! # Ok(()) }
//! ```

mod guest;
mod types;

pub use types::*;

use std::collections::HashMap;
use std::fmt;
use std::panic::{catch_unwind, AssertUnwindSafe};
use std::sync::mpsc::{channel, Receiver, Sender};
use std::sync::{Arc, Mutex};
use std::thread::JoinHandle;

use serde_json::{json, Value};

/// A synchronous operation handler: arguments in, JSON result or error text out.
pub type Handler = dyn Fn(Value) -> Result<Value, String> + Send + Sync + 'static;

/// Errors from the library itself (a program that fails is still `Ok` with
/// `RunResult::ok == false`).
#[derive(Debug)]
pub enum Error {
    /// The embedded engine rejected the request (bad operation, bad input...).
    Engine(String),
    /// The WASI engine process is gone or could not start.
    Runtime(String),
    /// Invalid use of this crate (duplicate operation, bad name...).
    Usage(String),
    /// A message could not be encoded or decoded.
    Json(serde_json::Error),
}

impl fmt::Display for Error {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        match self {
            Error::Engine(m) => write!(f, "engine: {m}"),
            Error::Runtime(m) => write!(f, "runtime: {m}"),
            Error::Usage(m) => write!(f, "{m}"),
            Error::Json(e) => write!(f, "json: {e}"),
        }
    }
}
impl std::error::Error for Error {}
impl From<serde_json::Error> for Error {
    fn from(e: serde_json::Error) -> Self {
        Error::Json(e)
    }
}

/// Per-run options.
#[derive(Default, Clone)]
pub struct RunOptions {
    input: Option<Value>,
    file: Option<String>,
    timeout_ms: u64,
}

impl RunOptions {
    /// Wall-clock limit for this one run, replacing [`Limits::timeout_ms`].
    ///
    /// The engine checks it between steps and while waiting on its own
    /// timers, so an infinite loop is stopped. It cannot interrupt a host
    /// handler that is already running (handlers are plain synchronous Rust
    /// calls on your thread): the deadline is noticed once the handler returns.
    pub fn timeout(mut self, d: std::time::Duration) -> Self {
        self.timeout_ms = d.as_millis().clamp(1, u64::MAX as u128) as u64;
        self
    }
    /// The JSON value exposed to the program as the global `input`.
    pub fn input(mut self, v: Value) -> Self {
        self.input = Some(v);
        self
    }
    /// File name used in diagnostics (default `program.js`).
    pub fn file(mut self, f: impl Into<String>) -> Self {
        self.file = Some(f.into());
        self
    }
}

/// Bounds for each run. Zero means the engine default.
#[derive(Default, Clone, Debug)]
pub struct Limits {
    pub max_steps: u64,
    pub max_call_depth: u32,
    pub max_concurrency: u32,
    pub timeout_ms: u64,
}

struct Session {
    to_guest: Option<Sender<Vec<u8>>>,
    from_guest: Receiver<Vec<u8>>,
    exit: Arc<Mutex<Option<String>>>,
    thread: Option<JoinHandle<()>>,
    next_id: u64,
}

impl Drop for Session {
    fn drop(&mut self) {
        self.to_guest.take(); // EOF ends the engine
        if let Some(t) = self.thread.take() {
            let _ = t.join();
        }
    }
}

impl Session {
    fn start() -> Session {
        let (to_guest, rx) = channel::<Vec<u8>>();
        let (tx, from_guest) = channel::<Vec<u8>>();
        let exit = Arc::new(Mutex::new(None));
        let e2 = exit.clone();
        let thread = std::thread::Builder::new()
            .name("clicodemode-engine".into())
            .stack_size(16 << 20)
            .spawn(move || guest::run(rx, tx, e2))
            .expect("spawn engine thread");
        Session {
            to_guest: Some(to_guest),
            from_guest,
            exit,
            thread: Some(thread),
            next_id: 0,
        }
    }

    fn send(&self, v: &Value) -> Result<(), Error> {
        let mut b = serde_json::to_vec(v)?;
        b.push(b'\n');
        self.to_guest
            .as_ref()
            .and_then(|t| t.send(b).ok())
            .ok_or_else(|| self.gone())
    }

    fn gone(&self) -> Error {
        let why = self.exit.lock().unwrap().clone();
        Error::Runtime(why.unwrap_or_else(|| "engine stopped".into()))
    }

    /// Send a request and serve the engine's operation calls until its reply.
    fn request(
        &mut self,
        method: &str,
        params: Value,
        handlers: &HashMap<String, Arc<Box<Handler>>>,
        logs: &mut String,
    ) -> Result<Value, Error> {
        self.next_id += 1;
        let id = self.next_id;
        self.send(&json!({"id": id, "method": method, "params": params}))?;
        loop {
            let line = self.from_guest.recv().map_err(|_| {
                // give the thread a moment to record why it stopped
                if let Some(t) = self.thread.take() {
                    let _ = t.join();
                }
                self.gone()
            })?;
            let msg: Value = match serde_json::from_slice(&line) {
                Ok(v) => v,
                Err(_) => continue,
            };
            if let Some(l) = msg.get("log").and_then(Value::as_str) {
                logs.push_str(l);
            } else if let Some(c) = msg.get("call") {
                let cid = c.get("id").cloned().unwrap_or(Value::Null);
                let name = c.get("name").and_then(Value::as_str).unwrap_or("");
                let args = c.get("args").cloned().unwrap_or(Value::Null);
                let out = match handlers.get(name) {
                    None => Err(format!("no handler for operation {name:?}")),
                    Some(h) => match catch_unwind(AssertUnwindSafe(|| h(args))) {
                        Ok(r) => r,
                        Err(_) => Err("handler panicked".to_string()),
                    },
                };
                let reply = match out {
                    Ok(v) => json!({"callResult": {"id": cid, "ok": true, "result": v}}),
                    Err(e) => json!({"callResult": {"id": cid, "ok": false, "error": e}}),
                };
                self.send(&reply)?;
            } else if msg.get("id").and_then(Value::as_u64) == Some(id) {
                if msg.get("ok").and_then(Value::as_bool) == Some(true) {
                    return Ok(msg.get("result").cloned().unwrap_or(Value::Null));
                }
                let e = msg
                    .get("error")
                    .and_then(Value::as_str)
                    .unwrap_or("unknown error");
                return Err(Error::Engine(e.to_string()));
            }
        }
    }
}

/// A Code Mode engine bound to a set of registered operations.
///
/// Each `CodeMode` owns its own engine instance (one WASI process image), so
/// two of them never share operations or state. The engine starts lazily on
/// first use; registering another operation restarts it with the new set.
pub struct CodeMode {
    version: String,
    ops: Vec<Operation>,
    modules: Vec<(String, String)>,
    handlers: HashMap<String, Arc<Box<Handler>>>,
    limits: Limits,
    session: Option<Session>,
    /// Everything programs printed (`print`/`console.log`) during the last call.
    last_output: String,
}

impl CodeMode {
    /// New engine for a CLI at `version` (feeds the capability fingerprint).
    pub fn new(version: impl Into<String>) -> Result<Self, Error> {
        Ok(CodeMode {
            version: version.into(),
            ops: Vec::new(),
            modules: Vec::new(),
            handlers: HashMap::new(),
            limits: Limits::default(),
            session: None,
            last_output: String::new(),
        })
    }

    /// Bound steps, depth, concurrency and wall-clock time of each run.
    pub fn limits(&mut self, limits: Limits) -> &mut Self {
        self.limits = limits;
        self.session = None;
        self
    }

    /// Register an operation and the handler that implements it. The handler
    /// runs on the thread that calls `run`/`eval`, synchronously.
    pub fn register<F>(&mut self, op: Operation, handler: F) -> Result<(), Error>
    where
        F: Fn(Value) -> Result<Value, String> + Send + Sync + 'static,
    {
        if !valid_name(&op.name) {
            return Err(Error::Usage(format!(
                "invalid operation name {:?} (want dotted segments of letters, digits, _ and -, e.g. health.check)",
                op.name
            )));
        }
        if self.handlers.contains_key(&op.name) {
            return Err(Error::Usage(format!(
                "operation {:?} already registered",
                op.name
            )));
        }
        self.handlers
            .insert(op.name.clone(), Arc::new(Box::new(handler)));
        self.ops.push(op);
        self.session = None; // restart with the new contract
        Ok(())
    }

    /// Add a JS (or `.json` data) module that programs import as
    /// `"scheme:path"`, from source text, usually embedded at build time with
    /// `include_str!` (see [`embed_modules!`]).
    pub fn register_module(&mut self, name: &str, source: &str) -> Result<(), Error> {
        let name = name.strip_suffix(".js").unwrap_or(name).to_string();
        if !valid_module_name(&name) {
            return Err(Error::Usage(format!(
                "invalid module name {name:?} (want scheme:path, e.g. mycli:layout)"
            )));
        }
        if self.modules.iter().any(|(n, _)| *n == name) {
            return Err(Error::Usage(format!("module {name:?} already registered")));
        }
        self.modules.push((name, source.to_string()));
        self.session = None; // restart with the new module set
        Ok(())
    }

    /// Add `(relative path, source)` pairs as `scheme:<relative path>`; `.js`
    /// and `.json` entries only. Pair it with [`embed_modules!`] to bake a
    /// folder into the binary.
    pub fn register_modules(
        &mut self,
        scheme: &str,
        files: &[(&str, &str)],
    ) -> Result<Vec<String>, Error> {
        let mut names = Vec::new();
        for (rel, src) in files {
            let rel = rel.trim_start_matches("./");
            if rel.ends_with(".js") || rel.ends_with(".json") {
                self.register_module(&format!("{scheme}:{rel}"), src)?;
                names.push(format!(
                    "{scheme}:{}",
                    rel.strip_suffix(".js").unwrap_or(rel)
                ));
            }
        }
        if names.is_empty() {
            return Err(Error::Usage("no .js or .json modules given".into()));
        }
        Ok(names)
    }

    /// Add every `.js`/`.json` file under `dir`, read now, as
    /// `scheme:<relative path>`.
    pub fn register_modules_dir(
        &mut self,
        scheme: &str,
        dir: impl AsRef<std::path::Path>,
    ) -> Result<Vec<String>, Error> {
        fn walk(
            root: &std::path::Path,
            d: &std::path::Path,
            out: &mut Vec<(String, String)>,
        ) -> std::io::Result<()> {
            let mut entries: Vec<_> = std::fs::read_dir(d)?.collect::<Result<_, _>>()?;
            entries.sort_by_key(|e| e.file_name());
            for e in entries {
                let p = e.path();
                if p.is_dir() {
                    walk(root, &p, out)?;
                } else if matches!(p.extension().and_then(|x| x.to_str()), Some("js" | "json")) {
                    let rel = p
                        .strip_prefix(root)
                        .unwrap_or(&p)
                        .to_string_lossy()
                        .replace('\\', "/");
                    out.push((rel, std::fs::read_to_string(&p)?));
                }
            }
            Ok(())
        }
        let mut found = Vec::new();
        walk(dir.as_ref(), dir.as_ref(), &mut found)
            .map_err(|e| Error::Usage(format!("reading modules: {e}")))?;
        let pairs: Vec<(&str, &str)> = found
            .iter()
            .map(|(a, b)| (a.as_str(), b.as_str()))
            .collect();
        self.register_modules(scheme, &pairs)
    }

    /// Add one `.js`/`.json` file, read now, as `scheme:<file name>`.
    pub fn register_module_file(
        &mut self,
        scheme: &str,
        file: impl AsRef<std::path::Path>,
    ) -> Result<(), Error> {
        let p = file.as_ref();
        let name = p
            .file_name()
            .and_then(|n| n.to_str())
            .unwrap_or_default()
            .to_string();
        if !(name.ends_with(".js") || name.ends_with(".json")) {
            return Err(Error::Usage(format!(
                "module file {name:?} must end in .js or .json"
            )));
        }
        let src = std::fs::read_to_string(p)
            .map_err(|e| Error::Usage(format!("reading {}: {e}", p.display())))?;
        self.register_module(&format!("{scheme}:{name}"), &src)
    }

    /// Text printed by programs (`print`, `console.log`) during the last call.
    pub fn output(&self) -> &str {
        &self.last_output
    }

    fn call(&mut self, method: &str, params: Value) -> Result<Value, Error> {
        if self.session.is_none() {
            let mut s = Session::start();
            let cfg = json!({
                "version": self.version,
                "operations": self.ops,
                "modules": self.modules.iter().map(|(n, s)| json!({"name": n, "source": s})).collect::<Vec<_>>(),
                "limits": {
                    "max_steps": self.limits.max_steps,
                    "max_call_depth": self.limits.max_call_depth,
                    "max_concurrency": self.limits.max_concurrency,
                    "timeout_ms": self.limits.timeout_ms,
                },
            });
            let mut sink = String::new();
            s.request("create", cfg, &self.handlers, &mut sink)?;
            self.session = Some(s);
        }
        let mut logs = String::new();
        let r = self
            .session
            .as_mut()
            .unwrap()
            .request(method, params, &self.handlers, &mut logs);
        if matches!(r, Err(Error::Runtime(_))) {
            self.session = None;
        }
        self.last_output = logs;
        r
    }

    fn typed<T: serde::de::DeserializeOwned>(
        &mut self,
        method: &str,
        params: Value,
    ) -> Result<T, Error> {
        Ok(serde_json::from_value(self.call(method, params)?)?)
    }

    /// Run a program. A program that throws gives `Ok(RunResult { ok: false, .. })`.
    pub fn run(&mut self, src: &str, opts: RunOptions) -> Result<RunResult, Error> {
        let file = opts.file.unwrap_or_else(|| "program.js".into());
        let mut r: RunResult = self.typed(
            "run",
            json!({"src": src, "file": file, "input": opts.input, "timeout_ms": opts.timeout_ms}),
        )?;
        r.stdout = self.last_output.clone();
        Ok(r)
    }

    /// Evaluate one expression.
    pub fn eval(&mut self, expr: &str) -> Result<RunResult, Error> {
        self.eval_with(expr, RunOptions::default())
    }

    /// Evaluate one expression with the global `input`.
    pub fn eval_with(&mut self, expr: &str, opts: RunOptions) -> Result<RunResult, Error> {
        let mut r: RunResult = self.typed(
            "eval",
            json!({"expr": expr, "input": opts.input, "timeout_ms": opts.timeout_ms}),
        )?;
        r.stdout = self.last_output.clone();
        Ok(r)
    }

    /// Parse and check a program without running it.
    pub fn check(&mut self, src: &str, file: &str) -> Result<Vec<Diagnostic>, Error> {
        self.typed("check", json!({"src": src, "file": file}))
    }

    /// Check a program and return its plan (hash, operations used, cache key).
    pub fn compile(&mut self, src: &str, file: &str) -> Result<CompileResult, Error> {
        self.typed("compile", json!({"src": src, "file": file}))
    }

    /// The capability document, with a fingerprint that changes with the operations.
    pub fn capabilities(&mut self) -> Result<Capabilities, Error> {
        self.typed("capabilities", Value::Null)
    }

    /// Registered operations, sorted by name.
    pub fn operations(&mut self) -> Result<Vec<Operation>, Error> {
        self.typed("operations", Value::Null)
    }

    /// One operation's contract.
    pub fn describe(&mut self, name: &str) -> Result<Operation, Error> {
        self.typed("describe", json!({"name": name}))
    }

    /// Language and operation reference, as Markdown.
    pub fn docs(&mut self) -> Result<String, Error> {
        self.typed("docs", Value::Null)
    }

    /// A generated SKILL.md for agents using this CLI's Code Mode.
    pub fn skill(&mut self) -> Result<String, Error> {
        self.typed("skill", Value::Null)
    }

    /// Engine and spec version.
    pub fn engine_version(&mut self) -> Result<String, Error> {
        self.typed("version", Value::Null)
    }
}

fn valid_module_name(n: &str) -> bool {
    let Some((scheme, path)) = n.split_once(':') else {
        return false;
    };
    let path = path.strip_suffix(".json").unwrap_or(path);
    valid_name(scheme)
        && !scheme.contains('.')
        && !path.is_empty()
        && path.split('/').all(|seg| {
            !seg.is_empty()
                && seg
                    .chars()
                    .all(|c| c.is_ascii_alphanumeric() || c == '_' || c == '-')
        })
}

/// Bake module files into the binary at build time, as `(relative path,
/// source)` pairs for [`CodeMode::register_modules`]. Paths are relative to
/// the crate root (`CARGO_MANIFEST_DIR`):
///
/// ```ignore
/// cm.register_modules("mycli", clicodemode::embed_modules!("js", ["layout.js", "util/math.js"]))?;
/// ```
#[macro_export]
macro_rules! embed_modules {
    ($dir:literal, [$($file:literal),+ $(,)?]) => {
        &[$(($file, include_str!(concat!(env!("CARGO_MANIFEST_DIR"), "/", $dir, "/", $file)))),+]
    };
}

fn valid_name(n: &str) -> bool {
    !n.is_empty()
        && n.split('.').all(|seg| {
            let mut c = seg.chars();
            matches!(c.next(), Some(ch) if ch.is_ascii_alphabetic())
                && c.all(|ch| ch.is_ascii_alphanumeric() || ch == '_' || ch == '-')
        })
}

#[cfg(test)]
mod tests {
    use super::*;

    /// A session whose "guest" is the test itself: it feeds `lines` to the
    /// host and then hangs up.
    fn fake(lines: &[&str]) -> Session {
        let (to_guest, rx) = channel::<Vec<u8>>();
        std::mem::forget(rx); // keep the host->guest pipe open so sends succeed
        let (tx, from_guest) = channel::<Vec<u8>>();
        for l in lines {
            tx.send(l.as_bytes().to_vec()).unwrap();
        }
        drop(tx);
        Session {
            to_guest: Some(to_guest),
            from_guest,
            exit: Arc::new(Mutex::new(Some("guest vanished".into()))),
            thread: None,
            next_id: 0,
        }
    }

    fn req(s: &mut Session) -> Result<Value, Error> {
        s.request("run", json!({}), &HashMap::new(), &mut String::new())
    }

    #[test]
    fn garbage_lines_are_skipped_and_the_reply_is_found() {
        let mut s = fake(&[
            "not json at all",
            "{\"unrelated\":1}",
            "{\"id\":99,\"ok\":true,\"result\":1}",
            "{\"id\":1,\"ok\":true,\"result\":{\"a\":2}}",
        ]);
        assert_eq!(req(&mut s).unwrap(), json!({"a": 2}));
    }

    #[test]
    fn guest_hangup_is_a_runtime_error_not_a_panic() {
        let mut s = fake(&["garbage"]);
        match req(&mut s) {
            Err(Error::Runtime(m)) => assert!(m.contains("guest vanished"), "{m}"),
            other => panic!("{other:?}"),
        }
    }

    #[test]
    fn error_reply_is_an_engine_error() {
        let mut s = fake(&["{\"id\":1,\"ok\":false,\"error\":\"boom\"}"]);
        assert!(matches!(req(&mut s), Err(Error::Engine(m)) if m == "boom"));
    }

    #[test]
    fn call_for_an_unknown_operation_is_answered_with_an_error() {
        let mut s = fake(&["{\"call\":{\"id\":7,\"name\":\"nope.x\",\"args\":{}}}"]);
        // the reply goes to the (unread) guest channel; the loop then hits the hangup
        assert!(matches!(req(&mut s), Err(Error::Runtime(_))));
    }

    #[test]
    fn wrong_shaped_result_is_a_json_error_not_a_panic() {
        // a run reply whose `ok` is a string cannot become a RunResult
        let mut cm = CodeMode::new("1").unwrap();
        cm.session = Some(fake(&[
            "{\"id\":1,\"ok\":true,\"result\":{\"ok\":\"yes\",\"ms\":\"slow\"}}",
        ]));
        assert!(matches!(
            cm.run("1", RunOptions::default()),
            Err(Error::Json(_))
        ));
        let mut cm = CodeMode::new("1").unwrap();
        cm.session = Some(fake(&["{\"id\":1,\"ok\":true,\"result\":42}"]));
        assert!(matches!(cm.capabilities(), Err(Error::Json(_))));
    }

    #[test]
    fn dead_session_is_dropped_so_the_next_call_restarts() {
        let mut cm = CodeMode::new("1").unwrap();
        cm.session = Some(fake(&[]));
        assert!(matches!(cm.eval("1"), Err(Error::Runtime(_))));
        assert!(cm.session.is_none());
        assert_eq!(cm.eval("1 + 1").unwrap().value, json!(2)); // fresh engine
    }
}

/// Build-script helper: bake a whole folder of modules into the binary.
///
/// In `build.rs`:
///
/// ```ignore
/// fn main() { clicodemode::build::embed_dir("js", "modules.rs").unwrap(); }
/// ```
///
/// and in your code:
///
/// ```ignore
/// cm.register_modules("mycli", include!(concat!(env!("OUT_DIR"), "/modules.rs")))?;
/// ```
///
/// Every `.js`/`.json` file under `dir` (relative to the crate root) becomes an
/// `include_str!` entry, so the sources are compiled in and the folder is not
/// read at run time. Cargo rebuilds when the folder changes.
pub mod build {
    use std::path::{Path, PathBuf};

    pub fn embed_dir(dir: &str, out_file: &str) -> std::io::Result<()> {
        let manifest =
            PathBuf::from(std::env::var("CARGO_MANIFEST_DIR").unwrap_or_else(|_| ".".into()));
        let root = manifest.join(dir);
        let mut files = Vec::new();
        collect(&root, &root, &mut files)?;
        files.sort();
        let mut src = String::from("&[\n");
        for (rel, abs) in &files {
            src.push_str(&format!("    ({rel:?}, include_str!({abs:?})),\n"));
        }
        src.push_str("]\n");
        let out =
            PathBuf::from(std::env::var("OUT_DIR").unwrap_or_else(|_| ".".into())).join(out_file);
        std::fs::write(out, src)?;
        println!("cargo:rerun-if-changed={}", root.display());
        Ok(())
    }

    fn collect(root: &Path, d: &Path, out: &mut Vec<(String, String)>) -> std::io::Result<()> {
        for e in std::fs::read_dir(d)? {
            let p = e?.path();
            if p.is_dir() {
                collect(root, &p, out)?;
            } else if matches!(p.extension().and_then(|x| x.to_str()), Some("js" | "json")) {
                let rel = p
                    .strip_prefix(root)
                    .unwrap_or(&p)
                    .to_string_lossy()
                    .replace('\\', "/");
                out.push((rel, p.canonicalize()?.to_string_lossy().into_owned()));
            }
        }
        Ok(())
    }
}
