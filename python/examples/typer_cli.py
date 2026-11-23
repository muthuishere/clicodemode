"""A Typer CLI whose existing commands are also available to agent-written programs.

    python examples/typer_cli.py tasks add "write docs" --priority 2
    python examples/typer_cli.py code run -e 'await ops.tasks.list({})'
"""

from __future__ import annotations

import json
import sys
from typing import Any, Dict, List, Optional

import typer

from clicodemode import CodeMode

TASKS: List[Dict[str, Any]] = []

tasks = typer.Typer(help="Task commands (the existing CLI).")


@tasks.command("add")
def add_task(title: str, priority: int = 1, done: bool = False) -> None:
    """Add a task."""
    TASKS.append({"id": len(TASKS) + 1, "title": title, "priority": priority, "done": done})
    typer.echo(json.dumps(TASKS[-1]))


@tasks.command("list")
def list_tasks(min_priority: int = typer.Option(0, help="hide lower priorities")) -> None:
    """List tasks."""
    typer.echo(json.dumps([t for t in TASKS if t["priority"] >= min_priority]))


def make_codemode() -> CodeMode:
    cm = CodeMode("1.0.0")
    cm.register_typer(app, exclude=["code"])
    return cm


code = typer.Typer(help="Run agent-written programs against this CLI's commands.")


@code.command("run")
def code_run(
    src: Optional[str] = typer.Option(None, "-e", "--eval", help="program text"),
    file: Optional[str] = typer.Argument(None),
) -> None:
    """Run a program; prints the result as JSON, exits 1 if it failed."""
    program = src if src is not None else open(str(file), encoding="utf-8").read()
    r = make_codemode().run(program)
    typer.echo(json.dumps({"ok": r.ok, "value": r.value, "error": r.error}))
    sys.exit(0 if r.ok else 1)


app = typer.Typer()
app.add_typer(tasks, name="tasks")
app.add_typer(code, name="code")

if __name__ == "__main__":
    app()
