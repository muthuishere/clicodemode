"""A Click CLI whose existing commands are also available to agent-written programs.

    python examples/click_cli.py inventory stock --sku apple
    python examples/click_cli.py code run -e 'await ops.inventory.add({sku: "apple", qty: 5})'
    python examples/click_cli.py code docs
"""

from __future__ import annotations

import json
import sys
from typing import Any, Dict

import click

from clicodemode import CodeMode, Policy

STOCK: Dict[str, int] = {"apple": 3, "pear": 0}


@click.group()
def inventory() -> None:
    """Inventory commands (these are the existing CLI; nothing here knows about Code Mode)."""


@inventory.command()
@click.option("--sku", required=True, help="item code")
def stock(sku: str) -> None:
    """Show the stock level of one item."""
    click.echo(json.dumps({"sku": sku, "qty": STOCK.get(sku, 0)}))


@inventory.command()
@click.argument("sku")
@click.option("--qty", type=int, default=1, help="how many to add")
def add(sku: str, qty: int) -> Dict[str, Any]:
    """Add stock; returns the new level."""
    if qty <= 0:
        raise click.BadParameter("qty must be positive")
    STOCK[sku] = STOCK.get(sku, 0) + qty
    return {"sku": sku, "qty": STOCK[sku]}


def make_codemode() -> CodeMode:
    # the policy is the gate: here nothing that touches the outside world may run
    cm = CodeMode("1.0.0", policy=Policy(allow=lambda op: op.side_effects != "external"))
    cm.register_click(main, exclude=["code"])  # every command except Code Mode itself
    return cm


@click.group()
def code() -> None:
    """Run agent-written programs against this CLI's commands."""


@code.command("run")
@click.argument("file", required=False, type=click.Path(exists=True))
@click.option("-e", "--eval", "src", help="program text instead of a file")
@click.option("--input", "input_json", default=None, help="JSON exposed to the program as `input`")
def code_run(file: str, src: str, input_json: str) -> None:
    """Run a program; prints the result as JSON, exits 1 if it failed."""
    program = src if src is not None else open(file, encoding="utf-8").read()
    r = make_codemode().run(program, input=json.loads(input_json) if input_json else None)
    click.echo(json.dumps({"ok": r.ok, "value": r.value, "error": r.error}))
    sys.exit(0 if r.ok else 1)


@code.command("docs")
def code_docs() -> None:
    """Print the generated language and operation reference."""
    click.echo(make_codemode().docs())


@click.group()
def main() -> None:
    """Inventory CLI with Code Mode."""


main.add_command(inventory)
main.add_command(code)

if __name__ == "__main__":
    main()
