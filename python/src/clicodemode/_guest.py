"""Hosts the Go engine (compiled to WASI preview1) inside wasmtime.

The guest is a ``wasip1`` command that reads JSON lines on stdin and writes
JSON lines on stdout. Instead of real file descriptors this module implements
the ~20 WASI functions the Go runtime imports, with stdin/stdout as queues:
``fd_read`` blocks the *guest worker thread* until the host sends a line and
``fd_write`` hands finished lines to the host. There is no filesystem,
environment or network. This is the same idea as ``rust/src/guest.rs``.

One :class:`Guest` is one running engine instance on its own worker thread.
Stopping a runaway program uses wasmtime epoch interruption (a per-engine
counter), so every guest owns its own ``wasmtime.Engine``; the expensive part,
the compiled module, is shared process-wide through ``Module.serialize`` /
``Module.deserialize`` (see :func:`load_module`).
"""

from __future__ import annotations

import os
import queue
import struct
import threading
import time
from pathlib import Path
from typing import Any, Callable, List, Optional, Tuple, Union

import wasmtime
from wasmtime import Caller, Config, Engine, FuncType, Linker, Memory, Module, Store, Trap, ValType

WASM_PATH = Path(__file__).with_name("_codemode.wasm")

ESUCCESS = 0
EBADF = 8
EINVAL = 28
ENOSYS = 52

_i32 = ValType.i32
_i64 = ValType.i64


class GuestExit:
    """Sentinel put on the output queue when the guest stops."""

    def __init__(self, message: Optional[str]) -> None:
        self.message = message


class _Exit(Exception):
    def __init__(self, code: int) -> None:
        super().__init__(f"proc_exit({code})")
        self.code = code


def _config() -> Config:
    cfg = Config()
    cfg.epoch_interruption = True
    return cfg


# --------------------------------------------------------------------------
# Module cache: compile once per process, deserialize per engine.
# --------------------------------------------------------------------------

_mod_lock = threading.Lock()
_serialized: Optional[bytes] = None
_compile_ms: float = 0.0


def load_module(engine: Engine) -> Tuple[Module, bool]:
    """Return a module for ``engine`` and whether this call paid the compile.

    The first call in the process compiles the wasm with Cranelift and keeps
    the serialized artifact; later calls only deserialize it (an mmap-sized
    cost), which is what makes creating many engines cheap.
    """
    global _serialized, _compile_ms
    with _mod_lock:
        if _serialized is None:
            if not WASM_PATH.exists():
                raise RuntimeError(
                    f"{WASM_PATH.name} is missing from the package; build it with "
                    "python/scripts/build_wasm.sh (a published wheel already contains it)"
                )
            t0 = time.perf_counter()
            module = Module(engine, WASM_PATH.read_bytes())
            _serialized = bytes(module.serialize())
            _compile_ms = (time.perf_counter() - t0) * 1000
            return module, True
        blob = _serialized
    return Module.deserialize(engine, blob), False


def last_compile_ms() -> float:
    return _compile_ms


# --------------------------------------------------------------------------


class Guest:
    """One engine instance running on a worker thread."""

    def __init__(self) -> None:
        self.inq: "queue.Queue[Optional[bytes]]" = queue.Queue()
        self.outq: "queue.Queue[Union[bytes, GuestExit]]" = queue.Queue()
        self._kill = threading.Event()
        self._inbuf = b""
        self._inpos = 0
        self._outbuf = bytearray()
        self._stderr = bytearray()
        self._start = time.monotonic()
        self._rng = int.from_bytes(os.urandom(8), "little") or 1
        self.exit_message: Optional[str] = None
        self.engine = Engine(_config())
        self.compiled_cold = False
        t0 = time.perf_counter()
        self._module, self.compiled_cold = load_module(self.engine)
        self.load_ms = (time.perf_counter() - t0) * 1000
        self._ready = threading.Event()
        old = threading.stack_size()
        try:
            threading.stack_size(16 << 20)
        except (ValueError, RuntimeError):  # pragma: no cover - platform limit
            pass
        try:
            self._thread = threading.Thread(target=self._main, name="clicodemode-engine", daemon=True)
            self._thread.start()
        finally:
            try:
                threading.stack_size(old)
            except (ValueError, RuntimeError):  # pragma: no cover
                pass

    # ---- host-facing API -------------------------------------------------

    def send(self, line: bytes) -> None:
        self.inq.put(line if line.endswith(b"\n") else line + b"\n")

    def alive(self) -> bool:
        return self._thread.is_alive()

    def stderr_tail(self) -> str:
        return bytes(self._stderr[-2000:]).decode("utf-8", "replace")

    def stop(self, hard: bool = False) -> None:
        """End the guest. Soft: EOF on stdin. Hard: also interrupt running code."""
        self._kill.set()
        self.inq.put(None)
        if hard:
            self.engine.increment_epoch()
        self._thread.join(timeout=10)

    # ---- worker thread ---------------------------------------------------

    def _main(self) -> None:
        msg: Optional[str] = None
        try:
            store = Store(self.engine)
            store.set_epoch_deadline(1)  # first increment_epoch() traps the guest
            linker = self._linker()
            instance = linker.instantiate(store, self._module)
            start = instance.exports(store)["_start"]
            start(store)  # type: ignore[operator]
        except _Exit as e:
            if e.code != 0:
                msg = f"engine exited with status {e.code}"
        except Trap as e:
            if self._kill.is_set():
                msg = "engine interrupted"
            else:
                msg = f"engine trapped: {str(e).splitlines()[0] if str(e) else 'trap'}"
        except wasmtime.WasmtimeError as e:
            c = getattr(e, "__cause__", None)
            if isinstance(c, _Exit):
                msg = None if c.code == 0 else f"engine exited with status {c.code}"
            elif self._kill.is_set():
                msg = "engine interrupted"
            else:
                msg = f"engine error: {e}"
        except BaseException as e:  # noqa: BLE001 - must not escape the thread
            if self._kill.is_set():
                msg = "engine interrupted"
            else:
                msg = f"engine error: {type(e).__name__}: {e}"
        tail = self.stderr_tail().strip()
        if msg and tail:
            msg += f" (stderr: {tail[-400:]})"
        self.exit_message = msg
        self.outq.put(GuestExit(msg))

    # ---- WASI preview1 ---------------------------------------------------

    def _linker(self) -> Linker:
        lk = Linker(self.engine)
        M = "wasi_snapshot_preview1"

        def fn(name: str, params: List[ValType], results: List[ValType], f: Callable[..., Any]) -> None:
            lk.define_func(M, name, FuncType(params, results), f, access_caller=True)

        def mem(c: Caller) -> Memory:
            m = c.get("memory")
            assert isinstance(m, Memory)
            return m

        def w32(c: Caller, p: int, v: int) -> None:
            mem(c).write(c, struct.pack("<I", v & 0xFFFFFFFF), p)

        def w64(c: Caller, p: int, v: int) -> None:
            mem(c).write(c, struct.pack("<Q", v & 0xFFFFFFFFFFFFFFFF), p)

        def iovs(c: Caller, p: int, n: int) -> List[Tuple[int, int]]:
            raw = mem(c).read(c, p, p + 8 * n)
            return [struct.unpack_from("<II", raw, 8 * i) for i in range(n)]

        fn("sched_yield", [], [_i32()], lambda c: ESUCCESS)

        def proc_exit(c: Caller, code: int) -> None:
            raise _Exit(code)

        fn("proc_exit", [_i32()], [], proc_exit)

        def args_sizes_get(c: Caller, argc: int, size: int) -> int:
            w32(c, argc, 1)
            w32(c, size, 5)
            return ESUCCESS

        fn("args_sizes_get", [_i32(), _i32()], [_i32()], args_sizes_get)

        def args_get(c: Caller, argv: int, buf: int) -> int:
            w32(c, argv, buf)
            mem(c).write(c, b"code\0", buf)
            return ESUCCESS

        fn("args_get", [_i32(), _i32()], [_i32()], args_get)

        def environ_sizes_get(c: Caller, n: int, size: int) -> int:
            w32(c, n, 0)
            w32(c, size, 0)
            return ESUCCESS

        fn("environ_sizes_get", [_i32(), _i32()], [_i32()], environ_sizes_get)
        fn("environ_get", [_i32(), _i32()], [_i32()], lambda c, a, b: ESUCCESS)

        def clock_time_get(c: Caller, cid: int, _prec: int, out: int) -> int:
            if cid == 0:
                ns = time.time_ns()
            elif 1 <= cid <= 3:
                ns = int((time.monotonic() - self._start) * 1e9) + 1_000_000_000
            else:
                return EINVAL
            w64(c, out, ns)
            return ESUCCESS

        fn("clock_time_get", [_i32(), _i64(), _i32()], [_i32()], clock_time_get)

        def random_get(c: Caller, buf: int, n: int) -> int:
            mem(c).write(c, os.urandom(n), buf)
            return ESUCCESS

        fn("random_get", [_i32(), _i32()], [_i32()], random_get)

        def fd_write(c: Caller, fd: int, iov: int, n: int, nw: int) -> int:
            if fd not in (1, 2):
                return EBADF
            m = mem(c)
            total = 0
            for ptr, ln in iovs(c, iov, n):
                chunk = m.read(c, ptr, ptr + ln)
                total += ln
                if fd == 2:
                    self._stderr += chunk
                    if len(self._stderr) > 8000:
                        del self._stderr[:-4000]
                else:
                    self._outbuf += chunk
            while True:
                i = self._outbuf.find(b"\n")
                if i < 0:
                    break
                line = bytes(self._outbuf[: i + 1])
                del self._outbuf[: i + 1]
                self.outq.put(line)
            w32(c, nw, total)
            return ESUCCESS

        fn("fd_write", [_i32()] * 4, [_i32()], fd_write)

        def fd_read(c: Caller, fd: int, iov: int, n: int, nr: int) -> int:
            if fd != 0:
                return EBADF
            if self._inpos >= len(self._inbuf):
                b = self.inq.get()
                if b is None:  # host went away: EOF
                    w32(c, nr, 0)
                    return ESUCCESS
                self._inbuf, self._inpos = b, 0
            m = mem(c)
            total = 0
            for ptr, ln in iovs(c, iov, n):
                take = min(ln, len(self._inbuf) - self._inpos)
                if take:
                    m.write(c, self._inbuf[self._inpos : self._inpos + take], ptr)
                self._inpos += take
                total += take
            w32(c, nr, total)
            return ESUCCESS

        fn("fd_read", [_i32()] * 4, [_i32()], fd_read)
        fn("fd_close", [_i32()], [_i32()], lambda c, fd: ESUCCESS)

        def fd_fdstat_get(c: Caller, fd: int, out: int) -> int:
            if not 0 <= fd <= 2:
                return EBADF
            mem(c).write(c, bytes([2]) + bytes(23), out)  # character device
            return ESUCCESS

        fn("fd_fdstat_get", [_i32(), _i32()], [_i32()], fd_fdstat_get)
        fn("fd_fdstat_set_flags", [_i32(), _i32()], [_i32()], lambda c, a, b: ENOSYS)
        fn("fd_filestat_get", [_i32(), _i32()], [_i32()], lambda c, a, b: EBADF)
        fn("fd_prestat_get", [_i32(), _i32()], [_i32()], lambda c, a, b: EBADF)
        fn("fd_prestat_dir_name", [_i32()] * 3, [_i32()], lambda c, a, b, d: EBADF)
        fn("path_filestat_get", [_i32()] * 5, [_i32()], lambda c, a, b, d, e, f: ENOSYS)
        fn("path_readlink", [_i32()] * 6, [_i32()], lambda c, a, b, d, e, f, g: ENOSYS)
        fn(
            "path_open",
            [_i32(), _i32(), _i32(), _i32(), _i32(), _i64(), _i64(), _i32(), _i32()],
            [_i32()],
            lambda c, a, b, d, e, f, g, h, i, j: ENOSYS,
        )

        def poll_oneoff(c: Caller, subs: int, events: int, n: int, out: int) -> int:
            # only timers are used (time.Sleep, scheduler idle)
            m = mem(c)
            raw = m.read(c, subs, subs + 48 * n)
            now_real = time.time_ns()
            now_mono = int((time.monotonic() - self._start) * 1e9) + 1_000_000_000
            wait: Optional[int] = None
            first: Optional[int] = None
            for i in range(n):
                o = 48 * i
                if raw[o + 8] == 0:
                    cid = struct.unpack_from("<I", raw, o + 16)[0]
                    timeout = struct.unpack_from("<Q", raw, o + 24)[0]
                    absolute = struct.unpack_from("<I", raw, o + 40)[0] & 1
                    rel = max(0, timeout - (now_real if cid == 0 else now_mono)) if absolute else timeout
                    if wait is None or rel < wait:
                        wait, first = rel, i
            if first is None:
                return ENOSYS
            self._kill.wait((wait or 0) / 1e9)  # wakes early when the guest is being stopped
            ud = struct.unpack_from("<Q", raw, 48 * first)[0]
            m.write(c, struct.pack("<Q", ud) + bytes(24), events)  # clock event, errno 0
            w32(c, out, 1)
            return ESUCCESS

        fn("poll_oneoff", [_i32()] * 4, [_i32()], poll_oneoff)
        return lk


def preload() -> None:  # pragma: no cover - convenience for benchmarks
    """Compile the module now so the first engine does not pay for it."""
    e = Engine(_config())
    load_module(e)


__all__ = ["Guest", "GuestExit", "WASM_PATH", "load_module", "preload", "last_compile_ms"]
