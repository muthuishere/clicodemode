"""An argparse CLI whose existing handlers are also available to agent-written programs.

    python examples/argparse_cli.py notes add "buy milk" --tag home
    python examples/argparse_cli.py code run -e 'await ops.notes.search({text: "milk"})'

The handlers are ordinary argparse handlers (``parser.set_defaults(func=...)``) that
print their output; Code Mode runs the same handlers in-process.
"""

from __future__ import annotations

import argparse
import json
import sys
from typing import Any, Dict, List, Optional

from clicodemode import CodeMode

NOTES: List[Dict[str, Any]] = []


def cmd_add(ns: argparse.Namespace) -> None:
    NOTES.append({"text": ns.text, "tags": ns.tag})
    print(json.dumps({"count": len(NOTES)}))


def cmd_search(ns: argparse.Namespace) -> None:
    print(json.dumps([n for n in NOTES if ns.text in n["text"]]))


def cmd_code_run(ns: argparse.Namespace) -> None:
    cm = CodeMode("1.0.0")
    cm.register_argparse(build_parser(), exclude=["code"])
    r = cm.run(ns.src)
    print(json.dumps({"ok": r.ok, "value": r.value, "error": r.error}))
    if not r.ok:
        sys.exit(1)


def build_parser() -> argparse.ArgumentParser:
    p = argparse.ArgumentParser(prog="notes-cli")
    sub = p.add_subparsers(dest="cmd", required=True)

    notes = sub.add_parser("notes", help="note commands").add_subparsers(dest="sub", required=True)
    add = notes.add_parser("add", description="Add a note.")
    add.add_argument("text")
    add.add_argument("--tag", action="append", default=[], help="repeatable")
    add.set_defaults(func=cmd_add)
    search = notes.add_parser("search", description="Find notes containing text.")
    search.add_argument("text")
    search.set_defaults(func=cmd_search)

    code = sub.add_parser("code", help="run programs").add_subparsers(dest="sub", required=True)
    run = code.add_parser("run", description="Run a program.")
    run.add_argument("-e", "--eval", dest="src", required=True)
    run.set_defaults(func=cmd_code_run)
    return p


def main(argv: Optional[List[str]] = None) -> int:
    ns = build_parser().parse_args(argv)
    ns.func(ns)
    return 0


if __name__ == "__main__":
    sys.exit(main())
