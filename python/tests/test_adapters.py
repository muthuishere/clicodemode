"""register_function / register_click / register_typer / register_argparse."""

from __future__ import annotations

import argparse
import enum
import json
from typing import List, Literal, Optional

import click
import pytest
import typer

from clicodemode import CodeMode, UsageError
from clicodemode.adapters import schema_for_type


class Color(enum.Enum):
    RED = "red"
    BLUE = "blue"


def test_schema_for_types() -> None:
    assert schema_for_type(int) == {"type": "integer"}
    assert schema_for_type(bool) == {"type": "boolean"}
    assert schema_for_type(List[str]) == {"type": "array", "items": {"type": "string"}}
    assert schema_for_type(Literal["a", "b"]) == {"enum": ["a", "b"]}
    assert schema_for_type(Color) == {"enum": ["red", "blue"]}
    assert schema_for_type(Optional[int]) == {"anyOf": [{"type": "integer"}, {"type": "null"}]}


def test_register_function_schema_and_call(cm: CodeMode) -> None:
    def greet(name: str, times: int = 2, loud: Optional[bool] = None) -> str:
        """Greet someone.

        Long text.
        """
        return (name + "!" if loud else name) * times

    cm.register_function("util.greet", greet, side_effects="none")
    op = cm.describe("util.greet")
    assert op.description == "Greet someone."
    assert op.input_schema["required"] == ["name"]
    assert op.input_schema["properties"]["times"] == {"type": "integer", "default": 2}
    assert cm.run('await call("util.greet", {name: "ab", times: 3})').value == "ababab"
    assert cm.run('await call("util.greet", {name: "x", loud: true})').value == "x!x!"


def test_register_function_unknown_and_missing_args_are_catchable(cm: CodeMode) -> None:
    cm.register_function("util.f", lambda a: a)
    r = cm.run('let m; try { await call("util.f", {b: 1}) } catch (e) { m = e.message } m')
    # the Go engine validates args against the derived schema before the handler is reached
    assert "not an allowed property" in r.value and "invalid arguments" in r.value
    r = cm.run('let m; try { await call("util.f", {}) } catch (e) { m = e.message } m')
    assert "a is required" in r.value


def test_schema_validation_happens_in_engine_before_handler(cm: CodeMode) -> None:
    seen = []

    def f(n: int) -> int:
        seen.append(n)
        return n

    cm.register_function("util.int", f)
    r = cm.run('await call("util.int", {n: "x"})')
    assert not r.ok and "must be integer" in r.error and seen == []


def test_register_function_rejects_bad_meta_and_positional_only(cm: CodeMode) -> None:
    with pytest.raises(UsageError):
        cm.register_function("a.b", lambda a: a, bogus=1)
    with pytest.raises(UsageError):
        cm.register_function("a.c", lambda a, /: a)


# ------------------------------------------------------------------ click


@click.group()
def app() -> None:
    """root"""


@app.command()
@click.argument("sku")
@click.option("--qty", type=int, default=1, help="how many")
@click.option("--tag", multiple=True)
@click.option("--color", type=click.Choice(["red", "blue"]), default="red")
@click.option("--loud/--quiet", default=False)
def put(sku: str, qty: int, tag: tuple, color: str, loud: bool) -> None:
    """Put an item."""
    click.echo(json.dumps({"sku": sku, "qty": qty, "tag": list(tag), "color": color, "loud": loud}))


@app.command()
@click.option("--n", type=int, required=True)
def fail(n: int) -> None:
    if n == 1:
        raise click.UsageError("one is bad")
    if n == 2:
        raise click.exceptions.Exit(3)
    click.echo("plain text out")


@app.group()
def sub() -> None:
    """nested"""


@sub.command()
def ping() -> str:
    """Return a value instead of printing."""
    return "pong"


@app.command(hidden=True)
def secret() -> None:
    click.echo("x")


def test_click_names_schema_and_exclusions(cm: CodeMode) -> None:
    names = cm.register_click(app, "inv", exclude=["fail"])
    assert names == ["inv.put", "inv.sub.ping"]  # hidden and excluded commands are not exposed
    op = cm.describe("inv.put")
    p = op.input_schema["properties"]
    assert op.input_schema["required"] == ["sku"] and op.description == "Put an item."
    assert p["qty"] == {"type": "integer", "description": "how many", "default": 1}
    assert p["tag"]["type"] == "array" and p["color"]["enum"] == ["red", "blue"] and p["loud"]["type"] == "boolean"


def test_click_call_runs_real_command_and_parses_output(cm: CodeMode) -> None:
    cm.register_click(app, "inv")
    r = cm.run('await call("inv.put", {sku: "-weird", qty: 4, tag: ["a", "b"], color: "blue", loud: true})')
    assert r.value == {"sku": "-weird", "qty": 4, "tag": ["a", "b"], "color": "blue", "loud": True}
    assert cm.run('await call("inv.put", {sku: "z", loud: false})').value["loud"] is False
    assert cm.run('await call("inv.sub.ping")').value == "pong"
    assert cm.run('await call("inv.fail", {n: 0})').value == "plain text out"


def test_click_errors_become_rejections_with_message(cm: CodeMode) -> None:
    cm.register_click(app, "inv")
    r = cm.run('await call("inv.fail", {n: 1})')
    assert not r.ok and "one is bad" in r.error
    assert "status 3" in cm.run('await call("inv.fail", {n: 2})').error
    assert "not an allowed property" in cm.run('await call("inv.put", {sku: "a", nope: 1})').error
    r = cm.run('await call("inv.put", {sku: "a", color: "green"})')
    assert not r.ok and "invalid arguments" in r.error


def test_click_single_command_needs_prefix_or_uses_its_name(cm: CodeMode) -> None:
    assert cm.register_click(put) == ["put"]


# ------------------------------------------------------------------ typer


def make_typer() -> typer.Typer:
    t = typer.Typer()

    @t.command("mul")
    def mul(a: int, b: int = 2, label: Optional[str] = None, color: Color = Color.RED) -> None:
        """Multiply."""
        typer.echo(json.dumps({"p": a * b, "label": label, "color": color.value}))

    @t.command("flags")
    def flags(force: bool = False, names: Optional[List[str]] = None) -> None:
        typer.echo(json.dumps({"force": force, "names": names}))

    return t


def test_typer_schema_and_calls(cm: CodeMode) -> None:
    names = cm.register_typer(make_typer(), "t")
    assert names == ["t.flags", "t.mul"] or sorted(names) == ["t.flags", "t.mul"]
    s = cm.describe("t.mul").input_schema
    assert s["properties"]["a"]["type"] == "integer" and s["required"] == ["a"]
    assert s["properties"]["color"] == {"enum": ["red", "blue"], "default": "red"} or "enum" in s["properties"]["color"]
    assert cm.run('await call("t.mul", {a: 6, b: 7, label: "x", color: "blue"})').value == {"p": 42, "label": "x", "color": "blue"}
    assert cm.run('await call("t.flags", {force: true, names: ["a", "b"]})').value == {"force": True, "names": ["a", "b"]}
    assert cm.run('await call("t.flags", {})').value == {"force": False, "names": None}


def test_typer_usage_error_is_a_rejection(cm: CodeMode) -> None:
    cm.register_typer(make_typer(), "t")
    r = cm.run('await call("t.mul", {b: 3})')
    assert not r.ok and r.error


# ------------------------------------------------------------------ argparse


def build_parser() -> argparse.ArgumentParser:
    p = argparse.ArgumentParser(prog="tool")
    sub = p.add_subparsers(dest="cmd", required=True)
    a = sub.add_parser("add", description="Add numbers.")
    a.add_argument("nums", type=int, nargs="+")
    a.add_argument("--scale", type=float, default=1.0)
    a.add_argument("--neg", action="store_true")
    a.add_argument("--tag", action="append", default=[])
    a.add_argument("--mode", choices=["x", "y"], default="x")
    a.set_defaults(func=lambda ns: print(json.dumps({"sum": sum(ns.nums) * ns.scale * (-1 if ns.neg else 1), "tag": ns.tag, "mode": ns.mode})))
    g = sub.add_parser("grp").add_subparsers(dest="g", required=True)
    h = g.add_parser("hello")
    h.add_argument("who", nargs="?", default="world")
    h.set_defaults(func=lambda ns: "hello " + ns.who)
    sub.add_parser("nofunc")  # no handler -> not exposed
    return p


def test_argparse_schema(cm: CodeMode) -> None:
    names = cm.register_argparse(build_parser(), "tool")
    assert names == ["tool.add", "tool.grp.hello"]
    s = cm.describe("tool.add").input_schema
    assert s["required"] == ["nums"] and s["properties"]["nums"] == {"type": "array", "items": {"type": "integer"}}
    assert s["properties"]["neg"]["type"] == "boolean" and s["properties"]["mode"]["items" if False else "enum"] == ["x", "y"]
    assert cm.describe("tool.add").description == "Add numbers."


def test_argparse_calls(cm: CodeMode) -> None:
    cm.register_argparse(build_parser(), "tool")
    r = cm.run('await call("tool.add", {nums: [1, 2, 3], scale: 2, tag: ["a", "b"], neg: true, mode: "y"})')
    assert r.value == {"sum": -12.0, "tag": ["a", "b"], "mode": "y"}
    assert cm.run('await call("tool.grp.hello", {who: "py"})').value == "hello py"
    assert cm.run('await call("tool.grp.hello")').value == "hello world"


def test_argparse_parse_error_is_a_rejection_with_message(cm: CodeMode) -> None:
    cm.register_argparse(build_parser(), "tool")
    r = cm.run('await call("tool.add", {nums: ["x"]})')
    assert not r.ok and "must be integer" in r.error  # caught by the schema, before argparse
    assert "not an allowed property" in cm.run('await call("tool.add", {nums: [1], bad: 1})').error
