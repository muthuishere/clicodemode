"""The :class:`CodeMode` engine: register Python handlers, run programs in Go/WASM."""

from __future__ import annotations

import inspect
import json
import queue
import threading
import time
from typing import Any, Callable, Dict, List, Optional, TypeVar

from ._guest import Guest, GuestExit
from .errors import EngineError, GuestCrashed, UsageError
from .types import (
    SIDE_EFFECTS,
    Capabilities,
    CompileResult,
    Diagnostic,
    Limits,
    Operation,
    Policy,
    Result,
)

Handler = Callable[[Dict[str, Any]], Any]
F = TypeVar("F", bound=Callable[..., Any])

# Extra time the host grants the engine's own timeout (Limits.timeout_ms) to
# end the run gracefully before the guest is interrupted from outside.
_GRACE_S = 1.0


class _Aborted(Exception):
    def __init__(self, reason: str) -> None:
        super().__init__(reason)
        self.reason = reason


def valid_name(n: str) -> bool:
    """Dotted segments of ASCII letters, digits, _ and -, each starting with a
    letter: ``health.check``, ``db.list-tables``, ``gh.listPRs`` (case-sensitive)."""
    if not n:
        return False
    for seg in n.split("."):
        if not seg or not (seg[0].isascii() and seg[0].isalpha()):
            return False
        if not all((c.isascii() and c.isalnum()) or c in "_-" for c in seg):
            return False
    return True


class CodeMode:
    """One Code Mode engine bound to a set of registered operations.

    Each instance owns its own WebAssembly instance, so two of them never
    share operations or state. The engine starts lazily on first use;
    registering another operation restarts it with the new contract.

    Thread-safety: an instance serialises its public methods with a lock, so
    sharing one across threads is safe but runs one call at a time.
    ``cancel()`` is the only method meant to be called from another thread
    while a run is in flight. Handlers run synchronously on the thread that
    called ``run``; use separate instances for real parallelism.
    """

    def __init__(
        self,
        version: str = "0.0.0",
        *,
        limits: Optional[Limits] = None,
        policy: Optional[Policy] = None,
    ) -> None:
        self.version = version
        self.limits = limits or Limits()
        self.policy = policy or Policy()
        self._ops: Dict[str, Operation] = {}
        self._handlers: Dict[str, Handler] = {}
        self._guest: Optional[Guest] = None
        self._next_id = 0
        self._lock = threading.RLock()
        self._busy = threading.local()
        self._cancel_now = threading.Event()  # set by cancel(), cleared when a run starts
        #: guests started over this engine's life (1 + number of restarts)
        self.guests_started = 0

    # ------------------------------------------------------------------ registration

    def register(self, op: Operation, handler: Handler) -> None:
        """Register ``handler(args: dict) -> JSON-serialisable`` as operation ``op``."""
        with self._lock:
            if not valid_name(op.name):
                raise UsageError(
                    f"invalid operation name {op.name!r} (want dotted segments of letters, digits, _ and -, e.g. health.check)"
                )
            if op.name in self._ops:
                raise UsageError(f"operation {op.name!r} already registered")
            if op.side_effects not in SIDE_EFFECTS:
                raise UsageError(f"side_effects must be one of {SIDE_EFFECTS}, got {op.side_effects!r}")
            if not callable(handler):
                raise UsageError(f"handler for {op.name!r} is not callable")
            self._ops[op.name] = op
            self._handlers[op.name] = handler
            self._drop_guest()  # restart with the new contract

    def operation(
        self,
        name: str,
        *,
        description: Optional[str] = None,
        input_schema: Optional[Dict[str, Any]] = None,
        output_schema: Optional[Dict[str, Any]] = None,
        side_effects: str = "local",
        idempotent: bool = False,
        authorization: str = "",
        examples: Optional[List[Dict[str, Any]]] = None,
    ) -> Callable[[F], F]:
        """Decorator registering ``fn(args: dict) -> Any`` as operation ``name``."""

        def deco(fn: F) -> F:
            desc = description if description is not None else (inspect.getdoc(fn) or "").split("\n\n")[0]
            self.register(
                Operation(
                    name,
                    desc.strip(),
                    input_schema,
                    output_schema,
                    side_effects,
                    idempotent,
                    authorization,
                    list(examples or []),
                ),
                fn,
            )
            return fn

        return deco

    def register_function(self, name: str, fn: Callable[..., Any], **meta: Any) -> None:
        """Register an ordinary function; the JS ``args`` object becomes keyword arguments."""
        from .adapters import register_function

        register_function(self, name, fn, **meta)

    def register_click(self, command: Any, prefix: str = "", **kw: Any) -> List[str]:
        """Register a Click command/group's commands. Returns the operation names."""
        from .adapters import register_click

        return register_click(self, command, prefix, **kw)

    def register_typer(self, app: Any, prefix: str = "", **kw: Any) -> List[str]:
        """Register a Typer app's commands (via the Click command Typer builds)."""
        from .adapters import register_typer

        return register_typer(self, app, prefix, **kw)

    def register_argparse(self, parser: Any, prefix: str = "", **kw: Any) -> List[str]:
        """Register an argparse parser's sub-commands (leaf parsers with a ``func`` default)."""
        from .adapters import register_argparse

        return register_argparse(self, parser, prefix, **kw)

    # ------------------------------------------------------------------ guest lifecycle

    def _drop_guest(self) -> None:
        g, self._guest = self._guest, None
        if g is not None:
            g.stop(hard=True)

    def close(self) -> None:
        """Stop the engine. The instance can still be used; it restarts lazily."""
        with self._lock:
            self._drop_guest()

    def __enter__(self) -> "CodeMode":
        return self

    def __exit__(self, *exc: Any) -> None:
        self.close()

    def __del__(self) -> None:  # pragma: no cover - best effort
        try:
            g = self._guest
            if g is not None:
                g.stop(hard=True)
        except Exception:
            pass

    def _ensure_guest(self) -> Guest:
        if self._guest is not None and self._guest.alive():
            return self._guest
        self._guest = None
        g = Guest()
        self.guests_started += 1
        cfg = {
            "version": self.version,
            "operations": [o.to_json() for o in self._ops.values()],
            "limits": self.limits.to_json(),
        }
        self._guest = g
        try:
            self._roundtrip(g, "create", cfg, deadline=None, cancel=None)
        except BaseException:
            self._drop_guest()
            raise
        return g

    # ------------------------------------------------------------------ protocol

    def _send(self, g: Guest, obj: Dict[str, Any]) -> None:
        g.send(json.dumps(obj, ensure_ascii=False, allow_nan=False).encode("utf-8"))

    def _serve_call(self, g: Guest, call: Dict[str, Any], logs: List[str]) -> None:
        cid = call.get("id")
        name = call.get("name", "")
        args = call.get("args")
        if not isinstance(args, dict):
            args = {} if args is None else {"value": args}
        reply = self._invoke(name, args)
        reply["id"] = cid
        try:
            self._send(g, {"callResult": reply})
        except (TypeError, ValueError) as e:  # result not JSON-serialisable
            self._send(
                g,
                {
                    "callResult": {
                        "id": cid,
                        "ok": False,
                        "error": f"operation {name!r} returned a value that is not JSON-serialisable: {e}",
                    }
                },
            )

    def _invoke(self, name: str, args: Dict[str, Any]) -> Dict[str, Any]:
        op = self._ops.get(name)
        handler = self._handlers.get(name)
        if op is None or handler is None:
            return {"ok": False, "error": f"no handler for operation {name!r}"}
        allow = self.policy.allow
        if allow is not None:
            try:
                verdict = allow(op)
            except Exception as e:  # a raising policy denies, with its reason
                return {"ok": False, "error": f"{name}: denied by policy: {e}"}
            if verdict is False:
                return {"ok": False, "error": f"{name}: denied by policy"}
        try:
            value = handler(args)
        except Exception as e:  # handler failure -> rejected call, message preserved
            return {"ok": False, "error": str(e) or type(e).__name__}
        # validate JSON-ability here so the error names the operation
        try:
            json.dumps(value, ensure_ascii=False, allow_nan=False)
        except (TypeError, ValueError) as e:
            return {
                "ok": False,
                "error": f"operation {name!r} returned a value that is not JSON-serialisable: {e}",
            }
        return {"ok": True, "result": value}

    def _roundtrip(
        self,
        g: Guest,
        method: str,
        params: Any,
        *,
        deadline: Optional[float],
        cancel: Optional[threading.Event],
        logs: Optional[List[str]] = None,
    ) -> Any:
        """Send one request and serve the engine's operation calls until its reply."""
        if logs is None:
            logs = []
        self._next_id += 1
        rid = self._next_id
        self._send(g, {"id": rid, "method": method, "params": params})
        while True:
            self._check_abort(deadline, cancel)
            try:
                item = g.outq.get(timeout=0.05)
            except queue.Empty:
                continue
            if isinstance(item, GuestExit):
                self._guest = None
                raise GuestCrashed(item.message or "engine stopped")
            try:
                msg = json.loads(item)
            except ValueError:
                continue
            if "log" in msg:
                logs.append(msg["log"])
            elif "call" in msg:
                self._busy.in_handler = True
                try:
                    self._serve_call(g, msg["call"], logs)
                finally:
                    self._busy.in_handler = False
            elif msg.get("id") == rid:
                if msg.get("ok") is True:
                    return msg.get("result")
                raise EngineError(msg.get("error") or "unknown error")

    def _check_abort(self, deadline: Optional[float], cancel: Optional[threading.Event]) -> None:
        if self._cancel_now.is_set() or (cancel is not None and cancel.is_set()):
            raise _Aborted("cancelled")
        if deadline is not None and time.monotonic() >= deadline:
            raise _Aborted("timeout")

    def _call(
        self,
        method: str,
        params: Any = None,
        *,
        timeout: Optional[float] = None,
        cancel: Optional[threading.Event] = None,
        logs: Optional[List[str]] = None,
    ) -> Any:
        with self._lock:
            if getattr(self._busy, "in_handler", False):
                raise UsageError("this CodeMode engine is already running; a handler cannot re-enter it")
            self._cancel_now.clear()  # cancel() only affects the run in flight
            g = self._ensure_guest()
            deadline = None
            if timeout is not None:
                deadline = time.monotonic() + timeout
            elif self.limits.timeout_ms:
                deadline = time.monotonic() + self.limits.timeout_ms / 1000 + _GRACE_S
            try:
                return self._roundtrip(g, method, params, deadline=deadline, cancel=cancel, logs=logs)
            except _Aborted:
                # The only way to stop running guest code is to interrupt the
                # instance, which cannot be resumed: drop it; the next call
                # starts a fresh engine (a few ms, see README).
                self._drop_guest()
                raise
            except GuestCrashed:
                self._guest = None
                raise

    # ------------------------------------------------------------------ public API

    def _result(self, raw: Any, logs: List[str], t0: float) -> Result:
        diags = [Diagnostic.from_json(d) for d in (raw.get("diagnostics") or [])]
        return Result(
            ok=bool(raw.get("ok")),
            value=raw.get("value"),
            error=raw.get("error", "") or "",
            diagnostics=diags,
            stdout="".join(logs),
            ms=int(raw.get("ms", 0)) or int((time.perf_counter() - t0) * 1000),
        )

    def _execute(
        self,
        method: str,
        params: Dict[str, Any],
        timeout: Optional[float],
        cancel: Optional[threading.Event],
    ) -> Result:
        logs: List[str] = []
        t0 = time.perf_counter()
        try:
            raw = self._call(method, params, timeout=timeout, cancel=cancel, logs=logs)
        except _Aborted as a:
            why = (
                f"cancelled: the run was stopped by the host"
                if a.reason == "cancelled"
                else f"timeout: the run exceeded {timeout if timeout is not None else self.limits.timeout_ms / 1000:g}s and was stopped"
            )
            return Result(
                ok=False,
                error=why + " (the engine was restarted)",
                stdout="".join(logs),
                ms=int((time.perf_counter() - t0) * 1000),
            )
        return self._result(raw, logs, t0)

    def run(
        self,
        src: str,
        *,
        input: Any = None,
        file: str = "program.js",
        timeout: Optional[float] = None,
        cancel: Optional[threading.Event] = None,
    ) -> Result:
        """Run a program. ``timeout`` is in seconds (wall clock, includes handler time).

        A program that throws, is rejected or times out gives ``Result(ok=False)``;
        exceptions are only for misuse and a crashed engine. Interrupting a
        runaway program restarts the engine (the next call pays about 10 ms).
        """
        return self._execute("run", {"src": src, "file": file, "input": input}, timeout, cancel)

    def eval(
        self,
        expr: str,
        *,
        input: Any = None,
        timeout: Optional[float] = None,
        cancel: Optional[threading.Event] = None,
    ) -> Result:
        """Evaluate one expression."""
        return self._execute("eval", {"expr": expr, "input": input}, timeout, cancel)

    def cancel(self) -> None:
        """Stop the run in flight on this engine (callable from any thread).

        Takes effect within ~50 ms unless a Python handler is executing: handlers
        run on the caller's thread and cannot be pre-empted, so cancellation is
        applied as soon as the handler returns.
        """
        self._cancel_now.set()

    def check(self, src: str, file: str = "program.js") -> List[Diagnostic]:
        """Parse and check a program without running it (diagnostics are ``file:line:col``)."""
        with self._lock:
            raw = self._call("check", {"src": src, "file": file})
        return [Diagnostic.from_json(d) for d in (raw or [])]

    def compile(self, src: str, file: str = "program.js") -> CompileResult:
        """Check a program and return its plan (hash, operations used, cache key)."""
        with self._lock:
            raw = self._call("compile", {"src": src, "file": file})
        prog = raw.get("program") or {}
        return CompileResult(
            file=prog.get("file", file),
            hash=prog.get("hash", ""),
            imports=list(prog.get("imports") or []),
            operations=list(prog.get("operations") or []),
            cache_key=prog.get("cache_key", ""),
            diagnostics=[Diagnostic.from_json(d) for d in (raw.get("diagnostics") or [])],
        )

    def capabilities(self) -> Capabilities:
        """Capability document; ``fingerprint`` changes whenever the operations change."""
        with self._lock:
            raw = self._call("capabilities")
        return Capabilities(
            spec=raw.get("spec", ""),
            version=raw.get("version", ""),
            fingerprint=raw.get("fingerprint", ""),
            language=raw.get("language") or {},
            operations=[Operation.from_json(o) for o in (raw.get("operations") or [])],
        )

    def operations(self) -> List[Operation]:
        """Registered operations, sorted by name."""
        with self._lock:
            raw = self._call("operations")
        return [Operation.from_json(o) for o in (raw or [])]

    def describe(self, name: str) -> Operation:
        """One operation's contract. Raises :class:`EngineError` for an unknown name."""
        with self._lock:
            return Operation.from_json(self._call("describe", {"name": name}))

    def docs(self) -> str:
        """Language and operation reference, as Markdown."""
        with self._lock:
            return str(self._call("docs"))

    def skill(self) -> str:
        """A generated SKILL.md for agents using this CLI's Code Mode."""
        with self._lock:
            return str(self._call("skill"))

    def engine_version(self) -> str:
        """Library and spec version reported by the embedded engine."""
        with self._lock:
            return str(self._call("version"))


__all__ = ["CodeMode", "valid_name"]
