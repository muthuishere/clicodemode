"""Code Mode for Python CLIs.

Register the handlers your CLI already has as operations, then run small
JavaScript-like programs that call them. The language engine is the
``clicodemode`` Go engine compiled to WebAssembly and executed by wasmtime;
Python never interprets the language.

>>> from clicodemode import CodeMode
>>> cm = CodeMode("1.0.0")
>>> @cm.operation("health.check", side_effects="none")
... def health(args):
...     return {"ok": True}
>>> cm.run("export default async () => await ops.health.check({})").value
{'ok': True}
"""

from .core import CodeMode, valid_name
from .errors import CodeModeError, EngineError, GuestCrashed, UsageError
from .types import (
    Capabilities,
    CompileResult,
    Diagnostic,
    Limits,
    Operation,
    Policy,
    Result,
)

__version__ = "0.6.1"

__all__ = [
    "CodeMode",
    "Limits",
    "Policy",
    "Operation",
    "Result",
    "Diagnostic",
    "CompileResult",
    "Capabilities",
    "CodeModeError",
    "EngineError",
    "GuestCrashed",
    "UsageError",
    "valid_name",
    "__version__",
]
