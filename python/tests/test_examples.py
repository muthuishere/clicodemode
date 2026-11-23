"""Each example CLI, driven through its real CLI runner and as a real process."""

from __future__ import annotations

import json
import subprocess
import sys
from pathlib import Path

import click.testing
import pytest
import typer.testing

import argparse_cli
import click_cli
import typer_cli

EXAMPLES = Path(__file__).resolve().parents[1] / "examples"
PROGRAM = 'await call("{op}", {args})'


def last_json(text: str):
    return json.loads(text.strip().splitlines()[-1])


# ---------------------------------------------------------------- click


def test_click_example_normal_command() -> None:
    r = click.testing.CliRunner().invoke(click_cli.main, ["inventory", "stock", "--sku", "apple"])
    assert r.exit_code == 0 and json.loads(r.output) == {"sku": "apple", "qty": click_cli.STOCK["apple"]}


def test_click_example_code_run_calls_existing_handlers() -> None:
    click_cli.STOCK.update(apple=3)
    src = 'const a = await call("inventory.add", {sku: "apple", qty: 5}); const s = await call("inventory.stock", {sku: "apple"}); [a, s]'
    r = click.testing.CliRunner().invoke(click_cli.main, ["code", "run", "-e", src])
    assert r.exit_code == 0, r.output
    out = last_json(r.output)
    assert out["ok"] and out["value"] == [{"sku": "apple", "qty": 8}, {"sku": "apple", "qty": 8}]
    assert click_cli.STOCK["apple"] == 8


def test_click_example_handler_validation_reaches_program() -> None:
    src = 'let m; try { await call("inventory.add", {sku: "x", qty: -1}) } catch (e) { m = e.message } m'
    r = click.testing.CliRunner().invoke(click_cli.main, ["code", "run", "-e", src])
    assert "qty must be positive" in last_json(r.output)["value"]


def test_click_example_failing_program_exits_1() -> None:
    r = click.testing.CliRunner().invoke(click_cli.main, ["code", "run", "-e", "throw new Error('nope')"])
    assert r.exit_code == 1 and not last_json(r.output)["ok"]


def test_click_example_does_not_expose_code_group() -> None:
    cm = click_cli.make_codemode()
    assert [o.name for o in cm.operations()] == ["inventory.add", "inventory.stock"]
    cm.close()


def test_click_example_docs() -> None:
    r = click.testing.CliRunner().invoke(click_cli.main, ["code", "docs"])
    assert r.exit_code == 0 and "inventory.add" in r.output


def test_click_example_input_and_file(tmp_path: Path) -> None:
    f = tmp_path / "p.js"
    f.write_text('await call("inventory.stock", {sku: input.sku})')
    r = click.testing.CliRunner().invoke(click_cli.main, ["code", "run", str(f), "--input", '{"sku":"pear"}'])
    assert last_json(r.output)["value"] == {"sku": "pear", "qty": 0}


# ---------------------------------------------------------------- typer


def test_typer_example_normal_command() -> None:
    typer_cli.TASKS.clear()
    r = typer.testing.CliRunner().invoke(typer_cli.app, ["tasks", "add", "first", "--priority", "2"])
    assert r.exit_code == 0 and json.loads(r.output)["title"] == "first"


def test_typer_example_code_run_calls_existing_handlers() -> None:
    typer_cli.TASKS.clear()
    src = (
        'await call("tasks.add", {title: "a", priority: 1}); await call("tasks.add", {title: "b", priority: 3});'
        ' await call("tasks.list", {min_priority: 2})'
    )
    r = typer.testing.CliRunner().invoke(typer_cli.app, ["code", "run", "-e", src])
    assert r.exit_code == 0, r.output
    assert last_json(r.output)["value"] == [{"id": 2, "title": "b", "priority": 3, "done": False}]
    assert len(typer_cli.TASKS) == 2


def test_typer_example_failing_program_exits_1() -> None:
    r = typer.testing.CliRunner().invoke(typer_cli.app, ["code", "run", "-e", 'await call("tasks.add", {nope: 1})'])
    assert r.exit_code == 1 and "invalid arguments" in last_json(r.output)["error"]


# ---------------------------------------------------------------- argparse


def test_argparse_example_normal_command(capsys: pytest.CaptureFixture) -> None:
    argparse_cli.NOTES.clear()
    assert argparse_cli.main(["notes", "add", "buy milk", "--tag", "home"]) == 0
    assert json.loads(capsys.readouterr().out) == {"count": 1}


def test_argparse_example_code_run_calls_existing_handlers(capsys: pytest.CaptureFixture) -> None:
    argparse_cli.NOTES.clear()
    src = 'await call("notes.add", {text: "milk and eggs", tag: ["a"]}); await call("notes.search", {text: "milk"})'
    assert argparse_cli.main(["code", "run", "-e", src]) == 0
    out = last_json(capsys.readouterr().out)
    assert out["ok"] and out["value"] == [{"text": "milk and eggs", "tags": ["a"]}]


def test_argparse_example_failing_program_exits_1(capsys: pytest.CaptureFixture) -> None:
    with pytest.raises(SystemExit) as e:
        argparse_cli.main(["code", "run", "-e", 'await call("notes.search", {})'])
    assert e.value.code == 1


# ---------------------------------------------------------------- real processes


@pytest.mark.parametrize(
    "script,args,needle",
    [
        ("click_cli.py", ["code", "run", "-e", 'await call("inventory.stock", {sku: "apple"})'], '"qty": 3'),
        ("typer_cli.py", ["code", "run", "-e", 'await call("tasks.add", {title: "t"})'], '"title": "t"'),
        ("argparse_cli.py", ["code", "run", "-e", 'await call("notes.add", {text: "n"})'], '"count": 1'),
    ],
)
def test_examples_as_real_processes(script: str, args: list, needle: str) -> None:
    p = subprocess.run([sys.executable, str(EXAMPLES / script), *args], capture_output=True, text=True, timeout=60)
    assert p.returncode == 0, p.stderr
    assert needle in p.stdout
