"""Turn existing Python functions and CLI handlers into Code Mode operations.

* :func:`register_function` - any function; the JSON schema comes from its signature.
* :func:`register_click` / :func:`register_typer` - each Click/Typer command becomes
  an operation named ``<prefix>.<group>.<command>``; the handler runs the command's
  own callback in-process (no subprocess), arguments are checked by Click itself.
* :func:`register_argparse` - each sub-parser that carries a callable default
  (``parser.set_defaults(func=handler)``, the usual argparse idiom) becomes an operation.

What a wrapped command returns to the program: its return value if it is not
``None``; otherwise what it printed to stdout (parsed as JSON when it is JSON,
else the text). Printed text is captured with ``contextlib.redirect_stdout``,
which is process-wide, so wrapped commands are serialised by a lock.

Not run: Click group callbacks / group-level options (only the leaf command is
invoked; pass ``obj=`` to supply ``ctx.obj``), argparse top-level options.
"""

from __future__ import annotations

import argparse
import contextlib
import enum
import inspect
import io
import json
import threading
import types
import typing
from typing import Any, Callable, Dict, List, Optional, Sequence, Tuple

from .errors import UsageError
from .types import Operation

_stdio_lock = threading.RLock()


# --------------------------------------------------------------------- schemas


def schema_for_type(tp: Any) -> Dict[str, Any]:
    """Best-effort JSON schema for a Python type annotation (``{}`` = anything)."""
    if tp is None or tp is type(None):
        return {"type": "null"}
    if tp is Any or tp is inspect.Parameter.empty:
        return {}
    origin = typing.get_origin(tp)
    args = typing.get_args(tp)
    if origin is typing.Union or origin is getattr(types, "UnionType", object()):
        non_null = [a for a in args if a is not type(None)]
        subs = [schema_for_type(a) for a in non_null]
        if len(non_null) < len(args):
            subs.append({"type": "null"})
        return subs[0] if len(subs) == 1 else {"anyOf": subs}
    if origin is typing.Literal:
        return {"enum": list(args)}
    if origin in (list, set, frozenset, tuple) or tp in (list, set, frozenset, tuple):
        s: Dict[str, Any] = {"type": "array"}
        if args and args[-1] is not Ellipsis and origin is not tuple:
            s["items"] = schema_for_type(args[0])
        return s
    if origin is dict or tp is dict:
        return {"type": "object"}
    if isinstance(tp, type):
        if issubclass(tp, enum.Enum):
            return {"enum": [m.value for m in tp]}
        if issubclass(tp, bool):
            return {"type": "boolean"}
        if issubclass(tp, int):
            return {"type": "integer"}
        if issubclass(tp, float):
            return {"type": "number"}
        if issubclass(tp, str):
            return {"type": "string"}
    return {}


def _first_para(text: Optional[str]) -> str:
    return (inspect.cleandoc(text or "").split("\n\n")[0]).replace("\n", " ").strip()


def register_function(cm: Any, name: str, fn: Callable[..., Any], **meta: Any) -> None:
    """Register ``fn``; the program's argument object is passed as keyword arguments.

    Extra keys raise ``TypeError`` inside the call (catchable in the program);
    ``meta`` takes ``description``, ``side_effects``, ``idempotent``,
    ``output_schema``, ``authorization``, ``examples``, ``input_schema``.
    """
    sig = inspect.signature(fn)
    try:
        hints = typing.get_type_hints(fn)
    except Exception:  # unresolved forward refs, py3.9 "X | None" strings...
        hints = {}
    props: Dict[str, Any] = {}
    required: List[str] = []
    var_kw = False
    for p in sig.parameters.values():
        if p.kind is p.VAR_KEYWORD:
            var_kw = True
            continue
        if p.kind in (p.VAR_POSITIONAL, p.POSITIONAL_ONLY):
            raise UsageError(f"{fn!r}: parameter {p.name!r} cannot be passed by keyword")
        s = schema_for_type(hints.get(p.name, p.annotation))
        if p.default is not p.empty and _jsonable(p.default):
            s = dict(s, default=p.default)
        props[p.name] = s
        if p.default is p.empty:
            required.append(p.name)
    schema: Dict[str, Any] = {"type": "object", "properties": props, "additionalProperties": var_kw}
    if required:
        schema["required"] = required
    schema = meta.pop("input_schema", None) or schema
    desc = meta.pop("description", None)
    op = Operation(
        name,
        desc if desc is not None else _first_para(inspect.getdoc(fn)),
        schema,
        meta.pop("output_schema", None),
        meta.pop("side_effects", "local"),
        meta.pop("idempotent", False),
        meta.pop("authorization", ""),
        list(meta.pop("examples", None) or []),
    )
    if meta:
        raise UsageError(f"unknown option(s) for register_function: {sorted(meta)}")
    cm.register(op, lambda args: fn(**args))


def _jsonable(v: Any) -> bool:
    try:
        json.dumps(v, allow_nan=False)
        return True
    except (TypeError, ValueError):
        return False


# --------------------------------------------------------------------- shared


def _opname(prefix: str, path: List[str]) -> str:
    parts = [p for p in [prefix] + path if p]
    return ".".join(p.lower().replace(" ", "-") for p in parts)


def _excluded(path: List[str], exclude: Sequence[str]) -> bool:
    dotted = ".".join(path)
    return any(dotted == e or dotted.startswith(e + ".") for e in exclude)


def _shape(ret: Any, text: str) -> Any:
    """Return value if any, else printed output (JSON-decoded when it is JSON)."""
    if ret is not None:
        return ret
    text = text.strip()
    if not text:
        return None
    try:
        return json.loads(text)
    except ValueError:
        return text


@contextlib.contextmanager
def _captured() -> Any:
    out, err = io.StringIO(), io.StringIO()
    with _stdio_lock, contextlib.redirect_stdout(out), contextlib.redirect_stderr(err):
        yield out, err


def _check_keys(args: Dict[str, Any], known: List[str]) -> None:
    extra = sorted(set(args) - set(known))
    if extra:
        raise ValueError(f"unknown argument(s): {', '.join(extra)} (expected: {', '.join(known) or 'none'})")


def _flag(opts: Sequence[str]) -> str:
    longs = [o for o in opts if o.startswith("--")]
    return max(longs, key=len) if longs else opts[0]


# --------------------------------------------------------------------- click


def _click_schema(param: Any) -> Dict[str, Any]:
    # Duck-typed on purpose: Typer >= 0.29 ships its own copy of Click, so
    # `isinstance(x, click.Group)` would miss Typer commands.
    desc = getattr(param, "type_descriptor", None)
    if desc is not None:  # Typer >= 0.29: the Python annotation is the source of truth
        ts = schema_for_type(desc.annotation)
        if getattr(param, "help", None):
            ts["description"] = param.help
        td = param.default
        if td is not None and not callable(td) and not isinstance(td, tuple) and _jsonable(td):
            ts["default"] = td
        return ts
    t = param.type
    tname = getattr(t, "name", "")
    is_flag = bool(getattr(param, "is_flag", False)) and not getattr(param, "count", False)
    if is_flag or tname == "boolean":
        s: Dict[str, Any] = {"type": "boolean"}
    elif getattr(t, "choices", None) is not None:
        s = {"type": "string", "enum": [str(c) for c in t.choices]}
    elif tname.startswith("integer") or getattr(param, "count", False):
        s = {"type": "integer"}
    elif tname.startswith("float"):
        s = {"type": "number"}
    else:
        s = {"type": "string"}
    if getattr(param, "multiple", False) or (param.nargs != 1 and not is_flag):
        s = {"type": "array", "items": s}
    h = getattr(param, "help", None)
    if h:
        s["description"] = h
    d = param.default
    if d is not None and not callable(d) and not isinstance(d, tuple) and _jsonable(d):
        s["default"] = d
    return s


def _click_argv(cmd: Any, args: Dict[str, Any]) -> List[str]:
    params = [p for p in cmd.params if p.expose_value]
    _check_keys(args, [p.name for p in params])
    flags: List[str] = []
    pos: List[str] = []
    for p in params:
        if p.name not in args or args[p.name] is None:
            continue
        v = args[p.name]
        if p.param_type_name == "option":
            f = _flag(p.opts)
            if getattr(p, "is_flag", False) and not getattr(p, "count", False):
                flags.append(f if v else (p.secondary_opts[0] if p.secondary_opts else ""))
            elif getattr(p, "count", False):
                flags += [f] * int(v)
            else:
                for item in v if p.multiple else [v]:
                    flags += [f] + [str(x) for x in (item if isinstance(item, (list, tuple)) else [item])]
        else:
            pos += [str(x) for x in (v if isinstance(v, (list, tuple)) else [v])]
    flags = [f for f in flags if f]
    return flags + (["--"] + pos if pos else [])


def _click_leaves(cmd: Any, path: List[str]) -> List[Tuple[List[str], Any]]:
    subs = getattr(cmd, "commands", None)
    if isinstance(subs, dict):  # a group
        out: List[Tuple[List[str], Any]] = []
        for n, sub in subs.items():
            if not getattr(sub, "hidden", False):
                out += _click_leaves(sub, path + [n])
        return out
    return [(path, cmd)]


def _run_click(cmd: Any, args: Dict[str, Any], obj: Any) -> Any:
    argv = _click_argv(cmd, args)
    with _captured() as (out, err):
        ret = None
        try:
            ctx = cmd.make_context(cmd.name or "command", argv, obj=obj)
            with ctx:
                ret = cmd.invoke(ctx)
        except SystemExit as e:
            code: Any = e.code
            if code not in (0, None):
                raise RuntimeError(f"exited with status {code}: {err.getvalue().strip() or out.getvalue().strip()}")
        except Exception as e:  # Click/Typer exceptions are matched by shape (see above)
            kind = type(e).__name__
            if kind == "Exit":
                if getattr(e, "exit_code", 0):
                    raise RuntimeError(f"exited with status {getattr(e, 'exit_code')}: {err.getvalue().strip() or out.getvalue().strip()}")
            elif kind == "Abort":
                raise RuntimeError("aborted")
            elif hasattr(e, "format_message"):  # ClickException: usage errors, bad parameters
                raise RuntimeError(e.format_message())
            else:
                raise
        return _shape(ret, out.getvalue())


def register_click(
    cm: Any,
    command: Any,
    prefix: str = "",
    *,
    side_effects: str = "local",
    idempotent: bool = False,
    obj: Any = None,
    exclude: Sequence[str] = (),
) -> List[str]:
    """Register each leaf command of a Click group (or one command) as an operation.

    ``exclude`` lists dotted command paths to leave out (for example the ``code``
    group that hosts Code Mode itself).
    """
    leaves = _click_leaves(command, [])
    if leaves and leaves[0][0] == [] and not prefix:
        prefix = command.name or ""
    names: List[str] = []
    for path, cmd in leaves:
        if _excluded(path, exclude):
            continue
        schema: Dict[str, Any] = {"type": "object", "properties": {}, "additionalProperties": False}
        req: List[str] = []
        for p in cmd.params:
            if not p.expose_value:
                continue
            schema["properties"][p.name] = _click_schema(p)
            if p.required:
                req.append(p.name)
        if req:
            schema["required"] = req
        name = _opname(prefix, path)
        cm.register(
            Operation(name, _first_para(cmd.help) or (cmd.short_help or ""), schema, None, side_effects, idempotent),
            lambda args, cmd=cmd: _run_click(cmd, args, obj),
        )
        names.append(name)
    return names


def register_typer(cm: Any, app: Any, prefix: str = "", **kw: Any) -> List[str]:
    """Register a Typer app. Typer builds a Click command; this wraps that one."""
    import typer.main

    command = typer.main.get_command(app)
    if not prefix and not hasattr(command, "commands"):
        prefix = getattr(getattr(app, "info", None), "name", None) or command.name or "app"
    return register_click(cm, command, prefix, **kw)


# --------------------------------------------------------------------- argparse


def _ap_actions(parser: argparse.ArgumentParser) -> List[argparse.Action]:
    skip = (argparse._HelpAction, argparse._SubParsersAction, argparse._VersionAction)
    return [a for a in parser._actions if not isinstance(a, skip)]


def _ap_schema(a: argparse.Action) -> Dict[str, Any]:
    if isinstance(a, (argparse._StoreTrueAction, argparse._StoreFalseAction, argparse._StoreConstAction)):
        s: Dict[str, Any] = {"type": "boolean"}
    elif isinstance(a, argparse._CountAction):
        s = {"type": "integer"}
    else:
        kinds: Dict[Any, str] = {int: "integer", float: "number", bool: "boolean"}
        s = {"type": kinds.get(a.type, "string")}
        if a.choices is not None:
            s["enum"] = list(a.choices)
        if isinstance(a, argparse._AppendAction) or a.nargs in ("*", "+") or (isinstance(a.nargs, int) and a.nargs > 1):
            s = {"type": "array", "items": s}
    if a.help and a.help != argparse.SUPPRESS:
        s["description"] = a.help
    if a.default is not None and a.default is not argparse.SUPPRESS and _jsonable(a.default):
        s["default"] = a.default
    return s


def _ap_argv(actions: List[argparse.Action], args: Dict[str, Any]) -> List[str]:
    _check_keys(args, [a.dest for a in actions])
    flags: List[str] = []
    pos: List[str] = []
    for a in actions:
        if a.dest not in args or args[a.dest] is None:
            continue
        v = args[a.dest]
        if a.option_strings:
            f = _flag(a.option_strings)
            if isinstance(a, argparse._StoreFalseAction):
                if v is False:
                    flags.append(f)
            elif isinstance(a, (argparse._StoreTrueAction, argparse._StoreConstAction)):
                if v:
                    flags.append(f)
            elif isinstance(a, argparse._CountAction):
                flags += [f] * int(v)
            elif isinstance(a, argparse._AppendAction):
                for item in v:
                    flags += [f] + [str(x) for x in (item if isinstance(item, (list, tuple)) else [item])]
            else:
                flags += [f] + [str(x) for x in (v if isinstance(v, (list, tuple)) else [v])]
        else:
            pos += [str(x) for x in (v if isinstance(v, (list, tuple)) else [v])]
    return flags + (["--"] + pos if pos else [])


def _ap_leaves(parser: argparse.ArgumentParser, path: List[str]) -> List[Tuple[List[str], argparse.ArgumentParser]]:
    subs = [a for a in parser._actions if isinstance(a, argparse._SubParsersAction)]
    if not subs:
        return [(path, parser)]
    out: List[Tuple[List[str], argparse.ArgumentParser]] = []
    seen = set()
    for act in subs:
        for n, sp in act.choices.items():
            if id(sp) in seen:  # aliases share one parser
                continue
            seen.add(id(sp))
            out += _ap_leaves(sp, path + [n])
    return out


def register_argparse(
    cm: Any,
    parser: argparse.ArgumentParser,
    prefix: str = "",
    *,
    func_attr: str = "func",
    side_effects: str = "local",
    idempotent: bool = False,
    exclude: Sequence[str] = (),
) -> List[str]:
    """Register each sub-parser that has ``set_defaults(<func_attr>=callable)``.

    The callable receives the parsed ``argparse.Namespace`` (your existing handler,
    unchanged). Sub-parsers without a callable default are skipped; ``exclude`` lists
    dotted sub-command paths to leave out.
    """
    names: List[str] = []
    for path, sp in _ap_leaves(parser, []):
        if _excluded(path, exclude) or not callable(sp.get_default(func_attr)):
            continue
        actions = _ap_actions(sp)
        props: Dict[str, Any] = {}
        req: List[str] = []
        for a in actions:
            props[a.dest] = _ap_schema(a)
            if a.option_strings:
                if a.required:
                    req.append(a.dest)
            elif a.nargs not in ("?", "*"):
                req.append(a.dest)
        schema: Dict[str, Any] = {"type": "object", "properties": props, "additionalProperties": False}
        if req:
            schema["required"] = req

        def handler(args: Dict[str, Any], path: List[str] = path, actions: List[argparse.Action] = actions) -> Any:
            argv = path + _ap_argv(actions, args)
            with _captured() as (out, err):
                try:
                    ns = parser.parse_args(argv)
                    ret = getattr(ns, func_attr)(ns)
                except SystemExit as e:
                    if e.code not in (0, None):
                        msg = err.getvalue().strip().splitlines()
                        raise RuntimeError(msg[-1] if msg else f"exited with status {e.code}")
                    ret = None
            return _shape(ret, out.getvalue())

        name = _opname(prefix, path)
        cm.register(
            Operation(name, sp.description or (sp.format_usage().strip()), schema, None, side_effects, idempotent),
            handler,
        )
        names.append(name)
    return names
