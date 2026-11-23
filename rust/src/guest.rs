//! The WASI side: runs the embedded Go engine on its own thread with a
//! minimal WASI preview1 implementation whose stdin/stdout are channels.
//! There is no filesystem, no environment and no network.

use std::collections::hash_map::RandomState;
use std::hash::{BuildHasher, Hasher};
use std::io::Write;
use std::sync::mpsc::{Receiver, Sender};
use std::sync::{Arc, Mutex, OnceLock};
use std::time::{Duration, Instant, SystemTime, UNIX_EPOCH};

use wasmi::{Caller, Engine, Extern, Linker, Memory, Module, Store};

static WASM: &[u8] = include_bytes!("../assets/codemode.wasm");
static MODULE: OnceLock<Result<(Engine, Module), String>> = OnceLock::new();

fn module() -> Result<&'static (Engine, Module), String> {
    MODULE
        .get_or_init(|| {
            let engine = Engine::default();
            Module::new(&engine, WASM)
                .map(|m| (engine, m))
                .map_err(|e| format!("cannot load the embedded engine: {e}"))
        })
        .as_ref()
        .map_err(|e| e.clone())
}

struct State {
    rx: Receiver<Vec<u8>>,
    tx: Sender<Vec<u8>>,
    inbuf: Vec<u8>,
    inpos: usize,
    outbuf: Vec<u8>,
    start: Instant,
    rng: u64,
}

const ESUCCESS: i32 = 0;
const EBADF: i32 = 8;
const EINVAL: i32 = 28;
const ENOSYS: i32 = 52;

fn mem(c: &mut Caller<'_, State>) -> Memory {
    match c.get_export("memory") {
        Some(Extern::Memory(m)) => m,
        _ => panic!("guest has no memory export"),
    }
}

fn rd32(m: &[u8], p: usize) -> u32 {
    u32::from_le_bytes(m[p..p + 4].try_into().unwrap())
}
fn rd64(m: &[u8], p: usize) -> u64 {
    u64::from_le_bytes(m[p..p + 8].try_into().unwrap())
}
fn wr32(m: &mut [u8], p: usize, v: u32) {
    m[p..p + 4].copy_from_slice(&v.to_le_bytes());
}
fn wr64(m: &mut [u8], p: usize, v: u64) {
    m[p..p + 8].copy_from_slice(&v.to_le_bytes());
}

fn next_rand(s: &mut State) -> u64 {
    // splitmix64
    s.rng = s.rng.wrapping_add(0x9E3779B97F4A7C15);
    let mut z = s.rng;
    z = (z ^ (z >> 30)).wrapping_mul(0xBF58476D1CE4E5B9);
    z = (z ^ (z >> 27)).wrapping_mul(0x94D049BB133111EB);
    z ^ (z >> 31)
}

fn e<E: ToString>(r: Result<&mut Linker<State>, E>) -> Result<(), String> {
    r.map(|_| ()).map_err(|e| e.to_string())
}

fn linker(engine: &Engine) -> Result<Linker<State>, String> {
    let mut l: Linker<State> = Linker::new(engine);
    let m = "wasi_snapshot_preview1";

    e(l.func_wrap(m, "sched_yield", |_c: Caller<'_, State>| -> i32 { ESUCCESS }))?;
    e(l.func_wrap(m, "proc_exit", |_c: Caller<'_, State>, code: i32| -> Result<(), wasmi::Error> {
        Err(wasmi::Error::i32_exit(code))
    }))?;
    e(l.func_wrap(m, "args_sizes_get", |mut c: Caller<'_, State>, argc: i32, size: i32| -> i32 {
        let mm = mem(&mut c);
        let d = mm.data_mut(&mut c);
        wr32(d, argc as usize, 1);
        wr32(d, size as usize, 5);
        ESUCCESS
    }))?;
    e(l.func_wrap(m, "args_get", |mut c: Caller<'_, State>, argv: i32, buf: i32| -> i32 {
        let mm = mem(&mut c);
        let d = mm.data_mut(&mut c);
        wr32(d, argv as usize, buf as u32);
        d[buf as usize..buf as usize + 5].copy_from_slice(b"code\0");
        ESUCCESS
    }))?;
    e(l.func_wrap(m, "environ_sizes_get", |mut c: Caller<'_, State>, n: i32, size: i32| -> i32 {
        let mm = mem(&mut c);
        let d = mm.data_mut(&mut c);
        wr32(d, n as usize, 0);
        wr32(d, size as usize, 0);
        ESUCCESS
    }))?;
    e(l.func_wrap(m, "environ_get", |_c: Caller<'_, State>, _a: i32, _b: i32| -> i32 { ESUCCESS }))?;
    e(l.func_wrap(m, "clock_time_get", |mut c: Caller<'_, State>, id: i32, _prec: i64, out: i32| -> i32 {
        let ns = match id {
            0 => SystemTime::now().duration_since(UNIX_EPOCH).map(|d| d.as_nanos() as u64).unwrap_or(0),
            1..=3 => c.data().start.elapsed().as_nanos() as u64 + 1_000_000_000,
            _ => return EINVAL,
        };
        let mm = mem(&mut c);
        wr64(mm.data_mut(&mut c), out as usize, ns);
        ESUCCESS
    }))?;
    e(l.func_wrap(m, "random_get", |mut c: Caller<'_, State>, buf: i32, len: i32| -> i32 {
        let mm = mem(&mut c);
        let (d, st) = mm.data_and_store_mut(&mut c);
        for (i, b) in d[buf as usize..(buf + len) as usize].iter_mut().enumerate() {
            if i % 8 == 0 {
                let _ = next_rand(st);
            }
            *b = (st.rng >> ((i % 8) * 8)) as u8;
        }
        ESUCCESS
    }))?;
    e(l.func_wrap(m, "fd_write", |mut c: Caller<'_, State>, fd: i32, iovs: i32, n: i32, nw: i32| -> i32 {
        if !(1..=2).contains(&fd) {
            return EBADF;
        }
        let mm = mem(&mut c);
        let (d, st) = mm.data_and_store_mut(&mut c);
        let mut total = 0usize;
        for i in 0..n as usize {
            let p = iovs as usize + i * 8;
            let (ptr, len) = (rd32(d, p) as usize, rd32(d, p + 4) as usize);
            let chunk = &d[ptr..ptr + len];
            total += len;
            if fd == 2 {
                let _ = std::io::stderr().write_all(chunk);
            } else {
                st.outbuf.extend_from_slice(chunk);
            }
        }
        while let Some(i) = st.outbuf.iter().position(|b| *b == b'\n') {
            let line: Vec<u8> = st.outbuf.drain(..=i).collect();
            let _ = st.tx.send(line);
        }
        wr32(d, nw as usize, total as u32);
        ESUCCESS
    }))?;
    e(l.func_wrap(m, "fd_read", |mut c: Caller<'_, State>, fd: i32, iovs: i32, n: i32, nr: i32| -> i32 {
        if fd != 0 {
            return EBADF;
        }
        let mm = mem(&mut c);
        let (d, st) = mm.data_and_store_mut(&mut c);
        if st.inpos >= st.inbuf.len() {
            match st.rx.recv() {
                Ok(b) => {
                    st.inbuf = b;
                    st.inpos = 0;
                }
                Err(_) => {
                    wr32(d, nr as usize, 0); // EOF: the host dropped us
                    return ESUCCESS;
                }
            }
        }
        let mut total = 0usize;
        for i in 0..n as usize {
            let p = iovs as usize + i * 8;
            let (ptr, len) = (rd32(d, p) as usize, rd32(d, p + 4) as usize);
            let take = len.min(st.inbuf.len() - st.inpos);
            d[ptr..ptr + take].copy_from_slice(&st.inbuf[st.inpos..st.inpos + take]);
            st.inpos += take;
            total += take;
        }
        wr32(d, nr as usize, total as u32);
        ESUCCESS
    }))?;
    e(l.func_wrap(m, "fd_close", |_c: Caller<'_, State>, _fd: i32| -> i32 { ESUCCESS }))?;
    e(l.func_wrap(m, "fd_fdstat_get", |mut c: Caller<'_, State>, fd: i32, out: i32| -> i32 {
        if !(0..=2).contains(&fd) {
            return EBADF;
        }
        let mm = mem(&mut c);
        let d = mm.data_mut(&mut c);
        let o = out as usize;
        d[o..o + 24].fill(0);
        d[o] = 2; // character device
        ESUCCESS
    }))?;
    e(l.func_wrap(m, "fd_fdstat_set_flags", |_c: Caller<'_, State>, _fd: i32, _f: i32| -> i32 { ENOSYS }))?;
    e(l.func_wrap(m, "fd_filestat_get", |_c: Caller<'_, State>, _fd: i32, _o: i32| -> i32 { EBADF }))?;
    e(l.func_wrap(m, "fd_prestat_get", |_c: Caller<'_, State>, _fd: i32, _o: i32| -> i32 { EBADF }))?;
    e(l.func_wrap(m, "fd_prestat_dir_name", |_c: Caller<'_, State>, _fd: i32, _p: i32, _l: i32| -> i32 { EBADF }))?;
    e(l.func_wrap(
        m,
        "path_filestat_get",
        |_c: Caller<'_, State>, _a: i32, _b: i32, _p: i32, _l: i32, _o: i32| -> i32 { ENOSYS },
    ))?;
    e(l.func_wrap(
        m,
        "path_readlink",
        |_c: Caller<'_, State>, _a: i32, _p: i32, _l: i32, _b: i32, _bl: i32, _o: i32| -> i32 { ENOSYS },
    ))?;
    e(l.func_wrap(
        m,
        "path_open",
        |_c: Caller<'_, State>, _a: i32, _b: i32, _p: i32, _l: i32, _c2: i32, _r: i64, _i: i64, _f: i32, _o: i32| -> i32 {
            ENOSYS
        },
    ))?;
    // poll_oneoff: only used for timers (time.Sleep, scheduler idle).
    e(l.func_wrap(m, "poll_oneoff", |mut c: Caller<'_, State>, subs: i32, events: i32, n: i32, out: i32| -> i32 {
        let mm = mem(&mut c);
        let (d, st) = mm.data_and_store_mut(&mut c);
        let mut wait: Option<u64> = None;
        let mut first: Option<usize> = None;
        let now_real = SystemTime::now().duration_since(UNIX_EPOCH).map(|d| d.as_nanos() as u64).unwrap_or(0);
        let now_mono = st.start.elapsed().as_nanos() as u64 + 1_000_000_000;
        for i in 0..n as usize {
            let p = subs as usize + i * 48;
            if d[p + 8] == 0 {
                let id = rd32(d, p + 16);
                let timeout = rd64(d, p + 24);
                let abs = rd32(d, p + 40) & 1 == 1;
                let rel = if abs { timeout.saturating_sub(if id == 0 { now_real } else { now_mono }) } else { timeout };
                if wait.is_none_or(|w| rel < w) {
                    wait = Some(rel);
                    first = Some(i);
                }
            }
        }
        let Some(i) = first else { return ENOSYS };
        std::thread::sleep(Duration::from_nanos(wait.unwrap_or(0)));
        let p = subs as usize + i * 48;
        let ud = rd64(d, p);
        let o = events as usize;
        d[o..o + 32].fill(0);
        wr64(d, o, ud);
        d[o + 10] = 0; // clock event
        wr32(d, out as usize, 1);
        ESUCCESS
    }))?;
    Ok(l)
}

/// Run the engine until the host drops its sender (EOF) or the guest exits.
/// Returns a message if it ended abnormally.
pub fn run(rx: Receiver<Vec<u8>>, tx: Sender<Vec<u8>>, exit: Arc<Mutex<Option<String>>>) {
    let r = (|| -> Result<(), String> {
        let (engine, module) = module()?;
        let linker = linker(engine)?;
        let seed = {
            let mut h = RandomState::new().build_hasher();
            h.write_u64(SystemTime::now().duration_since(UNIX_EPOCH).map(|d| d.as_nanos() as u64).unwrap_or(1));
            h.finish()
        };
        let mut store = Store::new(
            engine,
            State { rx, tx, inbuf: Vec::new(), inpos: 0, outbuf: Vec::new(), start: Instant::now(), rng: seed },
        );
        let inst = linker
            .instantiate_and_start(&mut store, module)
            .map_err(|e| e.to_string())?;
        let start = inst.get_typed_func::<(), ()>(&store, "_start").map_err(|e| e.to_string())?;
        match start.call(&mut store, ()) {
            Ok(()) => Ok(()),
            Err(e) => match e.i32_exit_status() {
                Some(0) => Ok(()),
                Some(c) => Err(format!("engine exited with status {c}")),
                None => Err(e.to_string()),
            },
        }
    })();
    if let Err(msg) = r {
        *exit.lock().unwrap() = Some(msg);
    }
}
